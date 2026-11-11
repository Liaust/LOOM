package watchedroots

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDirtyHintSnapshotPreservesConcurrentArrival(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "note.md", HintKind: DirtyHintModified}); err != nil {
		t.Fatal(err)
	}
	observed, err := store.ListDirtyHints("notes")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddDirtyHints("notes", []DirtyHint{{RelativePath: "note.md", HintKind: DirtyHintModified}, {RelativePath: "other.md", HintKind: DirtyHintCreated}}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := store.ClearObservedDirtyHints("notes", observed); err != nil || cleared != 0 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
	remaining, err := store.ListDirtyHints("notes")
	if err != nil || len(remaining) != 2 {
		t.Fatalf("hints=%v err=%v", remaining, err)
	}
	if cleared, err := store.ClearObservedDirtyHints("notes", remaining); err != nil || cleared != 2 {
		t.Fatalf("cleared=%d err=%v", cleared, err)
	}
}

func TestNativeWatchChangesAndRestartCatchup(t *testing.T) {
	dir := t.TempDir()
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*.md"}, Watch: WatchConfig{Enabled: true, Backend: WatchBackendAuto}, Scan: ScanConfig{StabilityWindow: "0s"}})
	store := NewStore(t.TempDir())
	wakes := make(chan struct{}, 32)
	watch, err := StartNativeWatch(t.Context(), store, root, func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close() })
	awaitHint := func(path string, kind string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			hints, err := store.ListDirtyHints("notes")
			if err != nil {
				t.Fatal(err)
			}
			for _, hint := range hints {
				// Linux can emit WRITE/CHMOD immediately after CREATE; coalescing
				// retains the latest observation, and all require the same stat.
				matches := hint.HintKind == kind || (kind == DirtyHintCreated && (hint.HintKind == DirtyHintModified || hint.HintKind == DirtyHintMetadataChanged))
				if hint.RelativePath == path && matches {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %s hint for %q", kind, path)
	}
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitHint("note.md", DirtyHintCreated)
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "swap.tmp"), []byte("two"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "swap.tmp"), filepath.Join(dir, "note.md")); err != nil {
		t.Fatal(err)
	}
	awaitHint("note.md", DirtyHintCreated)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "child.md"), []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitHint("sub", DirtyHintSubtree)
	if err := os.Remove(filepath.Join(dir, "note.md")); err != nil {
		t.Fatal(err)
	}
	awaitHint("note.md", DirtyHintDeleted)
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadPathState("notes", "sub/child.md"); err != nil {
		t.Fatal(err)
	}
	_ = watch.Close()
	if watch.Healthy() {
		t.Fatal("closed watch claims healthy")
	}
	if err := os.WriteFile(filepath.Join(dir, "missed.md"), []byte("offline change"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := StartNativeWatch(t.Context(), store, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	awaitHint("__rescan__", DirtyHintRescanRequired)
	if _, err := Reconcile(context.Background(), store, ScanRequest{Mode: ScanModeAuto, Root: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadPathState("notes", "missed.md"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeDeadlineAndFailedHintBackoff(t *testing.T) {
	root := testScanRoot(t, t.TempDir(), RootConfig{RootKey: "notes", SafeRootKey: "slice09", Scan: ScanConfig{StabilityWindow: "2s", FullRescanInterval: "15m"}})
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	last := now.Add(-10 * time.Second)
	full := now.Add(-time.Minute)
	if err := store.SaveCheckpoint("notes", RootCheckpoint{ConfigHash: root.ConfigHash, LastFullRescanAt: &full, OutputsCurrent: true}); err != nil {
		t.Fatal(err)
	}
	due, err := NextNativeReconcile(store, root, now, last, true, time.Minute)
	if err != nil || !due.Equal(full.Add(15*time.Minute)) {
		t.Fatalf("idle due=%v err=%v", due, err)
	}
	ready := now.Add(time.Second)
	cp := RootCheckpoint{ConfigHash: root.ConfigHash, RootReachable: true, LastFullRescanAt: &full, OutputsCurrent: true, PendingRescan: true, NextHashReadyAt: &ready}
	if err := store.SaveCheckpoint("notes", cp); err != nil {
		t.Fatal(err)
	}
	due, err = NextNativeReconcile(store, root, now, last, true, 15*time.Minute)
	if err != nil || !due.Equal(ready) {
		t.Fatalf("stability due=%v err=%v", due, err)
	}
	// Legacy checkpoints and repeated deferrals use a bounded retry, not the
	// full scan interval or an already-attempted immediate deadline.
	for _, stale := range []*time.Time{nil, &last} {
		cp.NextHashReadyAt = stale
		if err := store.SaveCheckpoint("notes", cp); err != nil {
			t.Fatal(err)
		}
		due, err = NextNativeReconcile(store, root, now, last, true, 15*time.Minute)
		if err != nil || !due.Equal(last.Add(time.Minute)) {
			t.Fatalf("deferred retry=%v err=%v", due, err)
		}
	}
	if err := store.SaveCheckpoint("notes", RootCheckpoint{ConfigHash: root.ConfigHash, LastFullRescanAt: &full, OutputsCurrent: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "old.md", HintKind: DirtyHintModified, LastSeenAt: last.Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	due, err = NextNativeReconcile(store, root, now, last, true, time.Minute)
	if err != nil || !due.Equal(last.Add(time.Minute)) {
		t.Fatalf("retry due=%v err=%v", due, err)
	}
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "new.md", HintKind: DirtyHintModified, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	due, err = NextNativeReconcile(store, root, now, last, true, time.Minute)
	if err != nil || !due.Equal(now.Add(2*time.Second)) {
		t.Fatalf("new edit due=%v err=%v", due, err)
	}
}

func TestNativeWatchBudgetFallbackAndNoFollow(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Watch: WatchConfig{Enabled: true, Backend: WatchBackendAuto}})
	store := NewStore(t.TempDir())
	watch, err := StartNativeWatch(t.Context(), store, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range watch.watcher.WatchList() {
		if path == outside || path == filepath.Join(dir, "link") {
			t.Fatal("watch followed symlink")
		}
	}
	_ = watch.Close()
	root.Config.Scan.MaxDirsPerRun = 1
	watch, err = StartNativeWatch(t.Context(), store, root, nil)
	if err != nil {
		t.Fatalf("scan page budget limited persistent watch coverage: %v", err)
	}
	_ = watch.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if watch, err := StartNativeWatch(ctx, store, root, nil); err == nil {
		_ = watch.Close()
		t.Fatal("cancelled watch registration succeeded")
	}
	observation, err := store.LoadWatchObservation("notes")
	if err != nil || observation.State != "polling" || observation.Reason != "native_watch_registration_failed" {
		t.Fatalf("observation=%v err=%v", observation, err)
	}
}

func TestFallbackContinuesPagesHashesAndOutputsWithoutIdleSpin(t *testing.T) {
	root := testScanRoot(t, t.TempDir(), RootConfig{RootKey: "notes", SafeRootKey: "slice09", Scan: ScanConfig{FullRescanInterval: "6h"}})
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	last := now.Add(-time.Second)
	full := now.Add(-time.Minute)
	for _, tc := range []struct {
		name       string
		checkpoint RootCheckpoint
		want       time.Time
	}{
		{"idle", RootCheckpoint{LastFullRescanAt: &full, OutputsCurrent: true}, last.Add(15 * time.Minute)},
		{"page", RootCheckpoint{PendingRescan: true, FullScanID: "incomplete", FullScanAfter: "a"}, last.Add(time.Second)},
		{"hashes", RootCheckpoint{PendingRescan: true, DeferredHashPaths: []string{"a.md"}}, last.Add(time.Second)},
		{"outputs", RootCheckpoint{OutputContinuationReady: true}, last.Add(time.Second)},
		{"unknown deferral", RootCheckpoint{PendingRescan: true}, last.Add(time.Minute)},
		{"failed continuation", RootCheckpoint{PendingRescan: true, FullScanID: "incomplete", OutputContinuationReady: true, LastErrorCode: "source_unavailable"}, last.Add(time.Minute)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.checkpoint.ConfigHash = root.ConfigHash
			tc.checkpoint.RootReachable = true
			if err := store.SaveCheckpoint("notes", tc.checkpoint); err != nil {
				t.Fatal(err)
			}
			got, err := NextNativeReconcile(store, root, now, last, false, 15*time.Minute)
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("due=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
	ready := now.Add(2 * time.Second)
	if err := store.SaveCheckpoint("notes", RootCheckpoint{ConfigHash: root.ConfigHash, RootReachable: true, PendingRescan: true, DeferredHashPaths: []string{"a.md"}, NextHashReadyAt: &ready}); err != nil {
		t.Fatal(err)
	}
	got, err := NextNativeReconcile(store, root, now, last, false, 15*time.Minute)
	if err != nil || !got.Equal(ready) {
		t.Fatalf("fallback bypassed stability: %v %v", got, err)
	}
	if err := store.SaveCheckpoint("notes", RootCheckpoint{ConfigHash: root.ConfigHash, PendingRescan: true, FullScanID: "incomplete", OutputContinuationReady: true}); err != nil {
		t.Fatal(err)
	}
	got, err = NextNativeReconcile(store, root, now, last, false, 15*time.Minute)
	if err != nil || !got.Equal(last.Add(time.Minute)) {
		t.Fatalf("unavailable source busy-looped: %v %v", got, err)
	}
}

func TestDirtyHintOverflowRetainsDurableRescan(t *testing.T) {
	store := NewStore(t.TempDir())
	hints := make([]DirtyHint, 4097)
	for i := range hints {
		hints[i] = DirtyHint{RelativePath: fmt.Sprintf("note-%d.md", i), HintKind: DirtyHintModified}
	}
	if err := store.AddDirtyHints("notes", hints); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListDirtyHints("notes")
	if err != nil || len(got) != 1 || got[0].HintKind != DirtyHintRescanRequired || got[0].Generation == "" {
		t.Fatalf("hints=%v err=%v", got, err)
	}
}
