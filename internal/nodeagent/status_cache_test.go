package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	loomsync "loom.local/loom/internal/sync"
)

func TestLocalStatusCacheDetachesAndInvalidatesAtomicReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	loads := 0
	load := func() ([]string, error) {
		loads++
		data, err := os.ReadFile(path)
		return []string{string(data)}, err
	}
	first, err := cachedLocalStatus("test", []string{path}, load)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = "mutated"
	second, err := cachedLocalStatus("test", []string{path}, load)
	if err != nil || second[0] != "one" || loads != 1 {
		t.Fatalf("cached status was not detached: %v %v %d", second, err, loads)
	}
	info, _ := os.Stat(path)
	replacement := path + ".new"
	if err := os.WriteFile(replacement, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	third, err := cachedLocalStatus("test", []string{path}, load)
	if err != nil || third[0] != "two" || loads != 2 {
		t.Fatalf("same-size/mtime replacement was stale: %v %v %d", third, err, loads)
	}
	if err := os.WriteFile(path, []byte("longer"), 0o600); err != nil {
		t.Fatal(err)
	}
	fourth, err := cachedLocalStatus("test", []string{path}, load)
	if err != nil || fourth[0] != "longer" || loads != 3 {
		t.Fatalf("in-place change was stale: %v %v %d", fourth, err, loads)
	}
}

func TestCachedSyncStatusObservesPendingWorkAndTransactionLocalChanges(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	initial, err := store.localSyncRootStatus(config, state, "notes")
	if err != nil || initial.Counts.Pending != 0 {
		t.Fatal(initial, err)
	}
	items, err := store.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, LocalSyncOutboxItem{LocalOutboxID: "cache-pending", ItemKind: loomsync.ItemKindDeletionRequest, Status: localSyncStatusPending, PayloadJSON: json.RawMessage(`{"root_key":"notes"}`)})
	if err := store.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	pending, err := store.localSyncRootStatus(config, state, "notes")
	if err != nil || pending.Counts.Pending != 1 {
		t.Fatalf("new delivery hidden by cache: %+v %v", pending, err)
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
	items[0].Status = localSyncStatusAccepted
	if err := batch.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	accepted, err := batch.localSyncRootStatus(config, state, "notes")
	if err != nil || accepted.Counts.Pending != 0 || accepted.Counts.Accepted != 1 {
		t.Fatalf("transaction-local acknowledgement hidden: %+v %v", accepted, err)
	}
}

func TestLocalStatusCacheDoesNotRetainChangingSnapshotOrErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "status.json")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	loads := 0
	load := func() (int, error) {
		loads++
		return loads, os.WriteFile(path, []byte{byte(loads)}, 0o600)
	}
	for i := 1; i <= 2; i++ {
		value, err := cachedLocalStatus("test-changing", []string{path}, load)
		if err != nil || value != i {
			t.Fatalf("changing load: %d %v", value, err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, err := cachedLocalStatus("test-changing", []string{path}, func() (int, error) { return 0, os.ErrNotExist })
		if err == nil {
			t.Fatal("missing snapshot returned stale success")
		}
	}
}
