package storagecatalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRebindPathsIsTransactionalAndIdempotent(t *testing.T) {
	state := &rebindFakeState{
		refs: map[string]rebindFakeRef{
			"ref-a": {entryID: "entry-a", uri: "/old/a"},
			"ref-b": {entryID: "entry-b", uri: "/changed/b"},
		},
		entries: map[string]rebindFakeEntry{"entry-a": {original: "/old/a", view: "old/a"}},
	}
	db := sql.OpenDB(rebindFakeConnector{state: state})
	defer db.Close()
	svc := NewService(db)

	_, err := svc.RebindPaths(context.Background(), RebindPathsInput{PhysicalRefs: []PhysicalRefPathRebind{
		{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"},
		{StoragePhysicalRefID: "ref-b", StorageEntryID: "entry-b", ExpectedURI: "/old/b", NewURI: "/new/b"},
	}})
	if err == nil || !strings.Contains(err.Error(), "changed since review") {
		t.Fatalf("changed evidence error = %v", err)
	}
	if got := state.refURI("ref-a"); got != "/old/a" {
		t.Fatalf("transaction leaked first update after later conflict: %q", got)
	}

	state.setRefURI("ref-b", "/old/b")
	input := RebindPathsInput{
		PhysicalRefs: []PhysicalRefPathRebind{
			{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"},
			{StoragePhysicalRefID: "ref-b", StorageEntryID: "entry-b", ExpectedURI: "/old/b", NewURI: "/new/b"},
		},
		Entries: []EntryPathRebind{{
			StorageEntryID: "entry-a", ExpectedOriginalSourcePath: "/old/a", NewOriginalSourcePath: "/new/a",
			ExpectedCurrentViewPath: "old/a", NewCurrentViewPath: "new/a",
		}},
	}
	result, err := svc.RebindPaths(context.Background(), input)
	if err != nil {
		t.Fatalf("RebindPaths returned error: %v", err)
	}
	if result.PhysicalRefsUpdated != 2 || result.EntriesUpdated != 1 || result.AlreadyApplied != 0 {
		t.Fatalf("result = %#v", result)
	}
	result, err = svc.RebindPaths(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent RebindPaths returned error: %v", err)
	}
	if result.AlreadyApplied != 3 || result.PhysicalRefsUpdated != 0 || result.EntriesUpdated != 0 {
		t.Fatalf("idempotent result = %#v", result)
	}
}

func TestRebindPathBatchesRollsBackEarlierChildWhenLaterEvidenceChanged(t *testing.T) {
	state := &rebindFakeState{
		refs: map[string]rebindFakeRef{
			"ref-a": {entryID: "entry-a", uri: "/old/a"},
			"ref-b": {entryID: "entry-b", uri: "/changed/b"},
		},
		entries: map[string]rebindFakeEntry{},
	}
	db := sql.OpenDB(rebindFakeConnector{state: state})
	defer db.Close()
	svc := NewService(db)
	batches := []RebindPathsInput{
		{PhysicalRefs: []PhysicalRefPathRebind{{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"}}},
		{PhysicalRefs: []PhysicalRefPathRebind{{StoragePhysicalRefID: "ref-b", StorageEntryID: "entry-b", ExpectedURI: "/old/b", NewURI: "/new/b"}}},
	}
	loads := 0
	_, err := svc.RebindPathBatches(context.Background(), len(batches), func(index int) (RebindPathsInput, error) {
		loads++
		return batches[index], nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed since review") {
		t.Fatalf("later child evidence error = %v", err)
	}
	if loads != 4 {
		t.Fatalf("loader calls = %d, want complete preflight plus transactional reload", loads)
	}
	if got := state.refURI("ref-a"); got != "/old/a" {
		t.Fatalf("later child failure committed earlier child mutation: %q", got)
	}
	state.setRefURI("ref-b", "/old/b")
	result, err := svc.RebindPathBatches(context.Background(), len(batches), func(index int) (RebindPathsInput, error) {
		return batches[index], nil
	})
	if err != nil || result.PhysicalRefsUpdated != 2 || result.AlreadyApplied != 0 {
		t.Fatalf("atomic batch apply result=%#v err=%v", result, err)
	}
	result, err = svc.RebindPathBatches(context.Background(), len(batches), func(index int) (RebindPathsInput, error) {
		return batches[index], nil
	})
	if err != nil || result.PhysicalRefsUpdated != 0 || result.AlreadyApplied != 2 {
		t.Fatalf("idempotent atomic batch retry result=%#v err=%v", result, err)
	}
}

func TestWorkspaceArchiveProjectionAtomicallyRebindsAndEmitsOneEvent(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	state := &workspaceProjectionFakeState{
		phase: "archive_payload_moved", digest: "sha256:" + strings.Repeat("a", 64),
		plan: json.RawMessage(`{"operation_id":"workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), plannedAt: now, intentAt: now, movedAt: now, updatedAt: now,
		refs:      map[string]rebindFakeRef{"ref-a": {entryID: "entry-a", uri: "/old/a"}},
		entries:   map[string]rebindFakeEntry{"entry-a": {original: "/old/a", view: "old/a"}},
		failEvent: true,
	}
	db := sql.OpenDB(workspaceProjectionFakeConnector{state: state})
	defer db.Close()
	service := NewService(db)
	input := WorkspaceArchiveProjectionInput{
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: state.digest,
		SourceAbsolutePath: "/old",
		Rebind: RebindPathsInput{
			PhysicalRefs: []PhysicalRefPathRebind{{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"}},
			Entries:      []EntryPathRebind{{StorageEntryID: "entry-a", ExpectedOriginalSourcePath: "/old/a", NewOriginalSourcePath: "/new/a", ExpectedCurrentViewPath: "old/a", NewCurrentViewPath: "new/a"}},
		},
		ManifestSchemaVersion: "storage.workspace_archive_manifest.v1", EvidenceKind: "physical_workspace_move",
		WorkspaceKind: "topic", ObjectID: "topic_object_one", Slug: "topic-one",
		InventoryDigest: "sha256:" + strings.Repeat("b", 64), ArchiveSourceIdentityJSON: json.RawMessage(`{"presence":"present"}`),
		ManifestJSON: json.RawMessage(`{"manifest":"exact"}`), AuthenticationKeyID: "workspace.archive.test",
		AuthenticationTag: "hmac-sha256:" + strings.Repeat("c", 64), ArchivedAt: now,
		EventID: "workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAV", EventSchemaVersion: "storage.workspace_lifecycle_event.v1",
		EventKind: "workspace.lifecycle_changed", Transition: "active_to_archived", FromState: "active", ToState: "archived",
		SourceRoot: "box", SourceRelativePath: "old", DestinationRoot: "storage", DestinationRelativePath: "archive/topics/topic-one/content",
		ActorID: "actor_archive_test", Reason: "archive topic", EventJSON: json.RawMessage(`{"event":"exact"}`), CommittedAt: now,
	}
	if _, err := service.CommitWorkspaceArchiveProjection(context.Background(), input); err == nil || !strings.Contains(err.Error(), "injected event failure") {
		t.Fatalf("late event failure = %v", err)
	}
	if state.refs["ref-a"].uri != "/old/a" || state.entries["entry-a"].original != "/old/a" || state.phase != "archive_payload_moved" || state.manifest != nil || state.event != nil {
		t.Fatalf("late failure leaked partial projection: %#v", state)
	}
	state.failEvent = false
	record, err := service.CommitWorkspaceArchiveProjection(context.Background(), input)
	if err != nil {
		t.Fatalf("projection commit: %v", err)
	}
	if record.Phase != "archive_projections_committed" || state.refs["ref-a"].uri != "/new/a" || state.entries["entry-a"].original != "/new/a" || state.manifestInserts != 1 || state.eventInserts != 1 {
		t.Fatalf("projection state = record:%#v state:%#v", record, state)
	}
	record, err = service.CommitWorkspaceArchiveProjection(context.Background(), input)
	if err != nil || record.Phase != "archive_projections_committed" || state.manifestInserts != 1 || state.eventInserts != 1 {
		t.Fatalf("idempotent projection = record:%#v state:%#v err:%v", record, state, err)
	}
}

func TestWorkspaceArchiveProjectionRejectsLateSourceCatalogPhantom(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	state := &workspaceProjectionFakeState{
		phase: "archive_payload_moved", digest: "sha256:" + strings.Repeat("a", 64),
		plan: json.RawMessage(`{"operation_id":"workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV"}`), plannedAt: now, intentAt: now, movedAt: now, updatedAt: now,
		refs: map[string]rebindFakeRef{
			"ref-a":    {entryID: "entry-a", uri: "/old/a"},
			"ref-late": {entryID: "entry-late", uri: "/old/late"},
		},
		entries: map[string]rebindFakeEntry{
			"entry-a":    {original: "/old/a", view: "old/a"},
			"entry-late": {original: "/old/late", view: "old/late"},
		},
	}
	db := sql.OpenDB(workspaceProjectionFakeConnector{state: state})
	defer db.Close()
	service := NewService(db)
	input := WorkspaceArchiveProjectionInput{
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV", PlanDigest: state.digest,
		SourceAbsolutePath: "/old",
		Rebind: RebindPathsInput{
			PhysicalRefs: []PhysicalRefPathRebind{{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"}},
			Entries:      []EntryPathRebind{{StorageEntryID: "entry-a", ExpectedOriginalSourcePath: "/old/a", NewOriginalSourcePath: "/new/a", ExpectedCurrentViewPath: "old/a", NewCurrentViewPath: "new/a"}},
		},
		ManifestSchemaVersion: "storage.workspace_archive_manifest.v1", EvidenceKind: "physical_workspace_move",
		WorkspaceKind: "topic", ObjectID: "topic_object_one", Slug: "topic-one",
		InventoryDigest: "sha256:" + strings.Repeat("b", 64), ArchiveSourceIdentityJSON: json.RawMessage(`{"presence":"present"}`),
		ManifestJSON: json.RawMessage(`{"manifest":"exact"}`), AuthenticationKeyID: "workspace.archive.test",
		AuthenticationTag: "hmac-sha256:" + strings.Repeat("c", 64), ArchivedAt: now,
		EventID: "workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAV", EventSchemaVersion: "storage.workspace_lifecycle_event.v1",
		EventKind: "workspace.lifecycle_changed", Transition: "active_to_archived", FromState: "active", ToState: "archived",
		SourceRoot: "box", SourceRelativePath: "old", DestinationRoot: "storage", DestinationRelativePath: "archive/topics/topic-one/content",
		ActorID: "actor_archive_test", Reason: "archive topic", EventJSON: json.RawMessage(`{"event":"exact"}`), CommittedAt: now,
	}
	if _, err := service.CommitWorkspaceArchiveProjection(context.Background(), input); err == nil || !strings.Contains(err.Error(), "changed after review") {
		t.Fatalf("late source-bound catalog row error = %v", err)
	}
	if state.refs["ref-a"].uri != "/old/a" || state.entries["entry-a"].original != "/old/a" || state.phase != "archive_payload_moved" || state.manifest != nil || state.event != nil || state.manifestInserts != 0 || state.eventInserts != 0 {
		t.Fatalf("late catalog phantom leaked successful projection: %#v", state)
	}
}

func TestWorkspaceRestoreProjectionAtomicallyRebindsManifestAndEvent(t *testing.T) {
	now := time.Unix(1_900_000_000, 0).UTC()
	state := &workspaceProjectionFakeState{
		operationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAY",
		phase:       "restore_payload_moved", digest: "sha256:" + strings.Repeat("a", 64),
		plan: json.RawMessage(`{"operation_kind":"restore"}`), plannedAt: now, intentAt: now, movedAt: now, updatedAt: now,
		refs:     map[string]rebindFakeRef{"ref-a": {entryID: "entry-a", uri: "/old/a"}},
		entries:  map[string]rebindFakeEntry{"entry-a": {original: "/old/a", view: "old/a"}},
		manifest: json.RawMessage(`{"state":"archived"}`), lifecycleState: "archived", failEvent: true,
	}
	db := sql.OpenDB(workspaceProjectionFakeConnector{state: state})
	defer db.Close()
	service := NewService(db)
	input := WorkspaceRestoreProjectionInput{
		ArchiveOperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OperationID:        state.operationID, PlanDigest: state.digest,
		SourceAbsolutePath: "/old", CatalogSourceRelativePath: "old",
		Rebind: RebindPathsInput{
			PhysicalRefs: []PhysicalRefPathRebind{{StoragePhysicalRefID: "ref-a", StorageEntryID: "entry-a", ExpectedURI: "/old/a", NewURI: "/new/a"}},
			Entries:      []EntryPathRebind{{StorageEntryID: "entry-a", ExpectedOriginalSourcePath: "/old/a", NewOriginalSourcePath: "/new/a", ExpectedCurrentViewPath: "old/a", NewCurrentViewPath: "new/a"}},
		},
		ExpectedManifestJSON: json.RawMessage(`{"state":"archived"}`), ManifestJSON: json.RawMessage(`{"state":"active"}`),
		RestorePlanDigest: state.digest, RestoreOperationID: state.operationID,
		AuthenticationKeyID: "workspace.archive.test", AuthenticationTag: "hmac-sha256:" + strings.Repeat("c", 64), RestoredAt: now,
		EventID: "workspace_lifecycle_event_01ARZ3NDEKTSV4RRFFQ69G5FAY", EventSchemaVersion: "storage.workspace_lifecycle_event.v1",
		EventKind: "workspace.lifecycle_changed", WorkspaceKind: "topic", ObjectID: "topic_object_one", Slug: "topic-one",
		Transition: "archived_to_active", FromState: "archived", ToState: "active",
		SourceRoot: "storage", SourceRelativePath: "archive/topics/topic-one/workspace",
		DestinationRoot: "box", DestinationRelativePath: "Topics/topic-one",
		ActorID: "actor_archive_test", Reason: "restore topic", EventJSON: json.RawMessage(`{"event":"restore"}`), CommittedAt: now,
	}
	if _, err := service.CommitWorkspaceRestoreProjection(context.Background(), input); err == nil || !strings.Contains(err.Error(), "injected event failure") {
		t.Fatalf("late restore event failure = %v", err)
	}
	if state.refs["ref-a"].uri != "/old/a" || state.entries["entry-a"].original != "/old/a" || state.phase != "restore_payload_moved" || state.lifecycleState != "archived" || string(state.manifest) != `{"state":"archived"}` || state.event != nil {
		t.Fatalf("late restore failure leaked partial projection: %#v", state)
	}
	state.failEvent = false
	record, err := service.CommitWorkspaceRestoreProjection(context.Background(), input)
	if err != nil {
		t.Fatalf("restore projection commit: %v", err)
	}
	if record.Phase != "restore_projections_committed" || state.refs["ref-a"].uri != "/new/a" || state.entries["entry-a"].original != "/new/a" || state.lifecycleState != "active" || string(state.manifest) != `{"state":"active"}` || state.eventInserts != 1 {
		t.Fatalf("restore projection state = record:%#v state:%#v", record, state)
	}
	record, err = service.CommitWorkspaceRestoreProjection(context.Background(), input)
	if err != nil || record.Phase != "restore_projections_committed" || state.eventInserts != 1 {
		t.Fatalf("idempotent restore projection = record:%#v state:%#v err:%v", record, state, err)
	}
}

type rebindFakeRef struct{ entryID, uri string }
type rebindFakeEntry struct{ original, view string }

type rebindFakeState struct {
	mu      sync.Mutex
	refs    map[string]rebindFakeRef
	entries map[string]rebindFakeEntry
}

func (s *rebindFakeState) refURI(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refs[id].uri
}

func (s *rebindFakeState) setRefURI(id, uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := s.refs[id]
	ref.uri = uri
	s.refs[id] = ref
}

type rebindFakeConnector struct{ state *rebindFakeState }

func (c rebindFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return &rebindFakeConn{state: c.state}, nil
}
func (c rebindFakeConnector) Driver() driver.Driver { return rebindFakeDriver{} }

type rebindFakeDriver struct{}

func (rebindFakeDriver) Open(string) (driver.Conn, error) { return nil, fmt.Errorf("use connector") }

type rebindFakeConn struct {
	state   *rebindFakeState
	refs    map[string]rebindFakeRef
	entries map[string]rebindFakeEntry
	inTx    bool
}

func (c *rebindFakeConn) Prepare(string) (driver.Stmt, error) { return nil, fmt.Errorf("unsupported") }
func (c *rebindFakeConn) Close() error                        { return nil }
func (c *rebindFakeConn) Begin() (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.refs = cloneRebindRefs(c.state.refs)
	c.entries = cloneRebindEntries(c.state.entries)
	c.inTx = true
	return rebindFakeTx{conn: c}, nil
}

func (c *rebindFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !c.inTx {
		return nil, fmt.Errorf("query outside transaction")
	}
	compact := strings.Join(strings.Fields(query), " ")
	if strings.Contains(compact, "SELECT uri FROM storage.storage_physical_refs") {
		id, entryID := fmt.Sprint(args[0].Value), fmt.Sprint(args[1].Value)
		ref, ok := c.refs[id]
		if !ok || ref.entryID != entryID {
			return &rebindFakeRows{columns: []string{"uri"}}, nil
		}
		return &rebindFakeRows{columns: []string{"uri"}, rows: [][]driver.Value{{ref.uri}}}, nil
	}
	if strings.Contains(compact, "SELECT original_source_path, current_view_path") {
		entry, ok := c.entries[fmt.Sprint(args[0].Value)]
		if !ok {
			return &rebindFakeRows{columns: []string{"original_source_path", "current_view_path"}}, nil
		}
		return &rebindFakeRows{columns: []string{"original_source_path", "current_view_path"}, rows: [][]driver.Value{{entry.original, entry.view}}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", compact)
}

func (c *rebindFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.inTx {
		return nil, fmt.Errorf("exec outside transaction")
	}
	compact := strings.Join(strings.Fields(query), " ")
	if strings.Contains(compact, "UPDATE storage.storage_physical_refs") {
		id := fmt.Sprint(args[1].Value)
		ref := c.refs[id]
		ref.uri = fmt.Sprint(args[0].Value)
		c.refs[id] = ref
		return driver.RowsAffected(1), nil
	}
	if strings.Contains(compact, "UPDATE storage.storage_entries") {
		c.entries[fmt.Sprint(args[2].Value)] = rebindFakeEntry{original: fmt.Sprint(args[0].Value), view: fmt.Sprint(args[1].Value)}
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected exec: %s", compact)
}

type rebindFakeTx struct{ conn *rebindFakeConn }

func (tx rebindFakeTx) Commit() error {
	tx.conn.state.mu.Lock()
	defer tx.conn.state.mu.Unlock()
	tx.conn.state.refs = cloneRebindRefs(tx.conn.refs)
	tx.conn.state.entries = cloneRebindEntries(tx.conn.entries)
	tx.conn.inTx = false
	return nil
}
func (tx rebindFakeTx) Rollback() error { tx.conn.inTx = false; return nil }

type rebindFakeRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *rebindFakeRows) Columns() []string { return r.columns }
func (r *rebindFakeRows) Close() error      { return nil }
func (r *rebindFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func cloneRebindRefs(source map[string]rebindFakeRef) map[string]rebindFakeRef {
	out := make(map[string]rebindFakeRef, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
func cloneRebindEntries(source map[string]rebindFakeEntry) map[string]rebindFakeEntry {
	out := make(map[string]rebindFakeEntry, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

type workspaceProjectionFakeState struct {
	mu                 sync.Mutex
	operationID        string
	phase              string
	digest             string
	plan               json.RawMessage
	plannedAt          time.Time
	intentAt           time.Time
	movedAt            time.Time
	projectedAt        time.Time
	updatedAt          time.Time
	refs               map[string]rebindFakeRef
	entries            map[string]rebindFakeEntry
	manifest           json.RawMessage
	lifecycleState     string
	restoreOperationID string
	restorePlanDigest  string
	event              json.RawMessage
	manifestInserts    int
	eventInserts       int
	failEvent          bool
}

type workspaceProjectionFakeConnector struct{ state *workspaceProjectionFakeState }

func (c workspaceProjectionFakeConnector) Connect(context.Context) (driver.Conn, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return &workspaceProjectionFakeConn{
		state: c.state, operationID: c.state.operationID, phase: c.state.phase, digest: c.state.digest,
		plan: append(json.RawMessage(nil), c.state.plan...), plannedAt: c.state.plannedAt,
		intentAt: c.state.intentAt, movedAt: c.state.movedAt, projectedAt: c.state.projectedAt,
		updatedAt: c.state.updatedAt, refs: cloneRebindRefs(c.state.refs), entries: cloneRebindEntries(c.state.entries),
		manifest: append(json.RawMessage(nil), c.state.manifest...), lifecycleState: c.state.lifecycleState,
		restoreOperationID: c.state.restoreOperationID, restorePlanDigest: c.state.restorePlanDigest,
		event:           append(json.RawMessage(nil), c.state.event...),
		manifestInserts: c.state.manifestInserts, eventInserts: c.state.eventInserts,
	}, nil
}

func (c workspaceProjectionFakeConnector) Driver() driver.Driver {
	return workspaceProjectionFakeDriver{}
}

type workspaceProjectionFakeDriver struct{}

func (workspaceProjectionFakeDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use connector")
}

type workspaceProjectionFakeConn struct {
	state              *workspaceProjectionFakeState
	operationID        string
	phase              string
	digest             string
	plan               json.RawMessage
	plannedAt          time.Time
	intentAt           time.Time
	movedAt            time.Time
	projectedAt        time.Time
	updatedAt          time.Time
	refs               map[string]rebindFakeRef
	entries            map[string]rebindFakeEntry
	manifest           json.RawMessage
	lifecycleState     string
	restoreOperationID string
	restorePlanDigest  string
	event              json.RawMessage
	manifestInserts    int
	eventInserts       int
	inTx               bool
}

func (c *workspaceProjectionFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unsupported")
}

func (c *workspaceProjectionFakeConn) Close() error { return nil }

func (c *workspaceProjectionFakeConn) Begin() (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.phase = c.state.phase
	c.operationID = c.state.operationID
	c.digest = c.state.digest
	c.plan = append(c.plan[:0], c.state.plan...)
	c.plannedAt = c.state.plannedAt
	c.intentAt = c.state.intentAt
	c.movedAt = c.state.movedAt
	c.projectedAt = c.state.projectedAt
	c.updatedAt = c.state.updatedAt
	c.refs = cloneRebindRefs(c.state.refs)
	c.entries = cloneRebindEntries(c.state.entries)
	c.manifest = append(c.manifest[:0], c.state.manifest...)
	c.lifecycleState = c.state.lifecycleState
	c.restoreOperationID = c.state.restoreOperationID
	c.restorePlanDigest = c.state.restorePlanDigest
	c.event = append(c.event[:0], c.state.event...)
	c.manifestInserts = c.state.manifestInserts
	c.eventInserts = c.state.eventInserts
	c.inTx = true
	return workspaceProjectionFakeTx{conn: c}, nil
}

func (c *workspaceProjectionFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	compact := strings.Join(strings.Fields(query), " ")
	if strings.Contains(compact, "SELECT phase, plan_digest, last_safe_phase") {
		return &rebindFakeRows{columns: []string{"phase", "plan_digest", "last_safe_phase"}, rows: [][]driver.Value{{c.phase, c.digest, nil}}}, nil
	}
	if strings.Contains(compact, "SELECT lifecycle_state, restore_operation_id, restore_plan_digest, manifest_json") {
		var restoreOperationID, restorePlanDigest driver.Value
		if c.restoreOperationID != "" {
			restoreOperationID = c.restoreOperationID
		}
		if c.restorePlanDigest != "" {
			restorePlanDigest = c.restorePlanDigest
		}
		return &rebindFakeRows{columns: []string{"lifecycle_state", "restore_operation_id", "restore_plan_digest", "manifest_json"}, rows: [][]driver.Value{{c.lifecycleState, restoreOperationID, restorePlanDigest, []byte(c.manifest)}}}, nil
	}
	if strings.Contains(compact, "SELECT storage_entry_id, original_source_path, current_view_path") {
		ids := make([]string, 0, len(c.entries))
		for id := range c.entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := make([][]driver.Value, 0, len(ids))
		for _, id := range ids {
			entry := c.entries[id]
			if workspaceProjectionFakePathMatches(entry.original, args) || workspaceProjectionFakePathMatches(entry.view, args) {
				rows = append(rows, []driver.Value{id, entry.original, entry.view})
			}
		}
		return &rebindFakeRows{columns: []string{"storage_entry_id", "original_source_path", "current_view_path"}, rows: rows}, nil
	}
	if strings.Contains(compact, "SELECT ref.storage_physical_ref_id, ref.storage_entry_id, ref.uri") {
		ids := make([]string, 0, len(c.refs))
		for id := range c.refs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		rows := make([][]driver.Value, 0, len(ids))
		for _, id := range ids {
			ref := c.refs[id]
			if workspaceProjectionFakePathMatches(ref.uri, args) {
				rows = append(rows, []driver.Value{id, ref.entryID, ref.uri})
			}
		}
		return &rebindFakeRows{columns: []string{"storage_physical_ref_id", "storage_entry_id", "uri"}, rows: rows}, nil
	}
	if strings.Contains(compact, "SELECT uri FROM storage.storage_physical_refs") {
		ref, ok := c.refs[fmt.Sprint(args[0].Value)]
		if !ok {
			return &rebindFakeRows{columns: []string{"uri"}}, nil
		}
		return &rebindFakeRows{columns: []string{"uri"}, rows: [][]driver.Value{{ref.uri}}}, nil
	}
	if strings.Contains(compact, "SELECT original_source_path, current_view_path") {
		entry, ok := c.entries[fmt.Sprint(args[0].Value)]
		if !ok {
			return &rebindFakeRows{columns: []string{"original_source_path", "current_view_path"}}, nil
		}
		return &rebindFakeRows{columns: []string{"original_source_path", "current_view_path"}, rows: [][]driver.Value{{entry.original, entry.view}}}, nil
	}
	if strings.Contains(compact, "SELECT manifest_json FROM storage.workspace_archive_manifests") {
		if len(c.manifest) == 0 {
			return &rebindFakeRows{columns: []string{"manifest_json"}}, nil
		}
		return &rebindFakeRows{columns: []string{"manifest_json"}, rows: [][]driver.Value{{[]byte(c.manifest)}}}, nil
	}
	if strings.Contains(compact, "SELECT details FROM storage.workspace_lifecycle_events") {
		if len(c.event) == 0 {
			return &rebindFakeRows{columns: []string{"details"}}, nil
		}
		return &rebindFakeRows{columns: []string{"details"}, rows: [][]driver.Value{{[]byte(c.event)}}}, nil
	}
	if strings.Contains(compact, "SELECT workspace_archive_operation_id, plan_digest, plan_json, phase") {
		projected := driver.Value(nil)
		if !c.projectedAt.IsZero() {
			projected = c.projectedAt
		}
		operationID := c.operationID
		if operationID == "" {
			operationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAV"
		}
		return &rebindFakeRows{columns: []string{"workspace_archive_operation_id", "plan_digest", "plan_json", "phase", "last_safe_phase", "terminal_status", "planned_at", "intent_committed_at", "payload_moved_at", "projections_committed_at", "completed_at", "updated_at"}, rows: [][]driver.Value{{
			operationID, c.digest, []byte(c.plan), c.phase, nil, "running", c.plannedAt, c.intentAt, c.movedAt, projected, nil, c.updatedAt,
		}}}, nil
	}
	if strings.Contains(compact, "FROM storage.workspace_archive_findings") {
		return &rebindFakeRows{columns: []string{"workspace_archive_finding_id", "finding_code", "severity", "at_phase", "summary", "repairable", "evidence_json", "created_at"}}, nil
	}
	return nil, fmt.Errorf("unexpected query: %s", compact)
}

func (c *workspaceProjectionFakeConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.inTx {
		return nil, fmt.Errorf("exec outside transaction")
	}
	compact := strings.Join(strings.Fields(query), " ")
	switch {
	case strings.Contains(compact, "LOCK TABLE storage.storage_entries"):
	case strings.Contains(compact, "UPDATE storage.storage_physical_refs"):
		id := fmt.Sprint(args[1].Value)
		ref := c.refs[id]
		ref.uri = fmt.Sprint(args[0].Value)
		c.refs[id] = ref
	case strings.Contains(compact, "UPDATE storage.storage_entries"):
		c.entries[fmt.Sprint(args[2].Value)] = rebindFakeEntry{original: fmt.Sprint(args[0].Value), view: fmt.Sprint(args[1].Value)}
	case strings.Contains(compact, "INSERT INTO storage.workspace_archive_manifests"):
		if len(c.manifest) == 0 {
			c.manifest = jsonArgument(args[9].Value)
			c.manifestInserts++
		}
	case strings.Contains(compact, "UPDATE storage.workspace_archive_manifests"):
		if c.lifecycleState != "archived" || !jsonDocumentsEqual(c.manifest, jsonArgument(args[8].Value)) {
			return driver.RowsAffected(0), nil
		}
		c.lifecycleState = "active"
		c.restoreOperationID = fmt.Sprint(args[1].Value)
		c.restorePlanDigest = fmt.Sprint(args[2].Value)
		c.manifest = jsonArgument(args[3].Value)
	case strings.Contains(compact, "INSERT INTO storage.workspace_lifecycle_events"):
		if c.state.failEvent {
			return nil, errors.New("injected event failure")
		}
		if len(c.event) == 0 {
			c.event = jsonArgument(args[16].Value)
			c.eventInserts++
		}
	case strings.Contains(compact, "UPDATE storage.workspace_archive_operations"):
		if strings.Contains(compact, "restore_projections_committed") {
			c.phase = "restore_projections_committed"
		} else {
			c.phase = "archive_projections_committed"
		}
		c.projectedAt = args[2].Value.(time.Time)
		c.updatedAt = c.projectedAt
	default:
		return nil, fmt.Errorf("unexpected exec: %s", compact)
	}
	return driver.RowsAffected(1), nil
}

func workspaceProjectionFakePathMatches(value string, args []driver.NamedValue) bool {
	for _, index := range []int{0, 2, 4} {
		root := fmt.Sprint(args[index].Value)
		if value == root || strings.HasPrefix(value, root+"/") {
			return true
		}
	}
	return false
}

type workspaceProjectionFakeTx struct{ conn *workspaceProjectionFakeConn }

func (tx workspaceProjectionFakeTx) Commit() error {
	tx.conn.state.mu.Lock()
	defer tx.conn.state.mu.Unlock()
	tx.conn.state.phase = tx.conn.phase
	tx.conn.state.operationID = tx.conn.operationID
	tx.conn.state.projectedAt = tx.conn.projectedAt
	tx.conn.state.updatedAt = tx.conn.updatedAt
	tx.conn.state.refs = cloneRebindRefs(tx.conn.refs)
	tx.conn.state.entries = cloneRebindEntries(tx.conn.entries)
	tx.conn.state.manifest = append(json.RawMessage(nil), tx.conn.manifest...)
	tx.conn.state.lifecycleState = tx.conn.lifecycleState
	tx.conn.state.restoreOperationID = tx.conn.restoreOperationID
	tx.conn.state.restorePlanDigest = tx.conn.restorePlanDigest
	tx.conn.state.event = append(json.RawMessage(nil), tx.conn.event...)
	tx.conn.state.manifestInserts = tx.conn.manifestInserts
	tx.conn.state.eventInserts = tx.conn.eventInserts
	tx.conn.inTx = false
	return nil
}

func (tx workspaceProjectionFakeTx) Rollback() error {
	tx.conn.inTx = false
	return nil
}

func jsonArgument(value any) json.RawMessage {
	switch typed := value.(type) {
	case []byte:
		return append(json.RawMessage(nil), typed...)
	case string:
		return json.RawMessage(typed)
	default:
		return json.RawMessage(fmt.Sprint(value))
	}
}
