package watchedroots

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
)

func TestReportCorrelatesOrdinaryBoxPolicyPostgres(t *testing.T) {
	raw := os.Getenv("LOOM_TEST_DB_URL")
	if raw == "" {
		t.Skip("requires local disposable PostgreSQL administrator LOOM_TEST_DB_URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" && !(h == "" && strings.HasPrefix(u.Query().Get("host"), "/tmp/")) {
		t.Fatal("requires a local disposable PostgreSQL endpoint")
	}
	admin, err := sql.Open("pgx", raw)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := "box_report_" + strings.ToLower(strings.TrimPrefix(ids.NewProjectID(), "project_"))
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(`DROP DATABASE "` + name + `" WITH (FORCE)`); err != nil {
			t.Error(err)
		}
	}()
	u.Path = "/" + name
	if _, err := migrations.Up(t.Context(), u.String(), filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	req := requestctx.Context{ActorID: ids.NewActorID()}
	execSQL(`INSERT INTO identity.actors (actor_id,actor_key,display_name,actor_kind,status) VALUES ($1,'report-test','Report test','human','active')`, req.ActorID)
	owner, other := ids.NewNodeID(), ids.NewNodeID()
	for i, id := range []string{owner, other} {
		execSQL(`INSERT INTO nodes.nodes (node_id,node_key,display_name,node_kind,node_role,runtime_class,status) VALUES ($1,$2,'Report test','server','main','native','active')`, id, fmt.Sprintf("report-owner-%d", i))
	}
	req.OriginNodeID, req.OriginNodeKey = owner, "report-owner-0"
	credential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: owner, Reason: "disposable report test"})
	if err != nil {
		t.Fatal(err)
	}
	otherCredential, err := nodes.NewService(db).IssueNodeCredential(t.Context(), req, nodes.IssueNodeCredentialInput{NodeRef: other, Reason: "disposable report test"})
	if err != nil {
		t.Fatal(err)
	}
	const root = "box_report_topics"
	execSQL(`INSERT INTO box.watch_root_registrations (box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,area_key,local_root_key,backend_root_key,config_hash,desired_config_hash,source_kind,activation_status)
	 VALUES ('box_watch_root_registration_report','box_report','/fixture/Box','/fixture/Box/.loom/box.yaml',$1,'report-owner-0','topics','topics',$2,'current','current','box_policy','pending_agent_apply')`, owner, root)
	input := ReportInput{CredentialToken: credential.CredentialToken, NodeRef: owner, RootKey: root, ConfigHash: "current", Status: StatusHealthy}
	service := NewService(db)
	assertStatus := func(want string) {
		t.Helper()
		var got string
		if err := db.QueryRow(`SELECT activation_status FROM box.watch_root_registrations WHERE box_id='box_report'`).Scan(&got); err != nil || got != want {
			t.Fatalf("registration status=%s, want %s: %v", got, want, err)
		}
	}
	report := func(in ReportInput) ReportResult {
		t.Helper()
		out, err := service.Report(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, mutate := range []func(*ReportInput){
		func(in *ReportInput) { in.CredentialToken = "" },
		func(in *ReportInput) { in.CredentialToken = "invalid" },
		func(in *ReportInput) { in.NodeRef = other },
	} {
		in := input
		mutate(&in)
		if _, err := service.Report(t.Context(), in); err == nil {
			t.Fatal("unauthenticated or wrong-node report accepted")
		}
		assertStatus("pending_agent_apply")
	}
	in := input
	in.NodeRef, in.CredentialToken = other, otherCredential.CredentialToken
	report(in)
	assertStatus("pending_agent_apply")
	in = input
	in.RootKey = root + "_wrong"
	report(in)
	assertStatus("pending_agent_apply")
	in = input
	in.ConfigHash = "old"
	report(in)
	assertStatus("pending_agent_apply")
	out := report(input)
	assertStatus("reported")
	if repeated := report(input); repeated.Root.WatchedRootID != out.Root.WatchedRootID {
		t.Fatal("repeat report changed root identity")
	}
	var coherent bool
	if err := db.QueryRow(`SELECT b.watched_root_id=w.watched_root_id AND b.last_reported_at=w.last_reported_at AND b.applied_revision=0 FROM box.watch_root_registrations b JOIN watched_roots.roots w ON w.node_id=b.node_id AND w.root_key=b.backend_root_key WHERE b.box_id='box_report'`).Scan(&coherent); err != nil || !coherent {
		t.Fatalf("report/activation evidence mismatch: %v", err)
	}
	report(in) // A newer mismatched report withdraws activation, not historical evidence.
	assertStatus("pending_agent_apply")
	report(input)
	assertStatus("reported")
	for _, status := range []string{"disabled", "stale", "blocked"} {
		execSQL(`UPDATE box.watch_root_registrations SET activation_status=$1 WHERE box_id='box_report'`, status)
		report(input)
		assertStatus(status)
	}
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='pending_agent_apply',source_contract_deleted_at=now() WHERE box_id='box_report'`)
	report(input)
	assertStatus("pending_agent_apply")
	execSQL(`UPDATE box.watch_root_registrations SET source_contract_deleted_at=NULL,source_kind='box_backup_contract' WHERE box_id='box_report'`)
	report(input)
	assertStatus("pending_agent_apply")
	execSQL(`UPDATE box.watch_root_registrations SET source_kind='box_policy',area_key='backup_topics' WHERE box_id='box_report'`)
	report(input)
	assertStatus("pending_agent_apply")
	execSQL(`UPDATE box.watch_root_registrations SET area_key='topics',owner_node_key='other-owner' WHERE box_id='box_report'`)
	report(input)
	assertStatus("pending_agent_apply")
	execSQL(`UPDATE box.watch_root_registrations SET owner_node_key='report-owner-0' WHERE box_id='box_report'`)
	report(input)
	assertStatus("reported")

	// A report waiting on a policy change must observe the committed disable,
	// while other readers see neither a partial report nor partial activation.
	lock, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.Exec(`UPDATE box.watch_root_registrations SET activation_status='disabled' WHERE box_id='box_report'`); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := service.Report(waitCtx, in)
		done <- err
		close(done)
	}()
	defer func() {
		cancel()
		_ = lock.Rollback()
		<-done
	}()
	waiting := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%UPDATE box.watch_root_registrations%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("report did not reach the locked registration")
	}
	assertStatus("reported")
	var visibleHash string
	if err := db.QueryRow(`SELECT config_hash FROM watched_roots.roots WHERE node_id=$1 AND root_key=$2`, owner, root).Scan(&visibleHash); err != nil || visibleHash != "current" {
		t.Fatalf("uncommitted report visible: %s %v", visibleHash, err)
	}
	if err := lock.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertStatus("disabled")
	execSQL(`UPDATE box.watch_root_registrations SET activation_status='pending_agent_apply' WHERE box_id='box_report'`)
	report(input)
	assertStatus("reported")

	// A failed correlation must roll back the report replacement as well.
	execSQL(`CREATE FUNCTION box.reject_test_report() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture correlation refusal'; END $$`)
	execSQL(`CREATE TRIGGER reject_report BEFORE UPDATE ON box.watch_root_registrations FOR EACH ROW EXECUTE FUNCTION box.reject_test_report()`)
	if _, err := service.Report(t.Context(), in); err == nil {
		t.Fatal("failed correlation was committed")
	}
	assertStatus("reported")
	var hash string
	if err := db.QueryRow(`SELECT config_hash FROM watched_roots.roots WHERE node_id=$1 AND root_key=$2`, owner, root).Scan(&hash); err != nil || hash != "current" {
		t.Fatalf("failed transaction replaced report: %s %v", hash, err)
	}
	execSQL(`DROP TRIGGER reject_report ON box.watch_root_registrations`)
	execSQL(`DELETE FROM box.watch_root_registrations WHERE box_id='box_report'`)
	report(input)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM box.watch_root_registrations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("removed registration resurrected: %d %v", count, err)
	}
}

func TestNormalizeReportInputDefaults(t *testing.T) {
	input, err := normalizeReportInput(ReportInput{
		RootKey:     "notes",
		ConfigJSON:  json.RawMessage(`{"sync":"selected_files"}`),
		SummaryJSON: json.RawMessage(`{"included":1}`),
		Findings: []FindingInput{{
			Kind:    "root_unavailable",
			Summary: "root is unavailable",
		}},
	})
	if err != nil {
		t.Fatalf("normalizeReportInput returned error: %v", err)
	}
	if input.WorkerKey != "node-agent.watched_root.notes" {
		t.Fatalf("worker key = %q", input.WorkerKey)
	}
	if input.Status != StatusUnknown {
		t.Fatalf("status = %q, want %q", input.Status, StatusUnknown)
	}
	if input.Findings[0].FindingKey == "" || input.Findings[0].Severity != FindingSeverityWarning || input.Findings[0].Status != FindingStatusOpen {
		t.Fatalf("unexpected finding defaults %#v", input.Findings[0])
	}
}

func TestNormalizeReportInputRejectsMissingRoot(t *testing.T) {
	_, err := normalizeReportInput(ReportInput{})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeReportInputRejectsInvalidJSON(t *testing.T) {
	_, err := normalizeReportInput(ReportInput{
		RootKey:     "notes",
		SummaryJSON: json.RawMessage(`{`),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestResolveMissingFindingsTxResolvesOnlyUnreportedOpenFindings(t *testing.T) {
	t.Parallel()
	store := &resolveMissingFindingStore{openKeys: []string{"current", "stale"}}
	db := sql.OpenDB(resolveMissingFindingConnector{store: store})
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}
	count, err := resolveMissingFindingsTx(context.Background(), tx, "watched_root_test", map[string]struct{}{"current": {}})
	if err != nil {
		t.Fatalf("resolveMissingFindingsTx failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("resolved count = %d, want 1", count)
	}
	if len(store.resolvedKeys) != 1 || store.resolvedKeys[0] != "stale" {
		t.Fatalf("resolved keys = %#v, want stale only", store.resolvedKeys)
	}
	if !strings.Contains(store.selectQuery, "status = 'open'") || !strings.Contains(store.updateQuery, "status = 'open'") {
		t.Fatalf("resolution queries should be scoped to open findings: select=%q update=%q", store.selectQuery, store.updateQuery)
	}
}

func TestNormalizeBackupBatchInputDefaults(t *testing.T) {
	input, err := normalizeBackupBatchInput(BackupBatchInput{
		RootKey: "notes",
		Items: []BackupBatchItemInput{{
			ItemKind:       BackupItemKindFile,
			RelativePath:   "Project.md",
			ContentHashURI: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
	})
	if err != nil {
		t.Fatalf("normalizeBackupBatchInput returned error: %v", err)
	}
	if input.WorkerKey != "node-agent.watched_root.notes" {
		t.Fatalf("worker key = %q", input.WorkerKey)
	}
	if input.BatchKind != BackupBatchKindWatchedRoot || input.BackupMode != "incremental_raw" {
		t.Fatalf("unexpected batch defaults %#v", input)
	}
	if input.Items[0].Status != BackupItemStatusAccepted || input.Items[0].LocalItemRef == "" {
		t.Fatalf("unexpected item defaults %#v", input.Items[0])
	}
}

func TestNormalizeBackupBatchInputRejectsNoncanonicalBatchKind(t *testing.T) {
	for _, batchKind := range []string{BackupItemKindFile, BackupItemKindDirectory, BackupItemKindDeletionMarker, "arbitrary"} {
		t.Run(batchKind, func(t *testing.T) {
			_, err := normalizeBackupBatchInput(BackupBatchInput{
				RootKey:   "notes",
				BatchKind: batchKind,
				Items: []BackupBatchItemInput{{
					ItemKind:     BackupItemKindDirectory,
					RelativePath: "Empty",
				}},
			})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestNormalizeBackupBatchInputRequiresCoherentMetadataOnlyEvidence(t *testing.T) {
	modifiedAt := time.Date(2026, 8, 28, 12, 33, 0, 0, time.UTC)
	valid := BackupBatchInput{
		RootKey:  "documents",
		Metadata: json.RawMessage(`{"local_batch_id":"local_backup_batch_test"}`),
		Items: []BackupBatchItemInput{{
			LocalItemRef:   "local_backup_item_test",
			ItemKind:       BackupItemKindDirectory,
			RelativePath:   ".loom-acceptance",
			ContentHashURI: "sha256:d98bbba89552ada7e53b891b230fc26bea2e2b272e009b580c40fe649336c855",
			SizeBytes:      160,
			ModifiedAt:     &modifiedAt,
			Metadata:       json.RawMessage(`{"source":"loom-node-agent","local_batch_id":"local_backup_batch_test","local_item_id":"local_backup_item_test","filesystem_observation":{"kind":"directory","logical_size_bytes":160}}`),
		}},
	}
	if _, err := normalizeBackupBatchInput(valid); err != nil {
		t.Fatalf("valid metadata-only input rejected: %v", err)
	}
	tests := map[string]func(*BackupBatchInput){
		"missing local batch": func(input *BackupBatchInput) { input.Metadata = json.RawMessage(`{}`) },
		"wrong observation": func(input *BackupBatchInput) {
			input.Items[0].Metadata = json.RawMessage(`{"source":"loom-node-agent","local_batch_id":"local_backup_batch_test","local_item_id":"local_backup_item_test","filesystem_observation":{"kind":"regular_file","logical_size_bytes":160}}`)
		},
		"unexpected artifact": func(input *BackupBatchInput) {
			input.Items[0].ArtifactKind = BackupArtifactKindFileTransfer
			input.Items[0].ArtifactRef = "file_transfer_unexpected"
		},
		"path escape": func(input *BackupBatchInput) { input.Items[0].RelativePath = "../external" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := valid
			input.Items = append([]BackupBatchItemInput(nil), valid.Items...)
			mutate(&input)
			if _, err := normalizeBackupBatchInput(input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

type resolveMissingFindingStore struct {
	openKeys     []string
	selectQuery  string
	updateQuery  string
	resolvedKeys []string
	rowsClosed   bool
}

type resolveMissingFindingConnector struct {
	store *resolveMissingFindingStore
}

func (c resolveMissingFindingConnector) Connect(context.Context) (driver.Conn, error) {
	return resolveMissingFindingConn{store: c.store}, nil
}

func (c resolveMissingFindingConnector) Driver() driver.Driver {
	return resolveMissingFindingDriver{}
}

type resolveMissingFindingDriver struct{}

func (resolveMissingFindingDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with resolveMissingFindingConnector")
}

type resolveMissingFindingConn struct {
	store *resolveMissingFindingStore
}

func (c resolveMissingFindingConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported in this test")
}

func (c resolveMissingFindingConn) Close() error {
	return nil
}

func (c resolveMissingFindingConn) Begin() (driver.Tx, error) {
	return resolveMissingFindingTx{}, nil
}

func (c resolveMissingFindingConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return resolveMissingFindingTx{}, nil
}

func (c resolveMissingFindingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.store.selectQuery = query
	return &resolveMissingFindingRows{store: c.store, keys: c.store.openKeys}, nil
}

func (c resolveMissingFindingConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !c.store.rowsClosed {
		return nil, errors.New("exec attempted before rows were closed")
	}
	c.store.updateQuery = query
	if len(args) >= 2 {
		if key, ok := args[1].Value.(string); ok {
			c.store.resolvedKeys = append(c.store.resolvedKeys, key)
		}
	}
	return driver.RowsAffected(1), nil
}

type resolveMissingFindingTx struct{}

func (resolveMissingFindingTx) Commit() error {
	return nil
}

func (resolveMissingFindingTx) Rollback() error {
	return nil
}

type resolveMissingFindingRows struct {
	store *resolveMissingFindingStore
	keys  []string
	idx   int
}

func (r *resolveMissingFindingRows) Columns() []string {
	return []string{"finding_key"}
}

func (r *resolveMissingFindingRows) Close() error {
	r.store.rowsClosed = true
	return nil
}

func (r *resolveMissingFindingRows) Next(dest []driver.Value) error {
	if r.idx >= len(r.keys) {
		return io.EOF
	}
	dest[0] = r.keys[r.idx]
	r.idx++
	return nil
}

func TestNormalizeBackupBatchInputRejectsMissingItems(t *testing.T) {
	_, err := normalizeBackupBatchInput(BackupBatchInput{RootKey: "notes"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestValidatePrivateBackupArtifactBindsBatchAndItemEvidence(t *testing.T) {
	t.Parallel()
	backupRoot := filepath.Join(t.TempDir(), "backups")
	node := nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"}
	content := []byte("private source payload")
	contentHash := sha256.Sum256(content)
	item := BackupBatchItemInput{
		LocalItemRef:             "local_backup_item_test",
		ItemKind:                 BackupItemKindFile,
		Status:                   BackupItemStatusAccepted,
		BackupMode:               "incremental_raw",
		RelativePath:             "notes/entry.md",
		ContentHashURI:           "sha256:" + hex.EncodeToString(contentHash[:]),
		SizeBytes:                int64(len(content)),
		ArtifactKind:             BackupArtifactKindPrivateBackupOperation,
		ArtifactRef:              "private_backup_test",
		PrivateBackupOperationID: "private_backup_test",
	}
	metadata := func(rootKey, batchID, relativePath, contentHash, payloadHash string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"backup_kind":"watched_root_file","source_node_key":"workspace-test","root_key":%q,"local_batch_id":%q,"custody_batch_key":%q,"relative_path":%q,"content_hash_uri":%q,"backup_mode":"incremental_raw","local_item_id":%q,"payload_sha256":%q}`,
			rootKey, batchID, batchID, relativePath, contentHash, item.LocalItemRef, payloadHash))
	}
	payloadPath := filepath.Join(backupRoot, node.NodeKey, "notes", "batch-a", "artifacts", item.PrivateBackupOperationID, "payload.tar")
	artifactPayload := watchedRootArtifactTarForTest(t, "notes", "batch-a", item, content)
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, artifactPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	payloadHash := sha256.Sum256(artifactPayload)
	evidence := privateBackupArtifactEvidence{
		OperationID:  item.PrivateBackupOperationID,
		OriginNodeID: node.NodeID,
		Status:       "stored",
		SizeBytes:    int64(len(artifactPayload)),
		StorageRef:   payloadPath,
		Metadata:     metadata("notes", "batch-a", item.RelativePath, item.ContentHashURI, hex.EncodeToString(payloadHash[:])),
	}
	if err := validatePrivateBackupArtifact(backupRoot, "notes", "batch-a", item, node, evidence); err != nil {
		t.Fatalf("matching evidence rejected: %v", err)
	}
	wrongArtifact := item
	wrongArtifact.ArtifactRef = "private_backup_other"
	if err := validatePrivateBackupArtifact(backupRoot, "notes", "batch-a", wrongArtifact, node, evidence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inconsistent artifact identity error = %v, want ErrInvalid", err)
	}
	tests := []struct {
		name   string
		mutate func(*privateBackupArtifactEvidence)
	}{
		{name: "cross batch", mutate: func(got *privateBackupArtifactEvidence) {
			got.Metadata = metadata("notes", "batch-b", item.RelativePath, item.ContentHashURI, hex.EncodeToString(payloadHash[:]))
		}},
		{name: "cross root", mutate: func(got *privateBackupArtifactEvidence) {
			got.Metadata = metadata("projects", "batch-a", item.RelativePath, item.ContentHashURI, hex.EncodeToString(payloadHash[:]))
		}},
		{name: "cross path", mutate: func(got *privateBackupArtifactEvidence) {
			got.Metadata = metadata("notes", "batch-a", "other.md", item.ContentHashURI, hex.EncodeToString(payloadHash[:]))
		}},
		{name: "cross content", mutate: func(got *privateBackupArtifactEvidence) {
			got.Metadata = metadata("notes", "batch-a", item.RelativePath, "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", hex.EncodeToString(payloadHash[:]))
		}},
		{name: "wrong custody path", mutate: func(got *privateBackupArtifactEvidence) {
			got.StorageRef = filepath.Join(backupRoot, node.NodeKey, "notes", "batch-b", "payload.tar")
		}},
		{name: "not stored", mutate: func(got *privateBackupArtifactEvidence) { got.Status = "received" }},
		{name: "transport size mismatch", mutate: func(got *privateBackupArtifactEvidence) { got.SizeBytes++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := evidence
			test.mutate(&got)
			if err := validatePrivateBackupArtifact(backupRoot, "notes", "batch-a", item, node, got); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func watchedRootArtifactTarForTest(t *testing.T, rootKey, batchID string, item BackupBatchItemInput, content []byte) []byte {
	t.Helper()
	contentHash := sha256.Sum256(content)
	manifest, err := json.Marshal(map[string]any{
		"schema_version": "watched_root.backup_artifact.v0.2", "root_key": rootKey,
		"relative_path": item.RelativePath, "content_hash_uri": "sha256:" + hex.EncodeToString(contentHash[:]),
		"size_bytes": len(content), "backup_mode": item.BackupMode, "local_batch_id": batchID,
		"local_item_id": item.LocalItemRef,
	})
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, member := range []struct {
		name    string
		payload []byte
	}{{"manifest.json", manifest}, {"content", content}} {
		if err := writer.WriteHeader(&tar.Header{Name: member.name, Mode: 0o600, Size: int64(len(member.payload)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestValidateFileTransferArtifactBindsBatchAndItemEvidence(t *testing.T) {
	t.Parallel()
	backupRoot := filepath.Join(t.TempDir(), "backups")
	node := nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"}
	content := []byte("physical file transfer payload")
	contentHash := sha256.Sum256(content)
	item := BackupBatchItemInput{
		LocalItemRef:   "local_backup_item_test",
		ItemKind:       BackupItemKindFile,
		Status:         BackupItemStatusAccepted,
		BackupMode:     "incremental_raw",
		RelativePath:   "notes/entry.md",
		ContentHashURI: "sha256:" + hex.EncodeToString(contentHash[:]),
		SizeBytes:      int64(len(content)),
		ArtifactKind:   BackupArtifactKindFileTransfer,
		ArtifactRef:    "file_transfer_test",
	}
	acceptedRelative := filepath.Join(node.NodeKey, "notes", "batch-a", "payload", filepath.FromSlash(item.RelativePath))
	acceptedPath := filepath.Join(backupRoot, acceptedRelative)
	if err := os.MkdirAll(filepath.Dir(acceptedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(acceptedPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := func(batchID, localItemID string) json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"backup_kind":"watched_root_file","root_key":"notes","relative_path":"notes/entry.md","content_hash_uri":%q,"backup_mode":"incremental_raw","local_batch_id":%q,"local_item_id":%q}`,
			item.ContentHashURI, batchID, localItemID))
	}
	evidence := fileTransferArtifactEvidence{
		SourceNodeID:           sql.NullString{String: node.NodeID, Valid: true},
		SourceNodeKey:          node.NodeKey,
		SourceRootKey:          "notes",
		SourceRelativePath:     item.RelativePath,
		DestinationLogicalPath: item.RelativePath,
		TransferKind:           filetransfer.KindWatchedRootBackup,
		CustodyMode:            filetransfer.CustodyModeBackupCopy,
		Status:                 filetransfer.StatusAccepted,
		ChecksumAlgorithm:      "sha256",
		ChecksumHex:            strings.TrimPrefix(item.ContentHashURI, "sha256:"),
		FileSizeBytes:          item.SizeBytes,
		AcceptedPath:           filepath.ToSlash(acceptedRelative),
		Metadata:               metadata("batch-a", item.LocalItemRef),
	}
	if err := validateFileTransferArtifact(backupRoot, "notes", "batch-a", item, node, evidence); err != nil {
		t.Fatalf("matching evidence rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*fileTransferArtifactEvidence)
	}{
		{name: "cross batch metadata", mutate: func(got *fileTransferArtifactEvidence) {
			got.Metadata = metadata("batch-b", item.LocalItemRef)
		}},
		{name: "cross batch accepted path", mutate: func(got *fileTransferArtifactEvidence) {
			got.AcceptedPath = "workspace-test/notes/batch-b/payload/notes/entry.md"
		}},
		{name: "cross root", mutate: func(got *fileTransferArtifactEvidence) { got.SourceRootKey = "projects" }},
		{name: "cross path", mutate: func(got *fileTransferArtifactEvidence) { got.DestinationLogicalPath = "other.md" }},
		{name: "cross content", mutate: func(got *fileTransferArtifactEvidence) { got.ChecksumHex = strings.Repeat("c", 64) }},
		{name: "missing node identity", mutate: func(got *fileTransferArtifactEvidence) { got.SourceNodeID = sql.NullString{} }},
		{name: "cross item metadata", mutate: func(got *fileTransferArtifactEvidence) {
			got.Metadata = metadata("batch-a", "other_item")
		}},
		{name: "not accepted", mutate: func(got *fileTransferArtifactEvidence) { got.Status = filetransfer.StatusUploading }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := evidence
			test.mutate(&got)
			if err := validateFileTransferArtifact(backupRoot, "notes", "batch-a", item, node, got); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
	corruptedContent := append([]byte(nil), content...)
	corruptedContent[0] ^= 0xff
	if err := os.WriteFile(acceptedPath, corruptedContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateFileTransferArtifact(backupRoot, "notes", "batch-a", item, node, evidence); !errors.Is(err, ErrInvalid) {
		t.Fatalf("physical checksum mismatch error = %v, want ErrInvalid", err)
	}
	if err := os.Remove(acceptedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), acceptedPath); err == nil {
		if err := validateFileTransferArtifact(backupRoot, "notes", "batch-a", item, node, evidence); !errors.Is(err, ErrInvalid) {
			t.Fatalf("physical symlink error = %v, want ErrInvalid", err)
		}
	}
}

func TestVerifyBackupArtifactsRevalidatesPhysicalCustodyOnEveryRetry(t *testing.T) {
	t.Run("file transfer", func(t *testing.T) {
		backupRoot := filepath.Join(t.TempDir(), "backups")
		node := nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"}
		content := []byte("file transfer retry payload")
		contentHash := sha256.Sum256(content)
		item := BackupBatchItemInput{
			LocalItemRef: "local_item", ItemKind: BackupItemKindFile, Status: BackupItemStatusAccepted,
			BackupMode: "incremental_raw", RelativePath: "notes/retry.md",
			ContentHashURI: "sha256:" + hex.EncodeToString(contentHash[:]), SizeBytes: int64(len(content)),
			ArtifactKind: BackupArtifactKindFileTransfer, ArtifactRef: "file_transfer_retry",
		}
		acceptedRelative := filepath.Join(node.NodeKey, "notes", "batch-retry", "payload", filepath.FromSlash(item.RelativePath))
		acceptedPath := filepath.Join(backupRoot, acceptedRelative)
		if err := os.MkdirAll(filepath.Dir(acceptedPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(acceptedPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		metadata := json.RawMessage(fmt.Sprintf(`{"backup_kind":"watched_root_file","root_key":"notes","relative_path":%q,"content_hash_uri":%q,"backup_mode":"incremental_raw","local_batch_id":"batch-retry","local_item_id":"local_item"}`,
			item.RelativePath, item.ContentHashURI))
		store := &backupArtifactEvidenceStore{fileTransfer: fileTransferArtifactEvidence{
			SourceNodeID: sql.NullString{String: node.NodeID, Valid: true}, SourceNodeKey: node.NodeKey,
			SourceRootKey: "notes", SourceRelativePath: item.RelativePath, DestinationLogicalPath: item.RelativePath,
			TransferKind: filetransfer.KindWatchedRootBackup, CustodyMode: filetransfer.CustodyModeBackupCopy,
			Status: filetransfer.StatusAccepted, ChecksumAlgorithm: filetransfer.ChecksumSHA256,
			ChecksumHex: strings.TrimPrefix(item.ContentHashURI, "sha256:"), FileSizeBytes: item.SizeBytes,
			AcceptedPath: filepath.ToSlash(acceptedRelative), Metadata: metadata,
		}}
		db := sql.OpenDB(backupArtifactEvidenceConnector{store: store})
		defer db.Close()
		stored := BackupItem{
			LocalItemRef: item.LocalItemRef, ItemKind: item.ItemKind, Status: item.Status,
			BackupMode: item.BackupMode, RelativePath: item.RelativePath, ContentHashURI: item.ContentHashURI,
			SizeBytes: item.SizeBytes, ArtifactKind: item.ArtifactKind, ArtifactRef: item.ArtifactRef,
		}
		if err := verifyBackupArtifacts(context.Background(), db, backupRoot, "notes", "batch-retry", backupInputsFromStoredItems([]BackupItem{stored}), node); err != nil {
			t.Fatalf("first verification: %v", err)
		}
		tamperedContent := append([]byte(nil), content...)
		tamperedContent[len(tamperedContent)-1] ^= 0xff
		if err := os.WriteFile(acceptedPath, tamperedContent, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyBackupArtifacts(context.Background(), db, backupRoot, "notes", "batch-retry", backupInputsFromStoredItems([]BackupItem{stored}), node); !errors.Is(err, ErrInvalid) {
			t.Fatalf("retry accepted tampered physical payload: %v", err)
		}
		if store.fileTransferQueries != 2 {
			t.Fatalf("file-transfer evidence queries = %d, want 2", store.fileTransferQueries)
		}
	})

	t.Run("private backup", func(t *testing.T) {
		backupRoot := filepath.Join(t.TempDir(), "backups")
		node := nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"}
		content := []byte("private retry payload")
		contentHash := sha256.Sum256(content)
		operationID := "private_backup_retry"
		item := BackupBatchItemInput{
			LocalItemRef: "local_item", ItemKind: BackupItemKindFile, Status: BackupItemStatusAccepted,
			BackupMode: "incremental_raw", RelativePath: "notes/retry.md",
			ContentHashURI: "sha256:" + hex.EncodeToString(contentHash[:]), SizeBytes: int64(len(content)),
			ArtifactKind: BackupArtifactKindPrivateBackupOperation, ArtifactRef: operationID,
			PrivateBackupOperationID: operationID,
		}
		payload := watchedRootArtifactTarForTest(t, "notes", "batch-retry", item, content)
		payloadHash := sha256.Sum256(payload)
		payloadPath := filepath.Join(backupRoot, node.NodeKey, "notes", "batch-retry", "artifacts", operationID, "payload.tar")
		if err := os.MkdirAll(filepath.Dir(payloadPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		metadata := json.RawMessage(fmt.Sprintf(`{"backup_kind":"watched_root_file","source_node_key":%q,"root_key":"notes","local_batch_id":"batch-retry","custody_batch_key":"batch-retry","relative_path":%q,"content_hash_uri":%q,"backup_mode":"incremental_raw","local_item_id":"local_item","payload_sha256":%q}`,
			node.NodeKey, item.RelativePath, item.ContentHashURI, hex.EncodeToString(payloadHash[:])))
		store := &backupArtifactEvidenceStore{privateBackup: privateBackupArtifactEvidence{
			OperationID: operationID, OriginNodeID: node.NodeID, Status: "stored", SizeBytes: int64(len(payload)),
			StorageRef: payloadPath, Metadata: metadata,
		}}
		db := sql.OpenDB(backupArtifactEvidenceConnector{store: store})
		defer db.Close()
		storedOperationID := operationID
		stored := BackupItem{
			LocalItemRef: item.LocalItemRef, ItemKind: item.ItemKind, Status: item.Status,
			BackupMode: item.BackupMode, RelativePath: item.RelativePath, ContentHashURI: item.ContentHashURI,
			SizeBytes: item.SizeBytes, ArtifactKind: item.ArtifactKind, ArtifactRef: item.ArtifactRef,
			PrivateBackupOperationID: &storedOperationID,
		}
		if err := verifyBackupArtifacts(context.Background(), db, backupRoot, "notes", "batch-retry", backupInputsFromStoredItems([]BackupItem{stored}), node); err != nil {
			t.Fatalf("first verification: %v", err)
		}
		if err := os.WriteFile(payloadPath, []byte("tampered private artifact"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verifyBackupArtifacts(context.Background(), db, backupRoot, "notes", "batch-retry", backupInputsFromStoredItems([]BackupItem{stored}), node); !errors.Is(err, ErrInvalid) {
			t.Fatalf("retry accepted tampered private payload: %v", err)
		}
		if store.privateBackupQueries != 2 {
			t.Fatalf("private-backup evidence queries = %d, want 2", store.privateBackupQueries)
		}
	})
}

type backupArtifactEvidenceStore struct {
	privateBackup        privateBackupArtifactEvidence
	fileTransfer         fileTransferArtifactEvidence
	privateBackupQueries int
	fileTransferQueries  int
}

type backupArtifactEvidenceConnector struct{ store *backupArtifactEvidenceStore }

func (c backupArtifactEvidenceConnector) Connect(context.Context) (driver.Conn, error) {
	return backupArtifactEvidenceConn{store: c.store}, nil
}

func (c backupArtifactEvidenceConnector) Driver() driver.Driver {
	return backupArtifactEvidenceDriver{}
}

type backupArtifactEvidenceDriver struct{}

func (backupArtifactEvidenceDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with backupArtifactEvidenceConnector")
}

type backupArtifactEvidenceConn struct{ store *backupArtifactEvidenceStore }

func (c backupArtifactEvidenceConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported in this test")
}

func (c backupArtifactEvidenceConn) Close() error { return nil }

func (c backupArtifactEvidenceConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported in this test")
}

func (c backupArtifactEvidenceConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	switch {
	case strings.Contains(query, "sync.private_backup_operations"):
		c.store.privateBackupQueries++
		evidence := c.store.privateBackup
		return &backupArtifactEvidenceRows{
			columns: []string{"origin_node_id", "status", "coarse_size_bytes", "storage_ref", "metadata"},
			values:  []driver.Value{evidence.OriginNodeID, evidence.Status, evidence.SizeBytes, evidence.StorageRef, []byte(evidence.Metadata)},
		}, nil
	case strings.Contains(query, "storage.file_transfers"):
		c.store.fileTransferQueries++
		evidence := c.store.fileTransfer
		var sourceNodeID driver.Value
		if evidence.SourceNodeID.Valid {
			sourceNodeID = evidence.SourceNodeID.String
		}
		return &backupArtifactEvidenceRows{
			columns: []string{"source_node_id", "source_node_key", "source_root_key", "source_relative_path", "destination_logical_path", "transfer_kind", "custody_mode", "status", "checksum_algorithm", "checksum_hex", "file_size_bytes", "accepted_path", "metadata"},
			values: []driver.Value{sourceNodeID, evidence.SourceNodeKey, evidence.SourceRootKey, evidence.SourceRelativePath,
				evidence.DestinationLogicalPath, evidence.TransferKind, evidence.CustodyMode, evidence.Status,
				evidence.ChecksumAlgorithm, evidence.ChecksumHex, evidence.FileSizeBytes, evidence.AcceptedPath, []byte(evidence.Metadata)},
		}, nil
	default:
		return nil, fmt.Errorf("unexpected artifact evidence query: %s", query)
	}
}

type backupArtifactEvidenceRows struct {
	columns []string
	values  []driver.Value
	read    bool
}

func (r *backupArtifactEvidenceRows) Columns() []string { return r.columns }
func (r *backupArtifactEvidenceRows) Close() error      { return nil }
func (r *backupArtifactEvidenceRows) Next(dest []driver.Value) error {
	if r.read {
		return io.EOF
	}
	copy(dest, r.values)
	r.read = true
	return nil
}

func TestBackupCurrentViewPathUsesPhysicalCustodyWithoutGeneratedNodeView(t *testing.T) {
	node := nodes.Node{NodeKey: "macbook"}
	batch := BackupBatch{
		WatchedRootBackupBatchID: "watched_root_backup_batch_main",
		Metadata:                 json.RawMessage(`{"local_batch_id":"local_backup_batch_test"}`),
	}
	item := BackupItem{
		WatchedRootBackupItemID: "watched_root_backup_item_01JTESTBACKUPITEM000000000",
		ItemKind:                BackupItemKindFile,
		Status:                  BackupItemStatusAccepted,
		RelativePath:            "neuroscience/synaptic.md",
	}

	notesPath := backupCurrentViewPath(node.NodeKey, WatchedRoot{RootKey: "loom_box__notes"}, batch, item, backupStorageContext{SourceArea: storagecatalog.SourceAreaNotes}, backupEntryCurrent)
	if notesPath != "backups/macbook/loom_box__notes/local_backup_batch_test/payload/neuroscience/synaptic.md" {
		t.Fatalf("notes view path = %q", notesPath)
	}
	if strings.Contains(notesPath, "macbook/Backups") || strings.Contains(notesPath, "/current/") {
		t.Fatalf("generated backup view leaked into canonical custody path: %q", notesPath)
	}
	if got := backupCurrentViewPath(node.NodeKey, WatchedRoot{RootKey: "notes"}, batch, item, backupStorageContext{}, backupEntryTombstone); got != "" {
		t.Fatalf("tombstone advertised a payload path: %q", got)
	}
	if got := backupCurrentViewPath(node.NodeKey, WatchedRoot{RootKey: "notes"}, batch, item, backupStorageContext{}, backupEntryDiagnostic); got != "" {
		t.Fatalf("diagnostic advertised a payload path: %q", got)
	}
}

func TestBackupCurrentViewPathUsesPrivateArtifactPayload(t *testing.T) {
	operationID := "private_backup_operation_test"
	got := backupCurrentViewPath("workspace", WatchedRoot{RootKey: "notes"}, BackupBatch{
		WatchedRootBackupBatchID: "main_batch",
		Metadata:                 json.RawMessage(`{"local_batch_id":"local_batch"}`),
	}, BackupItem{
		ArtifactKind:             BackupArtifactKindPrivateBackupOperation,
		PrivateBackupOperationID: &operationID,
		RelativePath:             "note.md",
	}, backupStorageContext{}, backupEntryCurrent)
	if got != "backups/workspace/notes/local_batch/artifacts/private_backup_operation_test/payload.tar" {
		t.Fatalf("private artifact path = %q", got)
	}
}

func TestWriteBackupCustodyManifestIsInspectableAndIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "storage", "backups")
	node := nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"}
	watchedRoot := WatchedRoot{WatchedRootID: "watched_root_test", RootKey: "notes"}
	batch := BackupBatch{
		WatchedRootBackupBatchID: "watched_root_backup_batch_test",
		BatchKind:                "watched_root_backup",
		BackupMode:               "incremental_raw",
		Status:                   BackupBatchStatusAccepted,
		ReceivedAt:               time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		Metadata:                 json.RawMessage(`{"local_batch_id":"local_backup_batch_test"}`),
	}
	items := []BackupItem{{
		WatchedRootBackupItemID: "watched_root_backup_item_test",
		ItemKind:                BackupItemKindDeletionMarker,
		RelativePath:            "removed.md",
		PreviousHashURI:         "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Status:                  BackupItemStatusAccepted,
	}}
	if err := writeBackupCustodyManifest(root, node, watchedRoot, batch, items); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := writeBackupCustodyManifest(root, node, watchedRoot, batch, items); err != nil {
		t.Fatalf("idempotent manifest rewrite: %v", err)
	}
	manifestPath := filepath.Join(root, node.NodeKey, watchedRoot.RootKey, "local_backup_batch_test", "manifest.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest backupCustodyManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.LocalBatchID != "local_backup_batch_test" || len(manifest.Items) != 1 || manifest.Items[0].PreviousHashURI == "" {
		t.Fatalf("manifest lost batch/deletion evidence: %#v", manifest)
	}
	changed := append([]BackupItem(nil), items...)
	changed[0].PreviousHashURI = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := writeBackupCustodyManifest(root, node, watchedRoot, batch, changed); err == nil || !strings.Contains(err.Error(), "different evidence") {
		t.Fatalf("changed manifest error = %v", err)
	}
}

func TestWriteBackupCustodyManifestRejectsSymlinkedComponent(t *testing.T) {
	temp := t.TempDir()
	root := filepath.Join(temp, "backups")
	external := filepath.Join(temp, "external")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatalf("mkdir external: %v", err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	if err := os.Symlink(external, filepath.Join(root, "workspace-test")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := writeBackupCustodyManifest(root,
		nodes.Node{NodeID: "node_test", NodeKey: "workspace-test"},
		WatchedRoot{RootKey: "notes"},
		BackupBatch{WatchedRootBackupBatchID: "main_batch", Metadata: json.RawMessage(`{"local_batch_id":"local_batch"}`)},
		nil,
	)
	if err == nil {
		t.Fatalf("symlinked component error = %v", err)
	}
	payload, readErr := os.ReadFile(sentinel)
	if readErr != nil || string(payload) != "safe" {
		t.Fatalf("external sentinel changed: %q err=%v", payload, readErr)
	}
}

func TestBackupCatalogStatesForDiagnosticsAndTombstones(t *testing.T) {
	processing, availability := backupCatalogStates(BackupItem{Status: BackupItemStatusFailed}, backupEntryDiagnostic)
	if processing != storagecatalog.ProcessingStateFailed || availability != storagecatalog.AvailabilityStateFailed {
		t.Fatalf("failed diagnostic states = %s/%s", processing, availability)
	}
	processing, availability = backupCatalogStates(BackupItem{Status: BackupItemStatusSkipped}, backupEntryDiagnostic)
	if processing != storagecatalog.ProcessingStateExcluded || availability != storagecatalog.AvailabilityStateDiscovered {
		t.Fatalf("skipped diagnostic states = %s/%s", processing, availability)
	}
	processing, availability = backupCatalogStates(BackupItem{ItemKind: BackupItemKindDeletionMarker}, backupEntryTombstone)
	if processing != storagecatalog.ProcessingStateMetadataOnly || availability != storagecatalog.AvailabilityStateTombstoned {
		t.Fatalf("tombstone states = %s/%s", processing, availability)
	}
}

func TestBackupItemFileClassUsesDirectoryItemKindAndObservation(t *testing.T) {
	directory := BackupItem{ItemKind: BackupItemKindDirectory, RelativePath: "Empty"}
	if got := backupItemFileClass(directory, directory.RelativePath); got != storagecatalog.FileClassDirectory {
		t.Fatalf("directory file class = %q", got)
	}

	metadata, err := json.Marshal(map[string]any{
		"filesystem_observation": map[string]any{
			"kind":       "directory",
			"is_package": false,
		},
	})
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	observedDirectory := BackupItem{ItemKind: BackupItemKindMetadata, RelativePath: "Observed", Metadata: metadata}
	if got := backupItemFileClass(observedDirectory, observedDirectory.RelativePath); got != storagecatalog.FileClassDirectory {
		t.Fatalf("observed directory file class = %q", got)
	}
}
