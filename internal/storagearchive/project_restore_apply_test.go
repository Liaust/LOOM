package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

func (s *projectArchivePlanProjectService) TransitionProjectPhysicalRestore(_ context.Context, req requestctx.Context, input projects.ProjectPhysicalRestoreTransitionInput) (projects.ProjectPhysicalRestoreTransitionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transitions++
	archive, present := projects.ParseProjectPhysicalArchiveState(s.detail.Project.Project.ArchiveState)
	previous := archive.Restore
	archive.Restore = nil
	if !present || !reflect.DeepEqual(archive, input.Archive.State) || req != input.State.Request {
		return projects.ProjectPhysicalRestoreTransitionResult{}, fmt.Errorf("restore binding changed")
	}
	next := input.State
	if next.Phase == projects.ProjectRestorePhaseComplete && next.EventID == "" {
		next.EventID = "event_01ARZ3NDEKTSV4RRFFQ69G5FAZ"
	}
	if err := projects.ValidateProjectPhysicalRestoreState(archive, next); err != nil {
		return projects.ProjectPhysicalRestoreTransitionResult{}, err
	}
	if previous == nil {
		if next.Phase != projects.ProjectRestorePhasePending {
			return projects.ProjectPhysicalRestoreTransitionResult{}, fmt.Errorf("restore intent missing")
		}
	} else {
		if err := projects.ValidateProjectPhysicalRestoreTransition(*previous, next); err != nil {
			return projects.ProjectPhysicalRestoreTransitionResult{}, err
		}
		if previous.Phase == next.Phase {
			return projects.ProjectPhysicalRestoreTransitionResult{Project: s.detail.Project.Project, State: *previous, Replay: true}, nil
		}
	}
	archive.Restore = &next
	payload, _ := json.Marshal(archive)
	s.detail.Project.Project.ArchiveState = payload
	if next.Phase == projects.ProjectRestorePhaseComplete {
		s.detail.Project.Project.Status = "active"
		s.detail.Registration.RegistrationStatus = projects.ProjectRegistrationStatusRegistered
		s.detail.Registration.ActivationStatus = projects.ProjectActivationStatusInactive
		s.repository.mu.Lock()
		s.repository.state.Project.Status = "active"
		for i := range s.repository.state.Members {
			member := &s.repository.state.Members[i]
			member.MembershipLifecycle = projects.RepositoryLifecycleActive
			if member.RepositoryOwnerProjectID == archive.ProjectID {
				member.RepositoryLifecycle = projects.RepositoryLifecycleActive
			}
		}
		s.repository.mu.Unlock()
		s.eventCount++
	}
	return projects.ProjectPhysicalRestoreTransitionResult{Project: s.detail.Project.Project, State: next}, nil
}

func newProjectRestoreApplyEnvironment(t *testing.T) (projectArchivePlanEnvironment, ProjectPhysicalRestorePlan, *projectRestorePlanTestPlanner) {
	t.Helper()
	env, archived, planner := newProjectRestorePlanEnvironment(t)
	plan, err := env.service.PlanProjectPhysicalRestore(context.Background(), projectRestorePlanRequest(), archived, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	env.service.Now = planner.Now
	return env, plan, planner
}

func TestProjectPhysicalRestoreApplyAndReplay(t *testing.T) {
	env, plan, _ := newProjectRestoreApplyEnvironment(t)
	before := snapshotWorkspacePayload(t, plan.Workspace.Source.Path.AbsolutePath)
	activation, quiescence := len(env.activation.inputs), env.quiescence.calls
	result, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if result.State == nil || result.State.Phase != projects.ProjectRestorePhaseComplete || result.State.EventID == "" || result.Replay || result.Recoverable || !result.MutationBlocked {
		t.Fatalf("restore result: %+v", result)
	}
	if result.Project.Project.Project.Status != "active" || result.Project.Registration.ActivationStatus != projects.ProjectActivationStatusInactive {
		t.Fatal("restore lifecycle is not inactive active custody")
	}
	if _, err := os.Lstat(plan.Workspace.Source.Path.AbsolutePath); !os.IsNotExist(err) {
		t.Fatalf("archived payload still exists: %v", err)
	}
	if got := snapshotWorkspacePayload(t, plan.Workspace.Destination.Path.AbsolutePath); !reflect.DeepEqual(before, got) {
		t.Fatal("restored payload changed")
	}
	if len(env.activation.inputs) != activation || env.quiescence.calls != quiescence {
		t.Fatal("restore called a runtime/fence operation")
	}
	if err := projects.EnsureProjectMutable(result.Project.Project.Project, "activation", "scripts"); !errors.Is(err, projects.ErrProjectRestoreBlocked) {
		t.Fatalf("restored project mutable: %v", err)
	}
	history, ok := projects.ParseProjectPhysicalArchiveState(result.Project.Project.Project.ArchiveState)
	if !ok {
		t.Fatal("archive history invalid")
	}
	history.Restore = nil
	if !reflect.DeepEqual(history, plan.ArchiveState) {
		t.Fatal("original archive changed")
	}
	payload, _ := json.Marshal(plan)
	var decoded ProjectPhysicalRestorePlan
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	replay, err := env.service.RecoverProjectPhysicalRestore(context.Background(), plan.Request, decoded, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replay || !reflect.DeepEqual(replay.State, result.State) || env.projects.eventCount != 2 {
		t.Fatalf("replay changed event/state: %+v", replay)
	}
	if _, err := env.service.ApplyProjectPhysicalArchive(context.Background(), plan.ArchivePlan, plan.ArchivePlan.PlanDigest); err == nil {
		t.Fatal("old archive apply accepted after restore")
	}
	if _, err := env.service.PlanProjectPhysicalRestore(context.Background(), plan.Request, plan.ArchivePlan, projectRestorePlanInput()); err == nil {
		t.Fatal("new restore planning accepted consumed archive")
	}
}

func TestProjectPhysicalRestoreCrashRecovery(t *testing.T) {
	for _, boundary := range []ProjectPhysicalRestoreFailureBoundary{ProjectRestoreBoundaryAfterIntent, ProjectRestoreBoundaryAfterWorkspaceMove, ProjectRestoreBoundaryBeforeProjectCommit, ProjectRestoreBoundaryAfterProjectCommit} {
		t.Run(string(boundary), func(t *testing.T) {
			env, plan, _ := newProjectRestoreApplyEnvironment(t)
			env.service.RestoreFailureHook = func(at ProjectPhysicalRestoreFailureBoundary) error {
				if at == boundary {
					return errors.New("injected restore interruption")
				}
				return nil
			}
			partial, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			if err == nil || partial.State == nil {
				t.Fatalf("missing partial state: %+v %v", partial, err)
			}
			if (partial.State.Phase == projects.ProjectRestorePhaseComplete) != (boundary == ProjectRestoreBoundaryAfterProjectCommit) {
				t.Fatal("false committed phase")
			}
			if partial.Recoverable == (boundary == ProjectRestoreBoundaryAfterProjectCommit) {
				t.Fatal("false recoverability")
			}
			if partial.Project != nil && !errors.Is(projects.EnsureProjectMutable(partial.Project.Project.Project, "write", "fixture"), projects.ErrProjectRestoreBlocked) {
				t.Fatal("interruption opened mutation guard")
			}
			if boundary == ProjectRestoreBoundaryAfterProjectCommit && partial.Project != nil {
				t.Fatal("post-commit interruption returned stale project detail")
			}
			env.service.RestoreFailureHook = nil
			recovered, err := env.service.RecoverProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.State.Phase != projects.ProjectRestorePhaseComplete || env.projects.eventCount != 2 {
				t.Fatal("recovery did not converge exactly once")
			}
		})
	}
}

func TestProjectPhysicalRestoreGenericCrashRecovery(t *testing.T) {
	for _, boundary := range []WorkspaceMoveBoundary{BoundaryBeforeRestoreIntent, BoundaryAfterRestoreIntent, BoundaryAfterRestorePayloadMove, BoundaryAfterRestoreMovedRecord, BoundaryAfterRestoredManifest, BoundaryAfterRestoreProjections, BoundaryAfterRestoreComplete} {
		t.Run(string(boundary), func(t *testing.T) {
			env, plan, planner := newProjectRestoreApplyEnvironment(t)
			planner.FailureHook = func(at WorkspaceMoveBoundary) error {
				if at == boundary {
					return errors.New("injected generic restore crash")
				}
				return nil
			}
			partial, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			if err == nil || partial.State == nil || !partial.Recoverable {
				t.Fatalf("generic interruption: %+v %v", partial, err)
			}
			planner.FailureHook = nil
			if _, err := env.service.RecoverProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err != nil {
				t.Fatal(err)
			}
			if env.projects.eventCount != 2 {
				t.Fatal("duplicate or missing restore event")
			}
		})
	}
}

func TestProjectPhysicalRestoreConcurrentReplay(t *testing.T) {
	env, plan, planner := newProjectRestoreApplyEnvironment(t)
	// Avoid instrumentation counters in the read-only planner under concurrency.
	env.service.WorkspaceMove = planner.WorkspaceMoveService
	var wg sync.WaitGroup
	errorsCh := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			errorsCh <- err
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if env.projects.eventCount != 2 {
		t.Fatal("concurrent replay duplicated event")
	}
}

func TestProjectPhysicalRestoreRefusesChangedRequestAndReview(t *testing.T) {
	for _, mutation := range []string{"actor", "origin", "scope", "correlation", "reason", "registration", "operation", "digest"} {
		t.Run(mutation, func(t *testing.T) {
			env, plan, _ := newProjectRestoreApplyEnvironment(t)
			req, digest := plan.Request, plan.PlanDigest
			switch mutation {
			case "actor":
				req.ActorID += "other"
			case "origin":
				req.OriginNodeID += "other"
			case "scope":
				req.ScopeID += "other"
			case "correlation":
				req.CorrelationID += "other"
			case "reason":
				plan.Workspace.Reason = "different"
			case "registration":
				plan.Registration.Registration.ContractHash = "sha256:" + strings.Repeat("f", 64)
			case "operation":
				plan.Workspace.OperationID = plan.ArchiveState.OperationID
			case "digest":
				digest = "sha256:" + strings.Repeat("f", 64)
			}
			before := env.projects.transitions
			if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), req, plan, digest); err == nil {
				t.Fatal("changed review/request accepted")
			}
			if env.projects.transitions != before {
				t.Fatal("refusal mutated lifecycle")
			}
		})
	}
}

func TestProjectPhysicalRestoreRecoveryRefusesDrift(t *testing.T) {
	for _, mutation := range []string{"request", "plan", "original archive", "member", "source", "runtime", "destination", "manifest"} {
		t.Run(mutation, func(t *testing.T) {
			env, plan, _ := newProjectRestoreApplyEnvironment(t)
			env.service.RestoreFailureHook = func(at ProjectPhysicalRestoreFailureBoundary) error {
				if at == ProjectRestoreBoundaryAfterIntent {
					return errors.New("pause")
				}
				return nil
			}
			if _, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err == nil {
				t.Fatal("no pause")
			}
			env.service.RestoreFailureHook = nil
			switch mutation {
			case "request", "plan", "original archive":
				state, _ := projects.ParseProjectPhysicalArchiveState(env.projects.detail.Project.Project.ArchiveState)
				if mutation == "request" {
					state.Restore.Request.CorrelationID += "drift"
				} else if mutation == "plan" {
					state.Restore.PlanDigest = "sha256:" + strings.Repeat("f", 64)
				} else {
					state.Reason += "drift"
				}
				env.projects.detail.Project.Project.ArchiveState, _ = json.Marshal(state)
			case "member":
				env.repositories.state.Members[0].Key += "drift"
			case "source":
				env.repositories.state.Source.SourceRevision++
			case "runtime":
				env.projects.detail.Registration.ActivationStatus = "active"
			case "destination":
				if err := os.Mkdir(plan.Workspace.Destination.Path.AbsolutePath, 0755); err != nil {
					t.Fatal(err)
				}
			case "manifest":
				if err := os.WriteFile(plan.Workspace.Source.Path.AbsolutePath+"/README.md", []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := env.service.RecoverProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest); err == nil {
				t.Fatal("drift accepted")
			}
			if env.projects.eventCount != 1 {
				t.Fatal("failed restore emitted completion")
			}
		})
	}
}

func TestProjectPhysicalRestorePhaseTimes(t *testing.T) {
	env, plan, planner := newProjectRestoreApplyEnvironment(t)
	now := plan.Workspace.PlannedAt.Add(time.Hour)
	env.service.Now, planner.Now = func() time.Time { return now }, func() time.Time { return now }
	env.service.RestoreFailureHook = func(at ProjectPhysicalRestoreFailureBoundary) error { now = now.Add(time.Minute); return nil }
	result, err := env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !result.State.StartedAt.Before(*result.State.WorkspaceCompletedAt) || !result.State.WorkspaceCompletedAt.Before(*result.State.RestoredAt) {
		t.Fatalf("occurrence times not truthful: %+v", result.State)
	}
}

type projectRestoreInspectFailure struct{ WorkspaceMoveService }

func (p projectRestoreInspectFailure) InspectOperation(context.Context, string) (WorkspaceArchiveInspection, error) {
	return WorkspaceArchiveInspection{}, errors.New("manifest authentication key was not found")
}

func TestProjectPhysicalRestoreRefusesAmbiguousAndOrphanEvidence(t *testing.T) {
	for _, scenario := range []string{"missing authentication", "orphan operation", "recover without intent"} {
		t.Run(scenario, func(t *testing.T) {
			env, plan, planner := newProjectRestoreApplyEnvironment(t)
			before := env.projects.transitions
			var err error
			switch scenario {
			case "missing authentication":
				env.service.WorkspaceMove = projectRestoreInspectFailure{planner.WorkspaceMoveService}
			case "orphan operation":
				if _, err := planner.ApplyRestore(context.Background(), plan.Workspace, plan.Workspace.PlanDigest); err != nil {
					t.Fatal(err)
				}
			case "recover without intent":
				_, err = env.service.RecoverProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			}
			if scenario != "recover without intent" {
				_, err = env.service.ApplyProjectPhysicalRestore(context.Background(), plan.Request, plan, plan.PlanDigest)
			}
			if err == nil || env.projects.transitions != before || env.projects.eventCount != 1 {
				t.Fatalf("ambiguous/orphan evidence was adopted: %v", err)
			}
		})
	}
}
