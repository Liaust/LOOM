package loomcli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/serviceregistry"
)

func TestServiceRegisterDeclarationProject(t *testing.T) {
	const projectID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, test := range []struct {
		action       string
		mismatch     bool
		backendError bool
		human        bool
	}{
		{action: "plan"}, {action: "apply"},
		{action: "plan", mismatch: true}, {action: "apply", mismatch: true},
		{action: "plan", backendError: true}, {action: "apply", backendError: true},
		{action: "plan", backendError: true, human: true}, {action: "apply", backendError: true, human: true},
	} {
		name := test.action
		if test.mismatch {
			name += "_project_mismatch"
		}
		if test.backendError {
			name += "_backend_error"
		}
		if test.human {
			name += "_human"
		}
		t.Run(name, func(t *testing.T) {
			action := test.action
			failure := response.ErrorEnvelope{
				Error: response.ErrorBody{Code: "service_registration.apply_failed", Summary: "Registration could not be applied.", Domain: "services", Target: "webdav", Hint: "Inspect the reviewed service allowlist.", CorrelationID: "server-registration-error"},
				Meta:  response.NewMeta("server-registration-error"),
			}
			failure.Meta.Redactions = nil
			root := t.TempDir()
			files := map[string]string{
				".loom/project.yaml":                       "kind: loom.project\nschema_version: project.contract.v0.5\nproject: {id: " + projectID + ", slug: webdav-project, name: WebDAV project, owner_node: main}\nresources: {}\n",
				".loom/contracts/services/webdav.yaml":     "kind: service_registration\nschema_version: loom.service.v0.1\nservice: {key: webdav, name: WebDAV, target_node: main}\nruntime: {manager: systemd, unit: webdav.service}\noperations: {status: true, stop: true}\n",
				".loom/contracts/services/unselected.yaml": "not a valid service contract",
			}
			for relative, raw := range files {
				path := filepath.Join(root, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
					t.Fatal(err)
				}
			}
			analysis := projectcontracts.Analyze(root)
			if !analysis.Report.OK || len(analysis.Plan.Services) != 0 {
				t.Fatalf("v0.5 fixture unexpectedly enrolled services: %+v", analysis.Report)
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				switch r.URL.Path {
				case "/v1/project-contract-registrations/" + projectID:
					if r.Method != http.MethodGet {
						t.Errorf("registration lookup method: %s", r.Method)
					}
					returnedID := projectID
					if test.mismatch {
						returnedID = "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
					}
					response.WriteJSON(w, http.StatusOK, response.Success("service.test", map[string]any{"project": map[string]any{"project": map[string]any{"project_id": returnedID, "slug": "webdav-project"}}}))
				case "/v1/service-registrations/" + action:
					if r.Method != http.MethodPost {
						t.Errorf("registration method: %s", r.Method)
					}
					var body struct {
						Registration serviceregistry.ProjectRegistrationInput `json:"registration"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					contract, err := projectcontracts.ParseServiceRegistrationContract([]byte(files[".loom/contracts/services/webdav.yaml"]))
					if err != nil {
						t.Error(err)
					}
					want := serviceregistry.ProjectRegistrationFromContract(projectID, serviceregistry.ProjectRegistrationScopeKey("webdav-project"), ".loom/contracts/services/webdav.yaml", contract)
					gotJSON, _ := json.Marshal(body.Registration)
					wantJSON, _ := json.Marshal(want)
					if !bytes.Equal(gotJSON, wantJSON) {
						t.Errorf("registration input: %s; want %s", gotJSON, wantJSON)
					}
					if test.backendError {
						response.WriteJSON(w, http.StatusBadRequest, failure)
						return
					}
					response.WriteJSON(w, http.StatusOK, response.Success("service.test", map[string]any{}))
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			args := []string{"--config", cfg, "service", "register", action, "--project", root, "--contract", "webdav"}
			if !test.human {
				args = append(args, "--json")
			}
			out, stderr, err := executeRootCommand(args...)
			if test.mismatch {
				if err == nil || !strings.Contains(err.Error(), "identity does not match") || requests != 1 {
					t.Fatalf("mismatched project: err=%v requests=%d out=%s stderr=%s", err, requests, out, stderr)
				}
				return
			}
			if test.backendError {
				if err == nil || requests != 2 {
					t.Fatalf("backend refusal: err=%v requests=%d out=%s stderr=%s", err, requests, out, stderr)
				}
				if test.human {
					for _, want := range []string{failure.Error.Code, failure.Error.Summary, failure.Error.Hint, failure.Meta.CorrelationID} {
						if !strings.Contains(stderr, want) {
							t.Errorf("stderr missing %q: %s", want, stderr)
						}
					}
				} else {
					var got response.ErrorEnvelope
					if err := json.Unmarshal([]byte(out), &got); err != nil || !reflect.DeepEqual(got, failure) {
						t.Errorf("backend error envelope lost: err=%v out=%s want=%+v", err, out, failure)
					}
				}
				return
			}
			if err != nil || requests != 2 {
				t.Fatalf("%s: err=%v requests=%d out=%s stderr=%s", action, err, requests, out, stderr)
			}
		})
	}
}

func TestServiceLifecycleRequiresYesBeforeTransport(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"service", "restart", "main@example"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("restart error=%v", err)
	}
}
func TestServiceLogsRejectOversizedSelectorBeforeTransport(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"service", "logs", "main@example", "--lines", "501"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "bounded maximum") {
		t.Fatalf("logs error=%v", err)
	}
}
func TestServiceLogsRejectNegativeSelectorsBeforeTransport(t *testing.T) {
	for _, flag := range []string{"--lines", "--max-bytes", "--max-age-seconds"} {
		cmd := NewRootCommand()
		cmd.SetArgs([]string{"service", "logs", "main@example", flag, "-1"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "bounded maximum") {
			t.Fatalf("%s error=%v", flag, err)
		}
	}
}
func TestServiceHumanRenderingSeparatesStates(t *testing.T) {
	var output bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&output)
	now := time.Now().UTC()
	renderServiceInspection(cmd, serviceregistry.ServiceInspection{ServiceListItem: serviceregistry.ServiceListItem{DisplayName: "Example", ProviderAddress: "main@example", RegistryState: serviceregistry.ProviderStateActive, ProcessState: serviceregistry.ProcessStateStopped, HealthStatus: "degraded", AvailabilityStatus: "unavailable", Provisioning: "external", LastObservedAt: &now}, RuntimeProfile: serviceregistry.RuntimeProfile{Manager: serviceregistry.ManagerSystemd, Unit: "example.service", Operations: []serviceregistry.Operation{serviceregistry.OperationStatus}}})
	text := output.String()
	for _, want := range []string{"Registry state: active", "Observed process state: stopped", "Provisioning authority: external"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
}
