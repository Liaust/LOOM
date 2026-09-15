package loomcli

import (
	"net/http"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

func TestProvenanceProjectContextCLIExactGet(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	snapshot := "11111111-1111-4111-8111-111111111111"
	calls := 0
	socket, closeServer := startStorageCommandServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/v1/provenance/projects/"+projectID || r.URL.Query().Get("snapshot_id") != snapshot {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		response.WriteJSON(w, http.StatusOK, response.Success("snapshot", provenance.ProjectProjectionSnapshot{
			ProjectID: projectID, SnapshotID: provenance.SemanticID(snapshot),
			Context: provenance.ProjectContextProjection{Name: "No Git project", ProjectID: projectID, Development: &projectstate.ProjectDevelopmentState{Posture: projectstate.ProjectDevelopmentReady, CurrentFocus: "Implement WebDAV", Documents: []projectstate.ProjectDevelopmentDocument{{Path: ".project/STATE.md", Excerpt: "Captured context"}}}},
		}))
	})
	defer closeServer()
	for _, format := range []string{"--json", "--plain", ""} {
		args := []string{"--socket", socket}
		if format != "" {
			args = append(args, format)
		}
		args = append(args, "provenance", "project", "get", projectID, "--snapshot", snapshot)
		out, stderr, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(out, projectID) {
			t.Fatalf("get: %v %s %s", err, out, stderr)
		}
		if format == "--json" && !strings.Contains(out, "Captured context") {
			t.Fatal("captured source missing")
		}
		if format == "" && !strings.Contains(out, "not accepted semantic decisions") {
			t.Fatal("source posture lost")
		}
	}
	if calls != 3 {
		t.Fatalf("extra retrieval calls: %d", calls)
	}
}

func TestProjectContextDevelopmentKeepsTechnicalReadiness(t *testing.T) {
	status := contextFixture()
	status.Development = &pc.DeclarationDevelopmentContext{Posture: "ready", Purpose: "WebDAV", CurrentFocus: "Implement service", Progress: "Not deployed", NextAction: "Start first handler", SnapshotRef: "/v1/provenance/projects/test"}
	view := newProjectContext(status)
	if view.Development == nil || view.Development.CurrentFocus != "Implement service" || view.Readiness.Healthy.State != pc.DeclarationUnknown {
		t.Fatalf("development mixed with runtime health: %+v", view)
	}
	status.Development.CurrentFocus = "unsafe\x1b[31m"
	view = newProjectContext(status)
	if view.Development != nil || view.Omitted["development"] != 1 {
		t.Fatal("unsafe context was not qualified as omitted")
	}
}
