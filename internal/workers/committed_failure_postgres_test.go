package workers

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type committedFailureRuntime struct {
	testRuntime
	result RunResult
}

func (r committedFailureRuntime) RunOnce(context.Context, RunContext) (RunResult, error) {
	return r.result, nil
}

func TestCommittedWorkerFailurePreservesCheckpointAndOutcomePostgres(t *testing.T) {
	// This fixture must receive an owned disposable database, never loom_main.
	_ = wakePostgresDB(t)
	db, req, instance, _ := workerPolicyPostgresFixture(t)
	next := time.Now().UTC().Add(5 * time.Second).Truncate(time.Microsecond)
	runtime := committedFailureRuntime{testRuntime: newTestRuntime(instance.WorkerKind), result: RunResult{
		Status: RunStatusFailed, Retryable: true, NextRunAfter: &next,
		ResultSummary:     json.RawMessage(`{"reason":"projection_source_path_invalid","processing_incomplete":true}`),
		Counters:          map[string]int64{"completed": 1, "failed": 1},
		CheckpointUpdates: []CheckpointUpdate{{Key: "default", SchemaVersion: "test.v1", Value: json.RawMessage(`{"retained_sequence":7}`), Metadata: json.RawMessage(`{}`)}},
	}}
	registry := NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, registry, nil)
	result, err := service.RunOnce(t.Context(), req, instance.WorkerKey, RunOnceInput{Reason: "preserve committed failed processing", IdempotencyKey: "committed-failure-" + instance.WorkerInstanceID})
	if err != nil || result.Run.RunStatus != RunStatusFailed || result.Health.HealthStatus != HealthDegraded || !result.Health.AttentionRequired || result.Health.LastSuccessAt != nil {
		t.Fatalf("failure presented as success: %+v %v", result, err)
	}
	if len(result.Checkpoints) != 1 || string(result.Checkpoints[0].CheckpointJSON) != `{"retained_sequence": 7}` {
		// PostgreSQL jsonb formatting is not the checkpoint identity.
		var value map[string]int
		if len(result.Checkpoints) != 1 || json.Unmarshal(result.Checkpoints[0].CheckpointJSON, &value) != nil || value["retained_sequence"] != 7 {
			t.Fatalf("committed failure lost checkpoint: %+v", result.Checkpoints)
		}
	}
	var counts map[string]int
	if json.Unmarshal(result.Run.CountersJSON, &counts) != nil || counts["completed"] != 1 || counts["failed"] != 1 || len(result.Run.ResultSummaryJSON) < 3 {
		t.Fatal("failed outcome discarded retained results", result.Run)
	}
	detail, err := service.InspectWorker(t.Context(), instance.WorkerKey)
	if err != nil || detail.Instance.NextRunAfter == nil || !detail.Instance.NextRunAfter.Equal(next) || detail.Instance.ConsecutiveFails != 1 {
		t.Fatalf("exact retry/failure state was replaced: %+v %v", detail.Instance, err)
	}
}
