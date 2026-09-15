package backupcontracts

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/requestctx"
)

func TestPreflightServicePendingIdempotentCompletedRetryAndExpiredPostgres(t *testing.T) {
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
	const nodeID = "node_pf_preflight"
	if _, err := db.ExecContext(ctx, `INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status, presence_state) VALUES ($1, 'preflight-node', 'Preflight Node', 'workstation', 'workspace', 'workspace', 'active', 'offline') ON CONFLICT (node_id) DO UPDATE SET status = 'active', presence_state = 'offline'`, nodeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM backup.protected_folder_preflights WHERE target_node_id = $1 AND retry_of_preflight_id IS NOT NULL`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM backup.protected_folder_preflights WHERE target_node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.message_acks WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM communication.messages WHERE node_id = $1`, nodeID)
		_, _ = db.ExecContext(ctx, `DELETE FROM nodes.nodes WHERE node_id = $1`, nodeID)
	})
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr-preflight")
	if err != nil {
		t.Fatal(err)
	}
	service := NewPreflightService(db)
	pathValue := filepath.Join(t.TempDir(), "folder")
	recheck := RecheckIdentityForContractKey("photos")
	input := PreflightCreateRequest{NodeRef: "preflight-node", Path: pathValue, Recheck: &recheck, IdempotencyKey: "preflight-test-idempotency"}
	record, err := service.Create(ctx, req, input)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != PreflightStatusPending || record.CommunicationMessageID == "" {
		t.Fatalf("pending record = %#v", record)
	}
	duplicate, err := service.Create(ctx, req, input)
	if err != nil || duplicate.PreflightID != record.PreflightID {
		t.Fatalf("idempotent duplicate = %#v err=%v", duplicate, err)
	}

	message, err := service.Communication.GetMessage(ctx, record.CommunicationMessageID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodePreflightPayload(message.PayloadJSON)
	if err != nil || payload.Recheck == nil || *payload.Recheck != recheck {
		t.Fatalf("durable recheck identity = %#v err=%v", payload.Recheck, err)
	}
	processedAt := time.Now().UTC()
	result := PreflightResult{SchemaVersion: ProtectedFolderPreflightResultVersion, RequestedPath: pathValue, CanonicalPath: pathValue, Exists: true, Directory: true, Readable: true, Budget: DefaultPreflightBudget(), Policy: PreflightPolicyEvidence{Profile: "managed", Fingerprint: "sha256:test"}}
	resultJSON, _ := json.Marshal(result)
	ack := communication.AckResult{Message: message, Ack: communication.MessageAck{AckStatus: communication.AckStatusCompleted, ResultJSON: resultJSON, ProcessedAt: &processedAt}}
	if err := service.ProjectAcknowledgement(ctx, ack); err != nil {
		t.Fatal(err)
	}
	if err := service.ProjectAcknowledgement(ctx, ack); err != nil {
		t.Fatalf("projection replay failed: %v", err)
	}
	completed, err := service.Get(ctx, record.PreflightID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != PreflightStatusCompleted || completed.Result == nil || !completed.Result.Readable {
		t.Fatalf("completed record = %#v", completed)
	}
	if err := service.ValidateForContract(ctx, record.PreflightID, Contract{OwnerNode: "preflight-node", Target: TargetSpec{Path: pathValue}}); err != nil {
		t.Fatal(err)
	}

	retried, err := service.Retry(ctx, req, record.PreflightID)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != PreflightStatusPending || retried.PreflightID == record.PreflightID || retried.RetryOfPreflightID != record.PreflightID {
		t.Fatalf("retry record = %#v", retried)
	}
	retryMessage, err := service.Communication.GetMessage(ctx, retried.CommunicationMessageID)
	if err != nil {
		t.Fatal(err)
	}
	retryPayload, err := DecodePreflightPayload(retryMessage.PayloadJSON)
	if err != nil || retryPayload.Recheck == nil || *retryPayload.Recheck != recheck {
		t.Fatalf("retried recheck identity = %#v err=%v", retryPayload.Recheck, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE backup.protected_folder_preflights SET expires_at = now() - interval '1 second' WHERE protected_folder_preflight_id = $1`, retried.PreflightID); err != nil {
		t.Fatal(err)
	}
	expired, err := service.Get(ctx, retried.PreflightID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.Status != PreflightStatusExpired || expired.ErrorCode != "preflight.expired" {
		t.Fatalf("expired record = %#v", expired)
	}
}

func TestProtectedFolderRecheckIdentityIsBoundedToDerivedRuntimeKeys(t *testing.T) {
	identity := RecheckIdentityForContractKey("photos")
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	spoofed := identity
	spoofed.WorkerKey = "watched-root.other"
	if err := spoofed.Validate(); err == nil {
		t.Fatal("expected spoofed worker identity to fail")
	}
	payload := ProtectedFolderPreflightPayload{
		SchemaVersion: ProtectedFolderControlSchemaVersion,
		PreflightID:   "backup_preflight_identity",
		TargetNode:    "node_test",
		RequestedPath: "/tmp/photos",
		Recheck:       &spoofed,
		Ignore:        IgnorePolicy{Profile: "managed", DiscoverUserRules: true},
		Budget:        DefaultPreflightBudget(),
		ExpiresAt:     time.Now().Add(time.Minute),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePreflightPayload(encoded); err == nil {
		t.Fatal("expected spoofed recheck payload to fail strict validation")
	}
}
