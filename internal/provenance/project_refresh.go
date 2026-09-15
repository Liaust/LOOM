package provenance

import (
	"context"
	"fmt"

	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/repostate"
)

// Shared composition for manual full observation and automatic metadata refresh.
// The latter intentionally never invokes the repository extractor or Git.
type ProjectRefreshService struct {
	Source       projectstate.ProvenanceProjectObserver
	Repositories repostate.ProvenanceProjector
	Projection   interface {
		SyncProjectProjection(context.Context, ProjectProjectionSyncInput) (ProjectProjectionSyncReceipt, error)
	}
}

func (s ProjectRefreshService) RefreshProject(ctx context.Context, project string, contextOnly bool) (ProjectProjectionSyncReceipt, error) {
	if s.Source == nil || s.Projection == nil {
		return ProjectProjectionSyncReceipt{}, fmt.Errorf("project context refresh is not configured")
	}
	var source projectstate.ProvenanceProjectSource
	var err error
	if contextOnly {
		observer, ok := s.Source.(interface {
			ObserveProjectContextForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error)
		})
		if !ok {
			return ProjectProjectionSyncReceipt{}, fmt.Errorf("project metadata observer is unavailable")
		}
		source, err = observer.ObserveProjectContextForProvenance(ctx, project)
	} else {
		source, err = s.Source.ObserveProjectForProvenance(ctx, project)
	}
	if err != nil {
		return ProjectProjectionSyncReceipt{}, err
	}
	input := ProjectProjectionSyncInput{Project: source.Projection, ContextOnly: contextOnly, Repositories: []repostate.ProvenanceProjection{}}
	if !contextOnly {
		if s.Repositories == nil {
			return ProjectProjectionSyncReceipt{}, fmt.Errorf("repository observer is unavailable")
		}
		for _, repository := range source.Repositories {
			if repository.Owned {
				input.Repositories = append(input.Repositories, s.Repositories.ProjectForProvenance(ctx, repository))
			}
		}
	}
	return s.Projection.SyncProjectProjection(ctx, input)
}
