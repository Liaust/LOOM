package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	loomsync "loom.local/loom/internal/sync"
)

func TestDeliveryCountsShareGenerationAndPreservePending(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	if err := store.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	items := []LocalSyncOutboxItem{
		{LocalOutboxID: "a", ItemKind: loomsync.ItemKindObjectMetadata, Status: localSyncStatusPending, PayloadJSON: json.RawMessage(`{"schema_version":"` + loomsync.FileObservationSchema + `","root_key":"a"}`)},
		{LocalOutboxID: "b", ItemKind: loomsync.ItemKindDeletionRequest, Status: localSyncStatusFailed, PayloadJSON: json.RawMessage(`{"root_key":"b"}`)},
		{LocalOutboxID: "c", ItemKind: loomsync.ItemKindDeletionRequest, Status: localSyncStatusAccepted, PayloadJSON: json.RawMessage(`{"root_key":"b"}`)},
	}
	if err := store.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	a, err := store.localSyncRootStatus(config, state, "a")
	if err != nil || a.Counts.Pending != 1 {
		t.Fatal(a, err)
	}
	// Reading a second root must reuse the one shared stable-generation entry.
	localStatusCache.Lock()
	var at time.Time
	for key, entry := range localStatusCache.entries {
		if key == "sync-roots\x00"+store.syncOutboxPath()+"\x00"+store.syncObjectsPath() {
			at = entry.at
		}
	}
	localStatusCache.Unlock()
	if at.IsZero() {
		t.Fatal("missing shared projection")
	}
	b, err := store.localSyncRootStatus(config, state, "b")
	if err != nil || b.Counts.Pending != 0 || b.Counts.Failed != 1 || b.Counts.Accepted != 1 {
		t.Fatal(b, err)
	}
	localStatusCache.Lock()
	entry := localStatusCache.entries["sync-roots\x00"+store.syncOutboxPath()+"\x00"+store.syncObjectsPath()]
	localStatusCache.Unlock()
	if !at.Equal(entry.at) {
		t.Fatal("second root rebuilt the same generation")
	}
	items[0].Status = localSyncStatusAccepted
	if err := store.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	a, err = store.localSyncRootStatus(config, state, "a")
	if err != nil || a.Counts.Pending != 0 || a.Counts.Accepted != 1 {
		t.Fatal("atomic ACK hidden", a, err)
	}
	locked, unlock, err := store.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := locked.beginSyncBatch()
	if err == nil {
		items[0].Status = localSyncStatusPending
		err = batch.SaveSyncOutbox(items)
	}
	if err == nil {
		a, err = batch.localSyncRootStatus(config, state, "a")
	}
	if releaseErr := unlock(); err != nil || releaseErr != nil || a.Counts.Pending != 1 {
		t.Fatal("batch-local pending work hidden", a, err, releaseErr)
	}
	// Malformed queues must fail rather than serve cached zero/pending counts.
	if err := os.WriteFile(store.syncOutboxPath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.localSyncRootStatus(config, state, "a"); err == nil {
		t.Fatal("malformed generation returned cached success")
	}
}

func TestBackupDeliveryCountsIgnoreHistoryAndSeeBatch(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	items := []LocalWatchedRootBackupOutboxItem{{RootKey: "notes", Status: localBackupStatusPending, LastErrorCode: "transport"}, {RootKey: "other", Status: localBackupStatusFailed}, {RootKey: "notes", Status: localBackupStatusAccepted}}
	if err := store.SaveWatchedRootBackupOutbox(items); err != nil {
		t.Fatal(err)
	}
	counts, err := store.backupDeliveryCountsByRoot()
	if err != nil || counts["notes"].Pending != 1 || counts["notes"].Retryable != 1 || counts["other"].Pending != 0 {
		t.Fatal(counts, err)
	}
	// Scheduling must not decode unrelated retained artifacts/items/batches.
	if err := os.WriteFile(store.watchedRootBackupItemsPath(), []byte("broken unrelated history"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deadline, err := watchedRootDeliveryDeadline(store, config, state, "notes", now.Add(15*time.Minute), now, 15*time.Minute)
	if err != nil || !deadline.Equal(now.Add(time.Minute)) {
		t.Fatal(deadline, err)
	}
	locked, unlock, err := store.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	batch, err := locked.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	items[0].Status = localBackupStatusAccepted
	if err := batch.SaveWatchedRootBackupOutbox(items); err != nil {
		t.Fatal(err)
	}
	counts, err = batch.backupDeliveryCountsByRoot()
	if err != nil || counts["notes"].Pending != 0 {
		t.Fatal("batch-local ACK hidden", counts, err)
	}
}

func TestDeliveryDeadlineDoesNotBlockNativeScheduling(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	_, unlock, err := store.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	now := time.Now()
	next := now.Add(time.Hour)
	deadline, err := watchedRootDeliveryDeadline(store, config, state, "busy", next, now, time.Hour)
	if err != nil || time.Since(now) > 100*time.Millisecond || deadline.Before(now) || deadline.After(now.Add(2*time.Second)) {
		t.Fatalf("scheduler blocked behind queue owner: deadline=%v elapsed=%v err=%v", deadline, time.Since(now), err)
	}
	// A native deadline sooner than the retry remains earlier; no event is lost.
	soon := time.Now().Add(100 * time.Millisecond)
	deadline, err = watchedRootDeliveryDeadline(store, config, state, "busy", soon, now, time.Hour)
	if err != nil || !deadline.Equal(soon) {
		t.Fatalf("native deadline postponed: %v %v", deadline, err)
	}
}
