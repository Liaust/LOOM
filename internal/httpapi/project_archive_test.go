package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
)

type projectPhysicalHTTPFake struct {
	calls  int
	kind   storagearchive.WorkspaceOperationKind
	req    requestctx.Context
	result storagearchive.ProjectPhysicalMutationSummary
	err    error
}

func (f *projectPhysicalHTTPFake) ProjectPhysicalScope(context.Context, string) (string, string, string, error) {
	return "project_test", "scope_project", "project:test", nil
}
func projectHTTPReview(req requestctx.Context, kind storagearchive.WorkspaceOperationKind) storagearchive.ProjectPhysicalPlanReview {
	w := workspaceArchiveHTTPReview(kind)
	w.Kind, w.ObjectID, w.ActorID = storagearchive.WorkspaceKindProject, "project_test", req.ActorID
	return storagearchive.ProjectPhysicalPlanReview{SchemaVersion: storagearchive.ProjectPhysicalPlanReviewSchemaVersion, ProjectID: "project_test", Request: req, Workspace: w, RegistrationRevision: 1, PlanDigest: workspaceHTTPDigest, ActivationState: storagearchive.WorkspaceActivationInactive}
}
func (f *projectPhysicalHTTPFake) ReviewProjectPhysicalArchive(_ context.Context, req requestctx.Context, _ string, _ storagearchive.ProjectPhysicalArchivePlanInput) (storagearchive.ProjectPhysicalPlanReview, error) {
	f.calls++
	f.req = req
	return projectHTTPReview(req, storagearchive.WorkspaceOperationArchive), f.err
}
func (f *projectPhysicalHTTPFake) ReviewProjectPhysicalRestore(_ context.Context, req requestctx.Context, _ string, _ storagearchive.ProjectPhysicalRestorePlanInput) (storagearchive.ProjectPhysicalPlanReview, error) {
	f.calls++
	f.req = req
	return projectHTTPReview(req, storagearchive.WorkspaceOperationRestore), f.err
}
func (f *projectPhysicalHTTPFake) ApplyReviewedProjectPhysicalPlan(_ context.Context, req requestctx.Context, _ string, input storagearchive.ProjectPhysicalApplyRequest) (storagearchive.ProjectPhysicalMutationSummary, error) {
	f.calls++
	f.req = req
	f.kind = input.Plan.Workspace.OperationKind
	return f.result, f.err
}
func (f *projectPhysicalHTTPFake) RecoverReviewedProjectPhysicalKind(_ context.Context, req requestctx.Context, _ string, _ storagearchive.ProjectPhysicalRecoverRequest, kind storagearchive.WorkspaceOperationKind) (storagearchive.ProjectPhysicalMutationSummary, error) {
	f.calls++
	f.req = req
	f.kind = kind
	return f.result, f.err
}

type projectHTTPPolicy struct {
	denied string
	calls  []string
	req    requestctx.Context
}

func (p *projectHTTPPolicy) Authorize(_ context.Context, req requestctx.Context, cap string) error {
	p.calls = append(p.calls, cap)
	p.req = req
	if req.ScopeID != "scope_project" || req.ScopeKey != "project:test" || cap == p.denied {
		return errors.New("private denial")
	}
	return nil
}
func projectHTTPServer(f *projectPhysicalHTTPFake, p *projectHTTPPolicy) Server {
	return NewServer(Services{ProjectPhysicalArchive: f, WorkspaceArchiveAuthorizer: p, WorkspaceArchiveRequestResolver: func(_ context.Context, corr string) (requestctx.Context, error) {
		return requestctx.Context{ActorID: workspaceHTTPActorID, OriginNodeID: "node_main", ScopeID: "scope_system", ScopeKey: "system", CorrelationID: corr, Source: "authenticated"}, nil
	}}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestProjectPhysicalFailureLogsPrivateCauseWithoutReturningIt(t *testing.T) {
	var logs strings.Builder
	s := NewServer(Services{}, slog.New(slog.NewTextHandler(&logs, nil)))
	w := httptest.NewRecorder()
	cause := errors.New("private planner failure /fixture/source")
	s.writeProjectPhysicalError(w, "corr_archive_diagnostic", cause, nil)
	if !strings.Contains(logs.String(), cause.Error()) || !strings.Contains(logs.String(), "corr_archive_diagnostic") {
		t.Fatalf("operator diagnostic missing: %s", logs.String())
	}
	if strings.Contains(w.Body.String(), cause.Error()) || strings.Contains(w.Body.String(), "/fixture/source") {
		t.Fatalf("private cause exposed: %s", w.Body.String())
	}
}

func TestProjectPhysicalHTTPClientRoutesAndPartialFailure(t *testing.T) {
	f, policy := &projectPhysicalHTTPFake{}, &projectHTTPPolicy{}
	server := httptest.NewServer(projectHTTPServer(f, policy).Handler())
	defer server.Close()
	client, _ := localclient.NewHTTP(server.URL)
	for _, restore := range []bool{false, true} {
		var review storagearchive.ProjectPhysicalPlanReview
		if restore {
			out, err := client.ReviewProjectPhysicalRestore(context.Background(), "corr_plan", "test", "reviewed")
			if err != nil {
				t.Fatal(err)
			}
			review = out.Data
		} else {
			out, err := client.ReviewProjectPhysicalArchive(context.Background(), "corr_plan", "test", "reviewed")
			if err != nil {
				t.Fatal(err)
			}
			review = out.Data
		}
		if review.Request.ScopeID != "scope_project" || review.Request.ScopeKey != "project:test" || review.Request.ActorID != workspaceHTTPActorID {
			t.Fatal("caller not independently scoped")
		}
		f.result = storagearchive.ProjectPhysicalMutationSummary{SchemaVersion: "storage.project_physical_mutation.v1", ProjectID: review.ProjectID, OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Phase: "project_state_pending", MutationBlocked: true, Recoverable: true, ActivationState: storagearchive.WorkspaceActivationInactive}
		f.err = fmt.Errorf("private /root/key: %w", storagearchive.ProjectPhysicalSurfaceError{Code: "operation_failed"})
		input := storagearchive.ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true}
		if restore {
			out, err := client.ApplyProjectPhysicalRestore(context.Background(), "corr_apply", "test", input)
			assertProjectHTTPPartial(t, out.Data, err)
		} else {
			out, err := client.ApplyProjectPhysicalArchive(context.Background(), "corr_apply", "test", input)
			assertProjectHTTPPartial(t, out.Data, err)
		}
		if f.req.CorrelationID != "corr_apply" || f.kind != review.Workspace.OperationKind {
			t.Fatal("route/caller confused")
		}
		f.err = nil
		inputRecover := storagearchive.ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true}
		if restore {
			if _, err := client.RecoverProjectPhysicalRestore(context.Background(), "corr_recover", "test", inputRecover); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := client.RecoverProjectPhysicalArchive(context.Background(), "corr_recover", "test", inputRecover); err != nil {
				t.Fatal(err)
			}
		}
		if f.kind != review.Workspace.OperationKind || policy.calls[len(policy.calls)-1] != capabilities.WorkspaceArchiveRecoverCapability {
			t.Fatal("recovery capability/kind missing")
		}
	}
}
func assertProjectHTTPPartial(t *testing.T, data storagearchive.ProjectPhysicalMutationSummary, err error) {
	t.Helper()
	var reqErr *localclient.RequestError
	if !errors.As(err, &reqErr) || reqErr.Envelope.Error.Code != "project_archive.operation_failed" || data.Phase != "project_state_pending" || !data.MutationBlocked || !data.Recoverable || strings.Contains(err.Error(), "private") {
		t.Fatalf("partial truth lost: %+v %v", data, err)
	}
}

func TestProjectPhysicalHTTPRejectsBeforeBackend(t *testing.T) {
	req := requestctx.Context{ActorID: workspaceHTTPActorID, OriginNodeID: "node_main", ScopeID: "scope_project", ScopeKey: "project:test", CorrelationID: "corr_original", Source: "authenticated"}
	for _, name := range []string{"actor", "origin", "scope", "scope_key", "source", "project", "digest", "confirm", "kind", "unknown", "trailing", "oversized", "denied", "wrong_method", "raw_path"} {
		t.Run(name, func(t *testing.T) {
			f, policy := &projectPhysicalHTTPFake{}, &projectHTTPPolicy{}
			input := storagearchive.ProjectPhysicalApplyRequest{Plan: projectHTTPReview(req, storagearchive.WorkspaceOperationArchive), PlanDigest: workspaceHTTPDigest, Confirm: true}
			method := http.MethodPost
			switch name {
			case "actor":
				input.Plan.Request.ActorID = "actor_other"
			case "origin":
				input.Plan.Request.OriginNodeID = "node_other"
			case "scope":
				input.Plan.Request.ScopeID = "scope_system"
			case "scope_key":
				input.Plan.Request.ScopeKey = "system"
			case "source":
				input.Plan.Request.Source = "submitted"
			case "project":
				input.Plan.ProjectID = "project_other"
			case "digest":
				input.PlanDigest = "different"
			case "confirm":
				input.Confirm = false
			case "kind":
				input.Plan.Workspace.OperationKind = storagearchive.WorkspaceOperationRestore
			case "denied":
				policy.denied = capabilities.WorkspaceArchiveApplyCapability
			case "wrong_method":
				method = http.MethodGet
			case "raw_path":
				input.Plan.Workspace.Source.AbsolutePath = "/private/source"
			}
			raw, _ := json.Marshal(input)
			switch name {
			case "unknown":
				raw = []byte(`{"private_root":"/private/source"}`)
			case "trailing":
				raw = append(raw, []byte(`{}`)...)
			case "oversized":
				raw = append(raw, []byte(strings.Repeat(" ", storagearchive.MaximumWorkspaceSurfaceRequestBytes))...)
			}
			w := httptest.NewRecorder()
			projectHTTPServer(f, policy).Handler().ServeHTTP(w, httptest.NewRequest(method, "/v1/projects/test/archive/apply", strings.NewReader(string(raw))))
			if w.Code < 400 || f.calls != 0 || strings.Contains(w.Body.String(), "/private") {
				t.Fatalf("refusal %d calls=%d: %s", w.Code, f.calls, w.Body.String())
			}
		})
	}
	for _, denied := range []string{capabilities.WorkspaceArchiveRestoreApplyCapability, capabilities.WorkspaceArchiveRecoverCapability} {
		f, policy := &projectPhysicalHTTPFake{}, &projectHTTPPolicy{denied: denied}
		w := httptest.NewRecorder()
		projectHTTPServer(f, policy).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/restore/recover", strings.NewReader(`{"confirm":true,"operation_id":"op","plan_digest":"digest"}`)))
		if w.Code != 403 || f.calls != 0 {
			t.Fatal("recovery did not require both grants")
		}
	}
}

func TestProjectPhysicalHTTPNotReadyAndClosedErrors(t *testing.T) {
	var missing *projectPhysicalHTTPFake
	w := httptest.NewRecorder()
	projectHTTPServer(missing, &projectHTTPPolicy{}).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/plan", strings.NewReader(`{}`)))
	if w.Code != 503 {
		t.Fatal("typed-nil dependency did not refuse")
	}
	f := &projectPhysicalHTTPFake{err: storagearchive.ProjectPhysicalSurfaceError{Code: "private /root/key"}}
	w = httptest.NewRecorder()
	projectHTTPServer(f, &projectHTTPPolicy{}).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/plan", strings.NewReader(`{}`)))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("unknown error escaped closed mapping")
	}
}

func TestProjectPhysicalHTTPAllRoutesRequireExactCapability(t *testing.T) {
	for suffix, capability := range map[string]string{
		"plan":            capabilities.WorkspaceArchivePlanCapability,
		"apply":           capabilities.WorkspaceArchiveApplyCapability,
		"recover":         capabilities.WorkspaceArchiveApplyCapability,
		"restore/plan":    capabilities.WorkspaceArchiveRestorePlanCapability,
		"restore/apply":   capabilities.WorkspaceArchiveRestoreApplyCapability,
		"restore/recover": capabilities.WorkspaceArchiveRestoreApplyCapability,
	} {
		f, policy := &projectPhysicalHTTPFake{}, &projectHTTPPolicy{denied: capability}
		w := httptest.NewRecorder()
		projectHTTPServer(f, policy).Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/"+suffix, strings.NewReader(`{}`)))
		if w.Code != 403 || f.calls != 0 || len(policy.calls) != 1 || policy.calls[0] != capability {
			t.Fatalf("route %s authorization: %d %+v calls=%d", suffix, w.Code, policy.calls, f.calls)
		}
	}
}

func TestProjectArchiveRuntimeGuardTypedFailures(t *testing.T) {
	for _, test := range []struct {
		name, code string
		err        error
	}{
		{"archive", "project_runtime.archived", projects.RuntimeArchivedError{}},
		{"pending", "project_runtime.archive_in_progress", projects.ProjectArchiveInProgressError{}},
		{"restoring", "project_runtime.restore_in_progress", projects.ProjectRestoreBlockedError{Phase: projects.ProjectRestorePhasePending}},
		{"restored", "project_runtime.restored_inactive", projects.ProjectRestoreBlockedError{Phase: projects.ProjectRestorePhaseComplete}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(Services{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
			w := httptest.NewRecorder()
			err := errors.Join(fmt.Errorf("private /root/secret: %w", test.err), errors.New("private key details"))
			if !server.writeProjectRuntimeArchivedError(w, "corr_test", "projects", "test", err) || w.Code != http.StatusConflict {
				t.Fatal("runtime failure was not typed")
			}
			if !strings.Contains(w.Body.String(), test.code) || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("unsafe failure: %s", w.Body.String())
			}
		})
	}
	if code, _ := projectRuntimeGuardError(errors.New("unrelated")); code != "" {
		t.Fatal("unrelated error classified")
	}
}

type projectArchiveInspectHTTPProjects struct {
	detail    projects.ProjectRegistrationDetail
	mutations int
}

func (p *projectArchiveInspectHTTPProjects) GetProjectRegistrationStatus(context.Context, string) (projects.ProjectRegistrationDetail, error) {
	return p.detail, nil
}
func (p *projectArchiveInspectHTTPProjects) UpdateProjectArchiveState(context.Context, requestctx.Context, string, json.RawMessage) (projects.Project, error) {
	p.mutations++
	return projects.Project{}, errors.New("forbidden mutation")
}

func TestProjectPhysicalArchiveInspectHTTPClient(t *testing.T) {
	p := &projectArchiveInspectHTTPProjects{detail: projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "test", Status: "active", ArchiveState: json.RawMessage(`{"schema_version":"project.physical_archive_state.v1","phase":"complete"}`)}}}}
	server := httptest.NewServer(NewServer(Services{ProjectArchive: storagearchive.NewProjectRuntimeService(storagearchive.ProjectRuntimeDeps{Projects: p})}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.InspectProjectArchive(context.Background(), "corr_inspect", "test")
	if err != nil || result.Data.Physical == nil || result.Data.Physical.EvidenceStatus != "conflict" || !result.Data.Physical.MutationBlocked || result.Data.RuntimeManifest != nil || p.mutations != 0 {
		t.Fatalf("inspection: %+v %v writes=%d", result.Data.Physical, err, p.mutations)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/projects/test/archive/inspect", nil)
	reply, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer reply.Body.Close()
	if reply.StatusCode != http.StatusMethodNotAllowed || p.mutations != 0 {
		t.Fatal("inspect accepted POST mutation")
	}
}
