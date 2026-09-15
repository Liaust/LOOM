package runtime

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"loom.local/loom/internal/projectquiescence"
)

type blockingWatchedRootRunner struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (runner *blockingWatchedRootRunner) Kind() string { return KindWatchedRoot }

func (runner *blockingWatchedRootRunner) RunOnce(ctx context.Context, _ Store, _ Env, _ WorkerInstance) (RunResult, error) {
	runner.calls.Add(1)
	select {
	case runner.started <- struct{}{}:
	default:
	}
	select {
	case <-runner.release:
		return RunResult{Status: RunStatusSucceeded, HealthStatus: WorkerStatusHealthy}, nil
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	}
}

func TestProjectArchiveWorkerLockDrainsRunAndReloadsDisabledInstance(t *testing.T) {
	store := NewStore(t.TempDir())
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("backend"), Kind: KindWatchedRoot, Enabled: true, ConfigHash: "sha256:test", ConfigJSON: []byte(`{"root_key":"backend"}`)}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	runner := &blockingWatchedRootRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	registry := NewRegistry(runner)
	runDone := make(chan error, 1)
	go func() {
		_, err := registry.RunOnce(context.Background(), store, Env{}, instance.WorkerKey, "corr-active")
		runDone <- err
	}()
	<-runner.started

	disableDone := make(chan error, 1)
	go func() {
		stale := instance
		stale.Enabled = false
		disableDone <- store.SaveInstance(stale)
	}()
	select {
	case err := <-disableDone:
		t.Fatalf("disable did not drain active run: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(runner.release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if err := <-disableDone; err != nil {
		t.Fatal(err)
	}

	output, err := registry.RunOnce(context.Background(), store, Env{}, instance.WorkerKey, "corr-stale-supervisor")
	if err != nil {
		t.Fatal(err)
	}
	if output.Run.Status != RunStatusSkipped || runner.calls.Load() != 1 {
		t.Fatalf("stale RunOnce reopened disabled worker: status=%s calls=%d", output.Run.Status, runner.calls.Load())
	}
}

func TestProjectArchiveWorkerLockCancellationStartsNoRun(t *testing.T) {
	store := NewStore(t.TempDir())
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("backend"), Kind: KindWatchedRoot, Enabled: true}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	release, err := store.AcquireWorkerExecutionLock(context.Background(), instance.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	runner := &blockingWatchedRootRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	_, err = NewRegistry(runner).RunOnce(ctx, store, Env{}, instance.WorkerKey, "corr-cancel")
	if !errors.Is(err, context.DeadlineExceeded) || runner.calls.Load() != 0 {
		t.Fatalf("cancelled worker wait = %v calls=%d", err, runner.calls.Load())
	}
}

func TestProjectArchiveStateDirectoriesPreserveRootModeAndRejectSymlinks(t *testing.T) {
	t.Run("preserves configured root mode", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := NewStore(root).Ensure(); err != nil {
			t.Fatal(err)
		}
		assertPathMode(t, root, 0o750)
		for _, path := range []string{
			filepath.Join(root, "project-archive-quiescence"),
			filepath.Join(root, "project-archive-quiescence", "locks"),
			filepath.Join(root, "project-archive-quiescence", "fences"),
			filepath.Join(root, "project-archive-quiescence", "receipts"),
		} {
			assertPathMode(t, path, 0o700)
		}
	})

	t.Run("rejects root symlink", func(t *testing.T) {
		parent := t.TempDir()
		realRoot := t.TempDir()
		linkedRoot := filepath.Join(parent, "runtime")
		if err := os.Symlink(realRoot, linkedRoot); err != nil {
			t.Fatal(err)
		}
		if err := NewStore(linkedRoot).PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err == nil {
			t.Fatal("publication followed a runtime-root symlink")
		}
		if _, err := os.Lstat(filepath.Join(realRoot, "project-archive-quiescence")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("root symlink target was modified: %v", err)
		}
	})

	t.Run("rejects data root ancestor symlink", func(t *testing.T) {
		parent := t.TempDir()
		external := t.TempDir()
		if err := os.Symlink(external, filepath.Join(parent, "linked-parent")); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(parent, "linked-parent", "runtime")
		if err := NewStore(root).PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err == nil {
			t.Fatal("publication followed a data-root ancestor symlink")
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatalf("data-root ancestor symlink target was modified: entries=%v err=%v", entries, err)
		}
	})

	t.Run("rejects archive ancestor symlink", func(t *testing.T) {
		root := t.TempDir()
		external := t.TempDir()
		if err := os.Symlink(external, filepath.Join(root, "project-archive-quiescence")); err != nil {
			t.Fatal(err)
		}
		if err := NewStore(root).PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err == nil {
			t.Fatal("publication followed an archive-ancestor symlink")
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatalf("archive ancestor symlink target was modified: entries=%v err=%v", entries, err)
		}
	})
}

func TestProjectArchivePublicationPreservesFenceAndReceiptConflicts(t *testing.T) {
	tests := []struct {
		name    string
		publish func(Store, []byte) error
	}{
		{name: "fence", publish: func(store Store, raw []byte) error {
			return store.PublishProjectArchiveFence(projectquiescence.TargetKindWatchedRoot, "worker-one", raw)
		}},
		{name: "receipt", publish: func(store Store, raw []byte) error {
			return store.PublishProjectArchiveReceipt("archive_test", raw)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var targetPath string
			conflict := []byte("conflicting-existing-state")
			store := NewStore(t.TempDir())
			store.beforeProjectArchivePublish = func(path string) {
				targetPath = path
				if err := os.WriteFile(path, conflict, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := test.publish(store, []byte("new-state")); !errors.Is(err, fs.ErrExist) {
				t.Fatalf("publication conflict = %v, want fs.ErrExist", err)
			}
			raw, err := os.ReadFile(targetPath)
			if err != nil || !bytes.Equal(raw, conflict) {
				t.Fatalf("existing conflict was replaced: raw=%q err=%v", raw, err)
			}
		})
	}
}

func TestProjectArchivePublicationAndReadRejectDirectoryReplacement(t *testing.T) {
	t.Run("publication archive ancestor replacement", func(t *testing.T) {
		root := t.TempDir()
		store := NewStore(root)
		if err := store.Ensure(); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		archiveRoot := filepath.Join(root, "project-archive-quiescence")
		store.beforeProjectArchivePublish = func(string) {
			if err := os.Rename(archiveRoot, archiveRoot+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, archiveRoot); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err == nil {
			t.Fatal("publication accepted replaced archive ancestor")
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatalf("replacement target was modified: entries=%v err=%v", entries, err)
		}
	})

	t.Run("publication runtime root replacement", func(t *testing.T) {
		parent := t.TempDir()
		root := filepath.Join(parent, "runtime")
		if err := os.Mkdir(root, 0o750); err != nil {
			t.Fatal(err)
		}
		store := NewStore(root)
		if err := store.Ensure(); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		store.beforeProjectArchivePublish = func(string) {
			if err := os.Rename(root, root+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, root); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err == nil {
			t.Fatal("publication accepted replaced runtime root")
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatalf("replacement root was modified: entries=%v err=%v", entries, err)
		}
	})

	t.Run("read archive ancestor replacement", func(t *testing.T) {
		root := t.TempDir()
		store := NewStore(root)
		if err := store.PublishProjectArchiveReceipt("archive_test", []byte("receipt")); err != nil {
			t.Fatal(err)
		}
		external := t.TempDir()
		archiveRoot := filepath.Join(root, "project-archive-quiescence")
		store.afterProjectArchiveReadOpen = func(string) {
			if err := os.Rename(archiveRoot, archiveRoot+".moved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(external, archiveRoot); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := store.ReadProjectArchiveReceipt("archive_test"); err == nil {
			t.Fatal("read accepted replaced archive ancestor")
		}
	})
}

func assertPathMode(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %#o, want %#o", path, info.Mode().Perm(), want)
	}
}
