package backupcontracts_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
)

func TestReconcileServiceRevisionRetryOutOfOrderProjectionAndTombstonePostgres(t *testing.T) {
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
	const nodeID = "node_pf_reconcile"
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status, presence_state) VALUES ($1, 'reconcile-node', 'Reconcile Node', 'workstation', 'workspace', 'workspace', 'active', 'offline') ON CONFLICT (node_id) DO UPDATE SET status = 'active'`, nodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM box.watch_root_registrations WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.message_acks WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.messages WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM nodes.nodes WHERE node_id = $1`, nodeID)
	})
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	boxRoot := t.TempDir()
	target := t.TempDir()
	sourcePath := filepath.Join(target, "source.txt")
	retainedPath := filepath.Join(t.TempDir(), "retained-backup.bin")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retainedPath, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	item1 := reconcileItem(t, "photos", target, 10*1024*1024)
	plan := box.WatchPlan{SchemaVersion: box.SchemaVersion, RootPath: boxRoot, ProjectRoot: boxRoot, OwnerNode: "reconcile-node", BoxID: "box_pf_reconcile", ContractPath: filepath.Join(boxRoot, ".loom", "box.yaml"), WatchedRoots: []projectcontracts.ProjectWatchedRootItem{item1}}
	if _, err := box.NewService(db).ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Plan: &plan}); err != nil {
		t.Fatal(err)
	}
	service := backupcontracts.NewReconcileService(db)
	queued1, err := service.QueueNodes(ctx, req, []string{"reconcile-node"})
	if err != nil || len(queued1) != 1 || queued1[0].DesiredRevision != 1 {
		t.Fatalf("first queue=%#v err=%v", queued1, err)
	}
	var payload1 backupcontracts.ProtectedFolderReconcilePayload
	if err := json.Unmarshal(queued1[0].Message.PayloadJSON, &payload1); err != nil {
		t.Fatal(err)
	}
	if len(payload1.Roots) != 1 || payload1.Roots[0].ContractKey != "photos" {
		t.Fatalf("first payload %#v", payload1)
	}
	reused, err := service.QueueNodes(ctx, req, []string{nodeID})
	if err != nil || !reused[0].Reused || reused[0].Message.CommunicationMessageID != queued1[0].Message.CommunicationMessageID {
		t.Fatalf("unchanged queue=%#v err=%v", reused, err)
	}

	item2 := reconcileItem(t, "photos", target, 20*1024*1024)
	plan.WatchedRoots = []projectcontracts.ProjectWatchedRootItem{item2}
	if _, err := box.NewService(db).ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Plan: &plan}); err != nil {
		t.Fatal(err)
	}
	queued2, err := service.QueueNodes(ctx, req, []string{nodeID})
	if err != nil || queued2[0].DesiredRevision != 2 || queued2[0].Message.CommunicationMessageID == queued1[0].Message.CommunicationMessageID {
		t.Fatalf("changed queue=%#v err=%v", queued2, err)
	}

	processed := time.Now().UTC()
	ack1 := reconcileAck(payload1, queued1[0].Message, &processed)
	if err := service.ProjectAcknowledgement(ctx, ack1); err != nil {
		t.Fatal(err)
	}
	assertRegistrationAppliedRevision(t, db, nodeID, 0)
	var payload2 backupcontracts.ProtectedFolderReconcilePayload
	if err := json.Unmarshal(queued2[0].Message.PayloadJSON, &payload2); err != nil {
		t.Fatal(err)
	}
	ack2 := reconcileAck(payload2, queued2[0].Message, &processed)
	if err := service.ProjectAcknowledgement(ctx, ack2); err != nil {
		t.Fatal(err)
	}
	if err := service.ProjectAcknowledgement(ctx, ack2); err != nil {
		t.Fatalf("projection replay: %v", err)
	}
	assertRegistrationAppliedRevision(t, db, nodeID, 2)
	if _, err := db.ExecContext(ctx, `UPDATE box.watch_root_registrations SET activation_status = 'reported' WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE communication.messages SET status = $2 WHERE communication_message_id = $1`, queued2[0].Message.CommunicationMessageID, communication.StatusAcked); err != nil {
		t.Fatal(err)
	}
	if _, err := box.NewService(db).ApplyWatchPolicy(ctx, req, box.WatchApplyInput{Plan: &plan}); err != nil {
		t.Fatal(err)
	}
	reusedAfterAck, err := service.QueueNodes(ctx, req, []string{nodeID})
	if err != nil || !reusedAfterAck[0].Reused || reusedAfterAck[0].Message.CommunicationMessageID != queued2[0].Message.CommunicationMessageID {
		t.Fatalf("acked unchanged queue=%#v err=%v", reusedAfterAck, err)
	}
	var preservedStatus string
	if err := db.QueryRowContext(ctx, `SELECT activation_status FROM box.watch_root_registrations WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID).Scan(&preservedStatus); err != nil {
		t.Fatal(err)
	}
	if preservedStatus != "reported" {
		t.Fatalf("idempotent apply/requeue reset converged registration to %q", preservedStatus)
	}

	retry, err := service.RetryNode(ctx, req, nodeID)
	if err != nil || retry.DesiredRevision != 2 || retry.Message.CommunicationMessageID == queued2[0].Message.CommunicationMessageID {
		t.Fatalf("retry=%#v err=%v", retry, err)
	}
	var appliedAtBefore time.Time
	if err := db.QueryRowContext(ctx, `SELECT last_applied_at FROM box.watch_root_registrations WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID).Scan(&appliedAtBefore); err != nil {
		t.Fatal(err)
	}
	var retryPayload backupcontracts.ProtectedFolderReconcilePayload
	if err := json.Unmarshal(retry.Message.PayloadJSON, &retryPayload); err != nil {
		t.Fatal(err)
	}
	retryProcessed := processed.Add(time.Hour)
	retryAck := reconcileAck(retryPayload, retry.Message, &retryProcessed)
	if err := service.ProjectAcknowledgement(ctx, retryAck); err != nil {
		t.Fatal(err)
	}
	var appliedAtAfter time.Time
	if err := db.QueryRowContext(ctx, `SELECT last_applied_at FROM box.watch_root_registrations WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID).Scan(&appliedAtAfter); err != nil {
		t.Fatal(err)
	}
	if !appliedAtAfter.Equal(appliedAtBefore) {
		t.Fatalf("unchanged retry moved configuration apply time from %s to %s", appliedAtBefore, appliedAtAfter)
	}
	if err := box.NewService(db).MarkBackupContractDeleted(ctx, boxRoot, "photos"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE box.watch_root_registrations SET activation_status = 'stale' WHERE node_id = $1`, nodeID); err != nil {
		t.Fatal(err)
	}
	tombstone, err := service.QueueNodes(ctx, req, []string{nodeID})
	if err != nil || tombstone[0].DesiredRevision != 3 {
		t.Fatalf("tombstone=%#v err=%v", tombstone, err)
	}
	var tombstonePayload backupcontracts.ProtectedFolderReconcilePayload
	if err := json.Unmarshal(tombstone[0].Message.PayloadJSON, &tombstonePayload); err != nil {
		t.Fatal(err)
	}
	if len(tombstonePayload.Roots) != 0 {
		t.Fatalf("deleted contract remained desired: %#v", tombstonePayload)
	}
	pending, err := backupcontracts.NewStatusService(db).List(ctx, boxRoot, backupcontracts.DefaultDirectoryRelPath, backupcontracts.ProtectedFolderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Folders) != 1 || !pending.Folders[0].Deleting || pending.Folders[0].MissingContract || pending.Folders[0].Lifecycle != backupcontracts.ProtectedFolderStatusWaitingForNode {
		t.Fatalf("queued deletion status = %#v", pending)
	}
	tombstoneAck := reconcileAck(tombstonePayload, tombstone[0].Message, &processed)
	if err := service.ProjectAcknowledgement(ctx, tombstoneAck); err != nil {
		t.Fatal(err)
	}
	completed, err := backupcontracts.NewStatusService(db).List(ctx, boxRoot, backupcontracts.DefaultDirectoryRelPath, backupcontracts.ProtectedFolderFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(completed.Folders) != 0 || len(completed.Contracts) != 0 || len(completed.Problems) != 0 {
		t.Fatalf("converged deletion remained user-visible: %#v", completed)
	}
	for _, staleAck := range []communication.AckResult{ack1, ack2} {
		if err := service.ProjectAcknowledgement(ctx, staleAck); err != nil {
			t.Fatal(err)
		}
	}
	var activation, lastAck string
	var desiredRevision, appliedRevision int64
	if err := db.QueryRowContext(ctx, `SELECT activation_status, last_node_ack_status, desired_revision, applied_revision FROM box.watch_root_registrations WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID).Scan(&activation, &lastAck, &desiredRevision, &appliedRevision); err != nil {
		t.Fatal(err)
	}
	if activation != "disabled" || lastAck != communication.AckStatusCompleted || desiredRevision != 3 || appliedRevision != 3 {
		t.Fatalf("stale acknowledgement revived tombstone: status=%s ack=%s desired=%d applied=%d", activation, lastAck, desiredRevision, appliedRevision)
	}
	for _, path := range []string{sourcePath, retainedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("deletion reconciliation touched retained data %s: %v", path, err)
		}
	}
}

func reconcileItem(t *testing.T, key, target string, maxBytes int64) projectcontracts.ProjectWatchedRootItem {
	t.Helper()
	contract := backupcontracts.Contract{SchemaVersion: backupcontracts.SchemaVersion, Key: key, OwnerNode: "reconcile-node", Status: backupcontracts.StatusActive, Target: backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: target}, Ignore: &backupcontracts.IgnorePolicy{Profile: "managed", DiscoverUserRules: true}, Backup: backupcontracts.BackupPolicy{Mode: backupcontracts.BackupModeIncrementalRaw, MaxFileBytes: maxBytes, MaxBatchBytes: maxBytes}}
	item, err := backupcontracts.WatchedRootItem(contract, "/box/.loom/contracts/backup/"+key+".yaml", backupcontracts.WatchPlanOptions{BoxRoot: "/box", BoxID: "box_pf_reconcile", OwnerNode: "reconcile-node"})
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func reconcileAck(payload backupcontracts.ProtectedFolderReconcilePayload, message communication.Message, processed *time.Time) communication.AckResult {
	result := backupcontracts.ProtectedFolderAck{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: communication.AppliedStateEvidence{SchemaVersion: communication.ControlEvidenceSchemaVersion, DesiredRevision: payload.Evidence.DesiredRevision, AppliedRevision: payload.Evidence.DesiredRevision, ConfigHash: payload.Evidence.ConfigHash, Outcome: communication.ControlOutcomeCompleted}}
	for _, root := range payload.Roots {
		result.Roots = append(result.Roots, backupcontracts.ProtectedFolderRootAck{ContractKey: root.ContractKey, ConfigHash: root.ConfigHash, Applied: true})
	}
	resultJSON, _ := json.Marshal(result)
	return communication.AckResult{Message: message, Ack: communication.MessageAck{AckStatus: communication.AckStatusCompleted, ResultJSON: resultJSON, ProcessedAt: processed}}
}

func assertRegistrationAppliedRevision(t *testing.T, db *sql.DB, nodeID string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`SELECT applied_revision FROM box.watch_root_registrations WHERE node_id = $1 AND source_contract_key = 'photos'`, nodeID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("applied revision=%d want=%d", got, want)
	}
}
