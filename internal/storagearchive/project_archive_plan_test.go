package storagearchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projectactivation"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagecatalog"
)

const projectArchiveAdapterOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAY"

type projectArchiveAdapterFixtureFile struct {
	SchemaVersion string                         `json:"schema_version"`
	Cases         []projectArchiveAdapterFixture `json:"cases"`
}

type projectArchiveAdapterFixture struct {
	Key             string   `json:"key"`
	OwnerNode       string   `json:"owner_node"`
	RootKind        string   `json:"root_kind"`
	RepositoryCount int      `json:"repository_count"`
	RuntimeFacets   []string `json:"runtime_facets"`
}

func TestProjectPhysicalArchivePlanFixtureMatrix(t *testing.T) {
	fixtures := loadProjectArchiveAdapterFixtures(t)
	if fixtures.SchemaVersion != "storage.project_archive_adapter_fixtures.v1" || len(fixtures.Cases) != 5 {
		t.Fatalf("unexpected fixture catalog: %#v", fixtures)
	}
	for _, fixture := range fixtures.Cases {
		fixture := fixture
		t.Run(fixture.Key, func(t *testing.T) {
			environment := newProjectArchivePlanEnvironment(t, fixture)
			plan, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), fixture.Key, ProjectPhysicalArchivePlanInput{
				OperationID: projectArchiveAdapterOperationID,
				Reason:      "archive completed project",
				PlannedAt:   time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
			})
			if fixture.OwnerNode != "main" || fixture.RootKind != "canonical" {
				if !errors.Is(err, projects.ErrProjectArchiveUnsupportedCustody) {
					t.Fatalf("error = %v, want typed unsupported_custody", err)
				}
				if environment.workspace.calls != 0 || len(environment.activation.inputs) != 0 {
					t.Fatalf("unsupported custody reached planners: workspace=%d deactivations=%d", environment.workspace.calls, len(environment.activation.inputs))
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanProjectPhysicalArchive returned error: %v", err)
			}
			if plan.Custody.CustodyKind != projects.ProjectArchiveCanonicalCustody || plan.Custody.RepositoryMemberCount != fixture.RepositoryCount {
				t.Fatalf("custody binding = %#v", plan.Custody)
			}
			if plan.Workspace.Source.Path.AbsolutePath != environment.canonicalRoot || plan.Workspace.Destination.Path.RelativePath != "archive/projects/"+fixture.Key+"/project" {
				t.Fatalf("workspace paths = %#v -> %#v", plan.Workspace.Source.Path, plan.Workspace.Destination.Path)
			}
			gotFacets := make([]string, 0, len(plan.Deactivations))
			for _, deactivation := range plan.Deactivations {
				gotFacets = append(gotFacets, deactivation.Input.Facet)
				if !deactivation.Input.DryRun || deactivation.Input.ProjectRoot != environment.registeredRoot || deactivation.Input.Reason != plan.Workspace.Reason {
					t.Fatalf("unbound deactivation input: %#v", deactivation.Input)
				}
			}
			if !reflect.DeepEqual(gotFacets, fixture.RuntimeFacets) {
				t.Fatalf("deactivation facets = %#v, want %#v", gotFacets, fixture.RuntimeFacets)
			}
			if err := ValidateProjectPhysicalArchivePlan(plan, environment.roots); err != nil {
				t.Fatalf("ValidateProjectPhysicalArchivePlan returned error: %v", err)
			}
			payload, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			var roundTripped ProjectPhysicalArchivePlan
			if err := json.Unmarshal(payload, &roundTripped); err != nil {
				t.Fatal(err)
			}
			if err := ValidateProjectPhysicalArchivePlan(roundTripped, environment.roots); err != nil {
				t.Fatalf("serialized project archive plan is not self-contained: %v", err)
			}
			second, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), fixture.Key, ProjectPhysicalArchivePlanInput{
				OperationID: projectArchiveAdapterOperationID,
				Reason:      "archive completed project",
				PlannedAt:   time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
			})
			if err != nil || second.PlanDigest != plan.PlanDigest {
				t.Fatalf("equivalent plan digest changed: first=%s second=%s err=%v", plan.PlanDigest, second.PlanDigest, err)
			}
		})
	}
}

func TestProjectPhysicalArchivePlanDigestBindsEveryEvidenceClass(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-many", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 3,
		RuntimeFacets: []string{"scripts", "workflows", "connectors", "schedules", "direct_events", "watched_roots", "modules", "services"},
	})
	base, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-many", ProjectPhysicalArchivePlanInput{
		OperationID: projectArchiveAdapterOperationID,
		Reason:      "archive completed project",
		PlannedAt:   time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*ProjectPhysicalArchivePlan)
	}{
		{name: "registration revision", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Registration.Registration.RegistrationRevision++ }},
		{name: "request identity", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Request.OriginNodeID = "node_changed" }},
		{name: "repository source", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Repository.Source.SourceRevision++ }},
		{name: "repository member", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Repository.Members[0].ObservationRevision++ }},
		{name: "runtime surface", mutate: func(plan *ProjectPhysicalArchivePlan) {
			plan.Registration.ScriptExposures[0].ActivationStatus = "disabled"
		}},
		{name: "watched root", mutate: func(plan *ProjectPhysicalArchivePlan) {
			plan.Registration.WatchedRootRegistrations[0].WorkerKey = "changed"
		}},
		{name: "deactivation input", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Deactivations[0].Input.Reason = "changed" }},
		{name: "deactivation action", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Deactivations[0].Actions[0].Status = "changed" }},
		{name: "service control identity", mutate: func(plan *ProjectPhysicalArchivePlan) {
			for index := range plan.Deactivations {
				if plan.Deactivations[index].Input.Facet != "services" {
					continue
				}
				metadata, err := projectactivation.DecodeProjectArchiveServiceActionMetadata(plan.Deactivations[index].Actions[0].Metadata)
				if err != nil {
					t.Fatal(err)
				}
				metadata.ProviderID = "prov_01ARZ3NDEKTSV4RRFFQ69G5FAX"
				plan.Deactivations[index].Actions[0].Metadata, _ = json.Marshal(metadata)
				return
			}
			t.Fatal("service deactivation missing from full fixture")
		}},
		{name: "source inventory", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Workspace.Inventory.TotalBytes++ }},
		{name: "workspace plan", mutate: func(plan *ProjectPhysicalArchivePlan) { plan.Workspace.Reason = "changed" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneProjectPhysicalArchivePlan(t, base)
			test.mutate(&changed)
			digest, err := ProjectPhysicalArchivePlanDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if digest == base.PlanDigest {
				t.Fatalf("%s did not affect project archive plan digest", test.name)
			}
		})
	}
}

func TestProjectPhysicalArchivePlanRejectsIdentitySubstitution(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1, RuntimeFacets: []string{"scripts", "watched_roots"}})
	tests := []struct {
		name   string
		mutate func(*projects.ProjectRegistrationDetail, *projects.ProjectRepositoryReadModel)
	}{
		{name: "registration project", mutate: func(detail *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			detail.Registration.ProjectID = "project_substituted"
		}},
		{name: "contract id", mutate: func(detail *projects.ProjectRegistrationDetail, _ *projects.ProjectRepositoryReadModel) {
			detail.Registration.Contract = json.RawMessage(strings.Replace(string(detail.Registration.Contract), `"id":"project_canonical_one"`, `"id":"project_substituted"`, 1))
		}},
		{name: "repository registration", mutate: func(_ *projects.ProjectRegistrationDetail, state *projects.ProjectRepositoryReadModel) {
			state.Source.ProjectContractRegistrationID = "project_contract_registration_substituted"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			detail := cloneProjectArchiveTestValue(t, environment.projects.detail)
			repository := cloneProjectArchiveTestValue(t, environment.repositories.state)
			test.mutate(&detail, &repository)
			environment.projects.detail = detail
			environment.repositories.state = repository
			if _, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID}); !errors.Is(err, projects.ErrProjectArchiveInvalidBinding) {
				t.Fatalf("error = %v, want invalid identity binding", err)
			}
		})
	}
}

func TestProjectPhysicalArchivePlanRejectsNonDryRunDeactivation(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1,
		RuntimeFacets: []string{"scripts", "watched_roots"},
	})
	environment.activation.forceNonDryRun = true
	_, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID})
	if err == nil || !strings.Contains(err.Error(), "was not an exact dry-run") {
		t.Fatalf("error = %v, want fail-closed non-dry-run rejection", err)
	}
}

func TestProjectPhysicalArchivePlanRejectsNestedRequestSubstitution(t *testing.T) {
	requestedAt := time.Date(2026, 9, 4, 10, 30, 0, 123456789, time.FixedZone("test", 2*60*60))
	tests := []struct {
		name       string
		mutatePlan func(*WorkspaceArchivePlan)
		want       string
	}{
		{name: "operation id", mutatePlan: func(plan *WorkspaceArchivePlan) {
			plan.OperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
		}, want: "operation_id"},
		{name: "reason", mutatePlan: func(plan *WorkspaceArchivePlan) { plan.Reason = "substituted reason" }, want: "reason"},
		{name: "planned time", mutatePlan: func(plan *WorkspaceArchivePlan) { plan.PlannedAt = plan.PlannedAt.Add(time.Second) }, want: "planned_at"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1, RuntimeFacets: []string{"scripts"}})
			environment.workspace.mutatePlan = test.mutatePlan
			_, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{
				OperationID: projectArchiveAdapterOperationID, Reason: "  requested reason  ", PlannedAt: requestedAt,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want exact %s substitution rejection", err, test.want)
			}
		})
	}
}

func TestProjectPhysicalArchivePlanPreservesGenericDefaultsAndTimeNormalization(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1})
	plan, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorkspaceRequest.OperationID != "" || !plan.WorkspaceRequest.PlannedAt.IsZero() || plan.WorkspaceRequest.Reason != "project archive" || plan.Workspace.OperationID == "" || plan.Workspace.PlannedAt.IsZero() {
		t.Fatalf("generic defaults were not preserved: request=%#v workspace=%#v", plan.WorkspaceRequest, plan.Workspace)
	}

	requestedAt := time.Date(2026, 9, 4, 10, 30, 0, 123456789, time.FixedZone("test", 2*60*60))
	plan, err = environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, PlannedAt: requestedAt})
	if err != nil {
		t.Fatal(err)
	}
	want := requestedAt.UTC().Truncate(time.Microsecond)
	if !plan.WorkspaceRequest.PlannedAt.Equal(want) || !plan.Workspace.PlannedAt.Equal(want) {
		t.Fatalf("planned time = request %s workspace %s, want %s", plan.WorkspaceRequest.PlannedAt, plan.Workspace.PlannedAt, want)
	}
}

func TestProjectPhysicalArchivePlanRejectsContractEntryRace(t *testing.T) {
	tests := []struct {
		name   string
		before func(*testing.T, projectArchivePlanEnvironment)
		want   string
	}{
		{
			name: "stale content",
			before: func(t *testing.T, environment projectArchivePlanEnvironment) {
				if err := os.WriteFile(environment.projects.detail.Registration.ContractPath, []byte(`{"stale":true}`), 0o640); err != nil {
					t.Fatal(err)
				}
			},
			want: "path, type, and hash",
		},
		{
			name: "same bytes replacement",
			before: func(t *testing.T, environment projectArchivePlanEnvironment) {
				path := environment.projects.detail.Registration.ContractPath
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, environment.projects.detail.Registration.Contract, 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			},
			want: "changed between custody authentication and workspace inventory",
		},
		{
			name: "terminal symlink",
			before: func(t *testing.T, environment projectArchivePlanEnvironment) {
				path := environment.projects.detail.Registration.ContractPath
				target := filepath.Join(filepath.Dir(path), "replacement.json")
				if err := os.WriteFile(target, environment.projects.detail.Registration.Contract, 0o640); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Base(target), path); err != nil {
					t.Fatal(err)
				}
			},
			want: "path, type, and hash",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1})
			environment.workspace.beforePlan = func() { test.before(t, environment) }
			_, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want contract race rejection containing %q", err, test.want)
			}
		})
	}
}

func TestProjectPhysicalArchivePlanRejectsMissingOrDuplicateContractInventoryEvidence(t *testing.T) {
	tests := []struct {
		name       string
		mutatePlan func(*WorkspaceArchivePlan)
	}{
		{name: "missing", mutatePlan: removeProjectArchiveContractInventoryEntry},
		{name: "duplicate", mutatePlan: duplicateProjectArchiveContractInventoryEntry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1})
			environment.workspace.mutatePlan = test.mutatePlan
			if _, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID}); err == nil {
				t.Fatal("mutated contract inventory evidence was accepted")
			}
		})
	}
}

func TestProjectPhysicalArchivePlanDetectsRuntimeDriftDuringPlanning(t *testing.T) {
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{
		Key: "canonical-one", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 1,
		RuntimeFacets: []string{"scripts", "watched_roots"},
	})
	environment.activation.after = func() {
		environment.projects.detail.Project.Project.UpdatedAt = environment.projects.detail.Project.Project.UpdatedAt.Add(time.Second)
	}
	_, err := environment.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), "canonical-one", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID})
	if err == nil || !strings.Contains(err.Error(), "state changed while planning") {
		t.Fatalf("error = %v, want concurrent runtime drift rejection", err)
	}
}

func TestInspectHistoricalProjectTarManifestIsExplicitReadOnlyEvidence(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("testdata", "project_runtime_manifest_historical_tar_v06.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "historical.json")
	if err := os.WriteFile(manifestPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	detail := projectRuntimeArchiveDetail()
	detail.Project.Project.ArchiveState = json.RawMessage(`{"runtime_manifest_path":` + strconvQuote(manifestPath) + `}`)
	service := NewProjectRuntimeService(ProjectRuntimeDeps{Projects: &fakeProjectRuntimeProjectService{detail: detail}})
	result, err := service.InspectProjectArchive(context.Background(), detail.Project.Project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RuntimeManifest == nil || result.HistoricalEvidence == nil {
		t.Fatalf("historical manifest was not read: %#v", result)
	}
	if result.HistoricalEvidence.Classification != projectHistoricalEvidenceTarSnapshot || !result.HistoricalEvidence.ReadOnly || result.HistoricalEvidence.PhysicalMoveEvidence {
		t.Fatalf("historical evidence classification = %#v", result.HistoricalEvidence)
	}
	if result.HistoricalEvidence.FilesystemSnapshotPath != result.RuntimeManifest.FilesystemSnapshotPath {
		t.Fatalf("historical tar path was not preserved")
	}
}

type projectArchivePlanEnvironment struct {
	roots          TrustedWorkspaceRoots
	canonicalRoot  string
	registeredRoot string
	projects       *projectArchivePlanProjectService
	repositories   *projectArchivePlanRepositoryService
	activation     *projectArchivePlanActivationService
	workspace      *projectArchivePlanWorkspacePlanner
	quiescence     *projectArchiveTestRuntimeQuiescence
	service        ProjectRuntimeService
}

func newProjectArchivePlanEnvironment(t *testing.T, fixture projectArchiveAdapterFixture) projectArchivePlanEnvironment {
	t.Helper()
	base := t.TempDir()
	roots := TrustedWorkspaceRoots{BoxRoot: filepath.Join(base, "box"), StorageRoot: filepath.Join(base, "storage")}
	canonicalRoot := filepath.Join(roots.BoxRoot, "Projects", fixture.Key)
	registeredRoot := canonicalRoot
	if fixture.RootKind == "external" {
		registeredRoot = filepath.Join(base, "external", fixture.Key)
	}
	for _, directory := range []string{canonicalRoot, registeredRoot, filepath.Join(roots.StorageRoot, "archive", "projects"), filepath.Join(registeredRoot, ".loom")} {
		if err := os.MkdirAll(directory, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, "README.md"), []byte("disposable project fixture\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(registeredRoot, ".loom", "project.json")
	detail, repository := projectArchivePlanFixtureState(fixture, registeredRoot, contractPath)
	if err := os.WriteFile(contractPath, detail.Registration.Contract, 0o640); err != nil {
		t.Fatal(err)
	}
	projectService := &projectArchivePlanProjectService{detail: detail}
	repositoryService := &projectArchivePlanRepositoryService{state: repository}
	projectService.repository = repositoryService
	activationService := &projectArchivePlanActivationService{detail: detail, projects: projectService}
	workspaceService := &projectArchivePlanWorkspacePlanner{planner: WorkspaceMoveService{
		Roots: roots, Catalog: projectArchivePlanCatalog{},
		Journal:       &moveFakeJournal{refURIs: map[string]string{}, entryPaths: map[string][2]string{}, restoreRecords: map[string]storagecatalog.WorkspaceArchiveJournalRecord{}},
		ManifestKeyID: "workspace.archive.project-test",
		ManifestKey:   func(context.Context, string) ([]byte, error) { return []byte("0123456789abcdef0123456789abcdef"), nil },
		Now:           func() time.Time { return time.Date(2026, 9, 4, 10, 33, 0, 0, time.UTC) },
	}}
	quiescence := &projectArchiveTestRuntimeQuiescence{}
	service := NewProjectRuntimeService(ProjectRuntimeDeps{
		Projects: projectService, RepositoryState: repositoryService, Activation: activationService,
		WorkspaceMove: workspaceService, RuntimeQuiescence: quiescence, WorkspaceRoots: roots,
		Now: func() time.Time { return time.Date(2026, 9, 4, 10, 32, 0, 0, time.UTC) },
	})
	return projectArchivePlanEnvironment{
		roots: roots, canonicalRoot: canonicalRoot, registeredRoot: registeredRoot,
		projects: projectService, repositories: repositoryService, activation: activationService,
		workspace: workspaceService, quiescence: quiescence, service: service,
	}
}

func projectArchivePlanFixtureState(fixture projectArchiveAdapterFixture, root, contractPath string) (projects.ProjectRegistrationDetail, projects.ProjectRepositoryReadModel) {
	projectID := "project_" + strings.ReplaceAll(fixture.Key, "-", "_")
	registrationID := "project_contract_registration_" + strings.ReplaceAll(fixture.Key, "-", "_")
	contract := json.RawMessage(`{"kind":"loom.project","schema_version":"project.contract.v0.4","project":{"id":"` + projectID + `","slug":"` + fixture.Key + `","name":"Fixture","owner_node":"` + fixture.OwnerNode + `","status":"active"}}`)
	detail := projects.ProjectRegistrationDetail{
		Project: projects.ProjectDetail{Project: projects.Project{
			ProjectID: projectID, ProjectScopeID: "scope_" + strings.ReplaceAll(fixture.Key, "-", "_"), ProjectScopeKey: "project:" + fixture.Key,
			Slug: fixture.Key, Name: "Fixture " + fixture.Key, Status: "active", UpdatedAt: time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC),
		}},
		Registration: &projects.ProjectContractRegistration{
			ProjectContractRegistrationID: registrationID, ProjectID: projectID, ProjectRoot: root, ContractPath: contractPath,
			ContractHash: projectArchivePlanTestDigest(contract), ContractSchemaVersion: "project.contract.v0.4", Contract: contract,
			RegistrationStatus: projects.ProjectRegistrationStatusRegistered, ActivationStatus: projects.ProjectActivationStatusBaseActive, RegistrationRevision: 7,
		},
	}
	for _, facet := range fixture.RuntimeFacets {
		switch facet {
		case "scripts":
			detail.ScriptExposures = []projects.ProjectScriptExposure{{ProjectScriptExposureID: "script_exposure_" + projectID, ProjectID: projectID, ScriptKey: "build", ActivationStatus: projects.ProjectScriptExposureStatusActive}}
		case "workflows":
			detail.WorkflowRegistrations = []projects.ProjectWorkflowRegistration{{ProjectWorkflowRegistrationID: "workflow_registration_" + projectID, ProjectID: projectID, WorkflowKey: "release", ActivationStatus: projects.ProjectWorkflowRegistrationStatusActive}}
		case "connectors":
			detail.ConnectorRegistrations = []projects.ProjectConnectorRegistration{{ProjectConnectorRegistrationID: "connector_registration_" + projectID, ProjectID: projectID, ConnectorKey: "source", ActivationStatus: projects.ProjectConnectorRegistrationStatusActive}}
		case "schedules":
			detail.ScheduleRegistrations = []projects.ProjectScheduleRegistration{{ProjectScheduleRegistrationID: "schedule_registration_" + projectID, ProjectID: projectID, ScheduleKey: "daily", ActivationStatus: projects.ProjectScheduleRegistrationStatusActive}}
		case "direct_events":
			detail.DirectEventRegistrations = []projects.ProjectDirectEventRegistration{{ProjectDirectEventRegistrationID: "event_registration_" + projectID, ProjectID: projectID, EventKey: "push", ActivationStatus: projects.ProjectDirectEventRegistrationStatusActive}}
		case "watched_roots":
			detail.WatchedRootRegistrations = []projects.ProjectWatchedRootRegistration{{ProjectWatchedRootRegistrationID: "watched_root_registration_" + projectID, ProjectID: projectID, NodeID: "node_main", OwnerNodeKey: "main", LocalRootKey: "project", BackendRootKey: fixture.Key + "__project", WorkerKey: "worker", ConfigHash: "sha256:" + strings.Repeat("a", 64), ActivationStatus: projects.ProjectWatchedRootRegistrationStatusApplied}}
		case "modules":
			detail.ModuleRegistrations = []projects.ProjectModuleRegistration{{ProjectModuleRegistrationID: "module_registration_" + projectID, ProjectID: projectID, ModuleKey: "app", ActivationStatus: projects.ProjectModuleRegistrationStatusRegistered}}
		case "services":
			detail.Facets = append(detail.Facets, projects.ProjectContractFacet{ProjectContractFacetID: "facet_services_" + projectID, ProjectID: projectID, FacetKey: "services", Enabled: true, Present: true, FacetStatus: projects.ProjectFacetStatusActivated})
		}
	}
	repository := projects.ProjectRepositoryReadModel{
		Project: projects.ProjectRepositoryReadProject{
			ProjectID: projectID, ProjectScopeID: detail.Project.Project.ProjectScopeID, ProjectScopeKey: detail.Project.Project.ProjectScopeKey,
			Slug: fixture.Key, Name: detail.Project.Project.Name, Status: "active", UpdatedAt: detail.Project.Project.UpdatedAt,
		},
		Source: &projects.ProjectRepositoryReadSource{
			ProjectContractRegistrationID: registrationID, ProjectContractSchemaVersion: "project.contract.v0.4", ReposContractSchemaVersion: "repos.contract.v0.4",
			ProjectRoot: root, OwnerNode: fixture.OwnerNode, SemanticDigest: "sha256:" + strings.Repeat("b", 64), LocationDigest: "sha256:" + strings.Repeat("c", 64),
			SourceRevision: 4, RegisteredAt: time.Date(2026, 9, 4, 8, 0, 0, 0, time.UTC),
		},
	}
	for index := 0; index < fixture.RepositoryCount; index++ {
		repository.Members = append(repository.Members, projects.ProjectRepositoryReadMember{
			RepositoryID: "repository_" + string(rune('a'+index)), RepositoryOwnerProjectID: projectID, Key: "repo-" + string(rune('a'+index)), Path: "repo-" + string(rune('a'+index)),
			Role: projects.ProjectRepositoryRoleComponent, MembershipLifecycle: projects.RepositoryLifecycleActive, RepositoryLifecycle: projects.RepositoryLifecycleActive,
			SourceBindingDigest: "sha256:" + strings.Repeat(string(rune('d'+index)), 64), StoredObservationPosture: projects.ProjectRepositoryObservationObserved,
			ObservationRevision: int64(index + 1),
		})
	}
	return detail, repository
}

func loadProjectArchiveAdapterFixtures(t *testing.T) projectArchiveAdapterFixtureFile {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", "project_archive_adapter_fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures projectArchiveAdapterFixtureFile
	if err := json.Unmarshal(payload, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func projectArchivePlanRequest() requestctx.Context {
	return requestctx.Context{ActorID: "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV", OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", CorrelationID: "corr_project_archive_plan"}
}

type projectArchivePlanProjectService struct {
	mu          sync.Mutex
	operationMu sync.Mutex
	detail      projects.ProjectRegistrationDetail
	repository  *projectArchivePlanRepositoryService
	eventCount  int
	transitions int
	lockKeys    []string
}

func (s *projectArchivePlanProjectService) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneProjectArchiveTestValue(nil, s.detail), nil
}

func (*projectArchivePlanProjectService) UpdateProjectArchiveState(context.Context, requestctx.Context, string, json.RawMessage) (projects.Project, error) {
	return projects.Project{}, errors.New("unexpected project mutation")
}

func (s *projectArchivePlanProjectService) AcquireProjectArchiveLocks(_ context.Context, keys []string) (func() error, error) {
	s.operationMu.Lock()
	s.mu.Lock()
	s.lockKeys = append([]string(nil), keys...)
	s.mu.Unlock()
	return func() error { s.operationMu.Unlock(); return nil }, nil
}

func (s *projectArchivePlanProjectService) TransitionProjectPhysicalArchive(_ context.Context, _ requestctx.Context, input projects.ProjectPhysicalArchiveTransitionInput) (projects.ProjectPhysicalArchiveTransitionResult, error) {
	if err := projects.ValidateProjectPhysicalArchiveState(input.State); err != nil {
		return projects.ProjectPhysicalArchiveTransitionResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions++
	current, hasCurrent := projects.ParseProjectPhysicalArchiveState(s.detail.Project.Project.ArchiveState)
	if hasCurrent && (current.OperationID != input.State.OperationID || current.PlanDigest != input.State.PlanDigest) {
		return projects.ProjectPhysicalArchiveTransitionResult{}, errors.New("project archive conflict")
	}
	if hasCurrent && current.Phase == projects.ProjectArchivePhaseComplete {
		return projects.ProjectPhysicalArchiveTransitionResult{Project: s.detail.Project.Project, State: current, Replay: true}, nil
	}
	payload, _ := json.Marshal(input.State)
	s.detail.Project.Project.ArchiveState = payload
	transition := projects.ProjectPhysicalArchiveTransitionResult{State: input.State}
	if input.State.Phase == projects.ProjectArchivePhaseComplete {
		s.detail.Project.Project.Status = "archived"
		if s.detail.Registration != nil {
			s.detail.Registration.RegistrationStatus = projects.ProjectRegistrationStatusArchived
			s.detail.Registration.ActivationStatus = projects.ProjectActivationStatusInactive
		}
		if s.repository != nil {
			s.repository.mu.Lock()
			s.repository.state.Project.Status = "archived"
			for index := range s.repository.state.Members {
				s.repository.state.Members[index].MembershipLifecycle = projects.RepositoryLifecycleArchived
				if s.repository.state.Members[index].RepositoryOwnerProjectID == input.State.ProjectID {
					s.repository.state.Members[index].RepositoryLifecycle = projects.RepositoryLifecycleArchived
				}
			}
			s.repository.mu.Unlock()
		}
		s.eventCount++
		transition.EventID = "event_project_archive_test"
	}
	transition.Project = s.detail.Project.Project
	return transition, nil
}

type projectArchivePlanRepositoryService struct {
	mu    sync.Mutex
	state projects.ProjectRepositoryReadModel
}

func (s *projectArchivePlanRepositoryService) ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := cloneProjectArchiveTestValue(nil, s.state)
	if s.state.Source != nil && clone.Source != nil {
		clone.Source.ProjectRoot = s.state.Source.ProjectRoot
	}
	return clone, nil
}

type projectArchivePlanActivationService struct {
	detail         projects.ProjectRegistrationDetail
	inputs         []projects.DeactivateProjectInput
	forceNonDryRun bool
	after          func()
	projects       *projectArchivePlanProjectService
}

func (s *projectArchivePlanActivationService) Deactivate(_ context.Context, req requestctx.Context, _ string, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	return s.deactivate(req, input)
}

func (s *projectArchivePlanActivationService) DeactivateForPhysicalArchive(_ context.Context, req requestctx.Context, _ string, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	return s.deactivate(req, input)
}

func (s *projectArchivePlanActivationService) deactivate(req requestctx.Context, input projects.DeactivateProjectInput) (projects.ProjectDeactivationResult, error) {
	s.inputs = append(s.inputs, input)
	if s.after != nil {
		s.after()
		s.after = nil
	}
	status := "would_disable"
	detail := cloneProjectArchiveTestValue(nil, s.detail)
	dryRun := !s.forceNonDryRun
	if !input.DryRun && !s.forceNonDryRun {
		status = "disabled"
		dryRun = false
		if s.projects != nil {
			s.projects.mu.Lock()
			setProjectArchiveFixtureFacetDisabled(&s.projects.detail, input.Facet, req, input.Reason)
			detail = cloneProjectArchiveTestValue(nil, s.projects.detail)
			s.projects.mu.Unlock()
		}
	}
	actionKind, actionKey, actionRef := input.Facet, input.Facet+"-one", ""
	if input.Facet == "watched_roots" {
		actionKind, actionKey, actionRef = "watched_root", "project", s.detail.WatchedRootRegistrations[0].BackendRootKey
	}
	if input.Facet == "services" {
		actionKind, actionKey, actionRef = "service", "service-one", "main@service-one"
	}
	action := projects.ProjectDeactivationAction{Key: actionKey, Kind: actionKind, Ref: actionRef, Status: status, Summary: "Disable bound " + input.Facet + "."}
	if input.Facet == "services" {
		action.Metadata, _ = json.Marshal(projectactivation.ProjectArchiveServiceActionMetadata{
			SchemaVersion: projectactivation.ProjectArchiveServiceActionMetadataSchemaVersion,
			OwnerNode:     "main", ProviderKey: "service-one", ProviderAddress: "main@service-one",
			ProviderID: "prov_01ARZ3NDEKTSV4RRFFQ69G5FAV", RuntimeProfileDigest: "sha256:" + strings.Repeat("c", 64),
			AllowlistKey: "service-one", Manager: "systemd", Unit: "loom-service-one.service",
		})
	}
	return projects.ProjectDeactivationResult{
		Detail: detail, Facet: input.Facet, DryRun: dryRun, Changed: true,
		Actions: []projects.ProjectDeactivationAction{action},
	}, nil
}

func setProjectArchiveFixtureFacetDisabled(detail *projects.ProjectRegistrationDetail, facet string, req requestctx.Context, reason string) {
	at := time.Date(2026, 9, 4, 10, 32, 0, 0, time.UTC)
	metadata, _ := json.Marshal(map[string]any{"source": "project.deactivate", "project_id": detail.Project.Project.ProjectID, "project_slug": detail.Project.Project.Slug, "facet": facet, "reason": reason})
	setAudit := func(status *string, actor *string, activatedAt *time.Time, rowMetadata *json.RawMessage, updatedAt *time.Time, disabled string) {
		*status, *actor, *activatedAt, *rowMetadata, *updatedAt = disabled, req.ActorID, at, metadata, at
	}
	switch facet {
	case "scripts":
		for index := range detail.ScriptExposures {
			row := &detail.ScriptExposures[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectScriptExposureStatusDisabled)
		}
	case "workflows":
		for index := range detail.WorkflowRegistrations {
			row := &detail.WorkflowRegistrations[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectWorkflowRegistrationStatusDisabled)
		}
	case "connectors":
		for index := range detail.ConnectorRegistrations {
			row := &detail.ConnectorRegistrations[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectConnectorRegistrationStatusDisabled)
		}
	case "schedules":
		for index := range detail.ScheduleRegistrations {
			row := &detail.ScheduleRegistrations[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectScheduleRegistrationStatusDisabled)
		}
	case "direct_events":
		for index := range detail.DirectEventRegistrations {
			row := &detail.DirectEventRegistrations[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectDirectEventRegistrationStatusDisabled)
		}
	case "watched_roots":
		for index := range detail.WatchedRootRegistrations {
			row := &detail.WatchedRootRegistrations[index]
			actor := req.ActorID
			row.ActivationStatus, row.LastAppliedByActorID, row.LastAppliedAt, row.Metadata, row.UpdatedAt = projects.ProjectWatchedRootRegistrationStatusDisabled, &actor, &at, metadata, at
		}
	case "modules":
		for index := range detail.ModuleRegistrations {
			row := &detail.ModuleRegistrations[index]
			setAudit(&row.ActivationStatus, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, projects.ProjectModuleRegistrationStatusDisabled)
		}
	case "services":
		for index := range detail.Facets {
			if detail.Facets[index].FacetKey == "services" {
				row := &detail.Facets[index]
				row.FacetStatus, row.Metadata, row.UpdatedAt = projects.ProjectFacetStatusDisabled, metadata, at
			}
		}
	}
}

type projectArchiveTestRuntimeQuiescence struct {
	calls  int
	mutate func(*ProjectRuntimeQuiescenceReceipt)
	err    error
}

func (s *projectArchiveTestRuntimeQuiescence) VerifyProjectArchiveRuntimeQuiescence(_ context.Context, _ requestctx.Context, request ProjectRuntimeQuiescenceRequest) (ProjectRuntimeQuiescenceReceipt, error) {
	s.calls++
	if s.err != nil {
		return ProjectRuntimeQuiescenceReceipt{}, s.err
	}
	receipt := ProjectRuntimeQuiescenceReceipt{SchemaVersion: request.SchemaVersion, ProjectID: request.ProjectID, ProjectSlug: request.ProjectSlug, OperationID: request.OperationID, PlanDigest: request.PlanDigest, Evidence: []ProjectRuntimeQuiescenceEvidence{}}
	for index, target := range request.Targets {
		receipt.Evidence = append(receipt.Evidence, ProjectRuntimeQuiescenceEvidence{ProjectRuntimeQuiescenceTarget: target, State: projectRuntimeQuiescenceStopped, FenceState: "active", TargetReceiptID: fmt.Sprintf("target-receipt-%d", index), ReceiptID: "sha256:" + strings.Repeat(string(rune('a'+index)), 64), ObservedAt: time.Date(2026, 9, 4, 10, 32, 0, 0, time.UTC)})
	}
	if s.mutate != nil {
		s.mutate(&receipt)
	}
	return receipt, nil
}

type projectArchivePlanWorkspacePlanner struct {
	planner    WorkspaceMoveService
	calls      int
	beforePlan func()
	mutatePlan func(*WorkspaceArchivePlan)
}

func (s *projectArchivePlanWorkspacePlanner) PlanArchive(ctx context.Context, input WorkspaceArchivePlanInput) (WorkspaceArchivePlan, error) {
	s.calls++
	if s.beforePlan != nil {
		s.beforePlan()
	}
	plan, err := s.planner.PlanArchive(ctx, input)
	if err != nil {
		return WorkspaceArchivePlan{}, err
	}
	if s.mutatePlan != nil {
		s.mutatePlan(&plan)
		if err := SealWorkspaceArchivePlan(&plan); err != nil {
			return WorkspaceArchivePlan{}, err
		}
	}
	return plan, nil
}

func (s *projectArchivePlanWorkspacePlanner) ApplyArchive(ctx context.Context, plan WorkspaceArchivePlan, digest string) (WorkspaceArchiveInspection, error) {
	return s.planner.ApplyArchive(ctx, plan, digest)
}

func (s *projectArchivePlanWorkspacePlanner) InspectArchive(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	return s.planner.InspectArchive(ctx, operationID)
}

func (s *projectArchivePlanWorkspacePlanner) RecoverArchive(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	return s.planner.RecoverArchive(ctx, operationID)
}

type projectArchivePlanCatalog struct{}

func (projectArchivePlanCatalog) ListWorkspaceArchiveCatalogEntries(context.Context, string, string) ([]storagecatalog.EntryDetail, error) {
	return []storagecatalog.EntryDetail{}, nil
}

func cloneProjectPhysicalArchivePlan(t *testing.T, plan ProjectPhysicalArchivePlan) ProjectPhysicalArchivePlan {
	return cloneProjectArchiveTestValue(t, plan)
}

func cloneProjectArchiveTestValue[T any](t *testing.T, value T) T {
	payload, err := json.Marshal(value)
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	var clone T
	if err := json.Unmarshal(payload, &clone); err != nil {
		if t != nil {
			t.Fatal(err)
		}
		panic(err)
	}
	return clone
}

func strconvQuote(value string) string {
	payload, _ := json.Marshal(value)
	return string(payload)
}

func projectArchivePlanTestDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func removeProjectArchiveContractInventoryEntry(plan *WorkspaceArchivePlan) {
	for index, entry := range plan.Inventory.Entries {
		if entry.RelativePath != ".loom/project.json" {
			continue
		}
		plan.Inventory.Entries = append(plan.Inventory.Entries[:index], plan.Inventory.Entries[index+1:]...)
		plan.Inventory.EntryCount--
		plan.Inventory.FileCount--
		plan.Inventory.TotalBytes -= entry.SizeBytes
		plan.Inventory.Digest, _ = noFollowInventoryDigest(plan.Inventory)
		return
	}
}

func duplicateProjectArchiveContractInventoryEntry(plan *WorkspaceArchivePlan) {
	for index, entry := range plan.Inventory.Entries {
		if entry.RelativePath != ".loom/project.json" {
			continue
		}
		plan.Inventory.Entries = append(plan.Inventory.Entries, NoFollowInventoryEntry{})
		copy(plan.Inventory.Entries[index+2:], plan.Inventory.Entries[index+1:])
		plan.Inventory.Entries[index+1] = entry
		plan.Inventory.EntryCount++
		plan.Inventory.FileCount++
		plan.Inventory.TotalBytes += entry.SizeBytes
		plan.Inventory.Digest, _ = noFollowInventoryDigest(plan.Inventory)
		return
	}
}

func declarationArchivePlanEnvironment(t *testing.T) (projectArchivePlanEnvironment, ProjectPhysicalArchivePlan) {
	t.Helper()
	environment := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "declaration-roundtrip", OwnerNode: "main", RootKind: "canonical", RepositoryCount: 0, RuntimeFacets: []string{}})
	registration := environment.projects.detail.Registration
	registration.Contract = json.RawMessage(strings.Replace(string(registration.Contract), "project.contract.v0.4", "project.contract.v0.5", 1))
	registration.ContractSchemaVersion = "project.contract.v0.5"
	registration.ContractPath = filepath.Join(environment.registeredRoot, ".loom", "project.yaml")
	registration.ContractHash = projectArchivePlanTestDigest(registration.Contract)
	if err := os.WriteFile(registration.ContractPath, registration.Contract, 0600); err != nil {
		t.Fatal(err)
	}
	environment.activation.detail = environment.projects.detail
	environment.repositories.state.Source.ProjectContractSchemaVersion = "project.contract.v0.5"
	environment.repositories.state.Source.ReposContractSchemaVersion = "project.contract.v0.5"
	plan, err := environment.service.PlanProjectPhysicalArchive(t.Context(), projectArchivePlanRequest(), "declaration-roundtrip", ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, Reason: "declaration roundtrip proof", PlannedAt: time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return environment, plan
}
func TestDeclarationArchivePlanJSONRoundTrip(t *testing.T) {
	environment, plan := declarationArchivePlanEnvironment(t)
	if !plan.InspectRegisteredServices || len(plan.Deactivations) != 1 || plan.Deactivations[0].Input.Facet != "services" {
		t.Fatal("declaration archive did not inspect registered services")
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip ProjectPhysicalArchivePlan
	if err = json.Unmarshal(raw, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if err = ValidateProjectPhysicalArchivePlan(roundtrip, environment.roots); err != nil {
		t.Fatal(err)
	}
	if roundtrip.PlanDigest != plan.PlanDigest {
		t.Fatal("declaration plan digest changed")
	}
	raw, err = json.Marshal(roundtrip.Repository.Source)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"project_contract_path", "repos_contract_path", "project_contract_digest", "repos_contract_digest"} {
		if strings.Contains(string(raw), key) {
			t.Fatal("private evidence added to public archive repository DTO")
		}
	}
}

func TestDeclarationArchiveHistoricalPlanPreservesValidation(t *testing.T) {
	environment, plan := declarationArchivePlanEnvironment(t)
	plan.InspectRegisteredServices = false
	plan.Deactivations = []ProjectArchiveDeactivationPlan{}
	if err := SealProjectPhysicalArchivePlan(&plan); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProjectPhysicalArchivePlan(plan, environment.roots); err != nil {
		t.Fatal("historical plan invalidated", err)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "inspect_registered_services") {
		t.Fatal("historical encoding changed")
	}
}
