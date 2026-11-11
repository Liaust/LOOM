package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type EnvProvider func() (Env, error)

type Supervisor struct {
	Store       Store
	Registry    Registry
	EnvProvider EnvProvider
	WorkerKeys  []string
	// Atomic publications are observed through their parent directories.
	WakeFiles []string
	// Coalesced delivery hints for specific workers, including during a run.
	WakeWorkers   map[string]string
	CorrelationID string
	ErrOut        io.Writer
}

func (s Supervisor) Run(ctx context.Context) error {
	return s.run(ctx, time.Minute)
}

func (s Supervisor) run(ctx context.Context, refreshInterval time.Duration) error {
	if err := s.Store.Ensure(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type schedule struct {
		started      time.Time
		done         chan struct{}
		watch        WakeSource
		watchConfig  string
		watchAttempt time.Time
		requested    bool
		recheck      bool
		deadline     time.Time
		instance     WorkerInstance
	}
	schedules := map[string]*schedule{}
	completed := make(chan struct{}, 1)
	wakes := make(chan struct{}, 1)
	var wakeMu sync.Mutex
	wakeKeys := map[string]bool{}
	wakeWorker := func(key string) {
		wakeMu.Lock()
		wakeKeys[key] = true
		wakeMu.Unlock()
		select {
		case wakes <- struct{}{}:
		default:
		}
	}
	var wg sync.WaitGroup
	defer func() {
		cancel()
		for _, state := range schedules {
			if state.watch != nil {
				_ = state.watch.Close()
			}
		}
		wg.Wait()
	}()
	if refreshInterval <= 0 {
		refreshInterval = time.Minute
	}
	var watcher *fsnotify.Watcher
	var events <-chan fsnotify.Event
	var watchErrors <-chan error
	wakeFiles := map[string]bool{}
	for _, path := range s.WakeFiles {
		if path != "" {
			wakeFiles[filepath.Clean(path)] = true
		}
	}
	for path := range s.WakeWorkers {
		wakeFiles[filepath.Clean(path)] = true
	}
	watchedDirs := map[string]bool{}
	defer func() {
		if watcher != nil {
			_ = watcher.Close()
		}
	}()
	startWatch := func() {
		if watcher == nil {
			candidate, err := fsnotify.NewWatcher()
			if err == nil {
				err = candidate.Add(s.Store.instancesDir())
				if err != nil {
					_ = candidate.Close()
				}
			}
			if err != nil {
				s.logf("worker configuration watch unavailable; using bounded refresh: %v\n", err)
				return
			}
			watcher, events, watchErrors = candidate, candidate.Events, candidate.Errors
			watchedDirs[s.Store.instancesDir()] = true
		}
		// Missing queue directories can be created by the first worker turn.
		// Retry registration at the ordinary bounded refresh, not in a busy loop.
		for path := range wakeFiles {
			dir := filepath.Dir(path)
			if watchedDirs[dir] {
				continue
			}
			if err := watcher.Add(dir); err != nil {
				s.logf("worker publication watch unavailable; using bounded refresh: %v\n", err)
				continue
			}
			watchedDirs[dir] = true
		}
	}
	stopWatch := func() {
		if watcher != nil {
			_ = watcher.Close()
		}
		watcher, events, watchErrors = nil, nil, nil
		watchedDirs = map[string]bool{}
	}
	// Register before the initial snapshot so concurrent CLI publications wake us.
	startWatch()
	var instances []WorkerInstance
	reload := true
	recheckAll := false
	refreshAt := time.Now().Add(refreshInterval)
	timer := time.NewTimer(0)
	defer timer.Stop()
	for ctx.Err() == nil {
		now := time.Now()
		if !now.Before(refreshAt) {
			startWatch()
			reload = true
			recheckAll = true
			refreshAt = now.Add(refreshInterval)
		}
		if reload {
			var err error
			instances, err = s.instances()
			if err != nil {
				return err
			}
			reload = false
		}
		if recheckAll {
			for _, state := range schedules {
				state.recheck = true
			}
			recheckAll = false
		}
		var env Env
		var envErr error
		envLoaded := false
		loadEnv := func() (Env, error) {
			if !envLoaded {
				env, envErr = s.EnvProvider()
				envLoaded = true
			}
			return env, envErr
		}
		next := refreshAt
		current := map[string]bool{}
		for _, instance := range instances {
			current[instance.WorkerKey] = instance.Enabled
			if !instance.Enabled {
				continue
			}
			state := schedules[instance.WorkerKey]
			if state == nil {
				state = &schedule{recheck: true}
				schedules[instance.WorkerKey] = state
			}
			if !sameScheduledInstance(state.instance, instance) {
				state.recheck = true
				state.instance = instance
			}
			eventRunner, eventDriven := s.Registry.runners[instance.Kind].(EventRunner)
			if eventDriven {
				if state.watch != nil && state.watch.Healthy() && state.watchConfig != string(instance.ConfigJSON) {
					if updater, ok := state.watch.(interface{ UpdateConfiguration(json.RawMessage) bool }); ok && updater.UpdateConfiguration(instance.ConfigJSON) {
						state.watchConfig = string(instance.ConfigJSON)
					}
				}
				if state.watch != nil && (!state.watch.Healthy() || state.watchConfig != string(instance.ConfigJSON)) {
					_ = state.watch.Close()
					state.watch = nil
					state.watchAttempt = time.Time{}
					state.recheck = true
				}
				if state.watch == nil && !now.Before(state.watchAttempt.Add(refreshInterval)) {
					state.watchAttempt, state.watchConfig = now, string(instance.ConfigJSON)
					key := instance.WorkerKey
					env, err := loadEnv()
					if err == nil {
						state.watch, err = eventRunner.Watch(ctx, s.Store, env, instance, func() { wakeWorker(key) })
					}
					state.recheck = true
					if err != nil {
						s.logf("%s native watch unavailable; polling fallback: %v\n", key, err)
					}
				}
			}
			if state.done != nil {
				select {
				case <-state.done:
					state.done = nil
					state.recheck = true
				default:
					continue
				}
			}
			interval := time.Duration(instance.IntervalSeconds) * time.Second
			if interval <= 0 {
				interval = 30 * time.Second
			}
			due := state.deadline
			// Completions and native hints invalidate only their own eligibility.
			// Queue/configuration publications and bounded refresh cover missed hints.
			if state.recheck || due.IsZero() || !now.Before(due) {
				due = state.started.Add(interval)
				if eventDriven {
					env, err := loadEnv()
					if err == nil {
						var observed time.Time
						observed, err = eventRunner.NextRunAt(s.Store, env, instance, now, state.started, state.watch != nil && state.watch.Healthy())
						if err == nil {
							due = observed
						}
					}
					if err != nil {
						s.logf("%s event deadline unavailable; polling fallback: %v\n", instance.WorkerKey, err)
					}
				}
				state.deadline, state.recheck = due, false
			}
			if state.requested {
				due = now
			}
			if now.Before(due) {
				if due.Before(next) {
					next = due
				}
				continue
			}
			state.started, state.done, state.requested = time.Now(), make(chan struct{}), false
			wg.Add(1)
			go func(instance WorkerInstance, done chan struct{}) {
				defer wg.Done()
				defer func() {
					close(done)
					select {
					case completed <- struct{}{}:
					default:
					}
				}()
				s.runWorker(ctx, instance)
			}(instance, state.done)
		}
		// Keep an in-flight identity until it finishes, even if temporarily disabled.
		for key, state := range schedules {
			if current[key] {
				continue
			}
			if state.done != nil {
				select {
				case <-state.done:
				default:
					continue
				}
			}
			delete(schedules, key)
			if state.watch != nil {
				_ = state.watch.Close()
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(max(0, time.Until(next)))
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-timer.C:
			case <-completed:
			case <-wakes:
				wakeMu.Lock()
				for key := range wakeKeys {
					if state := schedules[key]; state != nil {
						state.recheck = true
					}
				}
				clear(wakeKeys)
				wakeMu.Unlock()
			case event, ok := <-events:
				name := filepath.Clean(event.Name)
				if !ok || (watchedDirs[name] && event.Has(fsnotify.Remove|fsnotify.Rename)) {
					stopWatch()
					reload = true
					recheckAll = true
				} else if filepath.Dir(name) == s.Store.instancesDir() && strings.HasSuffix(filepath.Base(name), ".json") {
					reload = true
				} else if wakeFiles[name] && event.Has(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Remove) {
					if key := s.WakeWorkers[name]; key != "" {
						state := schedules[key]
						if state == nil {
							state = &schedule{}
							schedules[key] = state
						}
						state.requested = true
					} else {
						recheckAll = true
					}
					// Recheck eligibility without reloading worker instances or claiming work.
				} else {
					// Routine receipts and temporary files do not recheck all deadlines.
					continue
				}
			case err, ok := <-watchErrors:
				if ok {
					s.logf("worker configuration watch failed; using bounded refresh: %v\n", err)
				}
				stopWatch()
				reload = true
				recheckAll = true
			}
			break
		}
	}
	return nil
}

func sameScheduledInstance(a, b WorkerInstance) bool {
	// Publication receipts alone do not change a worker's deadline.
	return a.WorkerKey == b.WorkerKey && a.Kind == b.Kind && a.Enabled == b.Enabled &&
		a.IntervalSeconds == b.IntervalSeconds && a.ConfigHash == b.ConfigHash && string(a.ConfigJSON) == string(b.ConfigJSON)
}

func (s Supervisor) instances() ([]WorkerInstance, error) {
	if len(s.WorkerKeys) == 0 {
		return s.Store.LoadInstances()
	}
	instances := make([]WorkerInstance, 0, len(s.WorkerKeys))
	for _, workerKey := range s.WorkerKeys {
		instance, err := s.Store.LoadInstance(workerKey)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	return instances, nil
}

func (s Supervisor) runWorker(ctx context.Context, instance WorkerInstance) {
	env, err := s.EnvProvider()
	if err != nil {
		s.logf("%s env failed: %v\n", instance.WorkerKey, err)
		return
	}
	if _, err := s.Registry.RunOnce(ctx, s.Store, env, instance.WorkerKey, s.CorrelationID); err != nil {
		s.logf("%s failed: %v\n", instance.WorkerKey, err)
	}
}

func (s Supervisor) logf(format string, args ...any) {
	if s.ErrOut == nil {
		return
	}
	_, _ = fmt.Fprintf(s.ErrOut, format, args...)
}
