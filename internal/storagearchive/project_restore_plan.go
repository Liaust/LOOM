package storagearchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalRestorePlanSchemaVersion = "storage.project_physical_restore_plan.v1"

// ProjectWorkspaceRestorePlanner deliberately exposes no mutation or recovery
// methods. The generic kernel authenticates custody; this adapter binds it to
// the completed project lifecycle and the new restore request.
type ProjectWorkspaceRestorePlanner interface {
	PlanRestore(context.Context, WorkspaceRestorePlanInput) (WorkspaceArchivePlan, error)
	InspectArchive(context.Context, string) (WorkspaceArchiveInspection, error)
}

type ProjectPhysicalRestorePlanInput struct {
	OperationID string    `json:"operation_id,omitempty"`
	Reason      string    `json:"reason"`
	PlannedAt   time.Time `json:"planned_at,omitempty"`
}

type ProjectPhysicalRestorePlan struct {
	SchemaVersion    string                               `json:"schema_version"`
	Request          requestctx.Context                   `json:"request"`
	ArchivePlan      ProjectPhysicalArchivePlan           `json:"archive_plan"`
	ArchiveState     projects.ProjectPhysicalArchiveState `json:"archive_state"`
	Archive          WorkspaceArchiveInspection           `json:"archive"`
	Registration     projects.ProjectRegistrationDetail   `json:"registration"`
	Repository       projects.ProjectRepositoryReadModel  `json:"repository"`
	RepositoryRoot   string                               `json:"repository_root,omitempty"`
	WorkspaceRequest ProjectArchiveWorkspaceRequest       `json:"workspace_request"`
	Workspace        WorkspaceArchivePlan                 `json:"workspace"`
	ActivationState  WorkspaceActivationState             `json:"activation_state"`
	PlanDigest       string                               `json:"plan_digest"`
}

// PlanProjectPhysicalRestore requires the preserved project archive plan:
// current project state stores its digest, not a reconstructible copy. It must
// not re-plan archive against the now-absent active source or infer new custody.
func (s ProjectRuntimeService) PlanProjectPhysicalRestore(ctx context.Context, req requestctx.Context, archivePlan ProjectPhysicalArchivePlan, input ProjectPhysicalRestorePlanInput) (ProjectPhysicalRestorePlan, error) {
	if s.Projects == nil {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("project restore projects service is not configured")
	}
	planner, ok := s.WorkspaceMove.(ProjectWorkspaceRestorePlanner)
	if !ok || planner == nil {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("project restore workspace planner is not configured")
	}
	repositories := s.RepositoryState
	if repositories == nil {
		repositories, _ = s.Projects.(ProjectRuntimeRepositoryStateService)
	}
	if repositories == nil {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("project restore repository service is not configured")
	}
	if err := validateProjectRestoreRequest(req); err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}
	archivePlan, err := normalizeRestoreArchivePlan(archivePlan)
	if err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}
	if err := ValidateProjectPhysicalArchivePlanEnvelope(archivePlan, s.WorkspaceRoots); err != nil {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("project restore requires the exact preserved archive plan: %w", err)
	}
	detail, repository, archive, state, err := s.readProjectRestoreEvidence(ctx, repositories, planner, archivePlan)
	if err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "project restore"
	}
	workspace, err := planner.PlanRestore(ctx, WorkspaceRestorePlanInput{
		OperationID: input.OperationID, ArchiveOperationID: state.OperationID,
		ActorID: req.ActorID, Reason: reason, PlannedAt: input.PlannedAt,
	})
	if err != nil {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("plan project workspace restore: %w", err)
	}
	workspaceRequest := ProjectArchiveWorkspaceRequest{OperationID: input.OperationID, Reason: reason}
	if !input.PlannedAt.IsZero() {
		workspaceRequest.PlannedAt = normalizeWorkspaceTimestamp(input.PlannedAt)
	}
	plan := ProjectPhysicalRestorePlan{
		SchemaVersion: ProjectPhysicalRestorePlanSchemaVersion, Request: req,
		ArchivePlan: archivePlan, ArchiveState: state, Archive: archive,
		Registration: detail, Repository: repository,
		WorkspaceRequest: workspaceRequest, Workspace: workspace,
		ActivationState: WorkspaceActivationInactive,
	}
	if repository.Source != nil {
		plan.RepositoryRoot = repository.Source.ProjectRoot
	}
	if err := SealProjectPhysicalRestorePlan(&plan); err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}
	if err := ValidateProjectPhysicalRestorePlan(plan, s.WorkspaceRoots); err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}

	// Planning is not a lease. Re-read independent stores after the potentially
	// lengthy generic inventory, rejecting any observed lifecycle/source drift.
	finalDetail, finalRepository, finalArchive, finalState, err := s.readProjectRestoreEvidence(ctx, repositories, planner, archivePlan)
	if err != nil {
		return ProjectPhysicalRestorePlan{}, err
	}
	if !reflect.DeepEqual(detail, finalDetail) || !reflect.DeepEqual(repository, finalRepository) ||
		!reflect.DeepEqual(archive, finalArchive) || !reflect.DeepEqual(state, finalState) {
		return ProjectPhysicalRestorePlan{}, fmt.Errorf("project restore evidence changed while planning")
	}
	return plan, nil
}

func (s ProjectRuntimeService) readProjectRestoreEvidence(ctx context.Context, repositories ProjectRuntimeRepositoryStateService, planner ProjectWorkspaceRestorePlanner, archivePlan ProjectPhysicalArchivePlan) (projects.ProjectRegistrationDetail, projects.ProjectRepositoryReadModel, WorkspaceArchiveInspection, projects.ProjectPhysicalArchiveState, error) {
	var detail projects.ProjectRegistrationDetail
	var repository projects.ProjectRepositoryReadModel
	var archive WorkspaceArchiveInspection
	var state projects.ProjectPhysicalArchiveState
	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, archivePlan.Custody.ProjectID)
	if err == nil {
		detail, err = canonicalProjectRegistrationDetail(detail)
	}
	if err == nil {
		repository, err = repositories.ReadProjectRepositoryState(ctx, archivePlan.Custody.ProjectID)
	}
	if err == nil {
		repository, err = canonicalProjectRepositoryState(repository)
	}
	if err == nil {
		var present bool
		state, present, err = decodeExactProjectPhysicalArchiveState(detail.Project.Project.ArchiveState)
		if err == nil && (!present || state.Phase != projects.ProjectArchivePhaseComplete) {
			err = fmt.Errorf("project restore requires completed physical archive state")
		}
	}
	if err == nil {
		archive, err = planner.InspectArchive(ctx, state.OperationID)
	}
	if err == nil {
		err = validateProjectRestoreArchiveEvidence(archivePlan, state, detail, repository, archive)
	}
	return detail, repository, archive, state, err
}

func validateProjectRestoreArchiveEvidence(archivePlan ProjectPhysicalArchivePlan, state projects.ProjectPhysicalArchiveState, detail projects.ProjectRegistrationDetail, repository projects.ProjectRepositoryReadModel, archive WorkspaceArchiveInspection) error {
	if state.Restore != nil {
		return fmt.Errorf("project archive already has restore intent; recover its exact reviewed plan")
	}
	if err := validateProjectPhysicalArchiveStateAgainstPlan(archivePlan, state, archive, true); err != nil {
		return err
	}
	if err := validateCurrentProjectArchiveSnapshots(archivePlan, detail, repository, &state); err != nil {
		return err
	}
	return validateArchivedProjectPhysicalState(archivePlan, detail, repository, state)
}

func ValidateProjectPhysicalRestorePlan(plan ProjectPhysicalRestorePlan, roots TrustedWorkspaceRoots) error {
	if plan.SchemaVersion != ProjectPhysicalRestorePlanSchemaVersion || plan.ActivationState != WorkspaceActivationInactive {
		return fmt.Errorf("project restore plan must use the supported schema and inactive runtime posture")
	}
	if err := validateProjectRestoreRequest(plan.Request); err != nil {
		return err
	}
	archivePlan, err := normalizeRestoreArchivePlan(plan.ArchivePlan)
	if err != nil {
		return err
	}
	if err := ValidateProjectPhysicalArchivePlanEnvelope(archivePlan, roots); err != nil {
		return err
	}
	canonicalDetail, err := canonicalProjectRegistrationDetail(plan.Registration)
	if err != nil || !reflect.DeepEqual(canonicalDetail, plan.Registration) {
		return fmt.Errorf("project restore registration snapshot is not canonical")
	}
	repository, err := canonicalProjectRepositoryState(plan.Repository)
	if err != nil || !reflect.DeepEqual(repository, plan.Repository) {
		return fmt.Errorf("project restore repository snapshot is not canonical")
	}
	if repository.Source == nil {
		if plan.RepositoryRoot != "" {
			return fmt.Errorf("project restore source root has no repository source")
		}
	} else {
		if plan.RepositoryRoot != archivePlan.Custody.RepositoryProjectRoot ||
			(repository.Source.ProjectRoot != "" && repository.Source.ProjectRoot != plan.RepositoryRoot) {
			return fmt.Errorf("project restore repository root contradicts archive custody")
		}
		repository.Source.ProjectRoot = plan.RepositoryRoot
	}
	if err := validateProjectRestoreArchiveEvidence(archivePlan, plan.ArchiveState, plan.Registration, repository, plan.Archive); err != nil {
		return err
	}
	workspace := plan.Workspace
	if err := ValidateWorkspaceArchivePlan(workspace, roots); err != nil {
		return err
	}
	if workspace.OperationKind != WorkspaceOperationRestore || workspace.Kind != WorkspaceKindProject ||
		workspace.OperationID == plan.ArchiveState.OperationID || workspace.ObjectID != archivePlan.Custody.ProjectID ||
		workspace.Slug != archivePlan.Custody.ProjectSlug || workspace.ArchiveOperationID != plan.ArchiveState.OperationID ||
		workspace.ArchiveManifestDigest != plan.ArchiveState.ArchiveManifestDigest || workspace.ActorID != plan.Request.ActorID ||
		workspace.Source.Path != archivePlan.Workspace.Destination.Path || workspace.Destination.Path != archivePlan.Workspace.Source.Path ||
		!SamePathIdentity(workspace.Source.Identity, archivePlan.Workspace.Source.Identity) ||
		!reflect.DeepEqual(workspace.Inventory, archivePlan.Workspace.Inventory) {
		return fmt.Errorf("project restore workspace plan contradicts archived identity or custody")
	}
	if plan.ArchiveState.ArchivedAt == nil || workspace.PlannedAt.Before(*plan.ArchiveState.ArchivedAt) {
		return fmt.Errorf("project restore plan precedes project archive completion")
	}
	if err := validateProjectArchiveWorkspaceRequest(plan.WorkspaceRequest, workspace); err != nil {
		return err
	}
	digest, err := ProjectPhysicalRestorePlanDigest(plan)
	if err != nil {
		return err
	}
	if plan.PlanDigest != digest {
		return fmt.Errorf("project physical restore plan_digest mismatch")
	}
	return nil
}

func validateProjectRestoreRequest(req requestctx.Context) error {
	if req.ActorID == "" || req.ActorID != strings.TrimSpace(req.ActorID) ||
		req.OriginNodeID == "" || req.OriginNodeID != strings.TrimSpace(req.OriginNodeID) ||
		req.CorrelationID == "" || req.CorrelationID != strings.TrimSpace(req.CorrelationID) {
		return fmt.Errorf("project restore requires an exact actor, origin node and correlation identity")
	}
	return nil
}

func normalizeRestoreArchivePlan(plan ProjectPhysicalArchivePlan) (ProjectPhysicalArchivePlan, error) {
	root := ""
	if plan.Repository.Source != nil {
		root = plan.Repository.Source.ProjectRoot
	}
	cloned, err := cloneProjectArchiveJSON(plan)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	if cloned.Repository.Source != nil {
		if root != "" && root != plan.Custody.RepositoryProjectRoot {
			return ProjectPhysicalArchivePlan{}, fmt.Errorf("preserved archive repository root contradicts custody")
		}
		cloned.Repository.Source.ProjectRoot = plan.Custody.RepositoryProjectRoot
	}
	return cloned, nil
}

func ProjectPhysicalRestorePlanDigest(plan ProjectPhysicalRestorePlan) (string, error) {
	plan.PlanDigest = ""
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode project physical restore plan: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func SealProjectPhysicalRestorePlan(plan *ProjectPhysicalRestorePlan) error {
	if plan == nil {
		return fmt.Errorf("project physical restore plan is required")
	}
	digest, err := ProjectPhysicalRestorePlanDigest(*plan)
	if err == nil {
		plan.PlanDigest = digest
	}
	return err
}
