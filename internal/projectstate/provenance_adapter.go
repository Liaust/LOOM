package projectstate

import (
	"context"
	"fmt"
	"sort"

	"loom.local/loom/internal/projects"
)

// ProvenanceProjectSource is the bounded internal bridge from the technical
// project registry to the provenance projection owner. RepositoryRoot is an
// execution-only locator and must never be persisted or exposed by the
// provenance surface.
type ProvenanceProjectSource struct {
	Projection   ProjectProjection
	Repositories []ProvenanceRepositorySource
}

type ProvenanceRepositorySource struct {
	Projection  RepositoryProjection
	ProjectSlug string
	// SourceVersion is the accepted membership binding revision. Provenance
	// combines it with deterministic project/repository output, then allocates
	// its own monotonic projection revision transactionally.
	SourceVersion        int64
	RepositoryRoot       string `json:"-"`
	NavigationPath       string `json:"-"`
	MembershipSourcePath string `json:"-"`
	Available            bool
	ReasonCode           string
	Owned                bool
}

type ProvenanceProjectObserver interface {
	ObserveProjectForProvenance(context.Context, string) (ProvenanceProjectSource, error)
}

// ProvenanceProjectContextObserver is the metadata-only refresh boundary. It
// retains authoritative memberships without Git, .repo, or member-path probes.
type ProvenanceProjectContextObserver interface {
	ObserveProjectContextForProvenance(context.Context, string) (ProvenanceProjectSource, error)
}

func (s Service) ObserveProjectContextForProvenance(ctx context.Context, ref string) (ProvenanceProjectSource, error) {
	projection, model, err := s.observeProjectMode(ctx, ref, false)
	if err != nil {
		return ProvenanceProjectSource{}, err
	}
	return s.projectSourceForProvenance(projection, model, false)
}

// ObserveProjectForProvenance consumes the same bounded registry snapshot as
// ObserveProject. Execution-only locations and membership revisions cannot be
// mixed with a later source or member binding.
func (s Service) ObserveProjectForProvenance(ctx context.Context, ref string) (ProvenanceProjectSource, error) {
	projection, model, err := s.observeProject(ctx, ref)
	if err != nil {
		return ProvenanceProjectSource{}, err
	}
	return s.projectSourceForProvenance(projection, model, true)
}

func (s Service) projectSourceForProvenance(projection ProjectProjection, model projects.ProjectRepositoryReadModel, inspectRepositories bool) (ProvenanceProjectSource, error) {

	result := ProvenanceProjectSource{Projection: projection, Repositories: []ProvenanceRepositorySource{}}
	if model.Source == nil {
		return result, nil
	}
	membershipSource, err := repositoryMembershipSource(model.Source)
	if err != nil {
		return ProvenanceProjectSource{}, err
	}
	paths := s.Paths
	if paths == nil {
		paths = OSPathResolver{}
	}
	projectedByID := make(map[string]RepositoryProjection, len(projection.Members))
	for _, member := range projection.Members {
		projectedByID[member.RepositoryID] = member
	}
	for _, member := range model.Members {
		projected := projectedByID[member.RepositoryID]
		if member.ObservationRevision <= 0 {
			return ProvenanceProjectSource{}, fmt.Errorf("%w: membership revision is required", ErrProjectStateUnavailable)
		}
		source := ProvenanceRepositorySource{
			Projection: projected, ProjectSlug: projection.Project.Slug,
			SourceVersion: member.ObservationRevision, MembershipSourcePath: membershipSource,
			Owned: member.RepositoryOwnerProjectID == projection.Project.ProjectID &&
				(member.Role == projects.ProjectRepositoryRolePrimary || member.Role == projects.ProjectRepositoryRoleComponent),
		}
		if !source.Owned {
			source.ReasonCode = "repository_not_owned_by_project"
			result.Repositories = append(result.Repositories, source)
			continue
		}
		if projected.ObservationPosture == projects.ProjectRepositoryObservationRemoteUnavailable {
			source.ReasonCode = "remote_unavailable"
			result.Repositories = append(result.Repositories, source)
			continue
		}
		if !inspectRepositories {
			source.ReasonCode = "repository_not_sampled"
			result.Repositories = append(result.Repositories, source)
			continue
		}
		navigation, resolveErr := repositorySourceLocation(model.Source, member.Path)
		var resolved ResolvedPath
		if resolveErr == nil {
			source.NavigationPath = navigation
			resolved, resolveErr = paths.ResolveWithin(model.Source.ProjectRoot, navigation)
		}
		if resolveErr != nil {
			source.ReasonCode = pathObservationReason(resolveErr)
		} else if !resolved.Exists {
			source.ReasonCode = "member_path_missing"
		} else if !resolved.Directory {
			source.ReasonCode = "member_path_not_directory"
		} else {
			source.RepositoryRoot = resolved.Path
			source.Available = true
		}
		result.Repositories = append(result.Repositories, source)
	}
	sort.Slice(result.Repositories, func(i, j int) bool {
		return result.Repositories[i].Projection.RepositoryID < result.Repositories[j].Projection.RepositoryID
	})
	return result, nil
}
