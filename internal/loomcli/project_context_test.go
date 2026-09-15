package loomcli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func contextFixture() pc.DeclarationStatus {
	return pc.DeclarationStatus{
		SchemaVersion: pc.DeclarationStatusSchemaV05,
		Target:        pc.DeclarationTarget{ProjectID: "verified-project", OwnerNodeID: "verified-owner", ProjectRoot: "/owner/quote's project"},
		Revision:      "current-revision",
		Readiness: pc.DeclarationReadiness{
			Desired:    pc.DeclarationFact{State: pc.DeclarationSatisfied, Revision: "current-revision"},
			Queued:     pc.DeclarationFact{State: pc.DeclarationPending},
			Applied:    pc.DeclarationFact{State: pc.DeclarationFailed},
			Processing: pc.DeclarationFact{State: pc.DeclarationNotApplicable},
			Healthy:    pc.DeclarationFact{State: pc.DeclarationUnknown},
			Protected:  pc.DeclarationFact{State: pc.DeclarationUnknown},
			Verified:   pc.DeclarationFact{State: pc.DeclarationUnknown},
		},
		Operation: &pc.DeclarationResult{OperationID: "old-operation", PlanID: "old-plan", Revision: "old-revision", State: pc.DeclarationOperationSucceeded, Readiness: pc.DeclarationReadiness{Healthy: pc.DeclarationFact{State: pc.DeclarationSatisfied}}},
	}
}

func decodeContext(t *testing.T, out string) map[string]any {
	t.Helper()
	if len(out) > 8192 {
		t.Fatalf("JSON budget: %d", len(out))
	}
	d := json.NewDecoder(strings.NewReader(out))
	var got map[string]any
	if err := d.Decode(&got); err != nil {
		t.Fatalf("JSON: %v: %s", err, out)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		t.Fatal("second JSON document")
	}
	if got["schema_version"] != "loom.project.context.v1" {
		t.Fatalf("schema: %v", got)
	}
	return got
}

func TestProjectContextBoundedOwnerAndReadiness(t *testing.T) {
	status := contextFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 200, response.Success("context", status))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	out, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "context", "caller-selector", "--node", "selected-owner")
	if err != nil {
		t.Fatalf("%v %s", err, stderr)
	}
	got := decodeContext(t, out)
	if got["revision"] != status.Revision || got["target"].(map[string]any)["owner_node_id"] != status.Target.OwnerNodeID {
		t.Fatalf("wrong authority: %s", out)
	}
	var readiness pc.DeclarationReadiness
	raw, _ := json.Marshal(got["readiness"])
	_ = json.Unmarshal(raw, &readiness)
	if !reflect.DeepEqual(readiness, status.Readiness) {
		t.Fatalf("historical state projected: %+v", readiness)
	}
	if got["historical_operation"].(map[string]any)["revision"] != "old-revision" {
		t.Fatal("history not separate")
	}
	if got["source"].(map[string]any)["ref"] != "/owner/quote's project/.loom/project.yaml" {
		t.Fatalf("source guessed: %s", out)
	}
	out, stderr, err = executeRootCommand("--config", cfg, "project", "context", "caller-selector")
	if err != nil || len(out) > 4096 || strings.Count(out, "\n") > 16 || !strings.Contains(out, "Historical operation:") || !strings.Contains(out, "Healthy: unknown") {
		t.Fatalf("human: %v %s %s", err, out, stderr)
	}
	for _, name := range []string{"Desired:", "Queued:", "Applied:", "Processing:", "Healthy:", "Protected:", "Verified:"} {
		if !strings.Contains(out, name) {
			t.Fatalf("missing %s", name)
		}
	}
}

func TestProjectContextDoesNotAdvanceOrSearch(t *testing.T) {
	for _, selector := range []string{"/owner/quote's project", "/owner/new\nline", "."} {
		t.Run(selector, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/projects/"+selector+"/declaration-status" || r.URL.Query().Get("node_ref") != "explicit-owner" {
					t.Errorf("unexpected read/mutation: %s %s", r.Method, r.RequestURI)
				}
				response.WriteJSON(w, 200, response.Success("read", contextFixture()))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			_, stderr, err := executeRootCommand("--config", cfg, "project", "context", selector, "--node", "explicit-owner")
			if err != nil || calls != 1 {
				t.Fatalf("reads=%d err=%v %s", calls, err, stderr)
			}
		})
	}
	for _, code := range []pc.DeclarationErrorCode{pc.DeclarationTargetUnavailable, pc.DeclarationUnsupported} {
		t.Run(string(code), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				response.WriteJSON(w, 422, struct {
					response.ErrorEnvelope
					Detail pc.DeclarationError `json:"declaration_error"`
				}{response.ErrorEnvelope{Error: response.ErrorBody{Code: string(code), Summary: "owner refused"}}, pc.DeclarationError{Code: code, CauseCode: "remote_source_owner_required"}})
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			out, _, err := executeRootCommand("--config", cfg, "--json", "project", "context", "/remote", "--node", "wrong-owner")
			if err == nil || calls != 1 || !strings.Contains(out, "remote_source_owner_required") || strings.Contains(out, "loom.project.context.v1") {
				t.Fatalf("refusal lost: %v %s", err, out)
			}
		})
	}
}

func TestProjectContextVerboseSources(t *testing.T) {
	for _, mode := range []string{"matched", "revision_drift", "owner_drift", "root_drift", "unavailable", "oversized", "unsafe"} {
		t.Run(mode, func(t *testing.T) {
			status := contextFixture()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					if r.Method != "GET" {
						t.Error("default read changed")
					}
					response.WriteJSON(w, 200, response.Success("status", status))
					return
				}
				if calls != 2 || r.Method != "POST" || r.URL.Path != "/v1/project-declaration-plans" {
					t.Errorf("fanout: %s %s", r.Method, r.RequestURI)
				}
				var input pc.DeclarationPlanRequest
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.ProjectRef != status.Target.ProjectID || input.NodeRef != status.Target.OwnerNodeID || !reflect.DeepEqual(input.Effects, []pc.DeclarationEffect{pc.DeclarationReconcile}) {
					t.Errorf("unverified verbose selectors: %+v", input)
				}
				if mode == "unavailable" {
					response.WriteJSON(w, 503, response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.target_unavailable", Summary: "unreadable"}})
					return
				}
				plan := pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: status.Revision}
				plan.Basis.Target = status.Target
				plan.Basis.Sources = []pc.DeclarationSource{{Ref: "exact-source", Hash: "source-hash", Revision: "source-revision"}}
				switch mode {
				case "oversized":
					for i := 0; i < 1000; i++ {
						plan.Basis.Sources = append(plan.Basis.Sources, pc.DeclarationSource{Ref: "large-source", Hash: strings.Repeat("<", 512)})
					}
				case "unsafe":
					plan.Basis.Sources[0].Ref = "unsafe\nsource"
				case "revision_drift":
					plan.PlanID = "changed"
				case "owner_drift":
					plan.Basis.Target.OwnerNodeID = "other"
				case "root_drift":
					plan.Basis.Target.ProjectRoot = "/other"
				}
				response.WriteJSON(w, 200, response.Success("plan", plan))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			out, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "context", "caller", "--verbose")
			if err != nil || calls != 2 {
				t.Fatalf("%v calls=%d %s", err, calls, stderr)
			}
			got := decodeContext(t, out)
			want := mode
			if strings.HasSuffix(mode, "_drift") {
				want = "drift"
			}
			if mode == "oversized" || mode == "unsafe" {
				want = "matched"
				if len(got["omitted"].(map[string]any)) == 0 {
					t.Fatal("source omissions not reported")
				}
			}
			if got["source_status"] != want || strings.Contains(out, "exact-source") != (mode == "matched" || mode == "oversized") {
				t.Fatalf("mixed revisions: %s", out)
			}
		})
	}
}

func TestProjectContextOversizedAndUnsafe(t *testing.T) {
	for _, field := range []string{"collections", "identity", "control"} {
		t.Run(field, func(t *testing.T) {
			status := contextFixture()
			large := strings.Repeat("界", 9000)
			switch field {
			case "collections":
				status.Readiness.Healthy.EvidenceRef = large
				for i := 0; i < 1000; i++ {
					status.Errors = append(status.Errors, pc.DeclarationError{Code: pc.DeclarationOwnerFailed, CauseCode: "owner_pending", Message: "untrusted free text", CompletedEffects: []string{strings.Repeat("e", 1024)}})
				}
			case "identity":
				status.Target.ProjectID = large
				status.Target.ProjectRoot = "/" + large
				status.Revision = large
				status.Operation.OperationID = large
			case "control":
				status.Target.ProjectID = "--help\nINJECTED"
				status.Operation.OperationID = "bad\x1b[31m"
				status.Errors = []pc.DeclarationError{{Code: pc.DeclarationUnauthorized, CauseCode: "bad\nINJECTED"}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response.WriteJSON(w, 200, response.Success("bounded", status))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			for _, jsonMode := range []bool{true, false} {
				args := []string{"--config", cfg, "project", "context", "caller"}
				if jsonMode {
					args = append(args, "--json")
				}
				out, stderr, err := executeRootCommand(args...)
				if err != nil {
					t.Fatalf("%v %s", err, stderr)
				}
				if jsonMode {
					got := decodeContext(t, out)
					if len(got["omitted"].(map[string]any)) == 0 {
						t.Fatal("omissions not reported")
					}
				} else if len(out) > 4096 || strings.Count(out, "\n") > 16 || !strings.Contains(out, "Omitted:") {
					t.Fatalf("human budget/omissions: %d %s", len(out), out)
				}
				if strings.Contains(out, "INJECTED") || strings.Contains(out, "\x1b") || strings.Contains(out, large) {
					t.Fatal("unsafe/oversize field displayed")
				}
			}
		})
	}
	for _, args := range [][]string{{"project", "context", "--help"}, {"project", "--help"}} {
		out, _, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(out, "context") {
			t.Fatalf("help: %v %s", err, out)
		}
	}
}

func TestProjectContextAdversarialBudgetsAndVerboseDrift(t *testing.T) {
	for _, size := range []int{0, 128, 700, 1024, 2048, 4096, 4097} {
		for _, value := range []string{strings.Repeat("界", size), strings.Repeat("<&", size)} {
			status := contextFixture()
			status.Target.ProjectRoot = "/" + value
			status.Target.LocationRevision = value
			status.Revision = value
			for _, fact := range declarationFacts(&status.Readiness) {
				fact.Revision = value
				fact.EvidenceRef = value
			}
			status.Errors = []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed, CauseCode: "owner_pending", CompletedEffects: []string{value}}}
			view := newProjectContext(status)
			view.SourceStatus = "drift"
			before, _ := json.Marshal(view)
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := renderProjectContext(cmd, &options{}, view); err != nil {
				t.Fatal(err)
			}
			if len(out.String()) > 4096 || strings.Count(out.String(), "\n") > 16 || !strings.Contains(out.String(), "Source detail: drift") {
				t.Fatalf("human budget/drift size=%d: %d %s", size, out.Len(), out.String())
			}
			for _, name := range declarationFactNames {
				if !strings.Contains(out.String(), name+":") {
					t.Fatalf("readiness dropped: %s", out.String())
				}
			}
			after, _ := json.Marshal(view)
			if !bytes.Equal(before, after) {
				t.Fatal("human rendering mutated projection")
			}
			out.Reset()
			if err := renderProjectContext(cmd, &options{jsonOutput: true}, view); err != nil {
				t.Fatal(err)
			}
			decodeContext(t, out.String())
		}
	}
}
