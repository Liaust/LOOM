package storagearchive

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalArchiveResultSchemaVersion = "storage.project_physical_archive_result.v1"

type ProjectPhysicalArchiveFailureBoundary string

const (
	ProjectArchiveBoundaryAfterDeactivation        ProjectPhysicalArchiveFailureBoundary = "after_deactivation"
	ProjectArchiveBoundaryAfterWorkspaceMove       ProjectPhysicalArchiveFailureBoundary = "after_workspace_move"
	ProjectArchiveBoundaryBeforeProjectStateCommit ProjectPhysicalArchiveFailureBoundary = "before_project_state_commit"
)

type ProjectPhysicalArchiveFailureHook func(ProjectPhysicalArchiveFailureBoundary) error

type ProjectPhysicalArchiveCoordinator interface {
	AcquireProjectArchiveLocks(context.Context, []string) (func() error, error)
	TransitionProjectPhysicalArchive(context.Context, requestctx.Context, projects.ProjectPhysicalArchiveTransitionInput) (projects.ProjectPhysicalArchiveTransitionResult, error)
}

type ProjectWorkspaceArchiveExecutor interface {
	ProjectWorkspaceArchivePlanner
	ApplyArchive(context.Context, WorkspaceArchivePlan, string) (WorkspaceArchiveInspection, error)
	InspectArchive(context.Context, string) (WorkspaceArchiveInspection, error)
	RecoverArchive(context.Context, string) (WorkspaceArchiveInspection, error)
}

type ProjectPhysicalArchiveResult struct {
	SchemaVersion   string                               `json:"schema_version"`
	OperationID     string                               `json:"operation_id"`
	PlanDigest      string                               `json:"plan_digest"`
	Phase           projects.ProjectPhysicalArchivePhase `json:"phase"`
	MutationBlocked bool                                 `json:"mutation_blocked"`
	Recoverable     bool                                 `json:"recoverable"`
	Replay          bool                                 `json:"replay,omitempty"`
	Project         projects.ProjectRegistrationDetail   `json:"project"`
	State           projects.ProjectPhysicalArchiveState `json:"state"`
	Deactivations   []projects.ProjectDeactivationResult `json:"deactivations,omitempty"`
	Workspace       *WorkspaceArchiveInspection          `json:"workspace,omitempty"`
	EventID         string                               `json:"event_id,omitempty"`
}

func (s ProjectRuntimeService) ApplyProjectPhysicalArchive(ctx context.Context, plan ProjectPhysicalArchivePlan, reviewedDigest string) (ProjectPhysicalArchiveResult, error) {
	return s.applyProjectPhysicalArchive(ctx, plan, reviewedDigest, false)
}

func (s ProjectRuntimeService) RecoverProjectPhysicalArchive(ctx context.Context, plan ProjectPhysicalArchivePlan, reviewedDigest string) (ProjectPhysicalArchiveResult, error) {
	return s.applyProjectPhysicalArchive(ctx, plan, reviewedDigest, true)
}

func (s ProjectRuntimeService) applyProjectPhysicalArchive(ctx context.Context, plan ProjectPhysicalArchivePlan, reviewedDigest string, recoveryOnly bool) (ProjectPhysicalArchiveResult, error) {
	result := ProjectPhysicalArchiveResult{
		SchemaVersion: ProjectPhysicalArchiveResultSchemaVersion,
		OperationID:   plan.Workspace.OperationID,
		PlanDigest:    plan.PlanDigest,
	}
	if reviewedDigest == "" || reviewedDigest != plan.PlanDigest {
		return result, fmt.Errorf("project archive apply requires the exact reviewed plan digest")
	}
	if err := ValidateProjectPhysicalArchivePlanEnvelope(plan, s.WorkspaceRoots); err != nil {
		return result, err
	}
	coordinator, ok := s.Projects.(ProjectPhysicalArchiveCoordinator)
	if !ok {
		return result, fmt.Errorf("project archive lifecycle coordinator is not configured")
	}
	executor, ok := s.WorkspaceMove.(ProjectWorkspaceArchiveExecutor)
	if !ok {
		return result, fmt.Errorf("project archive workspace executor is not configured")
	}
	repositoryService := s.RepositoryState
	if repositoryService == nil {
		if inferred, ok := s.Projects.(ProjectRuntimeRepositoryStateService); ok {
			repositoryService = inferred
		}
	}
	if repositoryService == nil || s.Activation == nil {
		return result, fmt.Errorf("project archive repository and activation services are required")
	}
	archiveActivation, ok := s.Activation.(ProjectPhysicalArchiveActivationService)
	if !ok {
		return result, fmt.Errorf("project archive activation service does not support physical archive deactivation")
	}

	repositoryIDs := make([]string, 0, len(plan.Repository.Members))
	for _, member := range plan.Repository.Members {
		repositoryIDs = append(repositoryIDs, member.RepositoryID)
	}
	runtimeFacets := make([]string, 0, len(plan.Deactivations))
	for _, deactivation := range plan.Deactivations {
		runtimeFacets = append(runtimeFacets, deactivation.Input.Facet)
	}
	lockKeys := projects.ProjectArchiveLockKeys(plan.Custody.ProjectID, plan.Custody.ProjectSlug, plan.Workspace.OperationID, repositoryIDs, runtimeFacets)
	release, err := coordinator.AcquireProjectArchiveLocks(ctx, lockKeys)
	if err != nil {
		return result, err
	}
	defer func() { _ = release() }()

	current, err := s.Projects.GetProjectRegistrationStatus(ctx, plan.Custody.ProjectID)
	if err != nil {
		return result, err
	}
	repository, err := repositoryService.ReadProjectRepositoryState(ctx, plan.Custody.ProjectID)
	if err != nil {
		return result, err
	}
	state, hasState, err := decodeExactProjectPhysicalArchiveState(current.Project.Project.ArchiveState)
	if err != nil {
		return result, err
	}
	if state.Restore != nil {
		return result, fmt.Errorf("project archive has entered restore; archive replay is read-only history")
	}

	existingWorkspace, workspaceExists, err := inspectOptionalProjectWorkspaceArchive(ctx, executor, plan.Workspace.OperationID)
	if err != nil {
		return result, err
	}
	if recoveryOnly && !hasState && !workspaceExists {
		return result, fmt.Errorf("project archive operation has no durable state to recover")
	}
	if !hasState && workspaceExists {
		return result, fmt.Errorf("project archive workspace operation exists without prior mutation-blocked project state")
	}
	if !hasState {
		if plan.Registration.Registration != nil && plan.Registration.Registration.ContractSchemaVersion == projectcontracts.ProjectSchemaV05 && !plan.InspectRegisteredServices {
			return result, fmt.Errorf("project archive plan predates registered-service inspection; review a fresh plan")
		}
		if err := ValidateProjectPhysicalArchivePlan(plan, s.WorkspaceRoots); err != nil {
			return result, err
		}
		if err := validateCurrentProjectArchiveSnapshots(plan, current, repository, nil); err != nil {
			return result, err
		}
	} else {
		if err := validateProjectPhysicalArchiveStateAgainstPlan(plan, state, existingWorkspace, workspaceExists); err != nil {
			return result, err
		}
		if err := validateCurrentProjectArchiveSnapshots(plan, current, repository, &state); err != nil {
			return result, err
		}
	}

	if hasState && state.Phase == projects.ProjectArchivePhaseComplete {
		inspection := existingWorkspace
		if err := validateCompletedProjectWorkspaceArchive(plan, inspection); err != nil {
			return projectArchiveFailureResult(result, current, state, &inspection), err
		}
		if digest, err := projectWorkspaceManifestDigest(inspection); err != nil || digest != state.ArchiveManifestDigest {
			return projectArchiveFailureResult(result, current, state, &inspection), fmt.Errorf("project archive state manifest digest does not match generic custody evidence")
		}
		if err := validateArchivedProjectPhysicalState(plan, current, repository, state); err != nil {
			return projectArchiveFailureResult(result, current, state, &inspection), err
		}
		terminal, err := coordinator.TransitionProjectPhysicalArchive(ctx, plan.Request, projectArchiveTransitionInput(plan, state))
		if err != nil {
			return projectArchiveFailureResult(result, current, state, &inspection), err
		}
		if !terminal.Replay {
			return projectArchiveFailureResult(result, current, state, &inspection), fmt.Errorf("project archive terminal lifecycle did not validate as an exact replay")
		}
		quiescenceDigest, _, err := s.verifyProjectRuntimeQuiescence(ctx, plan)
		if err != nil {
			return projectArchiveFailureResult(result, current, state, &inspection), err
		}
		if quiescenceDigest != state.RuntimeQuiescenceDigest {
			return projectArchiveFailureResult(result, current, state, &inspection), fmt.Errorf("project archive terminal runtime quiescence evidence changed")
		}
		result.Project = current
		result.State = state
		result.Phase = state.Phase
		result.MutationBlocked = true
		result.Recoverable = false
		result.Replay = true
		result.Workspace = &inspection
		return result, nil
	}

	transitionInput := projectArchiveTransitionInput(plan, state)
	if !hasState {
		startedAt := normalizeWorkspaceTimestamp(plan.Workspace.PlannedAt)
		state = projects.ProjectPhysicalArchiveState{
			SchemaVersion:       projects.ProjectPhysicalArchiveStateSchemaVersion,
			Status:              projects.ProjectPhysicalArchiveStatusInProgress,
			Phase:               projects.ProjectArchivePhaseDeactivationPending,
			MutationBlocked:     true,
			ProjectID:           plan.Custody.ProjectID,
			ProjectSlug:         plan.Custody.ProjectSlug,
			OperationID:         plan.Workspace.OperationID,
			PlanDigest:          plan.PlanDigest,
			WorkspacePlanDigest: plan.Workspace.PlanDigest,
			ActivePath:          plan.Workspace.Source.Path.AbsolutePath,
			ArchivePath:         plan.Workspace.Destination.Path.AbsolutePath,
			ActorID:             plan.Workspace.ActorID,
			Reason:              plan.Workspace.Reason,
			StartedAt:           startedAt,
		}
		transitionInput = projectArchiveTransitionInput(plan, state)
		transition, err := coordinator.TransitionProjectPhysicalArchive(ctx, plan.Request, transitionInput)
		if err != nil {
			return result, err
		}
		current.Project.Project = transition.Project
		hasState = true
	}
	result = projectArchiveFailureResult(result, current, state, nil)

	deactivations := make([]projects.ProjectDeactivationResult, 0, len(plan.Deactivations))
	for _, reviewed := range plan.Deactivations {
		input := reviewed.Input
		input.DryRun = false
		deactivated, err := archiveActivation.DeactivateForPhysicalArchive(ctx, plan.Request, plan.Custody.ProjectID, input)
		if err != nil {
			result.Deactivations = deactivations
			return result, fmt.Errorf("deactivate project archive facet %s: %w", input.Facet, err)
		}
		if err := validateAppliedProjectDeactivation(reviewed, deactivated); err != nil {
			result.Deactivations = append(deactivations, deactivated)
			return result, err
		}
		deactivations = append(deactivations, deactivated)
	}
	current, err = s.Projects.GetProjectRegistrationStatus(ctx, plan.Custody.ProjectID)
	if err != nil {
		result.Deactivations = deactivations
		return result, err
	}
	if err := validateProjectRuntimeFullyDeactivated(plan, current); err != nil {
		result.Deactivations = deactivations
		return result, err
	}
	quiescenceDigest, _, err := s.verifyProjectRuntimeQuiescence(ctx, plan)
	if err != nil {
		result.Deactivations = deactivations
		return result, err
	}
	if state.Phase == projects.ProjectArchivePhaseDeactivationPending {
		at := s.projectArchiveNowAtLeast(state.StartedAt)
		state.Phase = projects.ProjectArchivePhaseRuntimeDeactivated
		state.RuntimeDeactivatedAt = &at
		state.RuntimeQuiescenceDigest = quiescenceDigest
		transition, err := coordinator.TransitionProjectPhysicalArchive(ctx, plan.Request, projectArchiveTransitionInput(plan, state))
		if err != nil {
			result.Deactivations = deactivations
			return result, err
		}
		current.Project.Project = transition.Project
	} else if state.RuntimeQuiescenceDigest != quiescenceDigest {
		result.Deactivations = deactivations
		return result, fmt.Errorf("project archive runtime quiescence evidence changed")
	}
	result = projectArchiveFailureResult(result, current, state, nil)
	result.Deactivations = deactivations
	if err := s.failProjectArchive(ProjectArchiveBoundaryAfterDeactivation); err != nil {
		return result, err
	}

	inspection := existingWorkspace
	if !workspaceExists || inspection.Operation.Phase != PhaseArchiveComplete {
		inspection, err = executor.ApplyArchive(ctx, plan.Workspace, plan.Workspace.PlanDigest)
		if err != nil {
			if observed, exists, inspectErr := inspectOptionalProjectWorkspaceArchive(ctx, executor, plan.Workspace.OperationID); inspectErr == nil && exists {
				result.Workspace = &observed
			}
			return result, err
		}
	}
	if err := validateCompletedProjectWorkspaceArchive(plan, inspection); err != nil {
		result.Workspace = &inspection
		return result, err
	}
	result.Workspace = &inspection
	if err := s.failProjectArchive(ProjectArchiveBoundaryAfterWorkspaceMove); err != nil {
		return result, err
	}

	manifestDigest, err := projectWorkspaceManifestDigest(inspection)
	if err != nil {
		return result, err
	}
	if state.Phase != projects.ProjectArchivePhaseProjectStatePending {
		if inspection.Operation.CompletedAt == nil {
			return result, fmt.Errorf("project archive workspace completion timestamp is missing")
		}
		workspaceAt := normalizeWorkspaceTimestamp(*inspection.Operation.CompletedAt)
		state.Phase = projects.ProjectArchivePhaseProjectStatePending
		state.WorkspaceCompletedAt = &workspaceAt
		state.ArchiveManifestDigest = manifestDigest
		transition, err := coordinator.TransitionProjectPhysicalArchive(ctx, plan.Request, projectArchiveTransitionInput(plan, state))
		if err != nil {
			return result, err
		}
		current.Project.Project = transition.Project
	} else if state.ArchiveManifestDigest != manifestDigest {
		return result, fmt.Errorf("project archive in-progress manifest digest conflicts with generic custody evidence")
	}
	result = projectArchiveFailureResult(result, current, state, &inspection)
	result.Deactivations = deactivations
	if err := s.failProjectArchive(ProjectArchiveBoundaryBeforeProjectStateCommit); err != nil {
		return result, err
	}

	archivedAt := s.projectArchiveNowAtLeast(*state.WorkspaceCompletedAt)
	state.Phase = projects.ProjectArchivePhaseComplete
	state.Status = projects.ProjectPhysicalArchiveStatusArchived
	state.ArchivedAt = &archivedAt
	transition, err := coordinator.TransitionProjectPhysicalArchive(ctx, plan.Request, projectArchiveTransitionInput(plan, state))
	if err != nil {
		return result, err
	}
	current, err = s.Projects.GetProjectRegistrationStatus(ctx, plan.Custody.ProjectID)
	if err != nil {
		return result, err
	}
	repository, err = repositoryService.ReadProjectRepositoryState(ctx, plan.Custody.ProjectID)
	if err != nil {
		return result, err
	}
	if err := validateProjectPhysicalArchiveStateAgainstPlan(plan, transition.State, inspection, true); err != nil {
		return result, err
	}
	if err := validateCurrentProjectArchiveSnapshots(plan, current, repository, &transition.State); err != nil {
		return result, err
	}
	if err := validateArchivedProjectPhysicalState(plan, current, repository, transition.State); err != nil {
		return result, err
	}
	result.Project = current
	result.State = transition.State
	result.Phase = transition.State.Phase
	result.MutationBlocked = true
	result.Recoverable = false
	result.Replay = transition.Replay
	result.EventID = transition.EventID
	result.Workspace = &inspection
	result.Deactivations = deactivations
	return result, nil
}

func inspectOptionalProjectWorkspaceArchive(ctx context.Context, executor ProjectWorkspaceArchiveExecutor, operationID string) (WorkspaceArchiveInspection, bool, error) {
	inspection, err := executor.InspectArchive(ctx, operationID)
	if err == nil {
		return inspection, true, nil
	}
	if strings.Contains(err.Error(), "was not found") {
		return WorkspaceArchiveInspection{}, false, nil
	}
	return WorkspaceArchiveInspection{}, false, err
}

func validateCompletedProjectWorkspaceArchive(plan ProjectPhysicalArchivePlan, inspection WorkspaceArchiveInspection) error {
	if inspection.Operation.OperationID != plan.Workspace.OperationID || inspection.Operation.PlanDigest != plan.Workspace.PlanDigest ||
		inspection.Operation.Phase != PhaseArchiveComplete || inspection.Operation.Status != OperationStatusComplete || inspection.Custody != CustodyArchived || inspection.Manifest == nil {
		return fmt.Errorf("project archive workspace operation is not complete and authenticated")
	}
	if inspection.Manifest.ArchiveOperationID != plan.Workspace.OperationID || inspection.Manifest.PlanDigest != plan.Workspace.PlanDigest ||
		inspection.Manifest.ObjectID != plan.Custody.ProjectID || inspection.Manifest.Slug != plan.Custody.ProjectSlug ||
		inspection.Manifest.ArchivePayloadPath.AbsolutePath != plan.Workspace.Destination.Path.AbsolutePath {
		return fmt.Errorf("project archive workspace evidence contradicts the reviewed project plan")
	}
	return nil
}

func projectWorkspaceManifestDigest(inspection WorkspaceArchiveInspection) (string, error) {
	if inspection.Manifest == nil {
		return "", fmt.Errorf("project archive workspace manifest is missing")
	}
	payload, err := json.Marshal(inspection.Manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeExactProjectPhysicalArchiveState(raw json.RawMessage) (projects.ProjectPhysicalArchiveState, bool, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null")) {
		return projects.ProjectPhysicalArchiveState{}, false, nil
	}
	var state projects.ProjectPhysicalArchiveState
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return projects.ProjectPhysicalArchiveState{}, false, fmt.Errorf("project physical archive state is invalid: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return projects.ProjectPhysicalArchiveState{}, false, fmt.Errorf("project physical archive state must contain one exact object")
	}
	if err := projects.ValidateProjectPhysicalArchiveState(state); err != nil {
		return projects.ProjectPhysicalArchiveState{}, false, fmt.Errorf("project physical archive state is invalid: %w", err)
	}
	return state, true, nil
}

func validateProjectPhysicalArchiveStateAgainstPlan(plan ProjectPhysicalArchivePlan, state projects.ProjectPhysicalArchiveState, workspace WorkspaceArchiveInspection, workspaceExists bool) error {
	if state.ProjectID != plan.Custody.ProjectID || state.ProjectSlug != plan.Custody.ProjectSlug ||
		state.OperationID != plan.Workspace.OperationID || state.PlanDigest != plan.PlanDigest ||
		state.WorkspacePlanDigest != plan.Workspace.PlanDigest || state.ActivePath != plan.Workspace.Source.Path.AbsolutePath ||
		state.ArchivePath != plan.Workspace.Destination.Path.AbsolutePath || state.ActorID != plan.Workspace.ActorID ||
		state.Reason != plan.Workspace.Reason || !state.StartedAt.Equal(normalizeWorkspaceTimestamp(plan.Workspace.PlannedAt)) {
		return fmt.Errorf("project physical archive state conflicts with immutable reviewed plan fields")
	}
	if !workspaceExists {
		if state.Phase == projects.ProjectArchivePhaseProjectStatePending || state.Phase == projects.ProjectArchivePhaseComplete {
			return fmt.Errorf("project archive state claims workspace completion without generic operation evidence")
		}
		return nil
	}
	if state.Phase == projects.ProjectArchivePhaseDeactivationPending {
		return fmt.Errorf("generic archive operation exists before exact runtime quiescence evidence")
	}
	if err := ValidateWorkspaceArchiveOperation(workspace.Operation); err != nil {
		return fmt.Errorf("project archive generic operation evidence is invalid: %w", err)
	}
	if !reflect.DeepEqual(workspace.Plan, plan.Workspace) || workspace.Operation.OperationID != plan.Workspace.OperationID ||
		workspace.Operation.PlanDigest != plan.Workspace.PlanDigest || workspace.Operation.OperationKind != WorkspaceOperationArchive ||
		workspace.Operation.Kind != WorkspaceKindProject || workspace.Operation.ObjectID != plan.Custody.ProjectID ||
		workspace.Operation.Slug != plan.Custody.ProjectSlug || workspace.Operation.ActorID != plan.Workspace.ActorID ||
		workspace.Operation.Reason != plan.Workspace.Reason || !workspace.Operation.PlannedAt.Equal(plan.Workspace.PlannedAt) ||
		!reflect.DeepEqual(workspace.Operation.Source, plan.Workspace.Source) || !reflect.DeepEqual(workspace.Operation.Destination, plan.Workspace.Destination) {
		return fmt.Errorf("project archive generic operation evidence contradicts the reviewed plan")
	}
	if workspace.Operation.IntentCommittedAt != nil && state.RuntimeDeactivatedAt != nil && workspace.Operation.IntentCommittedAt.Before(*state.RuntimeDeactivatedAt) {
		return fmt.Errorf("generic archive intent precedes exact runtime quiescence")
	}
	if workspace.Operation.Phase != PhaseArchiveComplete {
		if state.Phase == projects.ProjectArchivePhaseProjectStatePending || state.Phase == projects.ProjectArchivePhaseComplete {
			return fmt.Errorf("project archive state advances beyond incomplete generic custody evidence")
		}
		return nil
	}
	if err := validateCompletedProjectWorkspaceArchive(plan, workspace); err != nil {
		return err
	}
	if workspace.Operation.CompletedAt == nil || (state.RuntimeDeactivatedAt != nil && workspace.Operation.CompletedAt.Before(*state.RuntimeDeactivatedAt)) {
		return fmt.Errorf("generic archive completion precedes exact runtime quiescence")
	}
	if state.Phase == projects.ProjectArchivePhaseProjectStatePending || state.Phase == projects.ProjectArchivePhaseComplete {
		if state.WorkspaceCompletedAt == nil || !state.WorkspaceCompletedAt.Equal(*workspace.Operation.CompletedAt) {
			return fmt.Errorf("project archive state workspace timestamp does not match generic custody evidence")
		}
		digest, err := projectWorkspaceManifestDigest(workspace)
		if err != nil || state.ArchiveManifestDigest != digest {
			return fmt.Errorf("project archive state manifest digest does not match generic custody evidence")
		}
	}
	return nil
}

func validateCurrentProjectArchiveSnapshots(plan ProjectPhysicalArchivePlan, current projects.ProjectRegistrationDetail, repository projects.ProjectRepositoryReadModel, state *projects.ProjectPhysicalArchiveState) error {
	currentCanonical, err := canonicalProjectRegistrationDetail(current)
	if err != nil {
		return err
	}
	repositoryCanonical, err := canonicalProjectRepositoryState(repository)
	if err != nil {
		return err
	}
	if state == nil {
		if !reflect.DeepEqual(currentCanonical, plan.Registration) || !reflect.DeepEqual(repositoryCanonical, plan.Repository) {
			return fmt.Errorf("project registration, runtime, or repository state changed since the reviewed archive plan")
		}
		return nil
	}
	if err := normalizeAllowedProjectArchiveRegistrationLifecycle(plan, &currentCanonical, *state); err != nil {
		return err
	}
	if err := normalizeAllowedProjectArchiveRepositoryLifecycle(plan, &repositoryCanonical, *state); err != nil {
		return err
	}
	if !reflect.DeepEqual(currentCanonical, plan.Registration) || !reflect.DeepEqual(repositoryCanonical, plan.Repository) {
		return fmt.Errorf("project archive recovery snapshot changed outside allowed lifecycle fields")
	}
	return nil
}

func normalizeAllowedProjectArchiveRegistrationLifecycle(plan ProjectPhysicalArchivePlan, actual *projects.ProjectRegistrationDetail, state projects.ProjectPhysicalArchiveState) error {
	want := plan.Registration
	requireDeactivated := state.Phase != projects.ProjectArchivePhaseDeactivationPending
	terminal := state.Phase == projects.ProjectArchivePhaseComplete
	parsed, present, err := decodeExactProjectPhysicalArchiveState(actual.Project.Project.ArchiveState)
	if err != nil || !present || !reflect.DeepEqual(parsed, state) {
		return fmt.Errorf("project archive recovery state does not match its exact lifecycle snapshot")
	}
	actual.Project.Project.ArchiveState = want.Project.Project.ArchiveState
	actual.Project.Project.UpdatedAt = want.Project.Project.UpdatedAt
	if terminal {
		if actual.Project.Project.Status != "archived" {
			return fmt.Errorf("project archive terminal project lifecycle is inconsistent")
		}
		actual.Project.Project.Status = want.Project.Project.Status
	} else if actual.Project.Project.Status != want.Project.Project.Status {
		return fmt.Errorf("project archive in-progress project lifecycle changed")
	}
	if actual.Registration == nil || want.Registration == nil {
		return fmt.Errorf("project archive registration binding disappeared")
	}
	if terminal {
		if actual.Registration.RegistrationStatus != projects.ProjectRegistrationStatusArchived || actual.Registration.ActivationStatus != projects.ProjectActivationStatusInactive {
			return fmt.Errorf("project archive terminal registration lifecycle is inconsistent")
		}
		actual.Registration.RegistrationStatus = want.Registration.RegistrationStatus
		actual.Registration.ActivationStatus = want.Registration.ActivationStatus
		actual.Registration.UpdatedAt = want.Registration.UpdatedAt
	}

	facetSources := projectArchiveFacetDeactivationSources(plan)
	if len(actual.Facets) != len(want.Facets) || len(actual.ScriptExposures) != len(want.ScriptExposures) ||
		len(actual.ScheduleRegistrations) != len(want.ScheduleRegistrations) || len(actual.DirectEventRegistrations) != len(want.DirectEventRegistrations) ||
		len(actual.WatchedRootRegistrations) != len(want.WatchedRootRegistrations) || len(actual.ConnectorRegistrations) != len(want.ConnectorRegistrations) ||
		len(actual.ModuleRegistrations) != len(want.ModuleRegistrations) || len(actual.WorkflowRegistrations) != len(want.WorkflowRegistrations) {
		return fmt.Errorf("project archive recovery runtime registration set changed")
	}
	for index := range actual.Facets {
		facet := &actual.Facets[index]
		planned := want.Facets[index]
		sourceFacet, allowed := facetSources[planned.FacetKey]
		if !allowed {
			continue
		}
		if reflect.DeepEqual(*facet, planned) {
			if requireDeactivated && facet.FacetStatus != projects.ProjectFacetStatusDisabled {
				return fmt.Errorf("project archive facet %s lacks reviewed deactivation projection", facet.FacetKey)
			}
			continue
		}
		if facet.FacetStatus != projects.ProjectFacetStatusDisabled || !equalProjectArchiveJSON(facet.Metadata, projectArchiveDeactivationMetadata(plan, sourceFacet)) || facet.UpdatedAt.Before(state.StartedAt) {
			return fmt.Errorf("project archive facet %s changed outside reviewed deactivation lifecycle", facet.FacetKey)
		}
		facet.FacetStatus, facet.Metadata, facet.UpdatedAt = planned.FacetStatus, planned.Metadata, planned.UpdatedAt
	}
	for index := range actual.ScriptExposures {
		row, planned := &actual.ScriptExposures[index], want.ScriptExposures[index]
		if err := normalizeProjectArchiveActivatedRow("script", row.ScriptKey, &row.ActivationStatus, projects.ProjectScriptExposureStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "scripts", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	for index := range actual.ScheduleRegistrations {
		row, planned := &actual.ScheduleRegistrations[index], want.ScheduleRegistrations[index]
		if err := normalizeProjectArchiveActivatedRow("schedule", row.ScheduleKey, &row.ActivationStatus, projects.ProjectScheduleRegistrationStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "schedules", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	for index := range actual.DirectEventRegistrations {
		row, planned := &actual.DirectEventRegistrations[index], want.DirectEventRegistrations[index]
		if err := normalizeProjectArchiveActivatedRow("direct event", row.EventKey, &row.ActivationStatus, projects.ProjectDirectEventRegistrationStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "direct_events", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	for index := range actual.WatchedRootRegistrations {
		row, planned := &actual.WatchedRootRegistrations[index], want.WatchedRootRegistrations[index]
		unchanged := reflect.DeepEqual(*row, planned)
		if unchanged {
			if requireDeactivated && row.ActivationStatus != projects.ProjectWatchedRootRegistrationStatusDisabled {
				return fmt.Errorf("project archive watched root %s lacks reviewed deactivation projection", row.LocalRootKey)
			}
			continue
		}
		if row.ActivationStatus != projects.ProjectWatchedRootRegistrationStatusDisabled || row.LastAppliedByActorID == nil || *row.LastAppliedByActorID != plan.Request.ActorID || row.LastAppliedAt == nil || row.LastAppliedAt.Before(state.StartedAt) || row.UpdatedAt.Before(*row.LastAppliedAt) || !equalProjectArchiveJSON(row.Metadata, projectArchiveDeactivationMetadata(plan, "watched_roots")) {
			return fmt.Errorf("project archive watched root %s changed outside reviewed deactivation lifecycle", row.LocalRootKey)
		}
		row.ActivationStatus, row.LastAppliedByActorID, row.LastAppliedAt, row.Metadata, row.UpdatedAt = planned.ActivationStatus, planned.LastAppliedByActorID, planned.LastAppliedAt, planned.Metadata, planned.UpdatedAt
	}
	for index := range actual.ConnectorRegistrations {
		row, planned := &actual.ConnectorRegistrations[index], want.ConnectorRegistrations[index]
		if err := normalizeProjectArchiveActivatedRow("connector", row.ConnectorKey, &row.ActivationStatus, projects.ProjectConnectorRegistrationStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "connectors", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	for index := range actual.ModuleRegistrations {
		row, planned := &actual.ModuleRegistrations[index], want.ModuleRegistrations[index]
		if err := normalizeProjectArchiveActivatedRow("module", row.ModuleKey, &row.ActivationStatus, projects.ProjectModuleRegistrationStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "modules", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	for index := range actual.WorkflowRegistrations {
		row, planned := &actual.WorkflowRegistrations[index], want.WorkflowRegistrations[index]
		if err := normalizeProjectArchiveActivatedRow("workflow", row.WorkflowKey, &row.ActivationStatus, projects.ProjectWorkflowRegistrationStatusDisabled, &row.LastActivatedByActorID, &row.LastActivatedAt, &row.Metadata, &row.UpdatedAt, planned.ActivationStatus, planned.LastActivatedByActorID, planned.LastActivatedAt, planned.Metadata, planned.UpdatedAt, plan, "workflows", state, reflect.DeepEqual(*row, planned), requireDeactivated); err != nil {
			return err
		}
	}
	return nil
}

func normalizeProjectArchiveActivatedRow(kind, key string, status *string, disabled string, actor *string, activatedAt *time.Time, metadata *json.RawMessage, updatedAt *time.Time, plannedStatus, plannedActor string, plannedActivatedAt time.Time, plannedMetadata json.RawMessage, plannedUpdatedAt time.Time, plan ProjectPhysicalArchivePlan, facet string, state projects.ProjectPhysicalArchiveState, unchanged, requireDeactivated bool) error {
	if unchanged {
		if requireDeactivated && *status != disabled {
			return fmt.Errorf("project archive %s %s lacks reviewed deactivation projection", kind, key)
		}
		return nil
	}
	if *status != disabled || *actor != plan.Request.ActorID || activatedAt.IsZero() || activatedAt.Before(state.StartedAt) || updatedAt.Before(*activatedAt) || !equalProjectArchiveJSON(*metadata, projectArchiveDeactivationMetadata(plan, facet)) {
		return fmt.Errorf("project archive %s %s changed outside reviewed deactivation lifecycle", kind, key)
	}
	*status, *actor, *activatedAt, *metadata, *updatedAt = plannedStatus, plannedActor, plannedActivatedAt, plannedMetadata, plannedUpdatedAt
	return nil
}

func normalizeAllowedProjectArchiveRepositoryLifecycle(plan ProjectPhysicalArchivePlan, actual *projects.ProjectRepositoryReadModel, state projects.ProjectPhysicalArchiveState) error {
	want := plan.Repository
	terminal := state.Phase == projects.ProjectArchivePhaseComplete
	actual.Project.UpdatedAt = want.Project.UpdatedAt
	if terminal {
		if actual.Project.Status != "archived" {
			return fmt.Errorf("project archive terminal repository project lifecycle is inconsistent")
		}
		actual.Project.Status = want.Project.Status
	}
	if len(actual.Facets) != len(want.Facets) || len(actual.Members) != len(want.Members) {
		return fmt.Errorf("project archive recovery repository set changed")
	}
	facetSources := projectArchiveFacetDeactivationSources(plan)
	for index := range actual.Facets {
		if _, allowed := facetSources[want.Facets[index].FacetKey]; !allowed {
			continue
		}
		if actual.Facets[index].Status != want.Facets[index].Status {
			if actual.Facets[index].Status != projects.ProjectFacetStatusDisabled {
				return fmt.Errorf("project archive repository facet %s changed outside reviewed deactivation lifecycle", actual.Facets[index].FacetKey)
			}
			actual.Facets[index].Status = want.Facets[index].Status
		}
	}
	if terminal {
		for index := range actual.Members {
			if actual.Members[index].MembershipLifecycle != projects.RepositoryLifecycleArchived {
				return fmt.Errorf("project archive member %s terminal lifecycle is inconsistent", actual.Members[index].RepositoryID)
			}
			actual.Members[index].MembershipLifecycle = want.Members[index].MembershipLifecycle
			if want.Members[index].RepositoryOwnerProjectID == plan.Custody.ProjectID {
				if actual.Members[index].RepositoryLifecycle != projects.RepositoryLifecycleArchived {
					return fmt.Errorf("project archive repository %s terminal lifecycle is inconsistent", actual.Members[index].RepositoryID)
				}
				actual.Members[index].RepositoryLifecycle = want.Members[index].RepositoryLifecycle
			}
		}
	}
	return nil
}

func projectArchiveFacetDeactivationSources(plan ProjectPhysicalArchivePlan) map[string]string {
	result := map[string]string{}
	for _, deactivation := range plan.Deactivations {
		result[deactivation.Input.Facet] = deactivation.Input.Facet
		for _, action := range deactivation.Actions {
			if action.Kind == "project_facet" {
				result[action.Key] = deactivation.Input.Facet
			}
		}
	}
	return result
}

func projectArchiveDeactivationMetadata(plan ProjectPhysicalArchivePlan, facet string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"source": "project.deactivate", "project_id": plan.Custody.ProjectID, "project_slug": plan.Custody.ProjectSlug,
		"facet": facet, "reason": plan.Workspace.Reason,
	})
	return payload
}

func equalProjectArchiveJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func validateAppliedProjectDeactivation(reviewed ProjectArchiveDeactivationPlan, actual projects.ProjectDeactivationResult) error {
	if actual.Facet != reviewed.Input.Facet || actual.DryRun {
		return fmt.Errorf("project archive deactivation %s did not apply the reviewed facet", reviewed.Input.Facet)
	}
	type identity struct{ key, kind, ref, status, metadata string }
	want := make([]identity, 0, len(reviewed.Actions))
	got := make([]identity, 0, len(actual.Actions))
	for _, action := range reviewed.Actions {
		want = append(want, identity{action.Key, action.Kind, action.Ref, action.Status, string(action.Metadata)})
	}
	for _, action := range actual.Actions {
		switch action.Status {
		case "disabled", "already_disabled", "skipped":
		default:
			return fmt.Errorf("project archive deactivation %s left action %s in unsafe status %q", reviewed.Input.Facet, action.Key, action.Status)
		}
		got = append(got, identity{action.Key, action.Kind, action.Ref, action.Status, string(action.Metadata)})
	}
	sort.Slice(want, func(i, j int) bool {
		return want[i].kind+"\x00"+want[i].key+"\x00"+want[i].ref < want[j].kind+"\x00"+want[j].key+"\x00"+want[j].ref
	})
	sort.Slice(got, func(i, j int) bool {
		return got[i].kind+"\x00"+got[i].key+"\x00"+got[i].ref < got[j].kind+"\x00"+got[j].key+"\x00"+got[j].ref
	})
	if len(want) != len(got) {
		return fmt.Errorf("project archive deactivation %s action set changed since review", reviewed.Input.Facet)
	}
	for index := range want {
		if want[index].key != got[index].key || want[index].kind != got[index].kind || want[index].ref != got[index].ref || want[index].metadata != got[index].metadata || !projectArchiveDeactivationStatusMatches(want[index].status, got[index].status) {
			return fmt.Errorf("project archive deactivation %s action set changed since review", reviewed.Input.Facet)
		}
	}
	return nil
}

func projectArchiveDeactivationStatusMatches(reviewed, actual string) bool {
	switch reviewed {
	case "would_disable":
		return actual == "disabled" || actual == "already_disabled"
	case "already_disabled":
		return actual == "already_disabled"
	case "skipped":
		return actual == "skipped"
	default:
		return reviewed == actual
	}
}

func validateProjectRuntimeFullyDeactivated(plan ProjectPhysicalArchivePlan, detail projects.ProjectRegistrationDetail) error {
	for _, deactivation := range plan.Deactivations {
		facet := deactivation.Input.Facet
		switch facet {
		case "scripts":
			for _, item := range detail.ScriptExposures {
				if item.ActivationStatus != projects.ProjectScriptExposureStatusDisabled {
					return fmt.Errorf("archived project retains active script %s", item.ScriptKey)
				}
			}
		case "workflows":
			for _, item := range detail.WorkflowRegistrations {
				if item.ActivationStatus != projects.ProjectWorkflowRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active workflow %s", item.WorkflowKey)
				}
			}
		case "connectors":
			for _, item := range detail.ConnectorRegistrations {
				if item.ActivationStatus != projects.ProjectConnectorRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active connector %s", item.ConnectorKey)
				}
			}
		case "schedules":
			for _, item := range detail.ScheduleRegistrations {
				if item.ActivationStatus != projects.ProjectScheduleRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active schedule %s", item.ScheduleKey)
				}
			}
		case "direct_events":
			for _, item := range detail.DirectEventRegistrations {
				if item.ActivationStatus != projects.ProjectDirectEventRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active direct event %s", item.EventKey)
				}
			}
		case "watched_roots":
			for _, item := range detail.WatchedRootRegistrations {
				if item.ActivationStatus != projects.ProjectWatchedRootRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active watched root %s", item.LocalRootKey)
				}
			}
		case "modules":
			for _, item := range detail.ModuleRegistrations {
				if item.ActivationStatus != projects.ProjectModuleRegistrationStatusDisabled {
					return fmt.Errorf("archived project retains active module %s", item.ModuleKey)
				}
			}
		case "services":
			found := false
			for _, item := range detail.Facets {
				if item.FacetKey == "services" {
					found = true
					if item.FacetStatus != projects.ProjectFacetStatusDisabled {
						return fmt.Errorf("archived project retains active services facet")
					}
				}
			}
			if !found && (!plan.InspectRegisteredServices || detail.Registration == nil || detail.Registration.ContractSchemaVersion != projectcontracts.ProjectSchemaV05) {
				return fmt.Errorf("project archive services facet disappeared during deactivation")
			}
		default:
			return fmt.Errorf("unsupported project archive deactivation facet %q", facet)
		}
	}
	return nil
}

func validateArchivedProjectPhysicalState(plan ProjectPhysicalArchivePlan, detail projects.ProjectRegistrationDetail, repository projects.ProjectRepositoryReadModel, state projects.ProjectPhysicalArchiveState) error {
	if err := projects.ValidateProjectPhysicalArchiveState(state); err != nil {
		return err
	}
	if state.Phase != projects.ProjectArchivePhaseComplete || detail.Project.Project.Status != "archived" || detail.Registration == nil ||
		detail.Registration.RegistrationStatus != projects.ProjectRegistrationStatusArchived || detail.Registration.ActivationStatus != projects.ProjectActivationStatusInactive ||
		repository.Project.Status != "archived" {
		return fmt.Errorf("project physical archive lifecycle projection is incomplete")
	}
	for _, member := range repository.Members {
		if member.MembershipLifecycle != projects.RepositoryLifecycleArchived {
			return fmt.Errorf("project physical archive member %s remains active", member.RepositoryID)
		}
		if member.RepositoryOwnerProjectID == plan.Custody.ProjectID && member.RepositoryLifecycle != projects.RepositoryLifecycleArchived {
			return fmt.Errorf("project physical archive repository %s remains active", member.RepositoryID)
		}
	}
	return validateProjectRuntimeFullyDeactivated(plan, detail)
}

func projectArchiveTransitionInput(plan ProjectPhysicalArchivePlan, state projects.ProjectPhysicalArchiveState) projects.ProjectPhysicalArchiveTransitionInput {
	members := make([]projects.ProjectArchiveRepositoryMemberBinding, 0, len(plan.Repository.Members))
	for _, member := range plan.Repository.Members {
		members = append(members, projects.ProjectArchiveRepositoryMemberBinding{
			RepositoryID: member.RepositoryID, RepositoryOwnerProjectID: member.RepositoryOwnerProjectID, MemberKey: member.Key, Role: member.Role,
		})
	}
	return projects.ProjectPhysicalArchiveTransitionInput{
		State:                            state,
		ExpectedScopeID:                  plan.Custody.ProjectScopeID,
		ExpectedRegistrationID:           plan.Custody.RegistrationID,
		ExpectedRegistrationRevision:     plan.Custody.RegistrationRevision,
		ExpectedRepositorySourceRevision: plan.Custody.RepositorySourceRevision,
		ExpectedMembers:                  members,
	}
}

func projectArchiveFailureResult(base ProjectPhysicalArchiveResult, detail projects.ProjectRegistrationDetail, state projects.ProjectPhysicalArchiveState, workspace *WorkspaceArchiveInspection) ProjectPhysicalArchiveResult {
	base.Project = detail
	base.State = state
	base.Phase = state.Phase
	base.MutationBlocked = state.MutationBlocked
	base.Recoverable = state.Status == projects.ProjectPhysicalArchiveStatusInProgress
	base.Workspace = workspace
	return base
}

func (s ProjectRuntimeService) projectArchiveNowAtLeast(floor time.Time) time.Time {
	now := normalizeWorkspaceTimestamp(s.now())
	floor = normalizeWorkspaceTimestamp(floor)
	if now.Before(floor) {
		return floor
	}
	return now
}

func (s ProjectRuntimeService) failProjectArchive(boundary ProjectPhysicalArchiveFailureBoundary) error {
	if s.ArchiveFailureHook == nil {
		return nil
	}
	return s.ArchiveFailureHook(boundary)
}
