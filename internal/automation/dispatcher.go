package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

const (
	defaultDispatcherBatchSize       = 10
	maxDispatcherBatchSize           = 100
	defaultDispatcherLeaseDuration   = 2 * time.Minute
	defaultDispatcherRetryDelay      = time.Minute
	maxDispatcherRetryDelay          = 5 * time.Minute
	defaultDispatcherCapabilityScope = defaultSchedulerScope
)

type dispatchOutcome struct {
	Invocation         Invocation
	ScheduleFire       ScheduleFire
	Status             string
	Retrying           bool
	Succeeded          bool
	Failed             bool
	WaitingApproval    bool
	ManualAction       bool
	LastCapabilityCall string
	LastRouteID        string
	LastJobID          string
}

func (s Service) RunDispatcher(ctx context.Context, req requestctx.Context, input DispatcherRunInput) (DispatcherRunResult, error) {
	if s.DB == nil {
		return DispatcherRunResult{}, fmt.Errorf("database is required")
	}
	if s.Routing.DB == nil {
		return DispatcherRunResult{}, fmt.Errorf("routing service is required")
	}
	input = normalizeDispatcherRunInput(input)

	claimed, err := s.claimPendingInvocations(ctx, req, input)
	if err != nil {
		return DispatcherRunResult{}, err
	}
	result := DispatcherRunResult{Claimed: int64(len(claimed))}
	for _, invocation := range claimed {
		outcome, err := s.dispatchClaimedInvocation(ctx, req, invocation, input)
		if err != nil {
			return result, err
		}
		result.LastInvocationID = outcome.Invocation.InvocationID
		if outcome.LastCapabilityCall != "" {
			result.LastCapabilityCall = outcome.LastCapabilityCall
		}
		if outcome.LastRouteID != "" {
			result.LastRouteID = outcome.LastRouteID
		}
		if outcome.LastJobID != "" {
			result.LastJobID = outcome.LastJobID
		}
		if outcome.Succeeded {
			result.Succeeded++
		}
		if outcome.Failed {
			result.Failed++
		}
		if outcome.WaitingApproval {
			result.WaitingApproval++
		}
		if outcome.Retrying {
			result.Retrying++
		}
		if outcome.ManualAction {
			result.ManualAction++
		}
	}
	return result, nil
}

func (s Service) DispatchInvocationNow(ctx context.Context, req requestctx.Context, invocationRef string, input DispatcherRunInput) (DispatcherRunResult, error) {
	invocationRef = strings.TrimSpace(invocationRef)
	if invocationRef == "" {
		return DispatcherRunResult{}, fmt.Errorf("invocation ref is required")
	}
	input.InvocationRef = invocationRef
	input.BatchSize = 1
	return s.RunDispatcher(ctx, req, input)
}

func normalizeDispatcherRunInput(input DispatcherRunInput) DispatcherRunInput {
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if input.BatchSize <= 0 || input.BatchSize > maxDispatcherBatchSize {
		input.BatchSize = defaultDispatcherBatchSize
	}
	if input.LeaseDuration <= 0 {
		input.LeaseDuration = defaultDispatcherLeaseDuration
	}
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	input.InvocationRef = strings.TrimSpace(input.InvocationRef)
	if input.InvocationRef != "" {
		input.BatchSize = 1
	}
	return input
}

func (s Service) claimPendingInvocations(ctx context.Context, req requestctx.Context, input DispatcherRunInput) ([]Invocation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := invocationSelectSQL() + `
		WHERE status = $1
		  AND (next_attempt_at IS NULL OR next_attempt_at <= $2)
	`
	args := []any{InvocationStatusPending, input.Now}
	if input.InvocationRef != "" {
		args = append(args, input.InvocationRef)
		query += fmt.Sprintf(" AND invocation_id = $%d", len(args))
	}
	args = append(args, input.BatchSize)
	query += fmt.Sprintf(`
		ORDER BY created_at ASC
		LIMIT $%d
		FOR UPDATE SKIP LOCKED
	`, len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pending []Invocation
	for rows.Next() {
		invocation, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		pending = append(pending, invocation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	claimed := make([]Invocation, 0, len(pending))
	for _, invocation := range pending {
		updated, err := scanInvocation(tx.QueryRowContext(ctx, `
			UPDATE automation.invocations
			SET status = $2,
			    attempt_count = attempt_count + 1,
			    leased_by_worker_run_id = nullif($3, ''),
			    leased_at = $4,
			    lease_expires_at = $5,
			    updated_at = now()
			WHERE invocation_id = $1
			RETURNING invocation_id, automation_id, source_kind, source_ref,
			          source_occurrence_ref, actor_id, origin_node_id, scope_id,
			          project_id, target_capability, input_json, input_hash,
			          idempotency_key, status, attempt_count, max_attempts,
			          next_attempt_at, leased_by_worker_run_id, leased_at,
			          lease_expires_at, route_id, capability_call_id, job_id,
			          policy_decision_id, approval_id, grant_id, result_json,
			          result_refs_json, failure_code, failure_message, created_at,
			          updated_at, started_at, completed_at, failed_at, metadata_json
		`, invocation.InvocationID, InvocationStatusLeased, input.WorkerRunID, input.Now, input.Now.Add(input.LeaseDuration)))
		if err != nil {
			return nil, err
		}
		if err := appendLifecycleEventTx(ctx, tx, req, events.TypeInvocationLeased, "invocation", updated.InvocationID, updated.Status, map[string]any{
			"worker_run_id":    input.WorkerRunID,
			"attempt_count":    updated.AttemptCount,
			"lease_expires_at": input.Now.Add(input.LeaseDuration).Format(time.RFC3339Nano),
		}); err != nil {
			return nil, err
		}
		claimed = append(claimed, updated)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s Service) dispatchClaimedInvocation(ctx context.Context, req requestctx.Context, invocation Invocation, input DispatcherRunInput) (dispatchOutcome, error) {
	if err := ensureInvocationRuntimeActive(ctx, s.DB, invocation); err != nil {
		return s.markInvocationOutcome(ctx, req, invocation, input.Now, routing.CapabilityCallOutcome{}, InvocationStatusRequiresManualAction, FireStatusRequiresManualAction, events.TypeInvocationFailed, events.TypeScheduleFireFailed, "project_runtime.archived", err.Error())
	}
	calling, err := s.markInvocationCalling(ctx, req, invocation, input.Now)
	if err != nil {
		return dispatchOutcome{}, err
	}
	invocation = calling

	switch invocation.SourceKind {
	case SourceKindSchedule:
		return s.dispatchScheduleInvocation(ctx, req, invocation, input)
	case SourceKindDirectEvent:
		return s.dispatchDirectEventInvocation(ctx, req, invocation, input)
	default:
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "source_kind.unsupported", "unsupported invocation source kind: "+invocation.SourceKind)
	}
}

func (s Service) dispatchScheduleInvocation(ctx context.Context, req requestctx.Context, invocation Invocation, input DispatcherRunInput) (dispatchOutcome, error) {
	schedule, err := s.getSchedule(ctx, invocation.SourceRef)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "schedule.resolve_failed", err.Error())
	}

	scopeRef := defaultDispatcherCapabilityScope
	if invocation.ScopeID != nil && strings.TrimSpace(*invocation.ScopeID) != "" {
		scopeRef = *invocation.ScopeID
	}
	schedulerReq, err := requestctx.ResolveScheduler(ctx, s.DB, req.CorrelationID, requestctx.SchedulerResolveInput{
		ActorRef:      defaultSchedulerActorRef,
		OriginNodeRef: defaultSchedulerOriginNode,
		ScopeRef:      scopeRef,
		Source:        "schedule:" + schedule.ScheduleKey,
	})
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "request_context.resolve_failed", err.Error())
	}

	timeoutProfile, err := NormalizeTimeoutProfile(schedule.TimeoutProfileJSON)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "timeout_profile.invalid", err.Error())
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutProfile.TimeoutSeconds)*time.Second)
	defer cancel()

	approvalProfile, _ := NormalizeApprovalProfile(schedule.ApprovalProfileJSON)
	outcome, callErr := s.Routing.Call(callCtx, schedulerReq, routing.CapabilityCallInput{
		Target:          invocation.TargetCapability,
		ActorRef:        invocation.ActorID,
		OriginNodeRef:   invocation.OriginNodeID,
		ScopeRef:        scopeRef,
		Input:           invocation.InputJSON,
		RequestApproval: approvalProfile.Mode == ApprovalCreateAndWait,
		ApprovalReason:  "automation schedule " + schedule.ScheduleKey,
		Metadata: mustJSON(map[string]any{
			"schema_version":   "automation_dispatch.metadata.v0.2",
			"automation_id":    invocation.AutomationID,
			"invocation_id":    invocation.InvocationID,
			"schedule_id":      schedule.ScheduleID,
			"schedule_key":     schedule.ScheduleKey,
			"schedule_fire_id": invocation.SourceOccurrenceRef,
			"worker_run_id":    input.WorkerRunID,
			"attempt_count":    invocation.AttemptCount,
		}),
	}, dispatcherIdempotencyKey(invocation))
	if callErr != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return s.markInvocationOutcome(ctx, req, invocation, input.Now, outcome, InvocationStatusTimedOut, FireStatusFailed, events.TypeInvocationTimedOut, events.TypeScheduleFireFailed, "automation.dispatch_timed_out", callErr.Error())
		}
		if outcome.Route.RouteID == "" && outcome.CapabilityCall.CapabilityCallID == "" {
			return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "routing.call_failed", callErr.Error())
		}
		return s.markInvocationFailedOrRetry(ctx, req, invocation, input.Now, outcome, "routing.call_failed", callErr.Error())
	}

	switch outcome.Status {
	case routing.CapabilityCallStatusCompleted, routing.CapabilityCallStatusDispatched:
		return s.markInvocationOutcome(ctx, req, invocation, input.Now, outcome, InvocationStatusSucceeded, FireStatusCompleted, events.TypeInvocationCompleted, events.TypeScheduleFireCompleted, "", "")
	case routing.CapabilityCallStatusApprovalRequired:
		return s.markInvocationOutcome(ctx, req, invocation, input.Now, outcome, InvocationStatusWaitingApproval, FireStatusRequiresManualAction, events.TypeInvocationApprovalRequired, events.TypeScheduleFireFailed, "", "")
	case routing.CapabilityCallStatusFailed, routing.CapabilityCallStatusCancelled:
		return s.markInvocationFailedOrRetry(ctx, req, invocation, input.Now, outcome, defaultString(outcome.ErrorCode, "capability_call.failed"), outcome.ErrorMessage)
	default:
		return s.markInvocationFailedOrRetry(ctx, req, invocation, input.Now, outcome, "capability_call.unsupported_status", "unsupported capability call status: "+outcome.Status)
	}
}

func (s Service) dispatchDirectEventInvocation(ctx context.Context, req requestctx.Context, invocation Invocation, input DispatcherRunInput) (dispatchOutcome, error) {
	directEvent, err := s.getDirectEvent(ctx, invocation.SourceOccurrenceRef)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "direct_event.resolve_failed", err.Error())
	}
	endpoint, err := s.getDirectEventEndpoint(ctx, directEvent.EndpointID)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "direct_event_endpoint.resolve_failed", err.Error())
	}
	integration, err := s.getIntegration(ctx, directEvent.IntegrationID)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "integration.resolve_failed", err.Error())
	}
	automation, err := s.getAutomation(ctx, directEvent.AutomationID)
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "automation.resolve_failed", err.Error())
	}

	scopeRef := defaultDispatcherCapabilityScope
	if invocation.ScopeID != nil && strings.TrimSpace(*invocation.ScopeID) != "" {
		scopeRef = *invocation.ScopeID
	}
	externalReq, err := requestctx.ResolveExternalIntegration(ctx, s.DB, req.CorrelationID, requestctx.ExternalIntegrationResolveInput{
		ActorRef:      integration.ActorID,
		OriginNodeRef: defaultSchedulerOriginNode,
		ScopeRef:      scopeRef,
		Source:        "direct_event:" + endpoint.EndpointSlug,
	})
	if err != nil {
		return s.markInvocationRoutingError(ctx, req, invocation, input.Now, "request_context.resolve_failed", err.Error())
	}

	timeoutProfile, err := NormalizeTimeoutProfile(automation.TimeoutProfileJSON)
	if err != nil {
		return s.markInvocationRoutingError(ctx, externalReq, invocation, input.Now, "timeout_profile.invalid", err.Error())
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutProfile.TimeoutSeconds)*time.Second)
	defer cancel()

	approvalProfile, _ := NormalizeApprovalProfile(automation.ApprovalProfileJSON)
	outcome, callErr := s.Routing.Call(callCtx, externalReq, routing.CapabilityCallInput{
		Target:          invocation.TargetCapability,
		ActorRef:        invocation.ActorID,
		OriginNodeRef:   invocation.OriginNodeID,
		ScopeRef:        scopeRef,
		Input:           invocation.InputJSON,
		RequestApproval: approvalProfile.Mode == ApprovalCreateAndWait,
		ApprovalReason:  "direct event " + endpoint.EndpointSlug,
		Metadata: mustJSON(map[string]any{
			"schema_version":   "automation_dispatch.metadata.v0.2",
			"automation_id":    invocation.AutomationID,
			"invocation_id":    invocation.InvocationID,
			"direct_event_id":  directEvent.DirectEventID,
			"endpoint_id":      endpoint.EndpointID,
			"endpoint_slug":    endpoint.EndpointSlug,
			"integration_id":   integration.IntegrationID,
			"integration_key":  integration.IntegrationKey,
			"worker_run_id":    input.WorkerRunID,
			"attempt_count":    invocation.AttemptCount,
			"source_kind":      SourceKindDirectEvent,
			"source_record_id": directEvent.DirectEventID,
		}),
	}, dispatcherIdempotencyKey(invocation))
	if callErr != nil {
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return s.markInvocationOutcome(ctx, externalReq, invocation, input.Now, outcome, InvocationStatusTimedOut, FireStatusFailed, events.TypeInvocationTimedOut, events.TypeScheduleFireFailed, "automation.dispatch_timed_out", callErr.Error())
		}
		if outcome.Route.RouteID == "" && outcome.CapabilityCall.CapabilityCallID == "" {
			return s.markInvocationRoutingError(ctx, externalReq, invocation, input.Now, "routing.call_failed", callErr.Error())
		}
		return s.markInvocationFailedOrRetry(ctx, externalReq, invocation, input.Now, outcome, "routing.call_failed", callErr.Error())
	}

	switch outcome.Status {
	case routing.CapabilityCallStatusCompleted, routing.CapabilityCallStatusDispatched:
		return s.markInvocationOutcome(ctx, externalReq, invocation, input.Now, outcome, InvocationStatusSucceeded, FireStatusCompleted, events.TypeInvocationCompleted, events.TypeScheduleFireCompleted, "", "")
	case routing.CapabilityCallStatusApprovalRequired:
		return s.markInvocationOutcome(ctx, externalReq, invocation, input.Now, outcome, InvocationStatusWaitingApproval, FireStatusRequiresManualAction, events.TypeInvocationApprovalRequired, events.TypeScheduleFireFailed, "approval.required", "direct-event dispatch required approval")
	case routing.CapabilityCallStatusFailed, routing.CapabilityCallStatusCancelled:
		return s.markInvocationFailedOrRetry(ctx, externalReq, invocation, input.Now, outcome, defaultString(outcome.ErrorCode, "capability_call.failed"), outcome.ErrorMessage)
	default:
		return s.markInvocationFailedOrRetry(ctx, externalReq, invocation, input.Now, outcome, "capability_call.unsupported_status", "unsupported capability call status: "+outcome.Status)
	}
}

func (s Service) markInvocationCalling(ctx context.Context, req requestctx.Context, invocation Invocation, now time.Time) (Invocation, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Invocation{}, err
	}
	defer tx.Rollback()

	updated, err := scanInvocation(tx.QueryRowContext(ctx, `
		UPDATE automation.invocations
		SET status = $2,
		    started_at = COALESCE(started_at, $3),
		    updated_at = now()
		WHERE invocation_id = $1
		RETURNING invocation_id, automation_id, source_kind, source_ref,
		          source_occurrence_ref, actor_id, origin_node_id, scope_id,
		          project_id, target_capability, input_json, input_hash,
		          idempotency_key, status, attempt_count, max_attempts,
		          next_attempt_at, leased_by_worker_run_id, leased_at,
		          lease_expires_at, route_id, capability_call_id, job_id,
		          policy_decision_id, approval_id, grant_id, result_json,
		          result_refs_json, failure_code, failure_message, created_at,
		          updated_at, started_at, completed_at, failed_at, metadata_json
	`, invocation.InvocationID, InvocationStatusCalling, now))
	if err != nil {
		return Invocation{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeInvocationCalling, "invocation", updated.InvocationID, updated.Status, map[string]any{
		"attempt_count": updated.AttemptCount,
	}); err != nil {
		return Invocation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Invocation{}, err
	}
	return updated, nil
}

func (s Service) markInvocationFailedOrRetry(ctx context.Context, req requestctx.Context, invocation Invocation, now time.Time, outcome routing.CapabilityCallOutcome, code, message string) (dispatchOutcome, error) {
	if invocation.AttemptCount < invocation.MaxAttempts {
		nextAttempt := now.Add(dispatcherRetryDelay(invocation.AttemptCount))
		return s.markInvocationRetry(ctx, req, invocation, now, outcome, nextAttempt, code, message)
	}
	return s.markInvocationOutcome(ctx, req, invocation, now, outcome, InvocationStatusRequiresManualAction, FireStatusRequiresManualAction, events.TypeInvocationFailed, events.TypeScheduleFireFailed, code, message)
}

func (s Service) markInvocationRetry(ctx context.Context, req requestctx.Context, invocation Invocation, now time.Time, outcome routing.CapabilityCallOutcome, nextAttempt time.Time, code, message string) (dispatchOutcome, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return dispatchOutcome{}, err
	}
	defer tx.Rollback()

	refs := refsFromOutcome(outcome)
	updated, err := scanInvocation(tx.QueryRowContext(ctx, invocationOutcomeUpdateSQL(), invocation.InvocationID,
		InvocationStatusPending,
		nullableTime(&nextAttempt),
		refs.routeID,
		refs.capabilityCallID,
		refs.jobID,
		refs.policyDecisionID,
		refs.approvalID,
		refs.grantID,
		objectOrDefault(outcome.Result),
		objectOrDefault(outcome.ResultRefs),
		nullableString(code),
		nullableString(message),
		nil,
		nil,
		nil,
	))
	if err != nil {
		return dispatchOutcome{}, err
	}
	if err := appendLifecycleEventWithRefsTx(ctx, tx, req, events.TypeInvocationFailed, "invocation", updated.InvocationID, updated.Status, refs.routeID, refs.jobID, map[string]any{
		"failure_code":    code,
		"failure_message": message,
		"retry_at":        nextAttempt.Format(time.RFC3339Nano),
		"attempt_count":   updated.AttemptCount,
		"max_attempts":    updated.MaxAttempts,
	}); err != nil {
		return dispatchOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return dispatchOutcome{}, err
	}
	return dispatchOutcome{
		Invocation:         updated,
		Status:             updated.Status,
		Retrying:           true,
		LastCapabilityCall: refs.capabilityCallID,
		LastRouteID:        refs.routeID,
		LastJobID:          refs.jobID,
	}, nil
}

func (s Service) markInvocationRoutingError(ctx context.Context, req requestctx.Context, invocation Invocation, now time.Time, code, message string) (dispatchOutcome, error) {
	return s.markInvocationFailedOrRetry(ctx, req, invocation, now, routing.CapabilityCallOutcome{}, code, message)
}

func (s Service) markInvocationOutcome(ctx context.Context, req requestctx.Context, invocation Invocation, now time.Time, outcome routing.CapabilityCallOutcome, invocationStatus, fireStatus, invocationEvent, fireEvent, code, message string) (dispatchOutcome, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return dispatchOutcome{}, err
	}
	defer tx.Rollback()

	refs := refsFromOutcome(outcome)
	var completedAt any
	var failedAt any
	if invocationStatus == InvocationStatusSucceeded {
		completedAt = now
	}
	if invocationStatus == InvocationStatusFailed || invocationStatus == InvocationStatusRequiresManualAction || invocationStatus == InvocationStatusTimedOut {
		failedAt = now
	}
	updated, err := scanInvocation(tx.QueryRowContext(ctx, invocationOutcomeUpdateSQL(), invocation.InvocationID,
		invocationStatus,
		nil,
		refs.routeID,
		refs.capabilityCallID,
		refs.jobID,
		refs.policyDecisionID,
		refs.approvalID,
		refs.grantID,
		objectOrDefault(outcome.Result),
		objectOrDefault(outcome.ResultRefs),
		nullableString(code),
		nullableString(message),
		completedAt,
		nullableTimeFromAny(failedAt),
		now,
	))
	if err != nil {
		return dispatchOutcome{}, err
	}

	fire, err := updateScheduleFireOutcomeTx(ctx, tx, invocation.SourceOccurrenceRef, fireStatus, refs, code, message, now)
	if invocation.SourceKind == SourceKindSchedule && err != nil && !errors.Is(err, sql.ErrNoRows) {
		return dispatchOutcome{}, err
	}
	var directEvent DirectEvent
	if invocation.SourceKind == SourceKindDirectEvent {
		directEventStatus, directEventEvent := directEventOutcomeStatus(invocationStatus)
		directEvent, err = updateDirectEventOutcomeTx(ctx, tx, invocation.SourceOccurrenceRef, directEventStatus, refs, outcome, code, message, now)
		if err != nil {
			return dispatchOutcome{}, err
		}
		if err := appendLifecycleEventWithRefsTx(ctx, tx, req, directEventEvent, "direct_event", directEvent.DirectEventID, directEvent.Status, refs.routeID, refs.jobID, map[string]any{
			"invocation_id":      updated.InvocationID,
			"capability_call_id": refs.capabilityCallID,
			"failure_code":       code,
			"failure_message":    message,
		}); err != nil {
			return dispatchOutcome{}, err
		}
	}
	if err := appendLifecycleEventWithRefsTx(ctx, tx, req, invocationEvent, "invocation", updated.InvocationID, updated.Status, refs.routeID, refs.jobID, map[string]any{
		"capability_call_id": refs.capabilityCallID,
		"failure_code":       code,
		"failure_message":    message,
		"attempt_count":      updated.AttemptCount,
		"max_attempts":       updated.MaxAttempts,
	}); err != nil {
		return dispatchOutcome{}, err
	}
	if fire.ScheduleFireID != "" {
		if err := appendLifecycleEventWithRefsTx(ctx, tx, req, fireEvent, "schedule_fire", fire.ScheduleFireID, fire.Status, refs.routeID, refs.jobID, map[string]any{
			"invocation_id":      updated.InvocationID,
			"capability_call_id": refs.capabilityCallID,
			"failure_code":       code,
			"failure_message":    message,
		}); err != nil {
			return dispatchOutcome{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return dispatchOutcome{}, err
	}

	return dispatchOutcome{
		Invocation:         updated,
		ScheduleFire:       fire,
		Status:             updated.Status,
		Succeeded:          updated.Status == InvocationStatusSucceeded,
		Failed:             updated.Status == InvocationStatusFailed || updated.Status == InvocationStatusTimedOut,
		WaitingApproval:    updated.Status == InvocationStatusWaitingApproval,
		ManualAction:       updated.Status == InvocationStatusRequiresManualAction || fire.Status == FireStatusRequiresManualAction || directEvent.Status == DirectEventStatusFailed,
		LastCapabilityCall: refs.capabilityCallID,
		LastRouteID:        refs.routeID,
		LastJobID:          refs.jobID,
	}, nil
}

type dispatchRefs struct {
	routeID          string
	capabilityCallID string
	jobID            string
	policyDecisionID string
	approvalID       string
	grantID          string
}

func refsFromOutcome(outcome routing.CapabilityCallOutcome) dispatchRefs {
	refs := dispatchRefs{
		routeID:          strings.TrimSpace(outcome.Route.RouteID),
		capabilityCallID: strings.TrimSpace(outcome.CapabilityCall.CapabilityCallID),
		jobID:            strings.TrimSpace(outcome.JobID),
		policyDecisionID: strings.TrimSpace(outcome.PolicyDecisionID),
		approvalID:       strings.TrimSpace(outcome.ApprovalID),
		grantID:          strings.TrimSpace(outcome.GrantID),
	}
	if refs.jobID == "" && outcome.CapabilityCall.JobID != nil {
		refs.jobID = strings.TrimSpace(*outcome.CapabilityCall.JobID)
	}
	if refs.policyDecisionID == "" && outcome.CapabilityCall.PolicyDecisionID != nil {
		refs.policyDecisionID = strings.TrimSpace(*outcome.CapabilityCall.PolicyDecisionID)
	}
	if refs.approvalID == "" && outcome.CapabilityCall.ApprovalID != nil {
		refs.approvalID = strings.TrimSpace(*outcome.CapabilityCall.ApprovalID)
	}
	if refs.grantID == "" && outcome.CapabilityCall.GrantID != nil {
		refs.grantID = strings.TrimSpace(*outcome.CapabilityCall.GrantID)
	}
	return refs
}

func invocationOutcomeUpdateSQL() string {
	return `
		UPDATE automation.invocations
		SET status = $2,
		    next_attempt_at = $3,
		    leased_by_worker_run_id = NULL,
		    leased_at = NULL,
		    lease_expires_at = NULL,
		    route_id = nullif($4, ''),
		    capability_call_id = nullif($5, ''),
		    job_id = nullif($6, ''),
		    policy_decision_id = nullif($7, ''),
		    approval_id = nullif($8, ''),
		    grant_id = nullif($9, ''),
		    result_json = $10::jsonb,
		    result_refs_json = $11::jsonb,
		    failure_code = $12,
		    failure_message = $13,
		    completed_at = $14,
		    failed_at = $15,
		    updated_at = COALESCE($16, now())
		WHERE invocation_id = $1
		RETURNING invocation_id, automation_id, source_kind, source_ref,
		          source_occurrence_ref, actor_id, origin_node_id, scope_id,
		          project_id, target_capability, input_json, input_hash,
		          idempotency_key, status, attempt_count, max_attempts,
		          next_attempt_at, leased_by_worker_run_id, leased_at,
		          lease_expires_at, route_id, capability_call_id, job_id,
		          policy_decision_id, approval_id, grant_id, result_json,
		          result_refs_json, failure_code, failure_message, created_at,
		          updated_at, started_at, completed_at, failed_at, metadata_json
	`
}

func updateScheduleFireOutcomeTx(ctx context.Context, tx *sql.Tx, fireID, status string, refs dispatchRefs, code, message string, now time.Time) (ScheduleFire, error) {
	var completedAt any
	var failedAt any
	switch status {
	case FireStatusCompleted:
		completedAt = now
	case FireStatusFailed, FireStatusRequiresManualAction:
		failedAt = now
	}
	return scanScheduleFire(tx.QueryRowContext(ctx, `
		UPDATE automation.schedule_fires
		SET status = $2,
		    route_id = nullif($3, ''),
		    capability_call_id = nullif($4, ''),
		    job_id = nullif($5, ''),
		    failure_code = $6,
		    failure_message = $7,
		    completed_at = $8,
		    failed_at = $9,
		    updated_at = now()
		WHERE schedule_fire_id = $1
		RETURNING schedule_fire_id, schedule_id, automation_id, scheduled_for,
		          status, misfire_status, lateness_seconds, worker_run_id,
		          invocation_id, route_id, capability_call_id, job_id,
		          failure_code, failure_message, created_at, updated_at,
		          completed_at, failed_at, metadata_json
	`, fireID, status, refs.routeID, refs.capabilityCallID, refs.jobID, nullableString(code), nullableString(message), completedAt, failedAt))
}

func updateDirectEventOutcomeTx(ctx context.Context, tx *sql.Tx, directEventID, status string, refs dispatchRefs, outcome routing.CapabilityCallOutcome, code, message string, now time.Time) (DirectEvent, error) {
	var completedAt any
	if status == DirectEventStatusCompleted || status == DirectEventStatusFailed || status == DirectEventStatusTimedOut {
		completedAt = now
	}
	return scanDirectEvent(tx.QueryRowContext(ctx, `
		UPDATE automation.direct_events
		SET status = $2,
		    route_id = nullif($3, ''),
		    capability_call_id = nullif($4, ''),
		    job_id = nullif($5, ''),
		    result_json = $6::jsonb,
		    result_refs_json = $7::jsonb,
		    response_json = $6::jsonb,
		    failure_code = $8,
		    failure_message = $9,
		    completed_at = $10,
		    updated_at = now()
		WHERE direct_event_id = $1
		RETURNING direct_event_id, endpoint_id, integration_id, automation_id, status,
		          external_event_id, idempotency_key, request_method, request_path,
		          payload_hash, attempt_count, ingest_worker_run_id,
		          request_headers_json, query_json, raw_body_json, mapped_input_json,
		          response_json, invocation_id, route_id, capability_call_id, job_id,
		          result_json, result_refs_json, failure_code, failure_message,
		          received_at, updated_at, completed_at, metadata_json
	`, directEventID, status, refs.routeID, refs.capabilityCallID, refs.jobID, objectOrDefault(outcome.Result), objectOrDefault(outcome.ResultRefs), nullableString(code), nullableString(message), completedAt))
}

func directEventOutcomeStatus(invocationStatus string) (status string, eventType string) {
	switch invocationStatus {
	case InvocationStatusSucceeded:
		return DirectEventStatusCompleted, events.TypeDirectEventCompleted
	case InvocationStatusTimedOut:
		return DirectEventStatusTimedOut, events.TypeDirectEventTimedOut
	default:
		return DirectEventStatusFailed, events.TypeDirectEventFailed
	}
}

func dispatcherIdempotencyKey(invocation Invocation) string {
	return fmt.Sprintf("automation-dispatch:%s:attempt:%d", invocation.InvocationID, invocation.AttemptCount)
}

func dispatcherRetryDelay(attemptCount int) time.Duration {
	if attemptCount <= 0 {
		return defaultDispatcherRetryDelay
	}
	delay := time.Duration(attemptCount) * defaultDispatcherRetryDelay
	if delay > maxDispatcherRetryDelay {
		return maxDispatcherRetryDelay
	}
	return delay
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	normalized, err := normalizeJSONObject(raw, "json")
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return normalized
}

func nullableTimeFromAny(value any) any {
	if value == nil {
		return nil
	}
	if t, ok := value.(time.Time); ok {
		return t.UTC()
	}
	return value
}

func appendLifecycleEventWithRefsTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, eventType, targetKind, targetID, status, routeID, jobID string, payload map[string]any) error {
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
		RouteID:         routeID,
		JobID:           jobID,
		Status:          status,
		Payload:         payload,
		VisibilityClass: "internal",
	})
	return err
}
