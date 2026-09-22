package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type countingWatchedRootRunner struct{ calls atomic.Int32 }

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
