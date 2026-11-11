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

func TestKnowledgeRootsCadenceAndReceipts(t *testing.T) {
	r := NewKnowledgeRootsRuntime(nil)
	d := r.Describe()
	instance := r.DefaultInstances()[0]
	policy, err := workers.ParseTickPolicy(instance.TickPolicyJSON)
	if err != nil || policy.IntervalSeconds != 900 || d.MayTouchFilesystem || instance.WorkerKind != workers.KindKnowledgeRoots {
		t.Fatalf("maintenance ownership/cadence: %+v %v", instance, err)
	}
	if err := r.ValidateConfig(t.Context(), instance.ConfigJSON); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateConfig(t.Context(), json.RawMessage(`{"schema_version":"wrong"}`)); err == nil {
		t.Fatal("accepted an unknown config")
	}
	called := 0
	r.Reconcile = func(_ context.Context, input knowledge.SourceRootReconcileInput) (knowledge.SourceRootReconcileResult, error) {
		called++
		if !input.DiscoverFromStore || input.DryRun {
			t.Fatal("maintenance did not use the real root owner")
		}
		return knowledge.SourceRootReconcileResult{Applied: 3, Skipped: []knowledge.SourceRootSkip{{Reason: "not_notes"}}}, nil
	}
	before := time.Now().UTC()
	result, err := r.RunOnce(t.Context(), workers.RunContext{Instance: workers.WorkerInstance{ConfigJSON: instance.ConfigJSON, TickPolicyJSON: instance.TickPolicyJSON}})
	if err != nil || called != 1 || result.Status != workers.RunStatusSucceeded || result.Counters["roots_applied"] != 3 || len(result.CheckpointUpdates) != 1 || result.NextRunAfter == nil || result.NextRunAfter.Before(before.Add(15*time.Minute)) {
		t.Fatalf("repair completion: %+v %v", result, err)
	}
	r.Reconcile = func(context.Context, knowledge.SourceRootReconcileInput) (knowledge.SourceRootReconcileResult, error) {
		return knowledge.SourceRootReconcileResult{Applied: 1}, context.DeadlineExceeded
	}
	result, err = r.RunOnce(t.Context(), workers.RunContext{Instance: workers.WorkerInstance{ConfigJSON: instance.ConfigJSON, TickPolicyJSON: instance.TickPolicyJSON}})
	if !errors.Is(err, context.DeadlineExceeded) || len(result.CheckpointUpdates) != 0 || result.Status == workers.RunStatusSucceeded {
		t.Fatalf("failed repair manufactured success: %+v %v", result, err)
	}
}
