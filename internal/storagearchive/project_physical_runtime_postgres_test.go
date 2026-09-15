package storagearchive

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/serviceregistry"
)

// The host manager is the only simulated execution engine. Its state lives
// outside the payload and is re-opened on every call, including after restart.
type projectAcceptanceManager struct{ root string }

func (m projectAcceptanceManager) Execute(_ context.Context, input serviceregistry.ManagerRequest) (serviceregistry.ManagerResult, error) {
	key, err := os.ReadFile(filepath.Join(m.root, "manager-key"))
	if err != nil || input.AllowlistKey != string(key) {
		return serviceregistry.ManagerResult{}, fmt.Errorf("unexpected simulated manager target")
	}
	if input.Operation != serviceregistry.OperationStatus && input.Operation != serviceregistry.OperationStop {
		return serviceregistry.ManagerResult{}, fmt.Errorf("simulation forbids non-stop mutation")
	}
	if input.Operation == serviceregistry.OperationStop {
		if _, err := os.Stat(filepath.Join(m.root, "fail-stop")); err == nil {
			return serviceregistry.ManagerResult{}, fmt.Errorf("injected manager stop failure")
		}
		if err := os.WriteFile(filepath.Join(m.root, "manager-state"), []byte("stopped"), 0600); err != nil {
			return serviceregistry.ManagerResult{}, err
		}
	}
	raw, err := os.ReadFile(filepath.Join(m.root, "manager-state"))
	return serviceregistry.ManagerResult{Operation: input.Operation, Success: err == nil, ProcessState: serviceregistry.ObservedProcessState(raw)}, err
}

// This substitutes local delivery only: Main still creates the actual routed
// call, the node produces the receipt, and authenticated result ingestion and
// the production verifier independently re-read the persisted terminal state.
type projectAcceptanceDelivery struct {
	routing.Service
	node       nodeagent.ProjectArchiveQuiescenceService
	credential string
	nodeID     string
}

func (d projectAcceptanceDelivery) Call(ctx context.Context, req requestctx.Context, input routing.CapabilityCallInput, key string) (routing.CapabilityCallOutcome, error) {
	outcome, err := d.Service.Call(ctx, req, input, key)
	if err != nil {
		return outcome, err
	}
	if outcome.Route.Status != routing.RouteStatusDispatched || outcome.CapabilityCall.Status != routing.CapabilityCallStatusDispatched {
		return outcome, fmt.Errorf("local delivery requires an actual dispatched call: %s/%s", outcome.Route.Status, outcome.CapabilityCall.Status)
	}
	request, err := projectquiescence.DecodeRequest(outcome.CapabilityCall.InputJSON)
	if err != nil {
		return outcome, err
	}
	receipt, nodeErr := d.node.Quiesce(ctx, request)
	status, result, code, message := routing.CapabilityCallStatusCompleted, json.RawMessage(`{}`), "", ""
	if nodeErr != nil {
		status, code, message = routing.CapabilityCallStatusFailed, "acceptance.node_stop_failed", "disposable owner-node quiescence refused"
	} else {
		result, err = projectquiescence.CanonicalReceiptBytes(receipt)
		if err != nil {
			return outcome, err
		}
	}
	now := time.Now().UTC()
	_, err = d.Service.IngestRemoteResult(ctx, req, routing.RemoteResultInput{
		NodeRef: d.nodeID, CredentialToken: d.credential, IdempotencyKey: ids.NewIdempotencyID(),
		Payload: routing.RemoteResultPayload{RouteID: outcome.Route.RouteID, CapabilityCallID: outcome.CapabilityCall.CapabilityCallID,
			NodeID: d.nodeID, ProviderID: outcome.Route.ProviderID, CapabilityEndpointID: outcome.Route.CapabilityEndpointID,
			ExecutionStatus: status, ResultJSON: result, ResultRefsJSON: json.RawMessage(`{}`), CompletedAt: &now,
			ErrorCode: code, ErrorMessage: message, RuntimeMetadataJSON: json.RawMessage(`{}`)}, Metadata: json.RawMessage(`{}`),
	})
	return outcome, err
}

type projectAcceptanceRuntime struct {
	fixture        projectPostgresAcceptanceFixture
	providerID     string
	scheduleID     string
	workerKey      string
	nodeID         string
	credential     string
	allowlist      string
	nodeRoot       string
	sourceRevision int64
	sourceHistory  int
}

func (r projectAcceptanceRuntime) open(t *testing.T) (*sql.DB, ProjectRuntimeService) {
	t.Helper()
	db, service, workspace := newProjectPostgresAcceptanceService(t, r.fixture.root)
	workspace.FailureHook = func(boundary WorkspaceMoveBoundary) error {
		if boundary == BoundaryAfterIntent || boundary == BoundaryAfterRestoreIntent {
			r.requireStopped(t, db)
		}
		return nil
	}
	service.Activation = projectactivation.NewService(projectactivation.Deps{
		Projects: projects.NewService(db), Capabilities: capabilities.NewService(db), Automation: automation.NewService(db, routing.NewService(db)),
		Allowlists: serviceregistry.FileAllowlistResolver{Path: r.allowlist},
	})
	service.RuntimeQuiescence = NewRoutedProjectRuntimeQuiescenceVerifier(projectAcceptanceDelivery{
		Service: routing.NewService(db), nodeID: r.nodeID, credential: r.credential,
		node: nodeagent.ProjectArchiveQuiescenceService{
			Config: nodeagent.Config{NodeKey: "main", ServiceManager: nodeagent.ServiceManagerConfig{AllowlistPath: r.allowlist}},
			Store:  nodeagent.Store{DataDir: r.nodeRoot}, Manager: projectAcceptanceManager{root: r.nodeRoot},
		},
	})
	return db, service
}

func newProjectAcceptanceRuntime(t *testing.T, suffix string) projectAcceptanceRuntime {
	t.Helper()
	f := newProjectPostgresAcceptanceFixture(t, suffix)
	db, _, _ := newProjectPostgresAcceptanceService(t, f.root)
	defer db.Close()
	ctx := context.Background()
	project := projects.NewService(db)
	registry := capabilities.NewService(db)
	r := projectAcceptanceRuntime{fixture: f, nodeID: f.req.OriginNodeID, nodeRoot: filepath.Join(f.root, "owner-node"), allowlist: filepath.Join(f.root, "allowlist.yaml")}
	if err := os.MkdirAll(r.nodeRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.nodeRoot, "manager-state"), []byte("running"), 0600); err != nil {
		t.Fatal(err)
	}
	// Extend the declared disposable contract through normal registration before
	// any archive intent. Do not patch persisted lifecycle or terminal rows.
	path := filepath.Join(f.active, ".loom", "project.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("  services: true\n  schedules: true\n")...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{".loom/contracts/services", "schedules"} {
		if err := os.MkdirAll(filepath.Join(f.active, folder), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"services.yaml":  "kind: loom.services\nschema_version: services.contract.v0.4\nservices:\n  status: active\n",
		"schedules.yaml": "kind: loom.schedules\nschema_version: schedules.contract.v0.4\nschedules:\n  status: active\n",
	} {
		if err := os.WriteFile(filepath.Join(f.active, ".loom", "contracts", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	f.input, err = projectregistration.BuildInput(projectcontracts.Analyze(f.active), "project-physical-runtime-acceptance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.RegisterProjectContract(ctx, f.req, f.input); err != nil {
		t.Fatal(err)
	}
	detail, err := project.GetProjectRegistrationStatus(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := serviceregistry.BuildRuntimeProfile(serviceregistry.RuntimeProfileInput{
		Manager: serviceregistry.ManagerSystemd, Unit: "loom-acceptance.service", ServiceClass: serviceregistry.ServiceClassProject,
		Operations: []serviceregistry.Operation{serviceregistry.OperationStatus, serviceregistry.OperationStop},
		Health:     serviceregistry.RuntimeHealth{Kind: serviceregistry.HealthKindManager},
	})
	if err != nil {
		t.Fatal(err)
	}
	profileRaw, _ := json.Marshal(profile)
	provider, _, err := registry.EnsureProvider(ctx, f.req, capabilities.RegisterProviderInput{
		ProviderKey: "acceptance-" + suffix, CompactAddress: "main@acceptance-" + suffix, DisplayName: "Disposable service",
		ProviderType: capabilities.ProviderTypeService, NodeRef: r.nodeID, ScopeRef: f.req.ScopeID,
		Version: "1.0.0", Status: capabilities.ProviderStatusActive, RuntimeProfileJSON: profileRaw,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.providerID = provider.ProviderID
	if err := os.WriteFile(filepath.Join(r.nodeRoot, "manager-key"), []byte(provider.ProviderKey), 0600); err != nil {
		t.Fatal(err)
	}
	record, err := serviceregistry.NormalizeAndValidateAllowlistRecord(serviceregistry.AllowlistRecord{
		SchemaVersion: "loom.service_allowlist.v1", Key: provider.ProviderKey, NodeKey: "main", Manager: profile.Manager, Unit: profile.Unit,
		Operations: profile.Operations, LifecyclePolicy: serviceregistry.LifecyclePolicy("service_operations"), Health: serviceregistry.AllowlistHealth{Kind: serviceregistry.HealthKindManager},
		ProjectArchiveIdentity: &serviceregistry.ProjectArchiveServiceIdentity{ProviderID: provider.ProviderID, ProviderKey: provider.ProviderKey, ProviderAddress: provider.CompactAddress, RuntimeProfileDigest: sha256Hex(profileRaw)},
	})
	if err != nil {
		t.Fatal(err)
	}
	allowlistRaw, _ := yaml.Marshal(serviceregistry.Allowlist{Records: []serviceregistry.AllowlistRecord{record}})
	if err := os.WriteFile(r.allowlist, allowlistRaw, 0600); err != nil {
		t.Fatal(err)
	}
	compiled, err := serviceregistry.CompileServiceEndpoints(ctx, f.req, registry, provider, profile, record)
	if err != nil || len(compiled.Endpoints) != 2 {
		t.Fatal("compile active endpoint/binding fixture", err)
	}
	schedule, _, err := automation.NewService(db, routing.NewService(db)).EnsureSchedule(ctx, f.req, automation.CreateScheduleInput{
		ScheduleKey: "physical-" + suffix, DisplayName: "Disposable schedule", TargetCapability: provider.CompactAddress + ".service.status",
		InputJSON: json.RawMessage(`{}`), ScheduleKind: automation.ScheduleKindInterval, ScheduleExpr: "24h", Timezone: "UTC",
		ScopeRef: f.req.ScopeID, ProjectRef: f.projectID, RunAsActorRef: f.req.ActorID, Status: automation.ScheduleStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.scheduleID = schedule.Schedule.ScheduleID
	if _, err := project.UpsertProjectScheduleRegistration(ctx, f.req, projects.UpsertProjectScheduleRegistrationInput{
		ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID, ProjectID: f.projectID, ScheduleKey: "daily", BackendScheduleKey: schedule.Schedule.ScheduleKey,
		ScheduleFolder: "schedules/daily", ScheduleManifestPath: "schedules/daily/schedule.yaml", ScheduleHash: sha256Hex([]byte("disposable")),
		TargetCapability: provider.CompactAddress + ".service.status", AutomationID: schedule.Schedule.AutomationID, ScheduleID: r.scheduleID,
		ActivationStatus: projects.ProjectScheduleRegistrationStatusActive, Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	rootConfig := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "physical-" + suffix})
	configRaw, _ := json.Marshal(rootConfig)
	r.workerKey = noderuntime.WatchedRootWorkerKey(rootConfig.RootKey)
	if err := noderuntime.NewStore(r.nodeRoot).SaveInstance(noderuntime.WorkerInstance{WorkerKey: r.workerKey, Kind: noderuntime.KindWatchedRoot, Enabled: true, LocalRootKey: "repos", ConfigHash: watchedroots.ConfigHash(rootConfig), ConfigJSON: configRaw}); err != nil {
		t.Fatal(err)
	}
	if _, err := project.UpsertProjectWatchedRootRegistration(ctx, f.req, projects.UpsertProjectWatchedRootRegistrationInput{
		ProjectContractRegistrationID: detail.Registration.ProjectContractRegistrationID, ProjectID: f.projectID, NodeID: r.nodeID, OwnerNodeKey: "main",
		LocalRootKey: "repos", BackendRootKey: rootConfig.RootKey, WorkerKey: r.workerKey, SourceKinds: json.RawMessage(`["repos"]`),
		SafeRootKey: "fixture", RootRelativePath: ".", DisplayName: "Disposable watcher", SyncMode: "off", BackupMode: "off", IndexMode: "off", DeleteMode: "off",
		ConfigHash: watchedroots.ConfigHash(rootConfig), ConfigJSON: configRaw, CommandJSON: json.RawMessage(`[]`),
		ActivationStatus: projects.ProjectWatchedRootRegistrationStatusApplied, Metadata: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, facet := range []string{"services", "schedules", "repos"} {
		if err := project.MarkProjectFacetActivated(ctx, f.req, f.projectID, detail.Registration.ProjectContractRegistrationID, facet, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	credential, err := nodes.NewService(db).IssueNodeCredential(ctx, f.req, nodes.IssueNodeCredentialInput{NodeRef: r.nodeID, Reason: "disposable local delivery"})
	if err != nil {
		t.Fatal(err)
	}
	r.credential = credential.CredentialToken
	seedProjectAcceptanceQuiescenceEndpoint(t, db, f.req)
	detail, err = project.GetProjectRegistrationStatus(ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projectArchiveDeactivationFacets(detail), []string{"schedules", "watched_roots", "services"}) {
		t.Fatalf("fixture does not declare every active runtime: %v; facets=%+v", projectArchiveDeactivationFacets(detail), detail.Facets)
	}
	inspection, err := registry.InspectProvider(ctx, r.providerID)
	if err != nil || inspection.Provider.Status != capabilities.ProviderStatusActive || len(inspection.Endpoints) != 2 {
		t.Fatal("fixture provider must start active with two endpoints", err)
	}
	for _, endpoint := range inspection.Endpoints {
		capability, err := registry.InspectCapability(ctx, endpoint.CapabilityEndpointID)
		if err != nil || capability.Endpoint.Status != capabilities.EndpointStatusActive || capability.RuntimeBinding == nil || capability.RuntimeBinding.Status != capabilities.RuntimeBindingStatusActive {
			t.Fatal("fixture capability and binding must start active", err)
		}
	}
	if len(detail.ScheduleRegistrations) != 1 || detail.ScheduleRegistrations[0].ActivationStatus != projects.ProjectScheduleRegistrationStatusActive ||
		len(detail.WatchedRootRegistrations) != 1 || detail.WatchedRootRegistrations[0].ActivationStatus != projects.ProjectWatchedRootRegistrationStatusApplied {
		t.Fatal("fixture schedule and watched-root registrations must start active")
	}
	var scheduleState, automationState string
	if err := db.QueryRowContext(ctx, `SELECT s.status,a.status FROM automation.schedules s JOIN automation.automations a USING(automation_id) WHERE schedule_id=$1`, r.scheduleID).Scan(&scheduleState, &automationState); err != nil || scheduleState != "active" || automationState != "active" {
		t.Fatal("fixture schedule and automation must start active", err)
	}
	instance, err := noderuntime.NewStore(r.nodeRoot).LoadInstance(r.workerKey)
	if err != nil || !instance.Enabled {
		t.Fatal("fixture watched worker must start enabled", err)
	}
	model, err := project.ReadProjectRepositoryState(ctx, f.projectID)
	if err != nil || model.Source == nil || len(model.Members) != 2 {
		t.Fatal("fixture repository source is incomplete", err)
	}
	r.sourceRevision = int64(model.Source.SourceRevision)
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, f.projectID).Scan(&r.sourceHistory); err != nil {
		t.Fatal(err)
	}
	f.before, f.allocation = snapshotWorkspacePayload(t, f.active), allocatedBlocksAndInodes(t, f.active)
	r.fixture = f
	reviewDB, service := r.open(t)
	defer reviewDB.Close()
	r.fixture.review, err = service.ReviewProjectPhysicalArchive(ctx, f.req, f.projectID, ProjectPhysicalArchivePlanInput{Reason: "combined runtime acceptance"})
	if err != nil {
		t.Fatal("runtime review", err)
	}
	return r
}

func (r projectAcceptanceRuntime) requireObservedInactive(t *testing.T, db *sql.DB, lifecycle string) {
	t.Helper()
	project := projects.NewService(db)
	projection, err := (projectstate.Service{Reader: project, LocalNode: "main"}).ObserveProject(context.Background(), r.fixture.projectID)
	if err != nil || projection.Project.Lifecycle != lifecycle || int64(projection.Source.SourceRevision) != r.sourceRevision || len(projection.Members) != 2 {
		t.Fatal("observation changed project/source identity", err)
	}
	for _, member := range projection.Members {
		if member.RepositoryID != r.fixture.repoIDs[0] && member.RepositoryID != r.fixture.repoIDs[1] {
			t.Fatal("observation replaced repository identity")
		}
		if string(member.RepositoryLifecycle) != lifecycle || string(member.MembershipLifecycle) != lifecycle {
			t.Fatal("observation changed repository lifecycle")
		}
	}
	var history int
	if err := db.QueryRow(`SELECT count(*) FROM projects.project_repository_source_history WHERE project_id=$1`, r.fixture.projectID).Scan(&history); err != nil || history != r.sourceHistory {
		t.Fatal("immutable source history changed", err)
	}
	r.requireStopped(t, db)
}

func seedProjectAcceptanceQuiescenceEndpoint(t *testing.T, db *sql.DB, req requestctx.Context) {
	t.Helper()
	ctx := context.Background()
	registry := capabilities.NewService(db)
	provider, _, err := registry.EnsureProvider(ctx, req, capabilities.RegisterProviderInput{ProviderKey: "system", CompactAddress: "workspace/main@system", DisplayName: "Disposable owner-node system", ProviderType: capabilities.ProviderTypeSystem, NodeRef: req.OriginNodeID, ScopeRef: "system", Status: capabilities.ProviderStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	class, _, err := registry.EnsureCapabilityClass(ctx, req, capabilities.RegisterCapabilityClassInput{Namespace: "system", Name: "project.archive.quiesce", Version: "1.0.0", DisplayName: "Quiesce project", Form: capabilities.CapabilityFormCommand, InputSchemaJSON: json.RawMessage(`{"type":"object"}`), OutputSchemaJSON: json.RawMessage(`{"type":"object"}`), DefaultRiskLevel: capabilities.RiskLevelHigh, Status: capabilities.CapabilityClassStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	endpoint, _, err := registry.EnsureCapabilityEndpoint(ctx, req, capabilities.RegisterCapabilityEndpointInput{ProviderRef: provider.ProviderID, CapabilityClassRef: class.CapabilityClassID, EndpointName: "project.archive.quiesce", CompactAddress: "workspace/main@system.project.archive.quiesce", Form: capabilities.CapabilityFormCommand, InputSchemaJSON: json.RawMessage(`{"type":"object"}`), OutputSchemaJSON: json.RawMessage(`{"type":"object"}`), RiskLevel: capabilities.RiskLevelHigh, ExecutionAuthorizationLevel: 5, Status: capabilities.EndpointStatusActive})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, _, err = registry.EnsureEndpointVersion(ctx, req, capabilities.RegisterEndpointVersionInput{CapabilityEndpointRef: endpoint.CapabilityEndpointID, VersionLabel: "1.0.0", ManifestJSON: json.RawMessage(`{"source":"loom-node-agent","handler":"system.project.archive.quiesce","execution":"remote_node","explicit_authorization":true,"slice_enabled":true}`), InputSchemaJSON: endpoint.InputSchemaJSON, OutputSchemaJSON: endpoint.OutputSchemaJSON, RiskLevel: capabilities.RiskLevelHigh, ExecutionAuthorizationLevel: 5, Status: capabilities.EndpointVersionStatusActive, ApprovedByActorID: req.ActorID, ApprovedAt: &now})
	if err != nil {
		t.Fatal(err)
	}
}

func (r projectAcceptanceRuntime) requireStopped(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	provider, err := capabilities.NewService(db).InspectProvider(ctx, r.providerID)
	if err != nil || provider.Provider.Status != capabilities.ProviderStatusDisabled || len(provider.Endpoints) != 2 {
		t.Fatalf("provider is not disabled: status=%s endpoints=%d err=%v", provider.Provider.Status, len(provider.Endpoints), err)
	}
	for _, endpoint := range provider.Endpoints {
		capability, err := capabilities.NewService(db).InspectCapability(ctx, endpoint.CapabilityEndpointID)
		if err != nil || capability.Endpoint.Status != capabilities.EndpointStatusDisabled || capability.RuntimeBinding == nil || capability.RuntimeBinding.Status != capabilities.RuntimeBindingStatusDisabled {
			t.Fatal("capability/binding is not disabled", err)
		}
	}
	var schedule, automationState string
	if err := db.QueryRowContext(ctx, `SELECT s.status,a.status FROM automation.schedules s JOIN automation.automations a USING(automation_id) WHERE schedule_id=$1`, r.scheduleID).Scan(&schedule, &automationState); err != nil || schedule != "disabled" || automationState != "disabled" {
		t.Fatal("schedule/automation is not disabled", schedule, automationState, err)
	}
	detail, err := projects.NewService(db).GetProjectRegistrationStatus(ctx, r.fixture.projectID)
	if err != nil || len(detail.WatchedRootRegistrations) != 1 || detail.WatchedRootRegistrations[0].ActivationStatus != projects.ProjectWatchedRootRegistrationStatusDisabled {
		t.Fatal("watch registration is not disabled", err)
	}
	store := noderuntime.NewStore(r.nodeRoot)
	instance, err := store.LoadInstance(r.workerKey)
	if err != nil || instance.Enabled {
		t.Fatal("owner worker remains enabled", err)
	}
	instance.Enabled = true
	if err := store.SaveInstance(instance); !errors.Is(err, noderuntime.ErrProjectArchiveFenced) {
		t.Fatal("owner worker fence allowed restart", err)
	}
	raw, err := os.ReadFile(filepath.Join(r.nodeRoot, "manager-state"))
	if err != nil || string(raw) != "stopped" {
		t.Fatal("simulated host process is not stopped", err)
	}
	for kind, identity := range map[string]string{projectquiescence.TargetKindService: provider.Provider.ProviderKey, projectquiescence.TargetKindWatchedRoot: r.workerKey} {
		active, err := store.ProjectArchiveFenceActive(kind, identity)
		if err != nil || !active {
			t.Fatal("durable owner fence is absent", kind, err)
		}
	}
}

func TestProjectPhysicalPostgresAcceptanceRuntime(t *testing.T) {
	if os.Getenv("LOOM_PROJECT_PHYSICAL_TEST_DB_URL") == "" {
		t.Skip("requires smoke-owned PostgreSQL")
	}
	for _, failStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop_failure_%t", failStop), func(t *testing.T) {
			r := newProjectAcceptanceRuntime(t, fmt.Sprintf("runtime-%t", failStop))
			ctx := context.Background()
			db, service := r.open(t)
			defer db.Close()
			if failStop {
				if err := os.WriteFile(filepath.Join(r.nodeRoot, "fail-stop"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.ApplyReviewedProjectPhysicalPlan(ctx, r.fixture.req, r.fixture.projectID, ProjectPhysicalApplyRequest{Plan: r.fixture.review, PlanDigest: r.fixture.review.PlanDigest, Confirm: true})
			if failStop {
				if err == nil || !result.MutationBlocked {
					t.Fatal("failed node stop did not fail closed", err)
				}
				if _, err := os.Stat(r.fixture.active); err != nil {
					t.Fatal("payload moved after stop failure", err)
				}
				if _, err := os.Lstat(r.fixture.archived); !os.IsNotExist(err) {
					t.Fatal("archive exists after failed stop", err)
				}
				if err := os.Remove(filepath.Join(r.nodeRoot, "fail-stop")); err != nil {
					t.Fatal(err)
				}
				result, err = service.RecoverReviewedProjectPhysicalKind(ctx, r.fixture.req, r.fixture.projectID, ProjectPhysicalRecoverRequest{OperationID: r.fixture.review.Workspace.OperationID, PlanDigest: r.fixture.review.PlanDigest, Confirm: true}, WorkspaceOperationArchive)
			}
			if err != nil || result.Phase != "complete" {
				t.Fatalf("archive phase=%s err=%v", result.Phase, err)
			}
			r.requireStopped(t, db)
			r.requireObservedInactive(t, db, "archived")
			if !reflect.DeepEqual(snapshotWorkspacePayload(t, r.fixture.archived), r.fixture.before) {
				t.Fatal("payload changed")
			}
			// Close both sides and reconstruct from their durable stores. No setup
			// function re-seeds status or re-enables any resource after this point.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, service = r.open(t)
			defer db.Close()
			r.requireStopped(t, db)
			review, err := service.ReviewProjectPhysicalRestore(ctx, r.fixture.req, r.fixture.projectID, ProjectPhysicalRestorePlanInput{Reason: "inactive runtime acceptance"})
			if err != nil {
				t.Fatal(err)
			}
			result, err = service.ApplyReviewedProjectPhysicalPlan(ctx, r.fixture.req, r.fixture.projectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
			if err != nil || result.Phase != "complete" || result.ActivationState != "inactive" {
				t.Fatal("restore failed or activated runtime", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, service = r.open(t)
			defer db.Close()
			r.requireStopped(t, db)
			result, err = service.RecoverReviewedProjectPhysicalKind(ctx, r.fixture.req, r.fixture.projectID, ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true}, WorkspaceOperationRestore)
			if err != nil || !result.Replay {
				t.Fatal("restored replay failed", err)
			}
			r.requireObservedInactive(t, db, "active")
			if !reflect.DeepEqual(snapshotWorkspacePayload(t, r.fixture.active), r.fixture.before) || !reflect.DeepEqual(allocatedBlocksAndInodes(t, r.fixture.active), r.fixture.allocation) {
				t.Fatal("restore payload fidelity changed")
			}
			for _, event := range []string{"project.archived", "project.restored"} {
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type=$2`, r.fixture.projectID, event).Scan(&count); err != nil || count != 1 {
					t.Fatal("event count mismatch", count, err)
				}
			}
			if _, err := projects.NewService(db).RegisterProjectContract(ctx, r.fixture.req, r.fixture.input); err == nil {
				t.Fatal("registration reopened runtime")
			}
			if _, err := service.Activation.(projectactivation.Service).Activate(ctx, r.fixture.req, r.fixture.projectID, projects.ActivateProjectInput{Facet: "all"}); err == nil {
				t.Fatal("activation bypassed restored fence")
			}
		})
	}
}
