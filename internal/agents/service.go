package agents

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

const actorKindAgent = "agent"

type Service struct {
	DB       *sql.DB
	Routing  routing.Service
	Realtime realtime.Service
}

func NewService(db *sql.DB) Service {
	return NewServiceWithRuntime(db, routing.NewService(db), realtime.NewService(db))
}

func NewServiceWithRuntime(db *sql.DB, routingService routing.Service, realtimeService realtime.Service) Service {
	return Service{DB: db, Routing: routingService, Realtime: realtimeService}
}

type actorRecord struct {
	ID             string
	Key            string
	Kind           string
	Status         string
	HomeNodeID     *string
	DefaultScopeID *string
}

type capabilityToolCandidate struct {
	EndpointID                  string
	CompactAddress              string
	ProviderID                  string
	ProviderAddress             string
	TargetNodeID                string
	ProviderHealth              string
	ClassName                   string
	DisplayName                 string
	Description                 string
	Form                        string
	RiskLevel                   string
	ExecutionAuthorizationLevel int
	Metadata                    json.RawMessage
}

func (s Service) CreateAccessSession(ctx context.Context, req requestctx.Context, input CreateAccessSessionInput) (AccessSession, error) {
	input = normalizeCreateAccessSessionInput(input)
	if input.ActorRef == "" {
		return AccessSession{}, fmt.Errorf("actor_ref is required")
	}
	if !ValidOriginKind(input.OriginKind) {
		return AccessSession{}, fmt.Errorf("unsupported origin_kind: %s", input.OriginKind)
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return AccessSession{}, err
	}

	actor, err := resolveActor(ctx, s.DB, input.ActorRef)
	if err != nil {
		return AccessSession{}, err
	}
	if actor.Kind != actorKindAgent {
		return AccessSession{}, fmt.Errorf("actor %s is not an agent actor", input.ActorRef)
	}
	if actor.Status != "active" {
		return AccessSession{}, fmt.Errorf("agent actor %s is not active", input.ActorRef)
	}

	originNodeID := req.OriginNodeID
	if input.OriginNodeRef != "" {
		originNodeID, err = resolveNodeID(ctx, s.DB, input.OriginNodeRef)
		if err != nil {
			return AccessSession{}, err
		}
	}
	runtimeNodeID := originNodeID
	if input.RuntimeNodeRef != "" {
		runtimeNodeID, err = resolveNodeID(ctx, s.DB, input.RuntimeNodeRef)
		if err != nil {
			return AccessSession{}, err
		}
	}
	homeNodeID := runtimeNodeID
	if actor.HomeNodeID != nil && *actor.HomeNodeID != "" {
		homeNodeID = *actor.HomeNodeID
	}
	if input.HomeNodeRef != "" {
		homeNodeID, err = resolveNodeID(ctx, s.DB, input.HomeNodeRef)
		if err != nil {
			return AccessSession{}, err
		}
	}
	scopeID := req.ScopeID
	if actor.DefaultScopeID != nil && *actor.DefaultScopeID != "" {
		scopeID = *actor.DefaultScopeID
	}
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return AccessSession{}, err
		}
	}
	var projectID string
	if input.ProjectRef != "" {
		projectID, err = resolveProjectID(ctx, s.DB, input.ProjectRef)
		if err != nil {
			return AccessSession{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return AccessSession{}, err
	}
	defer tx.Rollback()

	sessionID := ids.NewAgentAccessSessionID()
	sessionKey := input.AccessSessionKey
	if sessionKey == "" {
		sessionKey = sessionID
	}
	session, err := scanAccessSession(tx.QueryRowContext(ctx, accessSessionSelectSQL(`
		INSERT INTO agents.agent_access_sessions (
			agent_access_session_id, access_session_key, actor_id, created_by_actor_id,
			origin_kind, origin_node_id, origin_client_id, runtime_node_id,
			home_node_id, current_scope_id, active_project_id, status, expires_at, metadata
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, nullif($8, ''),
		        nullif($9, ''), nullif($10, ''), nullif($11, ''), $12, $13, $14)
	`),
		sessionID,
		sessionKey,
		actor.ID,
		req.ActorID,
		input.OriginKind,
		originNodeID,
		input.OriginClientID,
		runtimeNodeID,
		homeNodeID,
		scopeID,
		projectID,
		AccessSessionStatusActive,
		input.ExpiresAt,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return AccessSession{}, err
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentAccessSessionCreated,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(session.CurrentScopeID),
		TargetKind:      "agent_access_session",
		TargetID:        session.AgentAccessSessionID,
		Status:          session.Status,
		Result:          "created",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"agent_access_session_id": session.AgentAccessSessionID,
			"actor_id":                session.ActorID,
			"origin_kind":             session.OriginKind,
			"runtime_node_id":         stringValue(session.RuntimeNodeID),
			"home_node_id":            stringValue(session.HomeNodeID),
			"current_scope_id":        stringValue(session.CurrentScopeID),
		},
	}); err != nil {
		return AccessSession{}, err
	}
	if err := tx.Commit(); err != nil {
		return AccessSession{}, err
	}
	return session, nil
}

func (s Service) ListAccessSessions(ctx context.Context, filter AccessSessionFilter) ([]AccessSession, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Status != "" && !ValidAccessSessionStatus(filter.Status) {
		return nil, fmt.Errorf("unsupported access session status filter: %s", filter.Status)
	}

	query := accessSessionSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if filter.ActorRef != "" {
		actor, err := resolveActor(ctx, s.DB, filter.ActorRef)
		if err != nil {
			return nil, err
		}
		add("actor_id =", actor.ID)
	}
	if filter.Status != "" {
		add("status =", filter.Status)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []AccessSession{}
	for rows.Next() {
		session, err := scanAccessSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s Service) GetAccessSession(ctx context.Context, ref string) (AccessSession, error) {
	return getAccessSession(ctx, s.DB, ref)
}

func (s Service) CreateWorkContext(ctx context.Context, req requestctx.Context, input CreateWorkContextInput) (WorkContextDetail, error) {
	input = normalizeCreateWorkContextInput(input)
	if input.AccessSessionRef == "" {
		return WorkContextDetail{}, fmt.Errorf("access_session_ref is required")
	}
	if input.Objective == "" {
		return WorkContextDetail{}, fmt.Errorf("objective is required")
	}
	if err := validateObjectJSON(input.Metadata, "metadata"); err != nil {
		return WorkContextDetail{}, err
	}

	session, err := getAccessSession(ctx, s.DB, input.AccessSessionRef)
	if err != nil {
		return WorkContextDetail{}, err
	}
	if session.Status != AccessSessionStatusActive {
		return WorkContextDetail{}, fmt.Errorf("access session is not active")
	}

	scopeID := stringValue(session.CurrentScopeID)
	if input.ScopeRef != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, input.ScopeRef)
		if err != nil {
			return WorkContextDetail{}, err
		}
	}
	projectID := stringValue(session.ActiveProjectID)
	if input.ProjectRef != "" {
		projectID, err = resolveProjectID(ctx, s.DB, input.ProjectRef)
		if err != nil {
			return WorkContextDetail{}, err
		}
	}
	runtimeNodeID := stringValue(session.RuntimeNodeID)
	if input.RuntimeNodeRef != "" {
		runtimeNodeID, err = resolveNodeID(ctx, s.DB, input.RuntimeNodeRef)
		if err != nil {
			return WorkContextDetail{}, err
		}
	}
	homeNodeID := stringValue(session.HomeNodeID)
	if input.HomeNodeRef != "" {
		homeNodeID, err = resolveNodeID(ctx, s.DB, input.HomeNodeRef)
		if err != nil {
			return WorkContextDetail{}, err
		}
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return WorkContextDetail{}, err
	}
	defer tx.Rollback()

	workContextID := ids.NewAgentWorkContextID()
	workContextKey := input.WorkContextKey
	if workContextKey == "" {
		workContextKey = workContextID
	}
	workContext, err := scanWorkContext(tx.QueryRowContext(ctx, workContextSelectSQL(`
		INSERT INTO agents.agent_work_contexts (
			agent_work_context_id, work_context_key, agent_access_session_id,
			actor_id, created_by_actor_id, objective, current_scope_id,
			active_project_id, runtime_node_id, home_node_id, status, metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, ''),
		        nullif($9, ''), nullif($10, ''), $11, $12)
	`),
		workContextID,
		workContextKey,
		session.AgentAccessSessionID,
		session.ActorID,
		req.ActorID,
		input.Objective,
		scopeID,
		projectID,
		runtimeNodeID,
		homeNodeID,
		WorkContextStatusActive,
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return WorkContextDetail{}, err
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentWorkContextCreated,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(workContext.CurrentScopeID),
		TargetKind:      "agent_work_context",
		TargetID:        workContext.AgentWorkContextID,
		Status:          workContext.Status,
		Result:          "created",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"agent_work_context_id":   workContext.AgentWorkContextID,
			"agent_access_session_id": workContext.AgentAccessSessionID,
			"actor_id":                workContext.ActorID,
			"objective":               workContext.Objective,
		},
	}); err != nil {
		return WorkContextDetail{}, err
	}

	toolViewDetail, err := s.buildToolViewTx(ctx, tx, req, session, workContext)
	if err != nil {
		return WorkContextDetail{}, err
	}
	workContext, err = scanWorkContext(tx.QueryRowContext(ctx, workContextSelectSQL(`
		UPDATE agents.agent_work_contexts
		SET active_tool_view_id = $2, updated_at = now()
		WHERE agent_work_context_id = $1
	`), workContext.AgentWorkContextID, toolViewDetail.ToolView.ToolViewID))
	if err != nil {
		return WorkContextDetail{}, err
	}

	if err := tx.Commit(); err != nil {
		return WorkContextDetail{}, err
	}
	return WorkContextDetail{
		WorkContext:   workContext,
		AccessSession: session,
		ToolView:      &toolViewDetail,
	}, nil
}

func (s Service) ListWorkContexts(ctx context.Context, filter WorkContextFilter) ([]WorkContext, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	if filter.Status != "" && !ValidWorkContextStatus(filter.Status) {
		return nil, fmt.Errorf("unsupported work context status filter: %s", filter.Status)
	}

	query := workContextSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if filter.ActorRef != "" {
		actor, err := resolveActor(ctx, s.DB, filter.ActorRef)
		if err != nil {
			return nil, err
		}
		add("actor_id =", actor.ID)
	}
	if filter.AccessSessionRef != "" {
		sessionID, err := resolveAccessSessionID(ctx, s.DB, filter.AccessSessionRef)
		if err != nil {
			return nil, err
		}
		add("agent_access_session_id =", sessionID)
	}
	if filter.Status != "" {
		add("status =", filter.Status)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contexts := []WorkContext{}
	for rows.Next() {
		workContext, err := scanWorkContext(rows)
		if err != nil {
			return nil, err
		}
		contexts = append(contexts, workContext)
	}
	return contexts, rows.Err()
}

func (s Service) GetWorkContext(ctx context.Context, ref string) (WorkContextDetail, error) {
	workContext, err := getWorkContext(ctx, s.DB, ref)
	if err != nil {
		return WorkContextDetail{}, err
	}
	session, err := getAccessSession(ctx, s.DB, workContext.AgentAccessSessionID)
	if err != nil {
		return WorkContextDetail{}, err
	}
	var detail *ToolViewDetail
	if workContext.ActiveToolViewID != nil && *workContext.ActiveToolViewID != "" {
		toolViewDetail, err := s.GetToolView(ctx, *workContext.ActiveToolViewID)
		if err != nil {
			return WorkContextDetail{}, err
		}
		detail = &toolViewDetail
	}
	return WorkContextDetail{WorkContext: workContext, AccessSession: session, ToolView: detail}, nil
}

func (s Service) GetToolView(ctx context.Context, ref string) (ToolViewDetail, error) {
	toolView, err := getToolView(ctx, s.DB, ref)
	if err != nil {
		return ToolViewDetail{}, err
	}
	entries, err := listToolViewEntries(ctx, s.DB, toolView.ToolViewID)
	if err != nil {
		return ToolViewDetail{}, err
	}
	return ToolViewDetail{ToolView: toolView, Entries: entries}, nil
}

func (s Service) buildToolViewTx(ctx context.Context, tx *sql.Tx, req requestctx.Context, session AccessSession, workContext WorkContext) (ToolViewDetail, error) {
	maxEntries := DefaultToolViewMaxEntries
	toolViewID := ids.NewToolViewID()
	sourceHash := toolViewSourceHash(session, workContext)
	version := 1
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version), 0) + 1
		FROM agents.tool_views
		WHERE agent_work_context_id = $1
	`, workContext.AgentWorkContextID).Scan(&version); err != nil {
		return ToolViewDetail{}, err
	}

	toolView, err := scanToolView(tx.QueryRowContext(ctx, toolViewSelectSQL(`
		INSERT INTO agents.tool_views (
			tool_view_id, agent_access_session_id, agent_work_context_id, actor_id,
			runtime_node_id, home_node_id, current_scope_id, active_project_id,
			version, status, max_entries, schema_budget, direct_capability_schema_budget,
			source_hash, metadata
		)
		VALUES ($1, $2, $3, $4, nullif($5, ''), nullif($6, ''), nullif($7, ''),
		        nullif($8, ''), $9, $10, $11, $12, $13, $14, $15)
	`),
		toolViewID,
		session.AgentAccessSessionID,
		workContext.AgentWorkContextID,
		workContext.ActorID,
		stringValue(workContext.RuntimeNodeID),
		stringValue(workContext.HomeNodeID),
		stringValue(workContext.CurrentScopeID),
		stringValue(workContext.ActiveProjectID),
		version,
		ToolViewStatusActive,
		maxEntries,
		DefaultToolViewSchemaBudget,
		DefaultToolViewDirectCapabilitySchemaLimit,
		sourceHash,
		json.RawMessage(`{"builder":"slice_16_part_1"}`),
	))
	if err != nil {
		return ToolViewDetail{}, err
	}

	entries := []ToolViewEntry{}
	for _, spec := range operatingToolSpecs() {
		entry, err := insertToolViewEntryTx(ctx, tx, toolView.ToolViewID, toolViewEntryInsert{
			EntryKind:        EntryKindOperatingTool,
			ToolName:         spec.Name,
			VisibilityState:  VisibilityVisible,
			SourceLayer:      SourceLayerOperating,
			ReasonCode:       "permanent_operating_tool",
			DisplayName:      spec.DisplayName,
			Description:      spec.Description,
			ApprovalHint:     "none",
			AvailabilityHint: "local",
			CompactMetadata:  json.RawMessage(`{"operating_tool":true}`),
			SchemaSummary:    schemaSummary(spec.InputSchema, spec.OutputSchema),
		})
		if err != nil {
			return ToolViewDetail{}, err
		}
		entries = append(entries, entry)
	}

	remaining := maxEntries - len(entries)
	if remaining > 0 {
		candidates, err := listInitialCapabilityCandidates(ctx, tx, remaining*2)
		if err != nil {
			return ToolViewDetail{}, err
		}
		for _, candidate := range candidates {
			if len(entries) >= maxEntries {
				break
			}
			authLevel, err := actorAuthorizationLevel(ctx, tx, workContext.ActorID, candidate.TargetNodeID)
			if err != nil {
				return ToolViewDetail{}, err
			}
			if authLevel == nil {
				continue
			}
			visibility := VisibilityRequestable
			reason := "actor_can_request_with_approval"
			approvalHint := "approval_required"
			if *authLevel >= candidate.ExecutionAuthorizationLevel {
				visibility = VisibilityVisible
				reason = "actor_authorized_on_target_node"
				approvalHint = "none"
			}
			entry, err := insertToolViewEntryTx(ctx, tx, toolView.ToolViewID, toolViewEntryInsert{
				EntryKind:                   EntryKindCapability,
				ToolName:                    capabilityToolName(candidate.CompactAddress),
				VisibilityState:             visibility,
				SourceLayer:                 SourceLayerCurrentScope,
				ReasonCode:                  reason,
				CapabilityEndpointID:        candidate.EndpointID,
				CapabilityAddress:           candidate.CompactAddress,
				ProviderID:                  candidate.ProviderID,
				ProviderAddress:             candidate.ProviderAddress,
				TargetNodeID:                candidate.TargetNodeID,
				DisplayName:                 candidate.DisplayName,
				Description:                 candidate.Description,
				RiskLevel:                   candidate.RiskLevel,
				ExecutionAuthorizationLevel: candidate.ExecutionAuthorizationLevel,
				ActorAuthorizationLevel:     authLevel,
				ApprovalHint:                approvalHint,
				AvailabilityHint:            candidate.ProviderHealth,
				MatchedUseSummary:           candidate.Description,
				CompactMetadata:             capabilityCompactMetadata(candidate),
				SchemaSummary:               json.RawMessage(`{"input_schema_available":true,"output_schema_available":true}`),
			})
			if err != nil {
				return ToolViewDetail{}, err
			}
			entries = append(entries, entry)
		}
	}

	if _, err := events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       events.TypeAgentToolViewCreated,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         stringValue(workContext.CurrentScopeID),
		TargetKind:      "tool_view",
		TargetID:        toolView.ToolViewID,
		Status:          toolView.Status,
		Result:          "created",
		VisibilityClass: "internal",
		Payload: map[string]any{
			"tool_view_id":          toolView.ToolViewID,
			"agent_work_context_id": workContext.AgentWorkContextID,
			"entry_count":           len(entries),
			"max_entries":           toolView.MaxEntries,
		},
	}); err != nil {
		return ToolViewDetail{}, err
	}

	return ToolViewDetail{ToolView: toolView, Entries: entries}, nil
}

type toolViewEntryInsert struct {
	EntryKind                   string
	ToolName                    string
	VisibilityState             string
	SourceLayer                 string
	ReasonCode                  string
	CapabilityEndpointID        string
	CapabilityAddress           string
	ProviderID                  string
	ProviderAddress             string
	TargetNodeID                string
	DisplayName                 string
	Description                 string
	RiskLevel                   string
	ExecutionAuthorizationLevel int
	ActorAuthorizationLevel     *int
	ApprovalHint                string
	AvailabilityHint            string
	MatchedUseSummary           string
	CompactMetadata             json.RawMessage
	SchemaSummary               json.RawMessage
}

func insertToolViewEntryTx(ctx context.Context, tx *sql.Tx, toolViewID string, input toolViewEntryInsert) (ToolViewEntry, error) {
	var executionAuth any
	if input.ExecutionAuthorizationLevel > 0 {
		executionAuth = input.ExecutionAuthorizationLevel
	}
	return scanToolViewEntry(tx.QueryRowContext(ctx, toolViewEntrySelectSQL(`
		INSERT INTO agents.tool_view_entries (
			tool_view_entry_id, tool_view_id, entry_kind, tool_name, visibility_state,
			source_layer, reason_code, capability_endpoint_id, capability_address,
			provider_id, provider_address, target_node_id, display_name, description,
			risk_level, execution_authorization_level, actor_authorization_level,
			approval_hint, availability_hint, matched_use_summary,
			compact_metadata, schema_summary_json
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, nullif($8, ''), $9, nullif($10, ''),
		        $11, nullif($12, ''), $13, $14, nullif($15, ''), $16, $17, $18,
		        $19, $20, $21, $22)
	`),
		ids.NewToolViewEntryID(),
		toolViewID,
		input.EntryKind,
		input.ToolName,
		input.VisibilityState,
		input.SourceLayer,
		input.ReasonCode,
		input.CapabilityEndpointID,
		input.CapabilityAddress,
		input.ProviderID,
		input.ProviderAddress,
		input.TargetNodeID,
		input.DisplayName,
		input.Description,
		input.RiskLevel,
		executionAuth,
		input.ActorAuthorizationLevel,
		input.ApprovalHint,
		input.AvailabilityHint,
		input.MatchedUseSummary,
		objectOrDefault(input.CompactMetadata),
		objectOrDefault(input.SchemaSummary),
	))
}

func normalizeCreateAccessSessionInput(input CreateAccessSessionInput) CreateAccessSessionInput {
	input.AccessSessionKey = strings.TrimSpace(input.AccessSessionKey)
	input.ActorRef = strings.TrimSpace(input.ActorRef)
	input.OriginKind = defaultString(strings.TrimSpace(input.OriginKind), OriginKindLocalCLI)
	input.OriginNodeRef = strings.TrimSpace(input.OriginNodeRef)
	input.OriginClientID = strings.TrimSpace(input.OriginClientID)
	input.RuntimeNodeRef = strings.TrimSpace(input.RuntimeNodeRef)
	input.HomeNodeRef = strings.TrimSpace(input.HomeNodeRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.ProjectRef = strings.TrimSpace(input.ProjectRef)
	return input
}

func normalizeCreateWorkContextInput(input CreateWorkContextInput) CreateWorkContextInput {
	input.WorkContextKey = strings.TrimSpace(input.WorkContextKey)
	input.AccessSessionRef = strings.TrimSpace(input.AccessSessionRef)
	input.Objective = strings.TrimSpace(input.Objective)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.ProjectRef = strings.TrimSpace(input.ProjectRef)
	input.RuntimeNodeRef = strings.TrimSpace(input.RuntimeNodeRef)
	input.HomeNodeRef = strings.TrimSpace(input.HomeNodeRef)
	return input
}

func listInitialCapabilityCandidates(ctx context.Context, q queryer, limit int) ([]capabilityToolCandidate, error) {
	if limit <= 0 {
		return []capabilityToolCandidate{}, nil
	}
	if limit > 80 {
		limit = 80
	}
	rows, err := q.QueryContext(ctx, `
		SELECT
			e.capability_endpoint_id,
			e.compact_address,
			e.provider_id,
			p.compact_address,
			p.node_id,
			COALESCE(h.health_status, 'unknown'),
			c.namespace || '.' || c.name,
			c.display_name,
			c.description,
			e.form,
			e.risk_level,
			e.execution_authorization_level,
			e.metadata
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN capabilities.capability_classes c ON c.capability_class_id = e.capability_class_id
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
		WHERE e.status = 'active'
		  AND p.status = 'active'
		ORDER BY
			CASE WHEN p.compact_address LIKE 'main@%' THEN 0 ELSE 1 END,
			e.execution_authorization_level,
			e.compact_address
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := []capabilityToolCandidate{}
	for rows.Next() {
		var candidate capabilityToolCandidate
		if err := rows.Scan(
			&candidate.EndpointID,
			&candidate.CompactAddress,
			&candidate.ProviderID,
			&candidate.ProviderAddress,
			&candidate.TargetNodeID,
			&candidate.ProviderHealth,
			&candidate.ClassName,
			&candidate.DisplayName,
			&candidate.Description,
			&candidate.Form,
			&candidate.RiskLevel,
			&candidate.ExecutionAuthorizationLevel,
			&candidate.Metadata,
		); err != nil {
			return nil, err
		}
		candidate.Metadata = jsonOrDefault(candidate.Metadata, `{}`)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func actorAuthorizationLevel(ctx context.Context, q queryer, actorID, nodeID string) (*int, error) {
	var level int
	err := q.QueryRowContext(ctx, `
		SELECT authorization_level
		FROM identity.actor_node_authorizations
		WHERE actor_id = $1
		  AND node_id = $2
		  AND status = 'active'
		  AND (expires_at IS NULL OR expires_at > now())
	`, actorID, nodeID).Scan(&level)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &level, nil
}

func resolveActor(ctx context.Context, q queryer, ref string) (actorRecord, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return actorRecord{}, fmt.Errorf("actor ref is required")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT actor_id, actor_key, actor_kind, status, home_node_id, default_scope_id
		FROM identity.actors
		WHERE actor_id = $1 OR actor_key = $1
		ORDER BY actor_id
		LIMIT 2
	`, ref)
	if err != nil {
		return actorRecord{}, err
	}
	defer rows.Close()

	records := []actorRecord{}
	for rows.Next() {
		var record actorRecord
		var homeNode sql.NullString
		var defaultScope sql.NullString
		if err := rows.Scan(&record.ID, &record.Key, &record.Kind, &record.Status, &homeNode, &defaultScope); err != nil {
			return actorRecord{}, err
		}
		record.HomeNodeID = nullableString(homeNode)
		record.DefaultScopeID = nullableString(defaultScope)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return actorRecord{}, err
	}
	if len(records) == 0 {
		return actorRecord{}, sql.ErrNoRows
	}
	if len(records) > 1 {
		return actorRecord{}, fmt.Errorf("actor ref is ambiguous: %s", ref)
	}
	return records[0], nil
}

func resolveNodeID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
		ORDER BY node_id
		LIMIT 2
	`, "node", ref)
}

func resolveScopeID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
		ORDER BY scope_id
		LIMIT 2
	`, "scope", ref)
}

func resolveProjectID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT project_id
		FROM projects.projects
		WHERE project_id = $1 OR slug = $1
		ORDER BY project_id
		LIMIT 2
	`, "project", ref)
}

func resolveAccessSessionID(ctx context.Context, q queryer, ref string) (string, error) {
	return resolveSingleID(ctx, q, `
		SELECT agent_access_session_id
		FROM agents.agent_access_sessions
		WHERE agent_access_session_id = $1 OR access_session_key = $1
		ORDER BY agent_access_session_id
		LIMIT 2
	`, "agent access session", ref)
}

func resolveSingleID(ctx context.Context, q queryer, query, label, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%s ref is required", label)
	}
	rows, err := q.QueryContext(ctx, query, ref)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", sql.ErrNoRows
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("%s ref is ambiguous: %s", label, ref)
	}
	return ids[0], nil
}

func getAccessSession(ctx context.Context, q queryer, ref string) (AccessSession, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return AccessSession{}, fmt.Errorf("access session ref is required")
	}
	return scanAccessSession(q.QueryRowContext(ctx, accessSessionSelectSQL()+`
		WHERE agent_access_session_id = $1 OR access_session_key = $1
	`, ref))
}

func getWorkContext(ctx context.Context, q queryer, ref string) (WorkContext, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return WorkContext{}, fmt.Errorf("work context ref is required")
	}
	return scanWorkContext(q.QueryRowContext(ctx, workContextSelectSQL()+`
		WHERE agent_work_context_id = $1 OR work_context_key = $1
	`, ref))
}

func getToolView(ctx context.Context, q queryer, ref string) (ToolView, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ToolView{}, fmt.Errorf("tool view ref is required")
	}
	return scanToolView(q.QueryRowContext(ctx, toolViewSelectSQL()+`
		WHERE tool_view_id = $1
	`, ref))
}

func listToolViewEntries(ctx context.Context, q queryer, toolViewID string) ([]ToolViewEntry, error) {
	rows, err := q.QueryContext(ctx, toolViewEntrySelectSQL()+`
		WHERE tool_view_id = $1
		ORDER BY
			CASE entry_kind WHEN 'operating_tool' THEN 0 ELSE 1 END,
			created_at,
			tool_name
	`, toolViewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []ToolViewEntry{}
	for rows.Next() {
		entry, err := scanToolViewEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func accessSessionSelectSQL(prefix ...string) string {
	columns := `
		agent_access_session_id, access_session_key, actor_id, created_by_actor_id,
		origin_kind, origin_node_id, origin_client_id, runtime_node_id,
		home_node_id, current_scope_id, active_project_id, status,
		created_at, updated_at, expires_at, closed_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.agent_access_sessions`
}

func workContextSelectSQL(prefix ...string) string {
	columns := `
		agent_work_context_id, work_context_key, agent_access_session_id,
		actor_id, created_by_actor_id, objective, current_scope_id,
		active_project_id, runtime_node_id, home_node_id, active_tool_view_id,
		status, created_at, updated_at, closed_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.agent_work_contexts`
}

func toolViewSelectSQL(prefix ...string) string {
	columns := `
		tool_view_id, agent_access_session_id, agent_work_context_id, actor_id,
		runtime_node_id, home_node_id, current_scope_id, active_project_id,
		version, status, max_entries, schema_budget, direct_capability_schema_budget,
		source_hash, created_at, invalidated_at, metadata
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.tool_views`
}

func toolViewEntrySelectSQL(prefix ...string) string {
	columns := `
		tool_view_entry_id, tool_view_id, entry_kind, tool_name, visibility_state,
		source_layer, reason_code, capability_endpoint_id, capability_address,
		provider_id, provider_address, target_node_id, display_name, description,
		risk_level, execution_authorization_level, actor_authorization_level,
		approval_hint, availability_hint, matched_use_summary, compact_metadata,
		schema_summary_json, created_at
	`
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + ` RETURNING ` + columns
	}
	return `SELECT ` + columns + ` FROM agents.tool_view_entries`
}

func scanAccessSession(scanner rowScanner) (AccessSession, error) {
	var session AccessSession
	var originNode, runtimeNode, homeNode, currentScope, activeProject sql.NullString
	var expiresAt, closedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&session.AgentAccessSessionID,
		&session.AccessSessionKey,
		&session.ActorID,
		&session.CreatedByActorID,
		&session.OriginKind,
		&originNode,
		&session.OriginClientID,
		&runtimeNode,
		&homeNode,
		&currentScope,
		&activeProject,
		&session.Status,
		&session.CreatedAt,
		&session.UpdatedAt,
		&expiresAt,
		&closedAt,
		&metadata,
	); err != nil {
		return AccessSession{}, err
	}
	session.OriginNodeID = nullableString(originNode)
	session.RuntimeNodeID = nullableString(runtimeNode)
	session.HomeNodeID = nullableString(homeNode)
	session.CurrentScopeID = nullableString(currentScope)
	session.ActiveProjectID = nullableString(activeProject)
	session.ExpiresAt = nullableTime(expiresAt)
	session.ClosedAt = nullableTime(closedAt)
	session.Metadata = jsonOrDefault(metadata, `{}`)
	return session, nil
}

func scanWorkContext(scanner rowScanner) (WorkContext, error) {
	var workContext WorkContext
	var currentScope, activeProject, runtimeNode, homeNode, activeToolView sql.NullString
	var closedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&workContext.AgentWorkContextID,
		&workContext.WorkContextKey,
		&workContext.AgentAccessSessionID,
		&workContext.ActorID,
		&workContext.CreatedByActorID,
		&workContext.Objective,
		&currentScope,
		&activeProject,
		&runtimeNode,
		&homeNode,
		&activeToolView,
		&workContext.Status,
		&workContext.CreatedAt,
		&workContext.UpdatedAt,
		&closedAt,
		&metadata,
	); err != nil {
		return WorkContext{}, err
	}
	workContext.CurrentScopeID = nullableString(currentScope)
	workContext.ActiveProjectID = nullableString(activeProject)
	workContext.RuntimeNodeID = nullableString(runtimeNode)
	workContext.HomeNodeID = nullableString(homeNode)
	workContext.ActiveToolViewID = nullableString(activeToolView)
	workContext.ClosedAt = nullableTime(closedAt)
	workContext.Metadata = jsonOrDefault(metadata, `{}`)
	return workContext, nil
}

func scanToolView(scanner rowScanner) (ToolView, error) {
	var toolView ToolView
	var sessionID, workContextID, runtimeNode, homeNode, currentScope, activeProject sql.NullString
	var invalidatedAt sql.NullTime
	var metadata []byte
	if err := scanner.Scan(
		&toolView.ToolViewID,
		&sessionID,
		&workContextID,
		&toolView.ActorID,
		&runtimeNode,
		&homeNode,
		&currentScope,
		&activeProject,
		&toolView.Version,
		&toolView.Status,
		&toolView.MaxEntries,
		&toolView.SchemaBudget,
		&toolView.DirectCapabilitySchemaBudget,
		&toolView.SourceHash,
		&toolView.CreatedAt,
		&invalidatedAt,
		&metadata,
	); err != nil {
		return ToolView{}, err
	}
	toolView.AgentAccessSessionID = nullableString(sessionID)
	toolView.AgentWorkContextID = nullableString(workContextID)
	toolView.RuntimeNodeID = nullableString(runtimeNode)
	toolView.HomeNodeID = nullableString(homeNode)
	toolView.CurrentScopeID = nullableString(currentScope)
	toolView.ActiveProjectID = nullableString(activeProject)
	toolView.InvalidatedAt = nullableTime(invalidatedAt)
	toolView.Metadata = jsonOrDefault(metadata, `{}`)
	return toolView, nil
}

func scanToolViewEntry(scanner rowScanner) (ToolViewEntry, error) {
	var entry ToolViewEntry
	var endpointID, providerID, targetNode, riskLevel sql.NullString
	var executionAuth, actorAuth sql.NullInt64
	var compactMetadata, schemaSummary []byte
	if err := scanner.Scan(
		&entry.ToolViewEntryID,
		&entry.ToolViewID,
		&entry.EntryKind,
		&entry.ToolName,
		&entry.VisibilityState,
		&entry.SourceLayer,
		&entry.ReasonCode,
		&endpointID,
		&entry.CapabilityAddress,
		&providerID,
		&entry.ProviderAddress,
		&targetNode,
		&entry.DisplayName,
		&entry.Description,
		&riskLevel,
		&executionAuth,
		&actorAuth,
		&entry.ApprovalHint,
		&entry.AvailabilityHint,
		&entry.MatchedUseSummary,
		&compactMetadata,
		&schemaSummary,
		&entry.CreatedAt,
	); err != nil {
		return ToolViewEntry{}, err
	}
	entry.CapabilityEndpointID = nullableString(endpointID)
	entry.ProviderID = nullableString(providerID)
	entry.TargetNodeID = nullableString(targetNode)
	entry.RiskLevel = nullableString(riskLevel)
	if executionAuth.Valid {
		v := int(executionAuth.Int64)
		entry.ExecutionAuthorizationLevel = &v
	}
	if actorAuth.Valid {
		v := int(actorAuth.Int64)
		entry.ActorAuthorizationLevel = &v
	}
	entry.CompactMetadata = jsonOrDefault(compactMetadata, `{}`)
	entry.SchemaSummaryJSON = jsonOrDefault(schemaSummary, `{}`)
	return entry, nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func nullableString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	out := value.String
	return &out
}

func nullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func jsonOrDefault(raw []byte, fallback string) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func validateObjectJSON(raw json.RawMessage, name string) error {
	raw = objectOrDefault(raw)
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s must be valid JSON: %w", name, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}

func schemaSummary(input, output json.RawMessage) json.RawMessage {
	summary := map[string]any{
		"input_schema_available":  len(strings.TrimSpace(string(input))) > 0,
		"output_schema_available": len(strings.TrimSpace(string(output))) > 0,
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func capabilityCompactMetadata(candidate capabilityToolCandidate) json.RawMessage {
	raw, err := json.Marshal(map[string]any{
		"class_name":      candidate.ClassName,
		"form":            candidate.Form,
		"provider_health": candidate.ProviderHealth,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func toolViewSourceHash(session AccessSession, workContext WorkContext) string {
	h := sha256.New()
	_, _ = h.Write([]byte(session.AgentAccessSessionID))
	_, _ = h.Write([]byte(workContext.AgentWorkContextID))
	_, _ = h.Write([]byte(workContext.ActorID))
	_, _ = h.Write([]byte(stringValue(workContext.CurrentScopeID)))
	_, _ = h.Write([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

var nonToolNameChar = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func capabilityToolName(address string) string {
	name := strings.ToLower(strings.TrimSpace(address))
	name = nonToolNameChar.ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return "capability"
	}
	return "cap_" + name
}
