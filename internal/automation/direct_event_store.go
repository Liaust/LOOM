package automation

import (
	"database/sql"
	"encoding/json"
)

func integrationSelectSQL() string {
	return `
		SELECT integration_id, integration_key, display_name, description, status,
		       actor_id, main_auth_level, allowed_scopes_json,
		       allowed_projects_json, allowed_endpoint_refs_json, created_by_actor_id,
		       created_at, updated_at, revoked_at, revoked_reason, metadata_json
		FROM automation.integrations
	`
}

func integrationAuthProfileSelectSQL() string {
	return `
		SELECT auth_profile_id, integration_id, display_name, auth_kind, status,
		       token_last_four, allowed_endpoint_refs_json, created_by_actor_id,
		       created_at, updated_at, revoked_at, revoked_reason, metadata_json
		FROM automation.integration_auth_profiles
	`
}

func directEventEndpointSelectSQL() string {
	return `
		SELECT endpoint_id, endpoint_slug, display_name, description, status,
		       integration_id, automation_id, event_type, endpoint_path, response_mode,
		       mapping_profile_json, auth_profile_refs_json, created_by_actor_id,
		       created_at, updated_at, disabled_at, metadata_json
		FROM automation.direct_event_endpoints
	`
}

func directEventSelectSQL() string {
	return `
		SELECT direct_event_id, endpoint_id, integration_id, automation_id, status,
		       external_event_id, idempotency_key, request_method, request_path,
		       payload_hash, attempt_count, ingest_worker_run_id,
		       request_headers_json, query_json, raw_body_json, mapped_input_json,
		       response_json, invocation_id, route_id, capability_call_id, job_id,
		       result_json, result_refs_json, failure_code, failure_message,
		       received_at, updated_at, completed_at, metadata_json
		FROM automation.direct_events
	`
}

func scanIntegration(scanner rowScanner) (Integration, error) {
	var out Integration
	var mainAuthLevel sql.NullInt64
	var revokedAt sql.NullTime
	var revokedReason sql.NullString
	var allowedScopes, allowedProjects, allowedEndpoints, metadata []byte
	if err := scanner.Scan(
		&out.IntegrationID,
		&out.IntegrationKey,
		&out.DisplayName,
		&out.Description,
		&out.Status,
		&out.ActorID,
		&mainAuthLevel,
		&allowedScopes,
		&allowedProjects,
		&allowedEndpoints,
		&out.CreatedByActorID,
		&out.CreatedAt,
		&out.UpdatedAt,
		&revokedAt,
		&revokedReason,
		&metadata,
	); err != nil {
		return Integration{}, err
	}
	if mainAuthLevel.Valid {
		level := int(mainAuthLevel.Int64)
		out.MainAuthLevel = &level
	}
	out.AllowedScopesJSON = jsonArrayOrDefault(allowedScopes)
	out.AllowedProjectsJSON = jsonArrayOrDefault(allowedProjects)
	out.AllowedEndpointRefsJSON = jsonArrayOrDefault(allowedEndpoints)
	out.RevokedAt = timePtr(revokedAt)
	out.RevokedReason = stringPtr(revokedReason)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanIntegrationAuthProfile(scanner rowScanner) (IntegrationAuthProfile, error) {
	var out IntegrationAuthProfile
	var revokedAt sql.NullTime
	var revokedReason sql.NullString
	var allowedEndpoints, metadata []byte
	if err := scanner.Scan(
		&out.AuthProfileID,
		&out.IntegrationID,
		&out.DisplayName,
		&out.AuthKind,
		&out.Status,
		&out.TokenLastFour,
		&allowedEndpoints,
		&out.CreatedByActorID,
		&out.CreatedAt,
		&out.UpdatedAt,
		&revokedAt,
		&revokedReason,
		&metadata,
	); err != nil {
		return IntegrationAuthProfile{}, err
	}
	out.AllowedEndpointRefsJSON = jsonArrayOrDefault(allowedEndpoints)
	out.RevokedAt = timePtr(revokedAt)
	out.RevokedReason = stringPtr(revokedReason)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanDirectEventEndpoint(scanner rowScanner) (DirectEventEndpoint, error) {
	var out DirectEventEndpoint
	var disabledAt sql.NullTime
	var mapping, authRefs, metadata []byte
	if err := scanner.Scan(
		&out.EndpointID,
		&out.EndpointSlug,
		&out.DisplayName,
		&out.Description,
		&out.Status,
		&out.IntegrationID,
		&out.AutomationID,
		&out.EventType,
		&out.EndpointPath,
		&out.ResponseMode,
		&mapping,
		&authRefs,
		&out.CreatedByActorID,
		&out.CreatedAt,
		&out.UpdatedAt,
		&disabledAt,
		&metadata,
	); err != nil {
		return DirectEventEndpoint{}, err
	}
	out.MappingProfileJSON = jsonOrDefault(mapping)
	out.AuthProfileRefsJSON = jsonArrayOrDefault(authRefs)
	out.DisabledAt = timePtr(disabledAt)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func scanDirectEvent(scanner rowScanner) (DirectEvent, error) {
	var out DirectEvent
	var ingestWorkerRunID, invocationID, routeID, capabilityCallID, jobID, failureCode, failureMessage sql.NullString
	var completedAt sql.NullTime
	var headers, query, rawBody, mappedInput, response, result, resultRefs, metadata []byte
	if err := scanner.Scan(
		&out.DirectEventID,
		&out.EndpointID,
		&out.IntegrationID,
		&out.AutomationID,
		&out.Status,
		&out.ExternalEventID,
		&out.IdempotencyKey,
		&out.RequestMethod,
		&out.RequestPath,
		&out.PayloadHash,
		&out.AttemptCount,
		&ingestWorkerRunID,
		&headers,
		&query,
		&rawBody,
		&mappedInput,
		&response,
		&invocationID,
		&routeID,
		&capabilityCallID,
		&jobID,
		&result,
		&resultRefs,
		&failureCode,
		&failureMessage,
		&out.ReceivedAt,
		&out.UpdatedAt,
		&completedAt,
		&metadata,
	); err != nil {
		return DirectEvent{}, err
	}
	out.RequestHeadersJSON = jsonOrDefault(headers)
	out.QueryJSON = jsonOrDefault(query)
	out.RawBodyJSON = jsonOrDefault(rawBody)
	out.MappedInputJSON = jsonOrDefault(mappedInput)
	out.ResponseJSON = jsonOrDefault(response)
	out.IngestWorkerRunID = stringPtr(ingestWorkerRunID)
	out.InvocationID = stringPtr(invocationID)
	out.RouteID = stringPtr(routeID)
	out.CapabilityCallID = stringPtr(capabilityCallID)
	out.JobID = stringPtr(jobID)
	out.ResultJSON = jsonOrDefault(result)
	out.ResultRefsJSON = jsonOrDefault(resultRefs)
	out.FailureCode = stringPtr(failureCode)
	out.FailureMessage = stringPtr(failureMessage)
	out.CompletedAt = timePtr(completedAt)
	out.MetadataJSON = jsonOrDefault(metadata)
	return out, nil
}

func jsonArrayOrDefault(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}
