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

func TestProjectPlanNamedActionsBounded(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	actions := []pc.DeclarationAction{{ID: "register-project", Kind: pc.DeclarationRegisterProject}, {ID: "notes-enrollment", Kind: pc.DeclarationEnrollKnowledge, Resource: "knowledge"}, {ID: "unsafe\ncommand", Kind: pc.DeclarationRegisterProject}}
	input := pc.DeclarationPlanRequest{ProjectRef: "project", NodeRef: "main", Effects: []pc.DeclarationEffect{pc.DeclarationWatches}}
	renderDeclarationPlanActions(cmd, actions, input)
	for _, want := range []string{"register-project (register_project)", "notes-enrollment (enroll_knowledge)", "Review actions omitted: 1", "loom project plan 'project' --json --node 'main' --watches-only"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "unsafe\ncommand") {
		t.Fatal("unsafe review item rendered")
	}
}

func TestWatchSelectionFailureReviewsNormalPlanWithoutReusingApply(t *testing.T) {
	input := pc.DeclarationApplyRequest{ProjectRef: "project", NodeRef: "main", Effects: []pc.DeclarationEffect{pc.DeclarationWatches}, PlanID: "old", IdempotencyKey: "original"}
	result := pc.DeclarationResult{State: pc.DeclarationOperationFailed, Errors: []pc.DeclarationError{{Code: pc.DeclarationPlanStale, CauseCode: "watch_configuration_binding_changed"}}}
	a := declarationResultNextAction(result, &input)
	if a.Kind != "replan" || strings.Contains(a.Command, "--watches-only") || strings.Contains(a.Command, " apply ") || !strings.Contains(a.Command, "loom project plan 'project'") || !strings.Contains(a.Command, "--node 'main'") {
		t.Fatalf("incorrect broader-plan guidance: %+v", a)
	}
	if input.Effects[0] != pc.DeclarationWatches || input.PlanID != "old" {
		t.Fatal("original reviewed request was mutated")
	}
	a = declarationResultNextAction(result, nil)
	if !strings.Contains(a.Reason, "without --watches-only") {
		t.Fatalf("missing no-request guidance: %+v", a)
	}
}

func TestProjectDeclarationWatchesOnlyCLIAndNextCommands(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request pc.DeclarationPlanRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(request.Effects, []pc.DeclarationEffect{pc.DeclarationWatches}) {
			t.Errorf("watch-only scope lost: %+v", request)
		}
		if r.URL.Path == "/v1/project-declaration-plans" {
			response.WriteJSON(w, 200, response.Success("plan", pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05, PlanID: "reviewed", Basis: pc.DeclarationPlanBasis{Target: contextFixture().Target}}))
		} else if r.URL.Path == "/v1/project-declaration-operations" {
			response.WriteJSON(w, 200, response.Success("apply", pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, State: pc.DeclarationOperationSucceeded}))
		} else {
			t.Errorf("wrong route: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	for _, args := range [][]string{
		{"plan", "fixture", "--node", "main", "--watches-only"},
		{"apply", "fixture", "--node", "main", "--watches-only", "--plan-id", "reviewed", "--idempotency-key", "original", "--resume", "durable"},
	} {
		_, stderr, err := executeRootCommand(append([]string{"--config", cfg, "--json", "project"}, args...)...)
		if err != nil {
			t.Fatal(stderr, err)
		}
	}
	input := pc.DeclarationApplyRequest{ProjectRef: "fixture", NodeRef: "main", PlanID: "reviewed", IdempotencyKey: "original", Effects: []pc.DeclarationEffect{pc.DeclarationWatches}}
	if !declarationRequestCustody(input) || !strings.Contains(declarationApplyCommand(input), "--watches-only") || !strings.Contains(declarationReplanAction(input, contextFixture().Target, "").Command, "--watches-only") {
		t.Fatal("rendered continuation lost watch-only scope")
	}
	if _, _, err := executeRootCommand("project", "plan", "fixture", "--watches-only", "--refresh-projections"); err == nil {
		t.Fatal("mixed effects accepted")
	}
}

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
		{"accepted_pending", pc.DeclarationOperationPartial, pc.DeclarationOwnerPending, "owner_pending", false, true, "Accepted; waiting", ""},
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

func TestProjectDeclarationStatusAdviceUsesCurrentBlockersAndPartialState(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		change                func(*pc.DeclarationStatus)
		kind, command, reason string
	}{
		{"historical_success", func(*pc.DeclarationStatus) {}, "none", "", "independently"},
		{"no_history", func(s *pc.DeclarationStatus) { s.Operation = nil }, "none", "", "independently"},
		{"current_owner_blocker", func(s *pc.DeclarationStatus) {
			r := s.Resources["webdav"]
			r.Errors = []pc.DeclarationError{{Code: pc.DeclarationUnauthorized, CauseCode: "permission_revoked"}}
			s.Resources["webdav"] = r
		}, "inspect", "loom project status", "permission"},
		{"current_partial_resource", func(s *pc.DeclarationStatus) {
			r := s.Resources["webdav"]
			r.Readiness.Applied.State = pc.DeclarationPending
			s.Resources["webdav"] = r
		}, "inspect", "loom project status", "resource blockers"},
		{"current_publication_blocker", func(s *pc.DeclarationStatus) { s.Resources["webdav"].Readiness.Endpoint.TLS = "pending" }, "inspect", "loom project status", "publication"},
		{"unfinished_operation", func(s *pc.DeclarationStatus) {
			s.Operation.State = pc.DeclarationOperationPartial
			s.Operation.Errors = []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed, CompletedEffects: []string{"retained-effect"}}}
		}, "inspect", "loom project operation", "original request"},
		{"recovery_completed", func(s *pc.DeclarationStatus) {
			s.Errors = []pc.DeclarationError{{Code: pc.DeclarationTargetUnavailable, CauseCode: "project_restored_inactive", Retryable: true, CompletedEffects: []string{"restored-files"}}}
		}, "inspect", "loom project status", "Recovery completed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := webDAVContextFixture()
			tt.change(&s)
			a := declarationStatusNextAction(s)
			if a.Kind != tt.kind || !strings.Contains(a.Command, tt.command) || !strings.Contains(a.Reason, tt.reason) || strings.Contains(a.Command, "loom project apply") || strings.Contains(a.Command, "--resume") || (tt.kind == "none" && a.Command != "") {
				t.Fatalf("wrong current advice: %+v", a)
			}
			if tt.name == "unfinished_operation" {
				view := newProjectContext(s)
				if view.HistoricalOperation == nil || !reflect.DeepEqual(view.HistoricalOperation.CompletedEffects, []string{"retained-effect"}) {
					t.Fatalf("completed effects lost: %+v", view)
				}
			}
		})
	}
}

func TestProjectDeclarationRecoveryCompletedIsNotRetryableAdvice(t *testing.T) {
	input := pc.DeclarationApplyRequest{ProjectRef: "project", NodeRef: "owner", PlanID: "plan", IdempotencyKey: "key", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}}
	result := pc.DeclarationResult{OperationID: "recovery", State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationTargetUnavailable, CauseCode: "project_restored_inactive", Retryable: true, CompletedEffects: []string{"restored-files"}}}, Actions: []pc.DeclarationActionResult{{EffectRef: "restored-files"}, {EffectRef: "retained-installation"}}}
	a := declarationResultNextAction(result, &input)
	if a.Kind != "inspect" || strings.Contains(a.Command, "apply") || !strings.Contains(a.Reason, "Recovery completed") || !reflect.DeepEqual(declarationCompletedEffects(result), []string{"restored-files", "retained-installation"}) {
		t.Fatalf("recovery retry/effects: %+v", a)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderDeclarationResult(cmd, &options{}, result, &input); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Completed effect: restored-files") || !strings.Contains(out.String(), "No automatic") && !strings.Contains(out.String(), "do not retry completed effects") {
		t.Fatalf("recovery output: %s", out.String())
	}
}

func TestProjectDeclarationMissingSelectorHint(t *testing.T) {
	for _, tt := range []struct {
		name   string
		code   pc.DeclarationErrorCode
		cause  string
		lookup bool
	}{
		{"confirmed_missing", pc.DeclarationReferenceMissing, "project_missing", true},
		{"unavailable_location", pc.DeclarationTargetUnavailable, "registered_project_location_required", false},
		{"wrong_owner", pc.DeclarationTargetUnavailable, "remote_source_owner_required", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			detail := pc.DeclarationError{Code: tt.code, CauseCode: tt.cause, CompletedEffects: []string{}}
			envelope := response.ErrorEnvelope{Error: response.ErrorBody{Code: string(tt.code), Target: "declaration", Summary: "Declaration lookup refused", Hint: "Server hint", CorrelationID: "server-correlation"}, Meta: response.Meta{CorrelationID: "server-correlation"}}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				response.WriteJSON(w, 404, struct {
					response.ErrorEnvelope
					Detail pc.DeclarationError `json:"declaration_error"`
				}{envelope, detail})
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			for _, command := range [][]string{
				{"context", "missing-selector", "--node", "selected-owner"},
				{"status", "missing-selector", "--node", "selected-owner"},
				{"plan", "missing-selector", "--node", "selected-owner"},
				{"apply", "missing-selector", "--node", "selected-owner", "--plan-id", "reviewed", "--idempotency-key", "original"},
			} {
				for _, jsonMode := range []bool{false, true} {
					args := append([]string{"--config", cfg, "project"}, command...)
					if jsonMode {
						args = append(args, "--json")
					}
					before := calls
					out, stderr, err := executeRootCommand(args...)
					if err == nil || calls != before+1 || strings.Contains(out, "loom project list") != tt.lookup {
						t.Fatalf("hint/fanout: err=%v %s %s", err, out, stderr)
					}
					if tt.lookup && !strings.Contains(out, "missing-selector") {
						t.Fatalf("submitted selector hidden: %s", out)
					}
					if jsonMode {
						var got struct {
							response.ErrorEnvelope
							Detail   pc.DeclarationError    `json:"declaration_error"`
							Selector string                 `json:"requested_selector"`
							Next     *declarationNextAction `json:"next_action"`
						}
						d := json.NewDecoder(strings.NewReader(out))
						if e := d.Decode(&got); e != nil {
							t.Fatal(e)
						}
						var extra any
						if d.Decode(&extra) != io.EOF || !reflect.DeepEqual(got.ErrorEnvelope, envelope) || !reflect.DeepEqual(got.Detail, detail) || (got.Next != nil) != tt.lookup {
							t.Fatalf("envelope altered: %s", out)
						}
					}
				}
			}
		})
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

func TestProjectDeclarationPendingJSONIncludesExactContinuation(t *testing.T) {
	input := pc.DeclarationApplyRequest{ProjectRef: "/chosen/project", NodeRef: "main", PlanID: "reviewed", IdempotencyKey: "original", Effects: []pc.DeclarationEffect{pc.DeclarationWatches}}
	result := pc.DeclarationResult{OperationID: "durable", State: pc.DeclarationOperationPartial, Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerPending, CauseCode: "owner_pending"}}}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := renderDeclarationResult(cmd, &options{jsonOutput: true}, result, &input); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Activity string                `json:"activity"`
		Next     declarationNextAction `json:"next_action"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Activity != "waiting_for_owner" {
		t.Fatalf("result=%s error=%v", out.String(), err)
	}
	for _, part := range []string{"--resume 'durable'", "--idempotency-key 'original'", "--plan-id 'reviewed'", "--watches-only"} {
		if !strings.Contains(got.Next.Command, part) {
			t.Fatalf("continuation lost %s: %+v", part, got.Next)
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

func TestProjectDeclarationStatusResourceFirstWithoutNewReadsOrJSON(t *testing.T) {
	status := webDAVContextFixture()
	status.Operation.Effects = []pc.DeclarationEffect{pc.DeclarationWatches}
	status.Operation.Readiness.Protected = pc.DeclarationFact{State: pc.DeclarationFailed, Revision: "stale-operation-evidence"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.RequestURI != "/v1/projects/caller/declaration-status?node_ref=selected" {
			t.Errorf("unexpected read/mutation: %s %s", r.Method, r.RequestURI)
		}
		response.WriteJSON(w, 200, response.Success("status", status))
	}))
	defer server.Close()
	cfg := writeProjectBackendInstallManifest(t, server.URL)
	for _, jsonMode := range []bool{false, true} {
		calls = 0
		args := []string{"--config", cfg, "project", "status", "caller", "--node", "selected"}
		if jsonMode {
			args = append(args, "--json")
		}
		out, stderr, err := executeRootCommand(args...)
		if err != nil || calls != 1 {
			t.Fatalf("status reads=%d err=%v %s", calls, err, stderr)
		}
		if jsonMode {
			var got, want map[string]any
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(status)
			_ = json.Unmarshal(raw, &want)
			want["readiness_note"] = declarationReadinessNote
			if !reflect.DeepEqual(got, want) {
				t.Fatal("status JSON contract changed", out)
			}
			continue
		}
		for _, want := range []string{
			"Resource: webdav (application); applied=satisfied@installation-revision; healthy=satisfied@generation-revision; protected=satisfied@backup-manifest-revision; verified=unknown",
			"Resource detail: loom project status 'verified-project' --node 'verified-owner' --json",
			"Current whole-declaration evidence:", "Healthy: unknown | Protected: unknown | Verified: unknown",
			"Last operation: old-operation (succeeded); revision old-revision", "Operation evidence: watch_configuration_only",
			"No current deployment action is established.", declarationReadinessNote,
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("missing %q: %s", want, out)
			}
		}
		if strings.Index(out, "Resource: webdav") > strings.Index(out, "Resource: files") || strings.Index(out, "Resource detail:") > strings.Index(out, "Current whole-declaration evidence:") || strings.Contains(out, "stale-operation-evidence") {
			t.Fatal("resource priority or historical separation lost", out)
		}
	}

	failed := status.Resources["webdav"]
	failed.Readiness.Healthy = pc.DeclarationFact{State: pc.DeclarationFailed, Revision: "failed-generation"}
	status.Resources["broken-app"] = failed
	status.Resources["zz-omitted-error"] = pc.DeclarationResourceStatus{Errors: []pc.DeclarationError{{Code: pc.DeclarationOwnerFailed, CauseCode: "exact_current_owner_failure"}}}
	out, stderr, err := executeRootCommand("--config", cfg, "project", "status", "caller", "--node", "selected")
	for _, want := range []string{"Resource: broken-app", "healthy=failed@failed-generation", "Resource: webdav", "Omitted: resources=1", "Cause: exact_current_owner_failure (declaration.owner_failed)", "Next: Inspect the request"} {
		if err != nil || !strings.Contains(out, want) {
			t.Fatalf("mixed/omitted failure hidden, want %q: %v %s %s", want, err, out, stderr)
		}
	}
	if strings.Contains(out, "Resource: zz-omitted-error") {
		t.Fatal("fixture did not exercise an omitted resource's cause", out)
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
