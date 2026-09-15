package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreDefaultInstancesAndOutboxSummary(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	if err := store.EnsureDefaultInstances(DefaultInstanceInput{
		HeartbeatIntervalSeconds: 45,
		PollIntervalSeconds:      15,
	}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	instances, err := store.LoadInstances()
	if err != nil {
		t.Fatalf("LoadInstances failed: %v", err)
	}
	if len(instances) != 7 {
		t.Fatalf("expected 7 default instances, got %d", len(instances))
	}
	foundHeartbeat := false
	foundStorageMount := false
	foundLaneHousekeeping := false
	for _, instance := range instances {
		if instance.WorkerKey == WorkerKeyHeartbeat {
			foundHeartbeat = true
			if instance.IntervalSeconds != 45 {
				t.Fatalf("expected heartbeat interval 45, got %d", instance.IntervalSeconds)
			}
		}
		if instance.WorkerKey == WorkerKeyStorageMount {
			foundStorageMount = true
			if instance.IntervalSeconds != 60 {
				t.Fatalf("expected storage mount interval 60, got %d", instance.IntervalSeconds)
			}
		}
		if instance.WorkerKey == WorkerKeyLaneHousekeeping {
			foundLaneHousekeeping = true
			if instance.IntervalSeconds != 60 {
				t.Fatalf("expected Lane housekeeping interval 60, got %d", instance.IntervalSeconds)
			}
		}
	}
	if !foundHeartbeat {
		t.Fatalf("expected heartbeat worker in %#v", instances)
	}
	if !foundStorageMount {
		t.Fatalf("expected storage mount worker in %#v", instances)
	}
	if !foundLaneHousekeeping {
		t.Fatalf("expected Lane housekeeping worker in %#v", instances)
	}

	item, err := store.QueueOutbox(OutboxItem{
		Kind:           OutboxKindMessageAck,
		IdempotencyKey: "idem-test",
		PayloadJSON:    json.RawMessage(`{"credential_token":"secret","ok":true}`),
	})
	if err != nil {
		t.Fatalf("QueueOutbox failed: %v", err)
	}
	if item.LocalOutboxID == "" || item.Status != OutboxStatusPending {
		t.Fatalf("unexpected outbox item %#v", item)
	}
	summary, err := store.OutboxSummary()
	if err != nil {
		t.Fatalf("OutboxSummary failed: %v", err)
	}
	if summary.Counts[OutboxStatusPending] != 1 || summary.TotalPendingBytes == 0 {
		t.Fatalf("unexpected outbox summary %#v", summary)
	}
	items, err := store.ListOutbox([]string{OutboxStatusPending}, 10)
	if err != nil {
		t.Fatalf("ListOutbox failed: %v", err)
	}
	redacted := RedactOutboxItems(items)
	if string(redacted[0].PayloadJSON) == string(items[0].PayloadJSON) {
		t.Fatalf("expected payload redaction, got %s", redacted[0].PayloadJSON)
	}
}

func TestDeleteInstancePreservesOtherWorkers(t *testing.T) {
	store := NewStore(t.TempDir())
	left := WorkerInstance{WorkerKey: WatchedRootWorkerKey("left"), Kind: KindWatchedRoot, Enabled: true}
	right := WorkerInstance{WorkerKey: WatchedRootWorkerKey("right"), Kind: KindWatchedRoot, Enabled: true}
	if err := store.SaveInstance(left); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveInstance(right); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteInstance(left.WorkerKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadInstance(left.WorkerKey); !os.IsNotExist(err) {
		t.Fatalf("deleted worker error=%v", err)
	}
	if _, err := store.LoadInstance(right.WorkerKey); err != nil {
		t.Fatalf("unrelated worker removed: %v", err)
	}
}

func TestRegistryRunsQueueReporter(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "data"))
	if err := store.EnsureDefaultInstances(DefaultInstanceInput{}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	if _, err := store.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck}); err != nil {
		t.Fatalf("QueueOutbox failed: %v", err)
	}
	output, err := DefaultRegistry().RunOnce(context.Background(), store, Env{
		ConfigPath: filepath.Join(dir, "config.json"),
		StatePath:  filepath.Join(dir, "state.json"),
		DataDir:    store.DataDir,
	}, WorkerKeyLocalQueueReporter, "corr-test")
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if output.Run.Status != RunStatusSucceeded || output.Health.Status != WorkerStatusHealthy {
		t.Fatalf("unexpected run output %#v", output)
	}
	summary, err := store.LoadLatestSummary()
	if err != nil {
		t.Fatalf("LoadLatestSummary failed: %v", err)
	}
	if summary.HighestStatus != WorkerStatusDegraded {
		t.Fatalf("expected summary to preserve aggregate degraded status, got %#v", summary)
	}
	if summary.Queues.OutboxPending != 1 {
		t.Fatalf("expected pending outbox summary, got %#v", summary)
	}
	runs, err := store.ListRuns(WorkerKeyLocalQueueReporter, 10)
	if err != nil {
		t.Fatalf("ListRuns failed: %v", err)
	}
	if len(runs) != 1 || runs[0].LocalRunID != output.Run.LocalRunID {
		t.Fatalf("unexpected runs %#v", runs)
	}
}

func TestBuildLocalSummaryIncludesWatchedRootBackupCounts(t *testing.T) {
	t.Parallel()
	store := NewStore(filepath.Join(t.TempDir(), "data"))
	if err := store.EnsureDefaultInstances(DefaultInstanceInput{}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	backupDir := filepath.Join(store.DataDir, "watched-root-backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		t.Fatalf("mkdir backup dir: %v", err)
	}
	batches := []map[string]any{
		{"local_batch_id": "batch_retry", "total_bytes": 12},
		{"local_batch_id": "batch_failed", "total_bytes": 34},
	}
	if payload, err := json.Marshal(batches); err != nil {
		t.Fatalf("marshal batches: %v", err)
	} else if err := os.WriteFile(filepath.Join(backupDir, "batches.json"), payload, 0o600); err != nil {
		t.Fatalf("write batches: %v", err)
	}
	outbox := []map[string]any{
		{"local_outbox_id": "outbox_retry", "local_ref": "batch_retry", "status": "pending", "last_error_code": "backup_artifact_upload_failed"},
		{"local_outbox_id": "outbox_failed", "local_ref": "batch_failed", "status": "failed", "last_error_code": "backup_transport_limit_exceeded"},
	}
	if payload, err := json.Marshal(outbox); err != nil {
		t.Fatalf("marshal outbox: %v", err)
	} else if err := os.WriteFile(filepath.Join(backupDir, "outbox.json"), payload, 0o600); err != nil {
		t.Fatalf("write outbox: %v", err)
	}

	summary, err := store.BuildLocalSummary()
	if err != nil {
		t.Fatalf("BuildLocalSummary failed: %v", err)
	}
	if summary.Queues.BackupPending != 1 ||
		summary.Queues.BackupRetryable != 1 ||
		summary.Queues.BackupFailed != 1 ||
		summary.Queues.BackupManualAction != 1 ||
		summary.Queues.BackupPendingBytes != 12 {
		t.Fatalf("unexpected backup queue summary %#v", summary.Queues)
	}
	if summary.HighestStatus != WorkerStatusRequiresManualAction {
		t.Fatalf("failed backup should require manual action, got %#v", summary)
	}
}

func TestSelfcheckRunnerReportsDegradedMissingCredential(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(configPath, []byte(`{"main_url":"http://10.44.0.2:8080","node_key":"workspace-test"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(statePath, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write state: %v", err)
	}
	store := NewStore(filepath.Join(dir, "data"))
	if err := store.EnsureDefaultInstances(DefaultInstanceInput{}); err != nil {
		t.Fatalf("EnsureDefaultInstances failed: %v", err)
	}
	output, err := DefaultRegistry().RunOnce(context.Background(), store, Env{
		ConfigPath: configPath,
		StatePath:  statePath,
		DataDir:    store.DataDir,
	}, WorkerKeySupervisorSelfcheck, "corr-test")
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if output.Health.Status != WorkerStatusDegraded {
		t.Fatalf("expected degraded selfcheck health, got %#v", output.Health)
	}
}
