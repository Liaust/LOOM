package loomcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func TestProjectDeploymentOutputKeepsGeneratedEvidenceBehindDetails(t *testing.T) {
	x := pc.ProjectDeploymentResult{ProjectID: "project", NodeID: "main", Resource: "intake", State: "checked", Plan: &pc.DeclarationPlan{PlanID: "internal-plan-digest"}, Operation: &pc.DeclarationResult{OperationID: "job_kept", Revision: "internal-revision", Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerPending, Message: "Continue the same operation."}}}}
	compact, err := json.Marshal(projectDeploymentOutput(x, false))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"compatibility_only", "job_kept", "Continue the same operation."} {
		if !strings.Contains(string(compact), required) {
			t.Fatal("lost useful compact result", required, string(compact))
		}
	}
	for _, hidden := range []string{"internal-plan-digest", "internal-revision", `"plan":`, `"operation":`} {
		if strings.Contains(string(compact), hidden) {
			t.Fatal("generated evidence leaked into default output", hidden)
		}
	}
	full, err := json.Marshal(projectDeploymentOutput(x, true))
	if err != nil || !strings.Contains(string(full), "internal-plan-digest") || !strings.Contains(string(full), "internal-revision") {
		t.Fatal("details lost evidence", err, string(full))
	}
}

func TestProjectDeployNativeWaitOnlyReadsAndReportsTerminalFailure(t *testing.T) {
	for _, noWait := range []bool{true, false} {
		t.Run(map[bool]string{true: "no-wait", false: "wait-failure"}[noWait], func(t *testing.T) {
			posts, reads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/v1/project-deploy" {
					posts++
					x := pc.ProjectDeploymentResult{ProjectID: "fixture", NodeID: "main", Resource: "worker", Builder: "cargo", BuildJobID: "job_build", State: "partial", Operation: &pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "job_parent", State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerPending, CauseCode: "owner_pending"}}}}
					response.WriteJSON(w, 200, response.Success("deploy", x))
					return
				}
				if r.Method != http.MethodGet {
					t.Errorf("wait advanced an operation: %s", r.Method)
				}
				reads++
				response.WriteJSON(w, 200, response.Success("read", pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "job_parent", State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed, CauseCode: "application_build_failed"}}}))
			}))
			defer server.Close()
			args := []string{"--config", writeProjectBackendInstallManifest(t, server.URL), "--json", "project", "deploy", "fixture", "--resource", "worker", "--yes"}
			if noWait {
				args = append(args, "--no-wait")
			}
			out, _, err := executeRootCommand(args...)
			if (err == nil) != noWait {
				t.Fatalf("terminal exit status: %v output=%s", err, out)
			}
			var result map[string]any
			if json.Unmarshal([]byte(out), &result) != nil || result["build_job_id"] != "job_build" || posts != 1 || (noWait && reads != 0) || (!noWait && reads != 1) {
				t.Fatalf("unexpected continuation: posts=%d reads=%d output=%s", posts, reads, out)
			}
		})
	}
}
