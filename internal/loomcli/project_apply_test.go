package loomcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"loom.local/loom/internal/ids"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/localclient"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/response"
)

func TestProjectDeclarationNextActionCauseMatrix(t *testing.T) {
	input := pc.DeclarationApplyRequest{ProjectRef: "/owner/quote's project", NodeRef: "selected", PlanID: "reviewed", IdempotencyKey: "original", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile, pc.DeclarationProjections}, ApprovalRefs: []string{"exact'approval"}}
	for _, tt := range []struct {
		name            string
		state           pc.DeclarationOperationState
		code            pc.DeclarationErrorCode
		cause           string
		retry, custody  bool
		want, forbidden string
	}{
		{"queued", pc.DeclarationOperationQueued, "", "", false, true, " --resume 'durable'", ""},
		{"running", pc.DeclarationOperationRunning, "", "", false, true, " --resume 'durable'", ""},
		{"uncertain", pc.DeclarationOperationPartial, pc.DeclarationOwnerFailed, "observation_uncertain", false, true, " --resume 'durable'", ""},
		{"pending", pc.DeclarationOperationPartial, pc.DeclarationOwnerFailed, "owner_pending", false, true, " --resume 'durable'", ""},
		{"retryable", pc.DeclarationOperationFailed, pc.DeclarationOwnerFailed, "transient", true, true, " --resume 'durable'", ""},
		{"denied", pc.DeclarationOperationFailed, pc.DeclarationUnauthorized, "permission_revoked", false, true, "permission", " --resume "},
		{"approval", pc.DeclarationOperationPartial, pc.DeclarationApprovalRequired, "approval_missing", false, true, "approval", " --resume "},
		{"unsupported", pc.DeclarationOperationPartial, pc.DeclarationUnsupported, "application_owner_required", false, true, "prerequisite", " --resume "},
		{"failed", pc.DeclarationOperationFailed, pc.DeclarationOwnerFailed, "permanent", false, true, "Inspect", " --resume "},
		{"failed_without_cause", pc.DeclarationOperationFailed, "", "", false, true, "Inspect", " --resume "},
		{"stale", pc.DeclarationOperationPartial, pc.DeclarationPlanStale, "source_changed", false, true, "loom project plan", "idempotency-key"},
		{"identity", pc.DeclarationOperationFailed, pc.DeclarationIdentityConflict, "identity_changed", false, true, "loom project plan", "idempotency-key"},
		{"succeeded", pc.DeclarationOperationSucceeded, "", "", false, true, "loom project status", " --resume "},
		{"superseded", pc.DeclarationOperationSuperseded, pc.DeclarationPlanStale, "source_changed", false, true, "loom project status", "loom project apply"},
		{"no_custody", pc.DeclarationOperationPartial, pc.DeclarationOwnerFailed, "owner_pending", true, false, "loom project", "loom project apply"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := pc.DeclarationResult{OperationID: "durable", State: tt.state, Target: contextFixture().Target, Actions: []pc.DeclarationActionResult{{EffectRef: "retained-effect", Error: &pc.DeclarationError{Code: tt.code, CauseCode: tt.cause, Retryable: tt.retry}}}}
			if tt.code != "" {
				result.Errors = []pc.DeclarationError{{Code: tt.code, CauseCode: tt.cause, Retryable: tt.retry, CompletedEffects: []string{"earlier-effect"}}}
			} else {
				result.Actions[0].Error = nil
			}
			var req *pc.DeclarationApplyRequest
			if tt.custody {
				req = &input
			}
			var out bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&out)
			if err := renderDeclarationResult(cmd, &options{}, result, req); err != nil {
				t.Fatal(err)
			}
			text := out.String()
			if !strings.Contains(text, tt.want) || tt.forbidden != "" && strings.Contains(text, tt.forbidden) {
				t.Fatalf("wrong advice: %s", text)
			}
			if !strings.Contains(text, "retained-effect") || tt.code != "" && (!strings.Contains(text, "earlier-effect") || !strings.Contains(text, tt.cause)) {
				t.Fatalf("cause/effects lost: %s", text)
			}
			if strings.Contains(text, " --resume ") {
				for _, part := range []string{`--node 'selected'`, `--plan-id 'reviewed'`, `--idempotency-key 'original'`, `--refresh-projections`, `--approval 'exact'"'"'approval'`} {
					if !strings.Contains(text, part) {
						t.Fatalf("request custody lost %s: %s", part, text)
					}
				}
			}
		})
	}
	for _, selector := range []string{"bad\nINJECTED", "bad\x1b[31m", "--help", strings.Repeat("x", 8192)} {
		input.ProjectRef = selector
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&out)
		_ = renderDeclarationResult(cmd, &options{}, pc.DeclarationResult{OperationID: "durable", State: pc.DeclarationOperationQueued}, &input)
		if strings.Contains(out.String(), "loom project apply") || !strings.Contains(out.String(), "Inspect") {
			t.Fatalf("unsafe advice: %s", out.String())
		}
	}
}

func TestProjectDeclarationErrorDetailWithoutResult(t *testing.T) {
	detail := pc.DeclarationError{Code: pc.DeclarationUnauthorized, CauseCode: "permission_revoked", Retryable: false, CompletedEffects: []string{"retained-effect"}, Message: "unavailable authority"}
	standard := response.ErrorEnvelope{Error: response.ErrorBody{Code: string(detail.Code), Summary: detail.Message, Hint: "Inspect prerequisites", CorrelationID: "exact-correlation"}, Meta: response.Meta{CorrelationID: "exact-correlation", IdempotencyKey: "exact-key", Source: "main-authoritative", Freshness: "live"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, 403, struct {
			response.ErrorEnvelope
			Detail pc.DeclarationError `json:"declaration_error"`
		}{standard, detail})
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	out, _, err := executeRootCommand("--config", cfg, "--json", "project", "apply", "selected", "--node", "owner", "--plan-id", "reviewed", "--idempotency-key", "exact-key")
	var typed *localclient.DeclarationRequestError
	if err == nil {
		t.Fatal("missing failure exit")
	}
	if !errors.As(err, &typed) {
		t.Fatal("typed error no longer returned")
	}
	var got struct {
		response.ErrorEnvelope
		Detail pc.DeclarationError `json:"declaration_error"`
	}
	d := json.NewDecoder(strings.NewReader(out))
	if d.Decode(&got) != nil || !reflect.DeepEqual(got.ErrorEnvelope, standard) || !reflect.DeepEqual(got.Detail, detail) {
		t.Fatalf("error detail lost: %s", out)
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		t.Fatal("second JSON document")
	}
	_, stderr, err := executeRootCommand("--config", cfg, "project", "apply", "selected", "--node", "owner", "--plan-id", "reviewed", "--idempotency-key", "exact-key")
	if err == nil || !strings.Contains(stderr, "exact-correlation") || !strings.Contains(stderr, "exact-key") || !strings.Contains(stderr, string(detail.Code)) {
		t.Fatalf("human error metadata lost: %v %s", err, stderr)
	}
}

func TestProjectDeclarationPreflightJSONAndHuman(t *testing.T) {
	detail := pc.DeclarationError{Code: pc.DeclarationTargetUnavailable, CauseCode: "application_preflight_blocked", Message: "Application prerequisites are incomplete.", CompletedEffects: []string{}, Preflight: []pc.DeclarationPreflightIssue{
		{Resource: "webdav", Field: "grant", State: "missing", Code: "application_grant_required", Message: "No runtime grant exists."},
		{Resource: "webdav", Field: "credentials", State: "missing", Code: "application_credential_binding_required", Message: "Credential materialization is not connected yet."},
	}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/project-declaration-plans" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		response.WriteJSON(w, 422, struct {
			response.ErrorEnvelope
			Detail pc.DeclarationError `json:"declaration_error"`
		}{response.ErrorEnvelope{Error: response.ErrorBody{Code: string(detail.Code), Summary: detail.Message, CorrelationID: "preflight-correlation"}}, detail})
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	out, _, err := executeRootCommand("--config", cfg, "--json", "project", "plan", "webdav", "--node", "main")
	var typed *localclient.DeclarationRequestError
	if !errors.As(err, &typed) || !reflect.DeepEqual(typed.Detail.Preflight, detail.Preflight) {
		t.Fatalf("local client dropped preflight: %v %s", err, out)
	}
	var responseBody struct {
		Detail pc.DeclarationError `json:"declaration_error"`
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	if decoder.Decode(&responseBody) != nil || !reflect.DeepEqual(responseBody.Detail, detail) {
		t.Fatal(out)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		t.Fatal("printed a second JSON document")
	}
	out, stderr, err := executeRootCommand("--config", cfg, "project", "plan", "webdav", "--node", "main")
	if err == nil || !strings.Contains(out, "webdav.grant [missing]") || !strings.Contains(out, "Credential materialization is not connected yet.") || !strings.Contains(stderr, "preflight-correlation") || strings.Contains(out, "loom project apply") {
		t.Fatalf("human preflight is incomplete or suggests applying: %v\n%s\n%s", err, out, stderr)
	}
}

func TestProjectDeclarationNextActionAdversarialCustody(t *testing.T) {
	target := contextFixture().Target
	input := pc.DeclarationApplyRequest{ProjectRef: "original", PlanID: "reviewed", IdempotencyKey: "key", Effects: declarationEffects(false)}
	stale := pc.DeclarationResult{OperationID: "durable", Target: target, State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationPlanStale}}}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	_ = renderDeclarationResult(cmd, &options{}, stale, &input)
	if !strings.Contains(out.String(), "--node 'verified-owner'") {
		t.Fatalf("same owner not fenced: %s", out.String())
	}
	for _, code := range []pc.DeclarationErrorCode{pc.DeclarationUnauthorized, pc.DeclarationApprovalRequired, pc.DeclarationUnsupported} {
		out.Reset()
		result := pc.DeclarationResult{OperationID: "durable", Target: target, State: pc.DeclarationOperationPartial}
		failure := &localclient.DeclarationRequestError{RequestError: &localclient.RequestError{Envelope: response.ErrorEnvelope{Error: response.ErrorBody{Code: string(code)}}}, Result: &result, Detail: pc.DeclarationError{Code: code, CauseCode: "server_detail", Retryable: true, CompletedEffects: []string{"outer-effect"}}}
		_ = renderDeclarationFailure(cmd, &options{}, "cid", failure, &input)
		if strings.Contains(out.String(), " --resume ") || !strings.Contains(out.String(), "server_detail") || !strings.Contains(out.String(), "outer-effect") {
			t.Fatalf("outer denial lost: %s", out.String())
		}
		if len(result.Errors) != 0 {
			t.Fatal("partial wire result mutated")
		}
	}
}

func TestProjectDeclarationCLISelectorsAndPartialResult(t *testing.T) {
	for _, status := range []int{202, 409} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			result := pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable-operation", PlanID: "reviewed", State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{ActionID: "registration", EffectRef: "committed"}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/project-declaration-operations" || r.Method != "POST" {
					t.Errorf("wrong route %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				var input pc.DeclarationApplyRequest
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input.ProjectRef != "/selected/project" || input.NodeRef != "selected-node" || input.PlanID != "reviewed" || input.IdempotencyKey != "original" || input.OperationID != "durable-operation" || !reflect.DeepEqual(input.Effects, []pc.DeclarationEffect{pc.DeclarationReconcile, pc.DeclarationProjections}) || !reflect.DeepEqual(input.ApprovalRefs, []string{"exact-approval"}) {
					t.Errorf("selector mismatch: %+v", input)
				}
				if status == 202 {
					response.WriteJSON(w, status, response.Success("pending", result))
				} else {
					response.WriteJSON(w, status, struct {
						response.ErrorEnvelope
						Data pc.DeclarationResult `json:"data"`
					}{response.ErrorEnvelope{Error: response.ErrorBody{Code: "declaration.plan_stale", Summary: "source_changed", CorrelationID: "server"}}, result})
				}
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			stdout, stderr, err := executeRootCommand("--config", cfg, "--json", "project", "apply", "/selected/project", "--node", "selected-node", "--plan-id", "reviewed", "--idempotency-key", "original", "--resume", "durable-operation", "--refresh-projections", "--approval", "exact-approval")
			if (err != nil) != (status == 409) {
				t.Fatalf("error=%v stderr=%s", err, stderr)
			}
			var got pc.DeclarationResult
			d := json.NewDecoder(strings.NewReader(stdout))
			if d.Decode(&got) != nil || !reflect.DeepEqual(got, result) {
				t.Fatalf("result changed: %s", stdout)
			}
			var extra any
			if d.Decode(&extra) != io.EOF {
				t.Fatalf("second JSON object: %s", stdout)
			}
			if strings.Contains(stdout+stderr, "transport.unavailable") {
				t.Fatal("typed partial error replaced")
			}
		})
	}
}
func TestProjectDeclarationCLIPlanStatusOperationAndHelp(t *testing.T) {
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.RequestURI)
		switch r.URL.Path {
		case "/v1/project-declaration-plans":
			var input pc.DeclarationPlanRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input.NodeRef != "selected" || input.ProjectRef != "remote" {
				t.Error("plan selector changed")
			}
			response.WriteJSON(w, 200, response.Success("plan", pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: "reviewed"}))
		case "/v1/projects/remote/declaration-status":
			response.WriteJSON(w, 200, response.Success("status", pc.DeclarationStatus{SchemaVersion: pc.DeclarationStatusSchemaV05}))
		case "/v1/project-declaration-operations/durable":
			response.WriteJSON(w, 200, response.Success("op", pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: "durable", State: pc.DeclarationOperationFailed}))
		default:
			t.Errorf("unexpected route %s", r.RequestURI)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	for _, args := range [][]string{{"plan", "remote", "--node", "selected"}, {"status", "remote", "--node", "selected"}, {"operation", "durable"}} {
		stdout, stderr, err := executeRootCommand(append([]string{"--config", cfg, "--json", "project"}, args...)...)
		if err != nil || !strings.Contains(stdout, "schema_version") {
			t.Fatalf("%v: %v %s %s", args, err, stdout, stderr)
		}
	}
	want := []string{"POST /v1/project-declaration-plans", "GET /v1/projects/remote/declaration-status?node_ref=selected", "GET /v1/project-declaration-operations/durable"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("routes=%v", calls)
	}
	for _, name := range []string{"plan", "apply", "operation", "status", "analyze", "registration-status", "register"} {
		stdout, _, err := executeRootCommand("project", name, "--help")
		if err != nil || !strings.Contains(stdout, "Usage:") {
			t.Fatalf("help %s: %v", name, err)
		}
	}
}
func TestProjectDeclarationCLINextActionPreservesOriginalRequest(t *testing.T) {
	input := pc.DeclarationApplyRequest{ProjectRef: "/tmp/quote's project", NodeRef: "selected", PlanID: "reviewed", IdempotencyKey: "original", OperationID: "durable", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile, pc.DeclarationProjections}, ApprovalRefs: []string{"exact"}}
	got := declarationApplyCommand(input)
	for _, part := range []string{`'/tmp/quote'"'"'s project'`, `--node 'selected'`, `--plan-id 'reviewed'`, `--idempotency-key 'original'`, `--resume 'durable'`, `--approval 'exact'`, `--refresh-projections`} {
		if !strings.Contains(got, part) {
			t.Fatalf("next action omitted %s: %s", part, got)
		}
	}
}

func TestProjectDeclarationCLIEndpointStatus(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	renderDeclarationEndpoint(cmd, &pc.ApplicationEndpointStatus{URL: "https://webdav.apps.example.com", DNS: "satisfied", TLS: "pending", Routing: "pending"})
	for _, want := range []string{"https://webdav.apps.example.com", "DNS: satisfied", "TLS: pending", "Route: pending"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("endpoint detail missing", out.String())
		}
	}
}

func TestProjectDeclarationCLILocalSourceSelectsNewWorkflow(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".loom"), 0700); err != nil {
		t.Fatal(err)
	}
	source := `{"kind":"loom.project","schema_version":"project.contract.v0.5","project":{"id":"` + ids.NewProjectID() + `","slug":"local-fixture","name":"Local fixture","owner_node":"main","status":"active"},"resources":{}}`
	if err := os.WriteFile(filepath.Join(root, pc.CanonicalRootContractPath), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/project-declaration-plans" {
			t.Errorf("wrong version route %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		var input pc.DeclarationPlanRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.ProjectRef != root {
			t.Errorf("local folder was not resolved: %s", input.ProjectRef)
		}
		response.WriteJSON(w, 200, response.Success("v05", pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: "reviewed"}))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	t.Chdir(root)
	for _, extra := range [][]string{nil, {"--refresh-projections"}} {
		args := append([]string{"--config", cfg, "--json", "project", "plan", "."}, extra...)
		stdout, stderr, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(stdout, "reviewed") {
			t.Fatalf("v0.5 source selected legacy: %v %s %s", err, stdout, stderr)
		}
	}
}
