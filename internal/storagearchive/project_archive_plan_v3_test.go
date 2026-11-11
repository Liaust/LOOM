package storagearchive

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
)

func newProjectV3PlanEnvironment(t *testing.T) (projectArchivePlanEnvironment, *WorkspaceMoveService, *WorkspaceActiveLocation) {
	t.Helper()
	env := newProjectArchivePlanEnvironment(t, projectArchiveAdapterFixture{Key: "canonical-empty", OwnerNode: "main", RootKind: "canonical"})
	location := &WorkspaceActiveLocation{ProjectID: "project_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Path:       workspaceV2Ref(env.roots, WorkspaceRootBox, "Projects/research/renamed-folder"),
		LocationID: "project_location_01ARZ3NDEKTSV4RRFFQ69G5FAV", RegistrationRevision: 7, RepositorySourceRevision: 4}
	if err := os.MkdirAll(filepath.Dir(location.Path.AbsolutePath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(env.canonicalRoot, location.Path.AbsolutePath); err != nil {
		t.Fatal(err)
	}
	detail := &env.projects.detail
	detail.Project.Project.ProjectID, detail.Registration.ProjectID = location.ProjectID, location.ProjectID
	detail.Registration.ProjectRoot = location.Path.AbsolutePath
	detail.Registration.ContractPath = filepath.Join(location.Path.AbsolutePath, ".loom", "project.json")
	var contract map[string]any
	if err := json.Unmarshal(detail.Registration.Contract, &contract); err != nil {
		t.Fatal(err)
	}
	contract["project"].(map[string]any)["id"] = location.ProjectID
	detail.Registration.Contract, _ = json.Marshal(contract)
	detail.Registration.ContractHash = projectArchivePlanTestDigest(detail.Registration.Contract)
	if err := os.WriteFile(detail.Registration.ContractPath, detail.Registration.Contract, 0o640); err != nil {
		t.Fatal(err)
	}
	env.repositories.state.Project.ProjectID = location.ProjectID
	env.repositories.state.Source.ProjectRoot = location.Path.AbsolutePath
	env.repositories.state.Source.ProjectContractPath = detail.Registration.ContractPath
	env.activation.detail = *detail
	if err := PrepareRetainedRoots(env.roots.StorageRoot); err != nil {
		t.Fatal(err)
	}
	move := env.workspace.planner
	move.NamedPlacement = true
	move.ActiveProjectLocation = func(context.Context, string) (*WorkspaceActiveLocation, error) { return location, nil }
	env.service.WorkspaceMove = &move
	return env, &move, location
}

func TestProjectPhysicalV3ArchivePlannerBindsLocationAndRevisions(t *testing.T) {
	env, _, location := newProjectV3PlanEnvironment(t)
	plan, err := env.service.PlanProjectPhysicalArchive(context.Background(), projectArchivePlanRequest(), location.ProjectID, ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, Reason: "archive relocated project"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != ProjectPhysicalArchivePlanSchemaVersionV3 || !sameWorkspaceActiveLocation(plan.ActiveLocation, location) || !sameWorkspaceActiveLocation(plan.ActiveLocation, plan.Workspace.ActiveLocation) || plan.Custody.CanonicalRoot != location.Path.AbsolutePath {
		t.Fatal("physical archive lost the exact accepted active location")
	}
	roundTrip := cloneProjectArchiveTestValue(t, plan)
	if err := ValidateProjectPhysicalArchivePlan(roundTrip, env.roots); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProjectPhysicalArchivePlan){
		"missing_outer_proof":     func(p *ProjectPhysicalArchivePlan) { p.ActiveLocation = nil },
		"missing_workspace_proof": func(p *ProjectPhysicalArchivePlan) { p.Workspace.ActiveLocation = nil },
		"outer_location_id":       func(p *ProjectPhysicalArchivePlan) { p.ActiveLocation.LocationID += "X" },
		"registration_revision": func(p *ProjectPhysicalArchivePlan) {
			p.ActiveLocation.RegistrationRevision++
			p.Workspace.ActiveLocation.RegistrationRevision++
		},
		"source_revision": func(p *ProjectPhysicalArchivePlan) {
			p.ActiveLocation.RepositorySourceRevision++
			p.Workspace.ActiveLocation.RepositorySourceRevision++
		},
		"current_registration": func(p *ProjectPhysicalArchivePlan) { p.Registration.Registration.RegistrationRevision++ },
		"current_source":       func(p *ProjectPhysicalArchivePlan) { p.Repository.Source.SourceRevision++ },
		"canonical_root": func(p *ProjectPhysicalArchivePlan) {
			p.Custody.CanonicalRoot = filepath.Join(env.roots.BoxRoot, "Projects", p.Custody.ProjectSlug)
		},
		"v2_envelope": func(p *ProjectPhysicalArchivePlan) { p.SchemaVersion = ProjectPhysicalArchivePlanSchemaVersionV2 },
	} {
		t.Run(name, func(t *testing.T) {
			copy := cloneProjectArchiveTestValue(t, plan)
			mutate(&copy)
			_ = SealWorkspaceArchivePlan(&copy.Workspace)
			_ = SealProjectPhysicalArchivePlan(&copy)
			if ValidateProjectPhysicalArchivePlanEnvelope(copy, env.roots) == nil {
				t.Fatal("resealed physical plan accepted substituted owner evidence")
			}
		})
	}
}

func TestProjectPhysicalV3ArchiveApplyUsesAcceptedLocation(t *testing.T) {
	env, move, location := newProjectV3PlanEnvironment(t)
	plan, err := env.service.PlanProjectPhysicalArchive(t.Context(), projectArchivePlanRequest(), location.ProjectID, ProjectPhysicalArchivePlanInput{
		OperationID: projectArchiveAdapterOperationID, Reason: "archive relocated project",
		PlannedAt: time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.service.ApplyProjectPhysicalArchive(t.Context(), plan, plan.PlanDigest)
	if err != nil || result.Phase != projects.ProjectArchivePhaseComplete {
		t.Fatalf("archive at accepted location: phase=%s error=%v", result.Phase, err)
	}
	if _, err := os.Stat(location.Path.AbsolutePath); !os.IsNotExist(err) {
		t.Fatal("accepted active path remains", err)
	}
	if data, err := os.ReadFile(filepath.Join(plan.Workspace.Destination.Path.AbsolutePath, "README.md")); err != nil || string(data) != "disposable project fixture\n" {
		t.Fatal("reviewed payload was not retained", err)
	}
	move.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 37, 0, 0, time.UTC) }
	env.service.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 36, 0, 0, time.UTC) }
	restore, err := env.service.PlanProjectPhysicalRestore(t.Context(), projectRestorePlanRequest(), plan, ProjectPhysicalRestorePlanInput{
		OperationID: projectRestorePlanTestOperationID, Reason: "restore relocated project inactive",
		PlannedAt: time.Date(2026, 9, 4, 10, 35, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := env.service.ApplyProjectPhysicalRestore(t.Context(), projectRestorePlanRequest(), restore, restore.PlanDigest)
	if err != nil || restored.ActivationState != WorkspaceActivationInactive || restored.State == nil || restored.State.Phase != projects.ProjectRestorePhaseComplete {
		t.Fatalf("inactive restore at accepted location: %+v error=%v", restored, err)
	}
	move.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 41, 0, 0, time.UTC) }
	env.service.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 40, 0, 0, time.UTC) }
	next, err := env.service.PlanProjectPhysicalArchive(t.Context(), projectArchivePlanRequest(), location.ProjectID, ProjectPhysicalArchivePlanInput{
		OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ", Reason: "rearchive restored inactive project",
		PlannedAt: time.Date(2026, 9, 4, 10, 39, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	rearchived, err := env.service.ApplyProjectPhysicalArchive(t.Context(), next, next.PlanDigest)
	if err == nil || !strings.Contains(err.Error(), "archive replay is read-only history") || rearchived.Phase != "" {
		t.Fatalf("inactive restore guard was bypassed: phase=%s error=%v", rearchived.Phase, err)
	}
}

type projectV3RestorePlanner struct {
	archive WorkspaceArchiveInspection
	restore WorkspaceArchivePlan
}

func (p projectV3RestorePlanner) PlanArchive(context.Context, WorkspaceArchivePlanInput) (WorkspaceArchivePlan, error) {
	return p.archive.Plan, nil
}

func (p projectV3RestorePlanner) PlanRestore(context.Context, WorkspaceRestorePlanInput) (WorkspaceArchivePlan, error) {
	return p.restore, nil
}

func (p projectV3RestorePlanner) InspectArchive(context.Context, string) (WorkspaceArchiveInspection, error) {
	return p.archive, nil
}

func TestProjectPhysicalV3RestorePlannerPreservesFrozenLocationInactive(t *testing.T) {
	ctx := context.Background()
	env, _, location := newProjectV3PlanEnvironment(t)
	archivePlan, err := env.service.PlanProjectPhysicalArchive(ctx, projectArchivePlanRequest(), location.ProjectID, ProjectPhysicalArchivePlanInput{OperationID: projectArchiveAdapterOperationID, Reason: "archive relocated project"})
	if err != nil {
		t.Fatal(err)
	}
	operation := workspaceV3CompleteOperation(t, archivePlan.Workspace, PhaseArchiveComplete)
	manifest := workspaceV3Manifest(t, env.roots, archivePlan.Workspace, operation)
	rawManifest, _ := json.Marshal(manifest)
	at := archivePlan.Workspace.PlannedAt
	state := projects.ProjectPhysicalArchiveState{SchemaVersion: projects.ProjectPhysicalArchiveStateSchemaVersion,
		Status: projects.ProjectPhysicalArchiveStatusArchived, Phase: projects.ProjectArchivePhaseComplete, MutationBlocked: true,
		ProjectID: location.ProjectID, ProjectSlug: archivePlan.Custody.ProjectSlug, OperationID: operation.OperationID,
		PlanDigest: archivePlan.PlanDigest, WorkspacePlanDigest: archivePlan.Workspace.PlanDigest,
		ActivePath: location.Path.AbsolutePath, ArchivePath: archivePlan.Workspace.Destination.Path.AbsolutePath,
		RuntimeQuiescenceDigest: sha256Digest([]byte("empty runtime")), ArchiveManifestDigest: sha256Digest(rawManifest),
		ActorID: archivePlan.Workspace.ActorID, Reason: archivePlan.Workspace.Reason, StartedAt: at,
		RuntimeDeactivatedAt: &at, WorkspaceCompletedAt: operation.CompletedAt, ArchivedAt: operation.CompletedAt}
	env.projects.detail.Project.Project.Status = "archived"
	env.projects.detail.Project.Project.ArchiveState, _ = json.Marshal(state)
	env.projects.detail.Registration.RegistrationStatus = projects.ProjectRegistrationStatusArchived
	env.projects.detail.Registration.ActivationStatus = projects.ProjectActivationStatusInactive
	env.repositories.state.Project.Status = "archived"
	restore := cloneProjectArchiveTestValue(t, archivePlan.Workspace)
	restore.OperationID, restore.OperationKind = projectRestorePlanTestOperationID, WorkspaceOperationRestore
	restore.ArchiveOperationID, restore.ArchiveManifestDigest = operation.OperationID, state.ArchiveManifestDigest
	restore.Source, restore.Destination = archivePlan.Workspace.Destination, archivePlan.Workspace.Source
	restore.Source.Identity, restore.Destination.Identity = archivePlan.Workspace.Source.Identity, PathIdentity{Presence: PathAbsent}
	restore.SourceAncestors, restore.DestinationAncestors = archivePlan.Workspace.DestinationAncestors, archivePlan.Workspace.SourceAncestors
	restore.Placement.Metadata.Identity = presentIdentity(restore.Source.Identity.DeviceID, 900, InventoryEntryDirectory)
	restore.ActorID, restore.Reason, restore.PlannedAt = projectRestorePlanRequest().ActorID, "restore inactive", at.Add(3*time.Second)
	if err := SealWorkspaceArchivePlan(&restore); err != nil {
		t.Fatal(err)
	}
	env.service.WorkspaceMove = projectV3RestorePlanner{archive: WorkspaceArchiveInspection{Plan: archivePlan.Workspace, Operation: operation, Manifest: &manifest, Custody: CustodyArchived}, restore: restore}
	plan, err := env.service.PlanProjectPhysicalRestore(ctx, projectRestorePlanRequest(), archivePlan, ProjectPhysicalRestorePlanInput{OperationID: restore.OperationID, Reason: restore.Reason, PlannedAt: restore.PlannedAt})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SchemaVersion != ProjectPhysicalRestorePlanSchemaVersionV3 || plan.ActivationState != WorkspaceActivationInactive || plan.Workspace.Destination.Path != location.Path || !sameWorkspaceActiveLocation(plan.ActiveLocation, archivePlan.ActiveLocation) {
		t.Fatal("physical restore did not freeze the original inactive location")
	}
	copy := cloneProjectArchiveTestValue(t, plan)
	if err := ValidateProjectPhysicalRestorePlan(copy, env.roots); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ProjectPhysicalRestorePlan){
		"active_runtime": func(p *ProjectPhysicalRestorePlan) { p.ActivationState = "active" },
		"current_location": func(p *ProjectPhysicalRestorePlan) {
			p.ActiveLocation.LocationID += "X"
			p.Workspace.ActiveLocation.LocationID += "X"
		},
		"v2_restore":            func(p *ProjectPhysicalRestorePlan) { p.SchemaVersion = "storage.project_physical_restore_plan.v2" },
		"registration_revision": func(p *ProjectPhysicalRestorePlan) { p.Registration.Registration.RegistrationRevision++ },
		"source_revision":       func(p *ProjectPhysicalRestorePlan) { p.Repository.Source.SourceRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			copy := cloneProjectArchiveTestValue(t, plan)
			mutate(&copy)
			_ = SealWorkspaceArchivePlan(&copy.Workspace)
			_ = SealProjectPhysicalRestorePlan(&copy)
			if ValidateProjectPhysicalRestorePlan(copy, env.roots) == nil {
				t.Fatal("resealed restore changed frozen location or inactive state")
			}
		})
	}
}
