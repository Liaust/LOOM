package sync_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodeagent"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/objectstore"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/search"
	loomsync "loom.local/loom/internal/sync"
)

func metadataDatabase(t *testing.T) (*sql.DB, requestctx.Context) {
	t.Helper()
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires a disposable local PostgreSQL administrator")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	host, socket := u.Hostname(), u.Query().Get("host")
	if !((socket == "" && (host == "localhost" || host == "127.0.0.1" || host == "::1")) || (host == "" && strings.HasPrefix(socket, "/tmp/"))) {
		t.Fatal("local disposable endpoint required")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	name := "metadata_" + strings.ToLower(ids.NewEventID())
	if _, err = admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var db *sql.DB
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(t.Context()); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(t.Context(), db, "corr_source_metadata_test")
	if err != nil {
		t.Fatal(err)
	}
	return db, req
}

func TestSourceMetadataPostgres(t *testing.T) {
	db, req := metadataDatabase(t)
	ctx := t.Context()
	s := loomsync.NewService(db)
	node, err := nodes.NewService(db).GetNode(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	cred, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: node.NodeID, Reason: "disposable metadata test"})
	if err != nil {
		t.Fatal(err)
	}
	store := objectstore.New(t.TempDir())
	objs := objects.NewService(db, store)
	finder := search.NewService(db, store)
	old := time.Date(2026, 9, 8, 19, 16, 3, 628449000, time.UTC)
	input := loomsync.SyncedObjectInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, LocalObjectRef: ids.NewObjectID(), LocalVersionRef: ids.NewObjectVersionID(), LocalSequence: 1,
		LogicalName: "cadence.md", SourcePath: "watched-root://notes/cadence.md", SourceMtime: &old, ScopeRef: req.ScopeID, SizeBytes: 3, MimeType: "text/markdown",
		HashURI: "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", ContentBase64: base64.StdEncoding.EncodeToString([]byte("abc")), IndexPolicy: loomsync.IndexPolicyNone}
	upload, err := s.IngestSyncedObject(ctx, req, objs, finder, input)
	if err != nil {
		t.Fatal(err)
	}
	p := loomsync.MetadataObservation{SchemaVersion: loomsync.MetadataObservationSchema, LocalObjectRef: input.LocalObjectRef, LocalVersionRef: input.LocalVersionRef,
		ObjectID: upload.ObjectID, VersionID: upload.VersionID, HashURI: input.HashURI, SourcePath: input.SourcePath, SourceMtime: old.Add(time.Minute)}
	item := loomsync.PushBatchItem{LocalRef: ids.NewLocalOutboxID(), ItemKind: loomsync.ItemKindObjectMetadata, StreamName: loomsync.StreamObjectMetadata, LocalSequence: 1}
	push := func(p loomsync.MetadataObservation, i loomsync.PushBatchItem) (loomsync.PushBatchResult, error) {
		i.PayloadJSON, _ = json.Marshal(p)
		return s.PushBatch(ctx, req, loomsync.PushBatchInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, BatchKind: loomsync.BatchKindObjectMetadata, Items: []loomsync.PushBatchItem{i}})
	}
	assertTime := func(want time.Time) {
		t.Helper()
		var got time.Time
		var v string
		if err := db.QueryRow(`SELECT source_mtime,latest_version_id FROM files.file_metadata WHERE object_id=$1`, p.ObjectID).Scan(&got, &v); err != nil {
			t.Fatal(err)
		}
		if !got.Equal(want) || v != p.VersionID {
			t.Fatalf("time/version = %s/%s, want %s/%s", got, v, want, p.VersionID)
		}
	}
	assertStatus := func(result loomsync.PushBatchResult, err error, status string) {
		t.Helper()
		if err != nil || len(result.Items) != 1 || result.Items[0].Status != status {
			t.Fatalf("status want %s: %#v %v", status, result, err)
		}
	}
	r, err := push(p, item)
	assertStatus(r, err, loomsync.ItemStatusAccepted)
	assertTime(p.SourceMtime)
	// Consume the real receiver receipt with the owner implementation. A fake
	// transport must not mask different local/remote digest encodings.
	owner := nodeagent.Store{DataDir: t.TempDir()}
	if err := owner.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(normalized)
	local := nodeagent.LocalSyncOutboxItem{LocalOutboxID: item.LocalRef, LocalRef: item.LocalRef,
		ItemKind: item.ItemKind, StreamName: item.StreamName, LocalSequence: item.LocalSequence,
		PayloadJSON: raw, PayloadHash: fmt.Sprintf("%x", sha256.Sum256(canonical)), Status: "pending"}
	if err := owner.SaveSyncOutbox([]nodeagent.LocalSyncOutboxItem{local}); err != nil {
		t.Fatal(err)
	}
	ws := watchedroots.NewStore(owner.DataDir)
	if err := ws.SavePathState(watchedroots.PathState{RootKey: "notes", RelativePath: "cadence.md",
		LocalObjectID: p.LocalObjectRef, LocalVersionID: p.LocalVersionRef, MainObjectID: p.ObjectID,
		MainVersionID: p.VersionID, LastSyncedHashURI: p.HashURI}); err != nil {
		t.Fatal(err)
	}
	if err := owner.ApplySyncPushResult(r); err != nil {
		t.Fatalf("owner refused actual receiver acknowledgement: %v", err)
	}
	ack, err := ws.LoadPathState("notes", "cadence.md")
	if err != nil || ack.LastSyncedModifiedAt == nil || !ack.LastSyncedModifiedAt.Equal(p.SourceMtime) || ack.LastSyncedMetadataSequence != item.LocalSequence {
		t.Fatalf("owner acknowledgement did not converge: %#v %v", ack, err)
	}
	terminal, err := owner.LoadSyncOutbox()
	if err != nil || len(terminal) != 1 || terminal[0].Status != loomsync.ItemStatusAccepted {
		t.Fatalf("owner outbox did not converge: %#v %v", terminal, err)
	}
	// An authenticated different owner cannot update this source.
	other := ids.NewNodeID()
	if _, err := db.Exec(`INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status)
		VALUES ($1,$1,'Other fixture owner','workstation','workspace','workspace','active')`, other); err != nil {
		t.Fatal(err)
	}
	otherCred, err := nodes.NewService(db).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: other, Reason: "metadata isolation fixture"})
	if err != nil {
		t.Fatal(err)
	}
	otherItem := item
	otherItem.PayloadJSON, _ = json.Marshal(p)
	foreign, err := s.PushBatch(ctx, req, loomsync.PushBatchInput{NodeRef: other, CredentialToken: otherCred.CredentialToken, Items: []loomsync.PushBatchItem{otherItem}})
	assertStatus(foreign, err, loomsync.ItemStatusConflicted)
	assertTime(p.SourceMtime)
	r, err = push(p, item)
	assertStatus(r, err, loomsync.ItemStatusDuplicate)
	mutated := p
	mutated.SourceMtime = old
	r, err = push(mutated, item)
	assertStatus(r, err, loomsync.ItemStatusConflicted)
	assertTime(p.SourceMtime)
	for _, change := range []func(*loomsync.MetadataObservation){
		func(x *loomsync.MetadataObservation) { x.LocalObjectRef = ids.NewObjectID() },
		func(x *loomsync.MetadataObservation) { x.LocalVersionRef = ids.NewObjectVersionID() },
		func(x *loomsync.MetadataObservation) { x.VersionID = ids.NewObjectVersionID() },
		func(x *loomsync.MetadataObservation) { x.HashURI = "sha256:" + strings.Repeat("a", 64) },
		func(x *loomsync.MetadataObservation) { x.SourcePath = "watched-root://other/cadence.md" },
	} {
		x := p
		change(&x)
		i := item
		i.LocalRef = ids.NewLocalOutboxID()
		i.LocalSequence = 2
		r, err = push(x, i)
		assertStatus(r, err, loomsync.ItemStatusConflicted)
		assertTime(p.SourceMtime)
	}
	// An older timestamp is valid under a newer observation; old sequence is not.
	second := item
	second.LocalRef = ids.NewLocalOutboxID()
	second.LocalSequence = 3
	mutated.SourceMtime = old.Add(-time.Hour)
	r, err = push(mutated, second)
	assertStatus(r, err, loomsync.ItemStatusAccepted)
	assertTime(mutated.SourceMtime)
	stale := item
	stale.LocalRef = ids.NewLocalOutboxID()
	stale.LocalSequence = 2
	r, err = push(p, stale)
	assertStatus(r, err, loomsync.ItemStatusConflicted)
	assertTime(mutated.SourceMtime)
	// Returning to an earlier value is still a distinct, ordered observation.
	third := item
	third.LocalRef = ids.NewLocalOutboxID()
	third.LocalSequence = 4
	r, err = push(p, third)
	assertStatus(r, err, loomsync.ItemStatusAccepted)
	assertTime(p.SourceMtime)
	var count int
	var versionTime time.Time
	if err := db.QueryRow(`SELECT count(*) FROM objects.object_versions WHERE object_id=$1`, p.ObjectID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new content version: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT (metadata->>'source_mtime')::timestamptz FROM objects.object_versions WHERE object_version_id=$1`, p.VersionID).Scan(&versionTime); err != nil || !versionTime.Equal(old) {
		t.Fatalf("historical metadata changed: %s %v", versionTime, err)
	}
	// A failure after metadata writes must roll the whole batch back.
	if _, err := db.Exec(`CREATE FUNCTION reject_metadata_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.event_type='sync.source_metadata_observed' THEN RAISE EXCEPTION 'fixture rollback'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER reject_metadata_event BEFORE INSERT ON events.events FOR EACH ROW EXECUTE FUNCTION reject_metadata_event()`); err != nil {
		t.Fatal(err)
	}
	fourth := item
	fourth.LocalRef = ids.NewLocalOutboxID()
	fourth.LocalSequence = 5
	if _, err = push(mutated, fourth); err == nil {
		t.Fatal("injected failure accepted")
	}
	assertTime(p.SourceMtime)
	if _, err := db.Exec(`DROP TRIGGER reject_metadata_event ON events.events`); err != nil {
		t.Fatal(err)
	}
	// Concurrent arrivals converge on the newer durable sequence, not its mtime.
	type outcome struct {
		sequence int64
		result   loomsync.PushBatchResult
		err      error
	}
	results := make(chan outcome, 2)
	for _, sequence := range []int64{5, 6} {
		go func(sequence int64) {
			x := p
			x.SourceMtime = old.Add(-time.Duration(sequence) * time.Hour)
			i := item
			i.LocalRef = ids.NewLocalOutboxID()
			i.LocalSequence = sequence
			r, err := push(x, i)
			results <- outcome{sequence, r, err}
		}(sequence)
	}
	for range 2 {
		o := <-results
		if o.err != nil || len(o.result.Items) != 1 {
			t.Fatalf("concurrent metadata: %#v %v", o.result, o.err)
		}
		status := o.result.Items[0].Status
		if (o.sequence == 6 && status != loomsync.ItemStatusAccepted) || (status != loomsync.ItemStatusAccepted && status != loomsync.ItemStatusConflicted) {
			t.Fatalf("concurrent sequence %d: %s", o.sequence, status)
		}
	}
	assertTime(old.Add(-6 * time.Hour))
	// Reusing a batch key cannot substitute its authenticated observation.
	idem := item
	idem.LocalRef = ids.NewLocalOutboxID()
	idem.LocalSequence = 7
	idem.PayloadJSON, _ = json.Marshal(p)
	batch := loomsync.PushBatchInput{NodeRef: node.NodeID, CredentialToken: cred.CredentialToken, IdempotencyKey: "metadata-fixture", Items: []loomsync.PushBatchItem{idem}}
	r, err = s.PushBatch(ctx, req, batch)
	assertStatus(r, err, loomsync.ItemStatusAccepted)
	r, err = s.PushBatch(ctx, req, batch)
	assertStatus(r, err, loomsync.ItemStatusAccepted)
	batch.Items[0].PayloadJSON, _ = json.Marshal(mutated)
	if _, err = s.PushBatch(ctx, req, batch); err == nil {
		t.Fatal("accepted changed batch-key payload")
	}
	assertTime(p.SourceMtime)
	// A newer real content version fences pending metadata for the old one.
	// The durable current watermark also survives removal of old transport receipts.
	if _, err := db.Exec(`DELETE FROM sync.batch_items WHERE origin_node_id=$1 AND item_kind='object_metadata'`, node.NodeID); err != nil {
		t.Fatal(err)
	}
	prunedReplay := item
	prunedReplay.LocalRef = ids.NewLocalOutboxID()
	prunedReplay.LocalSequence = 6
	r, err = push(mutated, prunedReplay)
	assertStatus(r, err, loomsync.ItemStatusConflicted)
	assertTime(p.SourceMtime)
	input.LocalVersionRef = ids.NewObjectVersionID()
	input.LocalSequence = 2
	input.ContentBase64 = base64.StdEncoding.EncodeToString([]byte("abcd"))
	input.SizeBytes = 4
	input.HashURI = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("abcd")))
	newUpload, err := s.IngestSyncedObject(ctx, req, objs, finder, input)
	if err != nil {
		t.Fatal(err)
	}
	oldItem := item
	oldItem.LocalRef = ids.NewLocalOutboxID()
	oldItem.LocalSequence = 8
	r, err = push(p, oldItem)
	assertStatus(r, err, loomsync.ItemStatusConflicted)
	var current string
	if err := db.QueryRow(`SELECT latest_version_id FROM files.file_metadata WHERE object_id=$1`, p.ObjectID).Scan(&current); err != nil || current != newUpload.VersionID {
		t.Fatalf("old metadata overwrote content: %s %v", current, err)
	}
}
