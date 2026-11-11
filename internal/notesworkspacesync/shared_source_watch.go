package notesworkspacesync

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

func SharedSourceRepairInterval(dataDir string, scope InventoryScope) time.Duration {
	instance, err := noderuntime.NewStore(dataDir).LoadInstance(noderuntime.WatchedRootWorkerKey(scope.WatchKey))
	if err != nil || instance.IntervalSeconds <= 0 {
		return time.Minute
	}
	return time.Duration(instance.IntervalSeconds) * time.Second
}

type sharedSourceWatch struct {
	watcher  *fsnotify.Watcher
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	healthy  atomic.Bool
	watching atomic.Bool
	reason   atomic.Value
}

func (w *sharedSourceWatch) Healthy() bool  { return w.healthy.Load() }
func (w *sharedSourceWatch) Watching() bool { return w.watching.Load() }
func (w *sharedSourceWatch) Close() error   { w.once.Do(func() { w.cancel(); <-w.done }); return nil }
func (w *sharedSourceWatch) WatchReason() string {
	reason, _ := w.reason.Load().(string)
	return reason
}

type sharedWatchFailure string

func (e sharedWatchFailure) Error() string { return string(e) }
func (e sharedWatchFailure) Unwrap() error { return ErrHeld }

func sourceWatchDetail(err error) string {
	var failure sharedWatchFailure
	if errors.As(err, &failure) {
		return string(failure)
	}
	return "watch_registration_failed"
}

func recoverableOwnerWatch(reason string) bool {
	return reason == "watch_owner_not_active" || reason == "watch_observation_unavailable" || reason == "watch_feed_unavailable"
}

func sourceWatchObserving(watch SourceWatch) bool {
	observer, ok := watch.(interface{ Watching() bool })
	return ok && observer.Watching()
}

func sourceWatchLeaseDetail(watch SourceWatch) string {
	if diagnostic, ok := watch.(interface{ WatchReason() string }); ok {
		return diagnostic.WatchReason()
	}
	return "watch_lease_unhealthy"
}

// Subscribe to the existing owner's bounded durable hints, not its source tree.
// The scanner may clear its own hints without losing the Notes invalidation.
func StartSharedSourceWatch(ctx context.Context, dataDir string, scope InventoryScope, persist func(context.Context, string) error) (SourceWatch, error) {
	if scope.WatchKey == "" || persist == nil {
		return nil, sharedWatchFailure("watch_identity_missing")
	}
	store := watchedroots.NewStore(dataDir)
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, sharedWatchFailure("watch_backend_unavailable")
	}
	if err = watcher.Add(store.RootDir(scope.WatchKey)); err != nil {
		watcher.Close()
		return nil, sharedWatchFailure("watch_owner_directory_unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &sharedSourceWatch{watcher: watcher, cancel: cancel, done: make(chan struct{})}
	seen := map[string]string{}
	read := func(initial bool) error {
		observation, err := store.LoadWatchObservation(scope.WatchKey)
		if err != nil {
			return sharedWatchFailure("watch_observation_unavailable")
		}
		if observation.State != "active" {
			return sharedWatchFailure("watch_owner_not_active")
		}
		feed, err := store.LoadSourceHints(scope.WatchKey)
		if err != nil {
			return sharedWatchFailure("watch_feed_unavailable")
		}
		if feed.RootPath != scope.Root {
			return sharedWatchFailure("watch_root_mismatch")
		}
		if feed.ConfigHash == "" || len(feed.Hints) > 4096 {
			return sharedWatchFailure("watch_feed_invalid")
		}
		recovering := !w.healthy.Load()
		w.healthy.Store(true)
		w.reason.Store("")
		if initial || recovering {
			if err := persist(ctx, ""); err != nil {
				return sharedWatchFailure("watch_hint_persistence_failed")
			}
		}
		next := make(map[string]string, len(feed.Hints))
		for key, hint := range feed.Hints {
			if hint.RootKey != scope.WatchKey || hint.Generation == "" {
				return sharedWatchFailure("watch_hint_identity_mismatch")
			}
			next[key] = hint.Generation
			if initial || seen[key] == hint.Generation {
				continue
			}
			relative := hint.RelativePath
			if relative != "" && !inventoryContains(scope.Prefix, relative) {
				if !inventoryContains(relative, scope.Prefix) {
					continue
				}
				relative = ""
			}
			if !inventoryPath(relative) {
				continue
			}
			if err := persist(ctx, relative); err != nil {
				return sharedWatchFailure("watch_hint_persistence_failed")
			}
		}
		seen = next
		return nil
	}
	if err = read(true); err != nil {
		w.healthy.Store(false)
		if !recoverableOwnerWatch(sourceWatchDetail(err)) {
			cancel()
			watcher.Close()
			return nil, err
		}
		w.reason.Store(sourceWatchDetail(err))
	}
	w.watching.Store(true)
	fail := func(reason string) {
		w.reason.Store(reason)
		w.healthy.Store(false)
		if !recoverableOwnerWatch(reason) {
			w.watching.Store(false)
		}
		_ = persist(ctx, "")
	}
	go func() {
		defer close(w.done)
		defer watcher.Close()
		defer w.healthy.Store(false)
		defer w.watching.Store(false)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-watcher.Errors:
				if ok {
					fail("watch_event_stream_failed")
				}
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				name := filepath.Base(event.Name)
				if name != "source-hints.json" && name != "watch-observation.json" {
					continue
				}
				if err := read(false); err != nil && !errors.Is(err, context.Canceled) {
					fail(sourceWatchDetail(err))
					if !recoverableOwnerWatch(sourceWatchDetail(err)) {
						return
					}
				}
			}
		}
	}()
	return w, nil
}
