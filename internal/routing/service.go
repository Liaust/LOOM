package routing

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/policy"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

type Service struct {
	DB       *sql.DB
	Policy   policy.Service
	Registry *ProviderRuntimeRegistry
}

func NewService(db *sql.DB) Service {
	return Service{
		DB:       db,
		Policy:   policy.NewService(db),
		Registry: NewProviderRuntimeRegistry(),
	}
}

func NewServiceWithRuntime(db *sql.DB, policyService policy.Service, registry *ProviderRuntimeRegistry) Service {
	if registry == nil {
		registry = NewProviderRuntimeRegistry()
	}
	return Service{
		DB:       db,
		Policy:   policyService,
		Registry: registry,
	}
}

func (s Service) ListRoutes(ctx context.Context, filter RouteFilter) ([]Route, error) {
	if filter.Limit <= 0 || filter.Limit > maxListLimit {
		filter.Limit = defaultListLimit
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidRouteStatus(status) {
		return nil, fmt.Errorf("unsupported route status filter: %s", status)
	}

	query := routeSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.ActorRef); ref != "" {
		id, err := resolveActorID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("actor_id =", id)
	}
	if ref := strings.TrimSpace(filter.OriginNodeRef); ref != "" {
		id, err := resolveNodeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("origin_node_id =", id)
	}
	if ref := strings.TrimSpace(filter.OriginScopeRef); ref != "" {
		id, err := resolveScopeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("origin_scope_id =", id)
	}
	if ref := strings.TrimSpace(filter.RuntimeNodeRef); ref != "" {
		id, err := resolveNodeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("runtime_node_id =", id)
	}
	if ref := strings.TrimSpace(filter.TargetNodeRef); ref != "" {
		id, err := resolveNodeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("target_node_id =", id)
	}
	if ref := strings.TrimSpace(filter.ProviderRef); ref != "" {
		id, err := resolveProviderID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("provider_id =", id)
	}
	if ref := strings.TrimSpace(filter.CapabilityEndpointRef); ref != "" {
		id, err := resolveCapabilityEndpointID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("capability_endpoint_id =", id)
	}
	if ref := strings.TrimSpace(filter.PolicyDecisionRef); ref != "" {
		id, err := resolvePolicyDecisionID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("policy_decision_id =", id)
	}
	if ref := strings.TrimSpace(filter.ApprovalRef); ref != "" {
		id, err := resolveApprovalID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("approval_id =", id)
	}
	if ref := strings.TrimSpace(filter.GrantRef); ref != "" {
		id, err := resolveGrantID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("grant_id =", id)
	}
	if ref := strings.TrimSpace(filter.JobRef); ref != "" {
		add("job_id =", ref)
	}
	if correlationID := strings.TrimSpace(filter.CorrelationID); correlationID != "" {
		add("correlation_id =", correlationID)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	routes := []Route{}
	for rows.Next() {
		route, err := scanRoute(rows)
		if err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	return routes, rows.Err()
}

func (s Service) GetRoute(ctx context.Context, ref string) (Route, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Route{}, fmt.Errorf("route ref is required")
	}
	row := s.DB.QueryRowContext(ctx, routeSelectSQL()+`
		WHERE route_id = $1 OR route_key = $1
	`, ref)
	return scanRoute(row)
}

func (s Service) ListCapabilityCalls(ctx context.Context, filter CapabilityCallFilter) ([]CapabilityCall, error) {
	if filter.Limit <= 0 || filter.Limit > maxListLimit {
		filter.Limit = defaultListLimit
	}
	if status := strings.TrimSpace(filter.Status); status != "" && !ValidCapabilityCallStatus(status) {
		return nil, fmt.Errorf("unsupported capability call status filter: %s", status)
	}

	query := capabilityCallSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if status := strings.TrimSpace(filter.Status); status != "" {
		add("status =", status)
	}
	if ref := strings.TrimSpace(filter.ActorRef); ref != "" {
		id, err := resolveActorID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("actor_id =", id)
	}
	if ref := strings.TrimSpace(filter.OriginNodeRef); ref != "" {
		id, err := resolveNodeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("origin_node_id =", id)
	}
	if ref := strings.TrimSpace(filter.ScopeRef); ref != "" {
		id, err := resolveScopeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("scope_id =", id)
	}
	if ref := strings.TrimSpace(filter.TargetNodeRef); ref != "" {
		id, err := resolveNodeID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("target_node_id =", id)
	}
	if ref := strings.TrimSpace(filter.ProviderRef); ref != "" {
		id, err := resolveProviderID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("provider_id =", id)
	}
	if ref := strings.TrimSpace(filter.CapabilityEndpointRef); ref != "" {
		id, err := resolveCapabilityEndpointID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("capability_endpoint_id =", id)
	}
	if ref := strings.TrimSpace(filter.PolicyDecisionRef); ref != "" {
		id, err := resolvePolicyDecisionID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("policy_decision_id =", id)
	}
	if ref := strings.TrimSpace(filter.ApprovalRef); ref != "" {
		id, err := resolveApprovalID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("approval_id =", id)
	}
	if ref := strings.TrimSpace(filter.GrantRef); ref != "" {
		id, err := resolveGrantID(ctx, s.DB, ref)
		if err != nil {
			return nil, err
		}
		add("grant_id =", id)
	}
	if ref := strings.TrimSpace(filter.JobRef); ref != "" {
		add("job_id =", ref)
	}
	if correlationID := strings.TrimSpace(filter.CorrelationID); correlationID != "" {
		add("correlation_id =", correlationID)
	}
	if idempotencyKey := strings.TrimSpace(filter.IdempotencyKey); idempotencyKey != "" {
		add("idempotency_key =", idempotencyKey)
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	calls := []CapabilityCall{}
	for rows.Next() {
		call, err := scanCapabilityCall(rows)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func (s Service) GetCapabilityCall(ctx context.Context, ref string) (CapabilityCall, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return CapabilityCall{}, fmt.Errorf("capability call ref is required")
	}
	row := s.DB.QueryRowContext(ctx, capabilityCallSelectSQL()+`
		WHERE capability_call_id = $1 OR capability_call_key = $1
	`, ref)
	return scanCapabilityCall(row)
}

func routeSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		RETURNING route_id, route_key, correlation_id, actor_id, origin_node_id,
		          origin_scope_id, runtime_node_id, target_node_id, provider_id,
		          capability_endpoint_id, capability_class_id, policy_decision_id,
		          approval_id, grant_id, job_id, route_kind, execution_mode,
		          selected_path_json, request_summary_json, result_target_json,
		          status, failure_code, failure_message, created_at, updated_at,
		          authorized_at, dispatched_at, started_at, completed_at, failed_at,
		          metadata
		`
	}
	return `
		SELECT route_id, route_key, correlation_id, actor_id, origin_node_id,
		       origin_scope_id, runtime_node_id, target_node_id, provider_id,
		       capability_endpoint_id, capability_class_id, policy_decision_id,
		       approval_id, grant_id, job_id, route_kind, execution_mode,
		       selected_path_json, request_summary_json, result_target_json,
		       status, failure_code, failure_message, created_at, updated_at,
		       authorized_at, dispatched_at, started_at, completed_at, failed_at,
		       metadata
		FROM routing.routes
	`
}

func capabilityCallSelectSQL(prefix ...string) string {
	if len(prefix) > 0 && strings.TrimSpace(prefix[0]) != "" {
		return prefix[0] + `
		RETURNING capability_call_id, capability_call_key, route_id, correlation_id,
		          idempotency_key, actor_id, origin_node_id, scope_id, target_node_id,
		          provider_id, capability_endpoint_id, operation, execution_mode,
		          policy_decision_id, approval_id, grant_id, job_id, status,
		          input_json, input_summary_json, result_json, result_refs_json,
		          error_code, error_message, created_at, updated_at, completed_at,
		          failed_at, metadata
		`
	}
	return `
		SELECT capability_call_id, capability_call_key, route_id, correlation_id,
		       idempotency_key, actor_id, origin_node_id, scope_id, target_node_id,
		       provider_id, capability_endpoint_id, operation, execution_mode,
		       policy_decision_id, approval_id, grant_id, job_id, status,
		       input_json, input_summary_json, result_json, result_refs_json,
		       error_code, error_message, created_at, updated_at, completed_at,
		       failed_at, metadata
		FROM routing.capability_calls
	`
}

func scanRoute(scanner rowScanner) (Route, error) {
	var route Route
	var originScopeID sql.NullString
	var runtimeNodeID sql.NullString
	var capabilityClassID sql.NullString
	var policyDecisionID sql.NullString
	var approvalID sql.NullString
	var grantID sql.NullString
	var jobID sql.NullString
	var failureCode sql.NullString
	var failureMessage sql.NullString
	var authorizedAt sql.NullTime
	var dispatchedAt sql.NullTime
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var failedAt sql.NullTime
	var selectedPath []byte
	var requestSummary []byte
	var resultTarget []byte
	var metadata []byte
	if err := scanner.Scan(
		&route.RouteID,
		&route.RouteKey,
		&route.CorrelationID,
		&route.ActorID,
		&route.OriginNodeID,
		&originScopeID,
		&runtimeNodeID,
		&route.TargetNodeID,
		&route.ProviderID,
		&route.CapabilityEndpointID,
		&capabilityClassID,
		&policyDecisionID,
		&approvalID,
		&grantID,
		&jobID,
		&route.RouteKind,
		&route.ExecutionMode,
		&selectedPath,
		&requestSummary,
		&resultTarget,
		&route.Status,
		&failureCode,
		&failureMessage,
		&route.CreatedAt,
		&route.UpdatedAt,
		&authorizedAt,
		&dispatchedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&metadata,
	); err != nil {
		return Route{}, err
	}
	route.OriginScopeID = nullStringPtr(originScopeID)
	route.RuntimeNodeID = nullStringPtr(runtimeNodeID)
	route.CapabilityClassID = nullStringPtr(capabilityClassID)
	route.PolicyDecisionID = nullStringPtr(policyDecisionID)
	route.ApprovalID = nullStringPtr(approvalID)
	route.GrantID = nullStringPtr(grantID)
	route.JobID = nullStringPtr(jobID)
	route.FailureCode = nullStringPtr(failureCode)
	route.FailureMessage = nullStringPtr(failureMessage)
	route.AuthorizedAt = nullTimePtr(authorizedAt)
	route.DispatchedAt = nullTimePtr(dispatchedAt)
	route.StartedAt = nullTimePtr(startedAt)
	route.CompletedAt = nullTimePtr(completedAt)
	route.FailedAt = nullTimePtr(failedAt)
	route.SelectedPathJSON = jsonOrDefault(selectedPath, `{}`)
	route.RequestSummaryJSON = jsonOrDefault(requestSummary, `{}`)
	route.ResultTargetJSON = jsonOrDefault(resultTarget, `{}`)
	route.Metadata = jsonOrDefault(metadata, `{}`)
	return route, nil
}

func scanCapabilityCall(scanner rowScanner) (CapabilityCall, error) {
	var call CapabilityCall
	var idempotencyKey sql.NullString
	var scopeID sql.NullString
	var policyDecisionID sql.NullString
	var approvalID sql.NullString
	var grantID sql.NullString
	var jobID sql.NullString
	var errorCode sql.NullString
	var errorMessage sql.NullString
	var completedAt sql.NullTime
	var failedAt sql.NullTime
	var inputJSON []byte
	var inputSummary []byte
	var resultJSON []byte
	var resultRefs []byte
	var metadata []byte
	if err := scanner.Scan(
		&call.CapabilityCallID,
		&call.CapabilityCallKey,
		&call.RouteID,
		&call.CorrelationID,
		&idempotencyKey,
		&call.ActorID,
		&call.OriginNodeID,
		&scopeID,
		&call.TargetNodeID,
		&call.ProviderID,
		&call.CapabilityEndpointID,
		&call.Operation,
		&call.ExecutionMode,
		&policyDecisionID,
		&approvalID,
		&grantID,
		&jobID,
		&call.Status,
		&inputJSON,
		&inputSummary,
		&resultJSON,
		&resultRefs,
		&errorCode,
		&errorMessage,
		&call.CreatedAt,
		&call.UpdatedAt,
		&completedAt,
		&failedAt,
		&metadata,
	); err != nil {
		return CapabilityCall{}, err
	}
	call.IdempotencyKey = nullStringPtr(idempotencyKey)
	call.ScopeID = nullStringPtr(scopeID)
	call.PolicyDecisionID = nullStringPtr(policyDecisionID)
	call.ApprovalID = nullStringPtr(approvalID)
	call.GrantID = nullStringPtr(grantID)
	call.JobID = nullStringPtr(jobID)
	call.ErrorCode = nullStringPtr(errorCode)
	call.ErrorMessage = nullStringPtr(errorMessage)
	call.CompletedAt = nullTimePtr(completedAt)
	call.FailedAt = nullTimePtr(failedAt)
	call.InputJSON = jsonOrDefault(inputJSON, `{}`)
	call.InputSummaryJSON = jsonOrDefault(inputSummary, `{}`)
	call.ResultJSON = jsonOrDefault(resultJSON, `{}`)
	call.ResultRefsJSON = jsonOrDefault(resultRefs, `{}`)
	call.Metadata = jsonOrDefault(metadata, `{}`)
	return call, nil
}

func resolveActorID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("actor ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT actor_id
		FROM identity.actors
		WHERE actor_id = $1 OR actor_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveNodeID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("node ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT node_id
		FROM nodes.nodes
		WHERE node_id = $1 OR node_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveScopeID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("scope ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT scope_id
		FROM scopes.scopes
		WHERE scope_id = $1 OR scope_key = $1 OR slug = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveProviderID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("provider ref is required")
	}
	rows, err := q.QueryContext(ctx, `
		SELECT provider_id
		FROM capabilities.providers
		WHERE provider_id = $1 OR compact_address = $1 OR provider_key = $1
		ORDER BY compact_address
		LIMIT 2
	`, ref)
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
		return "", fmt.Errorf("provider ref %q is ambiguous", ref)
	}
	return ids[0], nil
}

func resolveCapabilityEndpointID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("capability endpoint ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT capability_endpoint_id
		FROM capabilities.capability_endpoints
		WHERE capability_endpoint_id = $1 OR compact_address = $1
	`, ref).Scan(&id)
	return id, err
}

func resolvePolicyDecisionID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("policy decision ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT policy_decision_id
		FROM policy.decisions
		WHERE policy_decision_id = $1 OR decision_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveApprovalID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("approval ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT approval_id
		FROM policy.approvals
		WHERE approval_id = $1 OR approval_key = $1
	`, ref).Scan(&id)
	return id, err
}

func resolveGrantID(ctx context.Context, q queryer, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("grant ref is required")
	}
	var id string
	err := q.QueryRowContext(ctx, `
		SELECT grant_id
		FROM policy.grants
		WHERE grant_id = $1 OR grant_key = $1
	`, ref).Scan(&id)
	return id, err
}

func jsonOrDefault(raw []byte, fallback string) json.RawMessage {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}
