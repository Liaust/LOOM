package routing

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/serviceobservation"
)

const capabilityOperationPrefix = "capability:"

type plannedTarget struct {
	RoutePlan
	EndpointStatus          string
	ProviderStatus          string
	ProviderScopeID         string
	TargetNodeStatus        string
	ProviderHealth          string
	ProviderAvailable       string
	CapabilityForm          string
	EndpointRiskLevel       string
	AuthorizationLevel      int
	ProviderType            string
	EndpointName            string
	EndpointMetadata        json.RawMessage
	EndpointVersionStatus   string
	EndpointVersionManifest json.RawMessage
	RuntimeBindingID        string
	RuntimeBindingKind      string
	RuntimeBindingStatus    string
}

func (s Service) Call(ctx context.Context, req requestctx.Context, input CapabilityCallInput, idempotencyKey string) (CapabilityCallOutcome, error) {
	input, err := normalizeCallInput(input)
	if err != nil {
		return CapabilityCallOutcome{}, err
	}

	plan, err := s.PlanRoute(ctx, req, input)
	if err != nil {
		return CapabilityCallOutcome{}, err
	}

	route, call, eventIDs, err := s.createPlanned(ctx, req, plan, input, idempotencyKey)
	if err != nil {
		return CapabilityCallOutcome{}, err
	}

	explanation, err := s.Policy.Explain(ctx, req, policy.DecisionInput{
		Operation:             capabilityOperation(plan.CapabilityAddress),
		ActorRef:              plan.ActorID,
		OriginNodeRef:         plan.OriginNodeID,
		ScopeRef:              plan.OriginScopeID,
		CreateApprovalRequest: input.RequestApproval,
		ApprovalReason:        input.ApprovalReason,
		Metadata:              input.Metadata,
	})
	if err != nil {
		route, call, eventIDs, _ = s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, "policy.explain_failed", err.Error(), "", "", "", eventIDs)
		return outcome(route, call, eventIDs, nil), fmt.Errorf("policy explain failed: %w", err)
	}

	decision := explanation.Decision
	approvalID := pointerValue(decision.ApprovalID)
	grantID := pointerValue(decision.GrantID)
	if explanation.Approval != nil {
		approvalID = explanation.Approval.ApprovalID
	}
	if explanation.Grant != nil {
		grantID = explanation.Grant.GrantID
	}

	switch decision.Decision {
	case policy.DecisionDeny:
		route, call, eventIDs, err = s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, decision.ReasonCode, decision.SafeExplanation, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
		return outcome(route, call, eventIDs, nil), nil

	case policy.DecisionApprovalRequired:
		route, call, eventIDs, err = s.markApprovalRequired(ctx, req, route.RouteID, call.CapabilityCallID, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
		return outcome(route, call, eventIDs, nil), nil

	case policy.DecisionAllow:
		route, call, eventIDs, err = s.markAuthorized(ctx, req, route.RouteID, call.CapabilityCallID, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
	default:
		route, call, eventIDs, _ = s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, "policy.unsupported_decision", decision.Decision, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
		return outcome(route, call, eventIDs, nil), fmt.Errorf("unsupported policy decision: %s", decision.Decision)
	}

	if input.DryRun {
		result := json.RawMessage(`{"dry_run":true,"dispatched":false}`)
		route, call, eventIDs, err = s.markCompleted(ctx, req, route.RouteID, call.CapabilityCallID, result, json.RawMessage(`{}`), "", eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
		return outcome(route, call, eventIDs, &ExecutionResult{Status: CapabilityCallStatusCompleted, Result: result, ResultRefs: json.RawMessage(`{}`)}), nil
	}

	if plan.RouteKind == RouteKindRemote {
		route, call, dispatchMessageID, eventIDs, err := s.dispatchRemote(ctx, req, plan, route, call, input.Input, idempotencyKey, eventIDs)
		if err != nil {
			route, call, eventIDs, markErr := s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, "remote.dispatch_failed", err.Error(), decision.PolicyDecisionID, approvalID, grantID, eventIDs)
			if markErr != nil {
				return CapabilityCallOutcome{}, markErr
			}
			return outcome(route, call, eventIDs, nil), nil
		}
		out := outcome(route, call, eventIDs, nil)
		out.DispatchMessageID = dispatchMessageID
		return out, nil
	}

	adapter, ok := s.adapterFor(plan)
	if !ok {
		if s.Registry == nil {
			message := fmt.Sprintf("no local provider adapter registered for %s", plan.ProviderAddress)
			route, call, eventIDs, err = s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, "provider.adapter_missing", message, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
			if err != nil {
				return CapabilityCallOutcome{}, err
			}
			return outcome(route, call, eventIDs, nil), nil
		}
		adapter = NewDeclarativeRuntimeAdapter(capabilities.NewService(s.DB), s.Registry)
	}

	route, call, eventIDs, err = s.markDispatched(ctx, req, route.RouteID, call.CapabilityCallID, eventIDs)
	if err != nil {
		return CapabilityCallOutcome{}, err
	}

	execResult, err := adapter.Execute(ctx, ExecutionContext{
		RouteID:                 route.RouteID,
		CapabilityCallID:        call.CapabilityCallID,
		CorrelationID:           route.CorrelationID,
		ActorID:                 route.ActorID,
		OriginNodeID:            route.OriginNodeID,
		ScopeID:                 pointerValue(route.OriginScopeID),
		TargetNodeID:            route.TargetNodeID,
		ProviderID:              route.ProviderID,
		CapabilityEndpointID:    route.CapabilityEndpointID,
		ActiveEndpointVersionID: plan.ActiveEndpointVersionID,
		Operation:               call.Operation,
		PolicyDecisionID:        decision.PolicyDecisionID,
		GrantID:                 grantID,
	}, input.Input)
	if err != nil {
		code := "provider.execution_failed"
		message := err.Error()
		if runtimeCode, runtimeMessage, ok := RuntimeFailureCode(err); ok {
			code = runtimeCode
			message = runtimeMessage
		}
		route, call, eventIDs, markErr := s.markFailed(ctx, req, route.RouteID, call.CapabilityCallID, code, message, decision.PolicyDecisionID, approvalID, grantID, eventIDs)
		if markErr != nil {
			return CapabilityCallOutcome{}, markErr
		}
		return outcome(route, call, eventIDs, nil), nil
	}
	eventIDs = append(eventIDs, execResult.EventIDs...)
	if err := s.persistServiceManagerObservation(ctx, req, route.ProviderID, route.CapabilityEndpointID, objectOrDefault(execResult.Result), time.Now().UTC()); err != nil {
		route, call, eventIDs, markErr := s.markFailedWithResult(ctx, req, route.RouteID, call.CapabilityCallID, "provider.health_projection_failed", err.Error(), decision.PolicyDecisionID, approvalID, grantID, execResult.JobID, objectOrDefault(execResult.Result), objectOrDefault(execResult.ResultRefs), eventIDs)
		if markErr != nil {
			return CapabilityCallOutcome{}, markErr
		}
		return outcome(route, call, eventIDs, &execResult), nil
	}

	if grantID != "" {
		grant, err := s.Policy.ConsumeGrant(ctx, routeEventRequest(req, route), policy.GrantConsumeInput{
			GrantRef:         grantID,
			Reason:           "capability_call_executed",
			RouteID:          route.RouteID,
			CapabilityCallID: call.CapabilityCallID,
		})
		if err != nil {
			route, call, eventIDs, markErr := s.markFailedWithResult(ctx, req, route.RouteID, call.CapabilityCallID, "grant.consume_failed", err.Error(), decision.PolicyDecisionID, approvalID, grantID, execResult.JobID, objectOrDefault(execResult.Result), objectOrDefault(execResult.ResultRefs), eventIDs)
			if markErr != nil {
				return CapabilityCallOutcome{}, markErr
			}
			return outcome(route, call, eventIDs, nil), nil
		}
		grantID = grant.GrantID
	}

	if execResult.Status == CapabilityCallStatusFailed {
		message := "provider returned failed execution status"
		route, call, eventIDs, err = s.markFailedWithResult(ctx, req, route.RouteID, call.CapabilityCallID, "provider.execution_failed", message, decision.PolicyDecisionID, approvalID, grantID, execResult.JobID, objectOrDefault(execResult.Result), objectOrDefault(execResult.ResultRefs), eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
		return outcome(route, call, eventIDs, &execResult), nil
	}
	if execResult.Status != "" && execResult.Status != CapabilityCallStatusCompleted {
		message := fmt.Sprintf("provider returned unsupported execution status: %s", execResult.Status)
		route, call, eventIDs, err = s.markFailedWithResult(ctx, req, route.RouteID, call.CapabilityCallID, "provider.unsupported_status", message, decision.PolicyDecisionID, approvalID, grantID, execResult.JobID, objectOrDefault(execResult.Result), objectOrDefault(execResult.ResultRefs), eventIDs)
		if err != nil {
			return CapabilityCallOutcome{}, err
		}
		return outcome(route, call, eventIDs, &execResult), nil
	}

	route, call, eventIDs, err = s.markCompleted(ctx, req, route.RouteID, call.CapabilityCallID, objectOrDefault(execResult.Result), objectOrDefault(execResult.ResultRefs), execResult.JobID, eventIDs)
	if err != nil {
		return CapabilityCallOutcome{}, err
	}
	return outcome(route, call, eventIDs, &execResult), nil
}

func (s Service) PlanRoute(ctx context.Context, req requestctx.Context, input CapabilityCallInput) (RoutePlan, error) {
	target := strings.TrimSpace(input.Target)
	if target == "" {
		return RoutePlan{}, fmt.Errorf("target is required")
	}
	if strings.HasPrefix(target, capabilityOperationPrefix) {
		target = strings.TrimSpace(strings.TrimPrefix(target, capabilityOperationPrefix))
	}

	actorRef := defaultString(input.ActorRef, req.ActorID)
	originNodeRef := defaultString(input.OriginNodeRef, req.OriginNodeID)
	scopeRef := defaultString(input.ScopeRef, req.ScopeID)

	actorID, err := resolveActorID(ctx, s.DB, actorRef)
	if err != nil {
		return RoutePlan{}, fmt.Errorf("resolve actor %q: %w", actorRef, err)
	}
	originNodeID, err := resolveNodeID(ctx, s.DB, originNodeRef)
	if err != nil {
		return RoutePlan{}, fmt.Errorf("resolve origin node %q: %w", originNodeRef, err)
	}
	scopeID := ""
	if strings.TrimSpace(scopeRef) != "" {
		scopeID, err = resolveScopeID(ctx, s.DB, scopeRef)
		if err != nil {
			return RoutePlan{}, fmt.Errorf("resolve scope %q: %w", scopeRef, err)
		}
	}

	planned, err := s.resolvePlannedTarget(ctx, target)
	if err != nil {
		return RoutePlan{}, err
	}
	if planned.EndpointStatus != "active" {
		return RoutePlan{}, fmt.Errorf("capability endpoint is not active: %s", planned.EndpointStatus)
	}
	if planned.ProviderStatus != "active" {
		return RoutePlan{}, fmt.Errorf("capability provider is not active: %s", planned.ProviderStatus)
	}
	if planned.TargetNodeStatus != "active" {
		return RoutePlan{}, fmt.Errorf("target node is not active: %s", planned.TargetNodeStatus)
	}
	if !providerHealthAllowsRouting(planned.ProviderHealth, planned.ProviderAvailable) && !serviceManagementEndpointAllowsUnhealthyRouting(planned) {
		return RoutePlan{}, fmt.Errorf("provider is not routable: health=%s availability=%s", planned.ProviderHealth, planned.ProviderAvailable)
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      scopeID,
		ResourceKind: "capability_call_scope",
		ResourceRef:  target,
	}); err != nil {
		return RoutePlan{}, err
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      planned.ProviderScopeID,
		ResourceKind: "capability_provider",
		ResourceRef:  planned.ProviderAddress,
	}); err != nil {
		return RoutePlan{}, err
	}

	planned.ActorID = actorID
	planned.OriginNodeID = originNodeID
	planned.OriginScopeID = scopeID
	planned.RuntimeNodeID = planned.TargetNodeID
	planned.RouteKind = RouteKindLocal
	if planned.TargetNodeID != originNodeID || projectArchiveSystemEndpointRequiresNodeDispatch(planned) || serviceManagerNodeEndpointRequiresNodeDispatch(planned) || applicationSystemEndpointRequiresNodeDispatch(planned) {
		planned.RouteKind = RouteKindRemote
		planned.ExecutionMode = ExecutionModeImmediate
	}

	selectedPath := mustJSON(map[string]any{
		"route_kind":                 planned.RouteKind,
		"provider_id":                planned.ProviderID,
		"provider_address":           planned.ProviderAddress,
		"capability_endpoint_id":     planned.CapabilityEndpointID,
		"capability_address":         planned.CapabilityAddress,
		"active_endpoint_version_id": planned.ActiveEndpointVersionID,
		"target_node_id":             planned.TargetNodeID,
		"runtime_node_id":            planned.RuntimeNodeID,
		"execution_mode":             planned.ExecutionMode,
		"provider_health":            planned.ProviderHealth,
		"provider_availability":      planned.ProviderAvailable,
	})
	requestSummary := mustJSON(map[string]any{
		"target":           planned.CapabilityAddress,
		"operation":        capabilityOperation(planned.CapabilityAddress),
		"actor_ref":        defaultString(input.ActorRef, req.ActorKey),
		"origin_node_ref":  defaultString(input.OriginNodeRef, req.OriginNodeKey),
		"scope_ref":        defaultString(input.ScopeRef, req.ScopeKey),
		"request_approval": input.RequestApproval,
		"dry_run":          input.DryRun,
		"input_present":    string(objectOrDefault(input.Input)) != "{}",
	})

	planned.SelectedPathJSON = selectedPath
	planned.RequestSummaryJSON = requestSummary
	planned.ResultTargetJSON = objectOrDefault(input.ResultTarget)
	return planned.RoutePlan, nil
}

func (s Service) resolvePlannedTarget(ctx context.Context, target string) (plannedTarget, error) {
	var out plannedTarget
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			e.capability_endpoint_id,
			e.compact_address,
			e.capability_class_id,
			COALESCE(e.active_endpoint_version_id, ''),
			e.form,
			e.status,
			e.risk_level,
			e.execution_authorization_level,
			p.provider_id,
			p.compact_address,
			p.status,
			p.provider_type,
			p.node_id,
			p.scope_id,
			n.status,
			COALESCE(h.health_status, 'unknown'),
			COALESCE(h.availability_status, 'unknown'),
			e.endpoint_name,
			e.metadata,
			COALESCE(ev.status, ''),
			COALESCE(ev.manifest_json, '{}'::jsonb),
			COALESCE(b.runtime_binding_id, ''),
			COALESCE(b.runtime_kind, ''),
			COALESCE(b.status, '')
		FROM capabilities.capability_endpoints e
		JOIN capabilities.providers p ON p.provider_id = e.provider_id
		JOIN nodes.nodes n ON n.node_id = p.node_id
		LEFT JOIN capabilities.provider_health h ON h.provider_id = p.provider_id
		LEFT JOIN capabilities.endpoint_versions ev
			ON ev.capability_endpoint_version_id = e.active_endpoint_version_id
		LEFT JOIN capabilities.endpoint_runtime_bindings b
			ON b.capability_endpoint_version_id = e.active_endpoint_version_id
		WHERE e.capability_endpoint_id = $1 OR e.compact_address = $1
	`, target).Scan(
		&out.CapabilityEndpointID,
		&out.CapabilityAddress,
		&out.CapabilityClassID,
		&out.ActiveEndpointVersionID,
		&out.CapabilityForm,
		&out.EndpointStatus,
		&out.EndpointRiskLevel,
		&out.AuthorizationLevel,
		&out.ProviderID,
		&out.ProviderAddress,
		&out.ProviderStatus,
		&out.ProviderType,
		&out.TargetNodeID,
		&out.ProviderScopeID,
		&out.TargetNodeStatus,
		&out.ProviderHealth,
		&out.ProviderAvailable,
		&out.EndpointName,
		&out.EndpointMetadata,
		&out.EndpointVersionStatus,
		&out.EndpointVersionManifest,
		&out.RuntimeBindingID,
		&out.RuntimeBindingKind,
		&out.RuntimeBindingStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return plannedTarget{}, fmt.Errorf("capability endpoint was not found: %s", target)
	}
	if err != nil {
		return plannedTarget{}, err
	}
	out.RouteKind = RouteKindLocal
	out.ExecutionMode = executionModeForForm(out.CapabilityForm)
	return out, nil
}

func projectArchiveSystemEndpointRequiresNodeDispatch(target plannedTarget) bool {
	if target.ProviderType != capabilities.ProviderTypeSystem || target.ProviderStatus != capabilities.ProviderStatusActive ||
		target.EndpointStatus != capabilities.EndpointStatusActive || target.EndpointVersionStatus != capabilities.EndpointVersionStatusActive ||
		target.ActiveEndpointVersionID == "" || target.EndpointName != "project.archive.quiesce" {
		return false
	}
	providerAddress, err := capabilities.ParseProviderAddress(target.ProviderAddress)
	if err != nil || !strings.HasPrefix(providerAddress.ScopePath, "workspace/") || target.ProviderAddress != capabilities.NodeSystemProviderAddress(strings.TrimPrefix(providerAddress.ScopePath, "workspace/")) {
		return false
	}
	capabilityAddress, err := capabilities.ParseAddress(target.CapabilityAddress)
	if err != nil || capabilityAddress.ScopePath != providerAddress.ScopePath || capabilityAddress.ProviderKey != providerAddress.ProviderKey || capabilityAddress.CapabilityName != "project.archive.quiesce" {
		return false
	}
	var marker struct {
		Source                string `json:"source"`
		Handler               string `json:"handler"`
		Execution             string `json:"execution"`
		ExplicitAuthorization bool   `json:"explicit_authorization"`
		SliceEnabled          bool   `json:"slice_enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(target.EndpointVersionManifest))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil {
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return false
	}
	return marker.Source == "loom-node-agent" && marker.Handler == "system.project.archive.quiesce" &&
		marker.Execution == "remote_node" && marker.ExplicitAuthorization && marker.SliceEnabled
}

// Service managers execute on the authenticated owner node, including when the
// caller originates there. Remote denotes node transport; loomd has no local
// service-manager executor. Reuse the registry identity gate, not just the name.
func serviceManagerNodeEndpointRequiresNodeDispatch(target plannedTarget) bool {
	if target.ProviderStatus != capabilities.ProviderStatusActive || target.TargetNodeStatus != "active" {
		return false
	}
	_, ok := serviceManagementEndpointOperation(target)
	return ok
}

func serviceManagementEndpointAllowsUnhealthyRouting(target plannedTarget) bool {
	_, ok := serviceManagementEndpointOperation(target)
	return ok
}

func serviceManagementEndpointOperation(target plannedTarget) (string, bool) {
	if target.ProviderType != capabilities.ProviderTypeService || target.EndpointStatus != capabilities.EndpointStatusActive || target.EndpointName == "" ||
		target.ActiveEndpointVersionID == "" || target.RuntimeBindingID == "" ||
		target.EndpointVersionStatus != capabilities.EndpointVersionStatusActive ||
		target.RuntimeBindingKind != capabilities.RuntimeKindServiceManager ||
		target.RuntimeBindingStatus != capabilities.RuntimeBindingStatusActive {
		return "", false
	}
	operation := strings.TrimPrefix(target.EndpointName, "service.")
	if operation == target.EndpointName {
		return "", false
	}
	if !serviceobservation.StandardOperation(operation) {
		return "", false
	}
	var metadata struct {
		Source    string `json:"source"`
		Operation string `json:"operation"`
	}
	if json.Unmarshal(target.EndpointMetadata, &metadata) != nil || metadata.Source != "service_registry" || metadata.Operation != operation {
		return "", false
	}
	return operation, true
}

func (s Service) adapterFor(plan RoutePlan) (ProviderAdapter, bool) {
	if s.Registry == nil {
		return nil, false
	}
	if adapter, ok := s.Registry.Adapter(plan.ProviderID); ok {
		return adapter, true
	}
	return s.Registry.Adapter(plan.ProviderAddress)
}

func (s Service) createPlanned(ctx context.Context, req requestctx.Context, plan RoutePlan, input CapabilityCallInput, idempotencyKey string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}
	defer tx.Rollback()

	routeID := ids.NewRouteID()
	callID := ids.NewCapabilityCallID()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		INSERT INTO routing.routes (
			route_id, route_key, correlation_id, actor_id, origin_node_id,
			origin_scope_id, runtime_node_id, target_node_id, provider_id,
			capability_endpoint_id, capability_class_id, route_kind, execution_mode,
			selected_path_json, request_summary_json, result_target_json, status, metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, $9,
			$10, nullif($11, ''), $12, $13, $14, $15, $16, 'planned', $17
		)
	`),
		routeID,
		routeID,
		req.CorrelationID,
		plan.ActorID,
		plan.OriginNodeID,
		plan.OriginScopeID,
		plan.RuntimeNodeID,
		plan.TargetNodeID,
		plan.ProviderID,
		plan.CapabilityEndpointID,
		plan.CapabilityClassID,
		plan.RouteKind,
		plan.ExecutionMode,
		objectOrDefault(plan.SelectedPathJSON),
		objectOrDefault(plan.RequestSummaryJSON),
		objectOrDefault(plan.ResultTargetJSON),
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}

	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		INSERT INTO routing.capability_calls (
			capability_call_id, capability_call_key, route_id, correlation_id,
			idempotency_key, actor_id, origin_node_id, scope_id, target_node_id,
			provider_id, capability_endpoint_id, operation, execution_mode,
			status, input_json, input_summary_json, result_refs_json, metadata
		)
		VALUES (
			$1, $2, $3, $4, nullif($5, ''), $6, $7, nullif($8, ''), $9,
			$10, $11, $12, $13, 'planned', $14, $15, '{}'::jsonb, $16
		)
	`),
		callID,
		callID,
		route.RouteID,
		req.CorrelationID,
		strings.TrimSpace(idempotencyKey),
		plan.ActorID,
		plan.OriginNodeID,
		plan.OriginScopeID,
		plan.TargetNodeID,
		plan.ProviderID,
		plan.CapabilityEndpointID,
		capabilityOperation(plan.CapabilityAddress),
		plan.ExecutionMode,
		objectOrDefault(input.Input),
		objectOrDefault(plan.RequestSummaryJSON),
		objectOrDefault(input.Metadata),
	))
	if err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}

	eventReq := routeEventRequest(req, route)
	eventIDs := []string{}
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteCreated, "planned", "route_created", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallCreated, "planned", "capability_call_created", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}
	eventIDs = append(eventIDs, event.EventID)

	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, nil, err
	}
	return route, call, eventIDs, nil
}

func (s Service) markApprovalRequired(ctx context.Context, req requestctx.Context, routeID, callID, policyDecisionID, approvalID, grantID string, eventIDs []string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	defer tx.Rollback()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'waiting_for_approval',
		    policy_decision_id = nullif($2, ''),
		    approval_id = nullif($3, ''),
		    grant_id = nullif($4, ''),
		    updated_at = now()
		WHERE route_id = $1
	`), routeID, policyDecisionID, approvalID, grantID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'approval_required',
		    policy_decision_id = nullif($2, ''),
		    approval_id = nullif($3, ''),
		    grant_id = nullif($4, ''),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), callID, policyDecisionID, approvalID, grantID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteWaitingForApproval, route.Status, "approval_required", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallApprovalReq, call.Status, "approval_required", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	return route, call, eventIDs, nil
}

func (s Service) markAuthorized(ctx context.Context, req requestctx.Context, routeID, callID, policyDecisionID, approvalID, grantID string, eventIDs []string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	defer tx.Rollback()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'authorized',
		    policy_decision_id = nullif($2, ''),
		    approval_id = nullif($3, ''),
		    grant_id = nullif($4, ''),
		    authorized_at = COALESCE(authorized_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), routeID, policyDecisionID, approvalID, grantID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'authorized',
		    policy_decision_id = nullif($2, ''),
		    approval_id = nullif($3, ''),
		    grant_id = nullif($4, ''),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), callID, policyDecisionID, approvalID, grantID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteAuthorized, route.Status, "route_authorized", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallAuthorized, call.Status, "capability_call_authorized", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	return route, call, eventIDs, nil
}

func (s Service) markDispatched(ctx context.Context, req requestctx.Context, routeID, callID string, eventIDs []string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	defer tx.Rollback()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'dispatched',
		    dispatched_at = COALESCE(dispatched_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), routeID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'dispatched',
		    updated_at = now()
		WHERE capability_call_id = $1
	`), callID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteDispatched, route.Status, "route_dispatched", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallDispatched, call.Status, "capability_call_dispatched", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	return route, call, eventIDs, nil
}

func (s Service) markCompleted(ctx context.Context, req requestctx.Context, routeID, callID string, result, resultRefs json.RawMessage, jobID string, eventIDs []string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	defer tx.Rollback()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'completed',
		    job_id = nullif($2, ''),
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), routeID, jobID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'completed',
		    job_id = nullif($2, ''),
		    result_json = $3,
		    result_refs_json = $4,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), callID, jobID, objectOrDefault(result), objectOrDefault(resultRefs)))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteCompleted, route.Status, "route_completed", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallCompleted, call.Status, "capability_call_completed", nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	return route, call, eventIDs, nil
}

func (s Service) markFailed(ctx context.Context, req requestctx.Context, routeID, callID, code, message, policyDecisionID, approvalID, grantID string, eventIDs []string) (Route, CapabilityCall, []string, error) {
	return s.markFailedWithResult(ctx, req, routeID, callID, code, message, policyDecisionID, approvalID, grantID, "", json.RawMessage(`{}`), json.RawMessage(`{}`), eventIDs)
}

func (s Service) markFailedWithResult(ctx context.Context, req requestctx.Context, routeID, callID, code, message, policyDecisionID, approvalID, grantID, jobID string, result, resultRefs json.RawMessage, eventIDs []string) (Route, CapabilityCall, []string, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	defer tx.Rollback()
	route, err := scanRoute(tx.QueryRowContext(ctx, routeSelectSQL(`
		UPDATE routing.routes
		SET status = 'failed',
		    policy_decision_id = COALESCE(nullif($2, ''), policy_decision_id),
		    approval_id = COALESCE(nullif($3, ''), approval_id),
		    grant_id = COALESCE(nullif($4, ''), grant_id),
		    failure_code = nullif($5, ''),
		    failure_message = nullif($6, ''),
		    job_id = COALESCE(nullif($7, ''), job_id),
		    failed_at = COALESCE(failed_at, now()),
		    updated_at = now()
		WHERE route_id = $1
	`), routeID, policyDecisionID, approvalID, grantID, code, message, jobID))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	call, err := scanCapabilityCall(tx.QueryRowContext(ctx, capabilityCallSelectSQL(`
		UPDATE routing.capability_calls
		SET status = 'failed',
		    policy_decision_id = COALESCE(nullif($2, ''), policy_decision_id),
		    approval_id = COALESCE(nullif($3, ''), approval_id),
		    grant_id = COALESCE(nullif($4, ''), grant_id),
		    error_code = nullif($5, ''),
		    error_message = nullif($6, ''),
		    job_id = COALESCE(nullif($7, ''), job_id),
		    result_json = $8,
		    result_refs_json = $9,
		    failed_at = COALESCE(failed_at, now()),
		    updated_at = now()
		WHERE capability_call_id = $1
	`), callID, policyDecisionID, approvalID, grantID, code, message, jobID, objectOrDefault(result), objectOrDefault(resultRefs)))
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventReq := routeEventRequest(req, route)
	event, err := appendRouteEvent(ctx, tx, eventReq, route, events.TypeRouteFailed, route.Status, code, map[string]any{"message": message})
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	event, err = appendCallEvent(ctx, tx, eventReq, route, call, events.TypeCapabilityCallFailed, call.Status, code, map[string]any{"message": message})
	if err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	eventIDs = append(eventIDs, event.EventID)
	if err := tx.Commit(); err != nil {
		return Route{}, CapabilityCall{}, eventIDs, err
	}
	return route, call, eventIDs, nil
}

func appendRouteEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, route Route, eventType, status, result string, extra map[string]any) (events.Event, error) {
	payload := routeEventPayload(route)
	for key, value := range extra {
		payload[key] = value
	}
	return events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         pointerValue(route.OriginScopeID),
		TargetKind:      "route",
		TargetID:        route.RouteID,
		RouteID:         route.RouteID,
		JobID:           pointerValue(route.JobID),
		Status:          status,
		Result:          result,
		Payload:         payload,
		VisibilityClass: "internal",
	})
}

func appendCallEvent(ctx context.Context, tx *sql.Tx, req requestctx.Context, route Route, call CapabilityCall, eventType, status, result string, extra map[string]any) (events.Event, error) {
	payload := callEventPayload(route, call)
	for key, value := range extra {
		payload[key] = value
	}
	return events.AppendTx(ctx, tx, events.AppendInput{
		EventType:       eventType,
		EventLevel:      "audit",
		Request:         req,
		ScopeID:         pointerValue(call.ScopeID),
		TargetKind:      "capability_call",
		TargetID:        call.CapabilityCallID,
		RouteID:         route.RouteID,
		JobID:           pointerValue(call.JobID),
		Status:          status,
		Result:          result,
		Payload:         payload,
		VisibilityClass: "internal",
	})
}

func routeEventPayload(route Route) map[string]any {
	return map[string]any{
		"route_id":               route.RouteID,
		"capability_call_status": "",
		"actor_id":               route.ActorID,
		"origin_node_id":         route.OriginNodeID,
		"target_node_id":         route.TargetNodeID,
		"provider_id":            route.ProviderID,
		"capability_endpoint_id": route.CapabilityEndpointID,
		"policy_decision_id":     pointerValue(route.PolicyDecisionID),
		"approval_id":            pointerValue(route.ApprovalID),
		"grant_id":               pointerValue(route.GrantID),
		"job_id":                 pointerValue(route.JobID),
		"route_kind":             route.RouteKind,
		"execution_mode":         route.ExecutionMode,
		"status":                 route.Status,
	}
}

func callEventPayload(route Route, call CapabilityCall) map[string]any {
	return map[string]any{
		"route_id":               route.RouteID,
		"capability_call_id":     call.CapabilityCallID,
		"actor_id":               call.ActorID,
		"origin_node_id":         call.OriginNodeID,
		"target_node_id":         call.TargetNodeID,
		"provider_id":            call.ProviderID,
		"capability_endpoint_id": call.CapabilityEndpointID,
		"operation":              call.Operation,
		"policy_decision_id":     pointerValue(call.PolicyDecisionID),
		"approval_id":            pointerValue(call.ApprovalID),
		"grant_id":               pointerValue(call.GrantID),
		"job_id":                 pointerValue(call.JobID),
		"execution_mode":         call.ExecutionMode,
		"status":                 call.Status,
	}
}

func outcome(route Route, call CapabilityCall, eventIDs []string, execResult *ExecutionResult) CapabilityCallOutcome {
	out := CapabilityCallOutcome{
		Route:            route,
		CapabilityCall:   call,
		PolicyDecisionID: pointerValue(call.PolicyDecisionID),
		ApprovalID:       pointerValue(call.ApprovalID),
		GrantID:          pointerValue(call.GrantID),
		JobID:            pointerValue(call.JobID),
		Result:           objectOrDefault(call.ResultJSON),
		ResultRefs:       objectOrDefault(call.ResultRefsJSON),
		EventIDs:         eventIDs,
		Status:           call.Status,
		ErrorCode:        pointerValue(call.ErrorCode),
		ErrorMessage:     pointerValue(call.ErrorMessage),
	}
	if execResult != nil {
		out.Result = objectOrDefault(execResult.Result)
		out.ResultRefs = objectOrDefault(execResult.ResultRefs)
		if execResult.JobID != "" {
			out.JobID = execResult.JobID
		}
	}
	return out
}

func normalizeCallInput(input CapabilityCallInput) (CapabilityCallInput, error) {
	input.Target = strings.TrimSpace(input.Target)
	input.ActorRef = strings.TrimSpace(input.ActorRef)
	input.OriginNodeRef = strings.TrimSpace(input.OriginNodeRef)
	input.ScopeRef = strings.TrimSpace(input.ScopeRef)
	input.ApprovalReason = strings.TrimSpace(input.ApprovalReason)
	input.Input = objectOrDefault(input.Input)
	input.ResultTarget = objectOrDefault(input.ResultTarget)
	input.Metadata = objectOrDefault(input.Metadata)
	if input.Target == "" {
		return CapabilityCallInput{}, fmt.Errorf("target is required")
	}
	if err := ValidateCallInputJSON(input.Input); err != nil {
		return CapabilityCallInput{}, err
	}
	if err := validateJSONObject(input.ResultTarget, "result_target"); err != nil {
		return CapabilityCallInput{}, err
	}
	if err := validateJSONObject(input.Metadata, "metadata"); err != nil {
		return CapabilityCallInput{}, err
	}
	return input, nil
}

func executionModeForForm(form string) string {
	switch strings.TrimSpace(form) {
	case "job":
		return ExecutionModeJob
	case "session":
		return ExecutionModeSession
	case "stream", "subscription":
		return ExecutionModeStream
	default:
		return ExecutionModeImmediate
	}
}

func providerHealthAllowsRouting(health, availability string) bool {
	health = strings.TrimSpace(health)
	availability = strings.TrimSpace(availability)
	return (health == "ok" || health == "degraded") && (availability == "available" || availability == "limited")
}

func routeEventRequest(req requestctx.Context, route Route) requestctx.Context {
	out := req
	out.ActorID = route.ActorID
	out.OriginNodeID = route.OriginNodeID
	if route.OriginScopeID != nil {
		out.ScopeID = *route.OriginScopeID
	}
	return out
}

func capabilityOperation(address string) string {
	return capabilityOperationPrefix + strings.TrimSpace(address)
}

func objectOrDefault(raw json.RawMessage) json.RawMessage {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage(`{}`)
	}
	return raw
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return strings.TrimSpace(fallback)
	}
	return value
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func mustJSON(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func applicationSystemEndpointRequiresNodeDispatch(target plannedTarget) bool {
	if target.TargetNodeStatus != "active" || target.ProviderType != capabilities.ProviderTypeSystem || target.ProviderStatus != capabilities.ProviderStatusActive || target.EndpointStatus != capabilities.EndpointStatusActive || target.EndpointVersionStatus != capabilities.EndpointVersionStatusActive || target.ActiveEndpointVersionID == "" || target.RuntimeBindingID != "" {
		return false
	}
	valid := false
	for _, op := range []string{"inspect", "apply", "retire", "prerequisites"} {
		valid = valid || target.EndpointName == "project.application."+op
	}
	if !valid {
		return false
	}
	p, e := capabilities.ParseProviderAddress(target.ProviderAddress)
	if e != nil || !strings.HasPrefix(p.ScopePath, "workspace/") || target.ProviderAddress != capabilities.NodeSystemProviderAddress(strings.TrimPrefix(p.ScopePath, "workspace/")) {
		return false
	}
	a, e := capabilities.ParseAddress(target.CapabilityAddress)
	if e != nil || a.ScopePath != p.ScopePath || a.ProviderKey != p.ProviderKey || a.CapabilityName != target.EndpointName {
		return false
	}
	var marker struct {
		Source                string `json:"source"`
		Handler               string `json:"handler"`
		Execution             string `json:"execution"`
		ExplicitAuthorization bool   `json:"explicit_authorization"`
		SliceEnabled          bool   `json:"slice_enabled"`
	}
	d := json.NewDecoder(bytes.NewReader(target.EndpointVersionManifest))
	d.DisallowUnknownFields()
	if d.Decode(&marker) != nil {
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return false
	}
	return marker.Source == "loom-node-agent" && marker.Handler == "system."+target.EndpointName && marker.Execution == "remote_node" && marker.ExplicitAuthorization && marker.SliceEnabled
}
