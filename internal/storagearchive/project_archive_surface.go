package storagearchive

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalPlanReviewSchemaVersion = "storage.project_physical_plan_review.v1"

type ProjectPhysicalPlanEvidenceStore interface {
	SaveProjectPlanEvidence(context.Context, projects.ProjectPlanEvidence) error
	LoadProjectPlanEvidence(context.Context, string, string) (projects.ProjectPlanEvidence, bool, error)
}

// Request is a review binding, not authority. Every mutation compares it with
// independently resolved caller context before loading or using private data.
type ProjectPhysicalPlanReview struct {
	SchemaVersion            string                   `json:"schema_version"`
	ProjectID                string                   `json:"project_id"`
	Request                  requestctx.Context       `json:"request"`
	Workspace                WorkspaceMovePlanReview  `json:"workspace"`
	RegistrationRevision     int                      `json:"registration_revision"`
	RepositorySourceRevision int64                    `json:"repository_source_revision"`
	DeactivationFacets       int                      `json:"deactivation_facets"`
	DeactivationActions      int                      `json:"deactivation_actions"`
	ActivationState          WorkspaceActivationState `json:"activation_state"`
	PlanDigest               string                   `json:"plan_digest"`
}

type ProjectPhysicalApplyRequest struct {
	Plan       ProjectPhysicalPlanReview `json:"plan"`
	PlanDigest string                    `json:"plan_digest"`
	Confirm    bool                      `json:"confirm"`
}

type ProjectPhysicalRecoverRequest struct {
	OperationID string `json:"operation_id"`
	PlanDigest  string `json:"plan_digest"`
	Confirm     bool   `json:"confirm"`
}

type ProjectPhysicalMutationSummary struct {
	SchemaVersion   string                   `json:"schema_version"`
	ProjectID       string                   `json:"project_id"`
	OperationID     string                   `json:"operation_id"`
	PlanDigest      string                   `json:"plan_digest"`
	Phase           string                   `json:"phase,omitempty"`
	MutationBlocked bool                     `json:"mutation_blocked"`
	Recoverable     bool                     `json:"recoverable"`
	Replay          bool                     `json:"replay"`
	ActivationState WorkspaceActivationState `json:"activation_state,omitempty"`
}

// Surface errors intentionally omit underlying filesystem/database messages.
type ProjectPhysicalSurfaceError struct {
	Code  string
	cause error
}

func (e ProjectPhysicalSurfaceError) Error() string { return "project physical archive: " + e.Code }
func (e ProjectPhysicalSurfaceError) Unwrap() error { return e.cause }
func projectSurfaceError(code string) error         { return ProjectPhysicalSurfaceError{Code: code} }

// Resolve only canonical target scope; caller identity remains transport-owned.
func (s ProjectRuntimeService) ProjectPhysicalScope(ctx context.Context, ref string) (projectID, scopeID, scopeKey string, err error) {
	if projectSurfaceDependencyNil(s.Projects) {
		return "", "", "", projectSurfaceError("not_ready")
	}
	detail, loadErr := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	p := detail.Project.Project
	if loadErr != nil || p.ProjectID == "" || p.ProjectScopeID == "" || p.ProjectScopeKey == "" {
		return "", "", "", projectSurfaceError("project_unavailable")
	}
	return p.ProjectID, p.ProjectScopeID, p.ProjectScopeKey, nil
}

func ValidateProjectPhysicalPlanReview(review ProjectPhysicalPlanReview) error {
	return validateProjectPhysicalReview(review)
}

func (s ProjectRuntimeService) projectReviewReady(req requestctx.Context) error {
	if projectSurfaceDependencyNil(s.PlanEvidence) || projectSurfaceDependencyNil(s.Projects) || projectSurfaceDependencyNil(s.WorkspaceMove) {
		return projectSurfaceError("not_ready")
	}
	if projects.ValidateProjectPlanRequest(req) != nil {
		return projectSurfaceError("invalid_request")
	}
	return nil
}

func projectSurfaceDependencyNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

func bindProjectReviewRequest(live, original requestctx.Context) (requestctx.Context, error) {
	if projects.ValidateProjectPlanRequest(live) != nil || projects.ValidateProjectPlanRequest(original) != nil {
		return requestctx.Context{}, projectSurfaceError("invalid_request")
	}
	live.CorrelationID = original.CorrelationID
	if live != original {
		return requestctx.Context{}, projectSurfaceError("caller_mismatch")
	}
	return live, nil
}

func (s ProjectRuntimeService) ReviewProjectPhysicalArchive(ctx context.Context, req requestctx.Context, projectRef string, input ProjectPhysicalArchivePlanInput) (ProjectPhysicalPlanReview, error) {
	if err := s.projectReviewReady(req); err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	plan, err := s.PlanProjectPhysicalArchive(ctx, req, projectRef, input)
	if err != nil {
		return ProjectPhysicalPlanReview{}, ProjectPhysicalSurfaceError{Code: "plan_unavailable", cause: err}
	}
	// Make generated choices explicit so apply can reproduce the original plan.
	plan.WorkspaceRequest = projectExplicitWorkspaceRequest(plan.Workspace)
	if err := SealProjectPhysicalArchivePlan(&plan); err != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	if _, err := projectPlanEvidence(plan); err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	return s.projectArchiveReview(plan)
}

func (s ProjectRuntimeService) ReviewProjectPhysicalRestore(ctx context.Context, req requestctx.Context, projectRef string, input ProjectPhysicalRestorePlanInput) (ProjectPhysicalPlanReview, error) {
	if err := s.projectReviewReady(req); err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	archive, err := s.loadCurrentProjectArchivePlan(ctx, projectRef)
	if err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	plan, err := s.PlanProjectPhysicalRestore(ctx, req, archive, input)
	if err != nil {
		return ProjectPhysicalPlanReview{}, ProjectPhysicalSurfaceError{Code: "plan_unavailable", cause: err}
	}
	plan.WorkspaceRequest = projectExplicitWorkspaceRequest(plan.Workspace)
	if err := SealProjectPhysicalRestorePlan(&plan); err != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	if _, err := projectPlanEvidence(plan); err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	return s.projectRestoreReview(plan)
}

func projectExplicitWorkspaceRequest(plan WorkspaceArchivePlan) ProjectArchiveWorkspaceRequest {
	return ProjectArchiveWorkspaceRequest{OperationID: plan.OperationID, Reason: plan.Reason, PlannedAt: plan.PlannedAt}
}

func (s ProjectRuntimeService) projectArchiveReview(plan ProjectPhysicalArchivePlan) (ProjectPhysicalPlanReview, error) {
	if ValidateProjectPhysicalArchivePlanEnvelope(plan, s.WorkspaceRoots) != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	workspace, err := NewWorkspaceMovePlanReview(plan.Workspace)
	if err != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	review := ProjectPhysicalPlanReview{SchemaVersion: ProjectPhysicalPlanReviewSchemaVersion, ProjectID: plan.Custody.ProjectID, Request: plan.Request, Workspace: workspace,
		RegistrationRevision: plan.Custody.RegistrationRevision, RepositorySourceRevision: plan.Custody.RepositorySourceRevision,
		DeactivationFacets: len(plan.Deactivations), ActivationState: WorkspaceActivationInactive, PlanDigest: plan.PlanDigest}
	for _, d := range plan.Deactivations {
		review.DeactivationActions += len(d.Actions)
	}
	return review, validateProjectPhysicalReview(review)
}

func (s ProjectRuntimeService) projectRestoreReview(plan ProjectPhysicalRestorePlan) (ProjectPhysicalPlanReview, error) {
	if ValidateProjectPhysicalRestorePlan(plan, s.WorkspaceRoots) != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	workspace, err := NewWorkspaceMovePlanReview(plan.Workspace)
	if err != nil {
		return ProjectPhysicalPlanReview{}, projectSurfaceError("invalid_plan")
	}
	review := ProjectPhysicalPlanReview{SchemaVersion: ProjectPhysicalPlanReviewSchemaVersion, ProjectID: plan.ArchiveState.ProjectID, Request: plan.Request, Workspace: workspace,
		RegistrationRevision: plan.Registration.Registration.RegistrationRevision, RepositorySourceRevision: plan.ArchivePlan.Custody.RepositorySourceRevision,
		ActivationState: WorkspaceActivationInactive, PlanDigest: plan.PlanDigest}
	return review, validateProjectPhysicalReview(review)
}

func validateProjectPhysicalReview(review ProjectPhysicalPlanReview) error {
	if review.SchemaVersion != ProjectPhysicalPlanReviewSchemaVersion || ValidateWorkspaceMovePlanReview(review.Workspace) != nil ||
		projects.ValidateProjectPlanRequest(review.Request) != nil || review.ProjectID != review.Workspace.ObjectID || review.Workspace.Kind != WorkspaceKindProject ||
		review.Workspace.ActorID != review.Request.ActorID || !sha256DigestPattern.MatchString(review.PlanDigest) ||
		review.RegistrationRevision < 1 || review.RepositorySourceRevision < 0 || review.DeactivationFacets < 0 || review.DeactivationActions < 0 || review.ActivationState != WorkspaceActivationInactive {
		return projectSurfaceError("invalid_review")
	}
	return nil
}

func projectPlanEvidence(plan any) (projects.ProjectPlanEvidence, error) {
	payload, err := json.Marshal(plan)
	if err != nil || len(payload) > projects.MaximumProjectPlanEvidenceBytes {
		return projects.ProjectPlanEvidence{}, projectSurfaceError("evidence_too_large")
	}
	e := projects.ProjectPlanEvidence{Payload: payload, PayloadSHA256: projects.ProjectPlanPayloadDigest(payload)}
	switch p := plan.(type) {
	case ProjectPhysicalArchivePlan:
		e.OperationID, e.ProjectID, e.OperationKind, e.Request, e.PlanDigest = p.Workspace.OperationID, p.Custody.ProjectID, "archive", p.Request, p.PlanDigest
	case ProjectPhysicalRestorePlan:
		e.OperationID, e.ProjectID, e.OperationKind, e.Request, e.PlanDigest = p.Workspace.OperationID, p.ArchiveState.ProjectID, "restore", p.Request, p.PlanDigest
	default:
		return projects.ProjectPlanEvidence{}, projectSurfaceError("invalid_plan")
	}
	if projects.ValidateProjectPlanEvidence(e) != nil {
		return projects.ProjectPlanEvidence{}, projectSurfaceError("invalid_plan")
	}
	return e, nil
}

func decodeProjectPrivatePlan(e projects.ProjectPlanEvidence, target any) error {
	if projects.ValidateProjectPlanEvidence(e) != nil {
		return projectSurfaceError("evidence_conflict")
	}
	decoder := json.NewDecoder(bytes.NewReader(e.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return projectSurfaceError("evidence_conflict")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return projectSurfaceError("evidence_conflict")
	}
	return nil
}

func (s ProjectRuntimeService) loadProjectPlan(ctx context.Context, projectID, operationID string) (projects.ProjectPlanEvidence, bool, error) {
	e, exists, err := s.PlanEvidence.LoadProjectPlanEvidence(ctx, projectID, operationID)
	if err != nil {
		return projects.ProjectPlanEvidence{}, false, projectSurfaceError("evidence_unavailable")
	}
	if exists && (e.ProjectID != projectID || e.OperationID != operationID || projects.ValidateProjectPlanEvidence(e) != nil) {
		return projects.ProjectPlanEvidence{}, false, projectSurfaceError("evidence_conflict")
	}
	return e, exists, nil
}

func (s ProjectRuntimeService) loadCurrentProjectArchivePlan(ctx context.Context, projectRef string) (ProjectPhysicalArchivePlan, error) {
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, projectRef)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, projectSurfaceError("project_unavailable")
	}
	state, ok := projects.ParseProjectPhysicalArchiveState(detail.Project.Project.ArchiveState)
	if !ok || state.ProjectID != detail.Project.Project.ProjectID || state.Phase != projects.ProjectArchivePhaseComplete || state.Restore != nil {
		return ProjectPhysicalArchivePlan{}, projectSurfaceError("restore_not_ready")
	}
	e, exists, err := s.loadProjectPlan(ctx, state.ProjectID, state.OperationID)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	if !exists {
		return ProjectPhysicalArchivePlan{}, projectSurfaceError("evidence_missing")
	}
	var plan ProjectPhysicalArchivePlan
	if e.OperationKind != "archive" || e.PlanDigest != state.PlanDigest || decodeProjectPrivatePlan(e, &plan) != nil || ValidateProjectPhysicalArchivePlanEnvelope(plan, s.WorkspaceRoots) != nil {
		return plan, projectSurfaceError("evidence_conflict")
	}
	return plan, nil
}

func (s ProjectRuntimeService) ApplyReviewedProjectPhysicalPlan(ctx context.Context, live requestctx.Context, projectRef string, input ProjectPhysicalApplyRequest) (ProjectPhysicalMutationSummary, error) {
	if err := s.projectReviewReady(live); err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	review := input.Plan
	if !input.Confirm || input.PlanDigest != review.PlanDigest || validateProjectPhysicalReview(review) != nil {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_confirmation_required")
	}
	req, err := bindProjectReviewRequest(live, review.Request)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if err := s.requireProjectReviewRef(ctx, projectRef, review.ProjectID); err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	e, exists, err := s.loadProjectPlan(ctx, review.ProjectID, review.Workspace.OperationID)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if !exists {
		var plan any
		if review.Workspace.OperationKind == WorkspaceOperationArchive {
			plan, err = s.PlanProjectPhysicalArchive(ctx, req, review.ProjectID, ProjectPhysicalArchivePlanInput{OperationID: review.Workspace.OperationID, Reason: review.Workspace.Reason, PlannedAt: review.Workspace.PlannedAt})
		} else {
			var archive ProjectPhysicalArchivePlan
			archive, err = s.loadCurrentProjectArchivePlan(ctx, review.ProjectID)
			if err == nil {
				plan, err = s.PlanProjectPhysicalRestore(ctx, req, archive, ProjectPhysicalRestorePlanInput{OperationID: review.Workspace.OperationID, Reason: review.Workspace.Reason, PlannedAt: review.Workspace.PlannedAt})
			}
		}
		if err != nil {
			return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_stale")
		}
		e, err = projectPlanEvidence(plan)
		if err != nil {
			return ProjectPhysicalMutationSummary{}, err
		}
	}
	canonical, err := s.reviewPrivateEvidence(e)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if !reflect.DeepEqual(canonical, review) {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_mismatch")
	}
	if !exists {
		if err := s.PlanEvidence.SaveProjectPlanEvidence(ctx, e); err != nil {
			return ProjectPhysicalMutationSummary{}, projectSurfaceError("evidence_unavailable")
		}
		// Read back before any lifecycle mutation, including after an exact replay.
		stored, found, err := s.loadProjectPlan(ctx, e.ProjectID, e.OperationID)
		if err != nil || !found || !reflect.DeepEqual(stored, e) {
			return ProjectPhysicalMutationSummary{}, projectSurfaceError("evidence_conflict")
		}
	}
	return s.executePrivateProjectPlan(ctx, req, e, false)
}

func (s ProjectRuntimeService) RecoverReviewedProjectPhysicalPlan(ctx context.Context, live requestctx.Context, projectRef string, input ProjectPhysicalRecoverRequest) (ProjectPhysicalMutationSummary, error) {
	return s.recoverReviewedProjectPhysicalPlan(ctx, live, projectRef, input, "")
}

// The HTTP route supplies kind independently of submitted or persisted input.
func (s ProjectRuntimeService) RecoverReviewedProjectPhysicalKind(ctx context.Context, live requestctx.Context, projectRef string, input ProjectPhysicalRecoverRequest, kind WorkspaceOperationKind) (ProjectPhysicalMutationSummary, error) {
	if kind != WorkspaceOperationArchive && kind != WorkspaceOperationRestore {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("invalid_request")
	}
	return s.recoverReviewedProjectPhysicalPlan(ctx, live, projectRef, input, kind)
}

func (s ProjectRuntimeService) recoverReviewedProjectPhysicalPlan(ctx context.Context, live requestctx.Context, projectRef string, input ProjectPhysicalRecoverRequest, kind WorkspaceOperationKind) (ProjectPhysicalMutationSummary, error) {
	if err := s.projectReviewReady(live); err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if !input.Confirm || !workspacePathID(input.OperationID, workspaceOperationID) || !sha256DigestPattern.MatchString(input.PlanDigest) {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_confirmation_required")
	}
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, projectRef)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("project_unavailable")
	}
	e, exists, err := s.loadProjectPlan(ctx, detail.Project.Project.ProjectID, input.OperationID)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if !exists {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("evidence_missing")
	}
	if kind != "" && e.OperationKind != string(kind) {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_mismatch")
	}
	req, err := bindProjectReviewRequest(live, e.Request)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if e.PlanDigest != input.PlanDigest {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("review_mismatch")
	}
	return s.executePrivateProjectPlan(ctx, req, e, true)
}

func (s ProjectRuntimeService) requireProjectReviewRef(ctx context.Context, ref, projectID string) error {
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, ref)
	if err != nil || detail.Project.Project.ProjectID != projectID {
		return projectSurfaceError("project_mismatch")
	}
	return nil
}

func (s ProjectRuntimeService) reviewPrivateEvidence(e projects.ProjectPlanEvidence) (ProjectPhysicalPlanReview, error) {
	if e.OperationKind == "archive" {
		var plan ProjectPhysicalArchivePlan
		if err := decodeProjectPrivatePlan(e, &plan); err != nil {
			return ProjectPhysicalPlanReview{}, err
		}
		return s.projectArchiveReview(plan)
	}
	var plan ProjectPhysicalRestorePlan
	if err := decodeProjectPrivatePlan(e, &plan); err != nil {
		return ProjectPhysicalPlanReview{}, err
	}
	return s.projectRestoreReview(plan)
}

func (s ProjectRuntimeService) executePrivateProjectPlan(ctx context.Context, req requestctx.Context, e projects.ProjectPlanEvidence, recoverOnly bool) (ProjectPhysicalMutationSummary, error) {
	canonical, err := s.reviewPrivateEvidence(e)
	if err != nil {
		return ProjectPhysicalMutationSummary{}, err
	}
	if canonical.Request != req {
		return ProjectPhysicalMutationSummary{}, projectSurfaceError("caller_mismatch")
	}
	result := ProjectPhysicalMutationSummary{SchemaVersion: "storage.project_physical_mutation.v1", ProjectID: e.ProjectID, OperationID: e.OperationID, PlanDigest: e.PlanDigest}
	if e.OperationKind == "archive" {
		var plan ProjectPhysicalArchivePlan
		if err := decodeProjectPrivatePlan(e, &plan); err != nil {
			return result, err
		}
		plan, err = normalizeRestoreArchivePlan(plan)
		if err != nil {
			return result, projectSurfaceError("evidence_conflict")
		}
		var outcome ProjectPhysicalArchiveResult
		if recoverOnly {
			outcome, err = s.RecoverProjectPhysicalArchive(ctx, plan, e.PlanDigest)
		} else {
			outcome, err = s.ApplyProjectPhysicalArchive(ctx, plan, e.PlanDigest)
		}
		result.Phase, result.MutationBlocked, result.Recoverable, result.Replay = string(outcome.Phase), outcome.MutationBlocked, outcome.Recoverable, outcome.Replay
		if outcome.State.RuntimeDeactivatedAt != nil {
			result.ActivationState = WorkspaceActivationInactive
		}
	} else {
		var plan ProjectPhysicalRestorePlan
		if err := decodeProjectPrivatePlan(e, &plan); err != nil {
			return result, err
		}
		var outcome ProjectPhysicalRestoreResult
		if recoverOnly {
			outcome, err = s.RecoverProjectPhysicalRestore(ctx, req, plan, e.PlanDigest)
		} else {
			outcome, err = s.ApplyProjectPhysicalRestore(ctx, req, plan, e.PlanDigest)
		}
		result.MutationBlocked, result.Recoverable, result.Replay, result.ActivationState = outcome.MutationBlocked, outcome.Recoverable, outcome.Replay, outcome.ActivationState
		if outcome.State != nil {
			result.Phase = string(outcome.State.Phase)
		}
	}
	if err != nil {
		return result, projectSurfaceError("operation_failed")
	}
	return result, nil
}

// Keep compile-time wiring explicit without adding another persistence owner.
var _ ProjectPhysicalPlanEvidenceStore = projects.Service{}
