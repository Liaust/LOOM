package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
)

func TestProjectArchiveRoutedQuiescenceGroupsNodesAndPreservesAggregateOrder(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	router := newProjectArchiveRoutedQuiescenceFake(now)
	attempt := 0
	verifier := &RoutedProjectRuntimeQuiescenceVerifier{
		Routing: router, Now: func() time.Time { return now }, PollInterval: time.Millisecond,
		NewAttemptID: func() string { attempt++; return fmt.Sprintf("attempt-%d", attempt) },
	}
	request := routedProjectArchiveRequestFixture()
	original := routedProjectArchiveOriginalRequest()
	receipt, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), original, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(router.calls) != 2 || router.calls[0].nodeKey != "node-a" || router.calls[1].nodeKey != "node-b" {
		t.Fatalf("node call order = %#v", router.calls)
	}
	if len(receipt.Evidence) != len(request.Targets) {
		t.Fatalf("aggregate evidence = %#v", receipt.Evidence)
	}
	for index, target := range request.Targets {
		evidence := receipt.Evidence[index]
		if evidence.ProjectRuntimeQuiescenceTarget != target || evidence.FenceState != projectquiescence.FenceStateActive || !canonicalSHA256(evidence.ReceiptID) || evidence.TargetReceiptID == "" {
			t.Fatalf("aggregate evidence %d = %#v", index, evidence)
		}
	}
	for _, call := range router.calls {
		if call.input.ScopeRef != "system" || call.input.ActorRef != original.ActorID || call.input.OriginNodeRef != original.OriginNodeID {
			t.Fatalf("routed authority changed: %#v", call.input)
		}
		if call.request.NodeKey != call.nodeKey || call.request.ProjectID != request.ProjectID || call.request.PlanDigest != request.PlanDigest {
			t.Fatalf("node request binding = %#v", call.request)
		}
		var metadata map[string]any
		if err := json.Unmarshal(call.input.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["project_scope_id"] != original.ScopeID || metadata["project_scope_key"] != original.ScopeKey || metadata["request_digest"] != call.request.RequestDigest {
			t.Fatalf("routing metadata lost project binding: %#v", metadata)
		}
	}
}

func TestProjectArchiveRoutedQuiescenceReplayUsesFreshCallsWithStableEvidence(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	router := newProjectArchiveRoutedQuiescenceFake(now)
	attempt := 0
	verifier := &RoutedProjectRuntimeQuiescenceVerifier{
		Routing: router, Now: func() time.Time { return now }, PollInterval: time.Millisecond,
		NewAttemptID: func() string { attempt++; return fmt.Sprintf("attempt-%d", attempt) },
	}
	request := routedProjectArchiveRequestFixture()
	first, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), routedProjectArchiveOriginalRequest(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), routedProjectArchiveOriginalRequest(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(router.calls) != 4 {
		t.Fatalf("replay used %d calls, want 4 fresh node executions", len(router.calls))
	}
	if router.calls[0].idempotencyKey == router.calls[2].idempotencyKey || router.calls[1].idempotencyKey == router.calls[3].idempotencyKey {
		t.Fatal("replay reused an earlier routing attempt identity")
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("fresh replay changed aggregate evidence: first=%#v second=%#v", first, second)
	}
}

func TestProjectArchiveRoutedQuiescenceRefusesNonTerminalAndInvalidResults(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		mode string
	}{
		{name: "approval", mode: "approval"},
		{name: "route failure", mode: "route_failed"},
		{name: "cancelled", mode: "cancelled"},
		{name: "malformed result", mode: "malformed"},
		{name: "unknown receipt field", mode: "unknown_receipt_field"},
		{name: "wrong node", mode: "wrong_node"},
		{name: "wrong operation", mode: "wrong_operation"},
		{name: "changed target", mode: "changed_target"},
		{name: "oversized result", mode: "oversized"},
		{name: "same-node local bypass", mode: "local_route"},
		{name: "routing scope substitution", mode: "wrong_scope"},
		{name: "runtime node substitution", mode: "wrong_runtime_node"},
		{name: "correlation substitution", mode: "wrong_correlation"},
		{name: "routed input substitution", mode: "wrong_input"},
		{name: "missing completion time", mode: "missing_completion"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := newProjectArchiveRoutedQuiescenceFake(now)
			router.mode = test.mode
			verifier := &RoutedProjectRuntimeQuiescenceVerifier{
				Routing: router, Now: func() time.Time { return now }, PollInterval: time.Millisecond,
				NewAttemptID: func() string { return "attempt-one" },
			}
			if _, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), routedProjectArchiveOriginalRequest(), routedProjectArchiveRequestFixture()); err == nil {
				t.Fatalf("%s was accepted", test.name)
			}
		})
	}
}

func TestProjectArchiveRoutedQuiescenceAcceptsJSONBNormalizedReceipt(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	router := newProjectArchiveRoutedQuiescenceFake(now)
	router.mode = "jsonb_normalized"
	verifier := &RoutedProjectRuntimeQuiescenceVerifier{
		Routing: router, Now: func() time.Time { return now }, PollInterval: time.Millisecond,
		NewAttemptID: func() string { return "attempt-jsonb" },
	}
	if _, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), routedProjectArchiveOriginalRequest(), routedProjectArchiveRequestFixture()); err != nil {
		t.Fatalf("JSONB-normalized receipt was refused: %v", err)
	}
}

func TestProjectArchiveRoutedQuiescenceRejectsTerminalIdentitySubstitution(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for _, mode := range []string{
		"terminal_route_id", "terminal_call_id", "terminal_actor", "terminal_origin", "terminal_scope", "terminal_correlation",
		"terminal_runtime_node", "terminal_target_node", "terminal_provider", "terminal_endpoint",
		"terminal_class", "terminal_operation", "terminal_execution_mode", "terminal_input", "terminal_idempotency", "terminal_attempt_metadata",
	} {
		t.Run(mode, func(t *testing.T) {
			router := newProjectArchiveRoutedQuiescenceFake(now)
			router.mode = mode
			verifier := &RoutedProjectRuntimeQuiescenceVerifier{
				Routing: router, Now: func() time.Time { return now }, PollInterval: time.Millisecond,
				NewAttemptID: func() string { return "attempt-terminal" },
			}
			if _, err := verifier.VerifyProjectArchiveRuntimeQuiescence(context.Background(), routedProjectArchiveOriginalRequest(), routedProjectArchiveRequestFixture()); err == nil {
				t.Fatal("terminal identity substitution was accepted")
			}
		})
	}
}

func TestProjectArchiveRoutedQuiescenceTimeoutAndCancellationRefuse(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		cancel bool
	}{
		{name: "timeout"},
		{name: "cancellation", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := newProjectArchiveRoutedQuiescenceFake(now)
			router.mode = "dispatched"
			verifier := &RoutedProjectRuntimeQuiescenceVerifier{
				Routing: router, Now: func() time.Time { return now }, Timeout: 8 * time.Millisecond, PollInterval: time.Millisecond,
				NewAttemptID: func() string { return "attempt-timeout" },
			}
			ctx := context.Background()
			var cancel context.CancelFunc
			if test.cancel {
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := verifier.VerifyProjectArchiveRuntimeQuiescence(ctx, routedProjectArchiveOriginalRequest(), routedProjectArchiveRequestFixture())
			if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
				t.Fatalf("error = %v, want bounded context refusal", err)
			}
		})
	}
}

func TestProjectArchiveAggregateReceiptRejectsStaleOrUnfencedNodeEvidence(t *testing.T) {
	request := routedProjectArchiveRequestFixture()
	floor := time.Date(2026, 9, 4, 11, 59, 0, 0, time.UTC)
	receipt := ProjectRuntimeQuiescenceReceipt{
		SchemaVersion: request.SchemaVersion, ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug,
		OperationID: request.OperationID, PlanDigest: request.PlanDigest,
	}
	for index, target := range request.Targets {
		receipt.Evidence = append(receipt.Evidence, ProjectRuntimeQuiescenceEvidence{
			ProjectRuntimeQuiescenceTarget: target, State: projectquiescence.TargetStateStopped, FenceState: projectquiescence.FenceStateActive,
			TargetReceiptID: fmt.Sprintf("target-%d", index), ReceiptID: "sha256:" + strings.Repeat(string(rune('a'+index)), 64), ObservedAt: floor,
		})
	}
	for _, mutate := range []func(*ProjectRuntimeQuiescenceReceipt){
		func(value *ProjectRuntimeQuiescenceReceipt) {
			value.Evidence[0].ObservedAt = floor.Add(-time.Nanosecond)
		},
		func(value *ProjectRuntimeQuiescenceReceipt) { value.Evidence[0].FenceState = "inactive" },
		func(value *ProjectRuntimeQuiescenceReceipt) { value.Evidence[0].ReceiptID = "caller-supplied" },
	} {
		candidate := cloneProjectArchiveTestValue(t, receipt)
		mutate(&candidate)
		if err := validateProjectRuntimeQuiescenceReceipt(request, candidate, floor); err == nil {
			t.Fatalf("invalid aggregate receipt was accepted: %#v", candidate)
		}
	}
}

func TestProjectArchiveQuiescenceRoutingContextBindsCanonicalProjectScope(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-scope", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1,
	})
	plan, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-scope", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID})
	if err != nil {
		t.Fatal(err)
	}
	routed, err := projectRuntimeQuiescenceRoutingContext(plan)
	if err != nil {
		t.Fatal(err)
	}
	if routed.ActorID != plan.Request.ActorID || routed.OriginNodeID != plan.Request.OriginNodeID || routed.CorrelationID != plan.Request.CorrelationID ||
		routed.ScopeID != plan.Registration.Project.Project.ProjectScopeID || routed.ScopeKey != plan.Registration.Project.Project.ProjectScopeKey {
		t.Fatalf("routed project context = %#v", routed)
	}

	plan.Request.ScopeID = "scope_other"
	if _, err := projectRuntimeQuiescenceRoutingContext(plan); err == nil {
		t.Fatal("conflicting caller project scope was accepted")
	}
}

type projectArchiveRoutedCallRecord struct {
	nodeKey        string
	request        ProjectRuntimeQuiescenceRequest
	input          routing.CapabilityCallInput
	idempotencyKey string
}

type projectArchiveRoutedResult struct {
	route routing.Route
	call  routing.CapabilityCall
}

type projectArchiveRoutedQuiescenceFake struct {
	mu      sync.Mutex
	now     time.Time
	mode    string
	calls   []projectArchiveRoutedCallRecord
	results map[string]projectArchiveRoutedResult
}

func newProjectArchiveRoutedQuiescenceFake(now time.Time) *projectArchiveRoutedQuiescenceFake {
	return &projectArchiveRoutedQuiescenceFake{now: now, results: map[string]projectArchiveRoutedResult{}}
}

func (f *projectArchiveRoutedQuiescenceFake) Call(_ context.Context, original requestctx.Context, input routing.CapabilityCallInput, idempotencyKey string) (routing.CapabilityCallOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request, err := projectquiescence.DecodeRequest(input.Input)
	if err != nil {
		return routing.CapabilityCallOutcome{}, err
	}
	index := len(f.calls) + 1
	routeID := fmt.Sprintf("route_%d", index)
	callID := fmt.Sprintf("capability_call_%d", index)
	nodeID := "node_id_" + strings.ReplaceAll(request.NodeKey, "-", "_")
	classID := "cclass_archive_quiescence"
	scopeID := "scope_system"
	completedAt := f.now
	route := routing.Route{
		RouteID: routeID, RouteKey: routeID, CorrelationID: original.CorrelationID, ActorID: original.ActorID, OriginNodeID: original.OriginNodeID,
		OriginScopeID: &scopeID, RuntimeNodeID: &nodeID, TargetNodeID: nodeID,
		ProviderID: "prov_system_" + request.NodeKey, CapabilityEndpointID: "endp_archive_" + request.NodeKey,
		CapabilityClassID: &classID,
		RouteKind:         routing.RouteKindRemote, ExecutionMode: routing.ExecutionModeImmediate,
		SelectedPathJSON: json.RawMessage(`{}`), RequestSummaryJSON: json.RawMessage(`{}`), ResultTargetJSON: json.RawMessage(`{}`),
		Status: routing.RouteStatusDispatched, Metadata: append(json.RawMessage(nil), input.Metadata...),
	}
	call := routing.CapabilityCall{
		CapabilityCallID: callID, CapabilityCallKey: callID, RouteID: routeID, CorrelationID: original.CorrelationID, IdempotencyKey: &idempotencyKey,
		ActorID: original.ActorID, OriginNodeID: original.OriginNodeID, ScopeID: &scopeID,
		TargetNodeID: nodeID, ProviderID: route.ProviderID, CapabilityEndpointID: route.CapabilityEndpointID,
		Operation: "capability:" + input.Target, ExecutionMode: routing.ExecutionModeImmediate, Status: routing.CapabilityCallStatusDispatched,
		InputJSON: append(json.RawMessage(nil), input.Input...), InputSummaryJSON: json.RawMessage(`{}`), Metadata: append(json.RawMessage(nil), input.Metadata...),
	}
	receipt := ProjectRuntimeQuiescenceReceipt{Evidence: make([]ProjectRuntimeQuiescenceEvidence, 0, len(request.Targets))}
	for targetIndex, target := range request.Targets {
		receipt.Evidence = append(receipt.Evidence, ProjectRuntimeQuiescenceEvidence{
			ProjectRuntimeQuiescenceTarget: target, State: projectquiescence.TargetStateStopped, FenceState: projectquiescence.FenceStateActive,
			TargetReceiptID: fmt.Sprintf("target:%s:%d", request.NodeKey, targetIndex), ObservedAt: f.now,
		})
	}
	if err := projectquiescence.SealReceipt(request, &receipt); err != nil {
		return routing.CapabilityCallOutcome{}, err
	}
	resultJSON, _ := json.Marshal(receipt)
	terminalRoute := route
	terminalCall := call
	terminalRoute.Status, terminalCall.Status = routing.RouteStatusCompleted, routing.CapabilityCallStatusCompleted
	terminalRoute.CompletedAt, terminalCall.CompletedAt = &completedAt, &completedAt
	terminalCall.ResultJSON = resultJSON

	switch f.mode {
	case "approval":
		route.Status, call.Status = routing.RouteStatusWaitingForApproval, routing.CapabilityCallStatusApprovalRequired
		terminalRoute, terminalCall = route, call
	case "route_failed":
		terminalRoute.Status, terminalCall.Status = routing.RouteStatusFailed, routing.CapabilityCallStatusFailed
	case "cancelled":
		terminalRoute.Status, terminalCall.Status = routing.RouteStatusCancelled, routing.CapabilityCallStatusCancelled
	case "dispatched":
		terminalRoute, terminalCall = route, call
	case "malformed":
		terminalCall.ResultJSON = json.RawMessage(`{"not":"a receipt"}`)
	case "jsonb_normalized":
		terminalCall.ResultJSON, _ = json.MarshalIndent(receipt, "", "  ")
	case "unknown_receipt_field":
		var changed map[string]any
		_ = json.Unmarshal(terminalCall.ResultJSON, &changed)
		changed["unknown"] = true
		terminalCall.ResultJSON, _ = json.Marshal(changed)
	case "wrong_node":
		var changed ProjectRuntimeQuiescenceReceipt
		_ = json.Unmarshal(terminalCall.ResultJSON, &changed)
		changed.NodeKey = "other-node"
		terminalCall.ResultJSON, _ = json.Marshal(changed)
	case "wrong_operation":
		var changed ProjectRuntimeQuiescenceReceipt
		_ = json.Unmarshal(terminalCall.ResultJSON, &changed)
		changed.OperationID = "other-operation"
		terminalCall.ResultJSON, _ = json.Marshal(changed)
	case "changed_target":
		var changed ProjectRuntimeQuiescenceReceipt
		_ = json.Unmarshal(terminalCall.ResultJSON, &changed)
		changed.Evidence[0].OwnerNode = "other-node"
		terminalCall.ResultJSON, _ = json.Marshal(changed)
	case "oversized":
		terminalCall.ResultJSON = json.RawMessage(`{"padding":"` + strings.Repeat("x", projectquiescence.MaxReceiptBytes) + `"}`)
	case "local_route":
		route.RouteKind = routing.RouteKindLocal
	case "wrong_scope":
		wrongScope := "scope_other"
		call.ScopeID = &wrongScope
	case "wrong_runtime_node":
		wrongNode := "node_id_other"
		route.RuntimeNodeID = &wrongNode
	case "wrong_correlation":
		call.CorrelationID = "corr_other"
	case "wrong_input":
		call.InputJSON = json.RawMessage(`{"substituted":true}`)
	case "missing_completion":
		terminalRoute.CompletedAt = nil
	case "terminal_route_id":
		terminalRoute.RouteID, terminalRoute.RouteKey, terminalCall.RouteID = "route_other", "route_other", "route_other"
	case "terminal_call_id":
		terminalCall.CapabilityCallID, terminalCall.CapabilityCallKey = "capability_call_other", "capability_call_other"
	case "terminal_actor":
		terminalRoute.ActorID, terminalCall.ActorID = "actor_other", "actor_other"
	case "terminal_origin":
		terminalRoute.OriginNodeID, terminalCall.OriginNodeID = "node_other", "node_other"
	case "terminal_scope":
		wrongScope := "scope_other"
		terminalRoute.OriginScopeID = &wrongScope
		terminalCall.ScopeID = &wrongScope
	case "terminal_correlation":
		terminalRoute.CorrelationID, terminalCall.CorrelationID = "corr_other", "corr_other"
	case "terminal_runtime_node":
		wrongNode := "node_id_other"
		terminalRoute.RuntimeNodeID = &wrongNode
	case "terminal_target_node":
		wrongNode := "node_id_other"
		terminalRoute.RuntimeNodeID, terminalRoute.TargetNodeID, terminalCall.TargetNodeID = &wrongNode, wrongNode, wrongNode
	case "terminal_provider":
		terminalRoute.ProviderID, terminalCall.ProviderID = "prov_other", "prov_other"
	case "terminal_endpoint":
		terminalRoute.CapabilityEndpointID, terminalCall.CapabilityEndpointID = "endp_other", "endp_other"
	case "terminal_class":
		otherClass := "cclass_other"
		terminalRoute.CapabilityClassID = &otherClass
	case "terminal_operation":
		terminalCall.Operation = "capability:workspace/other@system.project.archive.quiesce"
	case "terminal_execution_mode":
		terminalRoute.ExecutionMode, terminalCall.ExecutionMode = routing.ExecutionModeJob, routing.ExecutionModeJob
	case "terminal_input":
		terminalCall.InputJSON = json.RawMessage(`{"substituted":true}`)
	case "terminal_idempotency":
		other := "project.archive.quiescence.other"
		terminalCall.IdempotencyKey = &other
	case "terminal_attempt_metadata":
		var changed map[string]any
		_ = json.Unmarshal(terminalCall.Metadata, &changed)
		changed["attempt_id"] = "attempt-other"
		terminalCall.Metadata, _ = json.Marshal(changed)
		terminalRoute.Metadata = append(json.RawMessage(nil), terminalCall.Metadata...)
	}

	f.calls = append(f.calls, projectArchiveRoutedCallRecord{nodeKey: request.NodeKey, request: request, input: input, idempotencyKey: idempotencyKey})
	f.results[routeID] = projectArchiveRoutedResult{route: terminalRoute, call: terminalCall}
	return routing.CapabilityCallOutcome{Route: route, CapabilityCall: call, Status: call.Status}, nil
}

func (f *projectArchiveRoutedQuiescenceFake) GetRoute(_ context.Context, ref string) (routing.Route, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result, ok := f.results[ref]
	if !ok {
		return routing.Route{}, fmt.Errorf("route not found")
	}
	return result.route, nil
}

func (f *projectArchiveRoutedQuiescenceFake) GetCapabilityCall(_ context.Context, ref string) (routing.CapabilityCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, result := range f.results {
		if result.call.CapabilityCallID == ref {
			return result.call, nil
		}
	}
	return routing.CapabilityCall{}, fmt.Errorf("call not found")
}

func routedProjectArchiveOriginalRequest() requestctx.Context {
	return requestctx.Context{
		ActorID: "actor_original", ActorKey: "owner", OriginNodeID: "node_origin", OriginNodeKey: "main",
		ScopeID: "scope_project", ScopeKey: "projects/example", CorrelationID: "corr_project_archive",
	}
}

func routedProjectArchiveRequestFixture() ProjectRuntimeQuiescenceRequest {
	targets := []ProjectRuntimeQuiescenceTarget{
		{
			Facet: projectquiescence.FacetServices, Kind: projectquiescence.TargetKindService, OwnerNode: "node-b",
			ProviderKey: "service-one", ProviderAddress: "node-b@service-one", ProviderID: "prov_service_one",
			RuntimeProfileDigest: "sha256:" + strings.Repeat("b", 64), AllowlistKey: "service-one", Manager: "systemd", Unit: "loom-service-one.service",
		},
		{
			Facet: projectquiescence.FacetWatchedRoots, Kind: projectquiescence.TargetKindWatchedRoot, OwnerNode: "node-a",
			LocalRootKey: "project", BackendRootRef: "example__project", WorkerKey: "watched-root", ConfigHash: "sha256:" + strings.Repeat("a", 64),
		},
	}
	return ProjectRuntimeQuiescenceRequest{
		SchemaVersion: ProjectRuntimeQuiescenceReceiptSchemaVersion, ProjectID: "project_example", ProjectSlug: "example",
		OperationID: "workspace_archive_operation_example", PlanDigest: "sha256:" + strings.Repeat("c", 64), Targets: targets,
	}
}
