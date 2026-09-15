package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func (s Service) CreateIntegration(ctx context.Context, req requestctx.Context, input CreateIntegrationInput) (IntegrationDetail, error) {
	if s.DB == nil {
		return IntegrationDetail{}, fmt.Errorf("database is required")
	}
	normalized, err := normalizeCreateIntegration(input)
	if err != nil {
		return IntegrationDetail{}, err
	}
	createdBy, err := resolveActorID(ctx, s.DB, req.ActorID)
	if err != nil {
		return IntegrationDetail{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationDetail{}, err
	}
	defer tx.Rollback()

	actorID, err := ensureIntegrationActor(ctx, tx, normalized.IntegrationKey, normalized.DisplayName, normalized.Metadata)
	if err != nil {
		return IntegrationDetail{}, err
	}
	if normalized.MainAuthLevel > 0 {
		mainNodeID, err := resolveMainNodeID(ctx, tx)
		if err != nil {
			return IntegrationDetail{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO identity.actor_node_authorizations (
				authorization_id, actor_id, node_id, authorization_level, status, metadata
			)
			VALUES ($1, $2, $3, $4, 'active', '{"integration":true}'::jsonb)
			ON CONFLICT (actor_id, node_id) DO UPDATE
			SET authorization_level = EXCLUDED.authorization_level,
			    status = 'active',
			    metadata = identity.actor_node_authorizations.metadata || EXCLUDED.metadata
		`, ids.NewActorNodeAuthorizationID(), actorID, mainNodeID, normalized.MainAuthLevel); err != nil {
			return IntegrationDetail{}, fmt.Errorf("authorize integration on main node: %w", err)
		}
	}

	integration, err := scanIntegration(tx.QueryRowContext(ctx, `
		INSERT INTO automation.integrations (
			integration_id, integration_key, display_name, description, status,
			actor_id, main_auth_level, allowed_scopes_json, allowed_projects_json,
			allowed_endpoint_refs_json, created_by_actor_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, 'active', $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11::jsonb)
		RETURNING integration_id, integration_key, display_name, description, status,
		          actor_id, main_auth_level, allowed_scopes_json,
		          allowed_projects_json, allowed_endpoint_refs_json, created_by_actor_id,
		          created_at, updated_at, revoked_at, revoked_reason, metadata_json
	`, ids.NewIntegrationID(),
		normalized.IntegrationKey,
		normalized.DisplayName,
		normalized.Description,
		actorID,
		normalized.MainAuthLevel,
		normalized.AllowedScopes,
		normalized.AllowedProjects,
		normalized.AllowedEndpointRefs,
		createdBy,
		normalized.Metadata,
	))
	if err != nil {
		return IntegrationDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeIntegrationCreated, "integration", integration.IntegrationID, integration.Status, map[string]any{
		"integration_key": integration.IntegrationKey,
		"actor_id":        integration.ActorID,
		"main_auth_level": normalized.MainAuthLevel,
	}); err != nil {
		return IntegrationDetail{}, err
	}

	if err := tx.Commit(); err != nil {
		return IntegrationDetail{}, err
	}
	return IntegrationDetail{Integration: integration}, nil
}

func (s Service) EnsureIntegration(ctx context.Context, req requestctx.Context, input CreateIntegrationInput) (IntegrationDetail, bool, error) {
	if s.DB == nil {
		return IntegrationDetail{}, false, fmt.Errorf("database is required")
	}
	normalized, err := normalizeCreateIntegration(input)
	if err != nil {
		return IntegrationDetail{}, false, err
	}
	existing, err := s.getIntegration(ctx, normalized.IntegrationKey)
	if err == nil {
		if existing.Status != IntegrationStatusActive {
			return IntegrationDetail{}, false, fmt.Errorf("integration %s is not active", normalized.IntegrationKey)
		}
		detail, err := s.GetIntegration(ctx, existing.IntegrationID)
		return detail, false, err
	}
	if err != sql.ErrNoRows {
		return IntegrationDetail{}, false, err
	}
	detail, err := s.CreateIntegration(ctx, req, input)
	if err != nil {
		return IntegrationDetail{}, false, err
	}
	return detail, true, nil
}

func (s Service) ListIntegrations(ctx context.Context, filter IntegrationFilter) ([]Integration, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := integrationSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Integration{}
	for rows.Next() {
		item, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) GetIntegration(ctx context.Context, ref string) (IntegrationDetail, error) {
	if s.DB == nil {
		return IntegrationDetail{}, fmt.Errorf("database is required")
	}
	integration, err := s.getIntegration(ctx, ref)
	if err != nil {
		return IntegrationDetail{}, err
	}
	authProfiles, err := s.ListIntegrationAuthProfiles(ctx, IntegrationAuthProfileFilter{IntegrationRef: integration.IntegrationID, Limit: 100})
	if err != nil {
		return IntegrationDetail{}, err
	}
	endpoints, err := s.ListDirectEventEndpoints(ctx, DirectEventEndpointFilter{IntegrationRef: integration.IntegrationID, Limit: 100})
	if err != nil {
		return IntegrationDetail{}, err
	}
	return IntegrationDetail{Integration: integration, AuthProfiles: authProfiles, Endpoints: endpoints}, nil
}

func (s Service) DisableIntegration(ctx context.Context, req requestctx.Context, ref string, input UpdateIntegrationStatusInput) (IntegrationDetail, error) {
	return s.updateIntegrationStatus(ctx, req, ref, IntegrationStatusDisabled, events.TypeIntegrationDisabled, input)
}

func (s Service) RevokeIntegration(ctx context.Context, req requestctx.Context, ref string, input UpdateIntegrationStatusInput) (IntegrationDetail, error) {
	return s.updateIntegrationStatus(ctx, req, ref, IntegrationStatusRevoked, events.TypeIntegrationRevoked, input)
}

func (s Service) CreateIntegrationAuthProfile(ctx context.Context, req requestctx.Context, integrationRef string, input CreateIntegrationAuthProfileInput) (IntegrationAuthProfileCreateResult, error) {
	if s.DB == nil {
		return IntegrationAuthProfileCreateResult{}, fmt.Errorf("database is required")
	}
	integration, err := s.getIntegration(ctx, integrationRef)
	if err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	if integration.Status != IntegrationStatusActive {
		return IntegrationAuthProfileCreateResult{}, fmt.Errorf("integration must be active")
	}
	normalized, err := normalizeCreateIntegrationAuthProfile(input)
	if err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	createdBy, err := resolveActorID(ctx, s.DB, req.ActorID)
	if err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}

	token := ""
	tokenHash := any(nil)
	tokenLast := ""
	if normalized.AuthKind == IntegrationAuthBearerHeader || normalized.AuthKind == IntegrationAuthQueryToken {
		token, err = newIntegrationToken()
		if err != nil {
			return IntegrationAuthProfileCreateResult{}, err
		}
		hash, err := hashIntegrationToken(token)
		if err != nil {
			return IntegrationAuthProfileCreateResult{}, err
		}
		tokenHash = hash
		tokenLast = tokenLastFour(token)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	defer tx.Rollback()

	profile, err := scanIntegrationAuthProfile(tx.QueryRowContext(ctx, `
		INSERT INTO automation.integration_auth_profiles (
			auth_profile_id, integration_id, display_name, auth_kind, status,
			token_hash, token_last_four, allowed_endpoint_refs_json, created_by_actor_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, 'active', $5, $6, $7::jsonb, $8, $9::jsonb)
		RETURNING auth_profile_id, integration_id, display_name, auth_kind, status,
		          token_last_four, allowed_endpoint_refs_json, created_by_actor_id,
		          created_at, updated_at, revoked_at, revoked_reason, metadata_json
	`, ids.NewIntegrationAuthProfileID(),
		integration.IntegrationID,
		normalized.DisplayName,
		normalized.AuthKind,
		tokenHash,
		tokenLast,
		normalized.AllowedEndpointRefs,
		createdBy,
		normalized.Metadata,
	))
	if err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeIntegrationAuthProfileCreated, "integration_auth_profile", profile.AuthProfileID, profile.Status, map[string]any{
		"integration_id":  integration.IntegrationID,
		"integration_key": integration.IntegrationKey,
		"auth_kind":       profile.AuthKind,
	}); err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationAuthProfileCreateResult{}, err
	}
	return IntegrationAuthProfileCreateResult{Profile: profile, Token: token}, nil
}

func (s Service) EnsureIntegrationAuthProfile(ctx context.Context, req requestctx.Context, integrationRef string, input CreateIntegrationAuthProfileInput) (IntegrationAuthProfile, bool, error) {
	if s.DB == nil {
		return IntegrationAuthProfile{}, false, fmt.Errorf("database is required")
	}
	integration, err := s.getIntegration(ctx, integrationRef)
	if err != nil {
		return IntegrationAuthProfile{}, false, err
	}
	if integration.Status != IntegrationStatusActive {
		return IntegrationAuthProfile{}, false, fmt.Errorf("integration must be active")
	}
	normalized, err := normalizeCreateIntegrationAuthProfile(input)
	if err != nil {
		return IntegrationAuthProfile{}, false, err
	}
	profile, err := scanIntegrationAuthProfile(s.DB.QueryRowContext(ctx, integrationAuthProfileSelectSQL()+`
		WHERE integration_id = $1
		  AND display_name = $2
		  AND auth_kind = $3
		  AND status = 'active'
		ORDER BY created_at ASC
		LIMIT 1
	`, integration.IntegrationID, normalized.DisplayName, normalized.AuthKind))
	if err == nil {
		return profile, false, nil
	}
	if err != sql.ErrNoRows {
		return IntegrationAuthProfile{}, false, err
	}
	if normalized.AuthKind != IntegrationAuthPrivateNetwork {
		return IntegrationAuthProfile{}, false, fmt.Errorf("token auth profiles must be created explicitly before project activation")
	}
	result, err := s.CreateIntegrationAuthProfile(ctx, req, integration.IntegrationID, input)
	if err != nil {
		return IntegrationAuthProfile{}, false, err
	}
	return result.Profile, true, nil
}

func (s Service) ListIntegrationAuthProfiles(ctx context.Context, filter IntegrationAuthProfileFilter) ([]IntegrationAuthProfile, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := integrationAuthProfileSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if ref := strings.TrimSpace(filter.IntegrationRef); ref != "" {
		integration, err := s.getIntegration(ctx, ref)
		if err != nil {
			return nil, err
		}
		add("integration_id =", integration.IntegrationID)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IntegrationAuthProfile{}
	for rows.Next() {
		item, err := scanIntegrationAuthProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) RevokeIntegrationAuthProfile(ctx context.Context, req requestctx.Context, ref string, input UpdateIntegrationAuthProfileStatusInput) (IntegrationAuthProfile, error) {
	if s.DB == nil {
		return IntegrationAuthProfile{}, fmt.Errorf("database is required")
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return IntegrationAuthProfile{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationAuthProfile{}, err
	}
	defer tx.Rollback()
	profile, err := scanIntegrationAuthProfile(tx.QueryRowContext(ctx, integrationAuthProfileSelectSQL()+`
		WHERE auth_profile_id = $1
		FOR UPDATE
	`, strings.TrimSpace(ref)))
	if err != nil {
		return IntegrationAuthProfile{}, err
	}
	updated, err := scanIntegrationAuthProfile(tx.QueryRowContext(ctx, `
		UPDATE automation.integration_auth_profiles
		SET status = 'revoked',
		    token_hash = NULL,
		    revoked_at = now(),
		    revoked_reason = nullif($2, ''),
		    updated_at = now(),
		    metadata_json = CASE WHEN $3::jsonb = '{}'::jsonb THEN metadata_json ELSE metadata_json || $3::jsonb END
		WHERE auth_profile_id = $1
		RETURNING auth_profile_id, integration_id, display_name, auth_kind, status,
		          token_last_four, allowed_endpoint_refs_json, created_by_actor_id,
		          created_at, updated_at, revoked_at, revoked_reason, metadata_json
	`, profile.AuthProfileID, strings.TrimSpace(input.Reason), metadata))
	if err != nil {
		return IntegrationAuthProfile{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeIntegrationAuthProfileRevoked, "integration_auth_profile", updated.AuthProfileID, updated.Status, map[string]any{
		"integration_id": updated.IntegrationID,
		"reason":         strings.TrimSpace(input.Reason),
	}); err != nil {
		return IntegrationAuthProfile{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationAuthProfile{}, err
	}
	return updated, nil
}

func (s Service) CreateDirectEventEndpoint(ctx context.Context, req requestctx.Context, input CreateDirectEventEndpointInput) (DirectEventEndpointDetail, error) {
	if s.DB == nil {
		return DirectEventEndpointDetail{}, fmt.Errorf("database is required")
	}
	normalized, err := s.normalizeCreateDirectEventEndpoint(ctx, req, input)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	defer tx.Rollback()

	detail, err := createDirectEventEndpointTx(ctx, tx, req, normalized)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	return detail, nil
}

func (s Service) EnsureDirectEventEndpoint(ctx context.Context, req requestctx.Context, input CreateDirectEventEndpointInput) (DirectEventEndpointDetail, bool, error) {
	if s.DB == nil {
		return DirectEventEndpointDetail{}, false, fmt.Errorf("database is required")
	}
	normalized, err := s.normalizeCreateDirectEventEndpoint(ctx, req, input)
	if err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	defer tx.Rollback()
	existing, err := scanDirectEventEndpoint(tx.QueryRowContext(ctx, directEventEndpointSelectSQL()+`
		WHERE endpoint_slug = $1
		FOR UPDATE
	`, normalized.EndpointSlug))
	if err == sql.ErrNoRows {
		detail, err := createDirectEventEndpointTx(ctx, tx, req, normalized)
		if err != nil {
			return DirectEventEndpointDetail{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return DirectEventEndpointDetail{}, false, err
		}
		return detail, true, nil
	}
	if err != nil {
		return DirectEventEndpointDetail{}, false, err
	}

	automation, err := scanAutomation(tx.QueryRowContext(ctx, `
		UPDATE automation.automations
		SET display_name = $2,
		    description = $3,
		    status = $4,
		    source_profile_json = $5::jsonb,
		    integration_profile_json = $6::jsonb,
		    mapping_profile_json = $7::jsonb,
		    target_profile_json = $8::jsonb,
		    communication_profile_json = $9::jsonb,
		    idempotency_profile_json = $10::jsonb,
		    storage_profile_json = $11::jsonb,
		    timeout_profile_json = $12::jsonb,
		    retry_profile_json = $13::jsonb,
		    run_as_actor_id = $14,
		    scope_id = $15,
		    project_id = $16,
		    metadata_json = $17::jsonb,
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
		automationStatusForDirectEventEndpointStatus(normalized.Status),
		normalized.SourceProfileJSON,
		normalized.IntegrationProfileJSON,
		normalized.MappingProfileJSON,
		normalized.TargetProfileJSON,
		normalized.CommunicationProfileJSON,
		normalized.IdempotencyProfileJSON,
		normalized.StorageProfileJSON,
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	endpoint, err := scanDirectEventEndpoint(tx.QueryRowContext(ctx, `
		UPDATE automation.direct_event_endpoints
		SET display_name = $2,
		    description = $3,
		    status = $4,
		    integration_id = $5,
		    event_type = $6,
		    endpoint_path = $7,
		    response_mode = $8,
		    mapping_profile_json = $9::jsonb,
		    auth_profile_refs_json = $10::jsonb,
		    metadata_json = $11::jsonb,
		    updated_at = now()
		WHERE endpoint_id = $1
		RETURNING endpoint_id, endpoint_slug, display_name, description, status,
		          integration_id, automation_id, event_type, endpoint_path, response_mode,
		          mapping_profile_json, auth_profile_refs_json, created_by_actor_id,
		          created_at, updated_at, disabled_at, metadata_json
	`,
		existing.EndpointID,
		normalized.DisplayName,
		normalized.Description,
		normalized.Status,
		normalized.Integration.IntegrationID,
		normalized.EventType,
		normalized.EndpointPath,
		normalized.ResponseMode,
		normalized.MappingProfileJSON,
		normalized.AuthProfileRefsJSON,
		normalized.Metadata,
	))
	if err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventEndpointUpdated, "direct_event_endpoint", endpoint.EndpointID, endpoint.Status, map[string]any{
		"endpoint_slug":     endpoint.EndpointSlug,
		"integration_id":    endpoint.IntegrationID,
		"integration_key":   normalized.Integration.IntegrationKey,
		"automation_id":     endpoint.AutomationID,
		"event_type":        endpoint.EventType,
		"target_capability": normalized.Target.CapabilityRef,
	}); err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return DirectEventEndpointDetail{}, false, err
	}
	return DirectEventEndpointDetail{Endpoint: endpoint, Integration: normalized.Integration, Automation: automation}, false, nil
}

func createDirectEventEndpointTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, normalized normalizedCreateDirectEventEndpoint) (DirectEventEndpointDetail, error) {
	automationID := ids.NewAutomationID()
	endpointID := ids.NewDirectEventEndpointID()
	automation, err := scanAutomation(tx.QueryRowContext(ctx, automationInsertSQL(), automationID,
		normalized.EndpointSlug,
		normalized.DisplayName,
		normalized.Description,
		automationStatusForDirectEventEndpointStatus(normalized.Status),
		SourceKindDirectEvent,
		normalized.SourceProfileJSON,
		normalized.IntegrationProfileJSON,
		normalized.MappingProfileJSON,
		normalized.TargetProfileJSON,
		normalized.CommunicationProfileJSON,
		normalized.IdempotencyProfileJSON,
		normalized.StorageProfileJSON,
		json.RawMessage(`{"mode":"worker_backed"}`),
		normalized.TimeoutProfileJSON,
		normalized.RetryProfileJSON,
		json.RawMessage(`{"policy":"allow_parallel"}`),
		json.RawMessage(`{"policy":"mark_missed"}`),
		json.RawMessage(`{"mode":"none"}`),
		normalized.CreatedByActorID,
		normalized.RunAsActorID,
		nullableString(normalized.ScopeID),
		nullableString(normalized.ProjectID),
		normalized.Metadata,
	))
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}

	endpoint, err := scanDirectEventEndpoint(tx.QueryRowContext(ctx, `
		INSERT INTO automation.direct_event_endpoints (
			endpoint_id, endpoint_slug, display_name, description, status,
			integration_id, automation_id, event_type, endpoint_path, response_mode,
			mapping_profile_json, auth_profile_refs_json, created_by_actor_id, metadata_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12::jsonb, $13, $14::jsonb)
		RETURNING endpoint_id, endpoint_slug, display_name, description, status,
		          integration_id, automation_id, event_type, endpoint_path, response_mode,
		          mapping_profile_json, auth_profile_refs_json, created_by_actor_id,
		          created_at, updated_at, disabled_at, metadata_json
	`, endpointID,
		normalized.EndpointSlug,
		normalized.DisplayName,
		normalized.Description,
		normalized.Status,
		normalized.Integration.IntegrationID,
		automation.AutomationID,
		normalized.EventType,
		normalized.EndpointPath,
		normalized.ResponseMode,
		normalized.MappingProfileJSON,
		normalized.AuthProfileRefsJSON,
		normalized.CreatedByActorID,
		normalized.Metadata,
	))
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeAutomationCreated, "automation", automation.AutomationID, automation.Status, map[string]any{
		"automation_key": automation.AutomationKey,
		"source_kind":    automation.SourceKind,
		"endpoint_id":    endpoint.EndpointID,
	}); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventEndpointCreated, "direct_event_endpoint", endpoint.EndpointID, endpoint.Status, map[string]any{
		"endpoint_slug":     endpoint.EndpointSlug,
		"integration_id":    endpoint.IntegrationID,
		"integration_key":   normalized.Integration.IntegrationKey,
		"automation_id":     endpoint.AutomationID,
		"event_type":        endpoint.EventType,
		"target_capability": normalized.Target.CapabilityRef,
	}); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	return DirectEventEndpointDetail{Endpoint: endpoint, Integration: normalized.Integration, Automation: automation}, nil
}

func (s Service) ListDirectEventEndpoints(ctx context.Context, filter DirectEventEndpointFilter) ([]DirectEventEndpoint, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	filter.Limit = normalizeLimit(filter.Limit)
	query := directEventEndpointSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
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
	if !hasExplicitRuntimeRef(filter.IntegrationRef, filter.AutomationRef, filter.ProjectRef) {
		query += archivedProjectByAutomationColumnSQL("automation_id")
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirectEventEndpoint{}
	for rows.Next() {
		item, err := scanDirectEventEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) ListDirectEventEndpointsForMaintenance(ctx context.Context, status string) ([]DirectEventEndpoint, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("database is required")
	}
	status = strings.TrimSpace(status)
	if status == "" {
		status = DirectEventEndpointStatusActive
	}
	rows, err := s.DB.QueryContext(ctx, directEventEndpointSelectSQL()+`
		WHERE status = $1
		ORDER BY created_at DESC
	`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DirectEventEndpoint{}
	for rows.Next() {
		item, err := scanDirectEventEndpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s Service) GetDirectEventEndpoint(ctx context.Context, ref string) (DirectEventEndpointDetail, error) {
	if s.DB == nil {
		return DirectEventEndpointDetail{}, fmt.Errorf("database is required")
	}
	endpoint, err := s.getDirectEventEndpoint(ctx, ref)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	integration, err := s.getIntegration(ctx, endpoint.IntegrationID)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	automation, err := s.getAutomation(ctx, endpoint.AutomationID)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	return DirectEventEndpointDetail{Endpoint: endpoint, Integration: integration, Automation: automation}, nil
}

func (s Service) PauseDirectEventEndpoint(ctx context.Context, req requestctx.Context, ref string, input UpdateDirectEventEndpointStatusInput) (DirectEventEndpointDetail, error) {
	return s.updateDirectEventEndpointStatus(ctx, req, ref, DirectEventEndpointStatusPaused, events.TypeDirectEventEndpointPaused, input)
}

func (s Service) ResumeDirectEventEndpoint(ctx context.Context, req requestctx.Context, ref string, input UpdateDirectEventEndpointStatusInput) (DirectEventEndpointDetail, error) {
	return s.updateDirectEventEndpointStatus(ctx, req, ref, DirectEventEndpointStatusActive, events.TypeDirectEventEndpointResumed, input)
}

func (s Service) DisableDirectEventEndpoint(ctx context.Context, req requestctx.Context, ref string, input UpdateDirectEventEndpointStatusInput) (DirectEventEndpointDetail, error) {
	return s.updateDirectEventEndpointStatus(ctx, req, ref, DirectEventEndpointStatusDisabled, events.TypeDirectEventEndpointDisabled, input)
}

func (s Service) PreviewDirectEventEndpointMapping(ctx context.Context, req requestctx.Context, ref string, input MappingPreviewInput) (MappingPreviewResult, error) {
	if s.DB == nil {
		return MappingPreviewResult{}, fmt.Errorf("database is required")
	}
	endpoint, err := s.getDirectEventEndpoint(ctx, ref)
	if err != nil {
		return MappingPreviewResult{}, err
	}
	automation, err := s.getAutomation(ctx, endpoint.AutomationID)
	if err != nil {
		return MappingPreviewResult{}, err
	}
	if err := ensureAutomationRuntimeActive(ctx, s.DB, automation, "direct_event_preview", endpoint.EndpointSlug); err != nil {
		return MappingPreviewResult{}, err
	}
	inputJSON, missing, fieldCount, err := ApplyMappingPreview(endpoint.MappingProfileJSON, input)
	if err != nil {
		return MappingPreviewResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return MappingPreviewResult{}, err
	}
	defer tx.Rollback()
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeDirectEventMappingPreviewed, "direct_event_endpoint", endpoint.EndpointID, endpoint.Status, map[string]any{
		"endpoint_slug":  endpoint.EndpointSlug,
		"missing_fields": missing,
		"field_count":    fieldCount,
	}); err != nil {
		return MappingPreviewResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return MappingPreviewResult{}, err
	}
	return MappingPreviewResult{Endpoint: endpoint, InputJSON: inputJSON, MissingFields: missing, FieldCount: fieldCount}, nil
}

type normalizedCreateIntegration struct {
	IntegrationKey      string
	DisplayName         string
	Description         string
	MainAuthLevel       int
	AllowedScopes       json.RawMessage
	AllowedProjects     json.RawMessage
	AllowedEndpointRefs json.RawMessage
	Metadata            json.RawMessage
}

func normalizeCreateIntegration(input CreateIntegrationInput) (normalizedCreateIntegration, error) {
	key := strings.ToLower(strings.TrimSpace(input.IntegrationKey))
	if key == "" {
		return normalizedCreateIntegration{}, fmt.Errorf("integration_key is required")
	}
	if !validKey(key) {
		return normalizedCreateIntegration{}, fmt.Errorf("integration_key must match ^[a-z][a-z0-9_-]{0,80}$")
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = key
	}
	mainAuthLevel := input.MainAuthLevel
	if mainAuthLevel == 0 {
		mainAuthLevel = 3
	}
	if mainAuthLevel < 0 || mainAuthLevel > 5 {
		return normalizedCreateIntegration{}, fmt.Errorf("main_auth_level must be between 1 and 5, or 0 for default")
	}
	allowedScopes, err := normalizeStringArrayJSON(input.AllowedScopes, "allowed_scopes")
	if err != nil {
		return normalizedCreateIntegration{}, err
	}
	allowedProjects, err := normalizeStringArrayJSON(input.AllowedProjects, "allowed_projects")
	if err != nil {
		return normalizedCreateIntegration{}, err
	}
	allowedEndpoints, err := normalizeStringArrayJSON(input.AllowedEndpointRefs, "allowed_endpoint_refs")
	if err != nil {
		return normalizedCreateIntegration{}, err
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return normalizedCreateIntegration{}, err
	}
	return normalizedCreateIntegration{
		IntegrationKey:      key,
		DisplayName:         displayName,
		Description:         strings.TrimSpace(input.Description),
		MainAuthLevel:       mainAuthLevel,
		AllowedScopes:       allowedScopes,
		AllowedProjects:     allowedProjects,
		AllowedEndpointRefs: allowedEndpoints,
		Metadata:            metadata,
	}, nil
}

type normalizedCreateIntegrationAuthProfile struct {
	DisplayName         string
	AuthKind            string
	AllowedEndpointRefs json.RawMessage
	Metadata            json.RawMessage
}

func normalizeCreateIntegrationAuthProfile(input CreateIntegrationAuthProfileInput) (normalizedCreateIntegrationAuthProfile, error) {
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = "default"
	}
	authKind := strings.TrimSpace(input.AuthKind)
	if authKind == "" {
		authKind = IntegrationAuthBearerHeader
	}
	switch authKind {
	case IntegrationAuthBearerHeader, IntegrationAuthQueryToken, IntegrationAuthPrivateNetwork:
	default:
		return normalizedCreateIntegrationAuthProfile{}, fmt.Errorf("unsupported auth_kind: %s", authKind)
	}
	allowedEndpoints, err := normalizeStringArrayJSON(input.AllowedEndpointRefs, "allowed_endpoint_refs")
	if err != nil {
		return normalizedCreateIntegrationAuthProfile{}, err
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return normalizedCreateIntegrationAuthProfile{}, err
	}
	return normalizedCreateIntegrationAuthProfile{
		DisplayName:         displayName,
		AuthKind:            authKind,
		AllowedEndpointRefs: allowedEndpoints,
		Metadata:            metadata,
	}, nil
}

type normalizedCreateDirectEventEndpoint struct {
	EndpointSlug             string
	DisplayName              string
	Description              string
	Status                   string
	EventType                string
	ResponseMode             string
	EndpointPath             string
	Integration              Integration
	Target                   TargetProfile
	CreatedByActorID         string
	RunAsActorID             string
	ScopeID                  string
	ProjectID                string
	SourceProfileJSON        json.RawMessage
	IntegrationProfileJSON   json.RawMessage
	MappingProfileJSON       json.RawMessage
	TargetProfileJSON        json.RawMessage
	CommunicationProfileJSON json.RawMessage
	IdempotencyProfileJSON   json.RawMessage
	StorageProfileJSON       json.RawMessage
	TimeoutProfileJSON       json.RawMessage
	RetryProfileJSON         json.RawMessage
	AuthProfileRefsJSON      json.RawMessage
	Metadata                 json.RawMessage
}

func (s Service) normalizeCreateDirectEventEndpoint(ctx context.Context, req requestctx.Context, input CreateDirectEventEndpointInput) (normalizedCreateDirectEventEndpoint, error) {
	slug := strings.ToLower(strings.TrimSpace(input.EndpointSlug))
	if slug == "" {
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("endpoint_slug is required")
	}
	if !validKey(slug) {
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("endpoint_slug must match ^[a-z][a-z0-9_-]{0,80}$")
	}
	status, err := normalizeInitialDirectEventEndpointStatus(input.Status)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	displayName := strings.TrimSpace(input.DisplayName)
	if displayName == "" {
		displayName = slug
	}
	eventType := strings.TrimSpace(input.EventType)
	if eventType == "" {
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("event_type is required")
	}
	targetCapability := strings.TrimSpace(input.TargetCapability)
	if targetCapability == "" {
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("target_capability is required")
	}
	if err := s.validateTargetCapability(ctx, targetCapability); err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	integration, err := s.getIntegration(ctx, input.IntegrationRef)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	if integration.Status != IntegrationStatusActive {
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("integration must be active")
	}
	responseMode := strings.TrimSpace(input.ResponseMode)
	if responseMode == "" {
		responseMode = DirectEventResponseAccepted
	}
	switch responseMode {
	case DirectEventResponseAccepted, DirectEventResponseSyncWait:
	default:
		return normalizedCreateDirectEventEndpoint{}, fmt.Errorf("unsupported response_mode: %s", responseMode)
	}
	mappingJSON, _, err := NormalizeMappingProfile(input.MappingProfile)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	idempotencyJSON, err := normalizeIdempotencyProfile(input.IdempotencyProfile)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	communicationJSON, err := normalizeCommunicationProfile(input.CommunicationProfile, responseMode)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	storageJSON, err := normalizeJSONObject(input.StorageProfile, "storage_profile")
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	authRefsJSON, authRefs, err := normalizeStringArrayJSONWithValues(input.AuthProfileRefs, "auth_profile_refs")
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	if err := s.validateAuthProfileRefs(ctx, integration.IntegrationID, authRefs); err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	scopeRef := strings.TrimSpace(input.ScopeRef)
	if scopeRef == "" {
		scopeRef = req.ScopeID
	}
	scopeID, err := resolveScopeID(ctx, s.DB, scopeRef)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	projectID := ""
	if strings.TrimSpace(input.ProjectRef) != "" {
		projectID, err = resolveProjectID(ctx, s.DB, input.ProjectRef)
		if err != nil {
			return normalizedCreateDirectEventEndpoint{}, err
		}
	}
	createdBy, err := resolveActorID(ctx, s.DB, req.ActorID)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	targetProfile := TargetProfile{
		CapabilityRef: targetCapability,
		ScopeRef:      scopeRef,
		ProjectRef:    strings.TrimSpace(input.ProjectRef),
	}
	targetJSON := mustJSON(targetProfile)
	if _, err := NormalizeTargetProfile(targetJSON); err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	timeoutJSON := mustJSON(TimeoutProfile{TimeoutSeconds: input.TimeoutSeconds})
	timeoutProfile, err := NormalizeTimeoutProfile(timeoutJSON)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	timeoutJSON = mustJSON(timeoutProfile)
	retryJSON := mustJSON(RetryProfile{MaxAttempts: input.MaxAttempts})
	retryProfile, err := NormalizeRetryProfile(retryJSON)
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	retryJSON = mustJSON(retryProfile)
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return normalizedCreateDirectEventEndpoint{}, err
	}
	endpointPath := "/v1/direct-events/ingest/" + slug
	sourceJSON := mustJSON(map[string]any{
		"source_kind":   SourceKindDirectEvent,
		"endpoint_slug": slug,
		"event_type":    eventType,
		"endpoint_path": endpointPath,
	})
	integrationJSON := mustJSON(DirectEventIntegrationProfile{
		IntegrationID:  integration.IntegrationID,
		IntegrationKey: integration.IntegrationKey,
		EndpointSlug:   slug,
		EventType:      eventType,
		AuthProfileIDs: authRefs,
	})

	return normalizedCreateDirectEventEndpoint{
		EndpointSlug:             slug,
		DisplayName:              displayName,
		Description:              strings.TrimSpace(input.Description),
		Status:                   status,
		EventType:                eventType,
		ResponseMode:             responseMode,
		EndpointPath:             endpointPath,
		Integration:              integration,
		Target:                   targetProfile,
		CreatedByActorID:         createdBy,
		RunAsActorID:             integration.ActorID,
		ScopeID:                  scopeID,
		ProjectID:                projectID,
		SourceProfileJSON:        sourceJSON,
		IntegrationProfileJSON:   integrationJSON,
		MappingProfileJSON:       mappingJSON,
		TargetProfileJSON:        targetJSON,
		CommunicationProfileJSON: communicationJSON,
		IdempotencyProfileJSON:   idempotencyJSON,
		StorageProfileJSON:       storageJSON,
		TimeoutProfileJSON:       timeoutJSON,
		RetryProfileJSON:         retryJSON,
		AuthProfileRefsJSON:      authRefsJSON,
		Metadata:                 metadata,
	}, nil
}

func (s Service) updateIntegrationStatus(ctx context.Context, req requestctx.Context, ref, status, eventType string, input UpdateIntegrationStatusInput) (IntegrationDetail, error) {
	if s.DB == nil {
		return IntegrationDetail{}, fmt.Errorf("database is required")
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return IntegrationDetail{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return IntegrationDetail{}, err
	}
	defer tx.Rollback()
	integration, err := scanIntegration(tx.QueryRowContext(ctx, integrationSelectSQL()+`
		WHERE integration_id = $1 OR integration_key = $1
		FOR UPDATE
	`, strings.TrimSpace(ref)))
	if err != nil {
		return IntegrationDetail{}, err
	}
	actorStatus := "disabled"
	authStatus := "disabled"
	authorizationStatus := "disabled"
	if status == IntegrationStatusRevoked {
		actorStatus = "revoked"
		authStatus = "revoked"
		authorizationStatus = "revoked"
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation.integrations
		SET status = $2,
		    revoked_at = CASE WHEN $2 = 'revoked' THEN now() ELSE revoked_at END,
		    revoked_reason = CASE WHEN $2 = 'revoked' THEN nullif($3, '') ELSE revoked_reason END,
		    updated_at = now(),
		    metadata_json = CASE WHEN $4::jsonb = '{}'::jsonb THEN metadata_json ELSE metadata_json || $4::jsonb END
		WHERE integration_id = $1
	`, integration.IntegrationID, status, strings.TrimSpace(input.Reason), metadata); err != nil {
		return IntegrationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actors
		SET status = $2,
		    revoked_at = CASE WHEN $2 = 'revoked' THEN now() ELSE revoked_at END,
		    revoked_reason = CASE WHEN $2 = 'revoked' THEN nullif($3, '') ELSE revoked_reason END,
		    updated_at = now()
		WHERE actor_id = $1
	`, integration.ActorID, actorStatus, strings.TrimSpace(input.Reason)); err != nil {
		return IntegrationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE identity.actor_node_authorizations
		SET status = $2
		WHERE actor_id = $1
	`, integration.ActorID, authorizationStatus); err != nil {
		return IntegrationDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation.integration_auth_profiles
		SET status = $2,
		    token_hash = CASE WHEN $2 = 'revoked' THEN NULL ELSE token_hash END,
		    revoked_at = CASE WHEN $2 = 'revoked' THEN now() ELSE revoked_at END,
		    revoked_reason = CASE WHEN $2 = 'revoked' THEN nullif($3, '') ELSE revoked_reason END,
		    updated_at = now()
		WHERE integration_id = $1
		  AND status = 'active'
	`, integration.IntegrationID, authStatus, strings.TrimSpace(input.Reason)); err != nil {
		return IntegrationDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, eventType, "integration", integration.IntegrationID, status, map[string]any{
		"integration_key": integration.IntegrationKey,
		"actor_id":        integration.ActorID,
		"reason":          strings.TrimSpace(input.Reason),
	}); err != nil {
		return IntegrationDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return IntegrationDetail{}, err
	}
	return s.GetIntegration(ctx, integration.IntegrationID)
}

func (s Service) updateDirectEventEndpointStatus(ctx context.Context, req requestctx.Context, ref, status, eventType string, input UpdateDirectEventEndpointStatusInput) (DirectEventEndpointDetail, error) {
	if s.DB == nil {
		return DirectEventEndpointDetail{}, fmt.Errorf("database is required")
	}
	metadata, err := normalizeJSONObject(input.Metadata, "metadata")
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	defer tx.Rollback()
	endpoint, err := scanDirectEventEndpoint(tx.QueryRowContext(ctx, directEventEndpointSelectSQL()+`
		WHERE endpoint_id = $1 OR endpoint_slug = $1
		FOR UPDATE
	`, strings.TrimSpace(ref)))
	if err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation.direct_event_endpoints
		SET status = $2,
		    disabled_at = CASE WHEN $2 = 'disabled' THEN now() ELSE disabled_at END,
		    updated_at = now(),
		    metadata_json = CASE WHEN $3::jsonb = '{}'::jsonb THEN metadata_json ELSE metadata_json || $3::jsonb END
		WHERE endpoint_id = $1
	`, endpoint.EndpointID, status, metadata); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	automationStatus := AutomationStatusActive
	automationEvent := events.TypeAutomationResumed
	if status == DirectEventEndpointStatusPaused {
		automationStatus = AutomationStatusPaused
		automationEvent = events.TypeAutomationPaused
	}
	if status == DirectEventEndpointStatusDisabled {
		automationStatus = AutomationStatusDisabled
		automationEvent = events.TypeAutomationDisabled
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE automation.automations
		SET status = $2, updated_at = now()
		WHERE automation_id = $1
	`, endpoint.AutomationID, automationStatus); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, eventType, "direct_event_endpoint", endpoint.EndpointID, status, map[string]any{
		"endpoint_slug": endpoint.EndpointSlug,
		"automation_id": endpoint.AutomationID,
		"reason":        strings.TrimSpace(input.Reason),
	}); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, automationEvent, "automation", endpoint.AutomationID, automationStatus, map[string]any{
		"endpoint_id": endpoint.EndpointID,
		"reason":      strings.TrimSpace(input.Reason),
	}); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return DirectEventEndpointDetail{}, err
	}
	return s.GetDirectEventEndpoint(ctx, endpoint.EndpointID)
}

func (s Service) getIntegration(ctx context.Context, ref string) (Integration, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Integration{}, fmt.Errorf("integration ref is required")
	}
	return scanIntegration(s.DB.QueryRowContext(ctx, integrationSelectSQL()+`
		WHERE integration_id = $1 OR integration_key = $1
	`, ref))
}

func (s Service) getDirectEventEndpoint(ctx context.Context, ref string) (DirectEventEndpoint, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return DirectEventEndpoint{}, fmt.Errorf("direct event endpoint ref is required")
	}
	return scanDirectEventEndpoint(s.DB.QueryRowContext(ctx, directEventEndpointSelectSQL()+`
		WHERE endpoint_id = $1 OR endpoint_slug = $1
	`, ref))
}

func (s Service) getIntegrationAuthProfile(ctx context.Context, ref string) (IntegrationAuthProfile, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return IntegrationAuthProfile{}, fmt.Errorf("integration auth profile ref is required")
	}
	return scanIntegrationAuthProfile(s.DB.QueryRowContext(ctx, integrationAuthProfileSelectSQL()+`
		WHERE auth_profile_id = $1
	`, ref))
}

func (s Service) getDirectEvent(ctx context.Context, ref string) (DirectEvent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return DirectEvent{}, fmt.Errorf("direct event ref is required")
	}
	return scanDirectEvent(s.DB.QueryRowContext(ctx, directEventSelectSQL()+`
		WHERE direct_event_id = $1
	`, ref))
}

func ensureIntegrationActor(ctx context.Context, tx *sql.Tx, integrationKey, displayName string, metadata json.RawMessage) (string, error) {
	actorKey := "integration:" + integrationKey
	actorID := ids.NewActorID()
	err := tx.QueryRowContext(ctx, `
		INSERT INTO identity.actors (actor_id, actor_key, display_name, actor_kind, status, metadata)
		VALUES ($1, $2, $3, 'external_integration', 'active', $4::jsonb)
		ON CONFLICT (actor_key) DO NOTHING
		RETURNING actor_id
	`, actorID, actorKey, displayName, metadata).Scan(&actorID)
	if err == nil {
		return actorID, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	var actorKind, status string
	err = tx.QueryRowContext(ctx, `
		SELECT actor_id, actor_kind, status
		FROM identity.actors
		WHERE actor_key = $1
	`, actorKey).Scan(&actorID, &actorKind, &status)
	if err != nil {
		return "", err
	}
	if actorKind != "external_integration" {
		return "", fmt.Errorf("actor key %q already belongs to actor_kind %q", actorKey, actorKind)
	}
	if status != "active" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE identity.actors
			SET status = 'active',
			    updated_at = now(),
			    metadata = metadata || $2::jsonb
			WHERE actor_id = $1
		`, actorID, metadata); err != nil {
			return "", err
		}
	}
	return actorID, nil
}

func resolveMainNodeID(ctx context.Context, db queryer) (string, error) {
	var nodeID string
	err := db.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_key = 'main'
		  AND status = 'active'
	`).Scan(&nodeID)
	if err != nil {
		return "", fmt.Errorf("resolve main node: %w", err)
	}
	return nodeID, nil
}

func normalizeStringArrayJSON(raw json.RawMessage, name string) (json.RawMessage, error) {
	normalized, _, err := normalizeStringArrayJSONWithValues(raw, name)
	return normalized, err
}

func normalizeStringArrayJSONWithValues(raw json.RawMessage, name string) (json.RawMessage, []string, error) {
	normalized, err := normalizeJSONArray(raw, name)
	if err != nil {
		return nil, nil, err
	}
	var values []string
	if err := json.Unmarshal(normalized, &values); err != nil {
		return nil, nil, fmt.Errorf("%s must be a JSON array of strings", name)
	}
	for i, value := range values {
		values[i] = strings.TrimSpace(value)
		if values[i] == "" {
			return nil, nil, fmt.Errorf("%s contains an empty string", name)
		}
	}
	normalized, err = json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	return json.RawMessage(normalized), values, nil
}

func normalizeCommunicationProfile(raw json.RawMessage, responseMode string) (json.RawMessage, error) {
	normalized, err := normalizeJSONObject(raw, "communication_profile")
	if err != nil {
		return nil, err
	}
	profile := CommunicationProfile{ResponseMode: responseMode}
	if err := json.Unmarshal(normalized, &profile); err != nil {
		return nil, fmt.Errorf("communication_profile is invalid JSON: %w", err)
	}
	if strings.TrimSpace(profile.ResponseMode) == "" {
		profile.ResponseMode = responseMode
	}
	switch profile.ResponseMode {
	case DirectEventResponseAccepted, DirectEventResponseSyncWait:
	default:
		return nil, fmt.Errorf("unsupported communication response_mode: %s", profile.ResponseMode)
	}
	if profile.ResponseMode == DirectEventResponseSyncWait {
		if profile.SyncWaitTimeoutSeconds <= 0 {
			profile.SyncWaitTimeoutSeconds = defaultDirectEventSyncWaitSeconds
		}
		if profile.SyncWaitTimeoutSeconds > maxDirectEventSyncWaitSeconds {
			return nil, fmt.Errorf("sync_wait_timeout_seconds must be <= %d", maxDirectEventSyncWaitSeconds)
		}
	} else {
		profile.SyncWaitTimeoutSeconds = 0
	}
	return mustJSON(profile), nil
}

func normalizeIdempotencyProfile(raw json.RawMessage) (json.RawMessage, error) {
	normalized, err := normalizeJSONObject(raw, "idempotency_profile")
	if err != nil {
		return nil, err
	}
	profile := IdempotencyProfile{Strategy: DirectEventIdempotencyNone}
	if err := json.Unmarshal(normalized, &profile); err != nil {
		return nil, fmt.Errorf("idempotency_profile is invalid JSON: %w", err)
	}
	profile.Strategy = strings.TrimSpace(profile.Strategy)
	if profile.Strategy == "" {
		profile.Strategy = DirectEventIdempotencyNone
	}
	profile.Path = strings.TrimSpace(profile.Path)
	profile.Header = strings.TrimSpace(profile.Header)
	switch profile.Strategy {
	case DirectEventIdempotencyNone:
		profile.Path = ""
		profile.Header = ""
	case DirectEventIdempotencyPayloadPath:
		if profile.Path == "" {
			return nil, fmt.Errorf("idempotency path is required for payload_path strategy")
		}
	case DirectEventIdempotencyHeader:
		if profile.Header == "" {
			return nil, fmt.Errorf("idempotency header is required for header strategy")
		}
	default:
		return nil, fmt.Errorf("unsupported idempotency strategy: %s", profile.Strategy)
	}
	return mustJSON(profile), nil
}

func validInitialDirectEventEndpointStatus(status string) bool {
	switch status {
	case DirectEventEndpointStatusActive, DirectEventEndpointStatusPaused, DirectEventEndpointStatusDisabled:
		return true
	default:
		return false
	}
}

func normalizeInitialDirectEventEndpointStatus(status string) (string, error) {
	status = strings.TrimSpace(status)
	if status == "" {
		return DirectEventEndpointStatusActive, nil
	}
	if !validInitialDirectEventEndpointStatus(status) {
		return "", fmt.Errorf("unsupported direct event endpoint status: %s", status)
	}
	return status, nil
}

func automationStatusForDirectEventEndpointStatus(status string) string {
	switch status {
	case DirectEventEndpointStatusPaused:
		return AutomationStatusPaused
	case DirectEventEndpointStatusDisabled:
		return AutomationStatusDisabled
	default:
		return AutomationStatusActive
	}
}

func (s Service) validateAuthProfileRefs(ctx context.Context, integrationID string, refs []string) error {
	for _, ref := range refs {
		var found string
		err := s.DB.QueryRowContext(ctx, `
			SELECT auth_profile_id
			FROM automation.integration_auth_profiles
			WHERE auth_profile_id = $1
			  AND integration_id = $2
			  AND status = 'active'
		`, ref, integrationID).Scan(&found)
		if err != nil {
			return fmt.Errorf("auth profile %q is not active for integration: %w", ref, err)
		}
	}
	return nil
}
