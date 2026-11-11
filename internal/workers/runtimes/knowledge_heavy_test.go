package runtimes

import (
	"context"
	"encoding/json"
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

func TestKnowledgeHeavyDrainPressurePauseKeepsDeadlineAndObservation(t *testing.T) {
	at := time.Now().UTC()
	next := at.Add(30 * time.Second)
	result, err := drainKnowledgeHeavy(t.Context(), 600*time.Second, workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 60}, func() time.Time { return at }, func() (knowledge.HeavyExecutorRunResult, error) {
		return knowledge.HeavyExecutorRunResult{Observation: knowledge.HeavyStageObservation{Admission: &workers.ResourceTurnAdmission{Reason: "memory_headroom", SampledAt: at, NextEligibleAt: &next}}}, nil
	})
	if err != nil || result.Counters["claimed"] != 0 || result.Counters["pressure_samples"] != 1 || result.Counters["pressure_deferred"] != 1 || result.NextRunAfter == nil || !result.NextRunAfter.Equal(next) {
		t.Fatalf("%+v %v", result, err)
	}
	var summary struct {
		Stages []knowledge.HeavyExecutorRunResult `json:"stages"`
	}
	if err = json.Unmarshal(result.ResultSummary, &summary); err != nil || len(summary.Stages) != 1 || summary.Stages[0].Observation.Admission.Reason != "memory_headroom" {
		t.Fatal("lost unclaimed pressure reason")
	}
}

func TestKnowledgeHeavyDrainCountsDurableUnitsAndYield(t *testing.T) {
	at := time.Now().UTC()
	calls := 0
	result, err := drainKnowledgeHeavy(t.Context(), 600*time.Second, workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30}, func() time.Time { return at }, func() (knowledge.HeavyExecutorRunResult, error) {
		calls++
		at = at.Add(31 * time.Second)
		return knowledge.HeavyExecutorRunResult{Claimed: 1, Observation: knowledge.HeavyStageObservation{Observed: json.RawMessage(`{"units":8}`)}}, nil
	})
	if err != nil || calls != 1 || result.Counters["completed_units"] != 8 || result.Counters["yielded_turns"] != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	old := json.RawMessage(`{"schema_version":"knowledge_heavy.config.v1","lease_duration_seconds":600,"resource":{"schema_version":"knowledge.heavy_resource_policy.v1","timeout_seconds":600,"max_units":200,"max_input_bytes":134217728,"max_temp_bytes":536870912}}`)
	config, err := parseKnowledgeHeavyConfig(old)
	if err != nil || config.Resource.TurnSeconds != 30 {
		t.Fatalf("saved config stopped working: %+v %v", config, err)
	}
}
