package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/lane"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
)

func TestLaneHousekeepingRuntimeExpiresSuccessfulQuarantineAndIsRestartSafe(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loom-box")
	statePath := filepath.Join(root, lane.DefaultStateRelPath)
	batchID := "lane_daemon_expiry"
	quarantinePath := filepath.Join(statePath, "cleanup", batchID)
	if err := os.MkdirAll(quarantinePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(quarantinePath, "payload.txt"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	completed := time.Now().UTC().Add(-time.Hour)
	expires := completed.Add(lane.SuccessfulCleanupQuarantineGrace)
	record := lane.BatchRecord{
		SchemaVersion: lane.BatchSchemaVersion, BatchID: batchID, Status: lane.BatchStatusLocalCleanupDone,
		LocalCleanupQuarantinePath: quarantinePath, LocalCleanupQuarantineState: lane.CleanupQuarantineRetainedForRecovery,
		LocalCleanupQuarantineExpiresAt: &expires, CompletedAt: &completed,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	batchDir := filepath.Join(statePath, "batches")
	if err := os.MkdirAll(batchDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(batchDir, batchID+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	runtimeStore := noderuntime.NewStore(filepath.Join(t.TempDir(), "runtime"))
	if err := runtimeStore.EnsureDefaultInstances(noderuntime.DefaultInstanceInput{}); err != nil {
		t.Fatal(err)
	}
	instance, err := runtimeStore.LoadInstance(noderuntime.WorkerKeyLaneHousekeeping)
	if err != nil {
		t.Fatal(err)
	}
	if instance.IntervalSeconds != lane.DefaultLaneHousekeepingIntervalSeconds {
		t.Fatalf("Lane housekeeping interval = %d", instance.IntervalSeconds)
	}
	instance.ConfigJSON = mustMarshalJSON(laneHousekeepingRuntimeConfig{BoxRootPath: root})
	if err := runtimeStore.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}

	first, err := nodeAgentRuntimeRegistry().RunOnce(context.Background(), runtimeStore, noderuntime.Env{}, instance.WorkerKey, "corr-lane-housekeeping")
	if err != nil {
		t.Fatal(err)
	}
	if first.Run.Status != noderuntime.RunStatusSucceeded || first.Health.Status != noderuntime.WorkerStatusHealthy {
		t.Fatalf("Lane housekeeping first run = %#v", first)
	}
	if _, err := os.Lstat(quarantinePath); !os.IsNotExist(err) {
		t.Fatalf("daemon worker did not expire quarantine: %v", err)
	}

	second, err := nodeAgentRuntimeRegistry().RunOnce(context.Background(), runtimeStore, noderuntime.Env{}, instance.WorkerKey, "corr-lane-housekeeping-restart")
	if err != nil {
		t.Fatal(err)
	}
	if second.Run.Status != noderuntime.RunStatusSkipped || second.Health.Status != noderuntime.WorkerStatusHealthy {
		t.Fatalf("restart/idempotent Lane housekeeping run = %#v", second)
	}
}
