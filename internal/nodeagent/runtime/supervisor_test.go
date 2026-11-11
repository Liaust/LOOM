package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingWatchedRootRunner struct{ calls atomic.Int32 }

type dispatchWakeRunner struct {
	started chan struct{}
	finish  chan struct{}
	calls   atomic.Int32
	active  atomic.Int32
	overlap atomic.Bool
}

func (*dispatchWakeRunner) Kind() string { return KindPoll }
func (r *dispatchWakeRunner) RunOnce(ctx context.Context, _ Store, _ Env, _ WorkerInstance) (RunResult, error) {
	if r.active.Add(1) != 1 {
		r.overlap.Store(true)
	}
	defer r.active.Add(-1)
	n := r.calls.Add(1)
	r.started <- struct{}{}
	if n == 1 {
		select {
		case <-r.finish:
		case <-ctx.Done():
		}
	}
	return RunResult{Status: RunStatusSucceeded, HealthStatus: WorkerStatusHealthy}, nil
}

func TestSupervisorDispatchWakeBypassesIntervalWithoutOverlapOrIdlePolling(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.SaveInstance(WorkerInstance{WorkerKey: WorkerKeyPoll, Kind: KindPoll, Enabled: true, IntervalSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	r := &dispatchWakeRunner{started: make(chan struct{}, 8), finish: make(chan struct{})}
	s := Supervisor{Store: store, Registry: NewRegistry(r), WakeWorkers: map[string]string{store.PollWakePath(): WorkerKeyPoll}, EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx, time.Hour) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	wait := func() {
		t.Helper()
		select {
		case <-r.started:
		case <-time.After(3 * time.Second):
			t.Fatal("dispatch waited for periodic poll")
		}
	}
	wait()
	if err := store.RequestPoll(); err != nil {
		t.Fatal(err)
	}
	// Let the hint arrive while the initial worker is still running.
	time.Sleep(80 * time.Millisecond)
	if r.calls.Load() != 1 {
		t.Fatal("overlapped running worker")
	}
	close(r.finish)
	wait()
	if r.overlap.Load() {
		t.Fatal("concurrent poll execution")
	}
	time.Sleep(100 * time.Millisecond)
	count := r.calls.Load()
	if err := os.WriteFile(filepath.Join(store.DataDir, "unrelated.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if r.calls.Load() != count {
		t.Fatal("idle or unrelated files triggered polling")
	}
	if err := store.RequestPoll(); err != nil {
		t.Fatal(err)
	}
	wait()
}

func TestSupervisorDeadlinesAndNativeConfigurationWake(t *testing.T) {
	store := NewStore(t.TempDir())
	runner := &countingWatchedRootRunner{}
	supervisor := Supervisor{Store: store, Registry: NewRegistry(runner), EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.run(ctx, time.Hour) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("deadline"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 1}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runner.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.calls.Load() < 2 {
		t.Fatal("worker deadline depended on the inventory-refresh interval")
	}
	// A separate CLI-style publication must be observed without waiting an hour.
	instance.Enabled = false
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	calls := runner.calls.Load()
	time.Sleep(1100 * time.Millisecond)
	if runner.calls.Load() != calls {
		t.Fatal("native configuration wake did not disable worker")
	}
}

func TestSupervisorDiscoversNewWorkersAndChangedIntervals(t *testing.T) {
	store := NewStore(t.TempDir())
	runner := &countingWatchedRootRunner{}
	supervisor := Supervisor{Store: store, Registry: NewRegistry(runner), EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.run(ctx, 10*time.Millisecond) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	waitCalls := func(want int32) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for runner.calls.Load() < want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if got := runner.calls.Load(); got != want {
			t.Fatalf("runner calls = %d, want %d", got, want)
		}
	}
	// Start without workers, then enroll one without restarting the supervisor.
	time.Sleep(30 * time.Millisecond)
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("new-root"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	waitCalls(1)
	instance.IntervalSeconds = 1
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	waitCalls(2)
	instance.Enabled = false
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if runner.calls.Load() != 2 {
		t.Fatal("disabled worker ran")
	}
}

func (runner *countingWatchedRootRunner) Kind() string { return KindWatchedRoot }
func (runner *countingWatchedRootRunner) RunOnce(context.Context, Store, Env, WorkerInstance) (RunResult, error) {
	runner.calls.Add(1)
	return RunResult{Status: RunStatusSucceeded, HealthStatus: WorkerStatusHealthy}, nil
}

func TestProjectArchiveStaleSupervisorCopyCannotReopenWorker(t *testing.T) {
	store := NewStore(t.TempDir())
	stale := WorkerInstance{WorkerKey: WatchedRootWorkerKey("backend"), Kind: KindWatchedRoot, Enabled: true}
	if err := store.SaveInstance(stale); err != nil {
		t.Fatal(err)
	}
	disabled := stale
	disabled.Enabled = false
	if err := store.SaveInstance(disabled); err != nil {
		t.Fatal(err)
	}
	runner := &countingWatchedRootRunner{}
	supervisor := Supervisor{Store: store, Registry: NewRegistry(runner), EnvProvider: func() (Env, error) { return Env{}, nil }}
	supervisor.runWorker(context.Background(), stale)
	if runner.calls.Load() != 0 {
		t.Fatalf("stale supervisor invoked watched-root runner %d times", runner.calls.Load())
	}
}

type wakeTestRunner struct {
	wake    func()
	ready   chan struct{}
	started chan struct{}
	finish  chan struct{}
	pending atomic.Bool
	calls   atomic.Int32
	active  atomic.Int32
	overlap atomic.Bool
}

type testWakeSource struct{ once sync.Once }

func (*testWakeSource) Healthy() bool { return true }
func (*testWakeSource) Close() error  { return nil }
func (*wakeTestRunner) Kind() string  { return KindWatchedRoot }
func (r *wakeTestRunner) Watch(_ context.Context, _ Store, _ Env, _ WorkerInstance, wake func()) (WakeSource, error) {
	r.wake = wake
	close(r.ready)
	return &testWakeSource{}, nil
}
func (r *wakeTestRunner) NextRunAt(_ Store, _ Env, _ WorkerInstance, now, last time.Time, _ bool) (time.Time, error) {
	if last.IsZero() || r.pending.Load() {
		return now, nil
	}
	return now.Add(time.Hour), nil
}
func (r *wakeTestRunner) RunOnce(ctx context.Context, _ Store, _ Env, _ WorkerInstance) (RunResult, error) {
	if r.active.Add(1) != 1 {
		r.overlap.Store(true)
	}
	defer r.active.Add(-1)
	r.pending.Store(false)
	n := r.calls.Add(1)
	r.started <- struct{}{}
	if n == 1 {
		select {
		case <-r.finish:
		case <-ctx.Done():
		}
	}
	return RunResult{Status: RunStatusSucceeded, HealthStatus: WorkerStatusHealthy}, nil
}

func TestSupervisorWakeDuringRunRechecksWithoutOverlap(t *testing.T) {
	store := NewStore(t.TempDir())
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("wake"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	runner := &wakeTestRunner{ready: make(chan struct{}), started: make(chan struct{}, 3), finish: make(chan struct{})}
	supervisor := Supervisor{Store: store, Registry: NewRegistry(runner), EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.run(ctx, time.Hour) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-runner.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("watch not started")
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial scan not started")
	}
	runner.pending.Store(true)
	runner.wake()
	close(runner.finish)
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight wake lost")
	}
	time.Sleep(50 * time.Millisecond)
	if runner.overlap.Load() || runner.calls.Load() != 2 {
		t.Fatalf("calls=%d overlap=%v", runner.calls.Load(), runner.overlap.Load())
	}
}

type publicationWakeRunner struct {
	wakeTestRunner
	queue       string
	evaluations atomic.Int32
}

func (r *publicationWakeRunner) NextRunAt(store Store, env Env, instance WorkerInstance, now, last time.Time, watching bool) (time.Time, error) {
	r.evaluations.Add(1)
	if !last.IsZero() {
		if _, err := os.Stat(r.queue); os.IsNotExist(err) {
			return now.Add(time.Hour), nil
		} else if err != nil {
			return time.Time{}, err
		}
	}
	return r.wakeTestRunner.NextRunAt(store, env, instance, now, last, watching)
}

func TestSupervisorAtomicPublicationWake(t *testing.T) {
	store := NewStore(t.TempDir())
	queueDir := t.TempDir()
	queue := filepath.Join(queueDir, "outbox.json")
	instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("publication"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600}
	if err := store.SaveInstance(instance); err != nil {
		t.Fatal(err)
	}
	runner := &publicationWakeRunner{wakeTestRunner: wakeTestRunner{ready: make(chan struct{}), started: make(chan struct{}, 3), finish: make(chan struct{})}, queue: queue}
	close(runner.finish)
	supervisor := Supervisor{Store: store, Registry: NewRegistry(runner), WakeFiles: []string{queue}, EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- supervisor.run(ctx, time.Hour) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial turn not started")
	}
	time.Sleep(100 * time.Millisecond)
	evaluations := runner.evaluations.Load()
	runner.pending.Store(true)
	if err := os.WriteFile(filepath.Join(queueDir, "unrelated.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
		t.Fatal("unrelated publication woke a worker")
	case <-time.After(100 * time.Millisecond):
	}
	if runner.evaluations.Load() != evaluations {
		t.Fatal("unrelated publication rechecked worker deadlines")
	}
	temporary := filepath.Join(queueDir, "outbox.json.tmp")
	if err := os.WriteFile(temporary, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, queue); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("atomic queue publication waited for the hourly refresh")
	}
	if runner.overlap.Load() {
		t.Fatal("publication overlapped an existing turn")
	}
}

type deadlineCacheRunner struct {
	mu          sync.Mutex
	wakes       map[string]func()
	evaluations map[string]int
	evaluated   chan string
	ran         chan string
	deadline    time.Time
}

func (*deadlineCacheRunner) Kind() string { return KindWatchedRoot }
func (r *deadlineCacheRunner) Watch(_ context.Context, _ Store, _ Env, instance WorkerInstance, wake func()) (WakeSource, error) {
	r.mu.Lock()
	r.wakes[instance.WorkerKey] = wake
	r.mu.Unlock()
	return &testWakeSource{}, nil
}
func (r *deadlineCacheRunner) NextRunAt(_ Store, _ Env, instance WorkerInstance, _ time.Time, last time.Time, _ bool) (time.Time, error) {
	r.mu.Lock()
	r.evaluations[instance.WorkerKey]++
	r.mu.Unlock()
	r.evaluated <- instance.WorkerKey
	if !last.IsZero() {
		return last.Add(time.Hour), nil
	}
	return r.deadline, nil
}
func (r *deadlineCacheRunner) RunOnce(_ context.Context, _ Store, _ Env, instance WorkerInstance) (RunResult, error) {
	r.ran <- instance.WorkerKey
	return RunResult{Status: RunStatusSucceeded, HealthStatus: WorkerStatusHealthy}, nil
}
func (r *deadlineCacheRunner) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evaluations[key]
}

func TestSupervisorCachesDeadlinesAcrossUnrelatedWakesAndCompletion(t *testing.T) {
	store := NewStore(t.TempDir())
	a := WorkerInstance{WorkerKey: WatchedRootWorkerKey("a"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600}
	b := a
	b.WorkerKey = WatchedRootWorkerKey("b")
	for _, instance := range []WorkerInstance{a, b, {WorkerKey: WorkerKeyPoll, Kind: KindPoll, Enabled: true, IntervalSeconds: 3600}} {
		if err := store.SaveInstance(instance); err != nil {
			t.Fatal(err)
		}
	}
	r := &deadlineCacheRunner{wakes: map[string]func(){}, evaluations: map[string]int{}, evaluated: make(chan string, 32), ran: make(chan string, 8), deadline: time.Now().Add(time.Hour)}
	poll := &dispatchWakeRunner{started: make(chan struct{}, 8), finish: make(chan struct{})}
	publication := filepath.Join(t.TempDir(), "queue.json")
	s := Supervisor{Store: store, Registry: NewRegistry(r, poll), WakeFiles: []string{publication}, WakeWorkers: map[string]string{store.PollWakePath(): WorkerKeyPoll}, EnvProvider: func() (Env, error) { return Env{}, nil }}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx, time.Hour) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	waitEvaluation := func(key string) {
		t.Helper()
		for {
			select {
			case got := <-r.evaluated:
				if got == key {
					return
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("no readiness evaluation for %s", key)
			}
		}
	}
	waitPoll := func() {
		t.Helper()
		select {
		case <-poll.started:
		case <-time.After(3 * time.Second):
			t.Fatal("poll did not run")
		}
	}
	waitEvaluation(b.WorkerKey)
	waitPoll()
	close(poll.finish)
	if err := store.RequestPoll(); err != nil {
		t.Fatal(err)
	}
	waitPoll()
	if r.count(a.WorkerKey) != 1 || r.count(b.WorkerKey) != 1 {
		t.Fatal("unrelated poll wake/completion invalidated root readiness")
	}
	r.mu.Lock()
	wakeA := r.wakes[a.WorkerKey]
	r.mu.Unlock()
	wakeA()
	waitEvaluation(a.WorkerKey)
	if r.count(b.WorkerKey) != 1 {
		t.Fatal("native wake for a invalidated b")
	}
	if err := store.SaveInstance(a); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if r.count(a.WorkerKey) != 2 || r.count(b.WorkerKey) != 1 {
		t.Fatal("unchanged instance publication invalidated cached deadlines")
	}
	if err := os.WriteFile(publication, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	waitEvaluation(b.WorkerKey)
	if r.count(a.WorkerKey) < 3 {
		t.Fatal("queue publication did not invalidate delivery readiness")
	}
}

func TestSupervisorCachedDeadlineAndBoundedRefresh(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "refresh"}[refresh], func(t *testing.T) {
			store := NewStore(t.TempDir())
			key := WatchedRootWorkerKey("cached")
			if err := store.SaveInstance(WorkerInstance{WorkerKey: key, Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600}); err != nil {
				t.Fatal(err)
			}
			r := &deadlineCacheRunner{wakes: map[string]func(){}, evaluations: map[string]int{}, evaluated: make(chan string, 32), ran: make(chan string, 8), deadline: time.Now().Add(100 * time.Millisecond)}
			interval := time.Hour
			if refresh {
				interval, r.deadline = 50*time.Millisecond, time.Now().Add(time.Hour)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- (Supervisor{Store: store, Registry: NewRegistry(r), EnvProvider: func() (Env, error) { return Env{}, nil }}).run(ctx, interval)
			}()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			if refresh {
				for range 2 {
					select {
					case <-r.evaluated:
					case <-time.After(3 * time.Second):
						t.Fatal("bounded refresh lost")
					}
				}
			} else {
				select {
				case <-r.ran:
				case <-time.After(3 * time.Second):
					t.Fatal("cached deadline lost")
				}
			}
		})
	}
}

type configurableWakeSource struct {
	mu      sync.Mutex
	config  string
	accept  bool
	healthy atomic.Bool
	closed  atomic.Bool
	updated chan string
}

func (w *configurableWakeSource) Healthy() bool { return w.healthy.Load() }
func (w *configurableWakeSource) Close() error {
	w.closed.Store(true)
	w.healthy.Store(false)
	return nil
}
func (w *configurableWakeSource) UpdateConfiguration(raw json.RawMessage) bool {
	w.mu.Lock()
	if w.accept {
		w.config = string(raw)
	}
	w.mu.Unlock()
	w.updated <- string(raw)
	return w.accept
}
func (w *configurableWakeSource) currentConfig() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.config
}

type configurableWatchRunner struct {
	*deadlineCacheRunner
	accept  bool
	watches chan *configurableWakeSource
}

func (r *configurableWatchRunner) Watch(_ context.Context, _ Store, _ Env, instance WorkerInstance, _ func()) (WakeSource, error) {
	w := &configurableWakeSource{config: string(instance.ConfigJSON), accept: r.accept, updated: make(chan string, 8)}
	w.healthy.Store(true)
	r.watches <- w
	return w, nil
}

func TestSupervisorOffersChangedRawConfigurationToHealthyWatch(t *testing.T) {
	for _, mode := range []string{"retain", "reopen", "unhealthy"} {
		t.Run(mode, func(t *testing.T) {
			store := NewStore(t.TempDir())
			instance := WorkerInstance{WorkerKey: WatchedRootWorkerKey("config"), Kind: KindWatchedRoot, Enabled: true, IntervalSeconds: 3600, ConfigJSON: json.RawMessage(`{"revision":1}`)}
			if err := store.SaveInstance(instance); err != nil {
				t.Fatal(err)
			}
			r := &configurableWatchRunner{
				deadlineCacheRunner: &deadlineCacheRunner{evaluations: map[string]int{}, evaluated: make(chan string, 32), ran: make(chan string, 8), deadline: time.Now().Add(time.Hour)},
				accept:              mode == "retain", watches: make(chan *configurableWakeSource, 8),
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- (Supervisor{Store: store, Registry: NewRegistry(r), EnvProvider: func() (Env, error) { return Env{}, nil }}).run(ctx, time.Hour)
			}()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			waitWatch := func() *configurableWakeSource {
				t.Helper()
				select {
				case w := <-r.watches:
					return w
				case <-time.After(3 * time.Second):
					t.Fatal("watch registration was not observed")
					return nil
				}
			}
			original := waitWatch()
			if mode == "unhealthy" {
				original.healthy.Store(false)
			}
			instance.ConfigJSON = json.RawMessage(`{"revision":2}`)
			if err := store.SaveInstance(instance); err != nil {
				t.Fatal(err)
			}
			published, err := store.LoadInstance(instance.WorkerKey)
			if err != nil {
				t.Fatal(err)
			}
			wantConfig := string(published.ConfigJSON)
			if mode != "unhealthy" {
				select {
				case raw := <-original.updated:
					if raw != wantConfig {
						t.Fatalf("watch received stale raw config: %s", raw)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("healthy watch was not offered changed configuration")
				}
			}
			if mode == "retain" {
				select {
				case <-r.watches:
					t.Fatal("accepted configuration restarted the watch")
				case <-time.After(100 * time.Millisecond):
				}
				if original.closed.Load() || original.currentConfig() != wantConfig {
					t.Fatal("retained watch still has the old root payload")
				}
				if err := store.SaveInstance(instance); err != nil {
					t.Fatal(err)
				}
				select {
				case <-original.updated:
					t.Fatal("supervisor retained an old configuration snapshot")
				case <-time.After(100 * time.Millisecond):
				}
			} else {
				replacement := waitWatch()
				if !original.closed.Load() || replacement.currentConfig() != wantConfig {
					t.Fatal("watch fallback did not reopen with current configuration")
				}
				if mode == "unhealthy" && len(original.updated) != 0 {
					t.Fatal("unhealthy watch was offered in-place configuration")
				}
			}
		})
	}
}
