package workers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type progressFailureRuntime struct {
	testRuntime
	t     *testing.T
	saved RunContext
}

func (r *progressFailureRuntime) RunOnce(ctx context.Context, run RunContext) (RunResult, error) {
	r.saved = run
	bad := run
	bad.Lease.Generation++
	if err := run.Service.RecordRunProgress(ctx, bad, json.RawMessage(`{"phase":"wrong"}`)); !errors.Is(err, ErrConflict) {
		r.t.Fatal("wrong fence accepted", err)
	}
	if err := run.Service.RecordRunProgress(ctx, run, json.RawMessage(`{"phase":"borg.create","status":"running","committed":false}`)); err != nil {
		r.t.Fatal(err)
	}
	if err := run.Service.RecordRunProgress(ctx, run, json.RawMessage(`{"oversize":"`+strings.Repeat("x", 17<<10)+`"}`)); !errors.Is(err, ErrInvalid) {
		r.t.Fatal("unbounded progress accepted", err)
	}
	return RunResult{ResultSummary: json.RawMessage(`{"phase":"create_interrupted","committed":false}`), Retryable: true,
		CheckpointUpdates: []CheckpointUpdate{{Key: "must-not-apply", SchemaVersion: "test.v1", Value: json.RawMessage(`{"accepted":true}`)}}}, context.DeadlineExceeded
}

func TestWorkerProgressFenceAndExecutionFailureEvidencePostgres(t *testing.T) {
	_ = wakePostgresDB(t)
	db, req, instance, _ := workerPolicyPostgresFixture(t)
	runtime := &progressFailureRuntime{testRuntime: newTestRuntime(instance.WorkerKind), t: t}
	registry := NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	service := NewService(db, registry, nil)
	result, err := service.RunOnce(t.Context(), req, instance.WorkerKey, RunOnceInput{Reason: "cloud progress/failure fence", IdempotencyKey: "progress-" + instance.WorkerInstanceID})
	if !errors.Is(err, context.DeadlineExceeded) || result.Run.RunStatus != RunStatusFailed || !result.Run.Retryable || len(result.Checkpoints) != 0 || result.Health.LastSuccessAt != nil {
		t.Fatalf("execution failure accepted or discarded: %+v %v", result, err)
	}
	var summary map[string]any
	if json.Unmarshal(result.Run.ResultSummaryJSON, &summary) != nil || summary["phase"] != "create_interrupted" || summary["committed"] != false {
		t.Fatal("lost structured failure", string(result.Run.ResultSummaryJSON))
	}
	if err := service.RecordRunProgress(t.Context(), runtime.saved, json.RawMessage(`{"phase":"late"}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("terminal history changed", err)
	}
}
