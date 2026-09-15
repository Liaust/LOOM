package workers

import (
	"context"
	"fmt"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
)

type staleRunRepair struct {
	WorkerInstanceID   string
	WorkerRunID        string
	ConsecutiveFailure int
	ExpiredLeases      int
}

func (s Service) RepairStaleRuns(ctx context.Context, req requestctx.Context, now time.Time) (StaleRunRepairResult, error) {
	if s.DB == nil {
		return StaleRunRepairResult{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return StaleRunRepairResult{}, err
	}
	defer tx.Rollback()

	leaseResult, err := tx.ExecContext(ctx, `
		UPDATE workers.worker_leases
		SET lease_status = 'expired',
		    released_at = $1,
		    renewed_at = $1
		WHERE lease_status = 'active'
		  AND expires_at <= $1
	`, now)
	if err != nil {
		return StaleRunRepairResult{}, err
	}
	expiredLeases, err := leaseResult.RowsAffected()
	if err != nil {
		return StaleRunRepairResult{}, err
	}

	runErr := fmt.Errorf("worker run exceeded its deadline or lost its lease; marked timed_out by stale-run repair")
	rows, err := tx.QueryContext(ctx, `
		WITH stale AS (
			SELECT wr.worker_run_id,
			       wi.worker_instance_id,
			       wr.lease_id
			FROM workers.worker_instances wi
			JOIN workers.worker_runs wr ON wr.worker_run_id = wi.current_run_id
			LEFT JOIN workers.worker_leases wl ON wl.worker_lease_id = wr.lease_id
			WHERE wr.run_status = 'running'
			  AND (
			      (wr.deadline_at IS NOT NULL AND wr.deadline_at <= $1)
			      OR (wl.worker_lease_id IS NOT NULL AND wl.lease_status IN ('expired', 'released', 'stolen'))
			      OR (wl.worker_lease_id IS NOT NULL AND wl.lease_status = 'active' AND wl.expires_at <= $1)
			  )
		),
		expired_stale_leases AS (
			UPDATE workers.worker_leases wl
			SET lease_status = 'expired',
			    released_at = $1,
			    renewed_at = $1
			FROM stale
			WHERE wl.worker_lease_id = stale.lease_id
			  AND wl.lease_status = 'active'
			RETURNING wl.worker_lease_id
		),
		updated_runs AS (
			UPDATE workers.worker_runs wr
			SET run_status = 'timed_out',
			    finished_at = $1,
			    error_json = $2,
			    retryable = true,
			    updated_at = now()
			FROM stale
			WHERE wr.worker_run_id = stale.worker_run_id
			RETURNING wr.worker_run_id, wr.worker_instance_id
		),
		updated_instances AS (
			UPDATE workers.worker_instances wi
			SET current_run_id = NULL,
			    last_run_id = updated_runs.worker_run_id,
			    last_failure_at = $1,
			    consecutive_failures = wi.consecutive_failures + 1,
			    lifecycle_status = CASE
			        WHEN wi.lifecycle_status IN ('disabled', 'retired') THEN wi.lifecycle_status
			        WHEN wi.consecutive_failures + 1 >= 3 THEN 'failed'
			        ELSE 'degraded'
			    END,
			    updated_at = now()
			FROM updated_runs
			WHERE wi.worker_instance_id = updated_runs.worker_instance_id
			  AND wi.current_run_id = updated_runs.worker_run_id
			RETURNING wi.worker_instance_id, updated_runs.worker_run_id, wi.consecutive_failures
		)
		SELECT worker_instance_id,
		       worker_run_id,
		       consecutive_failures,
		       (SELECT count(*) FROM expired_stale_leases) AS expired_stale_leases
		FROM updated_instances
		ORDER BY worker_instance_id
	`, now, []byte(errorJSON(runErr)))
	if err != nil {
		return StaleRunRepairResult{}, err
	}
	defer rows.Close()

	repairs := []staleRunRepair{}
	expiredStaleLeases := 0
	for rows.Next() {
		var repair staleRunRepair
		if err := rows.Scan(&repair.WorkerInstanceID, &repair.WorkerRunID, &repair.ConsecutiveFailure, &repair.ExpiredLeases); err != nil {
			return StaleRunRepairResult{}, err
		}
		expiredStaleLeases = repair.ExpiredLeases
		repairs = append(repairs, repair)
	}
	if err := rows.Err(); err != nil {
		return StaleRunRepairResult{}, err
	}

	result := StaleRunRepairResult{
		CheckedAt:         now,
		TimedOutRuns:      len(repairs),
		ExpiredLeases:     int(expiredLeases) + expiredStaleLeases,
		RepairedInstances: len(repairs),
		RunIDs:            make([]string, 0, len(repairs)),
	}
	for _, repair := range repairs {
		result.RunIDs = append(result.RunIDs, repair.WorkerRunID)
		health, err := upsertTimedOutHealth(ctx, tx, repair.WorkerInstanceID, repair.WorkerRunID, now, repair.ConsecutiveFailure, runErr)
		if err != nil {
			return StaleRunRepairResult{}, err
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerRunFailed, "worker_run", repair.WorkerRunID, RunStatusTimedOut, map[string]any{
			"worker_instance_id": repair.WorkerInstanceID,
			"repair_kind":        "stale_run_timeout",
			"error":              errorJSON(runErr),
		}); err != nil {
			return StaleRunRepairResult{}, err
		}
		if err := appendWorkerEvent(ctx, tx, req, events.TypeWorkerHealthUpdated, "worker_health", health.WorkerHealthID, health.HealthStatus, map[string]any{
			"worker_instance_id": repair.WorkerInstanceID,
			"worker_run_id":      repair.WorkerRunID,
			"health_status":      health.HealthStatus,
			"severity":           health.Severity,
			"repair_kind":        "stale_run_timeout",
		}); err != nil {
			return StaleRunRepairResult{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		return StaleRunRepairResult{}, err
	}
	return result, nil
}
