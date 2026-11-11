package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

func (s Store) healthSummaryWakePath() string {
	return filepath.Join(s.DataDir, "summaries", "worker-health.wake.json")
}

type queueReportWatch struct {
	cancel  context.CancelFunc
	done    chan struct{}
	healthy atomic.Bool
}

func (w *queueReportWatch) Healthy() bool { return w.healthy.Load() }
func (w *queueReportWatch) Close() error  { w.cancel(); <-w.done; return nil }

func (QueueReporterRunner) Watch(ctx context.Context, store Store, _ Env, _ WorkerInstance, wake func()) (WakeSource, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	syncRoot := filepath.Join(store.DataDir, "sync")
	for _, dir := range []string{store.DataDir, filepath.Join(store.DataDir, "summaries"), store.instancesDir(), syncRoot} {
		if err := watcher.Add(dir); err != nil && !(dir == syncRoot && errors.Is(err, fs.ErrNotExist)) {
			_ = watcher.Close()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	watch := &queueReportWatch{cancel: cancel, done: make(chan struct{})}
	watch.healthy.Store(true)
	go func() {
		defer close(watch.done)
		defer watcher.Close()
		defer watch.healthy.Store(false)
		for {
			select {
			case <-ctx.Done():
				return
			case <-watcher.Errors:
				return
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				path := filepath.Clean(event.Name)
				if path == syncRoot && event.Has(fsnotify.Create) {
					if watcher.Add(syncRoot) != nil {
						return
					}
					wake()
				}
				if path == filepath.Join(syncRoot, "records.db") || path == store.fileQueueCachePath() || path == store.healthSummaryWakePath() || filepath.Dir(path) == store.instancesDir() {
					if event.Has(fsnotify.Create | fsnotify.Write | fsnotify.Rename | fsnotify.Remove) {
						wake()
					}
				}
			}
		}
	}()
	return watch, nil
}

func (QueueReporterRunner) NextRunAt(store Store, _ Env, instance WorkerInstance, now, lastStarted time.Time, _ bool) (time.Time, error) {
	if lastStarted.IsZero() {
		return now, nil
	}
	if summary, err := store.LoadLatestSummary(); err == nil && summary.RetryAt != nil && now.Before(*summary.RetryAt) {
		return *summary.RetryAt, nil
	}
	checked := lastStarted
	if checkpoint, err := store.LoadCheckpoint(instance.WorkerKey); err == nil {
		var metadata struct {
			CheckedAt time.Time `json:"summary_checked_at"`
		}
		if json.Unmarshal(checkpoint.Metadata, &metadata) == nil && !metadata.CheckedAt.IsZero() {
			checked = metadata.CheckedAt
		}
	}
	interval := time.Duration(instance.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	next := lastStarted.Add(interval)
	for _, path := range []string{store.fileQueueCachePath(), store.healthSummaryWakePath(), store.instancesDir(), filepath.Join(store.DataDir, "sync", "records.db")} {
		info, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return time.Time{}, err
		}
		if info.ModTime().After(checked) {
			due := info.ModTime().Add(100 * time.Millisecond)
			if due.Before(next) {
				next = due
			}
		}
	}
	return next, nil
}
