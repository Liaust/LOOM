package notesworkspacesync

import (
	"context"
	"testing"
	"time"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestSharedSourceWatchUsesOwnerHintsAndSurvivesAcknowledgement(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	scope.WatchKey = "notes"
	store := watchedroots.NewStore(t.TempDir())
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "notes", Include: []string{"**"}, Watch: watchedroots.WatchConfig{Enabled: true, Backend: watchedroots.WatchBackendAuto, DebounceWindow: "10ms"}})
	root := watchedroots.ValidatedRoot{Config: config, ConfigHash: watchedroots.ConfigHash(config), RootPath: scope.Root, RootReachable: true}
	owner, err := watchedroots.StartNativeWatch(t.Context(), store, root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	watch, err := StartSharedSourceWatch(t.Context(), store.DataDir, scope, func(ctx context.Context, path string) error { return i.Hint(ctx, scope, path, time.Now()) })
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	inventorySeed(t, i, scope, now, 32)
	inventoryWrite(t, scope, "a.md", "new content")
	deadline := time.Now().Add(3 * time.Second)
	for {
		feed, err := store.LoadSourceHints("notes")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := feed.Hints["a.md"]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner did not publish arrival")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	for {
		var found bool
		if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error { _, err := r.Get(t.Context(), "a.md"); found = err == nil; return nil }); err != nil {
			t.Fatal(err)
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scanner acknowledgement lost Notes arrival")
		}
		time.Sleep(10 * time.Millisecond)
	}
	wrong := scope
	wrong.Root = t.TempDir()
	if lease, err := StartSharedSourceWatch(t.Context(), store.DataDir, wrong, func(context.Context, string) error { return nil }); err == nil {
		lease.Close()
		t.Fatal("accepted a different source root")
	} else if sourceWatchDetail(err) != "watch_root_mismatch" {
		t.Fatal("root mismatch lost diagnostic", err)
	}
	owner.Close()
	deadline = time.Now().Add(time.Second)
	for watch.Healthy() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if watch.Healthy() {
		t.Fatal("stopped owner reported healthy")
	}
	if diagnostic, ok := watch.(interface{ WatchReason() string }); !ok || diagnostic.WatchReason() != "watch_owner_not_active" {
		t.Fatal("terminal owner failure lost its diagnostic")
	}
}

func TestSharedSourceWatchRecoversOwnerStartupWithoutPolling(t *testing.T) {
	i, scope, _ := inventoryFixture(t)
	scope.WatchKey = "notes"
	store := watchedroots.NewStore(t.TempDir())
	config := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "notes", Include: []string{"**"}, Watch: watchedroots.WatchConfig{Enabled: true, Backend: watchedroots.WatchBackendAuto}})
	root := watchedroots.ValidatedRoot{Config: config, ConfigHash: watchedroots.ConfigHash(config), RootPath: scope.Root, RootReachable: true}
	owner, err := watchedroots.StartNativeWatch(t.Context(), store, root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	owner.Close()
	var lease SourceWatch
	calls := 0
	runtime := &Runtime{OpenSourceWatch: func(actual InventoryScope, _ func(context.Context, string) error) (SourceWatch, error) {
		calls++
		var err error
		lease, err = StartSharedSourceWatch(t.Context(), store.DataDir, actual, func(ctx context.Context, path string) error { return i.Hint(ctx, scope, path, time.Now()) })
		return lease, err
	}}
	defer runtime.CloseSourceWatches()
	if err := runtime.prepareInventories(t.Context(), i, map[string]InventoryScope{"c": scope}); err != nil {
		t.Fatal(err)
	}
	if lease == nil || lease.Healthy() || !sourceWatchObserving(lease) {
		t.Fatal("stopped owner did not retain its lightweight status subscription")
	}
	if err := runtime.prepareInventories(t.Context(), i, map[string]InventoryScope{"c": scope}); err != nil || calls != 1 {
		t.Fatal("waiting owner caused repeated registration", calls, err)
	}
	owner, err = watchedroots.StartNativeWatch(t.Context(), store, root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	deadline := time.Now().Add(time.Second)
	for !lease.Healthy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !lease.Healthy() {
		t.Fatal("owner startup did not recover through its actual status event")
	}
	if err := runtime.prepareInventories(t.Context(), i, map[string]InventoryScope{"c": scope}); err != nil || calls != 1 {
		t.Fatal("recovery replaced the observer or failed", calls, err)
	}
	if state := inventoryState(t, i, scope); state.WatchReason != "" || state.WatchDetail != "" {
		t.Fatal("recovered owner retained degraded inventory posture", state)
	}
}
