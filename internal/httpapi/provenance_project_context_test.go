package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/provenance"
)

type projectContextTransport struct {
	*provenanceTransportStub
	projectID string
	snapshot  provenance.SemanticID
	calls     int
	err       error
}

func (s *projectContextTransport) GetProjectProjection(_ context.Context, projectID string, snapshot provenance.SemanticID) (provenance.ProjectProjectionSnapshot, error) {
	s.calls++
	s.projectID, s.snapshot = projectID, snapshot
	return provenance.ProjectProjectionSnapshot{
		SchemaVersion: provenance.ProjectProjectionSchemaVersion, ProjectID: projectID, SnapshotID: snapshot,
		Context: provenance.ProjectContextProjection{ProjectID: projectID, Development: &projectstate.ProjectDevelopmentState{
			ProjectID: projectID, Posture: projectstate.ProjectDevelopmentReady, Purpose: "Captured purpose",
			Documents: []projectstate.ProjectDevelopmentDocument{{Path: ".project/STATE.md", Hash: "sha256:" + strings.Repeat("a", 64), Excerpt: "Captured work"}},
		}},
	}, s.err
}

func TestProvenanceProjectContextHTTPClientExactSnapshot(t *testing.T) {
	transport := &projectContextTransport{provenanceTransportStub: &provenanceTransportStub{}}
	auth := &provenanceAuthorizerStub{}
	server := httptest.NewServer(provenanceHTTPTestHandler(transport, auth, &provenanceIdempotencyStub{}))
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	snapshot := provenance.SemanticID("11111111-1111-4111-8111-111111111111")
	for _, selected := range []provenance.SemanticID{snapshot, ""} {
		result, err := client.GetProvenanceProject(context.Background(), "context", projectID, selected)
		if err != nil || transport.projectID != projectID || transport.snapshot != selected || result.Data.SnapshotID != selected || result.Data.Context.Development.Documents[0].Excerpt != "Captured work" {
			t.Fatalf("exact context: %+v %v", result, err)
		}
	}
	if transport.calls != 2 || transport.registerCalls != 0 || transport.repoSyncInput.Project.Project.ProjectID != "" {
		t.Fatal("context read performed mutation")
	}
	for _, capability := range auth.seen {
		if capability != capabilities.ProvenanceFoundationReadCapability {
			t.Fatalf("authority widened: %s", capability)
		}
	}
	transport.err = provenance.ErrProjectProjectionNotFound
	_, err = client.GetProvenanceProject(context.Background(), "missing", projectID, snapshot)
	var requestErr *localclient.RequestError
	if !errors.As(err, &requestErr) || requestErr.StatusCode != http.StatusNotFound {
		t.Fatalf("missing snapshot: %v", err)
	}
	before := transport.calls
	rec := httptest.NewRecorder()
	provenanceHTTPTestHandler(transport, auth, &provenanceIdempotencyStub{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/provenance/projects/"+projectID+"?snapshot_id=a&snapshot_id=b", nil))
	if rec.Code != http.StatusBadRequest || transport.calls != before {
		t.Fatalf("ambiguous citation accepted: %d", rec.Code)
	}
}
