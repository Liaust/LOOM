package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	defaultDirectEventIngestBatchSize   = 25
	maxDirectEventIngestBatchSize       = 200
	defaultDirectEventPayloadLimitBytes = 1 << 20
	maxDirectEventPayloadLimitBytes     = 10 << 20
	defaultDirectEventSyncWaitSeconds   = 10
	maxDirectEventSyncWaitSeconds       = 300
)

var ErrDirectEventUnauthorized = errors.New("direct event authentication failed")
var ErrDirectEventIdempotencyConflict = errors.New("direct event idempotency conflict")
var ErrDirectEventMappingFailed = errors.New("direct event mapping failed")
var ErrDirectEventSyncTimedOut = errors.New("direct event sync wait timed out")

func (s Service) AcceptDirectEvent(ctx context.Context, req requestctx.Context, input DirectEventHTTPRequestInput) (DirectEventIngestResult, error) {
	if s.DB == nil {
		return DirectEventIngestResult{}, fmt.Errorf("database is required")
	}
	endpoint, err := s.getDirectEventEndpoint(ctx, input.EndpointSlug)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	integration, err := s.getIntegration(ctx, endpoint.IntegrationID)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	automation, err := s.getAutomation(ctx, endpoint.AutomationID)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	if endpoint.Status != DirectEventEndpointStatusActive || automation.Status != AutomationStatusActive || integration.Status != IntegrationStatusActive {
		return DirectEventIngestResult{}, fmt.Errorf("direct event endpoint is not active")
	}
	if err := ensureAutomationRuntimeActive(ctx, s.DB, automation, "direct_event_endpoint", endpoint.EndpointSlug); err != nil {
		return DirectEventIngestResult{}, err
	}
	if method := strings.ToUpper(strings.TrimSpace(input.Method)); method != "POST" {
		return DirectEventIngestResult{}, fmt.Errorf("direct event method must be POST")
	}
	limit := directEventPayloadLimit(automation.StorageProfileJSON)
	if len(input.Body) > limit {
		return DirectEventIngestResult{}, fmt.Errorf("direct event payload exceeds %d bytes", limit)
	}
	rawBody, err := normalizeJSONObject(json.RawMessage(input.Body), "direct_event_body")
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	payloadHash, err := objectHash(rawBody)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	headersStored := directEventHeadersJSON(input.Headers)
	queryStored := directEventQueryJSON(input.Query)

	auth, err := s.AuthenticateDirectEvent(ctx, endpoint, input)
	if err != nil {
		event, recordErr := s.insertRejectedDirectEvent(ctx, req, endpoint, integration, automation, input, headersStored, queryStored, rawBody, payloadHash, "auth.failed", err.Error())
		if recordErr != nil {
			return DirectEventIngestResult{}, recordErr
		}
		return DirectEventIngestResult{
			DirectEvent:  event,
			Endpoint:     endpoint,
			Integration:  integration,
			Automation:   automation,
			Status:       event.Status,
			ResponseMode: endpoint.ResponseMode,
		}, fmt.Errorf("%w: %v", ErrDirectEventUnauthorized, err)
	}
	idempotencyKey, err := s.ComputeDirectEventIdempotencyKey(ctx, endpoint, input, rawBody, payloadHash)
	if err != nil {
		return DirectEventIngestResult{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	defer tx.Rollback()

	var existing DirectEvent
	existing, err = scanDirectEvent(tx.QueryRowContext(ctx, directEventSelectSQL()+`
		WHERE endpoint_id = $1
		  AND idempotency_key = $2
	`, endpoint.EndpointID, idempotencyKey))
	if err == nil {
		if existing.PayloadHash != payloadHash {
			return DirectEventIngestResult{}, fmt.Errorf("%w: direct event idempotency key already exists with a different payload", ErrDirectEventIdempotencyConflict)
		}
		if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventDuplicateDetected, "direct_event", existing.DirectEventID, DirectEventStatusDuplicate, map[string]any{
			"endpoint_id":     endpoint.EndpointID,
			"endpoint_slug":   endpoint.EndpointSlug,
			"direct_event_id": existing.DirectEventID,
		}); err != nil {
			return DirectEventIngestResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return DirectEventIngestResult{}, err
		}
		return DirectEventIngestResult{
			DirectEvent:  existing,
			Endpoint:     endpoint,
			Integration:  integration,
			Automation:   automation,
			Status:       DirectEventStatusDuplicate,
			Duplicate:    true,
			ResponseMode: endpoint.ResponseMode,
		}, nil
	}
	if err != sql.ErrNoRows {
		return DirectEventIngestResult{}, err
	}

	event, err := insertAcceptedDirectEventTx(ctx, tx, endpoint, integration, automation, input, headersStored, queryStored, rawBody, payloadHash, idempotencyKey)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventReceived, "direct_event", event.DirectEventID, event.Status, map[string]any{
		"endpoint_id":    endpoint.EndpointID,
		"endpoint_slug":  endpoint.EndpointSlug,
		"integration_id": integration.IntegrationID,
		"payload_hash":   payloadHash,
	}); err != nil {
		return DirectEventIngestResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventAuthenticated, "direct_event", event.DirectEventID, event.Status, map[string]any{
		"auth_kind":       auth.AuthKind,
		"auth_profile_id": auth.AuthProfileID,
	}); err != nil {
		return DirectEventIngestResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return DirectEventIngestResult{}, err
	}

	return DirectEventIngestResult{
		DirectEvent:  event,
		Endpoint:     endpoint,
		Integration:  integration,
		Automation:   automation,
		Status:       event.Status,
		ResponseMode: endpoint.ResponseMode,
	}, nil
}

func (s Service) ProcessDirectEventSyncWait(ctx context.Context, req requestctx.Context, directEventRef string, duplicate bool) (DirectEventIngestResult, error) {
	detail, err := s.GetDirectEvent(ctx, directEventRef)
	if err != nil {
		return DirectEventIngestResult{}, err
	}
	result := directEventIngestResultFromDetail(detail, duplicate, detail.Endpoint.ResponseMode)
	if detail.Endpoint.ResponseMode != DirectEventResponseSyncWait {
		return result, nil
	}

	timeout := directEventSyncWaitTimeout(detail.Automation.CommunicationProfileJSON)
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if detail.DirectEvent.Status == DirectEventStatusAccepted {
		if _, err := s.RunDirectEventIngest(waitCtx, req, DirectEventIngestRunInput{
			Now:            time.Now().UTC(),
			BatchSize:      1,
			DirectEventRef: detail.DirectEvent.DirectEventID,
		}); err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				return result, ErrDirectEventSyncTimedOut
			}
			return result, err
		}
		detail, err = s.GetDirectEvent(ctx, detail.DirectEvent.DirectEventID)
		if err != nil {
			return result, err
		}
		result = directEventIngestResultFromDetail(detail, duplicate, detail.Endpoint.ResponseMode)
	}

	switch detail.DirectEvent.Status {
	case DirectEventStatusMappingFailed:
		return result, ErrDirectEventMappingFailed
	case DirectEventStatusRejected:
		return result, ErrDirectEventUnauthorized
	case DirectEventStatusCompleted, DirectEventStatusFailed, DirectEventStatusTimedOut:
		return syncWaitTerminalResult(result)
	}

	if detail.DirectEvent.InvocationID != nil {
		if _, err := s.DispatchInvocationNow(waitCtx, req, *detail.DirectEvent.InvocationID, DispatcherRunInput{
			Now:       time.Now().UTC(),
			BatchSize: 1,
		}); err != nil {
			if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
				refreshed, refreshErr := s.GetDirectEvent(ctx, detail.DirectEvent.DirectEventID)
				if refreshErr == nil {
					result = directEventIngestResultFromDetail(refreshed, duplicate, refreshed.Endpoint.ResponseMode)
				}
				return result, ErrDirectEventSyncTimedOut
			}
			return result, err
		}
	}

	detail, err = s.GetDirectEvent(ctx, detail.DirectEvent.DirectEventID)
	if err != nil {
		return result, err
	}
	result = directEventIngestResultFromDetail(detail, duplicate, detail.Endpoint.ResponseMode)
	if errors.Is(waitCtx.Err(), context.DeadlineExceeded) && detail.DirectEvent.Status != DirectEventStatusCompleted {
		return result, ErrDirectEventSyncTimedOut
	}
	return syncWaitTerminalResult(result)
}

func (s Service) AuthenticateDirectEvent(ctx context.Context, endpoint DirectEventEndpoint, input DirectEventHTTPRequestInput) (IntegrationAuthResult, error) {
	refs := []string{}
	if len(endpoint.AuthProfileRefsJSON) > 0 {
		_ = json.Unmarshal(endpoint.AuthProfileRefsJSON, &refs)
	}
	refSet := map[string]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref != "" {
			refSet[ref] = struct{}{}
		}
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT auth_profile_id, auth_kind, token_hash
		FROM automation.integration_auth_profiles
		WHERE integration_id = $1
		  AND status = 'active'
		ORDER BY created_at ASC
	`, endpoint.IntegrationID)
	if err != nil {
		return IntegrationAuthResult{}, err
	}
	defer rows.Close()

	bearerToken := bearerTokenFromHeaders(input.Headers)
	queryToken := queryValue(input.Query, "loom_token")
	if queryToken == "" {
		queryToken = queryValue(input.Query, "token")
	}
	privateRemote := isPrivateRemote(input.RemoteAddr)
	checked := 0
	for rows.Next() {
		var authProfileID, authKind string
		var tokenHash sql.NullString
		if err := rows.Scan(&authProfileID, &authKind, &tokenHash); err != nil {
			return IntegrationAuthResult{}, err
		}
		if len(refSet) > 0 {
			if _, ok := refSet[authProfileID]; !ok {
				continue
			}
		}
		checked++
		switch authKind {
		case IntegrationAuthBearerHeader:
			if tokenHash.Valid && bearerToken != "" && verifyIntegrationToken(bearerToken, tokenHash.String) {
				return IntegrationAuthResult{Authenticated: true, AuthKind: authKind, AuthProfileID: authProfileID}, nil
			}
		case IntegrationAuthQueryToken:
			if tokenHash.Valid && queryToken != "" && verifyIntegrationToken(queryToken, tokenHash.String) {
				return IntegrationAuthResult{Authenticated: true, AuthKind: authKind, AuthProfileID: authProfileID}, nil
			}
		case IntegrationAuthPrivateNetwork:
			if privateRemote {
				return IntegrationAuthResult{Authenticated: true, AuthKind: authKind, AuthProfileID: authProfileID}, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return IntegrationAuthResult{}, err
	}
	if checked == 0 {
		return IntegrationAuthResult{FailureCode: "auth_profile.none"}, fmt.Errorf("no active auth profile is available for endpoint")
	}
	return IntegrationAuthResult{FailureCode: "auth.invalid"}, fmt.Errorf("direct event auth failed")
}

func (s Service) ComputeDirectEventIdempotencyKey(ctx context.Context, endpoint DirectEventEndpoint, input DirectEventHTTPRequestInput, rawBody json.RawMessage, payloadHash string) (string, error) {
	automation, err := s.getAutomation(ctx, endpoint.AutomationID)
	if err != nil {
		return "", err
	}
	profile := IdempotencyProfile{Strategy: DirectEventIdempotencyNone}
	if err := json.Unmarshal(automation.IdempotencyProfileJSON, &profile); err != nil {
		return "", fmt.Errorf("idempotency profile is invalid: %w", err)
	}
	switch strings.TrimSpace(profile.Strategy) {
	case "", DirectEventIdempotencyNone:
		return "direct-event:" + endpoint.EndpointID + ":" + payloadHash, nil
	case DirectEventIdempotencyHeader:
		value := headerValue(input.Headers, profile.Header)
		if value == "" {
			return "", fmt.Errorf("idempotency header %q is missing", profile.Header)
		}
		return "direct-event:" + endpoint.EndpointID + ":header:" + strings.TrimSpace(profile.Header) + ":" + value, nil
	case DirectEventIdempotencyPayloadPath:
		var object map[string]any
		if err := json.Unmarshal(rawBody, &object); err != nil {
			return "", err
		}
		value, ok, err := getObjectPath(object, strings.TrimPrefix(profile.Path, "$."))
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("idempotency payload path %q is missing", profile.Path)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return "direct-event:" + endpoint.EndpointID + ":payload:" + profile.Path + ":" + string(encoded), nil
	default:
		return "", fmt.Errorf("unsupported idempotency strategy: %s", profile.Strategy)
	}
}

func (s Service) RunDirectEventIngest(ctx context.Context, req requestctx.Context, input DirectEventIngestRunInput) (DirectEventIngestRunResult, error) {
	if s.DB == nil {
		return DirectEventIngestRunResult{}, fmt.Errorf("database is required")
	}
	input = normalizeDirectEventIngestRunInput(input)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEventIngestRunResult{}, err
	}
	defer tx.Rollback()

	query := directEventSelectSQL() + `
		WHERE status = $1
	`
	args := []any{DirectEventStatusAccepted}
	if input.DirectEventRef != "" {
		args = append(args, input.DirectEventRef)
		query += fmt.Sprintf(" AND direct_event_id = $%d", len(args))
	}
	args = append(args, input.BatchSize)
	query += fmt.Sprintf(`
		ORDER BY received_at ASC
		LIMIT $%d
		FOR UPDATE SKIP LOCKED
	`, len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return DirectEventIngestRunResult{}, err
	}
	defer rows.Close()

	eventsToProcess := []DirectEvent{}
	for rows.Next() {
		event, err := scanDirectEvent(rows)
		if err != nil {
			return DirectEventIngestRunResult{}, err
		}
		eventsToProcess = append(eventsToProcess, event)
	}
	if err := rows.Err(); err != nil {
		return DirectEventIngestRunResult{}, err
	}

	result := DirectEventIngestRunResult{
		Claimed:  int64(len(eventsToProcess)),
		NoWork:   len(eventsToProcess) == 0,
		MoreWork: len(eventsToProcess) >= input.BatchSize,
	}
	for _, event := range eventsToProcess {
		result.DirectEventIDs = append(result.DirectEventIDs, event.DirectEventID)
		updated, err := s.processAcceptedDirectEventTx(ctx, tx, req, event, input)
		if err != nil {
			result.Failed++
			return result, err
		}
		switch updated.Status {
		case DirectEventStatusInvocationCreated:
			result.Mapped++
			result.CreatedInvocations++
			if updated.InvocationID != nil {
				result.InvocationIDs = append(result.InvocationIDs, *updated.InvocationID)
			}
		case DirectEventStatusMappingFailed:
			result.MappingFailed++
		}
	}
	if err := tx.Commit(); err != nil {
		return DirectEventIngestRunResult{}, err
	}
	return result, nil
}

func (s Service) processAcceptedDirectEventTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, event DirectEvent, input DirectEventIngestRunInput) (DirectEvent, error) {
	event, err := scanDirectEvent(tx.QueryRowContext(ctx, `
		UPDATE automation.direct_events
		SET attempt_count = attempt_count + 1,
		    ingest_worker_run_id = nullif($2, ''),
		    updated_at = now()
		WHERE direct_event_id = $1
		RETURNING direct_event_id, endpoint_id, integration_id, automation_id, status,
		          external_event_id, idempotency_key, request_method, request_path,
		          payload_hash, attempt_count, ingest_worker_run_id,
		          request_headers_json, query_json, raw_body_json, mapped_input_json,
		          response_json, invocation_id, route_id, capability_call_id, job_id,
		          result_json, result_refs_json, failure_code, failure_message,
		          received_at, updated_at, completed_at, metadata_json
	`, event.DirectEventID, input.WorkerRunID))
	if err != nil {
		return DirectEvent{}, err
	}
	endpoint, err := scanDirectEventEndpoint(tx.QueryRowContext(ctx, directEventEndpointSelectSQL()+`
		WHERE endpoint_id = $1
	`, event.EndpointID))
	if err != nil {
		return DirectEvent{}, err
	}
	automation, err := scanAutomation(tx.QueryRowContext(ctx, automationSelectSQL()+`
		WHERE automation_id = $1
	`, event.AutomationID))
	if err != nil {
		return DirectEvent{}, err
	}
	if err := ensureAutomationRuntimeActive(ctx, tx, automation, "direct_event", event.DirectEventID); err != nil {
		updated, updateErr := updateDirectEventMappingFailedTx(ctx, tx, event.DirectEventID, "project_runtime.archived", err.Error())
		if updateErr != nil {
			return DirectEvent{}, updateErr
		}
		if eventErr := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventMappingFailed, "direct_event", updated.DirectEventID, updated.Status, map[string]any{
			"failure_code":    "project_runtime.archived",
			"failure_message": err.Error(),
		}); eventErr != nil {
			return DirectEvent{}, eventErr
		}
		return updated, nil
	}
	mappedInput, missing, _, err := ApplyMappingPreview(endpoint.MappingProfileJSON, MappingPreviewInput{
		BodyJSON:    event.RawBodyJSON,
		HeadersJSON: event.RequestHeadersJSON,
		QueryJSON:   event.QueryJSON,
	})
	if err != nil || len(missing) > 0 {
		code := "direct_event.mapping_failed"
		message := ""
		if err != nil {
			message = err.Error()
		}
		if len(missing) > 0 {
			message = "missing required fields: " + strings.Join(missing, ", ")
		}
		updated, updateErr := updateDirectEventMappingFailedTx(ctx, tx, event.DirectEventID, code, message)
		if updateErr != nil {
			return DirectEvent{}, updateErr
		}
		if eventErr := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventMappingFailed, "direct_event", updated.DirectEventID, updated.Status, map[string]any{
			"failure_code":    code,
			"failure_message": message,
		}); eventErr != nil {
			return DirectEvent{}, eventErr
		}
		return updated, nil
	}
	target, err := NormalizeTargetProfile(automation.TargetProfileJSON)
	if err != nil {
		return DirectEvent{}, err
	}
	retry, err := NormalizeRetryProfile(automation.RetryProfileJSON)
	if err != nil {
		return DirectEvent{}, err
	}
	originNodeID, err := resolveNodeID(ctx, tx, defaultSchedulerOriginNode)
	if err != nil {
		return DirectEvent{}, err
	}
	invocation, err := s.createInvocationTx(ctx, tx, req, CreateInvocationTxInput{
		AutomationID:        event.AutomationID,
		SourceKind:          SourceKindDirectEvent,
		SourceRef:           endpoint.EndpointID,
		SourceOccurrenceRef: event.DirectEventID,
		ActorID:             automation.RunAsActorID,
		OriginNodeID:        originNodeID,
		ScopeID:             automation.ScopeID,
		ProjectID:           automation.ProjectID,
		TargetCapability:    target.CapabilityRef,
		InputJSON:           mappedInput,
		IdempotencyKey:      "automation-invocation:direct_event:" + event.DirectEventID,
		MaxAttempts:         retry.MaxAttempts,
		Metadata: mustJSON(map[string]any{
			"schema_version":  "automation_invocation.metadata.v0.2",
			"direct_event_id": event.DirectEventID,
			"endpoint_id":     endpoint.EndpointID,
			"endpoint_slug":   endpoint.EndpointSlug,
		}),
		EventPayload: map[string]any{
			"direct_event_id": event.DirectEventID,
			"endpoint_id":     endpoint.EndpointID,
			"endpoint_slug":   endpoint.EndpointSlug,
		},
	})
	if err != nil {
		return DirectEvent{}, err
	}
	updated, err := scanDirectEvent(tx.QueryRowContext(ctx, `
		UPDATE automation.direct_events
		SET status = $2,
		    mapped_input_json = $3::jsonb,
		    invocation_id = $4,
		    updated_at = now()
		WHERE direct_event_id = $1
		RETURNING direct_event_id, endpoint_id, integration_id, automation_id, status,
		          external_event_id, idempotency_key, request_method, request_path,
		          payload_hash, attempt_count, ingest_worker_run_id,
		          request_headers_json, query_json, raw_body_json, mapped_input_json,
		          response_json, invocation_id, route_id, capability_call_id, job_id,
		          result_json, result_refs_json, failure_code, failure_message,
		          received_at, updated_at, completed_at, metadata_json
	`, event.DirectEventID, DirectEventStatusInvocationCreated, mappedInput, invocation.InvocationID))
	if err != nil {
		return DirectEvent{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventMapped, "direct_event", updated.DirectEventID, DirectEventStatusMapped, map[string]any{
		"endpoint_id": endpoint.EndpointID,
	}); err != nil {
		return DirectEvent{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventInvocationCreated, "direct_event", updated.DirectEventID, updated.Status, map[string]any{
		"endpoint_id":    endpoint.EndpointID,
		"invocation_id":  invocation.InvocationID,
		"automation_id":  invocation.AutomationID,
		"payload_hash":   event.PayloadHash,
		"mapped_hash":    invocation.InputHash,
		"worker_run_id":  input.WorkerRunID,
		"attempt_count":  updated.AttemptCount,
		"target_address": invocation.TargetCapability,
	}); err != nil {
		return DirectEvent{}, err
	}
	return updated, nil
}

func (s Service) GetDirectEvent(ctx context.Context, ref string) (DirectEventDetail, error) {
	if s.DB == nil {
		return DirectEventDetail{}, fmt.Errorf("database is required")
	}
	event, err := s.getDirectEvent(ctx, ref)
	if err != nil {
		return DirectEventDetail{}, err
	}
	endpoint, err := s.getDirectEventEndpoint(ctx, event.EndpointID)
	if err != nil {
		return DirectEventDetail{}, err
	}
	integration, err := s.getIntegration(ctx, event.IntegrationID)
	if err != nil {
		return DirectEventDetail{}, err
	}
	automation, err := s.getAutomation(ctx, event.AutomationID)
	if err != nil {
		return DirectEventDetail{}, err
	}
	var invocation *Invocation
	if event.InvocationID != nil {
		item, err := s.GetInvocation(ctx, *event.InvocationID)
		if err == nil {
			invocation = &item
		}
	}
	return DirectEventDetail{DirectEvent: event, Endpoint: endpoint, Integration: integration, Automation: automation, Invocation: invocation}, nil
}

func directEventIngestResultFromDetail(detail DirectEventDetail, duplicate bool, responseMode string) DirectEventIngestResult {
	return DirectEventIngestResult{
		DirectEvent:  detail.DirectEvent,
		Endpoint:     detail.Endpoint,
		Integration:  detail.Integration,
		Automation:   detail.Automation,
		Invocation:   detail.Invocation,
		Status:       detail.DirectEvent.Status,
		Duplicate:    duplicate,
		ResponseMode: responseMode,
	}
}

func syncWaitTerminalResult(result DirectEventIngestResult) (DirectEventIngestResult, error) {
	switch result.DirectEvent.Status {
	case DirectEventStatusMappingFailed:
		return result, ErrDirectEventMappingFailed
	case DirectEventStatusTimedOut:
		return result, ErrDirectEventSyncTimedOut
	default:
		return result, nil
	}
}

func (s Service) ListDirectEvents(ctx context.Context, filter DirectEventFilter) ([]DirectEvent, error) {
	return s.listDirectEvents(ctx, filter, false)
}

func (s Service) ListDirectEventFailures(ctx context.Context, filter DirectEventFilter) ([]DirectEvent, error) {
	return s.listDirectEvents(ctx, filter, true)
}

func (s Service) GetDirectEventRawPayload(ctx context.Context, req requestctx.Context, ref string) (DirectEventRawPayload, error) {
	event, err := s.getDirectEvent(ctx, ref)
	if err != nil {
		return DirectEventRawPayload{}, err
	}
	return DirectEventRawPayload{
		DirectEventID: event.DirectEventID,
		HeadersJSON:   event.RequestHeadersJSON,
		QueryJSON:     event.QueryJSON,
		BodyJSON:      event.RawBodyJSON,
		PayloadHash:   event.PayloadHash,
		ReceivedAt:    event.ReceivedAt,
	}, nil
}

func (s Service) listDirectEvents(ctx context.Context, filter DirectEventFilter, failuresOnly bool) ([]DirectEvent, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := directEventSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if failuresOnly {
		query += ` AND status IN ('rejected', 'mapping_failed', 'failed', 'timed_out')`
	} else if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.EndpointRef); ref != "" {
		endpoint, err := s.getDirectEventEndpoint(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("endpoint_id =", endpoint.EndpointID)
	}
	if ref := strings.TrimSpace(filter.IntegrationRef); ref != "" {
		integration, err := s.getIntegration(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("integration_id =", integration.IntegrationID)
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
		args = append(args, projectID)
		query += fmt.Sprintf(" AND automation_id IN (SELECT automation_id FROM automation.automations WHERE project_id = $%d)", len(args))
	}
	if !hasExplicitRuntimeRef(filter.EndpointRef, filter.IntegrationRef, filter.AutomationRef, filter.ProjectRef) {
		query += archivedProjectByAutomationColumnSQL("automation_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY received_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirectEvent{}
	for rows.Next() {
		item, err := scanDirectEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func insertAcceptedDirectEventTx(ctx context.Context, tx *sql.Tx, endpoint DirectEventEndpoint, integration Integration, automation Automation, input DirectEventHTTPRequestInput, headersJSON, queryJSON, rawBody json.RawMessage, payloadHash, idempotencyKey string) (DirectEvent, error) {
	return insertDirectEventTx(ctx, tx, endpoint, integration, automation, input, headersJSON, queryJSON, rawBody, payloadHash, idempotencyKey, DirectEventStatusAccepted, "", "")
}

func (s Service) insertRejectedDirectEvent(ctx context.Context, req requestctx.Context, endpoint DirectEventEndpoint, integration Integration, automation Automation, input DirectEventHTTPRequestInput, headersJSON, queryJSON, rawBody json.RawMessage, payloadHash, code, message string) (DirectEvent, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEvent{}, err
	}
	defer tx.Rollback()
	event, err := insertDirectEventTx(ctx, tx, endpoint, integration, automation, input, headersJSON, queryJSON, rawBody, payloadHash, "", DirectEventStatusRejected, code, message)
	if err != nil {
		return DirectEvent{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventReceived, "direct_event", event.DirectEventID, event.Status, map[string]any{
		"endpoint_id":   endpoint.EndpointID,
		"endpoint_slug": endpoint.EndpointSlug,
		"payload_hash":  payloadHash,
	}); err != nil {
		return DirectEvent{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventRejected, "direct_event", event.DirectEventID, event.Status, map[string]any{
		"failure_code":    code,
		"failure_message": message,
	}); err != nil {
		return DirectEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return DirectEvent{}, err
	}
	return event, nil
}

func insertDirectEventTx(ctx context.Context, tx *sql.Tx, endpoint DirectEventEndpoint, integration Integration, automation Automation, input DirectEventHTTPRequestInput, headersJSON, queryJSON, rawBody json.RawMessage, payloadHash, idempotencyKey, status, code, message string) (DirectEvent, error) {
	return scanDirectEvent(tx.QueryRowContext(ctx, `
		INSERT INTO automation.direct_events (
			direct_event_id, endpoint_id, integration_id, automation_id, status,
			external_event_id, idempotency_key, request_method, request_path,
			payload_hash, request_headers_json, query_json, raw_body_json,
			failure_code, failure_message, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, '', $6, $7, $8, $9, $10::jsonb, $11::jsonb, $12::jsonb, nullif($13, ''), nullif($14, ''), $15::jsonb)
		RETURNING direct_event_id, endpoint_id, integration_id, automation_id, status,
		          external_event_id, idempotency_key, request_method, request_path,
		          payload_hash, attempt_count, ingest_worker_run_id,
		          request_headers_json, query_json, raw_body_json, mapped_input_json,
		          response_json, invocation_id, route_id, capability_call_id, job_id,
		          result_json, result_refs_json, failure_code, failure_message,
		          received_at, updated_at, completed_at, metadata_json
	`, ids.NewDirectEventID(),
		endpoint.EndpointID,
		integration.IntegrationID,
		automation.AutomationID,
		status,
		idempotencyKey,
		strings.ToUpper(strings.TrimSpace(input.Method)),
		strings.TrimSpace(input.Path),
		payloadHash,
		headersJSON,
		queryJSON,
		rawBody,
		code,
		message,
		mustJSON(map[string]any{
			"schema_version": "direct_event.metadata.v0.2",
			"endpoint_slug":  endpoint.EndpointSlug,
			"remote_addr":    input.RemoteAddr,
		}),
	))
}

func updateDirectEventMappingFailedTx(ctx context.Context, tx *sql.Tx, directEventID, code, message string) (DirectEvent, error) {
	return scanDirectEvent(tx.QueryRowContext(ctx, `
		UPDATE automation.direct_events
		SET status = $2,
		    failure_code = $3,
		    failure_message = $4,
		    updated_at = now()
		WHERE direct_event_id = $1
		RETURNING direct_event_id, endpoint_id, integration_id, automation_id, status,
		          external_event_id, idempotency_key, request_method, request_path,
		          payload_hash, attempt_count, ingest_worker_run_id,
		          request_headers_json, query_json, raw_body_json, mapped_input_json,
		          response_json, invocation_id, route_id, capability_call_id, job_id,
		          result_json, result_refs_json, failure_code, failure_message,
		          received_at, updated_at, completed_at, metadata_json
	`, directEventID, DirectEventStatusMappingFailed, code, message))
}

func normalizeDirectEventIngestRunInput(input DirectEventIngestRunInput) DirectEventIngestRunInput {
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	if input.BatchSize <= 0 || input.BatchSize > maxDirectEventIngestBatchSize {
		input.BatchSize = defaultDirectEventIngestBatchSize
	}
	input.WorkerRunID = strings.TrimSpace(input.WorkerRunID)
	input.DirectEventRef = strings.TrimSpace(input.DirectEventRef)
	if input.DirectEventRef != "" {
		input.BatchSize = 1
	}
	return input
}

func directEventSyncWaitTimeout(raw json.RawMessage) time.Duration {
	profile := CommunicationProfile{ResponseMode: DirectEventResponseSyncWait, SyncWaitTimeoutSeconds: defaultDirectEventSyncWaitSeconds}
	if err := json.Unmarshal(raw, &profile); err != nil {
		return time.Duration(defaultDirectEventSyncWaitSeconds) * time.Second
	}
	seconds := profile.SyncWaitTimeoutSeconds
	if seconds <= 0 {
		seconds = defaultDirectEventSyncWaitSeconds
	}
	if seconds > maxDirectEventSyncWaitSeconds {
		seconds = maxDirectEventSyncWaitSeconds
	}
	return time.Duration(seconds) * time.Second
}

func directEventPayloadLimit(raw json.RawMessage) int {
	limit := defaultDirectEventPayloadLimitBytes
	var profile struct {
		PayloadLimitBytes int `json:"payload_limit_bytes"`
	}
	if err := json.Unmarshal(raw, &profile); err == nil && profile.PayloadLimitBytes > 0 {
		limit = profile.PayloadLimitBytes
	}
	if limit > maxDirectEventPayloadLimitBytes {
		return maxDirectEventPayloadLimitBytes
	}
	return limit
}

func directEventHeadersJSON(headers map[string][]string) json.RawMessage {
	out := map[string]any{}
	for key, values := range headers {
		cleanKey := strings.ToLower(strings.TrimSpace(key))
		if cleanKey == "" {
			continue
		}
		if isSensitiveHeader(cleanKey) {
			out[cleanKey] = "[redacted]"
			continue
		}
		out[cleanKey] = firstNonEmpty(values)
	}
	return mustJSON(out)
}

func directEventQueryJSON(query map[string][]string) json.RawMessage {
	out := map[string]any{}
	for key, values := range query {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			continue
		}
		if cleanKey == "token" || cleanKey == "loom_token" {
			out[cleanKey] = "[redacted]"
			continue
		}
		out[cleanKey] = firstNonEmpty(values)
	}
	return mustJSON(out)
}

func bearerTokenFromHeaders(headers map[string][]string) string {
	value := headerValue(headers, "authorization")
	if token, ok := strings.CutPrefix(value, "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	if token, ok := strings.CutPrefix(value, "bearer "); ok {
		return strings.TrimSpace(token)
	}
	return ""
}

func headerValue(headers map[string][]string, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	for key, values := range headers {
		if strings.ToLower(strings.TrimSpace(key)) == name {
			return firstNonEmpty(values)
		}
	}
	return ""
}

func queryValue(query map[string][]string, name string) string {
	for key, values := range query {
		if key == name {
			return firstNonEmpty(values)
		}
	}
	return ""
}

func firstNonEmpty(values []string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func isSensitiveHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "cookie", "set-cookie", "x-api-key", "x-loom-token":
		return true
	default:
		return false
	}
}

func isPrivateRemote(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" || host == "@" || strings.EqualFold(host, "unix") {
		return true
	}
	if parsedHost, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
