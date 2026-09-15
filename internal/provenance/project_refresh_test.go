package provenance

import (
	"context"
	"testing"

	"loom.local/loom/internal/projectstate"
)

type refreshSourceFixture struct{ full, metadata int }

func (s *refreshSourceFixture) ObserveProjectForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	s.full++
	return projectstate.ProvenanceProjectSource{}, nil
}
func (s *refreshSourceFixture) ObserveProjectContextForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	s.metadata++
	return projectstate.ProvenanceProjectSource{}, nil
}

type refreshProjectionFixture struct{ input ProjectProjectionSyncInput }

func (s *refreshProjectionFixture) SyncProjectProjection(_ context.Context, input ProjectProjectionSyncInput) (ProjectProjectionSyncReceipt, error) {
	s.input = input
	return ProjectProjectionSyncReceipt{}, nil
}
func TestProjectRefreshMetadataDoesNotInspectRepositories(t *testing.T) {
	source := &refreshSourceFixture{}
	projection := &refreshProjectionFixture{}
	refresh := ProjectRefreshService{Source: source, Projection: projection}
	if _, err := refresh.RefreshProject(t.Context(), "project", true); err != nil {
		t.Fatal(err)
	}
	if source.full != 0 || source.metadata != 1 || !projection.input.ContextOnly || len(projection.input.Repositories) != 0 {
		t.Fatal("metadata refresh broadened")
	}
}
