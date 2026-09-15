package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

type Service struct {
	DB      *sql.DB
	Routing routing.Service
}

func NewService(db *sql.DB, routingService routing.Service) Service {
	return Service{DB: db, Routing: routingService}
}

func hasExplicitRuntimeRef(refs ...string) bool {
	for _, ref := range refs {
		if strings.TrimSpace(ref) != "" {
			return true
		}
	}
	return false
}

func archivedProjectByProjectColumnSQL(projectColumn string) string {
	projectColumn = strings.TrimSpace(projectColumn)
	if projectColumn == "" {
		projectColumn = "project_id"
	}
	return fmt.Sprintf(`
		AND (%[1]s IS NULL OR NOT EXISTS (
			SELECT 1
			FROM projects.projects archived_project
			WHERE archived_project.project_id = %[1]s
			  AND archived_project.status = 'archived'
		))
	`, projectColumn)
}

func archivedProjectByAutomationColumnSQL(automationColumn string) string {
	automationColumn = strings.TrimSpace(automationColumn)
	if automationColumn == "" {
		automationColumn = "automation_id"
	}
	return fmt.Sprintf(`
		AND NOT EXISTS (
			SELECT 1
			FROM automation.automations archived_automation
			JOIN projects.projects archived_project ON archived_project.project_id = archived_automation.project_id
			WHERE archived_automation.automation_id = %[1]s
			  AND archived_project.status = 'archived'
		)
	`, automationColumn)
}

func (s Service) CreateSchedule(ctx context.Context, req requestctx.Context, input CreateScheduleInput) (ScheduleDetail, error) {
	if s.DB == nil {
		return ScheduleDetail{}, fmt.Errorf("database is required")
	}
	normalized, err := s.normalizeCreateSchedule(ctx, req, input)
	if err != nil {
		return ScheduleDetail{}, err
	}

	if input.DryRun {
		return previewSchedule(normalized), nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ScheduleDetail{}, err
	}
	defer tx.Rollback()

	detail, err := createScheduleTx(ctx, tx, req, normalized)
	if err != nil {
		return ScheduleDetail{}, err
	}

	if err := tx.Commit(); err != nil {
		return ScheduleDetail{}, err
	}
	return detail, nil
}

func (s Service) EnsureSchedule(ctx context.Context, req requestctx.Context, input CreateScheduleInput) (ScheduleDetail, bool, error) {
	if s.DB == nil {
		return ScheduleDetail{}, false, fmt.Errorf("database is required")
	}
	normalized, err := s.normalizeCreateSchedule(ctx, req, input)
	if err != nil {
		return ScheduleDetail{}, false, err
	}

	if input.DryRun {
		return previewSchedule(normalized), false, nil
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ScheduleDetail{}, false, err
	}
	defer tx.Rollback()

	existing, err := scanSchedule(tx.QueryRowContext(ctx, scheduleSelectSQL()+`
		WHERE schedule_key = $1
		FOR UPDATE
	`, normalized.ScheduleKey))
	if err == sql.ErrNoRows {
		detail, err := createScheduleTx(ctx, tx, req, normalized)
		if err != nil {
			return ScheduleDetail{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return ScheduleDetail{}, false, err
		}
		return detail, true, nil
	}
	if err != nil {
		return ScheduleDetail{}, false, err
	}

	updatedAutomation, err := scanAutomation(tx.QueryRowContext(ctx, `
		UPDATE automation.automations
		SET display_name = $2,
		    description = $3,
		    status = $4,
		    source_profile_json = $5::jsonb,
		    target_profile_json = $6::jsonb,
		    communication_profile_json = '{"response_mode":"accepted"}'::jsonb,
		    idempotency_profile_json = '{"strategy":"source_record"}'::jsonb,
		    execution_profile_json = '{"mode":"worker_backed"}'::jsonb,
		    timeout_profile_json = $7::jsonb,
		    retry_profile_json = $8::jsonb,
		    concurrency_profile_json = $9::jsonb,
		    misfire_profile_json = $10::jsonb,
		    approval_profile_json = $11::jsonb,
		    run_as_actor_id = $12,
		    scope_id = $13,
		    project_id = $14,
		    metadata_json = $15::jsonb,
		    updated_at = now()
		WHERE automation_id = $1
		RETURNING automation_id, automation_key, display_name, description, status,
		          source_kind, source_profile_json, integration_profile_json,
		          mapping_profile_json, target_profile_json, communication_profile_json,
		          idempotency_profile_json, storage_profile_json,
		          execution_profile_json, timeout_profile_json, retry_profile_json,
		          concurrency_profile_json, misfire_profile_json, approval_profile_json,
		          created_by_actor_id, run_as_actor_id, scope_id, project_id,
		          created_at, updated_at, metadata_json
	`,
		existing.AutomationID,
		normalized.DisplayName,
		normalized.Description,
		automationStatusForScheduleStatus(normalized.Status),
		normalized.SourceProfileJSON,
		normalized.TargetProfileJSON,
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		normalized.ConcurrencyProfileJSON,
		normalized.MisfireProfileJSON,
		normalized.ApprovalProfileJSON,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return ScheduleDetail{}, false, err
	}

	updatedSchedule, err := scanSchedule(tx.QueryRowContext(ctx, `
		UPDATE automation.schedules
		SET display_name = $2,
		    description = $3,
		    status = $4,
		    schedule_kind = $5,
		    schedule_expr = $6,
		    timezone = $7,
		    start_at = $8,
		    next_fire_at = $9,
		    input_json = $10::jsonb,
		    target_profile_json = $11::jsonb,
		    misfire_profile_json = $12::jsonb,
		    concurrency_profile_json = $13::jsonb,
		    approval_profile_json = $14::jsonb,
		    timeout_profile_json = $15::jsonb,
		    retry_profile_json = $16::jsonb,
		    run_as_actor_id = $17,
		    scope_id = $18,
		    project_id = $19,
		    metadata_json = $20::jsonb,
		    updated_at = now()
		WHERE schedule_id = $1
		RETURNING schedule_id, automation_id, schedule_key, display_name, description,
		          status, schedule_kind, schedule_expr, timezone, start_at, next_fire_at,
		          last_fire_at, last_schedule_fire_id, input_json, target_profile_json,
		          misfire_profile_json, concurrency_profile_json, approval_profile_json,
		          timeout_profile_json, retry_profile_json, created_by_actor_id,
		          run_as_actor_id, scope_id, project_id, created_at, updated_at,
		          metadata_json
	`,
		existing.ScheduleID,
		normalized.DisplayName,
		normalized.Description,
		normalized.Status,
		normalized.ScheduleKind,
		normalized.ScheduleExpr,
		normalized.Timezone,
		nullableTime(normalized.StartAt),
		nullableTime(normalized.NextFireAt),
		normalized.InputJSON,
		normalized.TargetProfileJSON,
		normalized.MisfireProfileJSON,
		normalized.ConcurrencyProfileJSON,
		normalized.ApprovalProfileJSON,
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return ScheduleDetail{}, false, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleUpdated, "schedule", updatedSchedule.ScheduleID, updatedSchedule.Status, map[string]any{
		"schedule_key":      updatedSchedule.ScheduleKey,
		"automation_id":     updatedSchedule.AutomationID,
		"schedule_kind":     updatedSchedule.ScheduleKind,
		"schedule_expr":     updatedSchedule.ScheduleExpr,
		"target_capability": normalized.Target.CapabilityRef,
	}); err != nil {
		return ScheduleDetail{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return ScheduleDetail{}, false, err
	}
	return ScheduleDetail{Schedule: updatedSchedule, Automation: updatedAutomation}, false, nil
}

func (s Service) ListAutomations(ctx context.Context, filter AutomationFilter) ([]Automation, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := automationSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if sourceKind := strings.TrimSpace(filter.SourceKind); sourceKind != "" {
		add("source_kind =", sourceKind)
	}
	query += archivedProjectByProjectColumnSQL("project_id")
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Automation{}
	for rows.Next() {
		item, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) GetAutomation(ctx context.Context, ref string) (AutomationDetail, error) {
	if s.DB == nil {
		return AutomationDetail{}, fmt.Errorf("database is required")
	}
	automation, err := s.getAutomation(ctx, ref)
	if err != nil {
		return AutomationDetail{}, err
	}
	schedules, err := s.ListSchedules(ctx, ScheduleFilter{AutomationRef: automation.AutomationID, Limit: 100})
	if err != nil {
		return AutomationDetail{}, err
	}
	endpoints, err := s.ListDirectEventEndpoints(ctx, DirectEventEndpointFilter{AutomationRef: automation.AutomationID, Limit: 100})
	if err != nil {
		return AutomationDetail{}, err
	}
	return AutomationDetail{Automation: automation, Schedules: schedules, DirectEventEndpoints: endpoints}, nil
}

func (s Service) ListSchedules(ctx context.Context, filter ScheduleFilter) ([]Schedule, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := scheduleSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
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
	if !hasExplicitRuntimeRef(filter.AutomationRef, filter.ProjectRef) {
		query += archivedProjectByProjectColumnSQL("project_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) ListSchedulesForMaintenance(ctx context.Context, status string) ([]Schedule, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = ScheduleStatusActive
	}
	rows, err := s.DB.QueryContext(ctx, scheduleSelectSQL()+`
		WHERE status = $1
		ORDER BY created_at DESC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		item, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) GetSchedule(ctx context.Context, ref string) (ScheduleDetail, error) {
	if s.DB == nil {
		return ScheduleDetail{}, fmt.Errorf("database is required")
	}
	schedule, err := s.getSchedule(ctx, ref)
	if err != nil {
		return ScheduleDetail{}, err
	}
	automation, err := s.getAutomation(ctx, schedule.AutomationID)
	if err != nil {
		return ScheduleDetail{}, err
	}
	return ScheduleDetail{Schedule: schedule, Automation: automation}, nil
}

func (s Service) PauseSchedule(ctx context.Context, req requestctx.Context, ref string, input UpdateScheduleStatusInput) (ScheduleDetail, error) {
	return s.updateScheduleStatus(ctx, req, ref, ScheduleStatusPaused, events.TypeSchedulePaused, input)
}

func (s Service) ResumeSchedule(ctx context.Context, req requestctx.Context, ref string, input UpdateScheduleStatusInput) (ScheduleDetail, error) {
	return s.updateScheduleStatus(ctx, req, ref, ScheduleStatusActive, events.TypeScheduleResumed, input)
}

func (s Service) DisableSchedule(ctx context.Context, req requestctx.Context, ref string, input UpdateScheduleStatusInput) (ScheduleDetail, error) {
	return s.updateScheduleStatus(ctx, req, ref, ScheduleStatusDisabled, events.TypeScheduleDisabled, input)
}

func (s Service) ListScheduleFires(ctx context.Context, filter ScheduleFireFilter) ([]ScheduleFire, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := scheduleFireSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.ScheduleRef); ref != "" {
		schedule, err := s.getSchedule(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("schedule_id =", schedule.ScheduleID)
	}
	if ref := strings.TrimSpace(filter.AutomationRef); ref != "" {
		automation, err := s.getAutomation(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("automation_id =", automation.AutomationID)
	}
	if !hasExplicitRuntimeRef(filter.ScheduleRef, filter.AutomationRef) {
		query += archivedProjectByAutomationColumnSQL("automation_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleFire{}
	for rows.Next() {
		item, err := scanScheduleFire(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) GetScheduleFire(ctx context.Context, ref string) (ScheduleFire, error) {
	if s.DB == nil {
		return ScheduleFire{}, fmt.Errorf("database is required")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ScheduleFire{}, fmt.Errorf("schedule fire ref is required")
	}
	return scanScheduleFire(s.DB.QueryRowContext(ctx, scheduleFireSelectSQL()+`
		WHERE schedule_fire_id = $1
	`, ref))
}

func (s Service) ListInvocations(ctx context.Context, filter InvocationFilter) ([]Invocation, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := invocationSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if sourceKind := strings.TrimSpace(filter.SourceKind); sourceKind != "" {
		add("source_kind =", sourceKind)
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
	if !hasExplicitRuntimeRef(filter.AutomationRef, filter.ProjectRef) {
		query += archivedProjectByProjectColumnSQL("project_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

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

func (s Service) GetInvocation(ctx context.Context, ref string) (Invocation, error) {
	if s.DB == nil {
		return Invocation{}, fmt.Errorf("database is required")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Invocation{}, fmt.Errorf("invocation ref is required")
	}
	return scanInvocation(s.DB.QueryRowContext(ctx, invocationSelectSQL()+`
		WHERE invocation_id = $1
	`, ref))
}

type normalizedCreateSchedule struct {
	ScheduleKey            string
	DisplayName            string
	Description            string
	TargetCapability       string
	Target                 TargetProfile
	InputJSON              json.RawMessage
	ScheduleKind           string
	ScheduleExpr           string
	Timezone               string
	StartAt                *time.Time
	NextFireAt             *time.Time
	CreatedByActorID       string
	RunAsActorID           string
	ScopeID                string
	ProjectID              string
	SourceProfileJSON      json.RawMessage
	TargetProfileJSON      json.RawMessage
	MisfireProfileJSON     json.RawMessage
	ConcurrencyProfileJSON json.RawMessage
	ApprovalProfileJSON    json.RawMessage
	TimeoutProfileJSON     json.RawMessage
	RetryProfileJSON       json.RawMessage
	Metadata               json.RawMessage
	Status                 string
}

func (s Service) normalizeCreateSchedule(ctx context.Context, req requestctx.Context, input CreateScheduleInput) (normalizedCreateSchedule, error) {
	input.ScheduleKey = strings.ToLower(strings.TrimSpace(input.ScheduleKey))
	if input.ScheduleKey == "" {
		return normalizedCreateSchedule{}, fmt.Errorf("schedule_key is required")
	}
	if !validKey(input.ScheduleKey) {
		return normalizedCreateSchedule{}, fmt.Errorf("schedule_key must match ^[a-z][a-z0-9_-]{0,80}$")
	}
	// Calendar declarations stay disabled until explicitly activated. Preserve the
	// original default for one-shot and interval callers.
	if strings.TrimSpace(input.ScheduleKind) == ScheduleKindCron && strings.TrimSpace(input.Status) == "" {
		input.Status = ScheduleStatusDisabled
	}
	status, err := normalizeInitialScheduleStatus(input.Status)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	input.Status = status
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if input.DisplayName == "" {
		input.DisplayName = input.ScheduleKey
	}
	input.Description = strings.TrimSpace(input.Description)
	input.TargetCapability = strings.TrimSpace(input.TargetCapability)
	if input.TargetCapability == "" {
		return normalizedCreateSchedule{}, fmt.Errorf("target_capability is required")
	}
	if err := s.validateTargetCapability(ctx, input.TargetCapability); err != nil {
		return normalizedCreateSchedule{}, err
	}

	inputJSON, err := normalizeJSONObject(input.InputJSON, "input_json")
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	if strings.TrimSpace(input.ScheduleKind) == "" {
		return normalizedCreateSchedule{}, fmt.Errorf("schedule_kind is required")
	}
	plan, err := ParseScheduleExpressionInTimezone(input.ScheduleKind, input.ScheduleExpr, input.Timezone, time.Now().UTC())
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	scheduleExpr := strings.TrimSpace(input.ScheduleExpr)
	if plan.Kind == ScheduleKindOneShot && scheduleExpr == "now" && plan.NextFireAt != nil {
		scheduleExpr = plan.NextFireAt.Format(time.RFC3339)
	}
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return normalizedCreateSchedule{}, fmt.Errorf("timezone is invalid: %w", err)
	}

	scopeRef := strings.TrimSpace(input.ScopeRef)
	if scopeRef == "" {
		scopeRef = req.ScopeID
	}
	scopeID, err := resolveScopeID(ctx, s.DB, scopeRef)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	projectID := ""
	if strings.TrimSpace(input.ProjectRef) != "" {
		projectID, err = resolveProjectID(ctx, s.DB, input.ProjectRef)
		if err != nil {
			return normalizedCreateSchedule{}, err
		}
	}
	createdBy, err := resolveActorID(ctx, s.DB, req.ActorID)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	runAsRef := strings.TrimSpace(input.RunAsActorRef)
	if runAsRef == "" {
		runAsRef = defaultSchedulerActorRef
	}
	runAs, err := resolveActorID(ctx, s.DB, runAsRef)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}

	targetProfile := TargetProfile{
		CapabilityRef: input.TargetCapability,
		ScopeRef:      scopeRef,
		ProjectRef:    strings.TrimSpace(input.ProjectRef),
	}
	targetProfileJSON := mustJSON(targetProfile)
	if _, err := NormalizeTargetProfile(targetProfileJSON); err != nil {
		return normalizedCreateSchedule{}, err
	}
	misfireProfileJSON := mustJSON(MisfireProfile{
		Policy:                defaultString(input.MisfirePolicy, MisfireMarkMissed),
		LatenessWindowSeconds: input.LatenessWindowSecs,
	})
	misfireProfile, err := NormalizeMisfireProfile(misfireProfileJSON)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	misfireProfileJSON = mustJSON(misfireProfile)
	concurrencyProfileJSON := mustJSON(ConcurrencyProfile{Policy: defaultString(input.ConcurrencyPolicy, ConcurrencyAllowParallel)})
	concurrencyProfile, err := NormalizeConcurrencyProfile(concurrencyProfileJSON)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	concurrencyProfileJSON = mustJSON(concurrencyProfile)
	timeoutProfileJSON := mustJSON(TimeoutProfile{TimeoutSeconds: input.TimeoutSeconds})
	timeoutProfile, err := NormalizeTimeoutProfile(timeoutProfileJSON)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	timeoutProfileJSON = mustJSON(timeoutProfile)
	retryProfileJSON := mustJSON(RetryProfile{MaxAttempts: input.MaxAttempts})
	retryProfile, err := NormalizeRetryProfile(retryProfileJSON)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	retryProfileJSON = mustJSON(retryProfile)
	approvalMode := defaultString(input.ApprovalPolicy, ApprovalNone)
	approvalProfileJSON := mustJSON(ApprovalProfile{Mode: approvalMode})
	approvalProfile, err := NormalizeApprovalProfile(approvalProfileJSON)
	if err != nil {
		return normalizedCreateSchedule{}, err
	}
	approvalProfileJSON = mustJSON(approvalProfile)
	sourceProfileJSON := mustJSON(map[string]any{
		"source_kind":  SourceKindSchedule,
		"schedule_key": input.ScheduleKey,
		"name":         defaultAutomationSourceName,
	})

	return normalizedCreateSchedule{
		ScheduleKey:            input.ScheduleKey,
		DisplayName:            input.DisplayName,
		Description:            input.Description,
		TargetCapability:       input.TargetCapability,
		Target:                 targetProfile,
		InputJSON:              inputJSON,
		ScheduleKind:           plan.Kind,
		ScheduleExpr:           scheduleExpr,
		Timezone:               timezone,
		StartAt:                plan.StartAt,
		NextFireAt:             plan.NextFireAt,
		CreatedByActorID:       createdBy,
		RunAsActorID:           runAs,
		ScopeID:                scopeID,
		ProjectID:              projectID,
		SourceProfileJSON:      sourceProfileJSON,
		TargetProfileJSON:      targetProfileJSON,
		MisfireProfileJSON:     misfireProfileJSON,
		ConcurrencyProfileJSON: concurrencyProfileJSON,
		ApprovalProfileJSON:    approvalProfileJSON,
		TimeoutProfileJSON:     timeoutProfileJSON,
		RetryProfileJSON:       retryProfileJSON,
		Metadata:               metadata,
		Status:                 input.Status,
	}, nil
}

// previewSchedule contains no durable identities and performs no writes.
func previewSchedule(n normalizedCreateSchedule) ScheduleDetail {
	var scopeID, projectID *string
	if n.ScopeID != "" {
		scopeID = &n.ScopeID
	}
	if n.ProjectID != "" {
		projectID = &n.ProjectID
	}
	return ScheduleDetail{DryRun: true, Schedule: Schedule{
		ScheduleKey:            n.ScheduleKey,
		DisplayName:            n.DisplayName,
		Description:            n.Description,
		Status:                 n.Status,
		ScheduleKind:           n.ScheduleKind,
		ScheduleExpr:           n.ScheduleExpr,
		Timezone:               n.Timezone,
		StartAt:                n.StartAt,
		NextFireAt:             n.NextFireAt,
		InputJSON:              n.InputJSON,
		TargetProfileJSON:      n.TargetProfileJSON,
		MisfireProfileJSON:     n.MisfireProfileJSON,
		ConcurrencyProfileJSON: n.ConcurrencyProfileJSON,
		ApprovalProfileJSON:    n.ApprovalProfileJSON,
		TimeoutProfileJSON:     n.TimeoutProfileJSON,
		RetryProfileJSON:       n.RetryProfileJSON,
		CreatedByActorID:       n.CreatedByActorID,
		RunAsActorID:           n.RunAsActorID,
		ScopeID:                scopeID,
		ProjectID:              projectID,
		MetadataJSON:           n.Metadata,
	}}
}

func createScheduleTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, normalized normalizedCreateSchedule) (ScheduleDetail, error) {
	automationID := ids.NewAutomationID()
	scheduleID := ids.NewScheduleID()
	automation, err := scanAutomation(tx.QueryRowContext(ctx, automationInsertSQL(), automationID,
		normalized.ScheduleKey,
		normalized.DisplayName,
		normalized.Description,
		automationStatusForScheduleStatus(normalized.Status),
		SourceKindSchedule,
		normalized.SourceProfileJSON,
		json.RawMessage(`{}`),
		json.RawMessage(`{}`),
		normalized.TargetProfileJSON,
		json.RawMessage(`{"response_mode":"accepted"}`),
		json.RawMessage(`{"strategy":"source_record"}`),
		json.RawMessage(`{}`),
		json.RawMessage(`{"mode":"worker_backed"}`),
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		normalized.ConcurrencyProfileJSON,
		normalized.MisfireProfileJSON,
		normalized.ApprovalProfileJSON,
		normalized.CreatedByActorID,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return ScheduleDetail{}, err
	}

	schedule, err := scanSchedule(tx.QueryRowContext(ctx, scheduleInsertSQL(), scheduleID,
		automation.AutomationID,
		normalized.ScheduleKey,
		normalized.DisplayName,
		normalized.Description,
		normalized.Status,
		normalized.ScheduleKind,
		normalized.ScheduleExpr,
		normalized.Timezone,
		nullableTime(normalized.StartAt),
		nullableTime(normalized.NextFireAt),
		normalized.InputJSON,
		normalized.TargetProfileJSON,
		normalized.MisfireProfileJSON,
		normalized.ConcurrencyProfileJSON,
		normalized.ApprovalProfileJSON,
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		normalized.CreatedByActorID,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return ScheduleDetail{}, err
	}

	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeAutomationCreated, "automation", automation.AutomationID, automation.Status, map[string]any{
		"automation_key": automation.AutomationKey,
		"source_kind":    automation.SourceKind,
		"schedule_id":    schedule.ScheduleID,
	}); err != nil {
		return ScheduleDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleCreated, "schedule", schedule.ScheduleID, schedule.Status, map[string]any{
		"schedule_key":      schedule.ScheduleKey,
		"automation_id":     automation.AutomationID,
		"schedule_kind":     schedule.ScheduleKind,
		"schedule_expr":     schedule.ScheduleExpr,
		"target_capability": normalized.Target.CapabilityRef,
	}); err != nil {
		return ScheduleDetail{}, err
	}
	return ScheduleDetail{Schedule: schedule, Automation: automation}, nil
}

func (s Service) updateScheduleStatus(ctx context.Context, req requestctx.Context, ref, status, eventType string, input UpdateScheduleStatusInput) (ScheduleDetail, error) {
	if s.DB == nil {
		return ScheduleDetail{}, fmt.Errorf("database is required")
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return ScheduleDetail{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ScheduleDetail{}, err
	}
	defer tx.Rollback()

	schedule, err := scanSchedule(tx.QueryRowContext(ctx, scheduleSelectSQL()+`
		WHERE schedule_id = $1 OR schedule_key = $1
		FOR UPDATE
	`, strings.TrimSpace(ref)))
	if err != nil {
		return ScheduleDetail{}, err
	}
	updated, err := scanSchedule(tx.QueryRowContext(ctx, `
		UPDATE automation.schedules
		SET status = $2,
		    updated_at = now(),
		    metadata_json = CASE
		        WHEN $3::jsonb = '{}'::jsonb THEN metadata_json
		        ELSE metadata_json || $3::jsonb
		    END
		WHERE schedule_id = $1
		RETURNING schedule_id, automation_id, schedule_key, display_name, description,
		          status, schedule_kind, schedule_expr, timezone, start_at, next_fire_at,
		          last_fire_at, last_schedule_fire_id, input_json, target_profile_json,
		          misfire_profile_json, concurrency_profile_json, approval_profile_json,
		          timeout_profile_json, retry_profile_json, created_by_actor_id,
		          run_as_actor_id, scope_id, project_id, created_at, updated_at,
		          metadata_json
	`, schedule.ScheduleID, status, metadata))
	if err != nil {
		return ScheduleDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, eventType, "schedule", updated.ScheduleID, updated.Status, map[string]any{
		"schedule_key": updated.ScheduleKey,
		"reason":       strings.TrimSpace(input.Reason),
	}); err != nil {
		return ScheduleDetail{}, err
	}
	if status == ScheduleStatusPaused || status == ScheduleStatusDisabled || status == ScheduleStatusActive {
		automationStatus := AutomationStatusActive
		automationEvent := events.TypeAutomationResumed
		if status == ScheduleStatusPaused {
			automationStatus = AutomationStatusPaused
			automationEvent = events.TypeAutomationPaused
		}
		if status == ScheduleStatusDisabled {
			automationStatus = AutomationStatusDisabled
			automationEvent = events.TypeAutomationDisabled
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE automation.automations
			SET status = $2, updated_at = now()
			WHERE automation_id = $1
		`, updated.AutomationID, automationStatus); err != nil {
			return ScheduleDetail{}, err
		}
		if err := appendLifecycleEventTx(ctx, tx, req, automationEvent, "automation", updated.AutomationID, automationStatus, map[string]any{
			"schedule_id": updated.ScheduleID,
			"reason":      strings.TrimSpace(input.Reason),
		}); err != nil {
			return ScheduleDetail{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ScheduleDetail{}, err
	}
	return s.GetSchedule(ctx, updated.ScheduleID)
}

func (s Service) getAutomation(ctx context.Context, ref string) (Automation, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Automation{}, fmt.Errorf("automation ref is required")
	}
	return scanAutomation(s.DB.QueryRowContext(ctx, automationSelectSQL()+`
		WHERE automation_id = $1 OR automation_key = $1
	`, ref))
}

func (s Service) getSchedule(ctx context.Context, ref string) (Schedule, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Schedule{}, fmt.Errorf("schedule ref is required")
	}
	return scanSchedule(s.DB.QueryRowContext(ctx, scheduleSelectSQL()+`
		WHERE schedule_id = $1 OR schedule_key = $1
	`, ref))
}

func (s Service) validateTargetCapability(ctx context.Context, ref string) error {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "capability:")
	if ref == "" {
		return fmt.Errorf("target capability is required")
	}
	var endpointID string
	err := s.DB.QueryRowContext(ctx, `
		SELECT endpoint.capability_endpoint_id
		FROM capabilities.capability_endpoints endpoint
		JOIN capabilities.providers provider ON provider.provider_id = endpoint.provider_id
		WHERE endpoint.compact_address = $1
		  AND endpoint.status = 'active'
		  AND provider.status = 'active'
	`, ref).Scan(&endpointID)
	if err != nil {
		return fmt.Errorf("target capability is not active or does not exist: %s: %w", ref, err)
	}
	return nil
}

func appendLifecycleEventTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType, targetKind, targetID, status string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	_, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         req.ScopeID,
		TargetKind:      targetKind,
		TargetID:        targetID,
		Status:          status,
		Payload:         payload,
		VisibilityClass: "internal",
	})
	return err
}

func normalizeLimit(limit int) int {
	if limit <= 0 || limit > maxScheduleListLimit {
		return defaultScheduleListLimit
	}
	return limit
}

func resolveActorID(ctx context.Context, db queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("actor ref is required")
	}
	var id string
	err := db.QueryRowContext(ctx, `
		SELECT actor_id
		FROM identity.actors
		WHERE (actor_id = $1 OR actor_key = $1)
		  AND status = 'active'
	`, ref).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("resolve actor %q: %w", ref, err)
	}
	return id, nil
}

func resolveScopeID(ctx context.Context, db queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("scope ref is required")
	}
	var id string
	err := db.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE (scope_id = $1 OR scope_key = $1 OR slug = $1)
		  AND status = 'active'
	`, ref).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("resolve scope %q: %w", ref, err)
	}
	return id, nil
}

func resolveProjectID(ctx context.Context, db queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("project ref is required")
	}
	var id string
	err := db.QueryRowContext(ctx, `
		SELECT project_id
		FROM projects.projects
		WHERE (project_id = $1 OR slug = $1)
		  AND status IN ('active', 'paused', 'blocked')
	`, ref).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("resolve project %q: %w", ref, err)
	}
	return id, nil
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func validInitialScheduleStatus(status string) bool {
	switch status {
	case ScheduleStatusActive, ScheduleStatusPaused, ScheduleStatusDisabled:
		return true
	default:
		return false
	}
}

func normalizeInitialScheduleStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return ScheduleStatusActive, nil
	}
	if !validInitialScheduleStatus(status) {
		return "", fmt.Errorf("unsupported schedule status: %s", status)
	}
	return status, nil
}

func automationStatusForScheduleStatus(status string) string {
	switch status {
	case ScheduleStatusPaused:
		return AutomationStatusPaused
	case ScheduleStatusDisabled:
		return AutomationStatusDisabled
	default:
		return AutomationStatusActive
	}
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
