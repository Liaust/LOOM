package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/db"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

func TestServiceRegistryPersistedRemoteDispatchIntegration(t *testing.T) {
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
	req, err := requestctx.ResolveBootstrap(ctx, sqlDB, "corr_service_registry_e2e")
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
		Operations:      []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationRestart},
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
			Runtime:    serviceregistry.RuntimeProfileInput{Manager: serviceregistry.ManagerLaunchd, Unit: unit, ServiceClass: serviceregistry.ServiceClassProject, Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStart, serviceregistry.OperationRestart}, Health: serviceregistry.RuntimeHealth{Kind: serviceregistry.HealthKindManager}},
		},
	}
	capabilityService := capabilities.NewService(sqlDB)
	reconcile, err := serviceregistry.ReconcileProject(ctx, req, capabilityService, serviceregistry.StaticAllowlistResolver{Allowlist: serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{record}}}, slug, []serviceregistry.ProjectRegistrationPlanInput{planInput})
	if err != nil {
		t.Fatal(err)
	}
	if len(reconcile.Registrations) != 1 || len(reconcile.Registrations[0].Endpoints.Endpoints) != 3 {
		t.Fatalf("registration did not persist endpoint intersection: %#v", reconcile)
	}
	provider := reconcile.Registrations[0].Provider
	if _, err := capabilityService.UpsertProviderHealth(ctx, req, provider.ProviderID, capabilities.ProviderHealthInput{HealthStatus: capabilities.HealthStatusUnhealthy, AvailabilityStatus: capabilities.AvailabilityStatusUnavailable, Message: "process failed before recovery", DetailsJSON: json.RawMessage(`{"process_state":"failed"}`)}); err != nil {
		t.Fatal(err)
	}
	target := provider.CompactAddress + ".service.status"
	routingService := routing.NewService(sqlDB)
	plan, err := routingService.PlanRoute(ctx, req, routing.CapabilityCallInput{Target: target, ScopeRef: project.ProjectScopeKey, Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("service-management health recovery route was blocked: %v", err)
	}
	outcome, err := routingService.Call(ctx, req, routing.CapabilityCallInput{Target: target, ScopeRef: project.ProjectScopeKey, Input: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"test":"service_registry"}`)}, "service-e2e-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RouteKind != routing.RouteKindRemote || outcome.DispatchMessageID == "" || outcome.Route.Status != routing.RouteStatusDispatched {
		t.Fatalf("remote route outcome = %#v plan=%#v", outcome, plan)
	}
	var payloadJSON []byte
	if err := sqlDB.QueryRowContext(ctx, `SELECT payload_json FROM communication.messages WHERE communication_message_id = $1`, outcome.DispatchMessageID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var dispatch routing.RemoteDispatchPayload
	if err := json.Unmarshal(payloadJSON, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.CapabilityEndpointID != plan.CapabilityEndpointID || dispatch.RuntimeBinding == nil || dispatch.RuntimeBinding.RuntimeKind != capabilities.RuntimeKindServiceManager {
		t.Fatalf("dispatch did not use persisted endpoint/runtime binding: %#v", dispatch)
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
	remoteResult, err := routingService.IngestRemoteResult(ctx, req, routing.RemoteResultInput{
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if remoteResult.Status != routing.CapabilityCallStatusCompleted {
		t.Fatalf("remote result = %#v", remoteResult)
	}
	inspection, err := capabilityService.InspectProvider(ctx, provider.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Health == nil || inspection.Health.HealthStatus != capabilities.HealthStatusOK || inspection.Health.AvailabilityStatus != capabilities.AvailabilityStatusAvailable || !strings.Contains(string(inspection.Health.DetailsJSON), `"process_state": "running"`) && !strings.Contains(string(inspection.Health.DetailsJSON), `"process_state":"running"`) {
		t.Fatalf("manager observation was not persisted: %#v", inspection.Health)
	}
}

func serviceRegistryMigrationsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
