package notesworkspacesync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

func splitRelative(relative string) []string {
	if relative == "" {
		return nil
	}
	return strings.Split(relative, "/")
}
func inventoryContains(prefix, relative string) bool {
	return prefix == "" || relative == prefix || strings.HasPrefix(relative, prefix+"/")
}
func inventoryPath(relative string) bool {
	return relative == "" || IsSourcePath(relative) || textPath(relative+"/probe.md")
}

// SourceWatch is a lease on hints, not source admission or deletion evidence.
type SourceWatch interface {
	Healthy() bool
	Close() error
}

// OpenSourceWatch must reuse the current owner for WatchKey. It may call
// StartSourceWatch only after proving there is no existing recursive watch for
// this admitted scope. Its context must be daemon lifetime, not a Run timeout.
type OpenSourceWatch func(InventoryScope, func(context.Context, string) error) (SourceWatch, error)

type nativeSourceWatch struct {
	watcher *fsnotify.Watcher
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	healthy bool
	scope   InventoryScope
	persist func(context.Context, string) error
	wake    func()
}

func (w *nativeSourceWatch) Healthy() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.healthy }
func (w *nativeSourceWatch) Close() error  { w.once.Do(func() { w.cancel(); <-w.done }); return nil }

// StartSourceWatch is for uncovered roots only. Every directory is registered
// before enumeration of its children; arrivals register before catch-up. Bounds
// limit watcher registration itself, never source per-file support.
func StartSourceWatch(ctx context.Context, scope InventoryScope, maxDirs int, persist func(context.Context, string) error, wake func()) (SourceWatch, error) {
	if maxDirs < 1 || persist == nil || !filepath.IsAbs(scope.Root) || !inventoryPath(scope.Prefix) {
		return nil, ErrHeld
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &nativeSourceWatch{watcher: watcher, cancel: cancel, done: make(chan struct{}), scope: scope, persist: persist, wake: wake, healthy: true}
	start := filepath.Join(scope.Root, filepath.FromSlash(scope.Prefix))
	if _, err = noFollowInfo(scope.Root, scope.Prefix); err == nil {
		err = watcher.Add(filepath.Dir(start))
	}
	if err == nil {
		err = w.add(ctx, start, maxDirs)
	}
	if err == nil {
		err = persist(ctx, "")
	}
	if err != nil {
		cancel()
		watcher.Close()
		return nil, err
	}
	go w.run(ctx, maxDirs)
	if wake != nil {
		wake()
	}
	return w, nil
}
func (w *nativeSourceWatch) add(ctx context.Context, start string, maxDirs int) error {
	known := map[string]bool{}
	for _, path := range w.watcher.WatchList() {
		known[path] = true
	}
	var visit func(string) error
	visit = func(directory string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(w.scope.Root, directory)
		if err != nil {
			return err
		}
		if relative == "." {
			relative = ""
		} else {
			relative = filepath.ToSlash(relative)
		}
		if !inventoryContains(w.scope.Prefix, relative) || !inventoryPath(relative) {
			return nil
		}
		info, err := noFollowInfo(w.scope.Root, relative)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return ErrHeld
		}
		if !known[directory] {
			if len(known) >= maxDirs+1 {
				return errors.New("Notes directory watch budget exceeded")
			}
			if err := w.watcher.Add(directory); err != nil {
				return err
			}
			known[directory] = true
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() && inventoryPath(filepath.ToSlash(filepath.Join(relative, entry.Name()))) {
				if err := visit(filepath.Join(directory, entry.Name())); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(start)
}
func (w *nativeSourceWatch) run(ctx context.Context, maxDirs int) {
	defer close(w.done)
	defer w.watcher.Close()
	defer func() { w.mu.Lock(); w.healthy = false; w.mu.Unlock() }()
	commit := func(relative string) bool {
		if w.persist(ctx, relative) != nil {
			return false
		}
		if w.wake != nil {
			w.wake()
		}
		return true
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			commit("")
			return // overflow/failed watch triggers durable repair
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			start := filepath.Join(w.scope.Root, filepath.FromSlash(w.scope.Prefix))
			if event.Name == start && event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				commit("")
				return
			}
			relative, err := filepath.Rel(w.scope.Root, event.Name)
			if err != nil {
				continue
			}
			relative = filepath.ToSlash(relative)
			if relative == "." {
				relative = ""
			}
			if !inventoryContains(w.scope.Prefix, relative) || !inventoryPath(relative) {
				continue
			}
			if event.Op&fsnotify.Create != 0 {
				if info, err := noFollowInfo(w.scope.Root, relative); err == nil && info.IsDir() {
					if err := w.add(ctx, event.Name, maxDirs); err != nil {
						commit("")
						return
					}
				}
			}
			if !commit(relative) {
				return
			}
		}
	}
}

// SourceHint is also the common producer hook for an existing exact watched
// root. It commits before wake; generation mismatch cannot touch active rows.
func (r *Runtime) SourceHint(ctx context.Context, scope InventoryScope, relative string) error {
	r.hintMu.Lock()
	defer r.hintMu.Unlock()
	if r.DB == nil {
		return ErrHeld
	}
	if r.Lifetime != nil && recoveryRequested(r.Config.ReplicaDir) {
		// Capture freezes SQL/native state. The request-removal event forces a
		// fresh inventory catch-up; do not mutate its fingerprint mid-capture.
		r.sourceRevision.Add(1)
		return nil
	}
	if err := (SourceInventory{Store: SQLStore{DB: r.DB}}).Hint(ctx, scope, relative, time.Now().UTC()); err != nil {
		return err
	}
	if relative == "" {
		r.layoutDependencies.invalidate(layoutPathKey(scope.Collection, ""), true, true)
	} else {
		r.layoutDependencies.invalidate(layoutPathKey(scope.Collection, relative), true, true)
	}
	r.sourceRevision.Add(1)
	if r.Wake != nil {
		r.Wake()
	}
	return nil
}
