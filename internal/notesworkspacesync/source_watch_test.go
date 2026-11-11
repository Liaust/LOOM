package notesworkspacesync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceWatchDurableArrivalAtomicSaveAndRename(t *testing.T) {
	i, s, now := inventoryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	wake := make(chan struct{}, 128)
	watch, err := StartSourceWatch(ctx, s, 20, func(ctx context.Context, path string) error { return i.Hint(ctx, s, path, time.Now().UTC()) }, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	inventorySeed(t, i, s, now, 32)
	await := func(path string, present bool) {
		t.Helper()
		deadline := time.NewTimer(3 * time.Second)
		defer deadline.Stop()
		for {
			var found bool
			err := i.Store.WithInventory(t.Context(), s, func(r InventoryRecords) error {
				e, err := r.Get(t.Context(), path)
				found = err == nil && e.Present == present
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if found {
				return
			}
			select {
			case <-wake:
			case <-deadline.C:
				t.Fatal("missing durable watch observation", path, present)
			}
		}
	}
	inventoryWrite(t, s, "a.md", "old")
	await("a.md", true)
	first := inventoryRow(t, i, s, "a.md")
	tmp := filepath.Join(s.Root, ".atomic")
	if err := os.WriteFile(tmp, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(s.Root, "a.md")); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for inventoryRow(t, i, s, "a.md").Sequence == first.Sequence {
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatal("atomic replacement not observed")
		}
	}
	if err := os.Rename(filepath.Join(s.Root, "a.md"), filepath.Join(s.Root, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	await("a.md", false)
	await("renamed.md", true)
	inventoryWrite(t, s, "arrived/nested.md", "new")
	// Directory creation is a durable bounded subtree request. Advance that page
	// to cover files created before recursive registration finishes.
	deadline2 := time.Now().Add(3 * time.Second)
	for {
		if err := i.Advance(t.Context(), s, time.Now().UTC(), 32); err != nil {
			t.Fatal(err)
		}
		var exists bool
		_ = i.Store.WithInventory(t.Context(), s, func(r InventoryRecords) error {
			_, err := r.Get(t.Context(), "arrived/nested.md")
			exists = err == nil
			return nil
		})
		if exists {
			break
		}
		if time.Now().After(deadline2) {
			t.Fatal("directory catch-up missing")
		}
		select {
		case <-wake:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := os.Remove(filepath.Join(s.Root, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	await("renamed.md", false)
}

func TestSourceWatchScopeAndNoFollow(t *testing.T) {
	i, s, _ := inventoryFixture(t)
	inventoryWrite(t, s, "Selected/a.md", "A")
	s.Prefix = "Selected"
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.Root, "Selected", "link")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	watch, err := StartSourceWatch(ctx, s, 5, func(ctx context.Context, path string) error { return i.Hint(ctx, s, path, time.Now().UTC()) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	if err := i.Hint(t.Context(), s, "Other/a.md", time.Now()); err == nil {
		t.Fatal("outside scope accepted")
	}
	if err := i.Hint(t.Context(), s, "Selected/link/secret.md", time.Now()); err != nil {
		t.Fatal(err)
	}
	if state := inventoryState(t, i, s); state.Degraded != "source_unavailable" {
		t.Fatal("symlink did not report unknown", state)
	}
}

type inventoryTestLease struct{ healthy, closed bool }

func (l *inventoryTestLease) Healthy() bool { return l.healthy }
func (l *inventoryTestLease) Close() error  { l.closed = true; return nil }
func TestInventorySharedWatchLeaseIsReusedAndRetries(t *testing.T) {
	i, scope, _ := inventoryFixture(t)
	calls := 0
	lease := &inventoryTestLease{healthy: true}
	runtime := &Runtime{OpenSourceWatch: func(actual InventoryScope, _ func(context.Context, string) error) (SourceWatch, error) {
		calls++
		if actual != scope {
			t.Fatal("inexact lease request", actual)
		}
		if calls > 1 {
			return nil, ErrHeld
		}
		return lease, nil
	}}
	scopes := map[string]InventoryScope{"c": scope}
	for n := 0; n < 2; n++ {
		if err := runtime.prepareInventories(t.Context(), i, scopes); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("duplicated existing watch subscription")
	}
	lease.healthy = false
	if err := runtime.prepareInventories(t.Context(), i, scopes); err != nil {
		t.Fatal(err)
	}
	if !lease.closed || calls != 2 || inventoryState(t, i, scope).Degraded != "watch_unavailable" {
		t.Fatal("lost unavailable posture")
	}
	if err := runtime.prepareInventories(t.Context(), i, scopes); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("hot registration retry")
	}
	runtime.inventories["c"].RetryAt = time.Now().Add(-time.Second)
	if err := runtime.prepareInventories(t.Context(), i, scopes); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("failed registration never reconsidered")
	}
}

func TestInventoryUnhealthyNewLeaseIsClosedAndBackedOff(t *testing.T) {
	i, scope, _ := inventoryFixture(t)
	lease := &inventoryTestLease{}
	calls := 0
	runtime := &Runtime{OpenSourceWatch: func(InventoryScope, func(context.Context, string) error) (SourceWatch, error) {
		calls++
		return lease, sharedWatchFailure("watch_owner_not_active")
	}}
	for range 3 {
		if err := runtime.prepareInventories(t.Context(), i, map[string]InventoryScope{"c": scope}); err != nil {
			t.Fatal(err)
		}
	}
	if !lease.closed || calls != 1 || runtime.inventories["c"].Watch != nil {
		t.Fatal("unhealthy returned lease caused repeated registration")
	}
	if state := inventoryState(t, i, scope); state.WatchDetail != "watch_owner_not_active" {
		t.Fatal("lost closed diagnostic", state)
	}
	progress, err := i.Progress(t.Context(), scope, time.Now())
	if err != nil || progress.WatchDetail != "watch_owner_not_active" {
		t.Fatal("progress omitted watch diagnostic", progress, err)
	}
}
