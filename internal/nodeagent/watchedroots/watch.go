package watchedroots

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type NativeWatch struct {
	watcher   *fsnotify.Watcher
	cancel    context.CancelFunc
	done      chan struct{}
	healthy   atomic.Bool
	closeOnce sync.Once
	root      atomic.Pointer[ValidatedRoot]
}

// Keep installed topology for pacing-only edits, but publish hints using the
// current exact configuration receipt. Material scope changes still restart it.
func (w *NativeWatch) UpdateConfiguration(raw json.RawMessage) bool {
	old := w.root.Load()
	var config RootConfig
	if old == nil || json.Unmarshal(raw, &config) != nil {
		return false
	}
	config = NormalizeRootConfig(config)
	config.Scan.MaxHashFileBytes = min(config.Scan.MaxHashFileBytes, old.SafeRoot.MaxFileBytes)
	if ContentConfigHash(config) != ContentConfigHash(old.Config) {
		return false
	}
	next := *old
	next.Config, next.ConfigHash = config, ConfigHash(config)
	w.root.Store(&next)
	return true
}

// This is a timestamped observation, not a substitute for daemon liveness.
type NativeWatchObservation struct {
	State      string    `json:"state"`
	Reason     string    `json:"reason,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

func (s Store) LoadWatchObservation(rootKey string) (NativeWatchObservation, error) {
	var observation NativeWatchObservation
	err := readJSONFile(filepath.Join(s.RootDir(rootKey), "watch-observation.json"), &observation)
	return observation, err
}

func (s Store) saveWatchObservation(rootKey, state, reason string) error {
	if err := s.EnsureRoot(rootKey); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(s.RootDir(rootKey), "watch-observation.json"), NativeWatchObservation{State: state, Reason: reason, ObservedAt: time.Now().UTC()}, 0o600)
}

func (w *NativeWatch) Healthy() bool { return w.healthy.Load() }
func (w *NativeWatch) Close() error {
	w.closeOnce.Do(func() { w.cancel(); <-w.done })
	return nil
}

func StartNativeWatch(ctx context.Context, store Store, root ValidatedRoot, wake func()) (*NativeWatch, error) {
	if wake == nil {
		wake = func() {}
	}
	if !root.Config.Watch.Enabled || root.Config.Watch.Backend != WatchBackendAuto || !root.RootReachable {
		_ = store.saveWatchObservation(root.Config.RootKey, "polling", "native_watch_unavailable")
		return nil, errors.New("native watch disabled or root unavailable")
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = store.saveWatchObservation(root.Config.RootKey, "polling", "native_watch_registration_failed")
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &NativeWatch{watcher: watcher, cancel: cancel, done: make(chan struct{})}
	w.root.Store(&root)
	directories := map[string]bool{}
	// Observe the parent too: replacing the root must not silently orphan the watch.
	if err = watcher.Add(filepath.Dir(root.RootPath)); err == nil {
		err = addNativeDirectories(ctx, watcher, root, root.RootPath, directories)
	}
	if err != nil {
		cancel()
		_ = watcher.Close()
		_ = store.saveWatchObservation(root.Config.RootKey, "polling", "native_watch_registration_failed")
		return nil, err
	}
	if err := store.MarkRescanRequired(root.Config.RootKey, "native_watch_startup"); err != nil {
		cancel()
		_ = watcher.Close()
		return nil, err
	}
	if err := store.publishSourceHints(root, []DirtyHint{{HintKind: DirtyHintRescanRequired}}); err != nil {
		cancel()
		_ = watcher.Close()
		return nil, err
	}
	if err := store.saveWatchObservation(root.Config.RootKey, "active", ""); err != nil {
		cancel()
		_ = watcher.Close()
		return nil, err
	}
	w.healthy.Store(true)
	go w.run(ctx, store, root, directories, wake)
	wake()
	return w, nil
}

// Persistent watch coverage is not a per-turn scan page. OS resource failures
// still fall back to polling, without changing source policy or kernel limits.
const nativeDirectoryWatchLimit = 8192

func addNativeDirectories(ctx context.Context, watcher *fsnotify.Watcher, root ValidatedRoot, start string, known ...map[string]bool) error {
	directories := map[string]bool{}
	if len(known) > 0 && known[0] != nil {
		directories = known[0]
	}
	registered := map[string]bool{}
	for _, path := range watcher.WatchList() {
		registered[path] = true
	}
	return filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root.RootPath {
			rel := relativeFromPath(root.RootPath, path)
			if !mayMatchDescendant(root.Config.Include, rel) {
				return filepath.SkipDir
			}
			if excluded, _ := excludedByCurrentFilePolicy(root, rel, true); excluded {
				return filepath.SkipDir
			}
			if hasHiddenSegment(rel) && root.Config.Scan.HiddenPolicy == HiddenPolicyExcludeByDefault && !isHiddenAcceptancePath(rel) {
				return filepath.SkipDir
			}
		}
		if registered[path] {
			directories[path] = true
			return nil
		}
		if len(registered) >= nativeDirectoryWatchLimit {
			return errors.New("native directory watch budget exceeded")
		}
		if err := watcher.Add(path); err != nil {
			return err
		}
		registered[path] = true
		directories[path] = true
		return nil
	})
}

func (w *NativeWatch) run(ctx context.Context, store Store, root ValidatedRoot, directories map[string]bool, wake func()) {
	defer close(w.done)
	defer w.watcher.Close()
	defer w.healthy.Store(false)
	pending := map[string]DirtyHint{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var deadline <-chan time.Time
	state, reason := "stopped", "native_watch_stopped"
	defer func() { _ = store.saveWatchObservation(root.Config.RootKey, state, reason) }()
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		hints := make([]DirtyHint, 0, len(pending))
		for _, hint := range pending {
			hints = append(hints, hint)
		}
		if err := store.AddDirtyHints(root.Config.RootKey, hints); err != nil {
			return err
		}
		if err := store.publishSourceHints(root, hints); err != nil {
			return err
		}
		pending = map[string]DirtyHint{}
		wake()
		return nil
	}
	fail := func() {
		state, reason = "polling", "native_watch_failed"
		_ = store.MarkRescanRequired(root.Config.RootKey, "native_watch_failed")
		wake()
	}
	for {
		select {
		case <-ctx.Done():
			_ = flush()
			return
		case <-deadline:
			deadline = nil
			if err := flush(); err != nil {
				fail()
				return
			}
		case err, ok := <-w.watcher.Errors:
			if !ok || err != nil {
				fail()
				return
			}
		case event, ok := <-w.watcher.Events:
			if !ok {
				fail()
				return
			}
			if current := w.root.Load(); current != nil {
				root = *current
			}
			name := filepath.Clean(event.Name)
			if name == root.RootPath && event.Has(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) {
				fail()
				return
			}
			rel, err := filepath.Rel(root.RootPath, name)
			if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			rel = filepath.ToSlash(rel)
			kind := DirtyHintModified
			if event.Has(fsnotify.Chmod) {
				kind = DirtyHintMetadataChanged
			}
			if event.Has(fsnotify.Create) {
				kind = DirtyHintCreated
			}
			if event.Has(fsnotify.Remove | fsnotify.Rename) {
				kind = DirtyHintDeleted
			}
			info, statErr := os.Lstat(name)
			liveDirectory := statErr == nil && info.IsDir()
			// The backend may already have removed its watch for a deleted directory.
			isDir := liveDirectory || directories[name]
			if filepath.Base(name) == ".loomignore" {
				// Refresh policy and watch topology after the owner's next scan.
				fail()
				return
			}
			if excluded, _ := excludedByCurrentFilePolicy(root, rel, isDir); excluded {
				continue
			}
			if event.Has(fsnotify.Remove | fsnotify.Rename) {
				for watched := range directories {
					if watched == name || strings.HasPrefix(watched, name+string(filepath.Separator)) {
						if err := w.watcher.Remove(watched); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
							fail()
							return
						}
						delete(directories, watched)
					}
				}
			}
			if liveDirectory && event.Has(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) {
				if err := addNativeDirectories(ctx, w.watcher, root, name, directories); err != nil {
					fail()
					return
				}
			}
			if isDir {
				// Catch pre-registration children and retained descendants at this prefix.
				kind = DirtyHintSubtree
			}
			observation := ObservePath(root, rel)
			pending = MergeDirtyHints(pending, []DirtyHint{{RelativePath: rel, HintKind: kind, Source: "native", LastSeenAt: time.Now().UTC(), Observation: &observation}})
			if len(pending) >= 256 {
				pending = map[string]DirtyHint{"": {HintKind: DirtyHintRescanRequired, Source: "native", LastErrorCode: "native_hint_overflow"}}
				if err := flush(); err != nil {
					fail()
					return
				}
			}
			if deadline == nil {
				timer.Reset(50 * time.Millisecond)
				deadline = timer.C
			}
		}
	}
}

// Native events set exact stability deadlines; an unavailable backend keeps the
// declared cadence as a bounded polling fallback, without claiming deletion.
func NextNativeReconcile(store Store, root ValidatedRoot, now time.Time, lastStarted time.Time, watching bool, interval time.Duration) (time.Time, error) {
	cp, err := store.LoadCheckpoint(root.Config.RootKey)
	if err != nil && !IsNotExist(err) {
		return time.Time{}, err
	}
	if lastStarted.IsZero() {
		return now, nil
	}
	retry := lastStarted.Add(interval)
	// Full safety scans and queued output retries are separate deadlines.
	workRetry := lastStarted.Add(min(interval, time.Minute))
	full, _ := time.ParseDuration(root.Config.Scan.FullRescanInterval)
	next := retry
	if watching && coverageConfigMatches(root, cp) && cp.LastFullRescanAt != nil && !cp.PendingRescan {
		next = cp.LastFullRescanAt.Add(full)
		if next.Before(lastStarted) {
			next = retry
		}
		if !cp.OutputsCurrent && workRetry.Before(next) {
			next = workRetry
		}
	}
	if cp.PendingRescan || len(cp.DeferredHashPaths) > 0 {
		due := workRetry
		if cp.RootReachable && cp.LastErrorCode == "" {
			if cp.FullScanID != "" || len(cp.DeferredHashPaths) > 0 {
				due = lastStarted.Add(time.Second)
			}
			if cp.NextHashReadyAt != nil && cp.NextHashReadyAt.After(lastStarted) {
				due = *cp.NextHashReadyAt
			}
		}
		if due.Before(next) {
			next = due
		}
	}
	if cp.OutputContinuationReady && cp.RootReachable && cp.LastErrorCode == "" {
		if due := lastStarted.Add(time.Second); due.Before(next) {
			next = due
		}
	}
	hints, err := store.ListDirtyHints(root.Config.RootKey)
	if err != nil {
		return time.Time{}, err
	}
	stability, _ := time.ParseDuration(root.Config.Scan.StabilityWindow)
	for _, hint := range hints {
		delay := stability
		if hint.Observation != nil && hint.Observation.Safe && hint.Observation.Exists && hint.Observation.Kind == PathKindFile {
			delay = effectiveHashStability(root.Config.Scan, hint.RelativePath, hint.Observation.SizeBytes, hint.Observation.Kind)
		}
		due := hint.LastSeenAt.Add(delay)
		if scan, ok := cp.SubtreeScans[hint.RelativePath]; ok && scan.Generation == hint.Generation && due.Before(scan.RetryAt) {
			due = scan.RetryAt
		}
		if hint.HintKind == DirtyHintRescanRequired {
			due = now
		}
		// Retained hints already attempted by the last scan must not spin on a
		// failed/unavailable source. A later event keeps its own exact deadline.
		if !hint.LastSeenAt.After(lastStarted) && due.Before(retry) {
			due = retry
		}
		if due.Before(next) {
			next = due
		}
	}
	return next, nil
}
