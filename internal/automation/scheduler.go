package automation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	defaultSchedulerBatchSize = 25
	maxSchedulerBatchSize     = 200

	fireMisfireNone    = "none"
	fireMisfireMissed  = "missed"
	fireMisfireSkipped = "skipped"
	fireMisfireLateRun = "late_run"
)

type schedulerProcessResult struct {
	Fire       ScheduleFire
	Invocation *Invocation
	Missed     bool
	Skipped    bool
}

func (s Service) RunScheduler(ctx context.Context, req requestctx.Context, input SchedulerRunInput) (SchedulerRunResult, error) {
	if s.DB == nil {
		return SchedulerRunResult{}, fmt.Errorf("database is required")
	}
	input = normalizeSchedulerRunInput(input)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SchedulerRunResult{}, err
	}
	defer tx.Rollback()

	dueSchedules, err := loadDueSchedulesTx(ctx, tx, input.Now, input.LookaheadWindow, input.BatchSize)
	if err != nil {
		return SchedulerRunResult{}, err
	}

	result := SchedulerRunResult{ScannedSchedules: int64(len(dueSchedules))}
	for _, schedule := range dueSchedules {
		processed, err := s.processDueScheduleTx(ctx, tx, req, schedule, input)
		if err != nil {
			result.FailedSchedules++
			return result, err
		}
		result.CreatedFires++
		result.ScheduleFireIDs = append(result.ScheduleFireIDs, processed.Fire.ScheduleFireID)
		if processed.Missed {
			result.MissedFires++
		}
		if processed.Skipped {
			result.SkippedFires++
		}
		if processed.Invocation != nil {
			result.CreatedInvocations++
			result.InvocationIDs = append(result.InvocationIDs, processed.Invocation.InvocationID)
		}
	}

	nextDue, err := nextDueScheduleAtTx(ctx, tx)
	if err != nil {
		return SchedulerRunResult{}, err
	}
	result.NextDueAt = nextDue

	if err := tx.Commit(); err != nil {
		return SchedulerRunResult{}, err
	}
	return result, nil
}

func normalizeSchedulerRunInput(input SchedulerRunInput) SchedulerRunInput {
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if input.BatchSize <= 0 || input.BatchSize > maxSchedulerBatchSize {
		input.BatchSize = defaultSchedulerBatchSize
	}
	if input.LookaheadWindow < 0 {
		input.LookaheadWindow = 0
	}
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	return input
}

func loadDueSchedulesTx(ctx context.Context, tx *sql.Tx, now time.Time, lookahead time.Duration, limit int) ([]Schedule, error) {
	cutoff := now.UTC()
	if lookahead > 0 {
		cutoff = cutoff.Add(lookahead)
	}
	rows, err := tx.QueryContext(ctx, scheduleSelectSQL()+`
		WHERE status = $1
		  AND next_fire_at IS NOT NULL
		  AND next_fire_at <= $2
		ORDER BY next_fire_at ASC, schedule_key ASC
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	`, ScheduleStatusActive, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Schedule{}
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, schedule)
	}
	return out, rows.Err()
}

func nextDueScheduleAtTx(ctx context.Context, tx *sql.Tx) (*time.Time, error) {
	var next sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT min(next_fire_at)
		FROM automation.schedules
		WHERE status = $1
		  AND next_fire_at IS NOT NULL
	`, ScheduleStatusActive).Scan(&next)
	if err != nil {
		return nil, err
	}
	if !next.Valid {
		return nil, nil
	}
	out := next.Time.UTC()
	return &out, nil
}

func (s Service) processDueScheduleTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, schedule Schedule, input SchedulerRunInput) (schedulerProcessResult, error) {
	if schedule.NextFireAt == nil {
		return schedulerProcessResult{}, fmt.Errorf("schedule %s has no next_fire_at", schedule.ScheduleID)
	}
	scheduledFor := schedule.NextFireAt.UTC()
	latenessSeconds := int(input.Now.Sub(scheduledFor).Seconds())
	if latenessSeconds < 0 {
		latenessSeconds = 0
	}

	misfireProfile, err := NormalizeMisfireProfile(schedule.MisfireProfileJSON)
	if err != nil {
		return schedulerProcessResult{}, err
	}
	concurrencyProfile, err := NormalizeConcurrencyProfile(schedule.ConcurrencyProfileJSON)
	if err != nil {
		return schedulerProcessResult{}, err
	}

	fireStatus, misfireStatus, shouldInvoke := schedulerFireDecision(misfireProfile, input.Now, scheduledFor)
	if shouldInvoke && concurrencyProfile.Policy == ConcurrencySkipIfPending {
		hasPending, err := hasPendingScheduleInvocationTx(ctx, tx, schedule.ScheduleID)
		if err != nil {
			return schedulerProcessResult{}, err
		}
		if hasPending {
			fireStatus = FireStatusSkipped
			misfireStatus = fireMisfireSkipped
			shouldInvoke = false
		}
	}

	fire, err := insertScheduleFireTx(ctx, tx, schedule, fireStatus, misfireStatus, latenessSeconds, input.WorkerRunID)
	if err != nil {
		return schedulerProcessResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireCreated, "schedule_fire", fire.ScheduleFireID, FireStatusCreated, map[string]any{
		"schedule_id":   schedule.ScheduleID,
		"schedule_key":  schedule.ScheduleKey,
		"automation_id": schedule.AutomationID,
		"scheduled_for": scheduledFor.Format(time.RFC3339Nano),
	}); err != nil {
		return schedulerProcessResult{}, err
	}

	processed := schedulerProcessResult{Fire: fire}
	switch fireStatus {
	case FireStatusMissed:
		processed.Missed = true
		if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireMissed, "schedule_fire", fire.ScheduleFireID, FireStatusMissed, map[string]any{
			"schedule_id":       schedule.ScheduleID,
			"schedule_key":      schedule.ScheduleKey,
			"lateness_seconds":  latenessSeconds,
			"misfire_policy":    misfireProfile.Policy,
			"misfire_status":    misfireStatus,
			"scheduled_for":     scheduledFor.Format(time.RFC3339Nano),
			"manual_action_due": true,
		}); err != nil {
			return schedulerProcessResult{}, err
		}
	case FireStatusSkipped:
		processed.Skipped = true
		if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireSkipped, "schedule_fire", fire.ScheduleFireID, FireStatusSkipped, map[string]any{
			"schedule_id":    schedule.ScheduleID,
			"schedule_key":   schedule.ScheduleKey,
			"skip_reason":    "concurrency_policy",
			"scheduled_for":  scheduledFor.Format(time.RFC3339Nano),
			"policy":         concurrencyProfile.Policy,
			"misfire_status": misfireStatus,
		}); err != nil {
			return schedulerProcessResult{}, err
		}
	default:
		if shouldInvoke {
			invocation, err := s.createInvocationForScheduleFireTx(ctx, tx, req, schedule, fire)
			if err != nil {
				return schedulerProcessResult{}, err
			}
			fire, err = updateScheduleFireInvocationCreatedTx(ctx, tx, fire.ScheduleFireID, invocation.InvocationID)
			if err != nil {
				return schedulerProcessResult{}, err
			}
			processed.Fire = fire
			processed.Invocation = &invocation
			if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireInvocationCreated, "schedule_fire", fire.ScheduleFireID, FireStatusInvocationCreated, map[string]any{
				"schedule_id":   schedule.ScheduleID,
				"schedule_key":  schedule.ScheduleKey,
				"automation_id": schedule.AutomationID,
				"invocation_id": invocation.InvocationID,
			}); err != nil {
				return schedulerProcessResult{}, err
			}
		}
	}

	if err := updateScheduleAfterFireTx(ctx, tx, req, schedule, fire, input.Now); err != nil {
		return schedulerProcessResult{}, err
	}
	return processed, nil
}

func schedulerFireDecision(profile MisfireProfile, now, scheduledFor time.Time) (fireStatus, misfireStatus string, shouldInvoke bool) {
	lateness := now.UTC().Sub(scheduledFor.UTC())
	if lateness < 0 {
		lateness = 0
	}
	switch profile.Policy {
	case MisfireRunIfLateWithin:
		if int(lateness.Seconds()) <= profile.LatenessWindowSeconds {
			if lateness > 0 {
				return FireStatusCreated, fireMisfireLateRun, true
			}
			return FireStatusCreated, fireMisfireNone, true
		}
		return FireStatusMissed, fireMisfireMissed, false
	case MisfireMarkMissed:
		if lateness > 0 {
			return FireStatusMissed, fireMisfireMissed, false
		}
		return FireStatusCreated, fireMisfireNone, true
	default:
		return FireStatusMissed, fireMisfireMissed, false
	}
}

func hasPendingScheduleInvocationTx(ctx context.Context, tx *sql.Tx, scheduleID string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM automation.invocations
			WHERE source_kind = $1
			  AND source_ref = $2
			  AND status IN ($3, $4, $5)
		)
	`, SourceKindSchedule, scheduleID, InvocationStatusPending, InvocationStatusLeased, InvocationStatusCalling).Scan(&exists)
	return exists, err
}

func insertScheduleFireTx(ctx context.Context, tx *sql.Tx, schedule Schedule, status, misfireStatus string, latenessSeconds int, workerRunID string) (ScheduleFire, error) {
	return scanScheduleFire(tx.QueryRowContext(ctx, `
		INSERT INTO automation.schedule_fires (
			schedule_fire_id, schedule_id, automation_id, scheduled_for, status,
			misfire_status, lateness_seconds, worker_run_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9::jsonb)
		RETURNING schedule_fire_id, schedule_id, automation_id, scheduled_for,
		          status, misfire_status, lateness_seconds, worker_run_id,
		          invocation_id, route_id, capability_call_id, job_id,
		          failure_code, failure_message, created_at, updated_at,
		          completed_at, failed_at, metadata_json
	`, ids.NewScheduleFireID(), schedule.ScheduleID, schedule.AutomationID, schedule.NextFireAt.UTC(), status, misfireStatus, latenessSeconds, strings.TrimSpace(workerRunID), mustJSON(map[string]any{
		"schema_version": "schedule_fire.metadata.v0.2",
		"schedule_key":   schedule.ScheduleKey,
	})))
}

func (s Service) createInvocationForScheduleFireTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, schedule Schedule, fire ScheduleFire) (Invocation, error) {
	target, err := NormalizeTargetProfile(schedule.TargetProfileJSON)
	if err != nil {
		return Invocation{}, err
	}
	retry, err := NormalizeRetryProfile(schedule.RetryProfileJSON)
	if err != nil {
		return Invocation{}, err
	}
	originNodeID, err := resolveNodeID(ctx, tx, defaultSchedulerOriginNode)
	if err != nil {
		return Invocation{}, err
	}

	return s.createInvocationTx(ctx, tx, req, CreateInvocationTxInput{
		AutomationID:        schedule.AutomationID,
		SourceKind:          SourceKindSchedule,
		SourceRef:           schedule.ScheduleID,
		SourceOccurrenceRef: fire.ScheduleFireID,
		ActorID:             schedule.RunAsActorID,
		OriginNodeID:        originNodeID,
		ScopeID:             schedule.ScopeID,
		ProjectID:           schedule.ProjectID,
		TargetCapability:    target.CapabilityRef,
		InputJSON:           schedule.InputJSON,
		IdempotencyKey:      "automation-invocation:schedule_fire:" + fire.ScheduleFireID,
		MaxAttempts:         retry.MaxAttempts,
		Metadata: mustJSON(map[string]any{
			"schema_version": "automation_invocation.metadata.v0.2",
			"schedule_key":   schedule.ScheduleKey,
			"schedule_id":    schedule.ScheduleID,
		}),
		EventPayload: map[string]any{
			"schedule_fire_id":     fire.ScheduleFireID,
			"schedule_fire_status": fire.Status,
		},
	})
}

func updateScheduleFireInvocationCreatedTx(ctx context.Context, tx *sql.Tx, fireID, invocationID string) (ScheduleFire, error) {
	return scanScheduleFire(tx.QueryRowContext(ctx, `
		UPDATE automation.schedule_fires
		SET status = $2,
		    invocation_id = $3,
		    updated_at = now()
		WHERE schedule_fire_id = $1
		RETURNING schedule_fire_id, schedule_id, automation_id, scheduled_for,
		          status, misfire_status, lateness_seconds, worker_run_id,
		          invocation_id, route_id, capability_call_id, job_id,
		          failure_code, failure_message, created_at, updated_at,
		          completed_at, failed_at, metadata_json
	`, fireID, FireStatusInvocationCreated, invocationID))
}

func updateScheduleAfterFireTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, schedule Schedule, fire ScheduleFire, now time.Time) error {
	after := fire.ScheduledFor
	// Coalesce calendar downtime into this one durable fire, then advance past
	// now. Never drain a backlog minute by minute after a restart.
	if schedule.ScheduleKind == ScheduleKindCron && now.After(after) {
		after = now
	}
	nextFire, err := ComputeNextFire(schedule, after)
	if err != nil {
		return err
	}
	nextStatus := schedule.Status
	if nextFire == nil {
		nextStatus = ScheduleStatusCompleted
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE automation.schedules
		SET status = $2,
		    last_fire_at = $3,
		    last_schedule_fire_id = $4,
		    next_fire_at = $5,
		    updated_at = now()
		WHERE schedule_id = $1
	`, schedule.ScheduleID, nextStatus, fire.ScheduledFor, fire.ScheduleFireID, nullableTime(nextFire))
	if err != nil {
		return err
	}
	if nextStatus == ScheduleStatusCompleted {
		return appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleUpdated, "schedule", schedule.ScheduleID, nextStatus, map[string]any{
			"schedule_key":      schedule.ScheduleKey,
			"schedule_fire_id":  fire.ScheduleFireID,
			"completion_reason": "one_shot_fired",
		})
	}
	return nil
}

func resolveNodeID(ctx context.Context, db queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("node ref is required")
	}
	var id string
	err := db.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE (node_id = $1 OR node_key = $1)
		  AND status = 'active'
	`, ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("resolve node %q: %w", ref, err)
	}
	if err != nil {
		return "", fmt.Errorf("resolve node %q: %w", ref, err)
	}
	return id, nil
}
