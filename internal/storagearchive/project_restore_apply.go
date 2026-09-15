package storagearchive

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type ProjectPhysicalRestoreFailureBoundary string

const (
	ProjectRestoreBoundaryAfterIntent         ProjectPhysicalRestoreFailureBoundary = "after_restore_intent"
	ProjectRestoreBoundaryAfterWorkspaceMove  ProjectPhysicalRestoreFailureBoundary = "after_restore_workspace_move"
	ProjectRestoreBoundaryBeforeProjectCommit ProjectPhysicalRestoreFailureBoundary = "before_restore_project_commit"
	ProjectRestoreBoundaryAfterProjectCommit  ProjectPhysicalRestoreFailureBoundary = "after_restore_project_commit"
)

type ProjectPhysicalRestoreFailureHook func(ProjectPhysicalRestoreFailureBoundary) error

type ProjectPhysicalRestoreCoordinator interface {
	AcquireProjectArchiveLocks(context.Context, []string) (func() error, error)
	TransitionProjectPhysicalRestore(context.Context, requestctx.Context, projects.ProjectPhysicalRestoreTransitionInput) (projects.ProjectPhysicalRestoreTransitionResult, error)
}

type ProjectWorkspaceRestoreExecutor interface {
	ProjectWorkspaceRestorePlanner
	ApplyRestore(context.Context, WorkspaceArchivePlan, string) (WorkspaceArchiveInspection, error)
	InspectOperation(context.Context, string) (WorkspaceArchiveInspection, error)
	RecoverOperation(context.Context, string) (WorkspaceArchiveInspection, error)
}

type ProjectPhysicalRestoreResult struct {
	SchemaVersion   string                                `json:"schema_version"`
	OperationID     string                                `json:"operation_id"`
	PlanDigest      string                                `json:"plan_digest"`
	State           *projects.ProjectPhysicalRestoreState `json:"state,omitempty"`
	Project         *projects.ProjectRegistrationDetail   `json:"project,omitempty"`
	Workspace       *WorkspaceArchiveInspection           `json:"workspace,omitempty"`
	MutationBlocked bool                                  `json:"mutation_blocked"`
	ActivationState WorkspaceActivationState              `json:"activation_state"`
	Recoverable     bool                                  `json:"recoverable"`
	Replay          bool                                  `json:"replay"`
}

func (s ProjectRuntimeService) ApplyProjectPhysicalRestore(ctx context.Context, req requestctx.Context, plan ProjectPhysicalRestorePlan, reviewedDigest string) (ProjectPhysicalRestoreResult, error) {
	return s.applyProjectPhysicalRestore(ctx, req, plan, reviewedDigest, false)
}

func (s ProjectRuntimeService) RecoverProjectPhysicalRestore(ctx context.Context, req requestctx.Context, plan ProjectPhysicalRestorePlan, reviewedDigest string) (ProjectPhysicalRestoreResult, error) {
	return s.applyProjectPhysicalRestore(ctx, req, plan, reviewedDigest, true)
}

func (s ProjectRuntimeService) applyProjectPhysicalRestore(ctx context.Context, req requestctx.Context, plan ProjectPhysicalRestorePlan, reviewedDigest string, recoveryOnly bool) (result ProjectPhysicalRestoreResult, err error) {
	result = ProjectPhysicalRestoreResult{SchemaVersion: "storage.project_physical_restore_result.v1", OperationID: plan.Workspace.OperationID,
		PlanDigest: plan.PlanDigest, MutationBlocked: true, ActivationState: WorkspaceActivationInactive}
	if reviewedDigest == "" || reviewedDigest != plan.PlanDigest || !reflect.DeepEqual(req, plan.Request) {
		return result, fmt.Errorf("project restore requires exact reviewed digest and live request identity")
	}
	if err := ValidateProjectPhysicalRestorePlan(plan, s.WorkspaceRoots); err != nil {
		return result, err
	}
	plan, err = cloneProjectArchiveJSON(plan)
	if err != nil {
		return result, err
	}
	plan.ArchivePlan, err = normalizeRestoreArchivePlan(plan.ArchivePlan)
	if err != nil {
		return result, err
	}
	if plan.Repository.Source != nil {
		plan.Repository.Source.ProjectRoot = plan.RepositoryRoot
	}
	coordinator, ok := s.Projects.(ProjectPhysicalRestoreCoordinator)
	if !ok {
		return result, fmt.Errorf("project restore lifecycle coordinator is not configured")
	}
	executor, ok := s.WorkspaceMove.(ProjectWorkspaceRestoreExecutor)
	if !ok {
		return result, fmt.Errorf("project restore workspace executor is not configured")
	}
	repositories := s.RepositoryState
	if repositories == nil {
		repositories, _ = s.Projects.(ProjectRuntimeRepositoryStateService)
	}
	if repositories == nil {
		return result, fmt.Errorf("project restore repository service is not configured")
	}
	ids := []string{}
	for _, member := range plan.Repository.Members {
		ids = append(ids, member.RepositoryID)
	}
	keys := projects.ProjectArchiveLockKeys(plan.ArchiveState.ProjectID, plan.ArchiveState.ProjectSlug, plan.ArchiveState.OperationID, ids, nil)
	keys = append(keys, "loom:project-archive:archive:"+plan.Workspace.OperationID)
	sort.Strings(keys)
	release, err := coordinator.AcquireProjectArchiveLocks(ctx, keys)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, release()) }()

	current, _, state, err := s.currentProjectRestoreSnapshots(ctx, repositories, plan)
	if err != nil {
		return result, err
	}
	result.Project, result.State = &current, state
	result.Recoverable = state != nil && state.Phase != projects.ProjectRestorePhaseComplete
	inspection, exists, err := inspectOptionalProjectRestore(ctx, executor, plan.Workspace.OperationID)
	if err != nil {
		return result, err
	}
	if state == nil {
		if recoveryOnly || exists {
			return result, fmt.Errorf("project restore has no durable intent; orphan workspace operations cannot be adopted")
		}
		fresh, err := s.PlanProjectPhysicalRestore(ctx, req, plan.ArchivePlan, ProjectPhysicalRestorePlanInput{
			OperationID: plan.Workspace.OperationID, Reason: plan.Workspace.Reason, PlannedAt: plan.Workspace.PlannedAt,
		})
		if err != nil {
			return result, err
		}
		fresh.WorkspaceRequest = plan.WorkspaceRequest
		if err := SealProjectPhysicalRestorePlan(&fresh); err != nil {
			return result, err
		}
		if fresh.PlanDigest != plan.PlanDigest {
			return result, fmt.Errorf("project restore review fields changed during exact live replanning")
		}
		initial := projects.ProjectPhysicalRestoreState{
			SchemaVersion: projects.ProjectPhysicalRestoreStateSchemaVersion, Request: req,
			OperationID: plan.Workspace.OperationID, PlanDigest: plan.PlanDigest, WorkspacePlanDigest: plan.Workspace.PlanDigest,
			Phase: projects.ProjectRestorePhasePending, Status: "in_progress", MutationBlocked: true,
			ActivationState: projects.ProjectActivationStatusInactive, StartedAt: s.projectArchiveNowAtLeast(plan.Workspace.PlannedAt),
		}
		transition, err := coordinator.TransitionProjectPhysicalRestore(ctx, req, projectRestoreTransitionInput(plan, initial))
		if err != nil {
			return result, err
		}
		state = &transition.State
		result.State, result.Recoverable = state, true
		result.Project.Project.Project = transition.Project
	}
	if err := validateProjectRestoreStateBinding(plan, *state); err != nil {
		return result, err
	}
	if exists {
		if err := validateProjectRestoreOperation(plan, *state, inspection); err != nil {
			return result, err
		}
	} else if state.Phase != projects.ProjectRestorePhasePending {
		return result, fmt.Errorf("project restore claims completed files without a workspace operation")
	}
	if state.Phase != projects.ProjectRestorePhaseComplete {
		if err := s.failProjectRestore(ProjectRestoreBoundaryAfterIntent); err != nil {
			return result, err
		}
		if exists {
			inspection, err = executor.RecoverOperation(ctx, plan.Workspace.OperationID)
		} else {
			inspection, err = executor.ApplyRestore(ctx, plan.Workspace, plan.Workspace.PlanDigest)
		}
		if err != nil {
			if observed, found, inspectErr := inspectOptionalProjectRestore(ctx, executor, plan.Workspace.OperationID); inspectErr == nil && found {
				result.Workspace = &observed
			}
			return result, err
		}
	}
	result.Workspace = &inspection
	if err := validateCompletedProjectRestore(plan, *state, inspection); err != nil {
		return result, err
	}
	if state.Phase == projects.ProjectRestorePhaseComplete {
		transition, err := coordinator.TransitionProjectPhysicalRestore(ctx, req, projectRestoreTransitionInput(plan, *state))
		if err != nil {
			return result, err
		}
		if !transition.Replay {
			return result, fmt.Errorf("project restore terminal transaction did not replay")
		}
		result.Replay, result.Recoverable = true, false
		return result, nil
	}
	if err := s.failProjectRestore(ProjectRestoreBoundaryAfterWorkspaceMove); err != nil {
		return result, err
	}
	if state.Phase == projects.ProjectRestorePhasePending {
		next := *state
		next.Phase = projects.ProjectRestorePhaseProjectStatePending
		next.WorkspaceCompletedAt = inspection.Operation.CompletedAt
		next.ActiveManifestDigest, err = projectWorkspaceManifestDigest(inspection)
		if err != nil {
			return result, err
		}
		transition, err := coordinator.TransitionProjectPhysicalRestore(ctx, req, projectRestoreTransitionInput(plan, next))
		if err != nil {
			return result, err
		}
		state = &transition.State
		result.State = state
		result.Project.Project.Project = transition.Project
	}
	if err := s.failProjectRestore(ProjectRestoreBoundaryBeforeProjectCommit); err != nil {
		return result, err
	}
	// Recheck snapshots after physical work; runtime/source changes are never
	// normalized away merely because the payload already moved.
	if _, _, _, err := s.currentProjectRestoreSnapshots(ctx, repositories, plan); err != nil {
		return result, err
	}
	next := *state
	at := s.projectArchiveNowAtLeast(*state.WorkspaceCompletedAt)
	next.Phase, next.Status, next.RestoredAt = projects.ProjectRestorePhaseComplete, "restored", &at
	transition, err := coordinator.TransitionProjectPhysicalRestore(ctx, req, projectRestoreTransitionInput(plan, next))
	if err != nil {
		return result, err
	}
	result.State, result.Recoverable = &transition.State, false
	// The terminal transaction is durable, but the old detail still describes
	// archived memberships. Omit it until the post-commit read is verified.
	result.Project = nil
	if err := s.failProjectRestore(ProjectRestoreBoundaryAfterProjectCommit); err != nil {
		return result, err
	}
	current, _, _, err = s.currentProjectRestoreSnapshots(ctx, repositories, plan)
	if err != nil {
		return result, err
	}
	result.Project = &current
	return result, nil
}

func (s ProjectRuntimeService) currentProjectRestoreSnapshots(ctx context.Context, repositories ProjectRuntimeRepositoryStateService, plan ProjectPhysicalRestorePlan) (projects.ProjectRegistrationDetail, projects.ProjectRepositoryReadModel, *projects.ProjectPhysicalRestoreState, error) {
	current, err := s.Projects.GetProjectRegistrationStatus(ctx, plan.ArchiveState.ProjectID)
	if err != nil {
		return current, projects.ProjectRepositoryReadModel{}, nil, err
	}
	repository, err := repositories.ReadProjectRepositoryState(ctx, plan.ArchiveState.ProjectID)
	if err != nil {
		return current, repository, nil, err
	}
	archive, present, err := decodeExactProjectPhysicalArchiveState(current.Project.Project.ArchiveState)
	if err != nil {
		return current, repository, nil, err
	}
	if !present {
		return current, repository, nil, fmt.Errorf("project restore durable archive state is missing")
	}
	state := archive.Restore
	archive.Restore = nil
	if !reflect.DeepEqual(archive, plan.ArchiveState) {
		return current, repository, state, fmt.Errorf("project restore original archive state changed")
	}
	if state != nil {
		if err := validateProjectRestoreStateBinding(plan, *state); err != nil {
			return current, repository, state, err
		}
	}
	actual, err := canonicalProjectRegistrationDetail(current)
	if err != nil {
		return current, repository, state, err
	}
	repos, err := canonicalProjectRepositoryState(repository)
	if err != nil {
		return current, repository, state, err
	}
	actual.Project.Project.ArchiveState = plan.Registration.Project.Project.ArchiveState
	actual.Project.Project.UpdatedAt = plan.Registration.Project.Project.UpdatedAt
	repos.Project.UpdatedAt = plan.Repository.Project.UpdatedAt
	if state != nil && state.Phase == projects.ProjectRestorePhaseComplete {
		if actual.Project.Project.Status != "active" || repos.Project.Status != "active" || actual.Registration == nil || actual.Registration.RegistrationStatus != projects.ProjectRegistrationStatusRegistered || actual.Registration.ActivationStatus != projects.ProjectActivationStatusInactive {
			return current, repository, state, fmt.Errorf("restored project is not active custody with inactive runtime")
		}
		actual.Project.Project.Status, repos.Project.Status = "archived", "archived"
		actual.Registration.RegistrationStatus = projects.ProjectRegistrationStatusArchived
		actual.Registration.UpdatedAt = plan.Registration.Registration.UpdatedAt
		for i := range repos.Members {
			member := &repos.Members[i]
			if member.MembershipLifecycle != projects.RepositoryLifecycleActive {
				return current, repository, state, fmt.Errorf("restored membership is not active")
			}
			member.MembershipLifecycle = projects.RepositoryLifecycleArchived
			if member.RepositoryOwnerProjectID == plan.ArchiveState.ProjectID {
				if member.RepositoryLifecycle != projects.RepositoryLifecycleActive {
					return current, repository, state, fmt.Errorf("restored owned repository is not active")
				}
				member.RepositoryLifecycle = projects.RepositoryLifecycleArchived
			}
		}
	}
	if !reflect.DeepEqual(actual, plan.Registration) || !reflect.DeepEqual(repos, plan.Repository) {
		return current, repository, state, fmt.Errorf("project restore current source, runtime or lifecycle changed outside allowed projections")
	}
	return current, repository, state, nil
}

func validateProjectRestoreStateBinding(plan ProjectPhysicalRestorePlan, state projects.ProjectPhysicalRestoreState) error {
	if err := projects.ValidateProjectPhysicalRestoreState(plan.ArchiveState, state); err != nil {
		return err
	}
	if !reflect.DeepEqual(state.Request, plan.Request) || state.OperationID != plan.Workspace.OperationID || state.PlanDigest != plan.PlanDigest || state.WorkspacePlanDigest != plan.Workspace.PlanDigest || state.StartedAt.Before(plan.Workspace.PlannedAt) {
		return fmt.Errorf("project restore durable request or plan binding changed")
	}
	return nil
}

func validateProjectRestoreOperation(plan ProjectPhysicalRestorePlan, state projects.ProjectPhysicalRestoreState, inspection WorkspaceArchiveInspection) error {
	op := inspection.Operation
	if err := ValidateWorkspaceArchiveOperation(op); err != nil {
		return err
	}
	if !reflect.DeepEqual(inspection.Plan, plan.Workspace) || op.OperationID != state.OperationID || op.OperationKind != WorkspaceOperationRestore || op.Kind != WorkspaceKindProject ||
		op.ObjectID != plan.Workspace.ObjectID || op.Slug != plan.Workspace.Slug || op.PlanDigest != state.WorkspacePlanDigest || op.ActorID != plan.Request.ActorID || op.Reason != plan.Workspace.Reason ||
		!op.PlannedAt.Equal(plan.Workspace.PlannedAt) || !reflect.DeepEqual(op.Source, plan.Workspace.Source) || !reflect.DeepEqual(op.Destination, plan.Workspace.Destination) ||
		op.IntentCommittedAt == nil || op.IntentCommittedAt.Before(state.StartedAt) {
		return fmt.Errorf("project restore generic operation contradicts durable intent")
	}
	return nil
}

func validateCompletedProjectRestore(plan ProjectPhysicalRestorePlan, state projects.ProjectPhysicalRestoreState, inspection WorkspaceArchiveInspection) error {
	if err := validateProjectRestoreOperation(plan, state, inspection); err != nil {
		return err
	}
	op, manifest := inspection.Operation, inspection.Manifest
	if op.Phase != PhaseRestoreComplete || op.Status != OperationStatusComplete || op.CompletedAt == nil || op.CompletedAt.Before(state.StartedAt) ||
		inspection.Custody != CustodyActive || inspection.ActivationState != WorkspaceActivationInactive || manifest == nil {
		return fmt.Errorf("project restore workspace is not complete and inactive")
	}
	if manifest.RestoreOperationID != state.OperationID || manifest.ArchiveOperationID != plan.ArchiveState.OperationID || manifest.ObjectID != plan.Workspace.ObjectID || manifest.Slug != plan.Workspace.Slug ||
		manifest.LifecycleState != WorkspaceLifecycleActive || manifest.ActivePath != plan.Workspace.Destination.Path {
		return fmt.Errorf("project restore active manifest contradicts the reviewed custody")
	}
	if err := ValidateWorkspaceArchiveManifestForOperations(*manifest, plan.Archive.Operation, &op); err != nil {
		return err
	}
	expected := *plan.Archive.Manifest
	expected.LifecycleState = WorkspaceLifecycleActive
	expected.RestoreOperationID, expected.RestorePlanDigest = state.OperationID, state.WorkspacePlanDigest
	expected.RestoredAt = op.PayloadMovedAt
	expected.LifecycleEventID = deterministicArchiveEventID(op.OperationID)
	// Authentication is verified by the generic kernel; every archived semantic
	// field must still agree with the exact preserved project review.
	expected.Authentication = manifest.Authentication
	if !sameWorkspaceManifest(expected, *manifest) {
		return fmt.Errorf("project restore active manifest changed original archive evidence")
	}
	if state.Phase != projects.ProjectRestorePhasePending {
		digest, err := projectWorkspaceManifestDigest(inspection)
		if err != nil || digest != state.ActiveManifestDigest || !state.WorkspaceCompletedAt.Equal(*op.CompletedAt) {
			return fmt.Errorf("project restore completion evidence changed")
		}
	}
	return nil
}

func inspectOptionalProjectRestore(ctx context.Context, executor ProjectWorkspaceRestoreExecutor, operationID string) (WorkspaceArchiveInspection, bool, error) {
	inspection, err := executor.InspectOperation(ctx, operationID)
	if err == nil {
		return inspection, true, nil
	}
	if err.Error() == fmt.Sprintf("workspace operation %q was not found", operationID) {
		return WorkspaceArchiveInspection{}, false, nil
	}
	return inspection, false, err
}

func projectRestoreTransitionInput(plan ProjectPhysicalRestorePlan, state projects.ProjectPhysicalRestoreState) projects.ProjectPhysicalRestoreTransitionInput {
	return projects.ProjectPhysicalRestoreTransitionInput{Archive: projectArchiveTransitionInput(plan.ArchivePlan, plan.ArchiveState), State: state}
}

func (s ProjectRuntimeService) failProjectRestore(boundary ProjectPhysicalRestoreFailureBoundary) error {
	if s.RestoreFailureHook == nil {
		return nil
	}
	return s.RestoreFailureHook(boundary)
}
