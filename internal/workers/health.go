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
		ConsecutiveFailures: instance.ConsecutiveFails,
		DetailsJSON:         details,
	})
}

func upsertSucceededHealth(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun) (WorkerHealth, error) {
	details, err := marshalJSONObject(map[string]any{
		"schema_version":       "worker_health.v0.2",
		"last_run_id":          run.WorkerRunID,
		"consecutive_failures": 0,
		"last_terminal_status": RunStatusSucceeded,
	}, "details_json")
	if err != nil {
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, healthUpdate{
		InstanceID:          instance.WorkerInstanceID,
		Status:              HealthHealthy,
		Severity:            SeverityInfo,
		Summary:             "Last worker run succeeded.",
		AttentionRequired:   false,
		LastSuccessAt:       run.FinishedAt,
		QueueDepth:          0,
		ConsecutiveFailures: 0,
		DetailsJSON:         details,
	})
}

func upsertFailedHealth(ctx context.Context, tx *sql.Tx, instance WorkerInstance, run WorkerRun, consecutiveFailures int, runErr error) (WorkerHealth, error) {
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
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, healthUpdate{
		InstanceID:          instance.WorkerInstanceID,
		Status:              status,
		Severity:            severity,
		Summary:             "Last worker run failed.",
		AttentionRequired:   attention,
		LastFailureAt:       run.FinishedAt,
		QueueDepth:          0,
		ConsecutiveFailures: consecutiveFailures,
		DetailsJSON:         details,
	})
}

func upsertTimedOutHealth(ctx context.Context, tx *sql.Tx, instanceID string, runID string, finishedAt time.Time, consecutiveFailures int, runErr error) (WorkerHealth, error) {
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
		return WorkerHealth{}, err
	}
	return upsertHealth(ctx, tx, healthUpdate{
		InstanceID:          instanceID,
		Status:              status,
		Severity:            severity,
		Summary:             "Worker run timed out.",
		AttentionRequired:   true,
		LastFailureAt:       &finishedAt,
		QueueDepth:          0,
		ConsecutiveFailures: consecutiveFailures,
		DetailsJSON:         details,
	})
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
		              queue_depth = EXCLUDED.queue_depth,
		              consecutive_failures = EXCLUDED.consecutive_failures,
		              details_json = EXCLUDED.details_json,
		              computed_at = now(),
		              metadata = EXCLUDED.metadata
		RETURNING worker_health_id, worker_instance_id, health_status, severity, summary,
		          attention_required, last_success_at, last_failure_at, current_run_id,
		          queue_depth, consecutive_failures, computed_at, details_json, metadata
	`, ids.NewWorkerHealthID(), update.InstanceID, update.Status, update.Severity, update.Summary, update.AttentionRequired, update.LastSuccessAt, update.LastFailureAt, update.CurrentRunID, update.QueueDepth, update.ConsecutiveFailures, []byte(details)))
}
