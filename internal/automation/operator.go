package automation

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	SchedulerWorkerKey         = "main.automation_scheduler"
	DispatcherWorkerKey        = "main.automation_dispatcher"
	DirectEventIngestWorkerKey = "main.direct_event_ingest"
)

func (s Service) ScheduleStatus(ctx context.Context) (ScheduleStatus, error) {
	if s.DB == nil {
		return ScheduleStatus{}, fmt.Errorf("database is required")
	}

	var status ScheduleStatus
	status.SchedulerWorkerKey = SchedulerWorkerKey
	status.DispatcherWorkerKey = DispatcherWorkerKey

	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'active'),
			count(*) FILTER (WHERE status = 'paused'),
			count(*) FILTER (WHERE status = 'disabled'),
			count(*) FILTER (WHERE status = 'completed'),
			count(*) FILTER (WHERE status = 'active' AND next_fire_at IS NOT NULL AND next_fire_at <= now()),
			min(next_fire_at) FILTER (WHERE status = 'active' AND next_fire_at IS NOT NULL)
		FROM automation.schedules
		WHERE true
	`+archivedProjectByProjectColumnSQL("project_id")).Scan(
		&status.ActiveScheduleCount,
		&status.PausedScheduleCount,
		&status.DisabledScheduleCount,
		&status.CompletedScheduleCount,
		&status.DueScheduleCount,
		nullableTimeScanner(&status.NextFireAt),
	); err != nil {
		return ScheduleStatus{}, err
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM automation.schedule_fires
		WHERE status = $1
	`+archivedProjectByAutomationColumnSQL("automation_id"), FireStatusMissed).Scan(&status.MissedFireCount); err != nil {
		return ScheduleStatus{}, err
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE status IN ($1, $2, $3)),
			count(*) FILTER (WHERE status IN ($4, $5, $6, $7)),
			min(created_at) FILTER (WHERE status IN ($1, $2, $3))
		FROM automation.invocations
		WHERE true
	`+archivedProjectByProjectColumnSQL("project_id")+`
	`, InvocationStatusPending, InvocationStatusLeased, InvocationStatusCalling,
		InvocationStatusFailed, InvocationStatusTimedOut, InvocationStatusRequiresManualAction, InvocationStatusWaitingApproval,
	).Scan(
		&status.PendingInvocationCount,
		&status.FailedInvocationCount,
		nullableTimeScanner(&status.OldestPendingInvocationAt),
	); err != nil {
		return ScheduleStatus{}, err
	}

	return status, nil
}

func (s Service) DirectEventStatus(ctx context.Context) (DirectEventStatus, error) {
	if s.DB == nil {
		return DirectEventStatus{}, fmt.Errorf("database is required")
	}

	var status DirectEventStatus
	status.DirectEventIngestWorkerKey = DirectEventIngestWorkerKey
	status.DispatcherWorkerKey = DispatcherWorkerKey

	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'active'),
			count(*) FILTER (WHERE status = 'paused'),
			count(*) FILTER (WHERE status = 'disabled')
		FROM automation.direct_event_endpoints
		WHERE true
	`+archivedProjectByAutomationColumnSQL("automation_id")).Scan(
		&status.ActiveEndpointCount,
		&status.PausedEndpointCount,
		&status.DisabledEndpointCount,
	); err != nil {
		return DirectEventStatus{}, err
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM automation.integrations
		WHERE status = 'active'
	`).Scan(&status.ActiveIntegrationCount); err != nil {
		return DirectEventStatus{}, err
	}

	if err := s.DB.QueryRowContext(ctx, `
		SELECT
			count(*) FILTER (WHERE status = $1),
			count(*) FILTER (WHERE status = $1),
			count(*) FILTER (WHERE status = $2),
			count(*) FILTER (WHERE status IN ($3, $4, $5, $6)),
			min(received_at) FILTER (WHERE status = $1),
			min(received_at) FILTER (WHERE status = $1)
		FROM automation.direct_events
		WHERE true
	`+archivedProjectByAutomationColumnSQL("automation_id")+`
	`, DirectEventStatusAccepted,
		DirectEventStatusInvocationCreated,
		DirectEventStatusRejected,
		DirectEventStatusMappingFailed,
		DirectEventStatusFailed,
		DirectEventStatusTimedOut,
	).Scan(
		&status.AcceptedCount,
		&status.MappingPendingCount,
		&status.InvocationCreatedCount,
		&status.FailedCount,
		nullableTimeScanner(&status.OldestAcceptedAt),
		nullableTimeScanner(&status.OldestMappingPendingAt),
	); err != nil {
		return DirectEventStatus{}, err
	}

	return status, nil
}

func (s Service) FireScheduleNow(ctx context.Context, req requestctx.Context, ref string, input FireScheduleInput) (FireScheduleResult, error) {
	if s.DB == nil {
		return FireScheduleResult{}, fmt.Errorf("database is required")
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return FireScheduleResult{}, err
	}
	now := time.Now().UTC()

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return FireScheduleResult{}, err
	}
	defer tx.Rollback()

	schedule, err := scanSchedule(tx.QueryRowContext(ctx, scheduleSelectSQL()+`
		WHERE schedule_id = $1 OR schedule_key = $1
		FOR UPDATE
	`, strings.TrimSpace(ref)))
	if err != nil {
		return FireScheduleResult{}, err
	}
	if schedule.Status == ScheduleStatusDisabled {
		return FireScheduleResult{}, fmt.Errorf("schedule %s is disabled", schedule.ScheduleKey)
	}
	if err := ensureScheduleRuntimeActive(ctx, tx, schedule); err != nil {
		return FireScheduleResult{}, err
	}

	fire, err := insertManualScheduleFireTx(ctx, tx, schedule, now, req, strings.TrimSpace(input.Reason), metadata)
	if err != nil {
		return FireScheduleResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireCreated, "schedule_fire", fire.ScheduleFireID, FireStatusCreated, map[string]any{
		"schedule_id":   schedule.ScheduleID,
		"schedule_key":  schedule.ScheduleKey,
		"automation_id": schedule.AutomationID,
		"scheduled_for": now.Format(time.RFC3339Nano),
		"manual":        true,
		"reason":        strings.TrimSpace(input.Reason),
	}); err != nil {
		return FireScheduleResult{}, err
	}

	invocation, err := s.createInvocationForScheduleFireTx(ctx, tx, req, schedule, fire)
	if err != nil {
		return FireScheduleResult{}, err
	}
	fire, err = updateScheduleFireInvocationCreatedTx(ctx, tx, fire.ScheduleFireID, invocation.InvocationID)
	if err != nil {
		return FireScheduleResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleFireInvocationCreated, "schedule_fire", fire.ScheduleFireID, FireStatusInvocationCreated, map[string]any{
		"schedule_id":   schedule.ScheduleID,
		"schedule_key":  schedule.ScheduleKey,
		"automation_id": schedule.AutomationID,
		"invocation_id": invocation.InvocationID,
		"manual":        true,
	}); err != nil {
		return FireScheduleResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return FireScheduleResult{}, err
	}
	result := FireScheduleResult{Schedule: schedule, Fire: fire, Invocation: invocation}
	if !input.DispatchNow {
		return result, nil
	}
	dispatch, err := s.DispatchInvocationNow(ctx, req, invocation.InvocationID, manualScheduleFireDispatcherInput(now))
	if err != nil {
		result.DispatchError = err.Error()
		return result, nil
	}
	result.Dispatch = &dispatch
	if refreshedFire, err := s.GetScheduleFire(ctx, fire.ScheduleFireID); err == nil {
		result.Fire = refreshedFire
	}
	if refreshedInvocation, err := s.GetInvocation(ctx, invocation.InvocationID); err == nil {
		result.Invocation = refreshedInvocation
	}
	return result, nil
}

func manualScheduleFireDispatcherInput(now time.Time) DispatcherRunInput {
	return DispatcherRunInput{Now: now.UTC()}
}

func (s Service) ListInvocationFailures(ctx context.Context, filter InvocationFilter) ([]Invocation, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := invocationSelectSQL() + ` WHERE status IN ($1, $2, $3, $4)`
	args := []any{InvocationStatusFailed, InvocationStatusTimedOut, InvocationStatusRequiresManualAction, InvocationStatusWaitingApproval}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if ref := strings.TrimSpace(filter.AutomationRef); ref != "" {
		automation, err := s.getAutomation(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("automation_id =", automation.AutomationID)
	}
	if ref := strings.TrimSpace(filter.ProjectRef); ref != "" {
		projectID, err := resolveProjectID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("project_id =", projectID)
	}
	if sourceKind := strings.TrimSpace(filter.SourceKind); sourceKind != "" {
		add("source_kind =", sourceKind)
	}
	if !hasExplicitRuntimeRef(filter.AutomationRef, filter.ProjectRef) {
		query += archivedProjectByProjectColumnSQL("project_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invocation{}
	for rows.Next() {
		item, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func insertManualScheduleFireTx(ctx context.Context, tx *sql.Tx, schedule Schedule, scheduledFor time.Time, req requestctx.Context, reason string, metadataJSON []byte) (ScheduleFire, error) {
	metadata := mustJSON(map[string]any{
		"schema_version":     "schedule_fire.metadata.v0.2",
		"schedule_key":       schedule.ScheduleKey,
		"manual":             true,
		"reason":             reason,
		"requested_by_actor": req.ActorID,
		"request_metadata":   jsonRawOrDefault(metadataJSON),
	})
	return scanScheduleFire(tx.QueryRowContext(ctx, `
		INSERT INTO automation.schedule_fires (
			schedule_fire_id, schedule_id, automation_id, scheduled_for, status,
			misfire_status, lateness_seconds, worker_run_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, 0, NULL, $7::jsonb)
		RETURNING schedule_fire_id, schedule_id, automation_id, scheduled_for,
		          status, misfire_status, lateness_seconds, worker_run_id,
		          invocation_id, route_id, capability_call_id, job_id,
		          failure_code, failure_message, created_at, updated_at,
		          completed_at, failed_at, metadata_json
	`, ids.NewScheduleFireID(), schedule.ScheduleID, schedule.AutomationID, scheduledFor.UTC(), FireStatusCreated, fireMisfireNone, metadata))
}

func nullableTimeScanner(target **time.Time) any {
	return scannerFunc(func(value sql.NullTime) {
		if value.Valid {
			out := value.Time.UTC()
			*target = &out
		}
	})
}

type scannerFunc func(sql.NullTime)

func (fn scannerFunc) Scan(src any) error {
	var value sql.NullTime
	if err := value.Scan(src); err != nil {
		return err
	}
	fn(value)
	return nil
}

func jsonRawOrDefault(raw []byte) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	return jsonRawMessage(raw)
}

type jsonRawMessage []byte

func (m jsonRawMessage) MarshalJSON() ([]byte, error) {
	if len(m) == 0 {
		return []byte(`{}`), nil
	}
	return m, nil
}
