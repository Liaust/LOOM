package httpapi

import (
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectstate"
)

func TestProjectDevelopmentNavigation(t *testing.T) {
	observer := historicalDevelopmentObserver{source: projectstate.ProvenanceProjectSource{Projection: projectstate.ProjectProjection{Development: projectstate.ProjectDevelopmentState{
		Posture: projectstate.ProjectDevelopmentArchived, Archive: &projectstate.ArchivedSourceContext{ReadOnlyRoot: "/retained/project", OperationID: "verified-operation"},
	}}}}
	server := Server{services: Services{ProjectRepos: ProjectRepositoryServices{Provenance: observer}}}
	status := pc.DeclarationStatus{Lifecycle: "archived", Target: pc.DeclarationTarget{ProjectID: "project", ProjectRoot: "/old/project"}}
	server.attachProjectDevelopment(t.Context(), &status)
	if status.Development.Archive.ReadOnlyRoot != "/retained/project" || status.Target.ProjectRoot != "/old/project" {
		t.Fatalf("read-only navigation lost or active root replaced: %+v", status)
	}
	observer.source.Projection.Development.Archive = nil
	server.services.ProjectRepos.Provenance = observer
	server.attachProjectDevelopment(t.Context(), &status)
	if status.Development.Archive != nil || status.Development.Posture != "unavailable" {
		t.Fatal("unverified root exposed")
	}
}
