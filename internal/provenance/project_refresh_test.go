package provenance

import (
	"context"
	"testing"

	"loom.local/loom/internal/projectstate"
)

type refreshSourceFixture struct {
	full, metadata int
	projection     projectstate.ProjectProjection
}

func (s *refreshSourceFixture) ObserveProjectForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	s.full++
	return projectstate.ProvenanceProjectSource{Projection: s.projection}, nil
}
func (s *refreshSourceFixture) ObserveProjectContextForProvenance(context.Context, string) (projectstate.ProvenanceProjectSource, error) {
	s.metadata++
	return projectstate.ProvenanceProjectSource{Projection: s.projection}, nil
}

func TestBoxProjectRefreshCarriesVerifiedHomeMembership(t *testing.T) {
	source := &refreshSourceFixture{projection: projectstate.ProjectProjection{Project: projectstate.ProjectIdentityProjection{ProjectID: projectionTestProjectID}}}
	projection := &refreshProjectionFixture{}
	refresh := ProjectRefreshService{Source: source, Projection: projection, HomeBoxID: func(_ context.Context, projectID string) (string, error) {
		if projectID != projectionTestProjectID {
			t.Fatal(projectID)
		}
		return "box_company", nil
	}}
	if _, err := refresh.RefreshProject(boxContext("box_company"), projectionTestProjectID, true); err != nil || projection.input.HomeBoxID != "box_company" {
		t.Fatal(projection.input.HomeBoxID, err)
	}
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
