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

	"loom.local/loom/internal/projects"
)

type projectInspectTestWorkspace struct {
	ProjectWorkspaceArchivePlanner
	inspector projectWorkspaceInspector
	mutate    func(*WorkspaceArchiveInspection)
	err       error
}

func (w projectInspectTestWorkspace) InspectOperation(ctx context.Context, id string) (WorkspaceArchiveInspection, error) {
	if w.err != nil {
		return WorkspaceArchiveInspection{}, w.err
	}
	value, err := w.inspector.InspectOperation(ctx, id)
	if err == nil && w.mutate != nil {
		w.mutate(&value)
	}
	return value, err
}

func TestProjectPhysicalArchiveInspectCurrentCustody(t *testing.T) {
	for _, restored := range []bool{false, true} {
		name := "archived"
		if restored {
			name = "restored"
		}
		t.Run(name, func(t *testing.T) {
			env, plan, _ := newProjectRestoreApplyEnvironment(t)
			payloadPath := plan.Workspace.Source.Path.AbsolutePath
			wantNext := "review_restore_plan"
			if restored {
				if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err != nil {
					t.Fatal(err)
				}
				payloadPath = plan.Workspace.Destination.Path.AbsolutePath
				wantNext = "activation_not_available"
			}
			before := snapshotWorkspacePayload(t, payloadPath)
			transitions, events, activation, quiescence := env.projects.transitions, env.projects.eventCount, len(env.activation.inputs), env.quiescence.calls
			result, err := env.service.InspectProjectArchive(context.Background(), plan.ArchiveState.ProjectID)
			if err != nil {
				t.Fatal(err)
			}
			if result.Physical == nil || result.Physical.EvidenceStatus != "verified" || result.Physical.NextAction != wantNext || !result.Physical.MutationBlocked || result.Physical.ActivationState != WorkspaceActivationInactive || result.Physical.Workspace == nil {
				t.Fatalf("physical inspection: %+v", result.Physical)
			}
			if result.RuntimeManifest != nil || result.RuntimeManifestPath != "" || result.HistoricalEvidence != nil {
				t.Fatal("physical inspection used legacy evidence")
			}
			encoded, _ := json.Marshal(result.Physical)
			for _, forbidden := range []string{env.roots.BoxRoot, env.roots.StorageRoot, "registered_root", "registration_plan", "inventory\":", "archive_plan", "safe_to_delete"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("summary leaks %q: %s", forbidden, encoded)
				}
			}
			if len(encoded) > MaximumWorkspaceSurfaceResponseBytes {
				t.Fatal("unbounded summary")
			}
			if !reflect.DeepEqual(before, snapshotWorkspacePayload(t, payloadPath)) || transitions != env.projects.transitions || events != env.projects.eventCount || activation != len(env.activation.inputs) || quiescence != env.quiescence.calls {
				t.Fatal("inspection mutated state or runtime")
			}
		})
	}
}

func TestProjectPhysicalArchiveInspectRefusesContradictoryEvidence(t *testing.T) {
	mutations := map[string]func(*WorkspaceArchiveInspection){
		"operation":  func(x *WorkspaceArchiveInspection) { x.Operation.OperationID = projectArchiveAdapterOperationID },
		"project":    func(x *WorkspaceArchiveInspection) { x.Operation.ObjectID += "other" },
		"actor":      func(x *WorkspaceArchiveInspection) { x.Operation.ActorID += "other" },
		"plan":       func(x *WorkspaceArchiveInspection) { x.Plan.Reason += "changed" },
		"source":     func(x *WorkspaceArchiveInspection) { x.Operation.Source.Path.AbsolutePath += "other" },
		"manifest":   func(x *WorkspaceArchiveInspection) { x.Manifest.Authentication.Tag += "bad" },
		"custody":    func(x *WorkspaceArchiveInspection) { x.Custody = CustodyArchived },
		"activation": func(x *WorkspaceArchiveInspection) { x.ActivationState = "active" },
		"completion": func(x *WorkspaceArchiveInspection) { x.Operation.CompletedAt = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			env, plan, planner := newProjectRestoreApplyEnvironment(t)
			if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			env.service.WorkspaceMove = projectInspectTestWorkspace{ProjectWorkspaceArchivePlanner: planner, inspector: planner, mutate: mutate}
			result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
			if err != nil || result.Physical == nil || result.Physical.EvidenceStatus != "conflict" || result.Physical.NextAction != "inspect_evidence" {
				t.Fatalf("substitution accepted: %+v %v", result.Physical, err)
			}
		})
	}
}

func TestProjectPhysicalArchiveInspectIncompleteAndUnavailable(t *testing.T) {
	for _, boundary := range []ProjectPhysicalRestoreFailureBoundary{ProjectRestoreBoundaryAfterIntent, ProjectRestoreBoundaryAfterWorkspaceMove, ProjectRestoreBoundaryBeforeProjectCommit} {
		t.Run(string(boundary), func(t *testing.T) {
			env, plan, _ := newProjectRestoreApplyEnvironment(t)
			env.service.RestoreFailureHook = func(at ProjectPhysicalRestoreFailureBoundary) error {
				if at == boundary {
					return errors.New("interrupted")
				}
				return nil
			}
			if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err == nil {
				t.Fatal("missing interruption")
			}
			result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
			if err != nil || result.Physical == nil || (result.Physical.EvidenceStatus != "pending" && result.Physical.EvidenceStatus != "unavailable") || result.Physical.NextAction != "recover_restore" {
				t.Fatalf("partial phase: %+v %v", result.Physical, err)
			}
		})
	}
	t.Run("inspection unavailable", func(t *testing.T) {
		env, _, planner := newProjectRestoreApplyEnvironment(t)
		env.service.WorkspaceMove = projectInspectTestWorkspace{ProjectWorkspaceArchivePlanner: planner, err: errors.New("private /secret/location unavailable")}
		result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
		if err != nil || result.Physical == nil || result.Physical.EvidenceStatus != "unavailable" {
			t.Fatalf("unavailable: %+v %v", result.Physical, err)
		}
		data, _ := json.Marshal(result.Physical)
		if strings.Contains(string(data), "secret") {
			t.Fatal("raw error leaked")
		}
	})
}

func TestProjectPhysicalArchiveInspectRejectsProjectedLifecycleDrift(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		env, _, _ := newProjectRestoreApplyEnvironment(t)
		if malformed {
			env.projects.detail.Project.Project.ArchiveState = json.RawMessage(`{"schema_version":"project.physical_archive_state.v1","phase":"complete"}`)
		} else {
			env.projects.detail.Project.Project.Status = "active"
		}
		result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
		if err != nil || result.Physical == nil || result.Physical.EvidenceStatus != "conflict" {
			t.Fatalf("project drift accepted: %+v %v", result.Physical, err)
		}
	}
}

func TestProjectPhysicalArchiveInspectionJSONDoesNotChangeHistory(t *testing.T) {
	env, plan, _ := newProjectRestoreApplyEnvironment(t)
	before := append(json.RawMessage(nil), env.projects.detail.Project.Project.ArchiveState...)
	result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
	if err != nil || !reflect.DeepEqual(before, result.ArchiveState) {
		t.Fatal("inspection changed archive history", err)
	}
	state, valid := projects.ParseProjectPhysicalArchiveState(result.ArchiveState)
	if !valid || state.PlanDigest != plan.ArchivePlan.PlanDigest {
		t.Fatal("plan binding lost")
	}
	if _, err := env.service.PlanProjectArchiveRestore(context.Background(), "fixture", ProjectArchiveRestoreInput{DryRun: true}); err == nil {
		t.Fatal("legacy planner claimed physical restore support")
	}
}

func TestProjectPhysicalArchiveInspectRejectsConcurrentProjectionChange(t *testing.T) {
	env, _, planner := newProjectRestoreApplyEnvironment(t)
	env.service.WorkspaceMove = projectInspectTestWorkspace{ProjectWorkspaceArchivePlanner: planner, inspector: planner, mutate: func(*WorkspaceArchiveInspection) { env.projects.detail.Project.Project.Status = "active" }}
	result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
	if err != nil || result.Physical == nil || result.Physical.EvidenceStatus != "conflict" || result.Physical.Workspace != nil {
		t.Fatalf("stale inspection: %+v %v", result.Physical, err)
	}
}

func TestProjectPhysicalArchiveInspectTypedNilUnavailable(t *testing.T) {
	env, _, _ := newProjectRestoreApplyEnvironment(t)
	var inspector *projectInspectTestWorkspace
	env.service.WorkspaceMove = inspector
	result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
	if err != nil || result.Physical == nil || result.Physical.EvidenceStatus != "unavailable" || result.Physical.NextAction != "inspect_evidence" {
		t.Fatalf("typed nil: %+v %v", result.Physical, err)
	}
}

func TestProjectPhysicalArchiveInspectDetectsActualPayloadDrift(t *testing.T) {
	for _, restored := range []bool{false, true} {
		env, plan, _ := newProjectRestoreApplyEnvironment(t)
		root := plan.Workspace.Source.Path.AbsolutePath
		if restored {
			if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			root = plan.Workspace.Destination.Path.AbsolutePath
		}
		if err := os.WriteFile(filepath.Join(root, "unexpected-after-review.txt"), []byte("disposable drift"), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := env.service.InspectProjectArchive(context.Background(), "fixture")
		if err != nil || result.Physical == nil || result.Physical.EvidenceStatus == "verified" || result.Physical.NextAction != "inspect_evidence" {
			t.Fatalf("payload drift accepted: %+v %v", result.Physical, err)
		}
	}
}
