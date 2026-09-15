package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"loom.local/loom/internal/projectcontracts"
)

func TestProjectCreateUsesExistingScaffoldTransport(t *testing.T) {
	called := false
	client := Client{BaseURL: "http://loom.local", client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		if req.Method != http.MethodPost || req.URL.Path != "/v1/project-scaffolds" {
			t.Errorf("unexpected endpoint: %s %s", req.Method, req.URL.Path)
		}
		var input projectcontracts.ScaffoldOptions
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Mode != projectcontracts.ScaffoldModeDeclaration || input.Directory != "/fixture/projects" || input.OwnerNode != "fixture-node" || !input.DryRun || input.Preset != "" {
			t.Errorf("wire input: %+v", input)
		}
		return okEnvelope(`{"ok":true,"mode":"declaration","project_root":"/fixture/projects/sample","owner_node":"fixture-node","files":[{"path":".loom/project.yaml","kind":"root_contract","action":"planned"}],"facets":[]}`), nil
	})}}
	result, err := client.ScaffoldProject(context.Background(), "corr_create", projectcontracts.ScaffoldOptions{Name: "Sample", OwnerNode: "fixture-node", Directory: "/fixture/projects", Mode: projectcontracts.ScaffoldModeDeclaration, DryRun: true})
	if err != nil || !called || result.Data.Mode != projectcontracts.ScaffoldModeDeclaration || len(result.Data.Files) != 1 {
		t.Fatalf("transport: %+v %v", result, err)
	}
}
