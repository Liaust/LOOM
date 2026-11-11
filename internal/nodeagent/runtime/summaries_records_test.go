package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalSummaryUsesOneOwnerSnapshotAndRetainsUnavailableCounts(t *testing.T) {
	s := NewStore(t.TempDir())
	reads := 0
	var unavailable bool
	s.ReadQueueSnapshot = func(context.Context) (QueueSnapshot, error) {
		reads++
		if unavailable {
			return QueueSnapshot{}, errors.New("queue owner unavailable")
		}
		return QueueSnapshot{Revision: 7, SyncPending: 6, SyncFailed: 1, SyncConflicted: 3,
			BackupPending: 1, BackupRetryable: 1, BackupFailed: 2, BackupManualAction: 2, BackupPendingBytes: 123}, nil
	}
	status, err := s.Status()
	if err != nil || reads != 1 || status.Summary.Queues.SyncPending != 6 || status.Summary.Queues.BackupPendingBytes != 123 || status.Summary.Availability != "available" {
		t.Fatal(status, reads, err)
	}
	observed := status.Summary.QueueObservedAt
	unavailable = true
	summary, err := s.BuildLocalSummary()
	if err != nil || reads != 2 || summary.Availability != "stale" || summary.Queues != status.Summary.Queues || summary.QueueRevision != 7 || !summary.QueueObservedAt.Equal(observed) {
		t.Fatal(summary, reads, err)
	}
	if summary.RetryAt == nil {
		t.Fatal("missing retry deadline")
	}
	for range 3 {
		again, err := s.BuildLocalSummary()
		if err != nil || reads != 2 || !again.GeneratedAt.Equal(summary.GeneratedAt) || !again.RetryAt.Equal(*summary.RetryAt) {
			t.Fatal(again, reads, err)
		}
	}
	// The deadline is persisted and survives a new runtime Store/process.
	restarted := NewStore(s.DataDir)
	restarted.ReadQueueSnapshot = s.ReadQueueSnapshot
	if _, err := restarted.BuildLocalSummary(); err != nil || reads != 2 {
		t.Fatal(reads, err)
	}
	retry := time.Now().Add(-time.Second)
	summary.RetryAt = &retry
	if err := s.SaveLatestSummary(summary); err != nil {
		t.Fatal(err)
	}
	unavailable = false
	recovered, err := s.BuildLocalSummary()
	if err != nil || reads != 3 || recovered.Availability != "available" || recovered.RetryAt != nil {
		t.Fatal(recovered, reads, err)
	}
}

func TestUnboundCurrentStoreAndOrphanedLegacyMarkersFailClosed(t *testing.T) {
	s := NewStore(t.TempDir())
	root := filepath.Join(s.DataDir, "sync")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "records.db")
	if err := os.WriteFile(path, []byte("not an independently readable queue"), 0600); err != nil {
		t.Fatal(err)
	}
	summary, err := s.BuildLocalSummary()
	if err != nil || summary.Availability != "unavailable" || summary.HighestStatus == WorkerStatusHealthy {
		t.Fatal(summary, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outbox.json"), []byte(`{"format":"loom.node-queue.records.v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.localSyncCounts(); err == nil {
		t.Fatal("orphaned marker was treated as an empty queue")
	}
}
