package runtimes

import (
	"context"
	"errors"
	"testing"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

func TestKnowledgeHeavyRuntimeContract(t *testing.T) {
	runtime := NewKnowledgeHeavyRuntime(nil)
	if runtime.Kind() != workers.KindKnowledgeHeavy {
		t.Fatalf("kind = %q", runtime.Kind())
	}
	descriptor := runtime.Describe()
	if descriptor.Status != workers.KindStatusActive {
		t.Fatalf("status = %q", descriptor.Status)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 || instances[0].WorkerKey != "main.knowledge_heavy" {
		t.Fatalf("instances = %#v", instances)
	}
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeHeavyDrainBoundsAndIdleCadence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		count   int
		elapsed time.Duration
		failed  bool
		want    int64
	}{
		{"idle", 0, 0, false, 0},
		{"short backlog", 3, time.Second, false, 3},
		{"item bound", 20, 0, false, 8},
		{"admission window", 20, 31 * time.Second, false, 1},
		{"long stage", 20, 600 * time.Second, false, 1},
		{"failure stops draining", 20, time.Second, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now().UTC()
			ctx, cancel := context.WithDeadline(t.Context(), at.Add(660*time.Second))
			defer cancel()
			calls := 0
			policy := workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30}
			result, err := drainKnowledgeHeavy(ctx, 600*time.Second, policy, func() time.Time { return at }, func() (knowledge.HeavyExecutorRunResult, error) {
				calls++
				if calls > tc.count {
					return knowledge.HeavyExecutorRunResult{}, nil
				}
				at = at.Add(tc.elapsed)
				if tc.failed {
					return knowledge.HeavyExecutorRunResult{Claimed: 1, Failed: 1}, nil
				}
				return knowledge.HeavyExecutorRunResult{Claimed: 1, Completed: 1}, nil
			})
			if err != nil || result.Counters["claimed"] != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			delay := 30 * time.Second
			if !tc.failed && tc.count >= 8 {
				delay = 5 * time.Second
			}
			if result.NextRunAfter == nil || !result.NextRunAfter.Equal(at.Add(delay)) {
				t.Fatalf("idle/batch must honor policy after finishing: %v", result.NextRunAfter)
			}
			if tc.failed && result.Counters["failed"] != 1 {
				t.Fatal("lost recorded stage failure")
			}
		})
	}
}

func TestKnowledgeHeavyDrainDeadlineCancellationAndError(t *testing.T) {
	at := time.Now().UTC()
	ctx, cancel := context.WithDeadline(t.Context(), at.Add(604*time.Second))
	defer cancel()
	called := false
	stage := func() (knowledge.HeavyExecutorRunResult, error) {
		called = true
		return knowledge.HeavyExecutorRunResult{}, errors.New("stage unavailable")
	}
	manual := workers.TickPolicy{Mode: workers.TickModeManual}
	result, err := drainKnowledgeHeavy(ctx, 600*time.Second, manual, func() time.Time { return at }, stage)
	if err != nil || called || result.NextRunAfter != nil {
		t.Fatal("insufficient full stage/cleanup budget admitted work or scheduled manual worker", err)
	}
	cancel()
	_, err = drainKnowledgeHeavy(ctx, 600*time.Second, manual, func() time.Time { return at }, stage)
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled call admitted work", err)
	}
	_, err = drainKnowledgeHeavy(t.Context(), 600*time.Second, manual, func() time.Time { return at }, stage)
	if err == nil || !called {
		t.Fatal("stage error was lost")
	}
}
