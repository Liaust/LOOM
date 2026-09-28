package storagearchive

import (
	"context"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/projectquiescence"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type ProjectReactivationRequest struct {
	RestoreOperationID string `json:"restore_operation_id"`
	Confirm            bool   `json:"confirm"`
}

type ProjectReactivationResult struct {
	ProjectID          string                            `json:"project_id"`
	RestoreOperationID string                            `json:"restore_operation_id"`
	MutationBlocked    bool                              `json:"mutation_blocked"`
	RuntimeStarted     bool                              `json:"runtime_started"`
	Replay             bool                              `json:"replay"`
	Receipt            projects.ProjectReactivationState `json:"receipt"`
	NextAction         string                            `json:"next_action"`
}

type projectReactivationCoordinator interface {
	AcquireProjectArchiveLocks(context.Context, []string) (func() error, error)
	CompleteProjectReactivation(context.Context, requestctx.Context, projects.ProjectPhysicalArchiveState, string) (projects.ProjectReactivationState, error)
}

type projectFenceReleaser interface {
	ReleaseProjectArchiveRuntimeFences(context.Context, requestctx.Context, ProjectRuntimeQuiescenceRequest) (ProjectRuntimeQuiescenceReceipt, error)
}

func (s ProjectRuntimeService) ReactivateProject(ctx context.Context, req requestctx.Context, ref string, input ProjectReactivationRequest) (ProjectReactivationResult, error) {
	var out ProjectReactivationResult
	if err := s.projectReviewReady(req); err != nil {
		return out, err
	}
	if !input.Confirm || input.RestoreOperationID == "" {
		return out, projectSurfaceError("review_confirmation_required")
	}
	coordinator, ok := s.Projects.(projectReactivationCoordinator)
	if !ok {
		return out, projectSurfaceError("not_ready")
	}
	projectID, scopeID, scopeKey, err := s.ProjectPhysicalScope(ctx, ref)
	if err != nil {
		return out, err
	}
	if req.ScopeID != scopeID || req.ScopeKey != scopeKey {
		return out, projectSurfaceError("caller_mismatch")
	}
	unlock, err := coordinator.AcquireProjectArchiveLocks(ctx, projects.ProjectArchiveLockKeys(projectID, "", input.RestoreOperationID, nil, nil))
	if err != nil {
		return out, err
	}
	defer unlock()
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, projectID)
	if err != nil {
		return out, err
	}
	state, ok := projects.ParseProjectPhysicalArchiveState(detail.Project.Project.ArchiveState)
	if !ok || state.Restore == nil || state.Restore.OperationID != input.RestoreOperationID || state.Restore.Phase != projects.ProjectRestorePhaseComplete {
		return out, projectSurfaceError("restore_not_ready")
	}
	out.ProjectID, out.RestoreOperationID, out.NextAction = projectID, input.RestoreOperationID, "apply_selected_project_resources"
	if state.Restore.Reactivation != nil {
		out.Replay = true
		out.Receipt, err = coordinator.CompleteProjectReactivation(ctx, req, state, state.Restore.Reactivation.ReleaseDigest)
		return out, err
	}
	inspection := s.inspectProjectPhysicalArchive(ctx, detail)
	if inspection == nil || inspection.EvidenceStatus != "verified" {
		return out, projectSurfaceError("evidence_conflict")
	}
	evidence, found, err := s.loadProjectPlan(ctx, projectID, state.OperationID)
	if err != nil {
		return out, err
	}
	var plan ProjectPhysicalArchivePlan
	if !found || evidence.OperationKind != "archive" || evidence.PlanDigest != state.PlanDigest || decodeProjectPrivatePlan(evidence, &plan) != nil || ValidateProjectPhysicalArchivePlanEnvelope(plan, s.WorkspaceRoots) != nil {
		return out, projectSurfaceError("evidence_conflict")
	}
	request, err := projectRuntimeQuiescenceRequest(plan)
	if err != nil {
		return out, err
	}
	request.Release = &projectquiescence.ReleaseBinding{OperationID: state.Restore.OperationID, PlanDigest: state.Restore.PlanDigest}
	releaser, ok := s.RuntimeQuiescence.(projectFenceReleaser)
	if !ok {
		return out, projectSurfaceError("not_ready")
	}
	receipt, err := releaser.ReleaseProjectArchiveRuntimeFences(ctx, req, request)
	if err != nil {
		return out, fmt.Errorf("release project runtime fences: %w", err)
	}
	if err := validateProjectRuntimeQuiescenceReceipt(request, receipt, *state.Restore.RestoredAt); err != nil {
		return out, err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return out, err
	}
	out.Receipt, err = coordinator.CompleteProjectReactivation(ctx, req, state, sha256Hex(raw))
	return out, err
}
