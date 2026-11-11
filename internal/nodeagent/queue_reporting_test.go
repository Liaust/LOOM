package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/communication"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

func TestQueueReportingRebuildDoesNotFailAcceptedFlush(t *testing.T) {
	acks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		acks++
		_ = json.NewEncoder(w).Encode(response.Success("test", communication.AckResult{
			Ack: communication.MessageAck{CommunicationAckID: "accepted-during-rebuild"},
		}))
	}))
	defer server.Close()
	s := testNodeAgentStore(t, t.TempDir(), server.URL, State{NodeID: "node_test", CredentialToken: "fixture"})
	runtime := s.runtimeStore()
	if _, err := runtime.QueueOutbox(noderuntime.OutboxItem{Kind: noderuntime.OutboxKindMessageAck, PayloadJSON: json.RawMessage(`{"communication_message_ref":"message","idempotency_key":"once"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.OutboxSummary(); err != nil {
		t.Fatal(err)
	}
	for i := range 515 {
		if err := os.WriteFile(filepath.Join(s.DataDir, "outbox", "done", fmt.Sprintf("history-%04d.json", i)), []byte("history is not decoded"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := noderuntime.RuntimeEnv(s.ConfigPath, s.StatePath, s.DataDir, "node_test", "workspace-test", server.URL, true)
	run, err := flushDueOutbox(t.Context(), env, runtime, 1)
	if err != nil || run.Submitted != 1 || run.Done != 1 || run.Failed != 0 || acks != 1 || run.SummaryAvailability != "stale" || run.SummaryUnavailableReason == "" || len(run.SummaryUnavailableReason) > 256 || run.Summary.Availability != "stale" {
		t.Fatal(run, acks, err)
	}
}

func queueReportingFixture(t *testing.T) (Store, Config, State) {
	t.Helper()
	s, config, state, _ := localSyncObjectTestStore(t)
	if err := s.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncOutbox([]LocalSyncOutboxItem{{LocalOutboxID: "sync-pending", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupBatches([]LocalWatchedRootBackupBatch{{LocalBatchID: "batch", RootKey: "notes", TotalBytes: 123}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupOutbox([]LocalWatchedRootBackupOutboxItem{{LocalOutboxID: "backup-pending", LocalRef: "batch", RootKey: "notes", Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, config, state
}

func TestQueueReportingReusesOwnerAndQualifiesContentionAcrossStatusReporterHeartbeat(t *testing.T) {
	s, config, state := queueReportingFixture(t)
	locked, release, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	owned := locked.runtimeStore()
	if err := owned.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{}); err != nil {
		t.Fatal(err)
	}
	status, err := owned.Status()
	if err != nil || status.Summary.Availability != "available" || status.Summary.Queues.SyncPending != 1 || status.Summary.Queues.BackupPendingBytes != 123 {
		t.Fatal(status, err)
	}
	before, err := os.ReadFile(s.queueRecordsPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owned.BuildLocalSummary(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.queueRecordsPath())
	if err != nil || string(before) != string(after) {
		t.Fatal("no-op report changed records", err)
	}
	// A separate observer cannot bypass the held owner, and must not call an
	// independent bbolt Open that waits for its own one-second timeout.
	observer := s.runtimeStore()
	started := time.Now()
	status, err = observer.Status()
	if err != nil || time.Since(started) > time.Second || status.Summary.Availability != "stale" || status.Summary.Queues.SyncPending != 1 || status.Summary.QueueRevision == 0 {
		t.Fatal(status, time.Since(started), err)
	}
	result, err := (noderuntime.QueueReporterRunner{}).RunOnce(t.Context(), observer, noderuntime.Env{}, noderuntime.WorkerInstance{})
	if err != nil || result.HealthStatus != noderuntime.WorkerStatusDegraded {
		t.Fatal(result, err)
	}
	var received nodes.HeartbeatInput
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(response.Success("test", nodes.Heartbeat{NodeHeartbeatID: "heartbeat", ReportedStatus: received.ReportedStatus}))
	}))
	defer server.Close()
	config.MainURL = server.URL
	if _, err := sendHeartbeatCore(t.Context(), s, config, state, observer, "test", heartbeatRequestOptions{reportedStatus: "ok"}); err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Runtime noderuntime.LocalSummary `json:"runtime"`
	}
	if err := json.Unmarshal(received.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if received.ReportedStatus != "degraded" || metadata.Runtime.Availability != "stale" || metadata.Runtime.Queues.BackupPendingBytes != 123 {
		t.Fatal(received.ReportedStatus, metadata)
	}
}

func TestQueueReportingByteRevisionAndOlderWriterRebuild(t *testing.T) {
	s, _, _ := queueReportingFixture(t)
	locked, release, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	initial, err := locked.readQueueSnapshot(t.Context())
	if err != nil || initial.BackupPendingBytes != 123 {
		t.Fatal(initial, err)
	}
	batch := LocalWatchedRootBackupBatch{LocalBatchID: "batch", RootKey: "notes", TotalBytes: 456}
	raw, _ := json.Marshal(batch)
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error { _, err := putQueueRecord(tx, "backup/batches", "batch", raw); return err }); err != nil {
		t.Fatal(err)
	}
	updated, err := locked.readQueueSnapshot(t.Context())
	if err != nil || updated.Revision <= initial.Revision || updated.BackupPendingBytes != 456 {
		t.Fatal(updated, err)
	}
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
		if _, err := putQueueRecord(tx, "backup/batches", "batch", raw); err != nil {
			return err
		}
		return carryQueueReport(tx)
	}); err != nil {
		t.Fatal(err)
	}
	noop, err := locked.readQueueSnapshot(t.Context())
	if err != nil || noop != updated {
		t.Fatal("no-op changed material revision", noop, updated, err)
	}
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
		if _, err := putQueueRecord(tx, "sync/cursors", "cursor", []byte(`{"root_key":"notes","sequence":42}`)); err != nil {
			return err
		}
		raw, _ := json.Marshal(LocalSyncOutboxItem{LocalOutboxID: "sync-pending", LocalRef: "retry-metadata-changed", Status: "pending"})
		_, err := putQueueRecord(tx, "sync/outbox", "sync-pending", raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if bookkeeping, err := locked.readQueueSnapshot(t.Context()); err != nil || bookkeeping != noop {
		t.Fatal("bookkeeping dirtied report revision", bookkeeping, noop, err)
	}
	batch.TotalBytes = 789
	raw, _ = json.Marshal(batch)
	// Simulate an older binary: row transaction without the derived watermark.
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error { return recordBucket(tx, "backup/batches", "rows").Put([]byte("batch"), raw) }); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := locked.readQueueSnapshot(t.Context())
	if err != nil || rebuilt.BackupPendingBytes != 789 || rebuilt.Revision <= noop.Revision {
		t.Fatal(rebuilt, err)
	}
	before, _ := os.ReadFile(s.queueRecordsPath())
	if again, err := locked.readQueueSnapshot(t.Context()); err != nil || again != rebuilt {
		t.Fatal(again, err)
	}
	after, _ := os.ReadFile(s.queueRecordsPath())
	if string(before) != string(after) {
		t.Fatal("rebuilt on every read")
	}
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
		raw, _ := json.Marshal(LocalWatchedRootBackupOutboxItem{LocalOutboxID: "backup-pending", LocalRef: "batch", RootKey: "notes", Status: "accepted"})
		_, err := putQueueRecord(tx, "backup/outbox", "backup-pending", raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := locked.readQueueSnapshot(t.Context())
	if err != nil || accepted.BackupPending != 0 || accepted.BackupPendingBytes != 0 {
		t.Fatal(accepted, err)
	}
}

func TestQueueReportingDoesNotMigrateOrUseLegacyOnInvalidCurrentStore(t *testing.T) {
	s, _, _ := queueReportingFixture(t)
	locked, release, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("counts")) }); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.queueRecordsPath())
	if _, err := s.readQueueSnapshot(context.Background()); err == nil {
		t.Fatal("missing current counters became a healthy zero")
	}
	after, _ := os.ReadFile(s.queueRecordsPath())
	if string(before) != string(after) {
		t.Fatal("report repaired authoritative counters")
	}
}
