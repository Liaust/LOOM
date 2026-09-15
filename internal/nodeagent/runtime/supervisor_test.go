package runtime

import (
	"context"
	"sync/atomic"
	"testing"
)

type countingWatchedRootRunner struct{ calls atomic.Int32 }

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
