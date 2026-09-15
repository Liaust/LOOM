package storagearchive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

const ProjectPhysicalArchivePlanSchemaVersion = "storage.project_physical_archive_plan.v1"

type ProjectRuntimeRepositoryStateService interface {
	ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error)
}

type ProjectWorkspaceArchivePlanner interface {
	PlanArchive(context.Context, WorkspaceArchivePlanInput) (WorkspaceArchivePlan, error)
}

type ProjectPhysicalArchivePlanInput struct {
	OperationID string    `json:"operation_id,omitempty"`
	Reason      string    `json:"reason"`
	PlannedAt   time.Time `json:"planned_at,omitempty"`
}

// ProjectArchiveWorkspaceRequest preserves which generic-plan values were
// supplied by the caller. Empty operation ID and planned time deliberately
// mean that the generic planner may generate them.
type ProjectArchiveWorkspaceRequest struct {
	OperationID string    `json:"operation_id,omitempty"`
	Reason      string    `json:"reason"`
	PlannedAt   time.Time `json:"planned_at,omitempty"`
}

type ProjectArchiveDeactivationPlan struct {
	Input    projects.DeactivateProjectInput      `json:"input"`
	Changed  bool                                 `json:"changed"`
	Actions  []projects.ProjectDeactivationAction `json:"actions"`
	Warnings []string                             `json:"warnings,omitempty"`
}

// ProjectPhysicalArchivePlan binds the complete project identity, repository
// and runtime snapshot, reviewed dry-run deactivations, and the sole generic
// filesystem-move plan. Building or validating this value performs no mutation.
type ProjectPhysicalArchivePlan struct {
	InspectRegisteredServices bool                                  `json:"inspect_registered_services,omitempty"`
	SchemaVersion             string                                `json:"schema_version"`
	Request                   requestctx.Context                    `json:"request"`
	WorkspaceRequest          ProjectArchiveWorkspaceRequest        `json:"workspace_request"`
	Custody                   projects.ProjectArchiveCustodyBinding `json:"custody"`
	Registration              projects.ProjectRegistrationDetail    `json:"registration"`
	Repository                projects.ProjectRepositoryReadModel   `json:"repository"`
	Deactivations             []ProjectArchiveDeactivationPlan      `json:"deactivations"`
	Workspace                 WorkspaceArchivePlan                  `json:"workspace"`
	PlanDigest                string                                `json:"plan_digest"`
}

func (s ProjectRuntimeService) PlanProjectPhysicalArchive(ctx context.Context, req requestctx.Context, projectRef string, input ProjectPhysicalArchivePlanInput) (ProjectPhysicalArchivePlan, error) {
	if s.Projects == nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project archive projects service is not configured")
	}
	if s.Activation == nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project archive activation service is not configured")
	}
	archiveActivation, ok := s.Activation.(ProjectPhysicalArchiveActivationService)
	if !ok {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project archive activation service does not support physical archive deactivation")
	}
	repositoryService := s.RepositoryState
	if repositoryService == nil {
		if inferred, ok := s.Projects.(ProjectRuntimeRepositoryStateService); ok {
			repositoryService = inferred
		}
	}
	if repositoryService == nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project archive repository-state service is not configured")
	}
	if s.WorkspaceMove == nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project archive workspace-move planner is not configured")
	}

	detail, err := s.Projects.GetProjectRegistrationStatus(ctx, strings.TrimSpace(projectRef))
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	repository, err := repositoryService.ReadProjectRepositoryState(ctx, detail.Project.Project.ProjectID)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	paths, err := ResolveWorkspacePaths(s.WorkspaceRoots, WorkspaceKindProject, detail.Project.Project.Slug)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	custody, err := projects.BuildProjectArchiveCustodyBinding(detail, repository, paths.Active.AbsolutePath)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}

	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = "project archive"
	}
	workspace, err := s.WorkspaceMove.PlanArchive(ctx, WorkspaceArchivePlanInput{
		OperationID: input.OperationID,
		Kind:        WorkspaceKindProject,
		ObjectID:    custody.ProjectID,
		Slug:        custody.ProjectSlug,
		ActorID:     strings.TrimSpace(req.ActorID),
		Reason:      reason,
		PlannedAt:   input.PlannedAt,
	})
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	workspaceRequest := ProjectArchiveWorkspaceRequest{OperationID: input.OperationID, Reason: reason}
	if !input.PlannedAt.IsZero() {
		workspaceRequest.PlannedAt = normalizeWorkspaceTimestamp(input.PlannedAt)
	}
	if err := validateProjectArchiveWorkspaceRequest(workspaceRequest, workspace); err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	if err := validateProjectArchiveContractInventory(custody, workspace); err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}

	canonicalDetail, err := canonicalProjectRegistrationDetail(detail)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	canonicalRepository, err := canonicalProjectRepositoryState(repository)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	deactivations := make([]ProjectArchiveDeactivationPlan, 0)
	inspectServices := canonicalDetail.Registration != nil && canonicalDetail.Registration.ContractSchemaVersion == projectcontracts.ProjectSchemaV05
	for _, facet := range physicalArchiveDeactivationFacets(canonicalDetail, inspectServices) {
		deactivationInput := projects.DeactivateProjectInput{
			Facet:       facet,
			ProjectRoot: custody.RegisteredRoot,
			Reason:      reason,
			DryRun:      true,
		}
		result, err := archiveActivation.DeactivateForPhysicalArchive(ctx, req, custody.ProjectID, deactivationInput)
		if err != nil {
			return ProjectPhysicalArchivePlan{}, fmt.Errorf("plan project deactivation %s: %w", facet, err)
		}
		resultDetail, err := canonicalProjectRegistrationDetail(result.Detail)
		if err != nil {
			return ProjectPhysicalArchivePlan{}, fmt.Errorf("canonicalize project deactivation %s: %w", facet, err)
		}
		if !result.DryRun || result.Facet != facet || !reflect.DeepEqual(resultDetail, canonicalDetail) {
			return ProjectPhysicalArchivePlan{}, fmt.Errorf("project deactivation %s was not an exact dry-run of the bound registration", facet)
		}
		actions := append([]projects.ProjectDeactivationAction(nil), result.Actions...)
		sort.Slice(actions, func(i, j int) bool {
			return projectDeactivationActionKey(actions[i]) < projectDeactivationActionKey(actions[j])
		})
		warnings := append([]string(nil), result.Warnings...)
		sort.Strings(warnings)
		deactivations = append(deactivations, ProjectArchiveDeactivationPlan{
			Input: deactivationInput, Changed: result.Changed, Actions: actions, Warnings: warnings,
		})
	}
	finalDetail, err := s.Projects.GetProjectRegistrationStatus(ctx, custody.ProjectID)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("re-read project archive registration: %w", err)
	}
	finalCanonicalDetail, err := canonicalProjectRegistrationDetail(finalDetail)
	if err != nil || !reflect.DeepEqual(finalCanonicalDetail, canonicalDetail) {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project registration or runtime state changed while planning archive")
	}
	finalRepository, err := repositoryService.ReadProjectRepositoryState(ctx, custody.ProjectID)
	if err != nil {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("re-read project archive repository state: %w", err)
	}
	finalCanonicalRepository, err := canonicalProjectRepositoryState(finalRepository)
	if err != nil || !reflect.DeepEqual(finalCanonicalRepository, canonicalRepository) {
		return ProjectPhysicalArchivePlan{}, fmt.Errorf("project repository state changed while planning archive")
	}

	plan := ProjectPhysicalArchivePlan{
		InspectRegisteredServices: inspectServices,
		SchemaVersion:             ProjectPhysicalArchivePlanSchemaVersion,
		Request:                   req,
		WorkspaceRequest:          workspaceRequest,
		Custody:                   custody,
		Registration:              canonicalDetail,
		Repository:                canonicalRepository,
		Deactivations:             deactivations,
		Workspace:                 workspace,
	}
	if err := SealProjectPhysicalArchivePlan(&plan); err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	if err := ValidateProjectPhysicalArchivePlan(plan, s.WorkspaceRoots); err != nil {
		return ProjectPhysicalArchivePlan{}, err
	}
	return plan, nil
}

// Preserve historical plan encoding/validation while new declaration plans
// inspect registered services even without a legacy services facet.
func physicalArchiveDeactivationFacets(detail projects.ProjectRegistrationDetail, inspectServices bool) []string {
	facets := projectArchiveDeactivationFacets(detail)
	if inspectServices {
		for _, facet := range facets {
			if facet == "services" {
				return facets
			}
		}
		facets = append(facets, "services")
	}
	return facets
}

func ValidateProjectPhysicalArchivePlanEnvelope(plan ProjectPhysicalArchivePlan, roots TrustedWorkspaceRoots) error {
	if plan.SchemaVersion != ProjectPhysicalArchivePlanSchemaVersion {
		return fmt.Errorf("unsupported project physical archive plan schema %q", plan.SchemaVersion)
	}
	if err := ValidateWorkspaceArchivePlan(plan.Workspace, roots); err != nil {
		return fmt.Errorf("workspace archive plan: %w", err)
	}
	if plan.Workspace.OperationKind != WorkspaceOperationArchive || plan.Workspace.Kind != WorkspaceKindProject ||
		plan.Workspace.ObjectID != plan.Custody.ProjectID || plan.Workspace.Slug != plan.Custody.ProjectSlug ||
		plan.Workspace.Source.Path.AbsolutePath != plan.Custody.CanonicalRoot || plan.Workspace.ActorID != strings.TrimSpace(plan.Request.ActorID) {
		return fmt.Errorf("project archive workspace plan contradicts custody binding")
	}
	if err := validateProjectArchiveWorkspaceRequest(plan.WorkspaceRequest, plan.Workspace); err != nil {
		return err
	}
	if err := validateProjectArchiveContractInventory(plan.Custody, plan.Workspace); err != nil {
		return err
	}
	canonicalDetail, err := canonicalProjectRegistrationDetail(plan.Registration)
	if err != nil || !reflect.DeepEqual(canonicalDetail, plan.Registration) {
		return fmt.Errorf("project archive registration snapshot is not canonical")
	}
	canonicalRepository, err := canonicalProjectRepositoryState(plan.Repository)
	if err != nil || !reflect.DeepEqual(canonicalRepository, plan.Repository) {
		return fmt.Errorf("project archive repository snapshot is not canonical")
	}
	expectedFacets := physicalArchiveDeactivationFacets(plan.Registration, plan.InspectRegisteredServices)
	if len(expectedFacets) != len(plan.Deactivations) {
		return fmt.Errorf("project archive deactivation plan is incomplete")
	}
	for index, facet := range expectedFacets {
		deactivation := plan.Deactivations[index]
		if deactivation.Input.Facet != facet || deactivation.Input.ProjectRoot != plan.Custody.RegisteredRoot ||
			deactivation.Input.Reason != plan.Workspace.Reason || !deactivation.Input.DryRun {
			return fmt.Errorf("project archive deactivation %d does not match bound input", index)
		}
		if !sort.SliceIsSorted(deactivation.Actions, func(i, j int) bool {
			return projectDeactivationActionKey(deactivation.Actions[i]) < projectDeactivationActionKey(deactivation.Actions[j])
		}) || !sort.StringsAreSorted(deactivation.Warnings) {
			return fmt.Errorf("project archive deactivation %s is not canonical", facet)
		}
		previousAction := ""
		for _, action := range deactivation.Actions {
			if strings.TrimSpace(action.Key) == "" || strings.TrimSpace(action.Kind) == "" || strings.TrimSpace(action.Status) == "" || strings.TrimSpace(action.Summary) == "" {
				return fmt.Errorf("project archive deactivation %s contains an incomplete action", facet)
			}
			key := projectDeactivationActionKey(action)
			if previousAction != "" && key <= previousAction {
				return fmt.Errorf("project archive deactivation %s contains duplicate actions", facet)
			}
			previousAction = key
		}
	}
	if _, err := projectRuntimeQuiescenceRequest(plan); err != nil {
		return fmt.Errorf("project archive runtime quiescence plan: %w", err)
	}
	if _, err := projectRuntimeQuiescenceRoutingContext(plan); err != nil {
		return err
	}
	digest, err := ProjectPhysicalArchivePlanDigest(plan)
	if err != nil {
		return err
	}
	if plan.PlanDigest != digest {
		return fmt.Errorf("project physical archive plan_digest mismatch")
	}
	return nil
}

func ValidateProjectPhysicalArchivePlan(plan ProjectPhysicalArchivePlan, roots TrustedWorkspaceRoots) error {
	if err := ValidateProjectPhysicalArchivePlanEnvelope(plan, roots); err != nil {
		return err
	}
	repositoryForGuard := plan.Repository
	if repositoryForGuard.Source != nil {
		source := *repositoryForGuard.Source
		repositoryForGuard.Source = &source
		if repositoryForGuard.Source.ProjectRoot != "" && filepath.Clean(repositoryForGuard.Source.ProjectRoot) != plan.Custody.RepositoryProjectRoot {
			return fmt.Errorf("project archive repository root contradicts custody binding")
		}
		repositoryForGuard.Source.ProjectRoot = plan.Custody.RepositoryProjectRoot
	}
	custody, err := projects.BuildProjectArchiveCustodyBinding(plan.Registration, repositoryForGuard, plan.Workspace.Source.Path.AbsolutePath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(custody, plan.Custody) {
		return fmt.Errorf("project archive custody binding does not match registration snapshot")
	}
	return nil
}

func validateProjectArchiveWorkspaceRequest(request ProjectArchiveWorkspaceRequest, workspace WorkspaceArchivePlan) error {
	if request.Reason == "" || workspace.Reason != request.Reason {
		return fmt.Errorf("project archive workspace plan does not preserve requested reason")
	}
	if request.OperationID != "" && workspace.OperationID != request.OperationID {
		return fmt.Errorf("project archive workspace plan does not preserve requested operation_id")
	}
	if !request.PlannedAt.IsZero() && !workspace.PlannedAt.Equal(request.PlannedAt) {
		return fmt.Errorf("project archive workspace plan does not preserve requested planned_at")
	}
	return nil
}

func validateProjectArchiveContractInventory(custody projects.ProjectArchiveCustodyBinding, workspace WorkspaceArchivePlan) error {
	if custody.ContractRelativePath == "" || custody.ContractContentDigest != custody.ContractHash {
		return fmt.Errorf("project archive contract binding is incomplete")
	}
	matches := 0
	for _, entry := range workspace.Inventory.Entries {
		if entry.RelativePath != custody.ContractRelativePath {
			continue
		}
		matches++
		if entry.Kind != InventoryEntryFile || entry.ContentDigest != custody.ContractHash {
			return fmt.Errorf("project archive contract inventory entry does not match registered contract path, type, and hash")
		}
		if entry.DeviceID != custody.ContractDeviceID || entry.Inode != custody.ContractInode || entry.Mode != custody.ContractMode ||
			entry.SizeBytes != custody.ContractSizeBytes || entry.ModifiedUnixNS != custody.ContractModifiedUnixNS {
			return fmt.Errorf("project archive contract entry changed between custody authentication and workspace inventory")
		}
	}
	if matches != 1 {
		return fmt.Errorf("project archive workspace inventory must contain exactly one authenticated contract entry")
	}
	return nil
}

func ProjectPhysicalArchivePlanDigest(plan ProjectPhysicalArchivePlan) (string, error) {
	plan.PlanDigest = ""
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode project physical archive plan: %w", err)
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func SealProjectPhysicalArchivePlan(plan *ProjectPhysicalArchivePlan) error {
	if plan == nil {
		return fmt.Errorf("project physical archive plan is required")
	}
	digest, err := ProjectPhysicalArchivePlanDigest(*plan)
	if err != nil {
		return err
	}
	plan.PlanDigest = digest
	return nil
}

func canonicalProjectRegistrationDetail(detail projects.ProjectRegistrationDetail) (projects.ProjectRegistrationDetail, error) {
	canonical, err := cloneProjectArchiveJSON(detail)
	if err != nil {
		return projects.ProjectRegistrationDetail{}, fmt.Errorf("clone project registration detail: %w", err)
	}
	sort.Slice(canonical.Facets, func(i, j int) bool {
		return canonical.Facets[i].FacetKey+"\x00"+canonical.Facets[i].ProjectContractFacetID < canonical.Facets[j].FacetKey+"\x00"+canonical.Facets[j].ProjectContractFacetID
	})
	sort.Slice(canonical.ScriptExposures, func(i, j int) bool {
		return canonical.ScriptExposures[i].ProjectScriptExposureID+"\x00"+canonical.ScriptExposures[i].ScriptKey < canonical.ScriptExposures[j].ProjectScriptExposureID+"\x00"+canonical.ScriptExposures[j].ScriptKey
	})
	sort.Slice(canonical.ScheduleRegistrations, func(i, j int) bool {
		return canonical.ScheduleRegistrations[i].ProjectScheduleRegistrationID+"\x00"+canonical.ScheduleRegistrations[i].ScheduleKey < canonical.ScheduleRegistrations[j].ProjectScheduleRegistrationID+"\x00"+canonical.ScheduleRegistrations[j].ScheduleKey
	})
	sort.Slice(canonical.DirectEventRegistrations, func(i, j int) bool {
		return canonical.DirectEventRegistrations[i].ProjectDirectEventRegistrationID+"\x00"+canonical.DirectEventRegistrations[i].EventKey < canonical.DirectEventRegistrations[j].ProjectDirectEventRegistrationID+"\x00"+canonical.DirectEventRegistrations[j].EventKey
	})
	sort.Slice(canonical.WatchedRootRegistrations, func(i, j int) bool {
		return canonical.WatchedRootRegistrations[i].ProjectWatchedRootRegistrationID+"\x00"+canonical.WatchedRootRegistrations[i].LocalRootKey < canonical.WatchedRootRegistrations[j].ProjectWatchedRootRegistrationID+"\x00"+canonical.WatchedRootRegistrations[j].LocalRootKey
	})
	sort.Slice(canonical.ConnectorRegistrations, func(i, j int) bool {
		return canonical.ConnectorRegistrations[i].ProjectConnectorRegistrationID+"\x00"+canonical.ConnectorRegistrations[i].ConnectorKey < canonical.ConnectorRegistrations[j].ProjectConnectorRegistrationID+"\x00"+canonical.ConnectorRegistrations[j].ConnectorKey
	})
	sort.Slice(canonical.ModuleRegistrations, func(i, j int) bool {
		return canonical.ModuleRegistrations[i].ProjectModuleRegistrationID+"\x00"+canonical.ModuleRegistrations[i].ModuleKey < canonical.ModuleRegistrations[j].ProjectModuleRegistrationID+"\x00"+canonical.ModuleRegistrations[j].ModuleKey
	})
	sort.Slice(canonical.WorkflowRegistrations, func(i, j int) bool {
		return canonical.WorkflowRegistrations[i].ProjectWorkflowRegistrationID+"\x00"+canonical.WorkflowRegistrations[i].WorkflowKey < canonical.WorkflowRegistrations[j].ProjectWorkflowRegistrationID+"\x00"+canonical.WorkflowRegistrations[j].WorkflowKey
	})
	return canonical, nil
}

func canonicalProjectRepositoryState(state projects.ProjectRepositoryReadModel) (projects.ProjectRepositoryReadModel, error) {
	canonical, err := cloneProjectArchiveJSON(state)
	if err != nil {
		return projects.ProjectRepositoryReadModel{}, fmt.Errorf("clone project repository state: %w", err)
	}
	if state.Source != nil && canonical.Source != nil {
		canonical.Source.ProjectRoot = state.Source.ProjectRoot
	}
	sort.Slice(canonical.Facets, func(i, j int) bool { return canonical.Facets[i].FacetKey < canonical.Facets[j].FacetKey })
	sort.Slice(canonical.Members, func(i, j int) bool {
		return canonical.Members[i].RepositoryID+"\x00"+canonical.Members[i].Key < canonical.Members[j].RepositoryID+"\x00"+canonical.Members[j].Key
	})
	return canonical, nil
}

func cloneProjectArchiveJSON[T any](value T) (T, error) {
	var cloned T
	payload, err := json.Marshal(value)
	if err != nil {
		return cloned, err
	}
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return cloned, err
	}
	return cloned, nil
}

func projectDeactivationActionKey(action projects.ProjectDeactivationAction) string {
	return action.Kind + "\x00" + action.Key + "\x00" + action.Ref + "\x00" + action.Status + "\x00" + action.Summary + "\x00" + string(action.Metadata)
}
