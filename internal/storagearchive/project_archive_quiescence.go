package storagearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

const (
	ProjectRuntimeQuiescenceReceiptSchemaVersion = projectquiescence.RequestSchemaVersion
	projectRuntimeQuiescenceStopped              = projectquiescence.TargetStateStopped

	defaultProjectArchiveQuiescenceRouteTimeout = 2 * time.Minute
	defaultProjectArchiveQuiescencePollInterval = 100 * time.Millisecond
)

type ProjectRuntimeQuiescenceTarget = projectquiescence.ProjectRuntimeQuiescenceTarget
type ProjectRuntimeQuiescenceEvidence = projectquiescence.Evidence
type ProjectRuntimeQuiescenceRequest = projectquiescence.Request
type ProjectRuntimeQuiescenceReceipt = projectquiescence.Receipt

// ProjectRuntimeQuiescenceVerifier is the narrow owner-node dependency needed
// before physical custody can move. Every invocation must execute a fresh node
// observation; registry deactivation or cached routing output is insufficient.
type ProjectRuntimeQuiescenceVerifier interface {
	VerifyProjectArchiveRuntimeQuiescence(context.Context, requestctx.Context, ProjectRuntimeQuiescenceRequest) (ProjectRuntimeQuiescenceReceipt, error)
}

type ProjectArchiveQuiescenceRouting interface {
	Call(context.Context, requestctx.Context, routing.CapabilityCallInput, string) (routing.CapabilityCallOutcome, error)
	GetRoute(context.Context, string) (routing.Route, error)
	GetCapabilityCall(context.Context, string) (routing.CapabilityCall, error)
}

// RoutedProjectRuntimeQuiescenceVerifier executes one dedicated system
// capability call per owner node and accepts only the exact authenticated
// terminal result attached to that newly created call.
type RoutedProjectRuntimeQuiescenceVerifier struct {
	Routing      ProjectArchiveQuiescenceRouting
	Timeout      time.Duration
	PollInterval time.Duration
	Now          func() time.Time
	NewAttemptID func() string
}

func NewRoutedProjectRuntimeQuiescenceVerifier(service ProjectArchiveQuiescenceRouting) *RoutedProjectRuntimeQuiescenceVerifier {
	return &RoutedProjectRuntimeQuiescenceVerifier{Routing: service}
}

func (v *RoutedProjectRuntimeQuiescenceVerifier) VerifyProjectArchiveRuntimeQuiescence(ctx context.Context, original requestctx.Context, request ProjectRuntimeQuiescenceRequest) (ProjectRuntimeQuiescenceReceipt, error) {
	aggregate := ProjectRuntimeQuiescenceReceipt{
		SchemaVersion: request.SchemaVersion, ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug,
		OperationID: request.OperationID, PlanDigest: request.PlanDigest, Evidence: []ProjectRuntimeQuiescenceEvidence{},
	}
	if len(request.Targets) == 0 {
		return aggregate, nil
	}
	if v == nil || v.Routing == nil {
		return aggregate, fmt.Errorf("project archive routed quiescence verifier is not configured")
	}
	if strings.TrimSpace(original.ActorID) == "" || strings.TrimSpace(original.OriginNodeID) == "" ||
		strings.TrimSpace(original.ScopeID) == "" || strings.TrimSpace(original.ScopeKey) == "" || strings.TrimSpace(original.CorrelationID) == "" {
		return aggregate, fmt.Errorf("project archive routed quiescence requires the original actor, origin node, project scope, and correlation identity")
	}

	grouped := map[string][]ProjectRuntimeQuiescenceTarget{}
	for _, target := range request.Targets {
		if err := projectquiescence.ValidateTarget(target); err != nil {
			return aggregate, err
		}
		grouped[target.OwnerNode] = append(grouped[target.OwnerNode], target)
	}
	nodes := make([]string, 0, len(grouped))
	for node := range grouped {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	attemptID := v.newAttemptID()
	if attemptID == "" {
		return aggregate, fmt.Errorf("project archive routed quiescence attempt identity is empty")
	}
	evidenceByTarget := make(map[string]ProjectRuntimeQuiescenceEvidence, len(request.Targets))
	for _, node := range nodes {
		nodeRequest := ProjectRuntimeQuiescenceRequest{
			ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug, OperationID: request.OperationID,
			PlanDigest: request.PlanDigest, NodeKey: node, Targets: grouped[node],
		}
		if err := projectquiescence.SealRequest(&nodeRequest); err != nil {
			return aggregate, fmt.Errorf("seal project archive quiescence request for node %s: %w", node, err)
		}
		receipt, err := v.executeNode(ctx, original, nodeRequest, attemptID)
		if err != nil {
			return aggregate, err
		}
		for _, evidence := range receipt.Evidence {
			key := projectRuntimeQuiescenceTargetKey(evidence.ProjectRuntimeQuiescenceTarget)
			if _, exists := evidenceByTarget[key]; exists {
				return aggregate, fmt.Errorf("project archive node receipts contain duplicate target evidence")
			}
			evidence.ReceiptID = receipt.ReceiptID
			evidenceByTarget[key] = evidence
		}
	}
	for _, target := range request.Targets {
		evidence, ok := evidenceByTarget[projectRuntimeQuiescenceTargetKey(target)]
		if !ok {
			return aggregate, fmt.Errorf("project archive node receipts are missing target evidence")
		}
		aggregate.Evidence = append(aggregate.Evidence, evidence)
	}
	if err := validateProjectRuntimeQuiescenceReceipt(request, aggregate, time.Time{}); err != nil {
		return aggregate, err
	}
	return aggregate, nil
}

func (v *RoutedProjectRuntimeQuiescenceVerifier) executeNode(ctx context.Context, original requestctx.Context, request ProjectRuntimeQuiescenceRequest, attemptID string) (ProjectRuntimeQuiescenceReceipt, error) {
	endpoint, err := projectArchiveQuiescenceEndpoint(request.NodeKey)
	if err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, err
	}
	metadata, err := json.Marshal(map[string]any{
		"source": "project.archive.quiescence", "attempt_id": attemptID,
		"project_id": request.ProjectID, "project_slug": request.ProjectSlug,
		"project_scope_id": original.ScopeID, "project_scope_key": original.ScopeKey,
		"operation_id": request.OperationID, "plan_digest": request.PlanDigest,
		"node_key": request.NodeKey, "request_digest": request.RequestDigest,
	})
	if err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, err
	}
	idempotencyKey := "project.archive.quiescence." + attemptID + "." + request.NodeKey
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultProjectArchiveQuiescenceRouteTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	outcome, err := v.Routing.Call(callCtx, original, routing.CapabilityCallInput{
		Target: endpoint, ActorRef: original.ActorID, OriginNodeRef: original.OriginNodeID,
		ScopeRef: "system", Input: raw, Metadata: metadata,
	}, idempotencyKey)
	if err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("route project archive quiescence to node %s: %w", request.NodeKey, err)
	}
	if err := validateProjectArchiveRoutedCall(original, endpoint, request, metadata, idempotencyKey, outcome.Route, outcome.CapabilityCall); err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, err
	}
	return v.waitForNodeReceipt(callCtx, original, endpoint, request, metadata, idempotencyKey, outcome.Route, outcome.CapabilityCall)
}

func (v *RoutedProjectRuntimeQuiescenceVerifier) waitForNodeReceipt(ctx context.Context, original requestctx.Context, endpoint string, request ProjectRuntimeQuiescenceRequest, metadata json.RawMessage, idempotencyKey string, initialRoute routing.Route, initialCall routing.CapabilityCall) (ProjectRuntimeQuiescenceReceipt, error) {
	interval := v.PollInterval
	if interval <= 0 {
		interval = defaultProjectArchiveQuiescencePollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		route, err := v.Routing.GetRoute(ctx, initialRoute.RouteID)
		if err != nil {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("read project archive quiescence route: %w", err)
		}
		call, err := v.Routing.GetCapabilityCall(ctx, initialCall.CapabilityCallID)
		if err != nil {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("read project archive quiescence call: %w", err)
		}
		if err := validateProjectArchiveRoutedCall(original, endpoint, request, metadata, idempotencyKey, route, call); err != nil {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence terminal identity: %w", err)
		}
		if !sameProjectArchiveRoutedIdentity(initialRoute, initialCall, route, call) {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence terminal route or call identity changed")
		}
		if route.Status == routing.RouteStatusCompleted && call.Status == routing.CapabilityCallStatusCompleted {
			if route.CompletedAt == nil || call.CompletedAt == nil {
				return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence terminal result has no completion time")
			}
			return decodeRoutedProjectArchiveReceipt(call.ResultJSON, request, v.now())
		}
		if projectArchiveRouteRefuses(route.Status) || projectArchiveCallRefuses(call.Status) {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence route refused: route=%s call=%s code=%s", route.Status, call.Status, firstNonEmpty(pointerString(call.ErrorCode), pointerString(route.FailureCode)))
		}
		if projectArchiveRouteTerminal(route.Status) || projectArchiveCallTerminal(call.Status) {
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence route has inconsistent terminal state: route=%s call=%s", route.Status, call.Status)
		}
		select {
		case <-ctx.Done():
			return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("project archive quiescence route did not reach a terminal result: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func validateProjectArchiveRoutedCall(original requestctx.Context, endpoint string, request ProjectRuntimeQuiescenceRequest, metadata json.RawMessage, idempotencyKey string, route routing.Route, call routing.CapabilityCall) error {
	if route.RouteKind != routing.RouteKindRemote || route.ExecutionMode != routing.ExecutionModeImmediate || call.ExecutionMode != routing.ExecutionModeImmediate ||
		route.RouteID == "" || route.RouteKey != route.RouteID || call.CapabilityCallID == "" || call.CapabilityCallKey != call.CapabilityCallID || call.RouteID != route.RouteID ||
		route.TargetNodeID == "" || route.ProviderID == "" || route.CapabilityEndpointID == "" || route.CapabilityClassID == nil || strings.TrimSpace(*route.CapabilityClassID) == "" ||
		call.TargetNodeID != route.TargetNodeID || call.ProviderID != route.ProviderID || call.CapabilityEndpointID != route.CapabilityEndpointID ||
		route.ActorID != original.ActorID || call.ActorID != original.ActorID || route.OriginNodeID != original.OriginNodeID || call.OriginNodeID != original.OriginNodeID ||
		route.CorrelationID != original.CorrelationID || call.CorrelationID != original.CorrelationID ||
		route.OriginScopeID == nil || call.ScopeID == nil || strings.TrimSpace(*route.OriginScopeID) == "" || *call.ScopeID != *route.OriginScopeID ||
		route.RuntimeNodeID == nil || *route.RuntimeNodeID != route.TargetNodeID || call.Operation != "capability:"+endpoint ||
		!equalOptionalString(route.PolicyDecisionID, call.PolicyDecisionID) || !equalOptionalString(route.ApprovalID, call.ApprovalID) ||
		!equalOptionalString(route.GrantID, call.GrantID) || !equalOptionalString(route.JobID, call.JobID) ||
		call.IdempotencyKey == nil || *call.IdempotencyKey != idempotencyKey ||
		!projectArchiveJSONDocumentsEqual(route.Metadata, metadata) || !projectArchiveJSONDocumentsEqual(call.Metadata, metadata) {
		return fmt.Errorf("project archive quiescence did not enter the exact authenticated remote route")
	}
	decoded, err := projectquiescence.DecodeRequest(call.InputJSON)
	if err != nil || !reflect.DeepEqual(decoded, request) {
		return fmt.Errorf("project archive quiescence routed input changed")
	}
	if route.Status == routing.RouteStatusWaitingForApproval || call.Status == routing.CapabilityCallStatusApprovalRequired {
		return fmt.Errorf("project archive quiescence route requires approval")
	}
	if projectArchiveRouteRefuses(route.Status) || projectArchiveCallRefuses(call.Status) {
		return fmt.Errorf("project archive quiescence route refused before dispatch")
	}
	return nil
}

func decodeRoutedProjectArchiveReceipt(raw json.RawMessage, request ProjectRuntimeQuiescenceRequest, now time.Time) (ProjectRuntimeQuiescenceReceipt, error) {
	if len(raw) == 0 || len(raw) > projectquiescence.MaxReceiptBytes {
		return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("quiescence receipt size is invalid")
	}
	var receipt ProjectRuntimeQuiescenceReceipt
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("decode quiescence receipt: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ProjectRuntimeQuiescenceReceipt{}, fmt.Errorf("quiescence receipt contains trailing data")
	}
	if err := projectquiescence.ValidateReceipt(request, receipt, time.Time{}, now); err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, err
	}
	return receipt, nil
}

func sameProjectArchiveRoutedIdentity(initialRoute routing.Route, initialCall routing.CapabilityCall, route routing.Route, call routing.CapabilityCall) bool {
	return initialRoute.RouteID == route.RouteID && initialRoute.RouteKey == route.RouteKey &&
		initialRoute.CorrelationID == route.CorrelationID && initialRoute.ActorID == route.ActorID && initialRoute.OriginNodeID == route.OriginNodeID &&
		equalOptionalString(initialRoute.OriginScopeID, route.OriginScopeID) && equalOptionalString(initialRoute.RuntimeNodeID, route.RuntimeNodeID) &&
		initialRoute.TargetNodeID == route.TargetNodeID && initialRoute.ProviderID == route.ProviderID && initialRoute.CapabilityEndpointID == route.CapabilityEndpointID &&
		equalOptionalString(initialRoute.CapabilityClassID, route.CapabilityClassID) && equalOptionalString(initialRoute.PolicyDecisionID, route.PolicyDecisionID) &&
		equalOptionalString(initialRoute.ApprovalID, route.ApprovalID) && equalOptionalString(initialRoute.GrantID, route.GrantID) && equalOptionalString(initialRoute.JobID, route.JobID) &&
		initialRoute.RouteKind == route.RouteKind && initialRoute.ExecutionMode == route.ExecutionMode && initialRoute.CreatedAt.Equal(route.CreatedAt) &&
		projectArchiveJSONDocumentsEqual(initialRoute.SelectedPathJSON, route.SelectedPathJSON) && projectArchiveJSONDocumentsEqual(initialRoute.RequestSummaryJSON, route.RequestSummaryJSON) &&
		projectArchiveJSONDocumentsEqual(initialRoute.ResultTargetJSON, route.ResultTargetJSON) && projectArchiveJSONDocumentsEqual(initialRoute.Metadata, route.Metadata) &&
		initialCall.CapabilityCallID == call.CapabilityCallID && initialCall.CapabilityCallKey == call.CapabilityCallKey && initialCall.RouteID == call.RouteID &&
		initialCall.CorrelationID == call.CorrelationID && equalOptionalString(initialCall.IdempotencyKey, call.IdempotencyKey) &&
		initialCall.ActorID == call.ActorID && initialCall.OriginNodeID == call.OriginNodeID && equalOptionalString(initialCall.ScopeID, call.ScopeID) &&
		initialCall.TargetNodeID == call.TargetNodeID && initialCall.ProviderID == call.ProviderID && initialCall.CapabilityEndpointID == call.CapabilityEndpointID &&
		initialCall.Operation == call.Operation && initialCall.ExecutionMode == call.ExecutionMode && equalOptionalString(initialCall.PolicyDecisionID, call.PolicyDecisionID) &&
		equalOptionalString(initialCall.ApprovalID, call.ApprovalID) && equalOptionalString(initialCall.GrantID, call.GrantID) && equalOptionalString(initialCall.JobID, call.JobID) && initialCall.CreatedAt.Equal(call.CreatedAt) &&
		projectArchiveJSONDocumentsEqual(initialCall.InputJSON, call.InputJSON) && projectArchiveJSONDocumentsEqual(initialCall.InputSummaryJSON, call.InputSummaryJSON) &&
		projectArchiveJSONDocumentsEqual(initialCall.Metadata, call.Metadata)
}

func equalOptionalString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func projectArchiveJSONDocumentsEqual(left, right json.RawMessage) bool {
	if len(bytes.TrimSpace(left)) == 0 || len(bytes.TrimSpace(right)) == 0 {
		return len(bytes.TrimSpace(left)) == 0 && len(bytes.TrimSpace(right)) == 0
	}
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	if leftDecoder.Decode(&struct{}{}) != io.EOF || rightDecoder.Decode(&struct{}{}) != io.EOF {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func projectArchiveQuiescenceEndpoint(nodeKey string) (string, error) {
	address, err := capabilities.ParseAddress(capabilities.NodeSystemProviderAddress(nodeKey) + ".project.archive.quiesce")
	if err != nil || address.CapabilityName != "project.archive.quiesce" || address.ProviderKey != capabilities.NodeSystemProviderKey(nodeKey) {
		return "", fmt.Errorf("project archive quiescence owner node is not a canonical routing key")
	}
	return address.CompactAddress, nil
}

func (v *RoutedProjectRuntimeQuiescenceVerifier) now() time.Time {
	if v.Now != nil {
		return v.Now().UTC()
	}
	return time.Now().UTC()
}

func (v *RoutedProjectRuntimeQuiescenceVerifier) newAttemptID() string {
	if v.NewAttemptID != nil {
		return strings.TrimSpace(v.NewAttemptID())
	}
	return ids.NewIdempotencyID()
}

func projectArchiveRouteRefuses(status string) bool {
	return status == routing.RouteStatusFailed || status == routing.RouteStatusCancelled || status == routing.RouteStatusExpired || status == routing.RouteStatusWaitingForApproval
}

func projectArchiveCallRefuses(status string) bool {
	return status == routing.CapabilityCallStatusFailed || status == routing.CapabilityCallStatusCancelled || status == routing.CapabilityCallStatusApprovalRequired
}

func projectArchiveRouteTerminal(status string) bool {
	return projectArchiveRouteRefuses(status) || status == routing.RouteStatusCompleted
}

func projectArchiveCallTerminal(status string) bool {
	return projectArchiveCallRefuses(status) || status == routing.CapabilityCallStatusCompleted
}

func pointerString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s ProjectRuntimeService) verifyProjectRuntimeQuiescence(ctx context.Context, plan ProjectPhysicalArchivePlan) (string, ProjectRuntimeQuiescenceReceipt, error) {
	request, err := projectRuntimeQuiescenceRequest(plan)
	if err != nil {
		return "", ProjectRuntimeQuiescenceReceipt{}, err
	}
	routingRequest, err := projectRuntimeQuiescenceRoutingContext(plan)
	if err != nil {
		return "", ProjectRuntimeQuiescenceReceipt{}, err
	}
	receipt := ProjectRuntimeQuiescenceReceipt{
		SchemaVersion: ProjectRuntimeQuiescenceReceiptSchemaVersion, ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug,
		OperationID: request.OperationID, PlanDigest: request.PlanDigest, Evidence: []ProjectRuntimeQuiescenceEvidence{},
	}
	if len(request.Targets) > 0 {
		if s.RuntimeQuiescence == nil {
			return "", receipt, fmt.Errorf("project archive requires exact owner-node runtime quiescence receipts before workspace move; watched-root supervisor and service-manager registry disablement is insufficient")
		}
		receipt, err = s.RuntimeQuiescence.VerifyProjectArchiveRuntimeQuiescence(ctx, routingRequest, request)
		if err != nil {
			return "", receipt, fmt.Errorf("verify project archive owner-node runtime quiescence: %w", err)
		}
	}
	if err := validateProjectRuntimeQuiescenceReceipt(request, receipt, plan.Workspace.PlannedAt); err != nil {
		return "", receipt, err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return "", receipt, err
	}
	digest := sha256Hex(payload)
	return digest, receipt, nil
}

func projectRuntimeQuiescenceRoutingContext(plan ProjectPhysicalArchivePlan) (requestctx.Context, error) {
	routingRequest := plan.Request
	project := plan.Registration.Project.Project
	if strings.TrimSpace(project.ProjectScopeID) == "" || strings.TrimSpace(project.ProjectScopeKey) == "" {
		return requestctx.Context{}, fmt.Errorf("project archive runtime quiescence project scope is incomplete")
	}
	if (routingRequest.ScopeID != "" && routingRequest.ScopeID != project.ProjectScopeID) ||
		(routingRequest.ScopeKey != "" && routingRequest.ScopeKey != project.ProjectScopeKey) {
		return requestctx.Context{}, fmt.Errorf("project archive runtime quiescence request scope contradicts the reviewed project")
	}
	routingRequest.ScopeID = project.ProjectScopeID
	routingRequest.ScopeKey = project.ProjectScopeKey
	return routingRequest, nil
}

func projectRuntimeQuiescenceRequest(plan ProjectPhysicalArchivePlan) (ProjectRuntimeQuiescenceRequest, error) {
	request := ProjectRuntimeQuiescenceRequest{
		SchemaVersion: ProjectRuntimeQuiescenceReceiptSchemaVersion, ProjectID: plan.Custody.ProjectID, ProjectSlug: plan.Custody.ProjectSlug,
		OperationID: plan.Workspace.OperationID, PlanDigest: plan.PlanDigest, Targets: []ProjectRuntimeQuiescenceTarget{},
	}
	for _, registration := range plan.Registration.WatchedRootRegistrations {
		target := ProjectRuntimeQuiescenceTarget{
			Facet: projectquiescence.FacetWatchedRoots, Kind: projectquiescence.TargetKindWatchedRoot,
			OwnerNode: registration.OwnerNodeKey, LocalRootKey: registration.LocalRootKey,
			BackendRootRef: registration.BackendRootKey, WorkerKey: registration.WorkerKey, ConfigHash: registration.ConfigHash,
		}
		if err := projectquiescence.ValidateTarget(target); err != nil {
			return request, fmt.Errorf("project archive watched-root quiescence target is incomplete: %w", err)
		}
		request.Targets = append(request.Targets, target)
	}
	for _, deactivation := range plan.Deactivations {
		if deactivation.Input.Facet != "services" {
			continue
		}
		for _, action := range deactivation.Actions {
			if action.Status == "skipped" && strings.TrimSpace(action.Ref) == "" {
				continue
			}
			if action.Kind != "service" || strings.TrimSpace(action.Key) == "" || strings.TrimSpace(action.Ref) == "" {
				return request, fmt.Errorf("project archive service quiescence target is incomplete")
			}
			metadata, err := projectactivation.DecodeProjectArchiveServiceActionMetadata(action.Metadata)
			if err != nil {
				return request, fmt.Errorf("project archive service quiescence target metadata: %w", err)
			}
			if metadata.ProviderKey != action.Key || metadata.ProviderAddress != action.Ref {
				return request, fmt.Errorf("project archive service quiescence target contradicts its deactivation action")
			}
			target := ProjectRuntimeQuiescenceTarget{
				Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService,
				OwnerNode: metadata.OwnerNode, ProviderKey: metadata.ProviderKey, ProviderAddress: metadata.ProviderAddress,
				ProviderID: metadata.ProviderID, RuntimeProfileDigest: metadata.RuntimeProfileDigest,
				AllowlistKey: metadata.AllowlistKey, Manager: metadata.Manager, Unit: metadata.Unit,
			}
			if err := projectquiescence.ValidateTarget(target); err != nil {
				return request, fmt.Errorf("project archive service quiescence target is incomplete: %w", err)
			}
			request.Targets = append(request.Targets, target)
		}
	}
	sort.Slice(request.Targets, func(i, j int) bool {
		return projectRuntimeQuiescenceTargetKey(request.Targets[i]) < projectRuntimeQuiescenceTargetKey(request.Targets[j])
	})
	if len(request.Targets) > 256 {
		return request, fmt.Errorf("project archive runtime quiescence target set exceeds its bound")
	}
	for index := 1; index < len(request.Targets); index++ {
		if projectRuntimeQuiescenceTargetKey(request.Targets[index-1]) == projectRuntimeQuiescenceTargetKey(request.Targets[index]) {
			return request, fmt.Errorf("project archive runtime quiescence targets are not unique")
		}
	}
	return request, nil
}

func validateProjectRuntimeQuiescenceReceipt(request ProjectRuntimeQuiescenceRequest, receipt ProjectRuntimeQuiescenceReceipt, floor time.Time) error {
	if err := projectquiescence.ValidateAggregateReceipt(request, receipt, floor); err != nil {
		return err
	}
	if receipt.RequestDigest != "" || receipt.NodeKey != "" || receipt.ReceiptID != "" {
		return fmt.Errorf("project archive aggregate quiescence receipt carries node-only identity")
	}
	for _, evidence := range receipt.Evidence {
		if evidence.FenceState != projectquiescence.FenceStateActive || !canonicalSHA256(evidence.ReceiptID) || strings.TrimSpace(evidence.TargetReceiptID) == "" || evidence.ObservedAt.Location() != time.UTC {
			return fmt.Errorf("project archive runtime target lacks exact fenced node receipt evidence")
		}
	}
	return nil
}

func projectRuntimeQuiescenceTargetKey(target ProjectRuntimeQuiescenceTarget) string {
	return projectquiescence.CanonicalTargetKey(target)
}

func canonicalSHA256(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && value == strings.ToLower(value)
}

func sha256Hex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
