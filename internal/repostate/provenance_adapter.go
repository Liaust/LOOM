package repostate

import (
	"context"

	"loom.local/loom/internal/projectstate"
)

// ProvenanceProjection retains the accepted deterministic extractor output
// beside its authoritative project membership. Available=false represents a
// registered repository that cannot safely be inspected; it is still
// projected with typed posture by the provenance owner.
type ProvenanceProjection struct {
	Source     projectstate.ProvenanceRepositorySource
	Extraction Extraction
	Available  bool
	ReasonCode string
}

type ProvenanceProjector interface {
	ProjectForProvenance(context.Context, projectstate.ProvenanceRepositorySource) ProvenanceProjection
}

type ProvenanceAdapter struct {
	Extractor Extractor
}

func (adapter ProvenanceAdapter) ProjectForProvenance(ctx context.Context, source projectstate.ProvenanceRepositorySource) ProvenanceProjection {
	result := ProvenanceProjection{Source: source, Available: source.Available, ReasonCode: source.ReasonCode}
	if !source.Available || !source.Owned {
		return result
	}
	if !validProjectRelativeNavigation(source.NavigationPath) || !validProjectRelativeNavigation(source.MembershipSourcePath) || source.SourceVersion <= 0 {
		result.Available = false
		result.ReasonCode = "repository_source_binding_invalid"
		return result
	}
	role := RepositoryRole(source.Projection.Role)
	membership := &AuthoritativeMembership{
		Resolved:         true,
		RepositoryID:     source.Projection.RepositoryID,
		OwnerProjectID:   source.Projection.RepositoryOwnerProjectID,
		OwnerProjectSlug: source.ProjectSlug,
		OwnerRole:        role,
		StateRoot:        source.Projection.StateRoot,
		NavigationPath:   source.NavigationPath,
		SourcePath:       source.MembershipSourcePath,
		SourceDigest:     source.Projection.SourceBindingDigest,
	}
	result.Extraction = adapter.Extractor.Extract(ctx, ExtractInput{
		RepositoryRoot: source.RepositoryRoot,
		Membership:     membership,
	})
	return result
}
