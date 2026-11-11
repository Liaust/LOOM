package storagearchive

import (
	"context"
	"fmt"
	"reflect"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

// ProjectDevelopmentInspector reuses physical custody verification; source paths
// from an archived registration alone never authorize reading a replacement tree.
type ProjectDevelopmentInspector struct {
	Archive ProjectRuntimeService
}

type verifiedProjectArchiveSource struct {
	projects.ProjectPhysicalArchiveState
	CurrentPath  string
	RelocationID string
}

func (s ProjectDevelopmentInspector) Inspect(ctx context.Context, input projectstate.ProjectDevelopmentInput) projectstate.ProjectDevelopmentState {
	input.VerifiedArchive = false
	if input.Lifecycle != "archived" {
		return projectstate.ReadProjectDevelopment(ctx, input)
	}
	unavailable := projectstate.ReadProjectDevelopment(ctx, input)
	before, ok := s.custody(ctx, input)
	if !ok {
		return unavailable
	}
	selected := input
	selected.ProjectRoot = before.CurrentPath
	selected.VerifiedArchive = true
	selected.ObserveGit = false
	result := projectstate.ReadProjectDevelopment(ctx, selected)
	after, ok := s.custody(ctx, input)
	if !ok || !reflect.DeepEqual(before, after) {
		return unavailable
	}
	result.Archive = projectArchiveSourceContext(before.ProjectPhysicalArchiveState)
	result.Archive.ReadOnlyRoot = before.CurrentPath
	return result
}

// ResolveRepositoryReadSource uses the same authenticated workspace inspection
// as project development, plus the preserved project plan's exact membership.
// No active-path probe, restore, alternate layout or new archive reader occurs.
func (s ProjectDevelopmentInspector) ResolveRepositoryReadSource(ctx context.Context, input projectstate.ProjectDevelopmentInput, model projects.ProjectRepositoryReadModel) (projectstate.RepositoryReadLocation, error) {
	before, err := s.repositoryCustody(ctx, input, model)
	if err != nil {
		return projectstate.RepositoryReadLocation{}, err
	}
	selected, err := projectstate.RelocateRepositoryReadSource(model.Source, before.CurrentPath)
	if err != nil {
		return projectstate.RepositoryReadLocation{}, err
	}
	return projectstate.RepositoryReadLocation{Source: selected, Archive: projectArchiveSourceContext(before.ProjectPhysicalArchiveState), Revalidate: func(ctx context.Context) error {
		after, err := s.repositoryCustody(ctx, input, model)
		if err != nil || !reflect.DeepEqual(before, after) {
			return fmt.Errorf("%w: retained repository source changed", projectstate.ErrProjectStateUnavailable)
		}
		return nil
	}}, nil
}

func projectArchiveSourceContext(state projects.ProjectPhysicalArchiveState) *projectstate.ArchivedSourceContext {
	return &projectstate.ArchivedSourceContext{OperationID: state.OperationID, ManifestDigest: state.ArchiveManifestDigest, ArchivedAt: state.ArchivedAt.UTC()}
}

func (s ProjectDevelopmentInspector) repositoryCustody(ctx context.Context, input projectstate.ProjectDevelopmentInput, model projects.ProjectRepositoryReadModel) (verifiedProjectArchiveSource, error) {
	invalid := fmt.Errorf("%w: retained repository membership or custody is unavailable", projectstate.ErrProjectStateUnavailable)
	if input.Lifecycle != "archived" || model.Project.ProjectID != input.ProjectID || model.Project.Status != "archived" || model.Source == nil || s.Archive.PlanEvidence == nil {
		return verifiedProjectArchiveSource{}, invalid
	}
	state, ok := s.custody(ctx, input)
	if !ok {
		return verifiedProjectArchiveSource{}, invalid
	}
	current, err := s.Archive.RepositoryState.ReadProjectRepositoryState(ctx, input.ProjectID)
	if err != nil || !reflect.DeepEqual(current, model) {
		return verifiedProjectArchiveSource{}, invalid
	}
	plan, err := s.Archive.loadCurrentProjectArchivePlan(ctx, input.ProjectID)
	if err != nil || plan.PlanDigest != state.PlanDigest || plan.Workspace.PlanDigest != state.WorkspacePlanDigest || plan.Custody.RepositoryProjectRoot != input.ProjectRoot || plan.Repository.Source == nil {
		return verifiedProjectArchiveSource{}, invalid
	}
	// Private plan JSON intentionally omits execution locators. Restore only
	// the root already bound by that plan before its existing snapshot check.
	plan.Repository.Source.ProjectRoot = plan.Custody.RepositoryProjectRoot
	detail, err := s.Archive.Projects.GetProjectRegistrationStatus(ctx, input.ProjectID)
	if err != nil || validateCurrentProjectArchiveSnapshots(plan, s.Archive.WorkspaceRoots, detail, current, &state.ProjectPhysicalArchiveState) != nil {
		return verifiedProjectArchiveSource{}, invalid
	}
	return state, nil
}

func (s ProjectDevelopmentInspector) custody(ctx context.Context, input projectstate.ProjectDevelopmentInput) (verifiedProjectArchiveSource, bool) {
	if input.OwnerNode == "" || input.OwnerNode != input.LocalNode || s.Archive.Projects == nil || s.Archive.RepositoryState == nil {
		return verifiedProjectArchiveSource{}, false
	}
	detail, err := s.Archive.Projects.GetProjectRegistrationStatus(ctx, input.ProjectID)
	if err != nil || detail.Project.Project.ProjectID != input.ProjectID || detail.Project.Project.Status != "archived" || detail.Registration == nil || detail.Registration.ProjectRoot != input.ProjectRoot {
		return verifiedProjectArchiveSource{}, false
	}
	model, err := s.Archive.RepositoryState.ReadProjectRepositoryState(ctx, input.ProjectID)
	if err != nil || model.Source == nil || model.Source.SourceRevision != input.SourceRevision || model.Source.OwnerNode != input.OwnerNode || model.Source.ProjectRoot != input.ProjectRoot {
		return verifiedProjectArchiveSource{}, false
	}
	state, ok := projects.ParseProjectPhysicalArchiveState(detail.Project.Project.ArchiveState)
	if !ok || state.Restore != nil || state.Phase != projects.ProjectArchivePhaseComplete {
		return verifiedProjectArchiveSource{}, false
	}
	inspection := s.Archive.inspectProjectPhysicalArchive(ctx, detail)
	if inspection == nil || inspection.EvidenceStatus != "verified" || inspection.ArchiveOperationID != state.OperationID {
		return verifiedProjectArchiveSource{}, false
	}
	current := state.ArchivePath
	if inspection.CurrentPayload != nil {
		current = inspection.CurrentPayload.AbsolutePath
	}
	return verifiedProjectArchiveSource{ProjectPhysicalArchiveState: state, CurrentPath: current, RelocationID: inspection.RelocationID}, true
}
