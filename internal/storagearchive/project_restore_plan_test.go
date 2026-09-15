package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const projectRestorePlanTestOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAZ"

type projectRestorePlanTestPlanner struct {
	WorkspaceMoveService
	beforePlan func()
	mutatePlan func(*WorkspaceArchivePlan)
	inspects   int
	plans      int
}

func (p *projectRestorePlanTestPlanner) PlanRestore(ctx context.Context, input WorkspaceRestorePlanInput) (WorkspaceArchivePlan, error) {
	p.plans++
	if p.beforePlan != nil {
		p.beforePlan()
	}
	plan, err := p.WorkspaceMoveService.PlanRestore(ctx, input)
	if err == nil && p.mutatePlan != nil {
		p.mutatePlan(&plan)
		err = SealWorkspaceArchivePlan(&plan)
	}
	return plan, err
}

func (p *projectRestorePlanTestPlanner) InspectArchive(ctx context.Context, operationID string) (WorkspaceArchiveInspection, error) {
	p.inspects++
	return p.WorkspaceMoveService.InspectArchive(ctx, operationID)
}

func newProjectRestorePlanEnvironment(t *testing.T) (projectArchivePlanEnvironment, ProjectPhysicalArchivePlan, *projectRestorePlanTestPlanner) {
	t.Helper()
	env, archivePlan := newProjectArchiveApplyEnvironment(t)
	if _, err := env.service.ApplyProjectPhysicalArchive(context.Background(), archivePlan, archivePlan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	planner := &projectRestorePlanTestPlanner{WorkspaceMoveService: env.workspace.planner}
	planner.Now = func() time.Time { return time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC) }
	env.service.WorkspaceMove = planner
	return env, archivePlan, planner
}

func projectRestorePlanInput() ProjectPhysicalRestorePlanInput {
	return ProjectPhysicalRestorePlanInput{
		OperationID: projectRestorePlanTestOperationID, Reason: "restore completed fixture without activation",
		PlannedAt: time.Date(2026, 9, 9, 8, 30, 0, 123456000, time.UTC),
	}
}

func projectRestorePlanRequest() requestctx.Context {
	req := projectArchivePlanRequest()
	req.ActorID = "actor_01ARZ3NDEKTSV4RRFFQ69G5FAY"
	req.CorrelationID = "corr_project_restore_plan"
	return req
}

// The fixture archives real disposable files using the existing move kernel;
// only project persistence is fake here. Production DB acceptance is a later
// transaction invariant, not a claim made by these read-only adapter tests.
func TestProjectPhysicalRestorePlanBindsArchiveWithoutMutation(t *testing.T) {
	env, archivePlan, planner := newProjectRestorePlanEnvironment(t)
	beforePayload := snapshotWorkspacePayload(t, archivePlan.Workspace.Destination.Path.AbsolutePath)
	beforeDetail, _ := env.projects.GetProjectRegistrationStatus(context.Background(), "fixture")
	beforeRepo, _ := env.repositories.ReadProjectRepositoryState(context.Background(), "fixture")
	beforeTransitions, beforeQuiescence, beforeActivation := env.projects.transitions, env.quiescence.calls, len(env.activation.inputs)
	journal := env.workspace.planner.Journal.(*moveFakeJournal)
	beforeJournal := cloneProjectArchiveTestValue(t, journal.record)
	beforeLockKeys := append([]string(nil), env.projects.lockKeys...)
	plan, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProjectPhysicalRestorePlan(plan, env.roots); err != nil {
		t.Fatal(err)
	}
	if plan.Workspace.Source.Path != archivePlan.Workspace.Destination.Path || plan.Workspace.Destination.Path != archivePlan.Workspace.Source.Path ||
		plan.ActivationState != WorkspaceActivationInactive || plan.Request.ActorID == archivePlan.Request.ActorID ||
		plan.Workspace.ArchiveManifestDigest != plan.ArchiveState.ArchiveManifestDigest || plan.Workspace.OperationID != projectRestorePlanTestOperationID {
		t.Fatalf("restore plan changed custody, actor or inactive binding: %#v", plan)
	}
	if planner.inspects != 2 || planner.plans != 1 {
		t.Fatalf("read calls: inspect=%d plan=%d", planner.inspects, planner.plans)
	}
	if _, err := os.Lstat(env.canonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("planning created active destination: %v", err)
	}
	afterDetail, _ := env.projects.GetProjectRegistrationStatus(context.Background(), "fixture")
	afterRepo, _ := env.repositories.ReadProjectRepositoryState(context.Background(), "fixture")
	if !reflect.DeepEqual(beforePayload, snapshotWorkspacePayload(t, archivePlan.Workspace.Destination.Path.AbsolutePath)) ||
		!reflect.DeepEqual(beforeDetail, afterDetail) || !reflect.DeepEqual(beforeRepo, afterRepo) ||
		!reflect.DeepEqual(beforeJournal, journal.record) || !reflect.DeepEqual(beforeLockKeys, env.projects.lockKeys) ||
		beforeTransitions != env.projects.transitions || beforeQuiescence != env.quiescence.calls || beforeActivation != len(env.activation.inputs) ||
		journal.restoreIntentCommits != 0 || journal.restoreCatalogCommits != 0 || journal.restoreEventCommits != 0 {
		t.Fatal("read-only restore plan changed payload, lifecycle, runtime, lock or journal state")
	}
}

func TestProjectPhysicalRestorePlanStableJSONReplay(t *testing.T) {
	env, archivePlan, _ := newProjectRestorePlanEnvironment(t)
	plan, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	// Source.ProjectRoot is intentionally private in the shared read model. The
	// restore envelope preserves it through its explicitly bound root field.
	encoded := cloneProjectArchiveTestValue(t, plan)
	if encoded.Repository.Source.ProjectRoot != "" || encoded.RepositoryRoot == "" {
		t.Fatal("fixture did not exercise private root JSON omission")
	}
	if err := ValidateProjectPhysicalRestorePlan(encoded, env.roots); err != nil {
		t.Fatalf("serialized restore plan lost its root binding: %v", err)
	}
	replay, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), cloneProjectArchiveTestValue(t, archivePlan), projectRestorePlanInput())
	if err != nil || plan.PlanDigest != replay.PlanDigest {
		t.Fatalf("serialized archive replay identity changed: %s -> %s, %v", plan.PlanDigest, replay.PlanDigest, err)
	}
	input := projectRestorePlanInput()
	input.PlannedAt = input.PlannedAt.In(time.FixedZone("same instant", 7200))
	replay, err = env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, input)
	if err != nil || plan.PlanDigest != replay.PlanDigest {
		t.Fatalf("same instant changed canonical identity: %v", err)
	}
}

func TestProjectPhysicalRestorePlanGeneratedRequestAndDigest(t *testing.T) {
	env, archivePlan, _ := newProjectRestorePlanEnvironment(t)
	generated, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, ProjectPhysicalRestorePlanInput{})
	if err != nil {
		t.Fatal(err)
	}
	if generated.WorkspaceRequest.OperationID != "" || !generated.WorkspaceRequest.PlannedAt.IsZero() ||
		generated.Workspace.OperationID == archivePlan.Workspace.OperationID || generated.Workspace.Reason != "project restore" {
		t.Fatal("generated request did not preserve unspecified fields and distinct identity")
	}
	first, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"actor", "origin", "scope", "correlation", "reason", "operation", "time"} {
		t.Run(field, func(t *testing.T) {
			req, input := projectRestorePlanRequest(), projectRestorePlanInput()
			switch field {
			case "actor":
				req.ActorID = archivePlan.Request.ActorID
			case "origin":
				req.OriginNodeID = "node_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
			case "scope":
				req.ScopeID = archivePlan.Custody.ProjectScopeID
			case "correlation":
				req.CorrelationID += "_other"
			case "reason":
				input.Reason += " again"
			case "operation":
				input.OperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FAX"
			case "time":
				input.PlannedAt = input.PlannedAt.Add(time.Second)
			}
			changed, err := env.service.PlanProjectPhysicalRestore(context.Background(), req, archivePlan, input)
			if err != nil || changed.PlanDigest == first.PlanDigest {
				t.Fatalf("changed %s did not change valid plan identity: %v", field, err)
			}
		})
	}
}

func TestProjectPhysicalRestorePlanFailsBeforeReadsForInvalidRequest(t *testing.T) {
	env, archivePlan, planner := newProjectRestorePlanEnvironment(t)
	for _, field := range []string{"actor", "origin", "correlation", "archive digest", "private archive root"} {
		t.Run(field, func(t *testing.T) {
			req := projectRestorePlanRequest()
			preserved := cloneProjectArchiveTestValue(t, archivePlan)
			switch field {
			case "actor":
				req.ActorID = " "
			case "origin":
				req.OriginNodeID = ""
			case "correlation":
				req.CorrelationID = ""
			case "archive digest":
				preserved.PlanDigest = ""
			case "private archive root":
				preserved.Repository.Source.ProjectRoot = "/not-the-reviewed-root"
			}
			if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), req, preserved, projectRestorePlanInput()); err == nil {
				t.Fatal("accepted invalid request")
			}
			if planner.plans != 0 || planner.inspects != 0 {
				t.Fatal("read generic archive before validating request")
			}
		})
	}
}

func TestProjectPhysicalRestorePlanRejectsSubstitution(t *testing.T) {
	env, archivePlan, _ := newProjectRestorePlanEnvironment(t)
	plan, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*ProjectPhysicalRestorePlan)
	}{
		{"schema", func(p *ProjectPhysicalRestorePlan) { p.SchemaVersion = "future" }},
		{"activation", func(p *ProjectPhysicalRestorePlan) { p.ActivationState = "active" }},
		{"actor", func(p *ProjectPhysicalRestorePlan) { p.Request.ActorID = archivePlan.Request.ActorID }},
		{"origin", func(p *ProjectPhysicalRestorePlan) { p.Request.OriginNodeID = "" }},
		{"correlation", func(p *ProjectPhysicalRestorePlan) { p.Request.CorrelationID = "" }},
		{"archive plan reason", func(p *ProjectPhysicalRestorePlan) { p.ArchivePlan.WorkspaceRequest.Reason = "other" }},
		{"archive state digest", func(p *ProjectPhysicalRestorePlan) { p.ArchiveState.PlanDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"archive operation", func(p *ProjectPhysicalRestorePlan) { p.Archive.Operation.ActorID = p.Request.ActorID }},
		{"manifest", func(p *ProjectPhysicalRestorePlan) { p.Archive.Manifest.ObjectID = "project_other" }},
		{"project", func(p *ProjectPhysicalRestorePlan) { p.Registration.Project.Project.ProjectID = "project_other" }},
		{"registration revision", func(p *ProjectPhysicalRestorePlan) { p.Registration.Registration.RegistrationRevision++ }},
		{"registration path", func(p *ProjectPhysicalRestorePlan) { p.Registration.Registration.ProjectRoot += "/other" }},
		{"contract", func(p *ProjectPhysicalRestorePlan) { p.Registration.Registration.Contract = json.RawMessage(`{}`) }},
		{"source revision", func(p *ProjectPhysicalRestorePlan) { p.Repository.Source.SourceRevision++ }},
		{"source root", func(p *ProjectPhysicalRestorePlan) { p.RepositoryRoot += "/other" }},
		{"private source root", func(p *ProjectPhysicalRestorePlan) { p.Repository.Source.ProjectRoot = "/untrusted" }},
		{"member", func(p *ProjectPhysicalRestorePlan) { p.Repository.Members[0].RepositoryID = "repo_other" }},
		{"active member", func(p *ProjectPhysicalRestorePlan) {
			p.Repository.Members[0].MembershipLifecycle = projects.RepositoryLifecycleActive
		}},
		{"runtime", func(p *ProjectPhysicalRestorePlan) {
			p.Registration.Registration.ActivationStatus = projects.ProjectActivationStatusBaseActive
		}},
		{"archive id reuse", func(p *ProjectPhysicalRestorePlan) {
			p.Workspace.OperationID = p.ArchiveState.OperationID
			p.WorkspaceRequest.OperationID = p.ArchiveState.OperationID
		}},
		{"archive link", func(p *ProjectPhysicalRestorePlan) {
			p.Workspace.ArchiveOperationID = projectRestorePlanTestOperationID
		}},
		{"manifest link", func(p *ProjectPhysicalRestorePlan) {
			p.Workspace.ArchiveManifestDigest = "sha256:" + strings.Repeat("f", 64)
		}},
		{"inventory", func(p *ProjectPhysicalRestorePlan) { p.Workspace.Inventory.Entries[0].Mode++ }},
		{"source inode", func(p *ProjectPhysicalRestorePlan) { p.Workspace.Source.Identity.Inode++ }},
		{"destination", func(p *ProjectPhysicalRestorePlan) { p.Workspace.Destination.Path.AbsolutePath += "-other" }},
		{"reason", func(p *ProjectPhysicalRestorePlan) { p.WorkspaceRequest.Reason = "other" }},
		{"requested operation", func(p *ProjectPhysicalRestorePlan) { p.WorkspaceRequest.OperationID = p.ArchiveState.OperationID }},
		{"requested time", func(p *ProjectPhysicalRestorePlan) {
			p.WorkspaceRequest.PlannedAt = p.WorkspaceRequest.PlannedAt.Add(time.Second)
		}},
		{"before archive", func(p *ProjectPhysicalRestorePlan) {
			p.Workspace.PlannedAt = p.ArchivePlan.Workspace.PlannedAt
			p.WorkspaceRequest.PlannedAt = p.Workspace.PlannedAt
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modified := cloneProjectArchiveTestValue(t, plan)
			test.edit(&modified)
			// Correct hashes are not authority to contradict joined evidence.
			if err := SealWorkspaceArchivePlan(&modified.Workspace); err != nil {
				t.Fatal(err)
			}
			if err := SealProjectPhysicalArchivePlan(&modified.ArchivePlan); err != nil {
				t.Fatal(err)
			}
			if err := SealProjectPhysicalRestorePlan(&modified); err != nil {
				t.Fatal(err)
			}
			if err := ValidateProjectPhysicalRestorePlan(modified, env.roots); err == nil {
				t.Fatal("accepted correctly resealed substitution")
			}
		})
	}
}

func TestProjectPhysicalRestorePlanRejectsUnreadyAndHistoricalState(t *testing.T) {
	tests := []struct {
		name string
		edit func(*projectArchivePlanEnvironment)
	}{
		{"active project", func(e *projectArchivePlanEnvironment) { e.projects.detail.Project.Project.Status = "active" }},
		{"missing state", func(e *projectArchivePlanEnvironment) { e.projects.detail.Project.Project.ArchiveState = nil }},
		{"historical tar", func(e *projectArchivePlanEnvironment) {
			e.projects.detail.Project.Project.ArchiveState = json.RawMessage(`{"schema_version":"project.archive_state.v0.6","status":"archived"}`)
		}},
		{"partial archive", func(e *projectArchivePlanEnvironment) {
			state, _ := projects.ParseProjectPhysicalArchiveState(e.projects.detail.Project.Project.ArchiveState)
			state.Status, state.Phase, state.ArchivedAt = projects.ProjectPhysicalArchiveStatusInProgress, projects.ProjectArchivePhaseProjectStatePending, nil
			e.projects.detail.Project.Project.ArchiveState, _ = json.Marshal(state)
		}},
		{"inactive mismatch", func(e *projectArchivePlanEnvironment) {
			e.projects.detail.Registration.ActivationStatus = projects.ProjectActivationStatusBaseActive
		}},
		{"member change", func(e *projectArchivePlanEnvironment) {
			e.repositories.state.Members = e.repositories.state.Members[1:]
		}},
		{"registration change", func(e *projectArchivePlanEnvironment) { e.projects.detail.Registration.RegistrationRevision++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, archivePlan, planner := newProjectRestorePlanEnvironment(t)
			test.edit(&env)
			if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput()); err == nil {
				t.Fatal("accepted unready project state")
			}
			if planner.plans != 0 {
				t.Fatal("called generic planner before project evidence validation")
			}
		})
	}
}

func TestProjectPhysicalRestorePlanRejectsPlanningRaces(t *testing.T) {
	tests := []struct {
		name string
		edit func(*projectArchivePlanEnvironment)
	}{
		{"project revision", func(e *projectArchivePlanEnvironment) { e.projects.detail.Registration.RegistrationRevision++ }},
		{"repository revision", func(e *projectArchivePlanEnvironment) { e.repositories.state.Source.SourceRevision++ }},
		{"project timestamp", func(e *projectArchivePlanEnvironment) {
			e.projects.detail.Project.Project.UpdatedAt = e.projects.detail.Project.Project.UpdatedAt.Add(time.Second)
		}},
		{"repository timestamp", func(e *projectArchivePlanEnvironment) {
			e.repositories.state.Project.UpdatedAt = e.repositories.state.Project.UpdatedAt.Add(time.Second)
		}},
		{"runtime", func(e *projectArchivePlanEnvironment) {
			e.projects.detail.ScheduleRegistrations[0].ActivationStatus = projects.ProjectScheduleRegistrationStatusActive
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, archivePlan, planner := newProjectRestorePlanEnvironment(t)
			planner.beforePlan = func() { test.edit(&env) }
			if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput()); err == nil {
				t.Fatal("returned a plan after current evidence changed")
			}
		})
	}
}

func TestProjectPhysicalRestorePlanRefusesPhysicalDrift(t *testing.T) {
	for _, scenario := range []string{"destination", "payload", "manifest", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			env, archivePlan, _ := newProjectRestorePlanEnvironment(t)
			payload := archivePlan.Workspace.Destination.Path.AbsolutePath
			var err error
			switch scenario {
			case "destination":
				err = os.Mkdir(env.canonicalRoot, 0o750)
			case "payload":
				err = os.WriteFile(filepath.Join(payload, "README.md"), []byte("changed fixture"), 0o640)
			case "manifest":
				err = os.WriteFile(filepath.Join(filepath.Dir(payload), "archive.json"), []byte(`{}`), 0o600)
			case "symlink":
				err = os.Symlink(payload, env.canonicalRoot)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotWorkspacePayload(t, payload)
			if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput()); err == nil {
				t.Fatal("accepted conflicting physical evidence")
			}
			if !reflect.DeepEqual(before, snapshotWorkspacePayload(t, payload)) {
				t.Fatal("refusal modified the retained payload")
			}
		})
	}
}

func TestProjectPhysicalRestorePlanRejectsResealedPlannerSubstitution(t *testing.T) {
	for _, field := range []string{"reason", "actor", "operation", "time", "identity"} {
		t.Run(field, func(t *testing.T) {
			env, archivePlan, planner := newProjectRestorePlanEnvironment(t)
			planner.mutatePlan = func(p *WorkspaceArchivePlan) {
				switch field {
				case "reason":
					p.Reason = "other"
				case "actor":
					p.ActorID = archivePlan.Request.ActorID
				case "operation":
					p.OperationID = archivePlan.Workspace.OperationID
				case "time":
					p.PlannedAt = p.PlannedAt.Add(time.Second)
				case "identity":
					p.ObjectID = "project_other"
				}
			}
			if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archivePlan, projectRestorePlanInput()); err == nil {
				t.Fatal("accepted generic planner substitution")
			}
		})
	}
}
