package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"loom.local/loom/internal/ids"
)

func upsertRunningHealth(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun) (WorkerHealth, error) {
	details, err := marshalJSONObject(map[string]any{
		"schema_version": "worker_health.v0.2",
		"last_run_id":    run.WorkerRunID,
		"state":          "running",
	}, "details_json")
	if err != nil {
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, healthUpdate{
		InstanceID:          instance.WorkerInstanceID,
		Status:              HealthRunning,
		Severity:            SeverityInfo,
		Summary:             "Worker run is active.",
		AttentionRequired:   false,
		CurrentRunID:        run.WorkerRunID,
		QueueDepth:          0,
		PreserveQueueDepth:  true,
		ConsecutiveFailures: instance.ConsecutiveFails,
		DetailsJSON:         details,
	})
}

func upsertSucceededHealth(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun) (WorkerHealth, error) {
	update, err := succeededHealthUpdate(instance, run)
	if err != nil {
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, update)
}

func admissionTimedOut(summary json.RawMessage) bool {
	var result struct {
		AdmissionTimedOut bool `json:"admission_timed_out"`
	}
	return json.Unmarshal(summary, &result) == nil && result.AdmissionTimedOut
}

func succeededHealthUpdate(instance WorkerInstance, run WorkerRun) (healthUpdate, error) {
	update := healthUpdate{
		InstanceID: instance.WorkerInstanceID, Status: HealthHealthy, Severity: SeverityInfo,
		Summary: "Last worker run succeeded.", LastSuccessAt: run.FinishedAt,
	}
	detail := map[string]any{
		"schema_version":       "worker_health.v0.2",
		"last_run_id":          run.WorkerRunID,
		"consecutive_failures": 0,
		"last_terminal_status": RunStatusSucceeded,
	}
	if admissionTimedOut(run.ResultSummaryJSON) {
		update.Status, update.Severity = HealthDegraded, SeverityWarning
		update.Summary = "Processing succeeded; Notes admission timed out and remains incomplete."
		update.AttentionRequired, update.PreserveQueueDepth = true, true
		update.ConsecutiveFailures = instance.ConsecutiveFails
		detail["consecutive_failures"] = instance.ConsecutiveFails
		detail["reason"] = "admission_timeout"
		detail["retryable"] = run.Retryable
		// The schema requires an integer; an old/default zero is not a measured empty queue.
		detail["queue_depth_basis"] = "not_measured_by_partial_run"
		detail["correlation_id"] = run.CorrelationID
		if warning := admissionHealthWarning(run.ResultSummaryJSON); warning != nil {
			detail["admission_warning"] = warning
		}
	}
	if instance.WorkerKind == "notes_workspace_sync" {
		var result struct {
			Enrollment *struct {
				Holds       []json.RawMessage `json:"holds"`
				Collections []struct {
					Stage string `json:"source_intake_stage"`
				} `json:"collections"`
			} `json:"enrollment"`
		}
		if json.Unmarshal(run.ResultSummaryJSON, &result) == nil && result.Enrollment != nil {
			held, catchingUp := len(result.Enrollment.Holds), 0
			for _, collection := range result.Enrollment.Collections {
				switch collection.Stage {
				case "scanning", "hashing", "publishing":
					catchingUp++
				}
			}
			detail["enrollment_held"], detail["source_intake_pending"] = held, catchingUp
			detail["client_delivery_verified"] = false
			update.Summary = "Worker execution completed; consult collection inventory for indexing and client delivery."
			if catchingUp > 0 {
				update.Summary = "Worker execution completed; source intake is still in progress."
			}
			if held > 0 {
				update.Status, update.Severity, update.AttentionRequired = HealthDegraded, SeverityWarning, true
				update.Summary = "Worker execution completed; some collections remain held at admission."
			}
		}
	}
	if !instance.Enabled || instance.LifecycleStatus == LifecycleDisabled || instance.LifecycleStatus == LifecycleRetired {
		update.Status = HealthDisabled
	} else if instance.Paused {
		update.Status = HealthPaused
	}
	var err error
	update.DetailsJSON, err = marshalJSONObject(detail, "details_json")
	return update, err
}

// Read only the closed, non-secret qualification, not arbitrary runtime output.
func admissionHealthWarning(summary json.RawMessage) map[string]any {
	var result struct {
		Warning *struct {
			Phase     string `json:"phase"`
			Reason    string `json:"reason"`
			ElapsedMS int64  `json:"elapsed_ms"`
			BudgetMS  int64  `json:"budget_ms"`
			Retryable bool   `json:"retryable"`
		} `json:"admission_warning"`
	}
	if json.Unmarshal(summary, &result) != nil || result.Warning == nil {
		return nil
	}
	warning := result.Warning
	switch warning.Phase {
	case "source_root_reconciliation", "source_root_listing", "source_generation",
		"priority_selection", "priority_reconciliation", "catalog_selection", "catalog_hydration",
		"inventory_selection", "candidate_reconciliation":
	default:
		return nil
	}
	if warning.Reason != "deadline_exceeded" && warning.Reason != "canceled" && warning.Reason != "operation_failed" {
		return nil
	}
	return map[string]any{"phase": warning.Phase, "reason": warning.Reason,
		"elapsed_ms": max(0, warning.ElapsedMS), "budget_ms": max(0, warning.BudgetMS), "retryable": warning.Retryable}
}

func upsertFailedHealth(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun, consecutiveFailures int, runErr error) (WorkerHealth, error) {
	update, err := failedHealthUpdate(instance, run, consecutiveFailures, runErr)
	if err != nil {
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, update)
}

func failedHealthUpdate(instance WorkerInstance, run WorkerRun, consecutiveFailures int, runErr error) (healthUpdate, error) {
	status := HealthDegraded
	severity := SeverityWarning
	attention := true
	if consecutiveFailures >= 3 {
		status = HealthFailed
		severity = SeverityCritical
	}
	details, err := marshalJSONObject(map[string]any{
		"schema_version":       "worker_health.v0.2",
		"last_run_id":          run.WorkerRunID,
		"consecutive_failures": consecutiveFailures,
		"last_terminal_status": RunStatusFailed,
		"error":                json.RawMessage(errorJSON(runErr)),
	}, "details_json")
	if err != nil {
		return healthUpdate{}, err
	}
	return healthUpdate{
		InstanceID:          instance.WorkerInstanceID,
		Status:              status,
		Severity:            severity,
		Summary:             "Last worker run failed.",
		AttentionRequired:   attention,
		LastFailureAt:       run.FinishedAt,
		QueueDepth:          0,
		ConsecutiveFailures: consecutiveFailures,
		DetailsJSON:         details,
	}, nil
}

func upsertTimedOutHealth(ctx context.Context, tx *sql.Tx, instanceID string, runID string, finishedAt time.Time, consecutiveFailures int, runErr error) (WorkerHealth, error) {
	update, err := timedOutHealthUpdate(instanceID, runID, finishedAt, consecutiveFailures, runErr)
	if err != nil {
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, update)
}

func timedOutHealthUpdate(instanceID string, runID string, finishedAt time.Time, consecutiveFailures int, runErr error) (healthUpdate, error) {
	status := HealthDegraded
	severity := SeverityWarning
	if consecutiveFailures >= 3 {
		status = HealthFailed
		severity = SeverityCritical
	}
	details, err := marshalJSONObject(map[string]any{
		"schema_version":       "worker_health.v0.2",
		"last_run_id":          runID,
		"consecutive_failures": consecutiveFailures,
		"last_terminal_status": RunStatusTimedOut,
		"error":                json.RawMessage(errorJSON(runErr)),
	}, "details_json")
	if err != nil {
		return healthUpdate{}, err
	}
	return healthUpdate{
		InstanceID:          instanceID,
		Status:              status,
		Severity:            severity,
		Summary:             "Worker run timed out.",
		AttentionRequired:   true,
		LastFailureAt:       &finishedAt,
		QueueDepth:          0,
		ConsecutiveFailures: consecutiveFailures,
		DetailsJSON:         details,
	}, nil
}

type healthUpdate struct {
	InstanceID          string
	Status              string
	Severity            string
	Summary             string
	AttentionRequired   bool
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	CurrentRunID        string
	QueueDepth          int
	PreserveQueueDepth  bool
	ConsecutiveFailures int
	DetailsJSON         json.RawMessage
}

func upsertHealth(ctx context.Context, tx *sql.Tx, update healthUpdate) (WorkerHealth, error) {
	details := rawJSONObjectOrDefault(update.DetailsJSON)
	return scanWorkerHealth(tx.QueryRowContext(ctx, `
		INSERT INTO workers.worker_health (
			worker_health_id, worker_instance_id, health_status, severity,
			summary, attention_required, last_success_at, last_failure_at,
			current_run_id, queue_depth, consecutive_failures, details_json, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, nullif($9, ''), $10, $11, $12,
		        '{"schema_version":"worker_health.metadata.v0.2"}'::jsonb)
		ON CONFLICT (worker_instance_id)
		DO UPDATE SET health_status = EXCLUDED.health_status,
		              severity = EXCLUDED.severity,
		              summary = EXCLUDED.summary,
		              attention_required = EXCLUDED.attention_required,
		              last_success_at = COALESCE(EXCLUDED.last_success_at, worker_health.last_success_at),
		              last_failure_at = COALESCE(EXCLUDED.last_failure_at, worker_health.last_failure_at),
		              current_run_id = EXCLUDED.current_run_id,
		              queue_depth = CASE WHEN $13 THEN worker_health.queue_depth ELSE EXCLUDED.queue_depth END,
		              consecutive_failures = EXCLUDED.consecutive_failures,
		              details_json = EXCLUDED.details_json,
		              computed_at = now(),
		              metadata = EXCLUDED.metadata
		RETURNING worker_health_id, worker_instance_id, health_status, severity, summary,
		          attention_required, last_success_at, last_failure_at, current_run_id,
		          queue_depth, consecutive_failures, computed_at, details_json, metadata
	`, ids.NewWorkerHealthID(), update.InstanceID, update.Status, update.Severity, update.Summary, update.AttentionRequired, update.LastSuccessAt, update.LastFailureAt, update.CurrentRunID, update.QueueDepth, update.ConsecutiveFailures, []byte(details), update.PreserveQueueDepth))
}
