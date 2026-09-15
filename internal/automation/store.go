package automation

import (
	"database/sql"
	"encoding/json"
	"time"
)

func automationSelectSQL() string {
	return `
		SELECT automation_id, automation_key, display_name, description, status,
		       source_kind, source_profile_json, integration_profile_json,
		       mapping_profile_json, target_profile_json, communication_profile_json,
		       idempotency_profile_json, storage_profile_json,
		       execution_profile_json, timeout_profile_json, retry_profile_json,
		       concurrency_profile_json, misfire_profile_json, approval_profile_json,
		       created_by_actor_id, run_as_actor_id, scope_id, project_id,
		       created_at, updated_at, metadata_json
		FROM automation.automations
	`
}

func automationInsertSQL() string {
	return `
		INSERT INTO automation.automations (
			automation_id, automation_key, display_name, description, status,
			source_kind, source_profile_json, integration_profile_json,
			mapping_profile_json, target_profile_json, communication_profile_json,
			idempotency_profile_json, storage_profile_json,
			execution_profile_json, timeout_profile_json, retry_profile_json,
			concurrency_profile_json, misfire_profile_json, approval_profile_json,
			created_by_actor_id, run_as_actor_id, scope_id, project_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10::jsonb,
		        $11::jsonb, $12::jsonb, $13::jsonb, $14::jsonb, $15::jsonb, $16::jsonb,
		        $17::jsonb, $18::jsonb, $19::jsonb, $20, $21, $22, $23, $24::jsonb)
		RETURNING automation_id, automation_key, display_name, description, status,
		          source_kind, source_profile_json, integration_profile_json,
		          mapping_profile_json, target_profile_json, communication_profile_json,
		          idempotency_profile_json, storage_profile_json,
		          execution_profile_json, timeout_profile_json, retry_profile_json,
		          concurrency_profile_json, misfire_profile_json, approval_profile_json,
		          created_by_actor_id, run_as_actor_id, scope_id, project_id,
		          created_at, updated_at, metadata_json
	`
}

func scheduleSelectSQL() string {
	return `
		SELECT schedule_id, automation_id, schedule_key, display_name, description,
		       status, schedule_kind, schedule_expr, timezone, start_at, next_fire_at,
		       last_fire_at, last_schedule_fire_id, input_json, target_profile_json,
		       misfire_profile_json, concurrency_profile_json, approval_profile_json,
		       timeout_profile_json, retry_profile_json, created_by_actor_id,
		       run_as_actor_id, scope_id, project_id, created_at, updated_at,
		       metadata_json
		FROM automation.schedules
	`
}

func scheduleInsertSQL() string {
	return `
		INSERT INTO automation.schedules (
			schedule_id, automation_id, schedule_key, display_name, description,
			status, schedule_kind, schedule_expr, timezone, start_at, next_fire_at,
			input_json, target_profile_json, misfire_profile_json, concurrency_profile_json,
			approval_profile_json, timeout_profile_json, retry_profile_json,
			created_by_actor_id, run_as_actor_id, scope_id, project_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb,
		        $14::jsonb, $15::jsonb, $16::jsonb, $17::jsonb, $18::jsonb,
		        $19, $20, $21, $22, $23::jsonb)
		RETURNING schedule_id, automation_id, schedule_key, display_name, description,
		          status, schedule_kind, schedule_expr, timezone, start_at, next_fire_at,
		          last_fire_at, last_schedule_fire_id, input_json, target_profile_json,
		          misfire_profile_json, concurrency_profile_json, approval_profile_json,
		          timeout_profile_json, retry_profile_json, created_by_actor_id,
		          run_as_actor_id, scope_id, project_id, created_at, updated_at,
		          metadata_json
	`
}

func scheduleFireSelectSQL() string {
	return `
		SELECT schedule_fire_id, schedule_id, automation_id, scheduled_for, status,
		       misfire_status, lateness_seconds, worker_run_id, invocation_id, route_id,
		       capability_call_id, job_id, failure_code, failure_message,
		       created_at, updated_at, completed_at, failed_at, metadata_json
		FROM automation.schedule_fires
	`
}

func invocationSelectSQL() string {
	return `
		SELECT invocation_id, automation_id, source_kind, source_ref,
		       source_occurrence_ref, actor_id, origin_node_id, scope_id, project_id,
		       target_capability, input_json, input_hash, idempotency_key, status,
		       attempt_count, max_attempts, next_attempt_at, leased_by_worker_run_id,
		       leased_at, lease_expires_at, route_id, capability_call_id, job_id,
		       policy_decision_id, approval_id, grant_id, result_json, result_refs_json,
		       failure_code, failure_message, created_at, updated_at, started_at,
		       completed_at, failed_at, metadata_json
		FROM automation.invocations
	`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAutomation(scanner rowScanner) (Automation, error) {
	var out Automation
	var source, integration, mapping, target, communication, idempotency, storage, execution, timeoutProfile, retry, concurrency, misfire, approval, metadata []byte
	var scopeID, projectID sql.NullString
	if err := scanner.Scan(
		&out.AutomationID,
		&out.AutomationKey,
		&out.DisplayName,
		&out.Description,
		&out.Status,
		&out.SourceKind,
		&source,
		&integration,
		&mapping,
		&target,
		&communication,
		&idempotency,
		&storage,
		&execution,
		&timeoutProfile,
		&retry,
		&concurrency,
		&misfire,
		&approval,
		&out.CreatedByActorID,
		&out.RunAsActorID,
		&scopeID,
		&projectID,
		&out.CreatedAt,
		&out.UpdatedAt,
		&metadata,
	); err != nil {
		return Automation{}, err
	}
	out.SourceProfileJSON = jsonOrDefault(source)
	out.IntegrationProfileJSON = jsonOrDefault(integration)
	out.MappingProfileJSON = jsonOrDefault(mapping)
	out.TargetProfileJSON = jsonOrDefault(target)
	out.CommunicationProfileJSON = jsonOrDefault(communication)
	out.IdempotencyProfileJSON = jsonOrDefault(idempotency)
	out.StorageProfileJSON = jsonOrDefault(storage)
	out.ExecutionProfileJSON = jsonOrDefault(execution)
	out.TimeoutProfileJSON = jsonOrDefault(timeoutProfile)
	out.RetryProfileJSON = jsonOrDefault(retry)
	out.ConcurrencyProfileJSON = jsonOrDefault(concurrency)
	out.MisfireProfileJSON = jsonOrDefault(misfire)
	out.ApprovalProfileJSON = jsonOrDefault(approval)
	out.ScopeID = stringPtr(scopeID)
	out.ProjectID = stringPtr(projectID)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanSchedule(scanner rowScanner) (Schedule, error) {
	var out Schedule
	var input, target, misfire, concurrency, approval, timeoutProfile, retry, metadata []byte
	var startAt, nextFireAt, lastFireAt sql.NullTime
	var lastFireID, scopeID, projectID sql.NullString
	if err := scanner.Scan(
		&out.ScheduleID,
		&out.AutomationID,
		&out.ScheduleKey,
		&out.DisplayName,
		&out.Description,
		&out.Status,
		&out.ScheduleKind,
		&out.ScheduleExpr,
		&out.Timezone,
		&startAt,
		&nextFireAt,
		&lastFireAt,
		&lastFireID,
		&input,
		&target,
		&misfire,
		&concurrency,
		&approval,
		&timeoutProfile,
		&retry,
		&out.CreatedByActorID,
		&out.RunAsActorID,
		&scopeID,
		&projectID,
		&out.CreatedAt,
		&out.UpdatedAt,
		&metadata,
	); err != nil {
		return Schedule{}, err
	}
	out.StartAt = timePtr(startAt)
	out.NextFireAt = timePtr(nextFireAt)
	out.LastFireAt = timePtr(lastFireAt)
	out.LastScheduleFireID = stringPtr(lastFireID)
	out.InputJSON = jsonOrDefault(input)
	out.TargetProfileJSON = jsonOrDefault(target)
	out.MisfireProfileJSON = jsonOrDefault(misfire)
	out.ConcurrencyProfileJSON = jsonOrDefault(concurrency)
	out.ApprovalProfileJSON = jsonOrDefault(approval)
	out.TimeoutProfileJSON = jsonOrDefault(timeoutProfile)
	out.RetryProfileJSON = jsonOrDefault(retry)
	out.ScopeID = stringPtr(scopeID)
	out.ProjectID = stringPtr(projectID)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanScheduleFire(scanner rowScanner) (ScheduleFire, error) {
	var out ScheduleFire
	var workerRunID, invocationID, routeID, callID, jobID, failureCode, failureMessage sql.NullString
	var completedAt, failedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&out.ScheduleFireID,
		&out.ScheduleID,
		&out.AutomationID,
		&out.ScheduledFor,
		&out.Status,
		&out.MisfireStatus,
		&out.LatenessSeconds,
		&workerRunID,
		&invocationID,
		&routeID,
		&callID,
		&jobID,
		&failureCode,
		&failureMessage,
		&out.CreatedAt,
		&out.UpdatedAt,
		&completedAt,
		&failedAt,
		&metadata,
	); err != nil {
		return ScheduleFire{}, err
	}
	out.WorkerRunID = stringPtr(workerRunID)
	out.InvocationID = stringPtr(invocationID)
	out.RouteID = stringPtr(routeID)
	out.CapabilityCallID = stringPtr(callID)
	out.JobID = stringPtr(jobID)
	out.FailureCode = stringPtr(failureCode)
	out.FailureMessage = stringPtr(failureMessage)
	out.CompletedAt = timePtr(completedAt)
	out.FailedAt = timePtr(failedAt)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanInvocation(scanner rowScanner) (Invocation, error) {
	var out Invocation
	var scopeID, projectID, leasedBy, routeID, callID, jobID, policyDecisionID, approvalID, grantID, failureCode, failureMessage sql.NullString
	var nextAttemptAt, leasedAt, leaseExpiresAt, startedAt, completedAt, failedAt sql.NullTime
	var input, result, resultRefs, metadata []byte
	if err := scanner.Scan(
		&out.InvocationID,
		&out.AutomationID,
		&out.SourceKind,
		&out.SourceRef,
		&out.SourceOccurrenceRef,
		&out.ActorID,
		&out.OriginNodeID,
		&scopeID,
		&projectID,
		&out.TargetCapability,
		&input,
		&out.InputHash,
		&out.IdempotencyKey,
		&out.Status,
		&out.AttemptCount,
		&out.MaxAttempts,
		&nextAttemptAt,
		&leasedBy,
		&leasedAt,
		&leaseExpiresAt,
		&routeID,
		&callID,
		&jobID,
		&policyDecisionID,
		&approvalID,
		&grantID,
		&result,
		&resultRefs,
		&failureCode,
		&failureMessage,
		&out.CreatedAt,
		&out.UpdatedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&metadata,
	); err != nil {
		return Invocation{}, err
	}
	out.ScopeID = stringPtr(scopeID)
	out.ProjectID = stringPtr(projectID)
	out.InputJSON = jsonOrDefault(input)
	out.NextAttemptAt = timePtr(nextAttemptAt)
	out.LeasedByWorkerRunID = stringPtr(leasedBy)
	out.LeasedAt = timePtr(leasedAt)
	out.LeaseExpiresAt = timePtr(leaseExpiresAt)
	out.RouteID = stringPtr(routeID)
	out.CapabilityCallID = stringPtr(callID)
	out.JobID = stringPtr(jobID)
	out.PolicyDecisionID = stringPtr(policyDecisionID)
	out.ApprovalID = stringPtr(approvalID)
	out.GrantID = stringPtr(grantID)
	out.ResultJSON = jsonOrDefault(result)
	out.ResultRefsJSON = jsonOrDefault(resultRefs)
	out.FailureCode = stringPtr(failureCode)
	out.FailureMessage = stringPtr(failureMessage)
	out.StartedAt = timePtr(startedAt)
	out.CompletedAt = timePtr(completedAt)
	out.FailedAt = timePtr(failedAt)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func jsonOrDefault(raw []byte) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	out := value.String
	return &out
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time.UTC()
	return &out
}
