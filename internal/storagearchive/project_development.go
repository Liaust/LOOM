package storagearchive

import (
	"context"
	"reflect"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
)

// ProjectDevelopmentInspector reuses physical custody verification; source paths
// from an archived registration alone never authorize reading a replacement tree.
type ProjectDevelopmentInspector struct {
	Archive ProjectRuntimeService
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
	selected.ProjectRoot = before.ArchivePath
	selected.VerifiedArchive = true
	selected.ObserveGit = false
	result := projectstate.ReadProjectDevelopment(ctx, selected)
	after, ok := s.custody(ctx, input)
	if !ok || !reflect.DeepEqual(before, after) {
		return unavailable
	}
	return result
}

func (s ProjectDevelopmentInspector) custody(ctx context.Context, input projectstate.ProjectDevelopmentInput) (projects.ProjectPhysicalArchiveState, bool) {
	if input.OwnerNode == "" || input.OwnerNode != input.LocalNode || s.Archive.Projects == nil || s.Archive.RepositoryState == nil {
		return projects.ProjectPhysicalArchiveState{}, false
	}
	detail, err := s.Archive.Projects.GetProjectRegistrationStatus(ctx, input.ProjectID)
	if err != nil || detail.Project.Project.ProjectID != input.ProjectID || detail.Project.Project.Status != "archived" || detail.Registration == nil || detail.Registration.ProjectRoot != input.ProjectRoot {
		return projects.ProjectPhysicalArchiveState{}, false
	}
	model, err := s.Archive.RepositoryState.ReadProjectRepositoryState(ctx, input.ProjectID)
	if err != nil || model.Source == nil || model.Source.SourceRevision != input.SourceRevision || model.Source.OwnerNode != input.OwnerNode || model.Source.ProjectRoot != input.ProjectRoot {
		return projects.ProjectPhysicalArchiveState{}, false
	}
	state, ok := projects.ParseProjectPhysicalArchiveState(detail.Project.Project.ArchiveState)
	if !ok || state.Restore != nil || state.Phase != projects.ProjectArchivePhaseComplete {
		return projects.ProjectPhysicalArchiveState{}, false
	}
	inspection := s.Archive.inspectProjectPhysicalArchive(ctx, detail)
	if inspection == nil || inspection.EvidenceStatus != "verified" || inspection.ArchiveOperationID != state.OperationID {
		return projects.ProjectPhysicalArchiveState{}, false
	}
	return state, true
}
