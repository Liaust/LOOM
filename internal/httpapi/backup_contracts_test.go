package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/response"
)

func TestBackupContractsEndpointListsAndInspectsBoxContracts(t *testing.T) {
	boxRoot := initializedBackupContractBox(t)
	_, err := backupcontracts.Create(backupcontracts.MutateInput{
		BoxRoot:          boxRoot,
		DirectoryRelPath: backupcontracts.DefaultDirectoryRelPath,
		Contract: backupcontracts.Contract{
			Key:       "field-data",
			OwnerNode: "main",
			Target: backupcontracts.TargetSpec{
				Scope: backupcontracts.TargetScopeOwnerNodeAbsolute,
				Path:  filepath.Join(t.TempDir(), "field-data"),
			},
		},
		Actor: "test",
	})
	if err != nil {
		t.Fatalf("create backup contract fixture: %v", err)
	}

	server := backupContractTestServer(boxRoot)
	req := httptest.NewRequest(http.MethodGet, "/v1/backup/contracts", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var listEnvelope response.Envelope[backupcontracts.ListResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatalf("decode list response: %v body=%s", err, rec.Body.String())
	}
	if len(listEnvelope.Data.Contracts) != 1 || listEnvelope.Data.Contracts[0].Key != "field-data" {
		t.Fatalf("unexpected list response: %#v", listEnvelope.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts/field-data", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status = %d body=%s", rec.Code, rec.Body.String())
	}
	var recordEnvelope response.Envelope[backupcontracts.ContractRecord]
	if err := json.Unmarshal(rec.Body.Bytes(), &recordEnvelope); err != nil {
		t.Fatalf("decode inspect response: %v body=%s", err, rec.Body.String())
	}
	if recordEnvelope.Data.Key != "field-data" || recordEnvelope.Data.Contract == nil || recordEnvelope.Data.Contract.Status != backupcontracts.StatusActive {
		t.Fatalf("unexpected inspect response: %#v", recordEnvelope.Data)
	}
}

func TestBackupContractsCreateDryRunDoesNotWriteContractFile(t *testing.T) {
	boxRoot := initializedBackupContractBox(t)
	targetPath := filepath.Join(t.TempDir(), "field-data")
	body := bytes.NewBufferString(`{"key":"field-data","target_path":` + quoteJSON(targetPath) + `,"dry_run":true}`)

	server := backupContractTestServer(boxRoot)
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/contracts", body)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create dry-run status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope response.Envelope[backupcontracts.LifecycleResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode dry-run response: %v body=%s", err, rec.Body.String())
	}
	if envelope.Data.Action != "create" || !envelope.Data.DryRun || envelope.Data.Contract.Key != "field-data" {
		t.Fatalf("unexpected dry-run response: %#v", envelope.Data)
	}
	if envelope.Data.Contract.SchemaVersion != backupcontracts.SchemaVersion || envelope.Data.Contract.Ignore == nil || envelope.Data.Contract.Ignore.Profile != "managed" || !envelope.Data.Contract.Ignore.DiscoverUserRules || len(envelope.Data.Contract.Exclude) != 0 {
		t.Fatalf("create API did not return profile-reference schema: %#v", envelope.Data.Contract)
	}
	if len(envelope.Data.WatchedRoots) != 1 || envelope.Data.WatchedRoots[0].BackupMode == "" {
		t.Fatalf("dry-run should include planned watched root: %#v", envelope.Data.WatchedRoots)
	}
	if _, err := os.Stat(filepath.Join(boxRoot, ".loom", "contracts", "backup", "field-data.yaml")); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote contract file or stat failed: %v", err)
	}
}

func TestBackupContractsCreateDryRunPreservesRequestedOwnerNode(t *testing.T) {
	boxRoot := initializedBackupContractBox(t)
	targetPath := filepath.Join(t.TempDir(), "field-data")
	body := bytes.NewBufferString(`{"key":"field-data","owner_node":"macbook","target_path":` + quoteJSON(targetPath) + `,"dry_run":true}`)

	server := backupContractTestServer(boxRoot)
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/contracts", body)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create dry-run status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope response.Envelope[backupcontracts.LifecycleResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.WatchedRoots) != 1 || envelope.Data.WatchedRoots[0].OwnerNode != "macbook" {
		t.Fatalf("requested owner node was overwritten: %#v", envelope.Data)
	}
	if envelope.Data.Applied || envelope.Data.NodeApplied || envelope.Data.NodeApplyQueued {
		t.Fatalf("dry-run falsely claimed node apply: %#v", envelope.Data)
	}
}

func TestBackupContractsCreateDoesNotRepairInitializedBox(t *testing.T) {
	boxRoot := initializedBackupContractBox(t)
	notesDir := filepath.Join(boxRoot, "Notes")
	agentsPath := filepath.Join(notesDir, "AGENTS.md")
	if err := os.Remove(agentsPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(notesDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(notesDir, 0o755) })

	resolved := box.Resolved{RootPath: boxRoot, Profile: box.ProfileMain, OwnerNode: "main"}
	if err := prepareBoxForBackupContracts(resolved); err != nil {
		t.Fatalf("prepare initialized Box: %v", err)
	}
	if _, err := os.Stat(agentsPath); !os.IsNotExist(err) {
		t.Fatalf("contract creation repaired optional Box file: %v", err)
	}
}

func TestBackupContractIgnorePolicyMigrationDryRunUsesCompatibleAPI(t *testing.T) {
	boxRoot := initializedBackupContractBox(t)
	legacy := backupcontracts.Contract{
		SchemaVersion: backupcontracts.LegacySchemaVersion,
		Key:           "field-data",
		OwnerNode:     "main",
		Target: backupcontracts.TargetSpec{
			Scope: backupcontracts.TargetScopeOwnerNodeAbsolute,
			Path:  filepath.Join(t.TempDir(), "field-data"),
		},
		Exclude: backupcontracts.DefaultExcludePatterns(),
	}
	payload, err := backupcontracts.Render(legacy)
	if err != nil {
		t.Fatalf("render legacy contract: %v", err)
	}
	contractPath, err := backupcontracts.ContractFilePath(boxRoot, "", "field-data")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(contractPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(contractPath, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	server := backupContractTestServer(boxRoot)
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/contracts/migrate-ignore-policy", bytes.NewBufferString(`{"dry_run":true}`))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("migration dry-run status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope response.Envelope[backupcontracts.MigrateIgnorePolicyResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode migration response: %v", err)
	}
	if envelope.Data.Migration.ChangedCount != 1 || envelope.Data.Migration.Items[0].ToSchema != backupcontracts.SchemaVersion || envelope.Data.Reconciled {
		t.Fatalf("unexpected migration response: %#v", envelope.Data)
	}
	unchanged, err := os.ReadFile(contractPath)
	if err != nil || string(unchanged) != string(payload) {
		t.Fatalf("API dry run changed contract: err=%v", err)
	}
}

func TestBackupContractPreflightEndpointsRemainPendingForOfflineNodePostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	const nodeID = "node_pf_http"
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status, presence_state) VALUES ($1, 'preflight-http', 'Preflight HTTP', 'workstation', 'workspace', 'workspace', 'active', 'offline') ON CONFLICT (node_id) DO UPDATE SET status = 'active', presence_state = 'offline'`, nodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM backup.protected_folder_preflights WHERE target_node_id = $1 AND retry_of_preflight_id IS NOT NULL`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM backup.protected_folder_preflights WHERE target_node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.messages WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM nodes.nodes WHERE node_id = $1`, nodeID)
	})
	server := NewServer(Services{DB: db}, slog.Default()).Handler()
	body := bytes.NewBufferString(`{"node_ref":"preflight-http","path":"/Users/test/Documents","idempotency_key":"http-preflight-test"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/contracts/preflights", body)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created response.Envelope[backupcontracts.PreflightRecord]
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Status != backupcontracts.PreflightStatusPending {
		t.Fatalf("offline node did not remain pending: %#v", created.Data)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts/preflights/"+created.Data.PreflightID, nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("get status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/backup/contracts/preflights/"+created.Data.PreflightID+"/retry", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBackupContractMutationQueuesOwnerNodeReconciliationPostgres(t *testing.T) {
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	const nodeID = "node_pf_http_reconcile"
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status, presence_state) VALUES ($1, 'http-reconcile', 'HTTP Reconcile', 'workstation', 'workspace', 'workspace', 'active', 'offline') ON CONFLICT (node_id) DO UPDATE SET status = 'active'`, nodeID); err != nil {
		t.Fatal(err)
	}
	boxRoot := initializedBackupContractBox(t)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM box.watch_root_registrations WHERE box_root_path = $1`, boxRoot)
		_, _ = db.ExecContext(ctx, `DELETE FROM scopes.scopes WHERE metadata->>'box_root_path' = $1`, boxRoot)
		_, _ = db.ExecContext(ctx, `DELETE FROM backup.protected_folder_preflights WHERE target_node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.message_acks WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.messages WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM nodes.nodes WHERE node_id = $1`, nodeID)
	})
	server := NewServer(Services{DB: db, Box: box.NewService(db), RuntimeConfig: config.Config{BoxPath: boxRoot, BoxProfile: box.ProfileMain, NodeID: "main", NodeRole: "main"}}, slog.Default()).Handler()
	target := t.TempDir()
	sourcePath := filepath.Join(target, "source.txt")
	retainedBackupPath := filepath.Join(t.TempDir(), "retained-backup.bin")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedBackupPath, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"key":"http-folder","owner_node":"http-reconcile","target_path":` + quoteJSON(target) + `}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/backup/contracts", body)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created response.Envelope[backupcontracts.LifecycleResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Data.DesiredStateRegistered || !created.Data.NodeApplyQueued || created.Data.NodeApplied || created.Data.DesiredRevision != 1 {
		t.Fatalf("untruthful queued lifecycle %#v", created.Data)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts/http-folder", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", rec.Code, rec.Body.String())
	}
	var detail response.Envelope[backupcontracts.ProtectedFolderRecord]
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Lifecycle != backupcontracts.ProtectedFolderStatusWaitingForNode || detail.Data.NodeApplied {
		t.Fatalf("offline owner lifecycle is not truthful: %#v", detail.Data)
	}
	req = httptest.NewRequest(http.MethodPost, "/v1/backup/contracts/http-folder/recheck", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("recheck status=%d body=%s", rec.Code, rec.Body.String())
	}
	preflightMessages, err := communication.NewService(db).ListMessages(ctx, communication.MessageFilter{NodeRef: nodeID, Kind: communication.KindProtectedFolderPreflight, Limit: 10})
	if err != nil || len(preflightMessages) != 1 {
		t.Fatalf("preflight messages=%#v err=%v", preflightMessages, err)
	}
	recheckPayload, err := backupcontracts.DecodePreflightPayload(preflightMessages[0].PayloadJSON)
	wantIdentity := backupcontracts.RecheckIdentityForContractKey("http-folder")
	if err != nil || recheckPayload.Recheck == nil || *recheckPayload.Recheck != wantIdentity {
		t.Fatalf("recheck identity=%#v err=%v", recheckPayload.Recheck, err)
	}
	messages, err := communication.NewService(db).ListMessages(ctx, communication.MessageFilter{NodeRef: nodeID, Kind: communication.KindProtectedFolderReconcile, Limit: 10})
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/backup/contracts/http-folder/retry-activation", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	messages, err = communication.NewService(db).ListMessages(ctx, communication.MessageFilter{NodeRef: nodeID, Kind: communication.KindProtectedFolderReconcile, Limit: 10})
	if err != nil || len(messages) != 2 {
		t.Fatalf("retry messages=%#v err=%v", messages, err)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v1/backup/contracts/http-folder", bytes.NewBufferString(`{}`))
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	var deleted response.Envelope[backupcontracts.LifecycleResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Data.Contract.Key != "http-folder" || !deleted.Data.NodeApplyQueued {
		t.Fatalf("delete lifecycle=%#v", deleted.Data)
	}
	for _, path := range []string{sourcePath, retainedBackupPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("API deletion touched retained data %s: %v", path, err)
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var deleting response.Envelope[backupcontracts.ProtectedFolderListResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &deleting); err != nil {
		t.Fatal(err)
	}
	if len(deleting.Data.Folders) != 1 || !deleting.Data.Folders[0].Deleting || deleting.Data.Folders[0].MissingContract || deleting.Data.Folders[0].Lifecycle != backupcontracts.ProtectedFolderStatusWaitingForNode {
		t.Fatalf("queued API deletion=%#v", deleting.Data)
	}

	messages, err = communication.NewService(db).ListMessages(ctx, communication.MessageFilter{NodeRef: nodeID, Kind: communication.KindProtectedFolderReconcile, Limit: 10})
	if err != nil || len(messages) != 3 {
		t.Fatalf("delete messages=%#v err=%v", messages, err)
	}
	tombstoneMessage := messages[0]
	var tombstonePayload backupcontracts.ProtectedFolderReconcilePayload
	if err := json.Unmarshal(tombstoneMessage.PayloadJSON, &tombstonePayload); err != nil {
		t.Fatal(err)
	}
	if len(tombstonePayload.Roots) != 0 {
		t.Fatalf("delete payload retained roots: %#v", tombstonePayload)
	}
	processed := time.Now().UTC()
	ackPayload := backupcontracts.ProtectedFolderAck{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: communication.AppliedStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion, DesiredRevision: tombstonePayload.Evidence.DesiredRevision, AppliedRevision: tombstonePayload.Evidence.DesiredRevision, ConfigHash: tombstonePayload.Evidence.ConfigHash, Outcome: communication.ControlOutcomeCompleted}}
	ackJSON, _ := json.Marshal(ackPayload)
	if err := backupcontracts.NewReconcileService(db).ProjectAcknowledgement(ctx, communication.AckResult{Message: tombstoneMessage, Ack: communication.MessageAck{AckStatus: communication.AckStatusCompleted, ResultJSON: ackJSON, ProcessedAt: &processed}}); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var converged response.Envelope[backupcontracts.ProtectedFolderListResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &converged); err != nil {
		t.Fatal(err)
	}
	if len(converged.Data.Folders) != 0 || len(converged.Data.Contracts) != 0 || len(converged.Data.Problems) != 0 {
		t.Fatalf("completed API tombstone remained visible: %#v", converged.Data)
	}

	accidentalTarget := t.TempDir()
	body = bytes.NewBufferString(`{"key":"accidental-missing","owner_node":"http-reconcile","target_path":` + quoteJSON(accidentalTarget) + `}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/backup/contracts", body)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("accidental fixture create status=%d body=%s", rec.Code, rec.Body.String())
	}
	accidentalPath, err := backupcontracts.ContractFilePath(boxRoot, backupcontracts.DefaultDirectoryRelPath, "accidental-missing")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(accidentalPath); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/backup/contracts", nil)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	var accidental response.Envelope[backupcontracts.ProtectedFolderListResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &accidental); err != nil {
		t.Fatal(err)
	}
	if len(accidental.Data.Folders) != 1 || accidental.Data.Folders[0].Key != "accidental-missing" || !accidental.Data.Folders[0].MissingContract || accidental.Data.Folders[0].Deleting || accidental.Data.Folders[0].Lifecycle != backupcontracts.ProtectedFolderStatusAttention {
		t.Fatalf("accidental missing YAML was hidden: %#v", accidental.Data)
	}
}

func initializedBackupContractBox(t *testing.T) string {
	t.Helper()
	boxRoot := t.TempDir()
	resolved := box.Resolved{
		RootPath:  boxRoot,
		Profile:   box.ProfileMain,
		OwnerNode: "main",
		NodeRole:  "main",
	}
	if _, err := box.Init(box.InitInput{Resolved: resolved}); err != nil {
		t.Fatalf("initialize Box: %v", err)
	}
	return boxRoot
}

func backupContractTestServer(boxRoot string) http.Handler {
	return NewServer(Services{
		RuntimeConfig: config.Config{
			BoxPath:    boxRoot,
			BoxProfile: box.ProfileMain,
			NodeID:     "main",
			NodeRole:   "main",
		},
	}, nil).Handler()
}

func quoteJSON(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}
