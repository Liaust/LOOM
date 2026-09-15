package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projectapply"
	pc "loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

func TestProjectDeclarationApplicationPreflight(t *testing.T) {
	f := &projectapply.Failure{Code: pc.DeclarationTargetUnavailable, Cause: "application_preflight_blocked", Preflight: []pc.DeclarationPreflightIssue{
		{Resource: "webdav", Field: "grant", Code: "application_grant_required"},
		{Resource: "webdav", Field: "credentials", Code: "application_credential_binding_required"},
	}}
	service := &declarationServiceStub{err: f}
	h, _ := declarationTestHandler(service)
	w := declarationHTTP(t, h, "POST", "/v1/project-declaration-plans", declarationJSONTest(t, pc.DeclarationPlanRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "webdav"}))
	var result ProjectDeclarationErrorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 422 || result.DeclarationError.Retryable || result.Data != nil || service.calls != 1 || !reflect.DeepEqual(result.DeclarationError.Preflight, f.PublicPreflight()) || result.Error.CorrelationID == "" {
		t.Fatalf("preflight lost or treated as execution: status=%d body=%s", w.Code, w.Body)
	}
}

type declarationServiceStub struct {
	calls     int
	principal projectapply.Principal
	plan      pc.DeclarationPlanRequest
	apply     pc.DeclarationApplyRequest
	result    pc.DeclarationResult
	err       error
}

func (s *declarationServiceStub) Plan(_ context.Context, p projectapply.Principal, r pc.DeclarationPlanRequest) (pc.DeclarationPlan, error) {
	s.calls++
	s.principal = p
	s.plan = r
	return pc.DeclarationPlan{SchemaVersion: pc.DeclarationPlanSchemaV05}, s.err
}
func (s *declarationServiceStub) Apply(_ context.Context, p projectapply.Principal, r pc.DeclarationApplyRequest) (pc.DeclarationResult, error) {
	s.calls++
	s.principal = p
	s.apply = r
	return s.result, s.err
}
func (s *declarationServiceStub) Operation(_ context.Context, p projectapply.Principal, _ string) (pc.DeclarationResult, error) {
	s.calls++
	s.principal = p
	return s.result, s.err
}
func (s *declarationServiceStub) Status(_ context.Context, p projectapply.Principal, r pc.DeclarationPlanRequest) (pc.DeclarationStatus, error) {
	s.calls++
	s.principal = p
	s.plan = r
	return pc.DeclarationStatus{SchemaVersion: pc.DeclarationStatusSchemaV05}, s.err
}
func declarationTestHandler(service ProjectDeclarationService) (http.Handler, projectapply.Principal) {
	p := projectapply.Principal{ActorID: ids.NewActorID(), OriginNodeID: ids.NewNodeID()}
	server := NewServer(Services{ProjectDeclaration: service, ProjectDeclarationRequestResolver: func(context.Context, string) (requestctx.Context, error) {
		return requestctx.Context{ActorID: p.ActorID, OriginNodeID: p.OriginNodeID}, nil
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return server.Handler(), p
}
func declarationHTTP(t *testing.T, h http.Handler, method, path, raw string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(raw))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func declarationTestApply() pc.DeclarationApplyRequest {
	return pc.DeclarationApplyRequest{SchemaVersion: pc.DeclarationRequestSchemaV05, ProjectRef: "/tmp/project", NodeRef: "main", Effects: []pc.DeclarationEffect{pc.DeclarationReconcile}, PlanID: "sha256:" + strings.Repeat("a", 64), IdempotencyKey: "original"}
}
func declarationJSONTest(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestProjectDeclarationHTTPAdmission(t *testing.T) {
	valid := `{"schema_version":"` + pc.DeclarationRequestSchemaV05 + `","project_ref":"/tmp/project"}`
	cases := []struct{ name, method, path, body string }{
		{"unknown", "POST", "/v1/project-declaration-plans", strings.TrimSuffix(valid, "}") + `,"actor_id":"injected"}`},
		{"duplicate", "POST", "/v1/project-declaration-plans", strings.TrimSuffix(valid, "}") + `,"project_ref":"other"}`},
		{"case", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "project_ref", "Project_Ref", 1)},
		{"trailing", "POST", "/v1/project-declaration-plans", valid + ` {}`},
		{"oversized", "POST", "/v1/project-declaration-plans", strings.Repeat(" ", declarationBodyLimit) + valid},
		{"null", "POST", "/v1/project-declaration-plans", strings.Replace(valid, `"/tmp/project"`, `null`, 1)},
		{"array", "POST", "/v1/project-declaration-plans", "[]"},
		{"effects_empty", "POST", "/v1/project-declaration-plans", strings.TrimSuffix(valid, "}") + `,"effects":[]}`},
		{"effects_duplicate", "POST", "/v1/project-declaration-plans", strings.TrimSuffix(valid, "}") + `,"effects":["reconcile","reconcile"]}`},
		{"effects_unknown", "POST", "/v1/project-declaration-plans", strings.TrimSuffix(valid, "}") + `,"effects":["run_shell"]}`},
		{"traversal", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "/tmp/project", "/tmp/../project", 1)},
		{"relative_path", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "/tmp/project", "../project", 1)},
		{"invalid_utf8", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "/tmp/project", string([]byte{255}), 1)},
		{"selector", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "/tmp/project", " ", 1)},
		{"id", "POST", "/v1/project-declaration-plans", strings.Replace(valid, "/tmp/project", "project_fake", 1)},
		{"query", "POST", "/v1/project-declaration-plans?actor_id=owner", valid},
		{"status_query", "GET", "/v1/projects/example/declaration-status?actor_id=owner", ""},
		{"status_duplicate", "GET", "/v1/projects/example/declaration-status?node_ref=main&node_ref=elsewhere", ""},
		{"status_body", "GET", "/v1/projects/example/declaration-status", valid},
		{"operation_id", "GET", "/v1/project-declaration-operations/job_fake", ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			svc := &declarationServiceStub{}
			h, _ := declarationTestHandler(svc)
			w := declarationHTTP(t, h, tt.method, tt.path, tt.body)
			if w.Code != 422 || svc.calls != 0 {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, svc.calls, w.Body)
			}
		})
	}
	for _, change := range []func(*pc.DeclarationApplyRequest){func(r *pc.DeclarationApplyRequest) { r.Effects = nil }, func(r *pc.DeclarationApplyRequest) { r.PlanID = "fake" }, func(r *pc.DeclarationApplyRequest) { r.IdempotencyKey = "" }, func(r *pc.DeclarationApplyRequest) { r.OperationID = "job_fake" }, func(r *pc.DeclarationApplyRequest) { r.ApprovalRefs = []string{"a", "a"} }} {
		r := declarationTestApply()
		change(&r)
		svc := &declarationServiceStub{}
		h, _ := declarationTestHandler(svc)
		w := declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, r))
		if w.Code != 422 || svc.calls != 0 {
			t.Fatalf("invalid apply dispatched: %s", w.Body)
		}
	}
	svc := &declarationServiceStub{}
	h, p := declarationTestHandler(svc)
	w := declarationHTTP(t, h, "POST", "/v1/project-declaration-plans", valid)
	if w.Code != 200 || svc.principal != p {
		t.Fatalf("identity not server resolved: %d %+v", w.Code, svc.principal)
	}
	w = declarationHTTP(t, h, "GET", "/v1/projects/"+url.PathEscape("/tmp/my project")+"/declaration-status?node_ref=main", "")
	if w.Code != 200 || svc.plan.ProjectRef != "/tmp/my project" || svc.plan.NodeRef != "main" {
		t.Fatalf("status selector: %d %+v", w.Code, svc.plan)
	}
}
func TestProjectDeclarationHTTPErrorsAndDurableResults(t *testing.T) {
	for _, tt := range []struct {
		code   pc.DeclarationErrorCode
		cause  string
		status int
	}{{pc.DeclarationOwnerFailed, "owner_pending", 202}, {pc.DeclarationOwnerFailed, "observation_uncertain", 503}, {pc.DeclarationPlanStale, "source_changed", 409}, {pc.DeclarationUnauthorized, "permission_revoked", 403}, {pc.DeclarationInvalid, "invalid_selector", 422}, {pc.DeclarationUnsupported, "application_owner_required", 422}} {
		t.Run(tt.cause, func(t *testing.T) {
			result := pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: ids.NewJobID(), State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{ActionID: "registration", EffectRef: "receipt", State: pc.DeclarationOperationSucceeded}}}
			svc := &declarationServiceStub{result: result, err: declarationFailure(tt.code, tt.cause)}
			h, _ := declarationTestHandler(svc)
			w := declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, declarationTestApply()))
			var env response.Envelope[pc.DeclarationResult]
			if json.Unmarshal(w.Body.Bytes(), &env) != nil || w.Code != tt.status || env.Data.OperationID != result.OperationID || env.Data.Actions[0].EffectRef != "receipt" || env.Meta.IdempotencyKey != "original" {
				t.Fatalf("result lost: %d %s", w.Code, w.Body)
			}
		})
	}
	svc := &declarationServiceStub{err: errors.New("secret /private/path token")}
	h, _ := declarationTestHandler(svc)
	w := declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, declarationTestApply()))
	if strings.Contains(w.Body.String(), "secret") || w.Code != 503 {
		t.Fatalf("raw cause leak: %s", w.Body)
	}
	for _, err := range []error{declarationFailure(pc.DeclarationUnauthorized, "denied"), declarationFailure(pc.DeclarationReferenceMissing, "operation_missing")} {
		svc.err = err
		w = declarationHTTP(t, h, "GET", "/v1/project-declaration-operations/"+ids.NewJobID(), "")
		var env ProjectDeclarationErrorEnvelope
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != 404 || env.DeclarationError.CauseCode != "operation_missing" {
			t.Fatalf("operation existence leaked: %s", w.Body)
		}
	}
	svc.err = nil
	svc.result = pc.DeclarationResult{State: pc.DeclarationOperationFailed, OperationID: ids.NewJobID()}
	w = declarationHTTP(t, h, "GET", "/v1/project-declaration-operations/"+svc.result.OperationID, "")
	if w.Code != 200 {
		t.Fatal("historical failure must be readable")
	}
	for _, svc := range []ProjectDeclarationService{nil, (*projectapply.Service)(nil)} {
		h, _ := declarationTestHandler(svc)
		w = declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, declarationTestApply()))
		if w.Code != 503 {
			t.Fatalf("nil service status=%d", w.Code)
		}
	}
}

func TestProjectDeclarationHTTPNextActionCauseMatrix(t *testing.T) {
	for _, tt := range []struct {
		code        pc.DeclarationErrorCode
		cause, want string
		resume      bool
	}{
		{pc.DeclarationUnauthorized, "permission_revoked", "permission", false},
		{pc.DeclarationApprovalRequired, "approval_missing", "approval", false},
		{pc.DeclarationUnsupported, "application_owner_required", "prerequisite", false},
		{pc.DeclarationUnsupported, "core_not_configured", "prerequisite", false},
		{pc.DeclarationPlanStale, "source_changed", "same owner", false},
		{pc.DeclarationIdentityConflict, "identity_changed", "same owner", false},
		{pc.DeclarationOwnerFailed, "observation_uncertain", "original", true},
	} {
		t.Run(tt.cause, func(t *testing.T) {
			result := pc.DeclarationResult{SchemaVersion: pc.DeclarationResultSchemaV05, OperationID: ids.NewJobID(), State: pc.DeclarationOperationPartial, Actions: []pc.DeclarationActionResult{{EffectRef: "retained-effect"}}}
			svc := &declarationServiceStub{result: result, err: declarationFailure(tt.code, tt.cause)}
			h, _ := declarationTestHandler(svc)
			w := declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, declarationTestApply()))
			var env ProjectDeclarationErrorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(env.Error.Hint, tt.want) || strings.Contains(env.Error.Hint, "resume") != tt.resume {
				t.Fatalf("wrong hint: %s", env.Error.Hint)
			}
			if env.DeclarationError.Code != tt.code || env.DeclarationError.CauseCode != tt.cause || len(env.DeclarationError.CompletedEffects) != 1 || env.DeclarationError.CompletedEffects[0] != "retained-effect" || env.Data.OperationID != result.OperationID || env.Meta.IdempotencyKey != "original" || env.Error.CorrelationID == "" {
				t.Fatalf("error metadata changed: %s", w.Body)
			}
		})
	}
	for _, state := range []pc.DeclarationOperationState{pc.DeclarationOperationSucceeded, pc.DeclarationOperationSuperseded} {
		result := pc.DeclarationResult{OperationID: ids.NewJobID(), State: state}
		svc := &declarationServiceStub{result: result, err: declarationFailure(pc.DeclarationOwnerFailed, "observation_uncertain")}
		h, _ := declarationTestHandler(svc)
		w := declarationHTTP(t, h, "POST", "/v1/project-declaration-operations", declarationJSONTest(t, declarationTestApply()))
		var env ProjectDeclarationErrorEnvelope
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if strings.Contains(env.Error.Hint, "resume") {
			t.Fatalf("terminal operation resumed: %s", env.Error.Hint)
		}
	}
}
