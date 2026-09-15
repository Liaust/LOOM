package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

func (s Service) RunOnce(ctx context.Context, req requestctx.Context, workerRef string, input RunOnceInput) (RunOnceResult, error) {
	if s.DB == nil {
		return RunOnceResult{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if s.Registry == nil {
		return RunOnceResult{}, fmt.Errorf("%w: worker registry is required", ErrInvalid)
	}

	input, err := normalizeRunOnceInput(input)
	if err != nil {
		return RunOnceResult{}, err
	}

	instance, err := resolveWorkerInstance(ctx, s.DB, workerRef)
	if err != nil {
		return RunOnceResult{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ProjectID:    stringPointerValue(instance.ProjectID),
		ScopeID:      stringPointerValue(instance.ScopeID),
		ResourceKind: "worker",
		ResourceRef:  instance.WorkerKey,
	}); err != nil {
		return RunOnceResult{}, err
	}
	if !instance.Enabled {
		return RunOnceResult{}, fmt.Errorf("%w: worker %s is disabled", ErrConflict, instance.WorkerKey)
	}
	if instance.Paused {
		return RunOnceResult{}, fmt.Errorf("%w: worker %s is paused", ErrConflict, instance.WorkerKey)
	}
	runtime, ok := s.Registry.Get(instance.WorkerKind)
	if !ok {
		return RunOnceResult{}, fmt.Errorf("%w: runtime for worker kind %s is not registered", ErrNotFound, instance.WorkerKind)
	}
	if err := ValidateConfigObject(ctx, runtime, instance.ConfigJSON); err != nil {
		return RunOnceResult{}, err
	}
	kind, err := getWorkerKind(ctx, s.DB, instance.WorkerKind)
	if err != nil {
		return RunOnceResult{}, err
	}

	timeout := timeoutFromPolicy(instance.TimeoutPolicyJSON)
	deadline := time.Now().UTC().Add(timeout)
	leaseTTL := timeout + time.Minute

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return RunOnceResult{}, err
	}
	defer tx.Rollback()

	instance, err = lockWorkerInstance(ctx, tx, instance.WorkerInstanceID)
	if err != nil {
		return RunOnceResult{}, err
	}
	if !instance.Enabled {
		return RunOnceResult{}, fmt.Errorf("%w: worker %s is disabled", ErrConflict, instance.WorkerKey)
	}
	if instance.Paused {
		return RunOnceResult{}, fmt.Errorf("%w: worker %s is paused", ErrConflict, instance.WorkerKey)
	}
	if instance.CurrentRunID != nil {
		return RunOnceResult{}, fmt.Errorf("%w: worker %s has active run %s", ErrConflict, instance.WorkerKey, *instance.CurrentRunID)
	}
	if input.TriggerKind == TriggerSupervisorTick {
		fingerprint, err := TickPolicyFingerprint(instance.TickPolicyJSON)
		if err != nil {
			return RunOnceResult{}, err
		}
		if fingerprint != input.ExpectedPolicyFingerprint {
			return RunOnceResult{}, fmt.Errorf("%w: worker %s policy changed after supervisor selection", ErrConflict, instance.WorkerKey)
		}
		policy, err := ParseTickPolicy(instance.TickPolicyJSON)
		if err != nil {
			return RunOnceResult{}, err
		}
		if !policy.Due(input.ScheduleEvidenceAt, instance.NextRunAfter) {
			return RunOnceResult{}, fmt.Errorf("%w: worker %s is no longer due under its current policy", ErrConflict, instance.WorkerKey)
		}
	}

	control, err := createRunOnceControl(ctx, tx, req, instance, input)
	if err != nil {
		return RunOnceResult{}, err
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerControlRequested, "worker_control", control.WorkerControlID, ControlStatusRequested, map[string]any{
		"worker_instance_id": instance.WorkerInstanceID,
		"worker_key":         instance.WorkerKey,
		"control_kind":       ControlRunOnce,
	}); err != nil {
		return RunOnceResult{}, err
	}

	lease, err := acquireLease(ctx, tx, instance, holderID(req), LeaseHolderLoomd, leaseTTL)
	if err != nil {
		return RunOnceResult{}, err
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerLeaseAcquired, "worker_lease", lease.WorkerLeaseID, LeaseStatusActive, map[string]any{
		"worker_instance_id": instance.WorkerInstanceID,
		"worker_key":         instance.WorkerKey,
		"lease_generation":   lease.Generation,
	}); err != nil {
		return RunOnceResult{}, err
	}

	run, err := startRun(ctx, tx, req, instance, lease, input, deadline)
	if err != nil {
		return RunOnceResult{}, err
	}
	if err := attachLeaseRun(ctx, tx, lease, run.WorkerRunID); err != nil {
		return RunOnceResult{}, err
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerRunStarted, "worker_run", run.WorkerRunID, RunStatusRunning, map[string]any{
		"worker_instance_id": instance.WorkerInstanceID,
		"worker_key":         instance.WorkerKey,
		"worker_kind":        instance.WorkerKind,
	}); err != nil {
		return RunOnceResult{}, err
	}
	if _, err := upsertRunningHealth(ctx, tx, instance, run); err != nil {
		return RunOnceResult{}, err
	}
	checkpoints, err := loadCheckpoints(ctx, tx, instance.WorkerInstanceID)
	if err != nil {
		return RunOnceResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunOnceResult{}, err
	}

	runtimeResult, runErr := executeRuntimeOnce(ctx, runtime, RunContext{
		Service:        s,
		Instance:       instance,
		Kind:           kind,
		Run:            run,
		Lease:          lease,
		Checkpoints:    checkpoints,
		Request:        req,
		Logger:         s.Logger,
		StartedAt:      run.StartedAt,
		Deadline:       deadline,
		CorrelationID:  req.CorrelationID,
		IdempotencyKey: input.IdempotencyKey,
	}, timeout)

	completionCtx, cancelCompletion := detachedWorkerCompletionContext(ctx)
	defer cancelCompletion()
	completedRun, health, appliedCheckpoints, completedControl, completeErr := s.completeRunOnce(completionCtx, req, instance, run, lease, control, runtimeResult, runErr)
	if completeErr != nil {
		return RunOnceResult{}, completeErr
	}
	detail, err := s.InspectWorker(completionCtx, instance.WorkerInstanceID)
	if err != nil {
		return RunOnceResult{}, err
	}
	allCheckpoints, err := listCheckpoints(completionCtx, s.DB, instance.WorkerInstanceID)
	if err != nil {
		return RunOnceResult{}, err
	}
	if len(appliedCheckpoints) > 0 {
		allCheckpoints = appliedCheckpoints
	}

	return RunOnceResult{
		Worker:      detail,
		Run:         completedRun,
		Health:      health,
		Checkpoints: allCheckpoints,
		Control:     completedControl,
	}, runErr
}

const workerCompletionTimeout = 30 * time.Second

func detachedWorkerCompletionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), workerCompletionTimeout)
}

func executeRuntimeOnce(ctx context.Context, runtime Runtime, runCtx RunContext, timeout time.Duration) (result RunResult, runErr error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			runErr = fmt.Errorf("worker runtime panic: %v\n%s", recovered, string(debug.Stack()))
		}
		if runErr == nil && execCtx.Err() != nil {
			runErr = execCtx.Err()
		}
	}()
	result, runErr = runtime.RunOnce(execCtx, runCtx)
	return result, runErr
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s Service) completeRunOnce(ctx context.Context, req requestctx.Context, instance WorkerInstance, run WorkerRun, lease WorkerLease, control WorkerControl, runtimeResult RunResult, runErr error) (WorkerRun, WorkerHealth, []WorkerCheckpoint, *WorkerControl, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return WorkerRun{}, WorkerHealth{}, nil, nil, err
	}
	defer tx.Rollback()

	var checkpoints []WorkerCheckpoint
	if runErr == nil {
		checkpoints, err = applyCheckpointUpdates(ctx, tx, run, lease, runtimeResult.CheckpointUpdates)
		if err != nil {
			_ = tx.Rollback()
			return s.completeRunOnce(ctx, req, instance, run, lease, control, RunResult{}, err)
		}
	}

	var completedRun WorkerRun
	var health WorkerHealth
	var controlOut *WorkerControl
	if runErr != nil {
		var consecutiveFailures int
		completedRun, consecutiveFailures, err = failRun(ctx, tx, instance, run, lease, runErr)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		health, err = upsertFailedHealth(ctx, tx, instance, completedRun, consecutiveFailures, runErr)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		controlOut, err = markControlFailed(ctx, tx, control, completedRun, runErr)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerRunFailed, "worker_run", completedRun.WorkerRunID, RunStatusFailed, map[string]any{
			"worker_instance_id": instance.WorkerInstanceID,
			"worker_key":         instance.WorkerKey,
			"worker_kind":        instance.WorkerKind,
			"error":              json.RawMessage(errorJSON(runErr)),
		}); err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerControlFailed, "worker_control", control.WorkerControlID, ControlStatusFailed, map[string]any{
			"worker_run_id": completedRun.WorkerRunID,
		}); err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
	} else {
		completedRun, err = completeRun(ctx, tx, instance, run, lease, runtimeResult)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		health, err = upsertSucceededHealth(ctx, tx, instance, completedRun)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		controlOut, err = markControlApplied(ctx, tx, control, completedRun)
		if err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		for _, checkpoint := range checkpoints {
			if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerCheckpointUpdated, "worker_checkpoint", checkpoint.WorkerCheckpointID, "updated", map[string]any{
				"worker_instance_id": instance.WorkerInstanceID,
				"worker_key":         instance.WorkerKey,
				"worker_run_id":      completedRun.WorkerRunID,
				"checkpoint_key":     checkpoint.CheckpointKey,
			}); err != nil {
				return WorkerRun{}, WorkerHealth{}, nil, nil, err
			}
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerRunSucceeded, "worker_run", completedRun.WorkerRunID, RunStatusSucceeded, map[string]any{
			"worker_instance_id": instance.WorkerInstanceID,
			"worker_key":         instance.WorkerKey,
			"worker_kind":        instance.WorkerKind,
		}); err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerControlApplied, "worker_control", control.WorkerControlID, ControlStatusApplied, map[string]any{
			"worker_run_id": completedRun.WorkerRunID,
		}); err != nil {
			return WorkerRun{}, WorkerHealth{}, nil, nil, err
		}
	}

	if err := releaseLease(ctx, tx, lease); err != nil {
		return WorkerRun{}, WorkerHealth{}, nil, nil, err
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerLeaseReleased, "worker_lease", lease.WorkerLeaseID, LeaseStatusReleased, map[string]any{
		"worker_instance_id": instance.WorkerInstanceID,
		"worker_key":         instance.WorkerKey,
		"worker_run_id":      completedRun.WorkerRunID,
		"lease_generation":   lease.Generation,
	}); err != nil {
		return WorkerRun{}, WorkerHealth{}, nil, nil, err
	}
	if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerHealthUpdated, "worker_health", health.WorkerHealthID, health.HealthStatus, map[string]any{
		"worker_instance_id": instance.WorkerInstanceID,
		"worker_key":         instance.WorkerKey,
		"worker_run_id":      completedRun.WorkerRunID,
		"health_status":      health.HealthStatus,
		"severity":           health.Severity,
	}); err != nil {
		return WorkerRun{}, WorkerHealth{}, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return WorkerRun{}, WorkerHealth{}, nil, nil, err
	}
	return completedRun, health, checkpoints, controlOut, nil
}

func startRun(ctx context.Context, tx *sql.Tx, req requestctx.Context, instance WorkerInstance, lease WorkerLease, input RunOnceInput, deadline time.Time) (WorkerRun, error) {
	metadata, err := marshalJSONObject(map[string]any{
		"schema_version":   "worker_run.metadata.v0.2",
		"run_once_reason":  input.Reason,
		"request_metadata": rawJSONObjectOrDefault(input.Metadata),
	}, "metadata")
	if err != nil {
		return WorkerRun{}, err
	}
	leaseGeneration := lease.Generation
	run, err := scanWorkerRun(tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_runs (
			worker_run_id, worker_instance_id, worker_kind, run_status,
			trigger_kind, trigger_ref, lease_id, lease_generation, correlation_id,
			idempotency_key, started_at, deadline_at, metadata
		)
		VALUES ($1, $2, $3, 'running', $4, $5, $6, $7, $8, $9, now(), $10, $11)
		RETURNING worker_run_id, worker_instance_id, worker_kind, run_status,
		          trigger_kind, trigger_ref, lease_id, lease_generation, correlation_id,
		          idempotency_key, started_at, finished_at, deadline_at,
		          result_summary_json, counters_json, resource_usage_json, error_json,
		          retryable, created_at, updated_at, metadata
	`, ids.NewWorkerRunID(), instance.WorkerInstanceID, instance.WorkerKind, input.TriggerKind, input.TriggerRef, lease.WorkerLeaseID, leaseGeneration, req.CorrelationID, input.IdempotencyKey, deadline, []byte(metadata)))
	if err != nil {
		return WorkerRun{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_instances
		SET current_run_id = $2,
		    lifecycle_status = CASE
		        WHEN lifecycle_status IN ('disabled', 'retired') THEN lifecycle_status
		        ELSE 'active'
		    END,
		    updated_at = now()
		WHERE worker_instance_id = $1
	`, instance.WorkerInstanceID, run.WorkerRunID); err != nil {
		return WorkerRun{}, err
	}
	return run, nil
}

func completeRun(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun, lease WorkerLease, result RunResult) (WorkerRun, error) {
	if err := ensureLeaseFence(ctx, tx, lease); err != nil {
		return WorkerRun{}, err
	}
	resultSummary := rawJSONObjectOrDefault(result.ResultSummary)
	countersValue := result.Counters
	if countersValue == nil {
		countersValue = map[string]int64{}
	}
	counters, err := marshalJSONObject(countersValue, "counters_json")
	if err != nil {
		return WorkerRun{}, err
	}
	resourceUsage := rawJSONObjectOrDefault(result.ResourceUsage)
	finishedAt := time.Now().UTC()
	completedRun, err := scanWorkerRun(tx.QueryRowContext(ctx, `
		UPDATE workers.worker_runs
		SET run_status = 'succeeded',
		    finished_at = $2,
		    result_summary_json = $3,
		    counters_json = $4,
		    resource_usage_json = $5,
		    retryable = $6,
		    updated_at = now()
		WHERE worker_run_id = $1
		RETURNING worker_run_id, worker_instance_id, worker_kind, run_status,
		          trigger_kind, trigger_ref, lease_id, lease_generation, correlation_id,
		          idempotency_key, started_at, finished_at, deadline_at,
		          result_summary_json, counters_json, resource_usage_json, error_json,
		          retryable, created_at, updated_at, metadata
	`, run.WorkerRunID, finishedAt, []byte(resultSummary), []byte(counters), []byte(resourceUsage), result.Retryable))
	if err != nil {
		return WorkerRun{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE workers.worker_instances
		SET current_run_id = NULL,
		    last_run_id = $2,
		    last_success_at = $3,
		    next_run_after = $4,
		    consecutive_failures = 0,
		    lifecycle_status = CASE
		        WHEN lifecycle_status IN ('disabled', 'retired') THEN lifecycle_status
		        ELSE 'active'
		    END,
		    updated_at = now()
		WHERE worker_instance_id = $1
	`, instance.WorkerInstanceID, completedRun.WorkerRunID, finishedAt, result.NextRunAfter)
	if err != nil {
		return WorkerRun{}, err
	}
	return completedRun, nil
}

func failRun(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun, lease WorkerLease, runErr error) (WorkerRun, int, error) {
	if err := ensureLeaseFence(ctx, tx, lease); err != nil {
		return WorkerRun{}, 0, err
	}
	finishedAt := time.Now().UTC()
	failedRun, err := scanWorkerRun(tx.QueryRowContext(ctx, `
		UPDATE workers.worker_runs
		SET run_status = 'failed',
		    finished_at = $2,
		    error_json = $3,
		    retryable = true,
		    updated_at = now()
		WHERE worker_run_id = $1
		RETURNING worker_run_id, worker_instance_id, worker_kind, run_status,
		          trigger_kind, trigger_ref, lease_id, lease_generation, correlation_id,
		          idempotency_key, started_at, finished_at, deadline_at,
		          result_summary_json, counters_json, resource_usage_json, error_json,
		          retryable, created_at, updated_at, metadata
	`, run.WorkerRunID, finishedAt, []byte(errorJSON(runErr))))
	if err != nil {
		return WorkerRun{}, 0, err
	}
	var consecutiveFailures int
	if err := tx.QueryRowContext(ctx, `
		UPDATE workers.worker_instances
		SET current_run_id = NULL,
		    last_run_id = $2,
		    last_failure_at = $3,
		    consecutive_failures = consecutive_failures + 1,
		    lifecycle_status = CASE
		        WHEN lifecycle_status IN ('disabled', 'retired') THEN lifecycle_status
		        WHEN consecutive_failures + 1 >= 3 THEN 'failed'
		        ELSE 'degraded'
		    END,
		    updated_at = now()
		WHERE worker_instance_id = $1
		RETURNING consecutive_failures
	`, instance.WorkerInstanceID, failedRun.WorkerRunID, finishedAt).Scan(&consecutiveFailures); err != nil {
		return WorkerRun{}, 0, err
	}
	return failedRun, consecutiveFailures, nil
}

func appendWorkerEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType, targetKind, targetID, status string, payload map[string]any) error {
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      targetKind,
		TargetID:        targetID,
		Status:          status,
		Result:          "ok",
		Payload:         payload,
		VisibilityClass: "internal",
	})
	return err
}

func holderID(req requestctx.Context) string {
	if strings.TrimSpace(req.OriginNodeID) != "" {
		return req.OriginNodeID
	}
	return "loomd"
}

func timeoutFromPolicy(raw json.RawMessage) time.Duration {
	raw = rawJSONObjectOrDefault(raw)
	var payload struct {
		RunTimeoutSeconds int `json:"run_timeout_seconds"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 30 * time.Second
	}
	if payload.RunTimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(payload.RunTimeoutSeconds) * time.Second
}
