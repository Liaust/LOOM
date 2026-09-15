package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

func TestServiceRegistryPersistedSameNodeDispatchIntegration(t *testing.T) {
	dbURL := strings.TrimSpace(os.Getenv("LOOM_SERVICE_REGISTRY_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("set LOOM_SERVICE_REGISTRY_TEST_DB_URL to a dedicated database for persisted service routing integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := migrations.Up(ctx, dbURL, serviceRegistryMigrationsDir(t)); err != nil {
		t.Fatalf("migrate integration database: %v", err)
	}
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if _, err := bootstrap.NewService(sqlDB).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_service_registry_same_node")
	if err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	nodeKey := "service-e2e-" + suffix
	nodeID := ids.NewNodeID()
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO nodes.nodes (node_id, node_key, display_name, node_kind, node_role, runtime_class, status, owner_actor_id, metadata)
		VALUES ($1, $2, 'Service E2E node', 'workspace', 'worker', 'workspace-full', 'active', $3, '{"test":"service_registry"}'::jsonb)
	`, nodeID, nodeKey, req.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `
		INSERT INTO identity.actor_node_authorizations (authorization_id, actor_id, node_id, authorization_level, status, metadata)
		VALUES ($1, $2, $3, 5, 'active', '{"test":"service_registry"}'::jsonb)
	`, "auth_service_e2e_"+suffix, req.ActorID, nodeID); err != nil {
		t.Fatal(err)
	}
	slug := "service-e2e-" + suffix
	created, err := projects.NewService(sqlDB).CreateProject(ctx, req, projects.CreateInput{Name: "Service Registry E2E", Slug: slug, HomeNodeRef: "main", Metadata: json.RawMessage(`{"test":"service_registry"}`)})
	if err != nil {
		t.Fatal(err)
	}
	project := created.Project.Project
	unit := "local.loom.service-e2e-" + suffix
	record, err := serviceregistry.NormalizeAndValidateAllowlistRecord(serviceregistry.AllowlistRecord{
		SchemaVersion: serviceregistry.AllowlistSchemaV1, Key: "service-e2e-" + suffix, NodeKey: nodeKey,
		Manager: serviceregistry.ManagerLaunchd, Unit: unit,
		Operations:      []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationRestart, serviceregistry.OperationStop, serviceregistry.OperationLogs},
		LifecyclePolicy: "service_operations", Health: serviceregistry.AllowlistHealth{Kind: serviceregistry.HealthKindManager},
	})
	if err != nil {
		t.Fatal(err)
	}
	sourcePath, err := projectcontracts.KeyedProjectContractPath("services", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	planInput := serviceregistry.ProjectRegistrationPlanInput{
		ProviderKey: projectcontracts.ProjectServiceProviderKey(slug, "fixture"), ProjectSlug: slug, ScopeRef: project.ProjectScopeID,
		Registration: serviceregistry.ProjectRegistrationInput{
			ProjectID: project.ProjectID, ScopeKey: serviceregistry.ProjectRegistrationScopeKey(project.Slug), TargetNode: nodeKey,
			SourcePath: sourcePath,
			Service:    serviceregistry.ServiceIdentity{Key: "fixture", DisplayName: "Service E2E Fixture", Class: serviceregistry.ServiceClassProject},
			Runtime:    serviceregistry.RuntimeProfileInput{Manager: serviceregistry.ManagerLaunchd, Unit: unit, ServiceClass: serviceregistry.ServiceClassProject, Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationRestart, serviceregistry.OperationStop, serviceregistry.OperationLogs}, Health: serviceregistry.RuntimeHealth{Kind: serviceregistry.HealthKindManager}},
		},
	}
	capabilityService := capabilities.NewService(sqlDB)
	reconcile, err := serviceregistry.ReconcileProject(ctx, req, capabilityService, serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{record}}}, slug, []serviceregistry.ProjectRegistrationPlanInput{planInput})
	if err != nil {
		t.Fatal(err)
	}
	if len(reconcile.Registrations) != 1 || len(reconcile.Registrations[0].Endpoints.Endpoints) != 5 {
		t.Fatalf("registration did not persist endpoint intersection: %#v", reconcile)
	}
	provider := reconcile.Registrations[0].Provider
	if _, err := capabilityService.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusUnhealthy, AvailabilityStatus: capabilities.AvailabilityStatusUnavailable, Message: "process failed before recovery", DetailsJSON: json.RawMessage(`{"process_state":"failed"}`)}); err != nil {
		t.Fatal(err)
	}
	target := provider.CompactAddress + ".service.status"
	routingService := routing.NewService(sqlDB)
	plan, err := routingService.PlanRoute(ctx, req, routing.CapabilityCallInput{Target: target, OriginNodeRef: nodeID, ScopeRef: project.ProjectScopeKey, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("service-management health recovery route was blocked: %v", err)
	}
	if plan.OriginNodeID != nodeID || plan.TargetNodeID != nodeID || plan.RuntimeNodeID != nodeID || plan.ExecutionMode != routing.ExecutionModeImmediate {
		t.Fatalf("same-node identities or execution mode changed: %#v", plan)
	}
	for _, operation := range []string{"status", "start", "stop", "restart", "logs"} {
		t.Run("route_"+operation, func(t *testing.T) {
			got, err := routingService.PlanRoute(ctx, req, routing.CapabilityCallInput{Target: provider.CompactAddress + ".service." + operation, OriginNodeRef: nodeID, ScopeRef: project.ProjectScopeKey})
			if err != nil || got.RouteKind != routing.RouteKindRemote || got.RuntimeNodeID != nodeID {
				t.Fatalf("owner dispatch: route=%s runtime=%s err=%v", got.RouteKind, got.RuntimeNodeID, err)
			}
		})
	}
	callInput := routing.CapabilityCallInput{Target: target, OriginNodeRef: nodeID, ScopeRef: project.ProjectScopeKey, Input: json.RawMessage(`{}`)}
	fixtureExec := func(query string, args ...any) {
		t.Helper()
		if _, err := sqlDB.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	assertMessageCount := func(t *testing.T, callID, direction string, want int) {
		t.Helper()
		var count int
		if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM communication.messages WHERE capability_call_id = $1 AND direction = $2`, callID, direction).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != want {
			t.Fatalf("%s messages=%d, want %d", direction, count, want)
		}
	}
	t.Run("dry_run_does_not_dispatch", func(t *testing.T) {
		input := callInput
		input.DryRun = true
		got, err := routingService.Call(ctx, req, input, "same-node-dry-"+suffix)
		if err != nil || got.Status != routing.CapabilityCallStatusCompleted || got.DispatchMessageID != "" || !strings.Contains(string(got.Result), `"dry_run": true`) && !strings.Contains(string(got.Result), `"dry_run":true`) {
			t.Fatalf("dry run status=%s dispatch=%s result=%s err=%v", got.Status, got.DispatchMessageID, got.Result, err)
		}
		assertMessageCount(t, got.CapabilityCall.CapabilityCallID, "main_to_node", 0)
	})
	t.Run("unrelated_same_node_capability_stays_local", func(t *testing.T) {
		fixtureExec(`UPDATE capabilities.providers SET provider_type = 'connector' WHERE provider_id = $1`, provider.ProviderID)
		defer fixtureExec(`UPDATE capabilities.providers SET provider_type = 'service' WHERE provider_id = $1`, provider.ProviderID)
		if _, err := capabilityService.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusOK, AvailabilityStatus: capabilities.AvailabilityStatusAvailable}); err != nil {
			t.Fatal(err)
		}
		got, err := routingService.PlanRoute(ctx, req, callInput)
		if err != nil || got.RouteKind != routing.RouteKindLocal {
			t.Fatalf("unrelated local capability: route=%s err=%v", got.RouteKind, err)
		}
	})
	if _, err := capabilityService.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusUnhealthy, AvailabilityStatus: capabilities.AvailabilityStatusUnavailable, DetailsJSON: json.RawMessage(`{"process_state":"failed"}`)}); err != nil {
		t.Fatal(err)
	}
	t.Run("policy_deny_does_not_dispatch", func(t *testing.T) {
		fixtureExec(`UPDATE identity.actor_node_authorizations SET status = 'disabled' WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		defer fixtureExec(`UPDATE identity.actor_node_authorizations SET status = 'active' WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		got, err := routingService.Call(ctx, req, callInput, "same-node-deny-"+suffix)
		if err != nil || got.ErrorCode != "actor_not_authorized_on_target_node" || got.DispatchMessageID != "" {
			t.Fatalf("denied call status=%s code=%s dispatch=%s err=%v", got.Status, got.ErrorCode, got.DispatchMessageID, err)
		}
		assertMessageCount(t, got.CapabilityCall.CapabilityCallID, "main_to_node", 0)
	})
	t.Run("approval_and_grant_remain_required", func(t *testing.T) {
		var originalLevel int
		if err := sqlDB.QueryRowContext(ctx, `SELECT execution_authorization_level FROM capabilities.capability_endpoints WHERE capability_endpoint_id = $1`, plan.CapabilityEndpointID).Scan(&originalLevel); err != nil {
			t.Fatal(err)
		}
		fixtureExec(`UPDATE capabilities.capability_endpoints SET execution_authorization_level = 5 WHERE capability_endpoint_id = $1`, plan.CapabilityEndpointID)
		defer fixtureExec(`UPDATE capabilities.capability_endpoints SET execution_authorization_level = $2 WHERE capability_endpoint_id = $1`, plan.CapabilityEndpointID, originalLevel)
		fixtureExec(`UPDATE identity.actor_node_authorizations SET authorization_level = 1 WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		defer fixtureExec(`UPDATE identity.actor_node_authorizations SET authorization_level = 5 WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		input := callInput
		input.RequestApproval = true
		got, err := routingService.Call(ctx, req, input, "same-node-approval-"+suffix)
		if err != nil || got.Status != routing.CapabilityCallStatusApprovalRequired || got.ApprovalID == "" || got.DispatchMessageID != "" {
			t.Fatalf("approval status=%s approval=%s dispatch=%s err=%v", got.Status, got.ApprovalID, got.DispatchMessageID, err)
		}
		assertMessageCount(t, got.CapabilityCall.CapabilityCallID, "main_to_node", 0)
		fixtureExec(`UPDATE identity.actor_node_authorizations SET authorization_level = 5 WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		approved, err := routingService.Policy.DecideApproval(ctx, req, policy.ApprovalDecisionInput{ApprovalRef: got.ApprovalID, Decision: policy.ApprovalDecisionApprove, DecisionReason: "isolated routing fixture"})
		if err != nil || approved.Grant == nil {
			t.Fatalf("fixture approval: %v", err)
		}
		fixtureExec(`UPDATE identity.actor_node_authorizations SET authorization_level = 1 WHERE actor_id = $1 AND node_id = $2`, req.ActorID, nodeID)
		granted, err := routingService.Call(ctx, req, callInput, "same-node-grant-"+suffix)
		if err != nil || granted.Status != routing.CapabilityCallStatusDispatched || granted.GrantID != approved.Grant.GrantID {
			t.Fatalf("grant-backed call status=%s grant=%s err=%v", granted.Status, granted.GrantID, err)
		}
		var grantPayload routing.RemoteDispatchPayload
		var raw []byte
		if err := sqlDB.QueryRowContext(ctx, `SELECT payload_json FROM communication.messages WHERE communication_message_id = $1`, granted.DispatchMessageID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &grantPayload); err != nil || grantPayload.GrantID != approved.Grant.GrantID {
			t.Fatalf("dispatch grant not preserved: %v", err)
		}
	})
	t.Run("archived_project_stays_fenced", func(t *testing.T) {
		fixtureExec(`UPDATE projects.projects SET status = 'archived' WHERE project_id = $1`, project.ProjectID)
		defer fixtureExec(`UPDATE projects.projects SET status = 'active' WHERE project_id = $1`, project.ProjectID)
		for _, scope := range []string{project.ProjectScopeKey, req.ScopeID} {
			input := callInput
			input.ScopeRef = scope
			if _, err := routingService.Call(ctx, req, input, "same-node-archived-"+suffix); !projects.IsProjectRuntimeArchived(err) {
				t.Fatalf("archived origin/provider scope was not fenced: %v", err)
			}
		}
	})
	outcome, err := routingService.Call(ctx, req, routing.CapabilityCallInput{Target: target, OriginNodeRef: nodeID, ScopeRef: project.ProjectScopeKey, Input: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"test":"service_registry"}`)}, "service-e2e-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RouteKind != routing.RouteKindRemote || outcome.DispatchMessageID == "" || outcome.Route.Status != routing.RouteStatusDispatched {
		t.Fatalf("same-node route=%s status=%s dispatch=%q error=%s plan=%s", outcome.Route.RouteKind, outcome.Status, outcome.DispatchMessageID, outcome.ErrorCode, plan.RouteKind)
	}
	assertMessageCount(t, outcome.CapabilityCall.CapabilityCallID, "main_to_node", 1)
	var payloadJSON []byte
	var queueNodeID string
	if err := sqlDB.QueryRowContext(ctx, `SELECT node_id, payload_json FROM communication.messages WHERE communication_message_id = $1`, outcome.DispatchMessageID).Scan(&queueNodeID, &payloadJSON); err != nil {
		t.Fatal(err)
	}
	if queueNodeID != nodeID {
		t.Fatalf("dispatch queued for %s, want authenticated owner %s", queueNodeID, nodeID)
	}
	var dispatch routing.RemoteDispatchPayload
	if err := json.Unmarshal(payloadJSON, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.CapabilityEndpointID != plan.CapabilityEndpointID || dispatch.RuntimeBinding == nil || dispatch.RuntimeBinding.RuntimeKind != capabilities.RuntimeKindServiceManager {
		t.Fatalf("dispatch did not use persisted endpoint/runtime binding: %#v", dispatch)
	}
	if dispatch.TargetNodeID != nodeID || dispatch.OriginNodeID != nodeID || dispatch.ActorID != req.ActorID || dispatch.ScopeID != project.ProjectScopeID || dispatch.RouteID != outcome.Route.RouteID || dispatch.CapabilityCallID != outcome.CapabilityCall.CapabilityCallID || dispatch.PolicyDecisionID != outcome.PolicyDecisionID || dispatch.IdempotencyKey != "service-e2e-"+suffix {
		t.Fatalf("dispatch changed persisted call/owner/policy/idempotency identity: %#v", dispatch)
	}
	var runtimeConfig struct {
		AllowlistKey string `json:"allowlist_key"`
		Operation    string `json:"operation"`
	}
	if err := json.Unmarshal(dispatch.RuntimeBinding.RuntimeConfigJSON, &runtimeConfig); err != nil || runtimeConfig.AllowlistKey != record.Key || runtimeConfig.Operation != string(serviceregistry.OperationStatus) {
		t.Fatalf("runtime binding config = %s err=%v", dispatch.RuntimeBinding.RuntimeConfigJSON, err)
	}
	credential, err := nodes.NewService(sqlDB).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: nodeKey, Reason: "service registry integration"})
	if err != nil {
		t.Fatal(err)
	}
	completedAt := time.Now().UTC()
	resultInput := routing.RemoteResultInput{
		NodeRef: nodeKey, CredentialToken: credential.CredentialToken,
		Payload: routing.RemoteResultPayload{
			RouteID: dispatch.RouteID, CapabilityCallID: dispatch.CapabilityCallID, NodeID: nodeID,
			ProviderID: provider.ProviderID, ProviderAddress: provider.CompactAddress,
			CapabilityEndpointID: dispatch.CapabilityEndpointID, CapabilityAddress: dispatch.CapabilityAddress,
			Operation: "service.status", ExecutionStatus: routing.CapabilityCallStatusCompleted,
			ResultJSON:     json.RawMessage(`{"operation":"status","success":true,"process_state":"running","message":"observed"}`),
			ResultRefsJSON: json.RawMessage(`{}`), RuntimeMetadataJSON: json.RawMessage(`{"runtime":"service_manager"}`), CompletedAt: &completedAt,
		},
		Metadata: json.RawMessage(`{"test":"service_registry"}`),
	}
	// Results below are supplied fixture observations, never evidence that a
	// host service manager executed or that an application was installed.
	otherCredential, err := nodes.NewService(sqlDB).IssueNodeCredential(ctx, req, nodes.IssueNodeCredentialInput{NodeRef: req.OriginNodeID, Reason: "isolated wrong-owner result fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*routing.RemoteResultInput){
		"invalid credential":          func(in *routing.RemoteResultInput) { in.CredentialToken = "invalid-fixture-token" },
		"credential for another node": func(in *routing.RemoteResultInput) { in.CredentialToken = otherCredential.CredentialToken },
		"wrong payload node":          func(in *routing.RemoteResultInput) { in.Payload.NodeID = req.OriginNodeID },
		"authenticated wrong owner": func(in *routing.RemoteResultInput) {
			in.NodeRef, in.Payload.NodeID, in.CredentialToken = req.OriginNodeID, req.OriginNodeID, otherCredential.CredentialToken
		},
		"wrong provider": func(in *routing.RemoteResultInput) { in.Payload.ProviderID = "prov_wrong_fixture" },
		"wrong endpoint": func(in *routing.RemoteResultInput) { in.Payload.CapabilityEndpointID = "endp_wrong_fixture" },
	} {
		t.Run(name, func(t *testing.T) {
			input := resultInput
			mutate(&input)
			if _, err := routingService.IngestRemoteResult(ctx, req, input); err == nil {
				t.Fatal("invalid result accepted")
			}
			assertMessageCount(t, dispatch.CapabilityCallID, "node_to_main", 0)
			var callStatus string
			if err := sqlDB.QueryRowContext(ctx, `SELECT status FROM routing.capability_calls WHERE capability_call_id = $1`, dispatch.CapabilityCallID).Scan(&callStatus); err != nil {
				t.Fatal(err)
			}
			inspection, err := capabilityService.InspectProvider(ctx, provider.ProviderID)
			if err != nil || callStatus != routing.CapabilityCallStatusDispatched || inspection.Health == nil || inspection.Health.HealthStatus != capabilities.HealthStatusUnhealthy || inspection.Health.AvailabilityStatus != capabilities.AvailabilityStatusUnavailable {
				t.Fatalf("rejected result mutated call/observation: status=%s health=%#v err=%v", callStatus, inspection.Health, err)
			}
		})
	}
	remoteResult, err := routingService.IngestRemoteResult(ctx, req, resultInput)
	if err != nil {
		t.Fatal(err)
	}
	if remoteResult.Status != routing.CapabilityCallStatusCompleted {
		t.Fatalf("remote result = %#v", remoteResult)
	}
	if remoteResult.Route.RouteID != dispatch.RouteID || remoteResult.CapabilityCall.CapabilityCallID != dispatch.CapabilityCallID {
		t.Fatal("result completed different durable route/call identities")
	}
	t.Run("authenticated_result_replay", func(t *testing.T) {
		replayed, err := routingService.IngestRemoteResult(ctx, req, resultInput)
		if err != nil || !replayed.Idempotent || replayed.Route.RouteID != remoteResult.Route.RouteID || replayed.CapabilityCall.CapabilityCallID != remoteResult.CapabilityCall.CapabilityCallID || replayed.MessageID != remoteResult.MessageID {
			t.Fatalf("replay changed durable identities: %v", err)
		}
		assertMessageCount(t, dispatch.CapabilityCallID, "node_to_main", 1)
	})
	t.Run("conflicting_result_replay", func(t *testing.T) {
		conflict := resultInput
		conflict.Payload.ResultJSON = json.RawMessage(`{"operation":"status","success":true,"process_state":"stopped"}`)
		if _, err := routingService.IngestRemoteResult(ctx, req, conflict); err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
			t.Fatalf("conflicting replay should fail idempotency: %v", err)
		}
		conflict.IdempotencyKey = "different-result-key-" + suffix
		if _, err := routingService.IngestRemoteResult(ctx, req, conflict); err == nil || !strings.Contains(err.Error(), "conflicting terminal result") {
			t.Fatalf("new message key must not replace terminal result: %v", err)
		}
		assertMessageCount(t, dispatch.CapabilityCallID, "node_to_main", 1)
	})
	inspection, err := capabilityService.InspectProvider(ctx, provider.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Health == nil || inspection.Health.HealthStatus != capabilities.HealthStatusOK || inspection.Health.AvailabilityStatus != capabilities.AvailabilityStatusAvailable || !strings.Contains(string(inspection.Health.DetailsJSON), `"process_state": "running"`) && !strings.Contains(string(inspection.Health.DetailsJSON), `"process_state":"running"`) {
		t.Fatalf("manager observation was not persisted: %#v", inspection.Health)
	}
}
