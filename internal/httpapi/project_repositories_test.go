package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
)

type projectRepositoryObserverStub struct {
	projection projectstate.ProjectProjection
	err        error
	calls      int
	ref        string
}

func (stub *projectRepositoryObserverStub) ObserveProject(_ context.Context, ref string) (projectstate.ProjectProjection, error) {
	stub.calls++
	stub.ref = ref
	return stub.projection, stub.err
}

type projectRepositoryAuthorizerStub struct {
	projectID string
	err       error
	calls     int
	req       requestctx.Context
	ref       string
}

func (stub *projectRepositoryAuthorizerStub) AuthorizeProjectRepositoryRead(_ context.Context, req requestctx.Context, ref string) (string, error) {
	stub.calls++
	stub.req = req
	stub.ref = ref
	return stub.projectID, stub.err
}

func TestProjectRepositoryReadDeniesBeforeObservation(t *testing.T) {
	observer := &projectRepositoryObserverStub{}
	authorizer := &projectRepositoryAuthorizerStub{err: ErrProjectRepositoryReadForbidden}
	handler := projectRepositoryHTTPTestHandler(observer, authorizer)
	request := httptest.NewRequest(http.MethodGet, "/v1/projects/private-project/repos/status", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if observer.calls != 0 {
		t.Fatalf("observer calls=%d, want 0", observer.calls)
	}
	if authorizer.calls != 1 || authorizer.ref != "private-project" || authorizer.req.ActorID != "actor_test" || authorizer.req.OriginNodeID != "node_test" {
		t.Fatalf("authorization=%#v calls=%d ref=%q", authorizer.req, authorizer.calls, authorizer.ref)
	}
	var failure response.ErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "project.repositories.forbidden" {
		t.Fatalf("error code=%q", failure.Error.Code)
	}
}

func TestProjectRepositorySurfacesAreBoundedPaginatedAndArchiveReadable(t *testing.T) {
	observedAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	registeredAt := observedAt.Add(-time.Hour)
	observer := &projectRepositoryObserverStub{projection: projectstate.ProjectProjection{
		SchemaVersion: projectstate.SchemaVersion,
		Project: projectstate.ProjectIdentityProjection{
			ProjectID: "project_archived",
			Slug:      "archived-project",
			Name:      "Sensitive Name Not Needed By The Surface",
			Lifecycle: "archived",
		},
		Source: projectstate.SourceProjection{
			Posture:                      projectstate.SourcePostureRegistered,
			ProjectContractSchemaVersion: "project.contract.v0.4",
			ReposContractSchemaVersion:   "repos.contract.v0.4",
			SemanticDigest:               "sha256:must-not-render",
			LocationDigest:               "sha256:must-not-render-location",
			SourceRevision:               3,
			RegisteredAt:                 &registeredAt,
		},
		Observation: projectstate.ObservationSummary{
			Posture:     projects.ProjectRepositoryObservationObserved,
			MemberCount: 2,
			Observed:    2,
		},
		ObservedAt: observedAt,
		Members: []projectstate.RepositoryProjection{
			{
				RepositoryID:             "repo_02",
				RepositoryOwnerProjectID: "project_archived",
				Key:                      "second",
				Role:                     projects.ProjectRepositoryRoleComponent,
				RelativeSource:           "services/second",
				MembershipLifecycle:      projects.RepositoryLifecycleArchived,
				RepositoryLifecycle:      projects.RepositoryLifecycleArchived,
				ObservationPosture:       projects.ProjectRepositoryObservationObserved,
				ObservedAt:               &observedAt,
				DevelopmentState:         projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateNotEnabled},
				Git: &projectstate.GitProjection{
					LocalIdentityDigest:           "sha256:must-not-render-git-identity",
					CurrentBranch:                 "main",
					Head:                          "0123456789abcdef",
					Worktree:                      true,
					WorktreeCanonicalProjectState: true,
					Dirty:                         projectstate.GitDirtyProjection{Dirty: true, TrackedChanges: true},
				},
			},
			{
				RepositoryID:             "repo_01",
				RepositoryOwnerProjectID: "project_archived",
				Key:                      "first",
				Role:                     projects.ProjectRepositoryRolePrimary,
				RelativeSource:           "first",
				MembershipLifecycle:      projects.RepositoryLifecycleArchived,
				RepositoryLifecycle:      projects.RepositoryLifecycleArchived,
				ObservationPosture:       projects.ProjectRepositoryObservationObserved,
				ObservedAt:               &observedAt,
				DevelopmentState:         projectstate.DevelopmentStateProjection{Posture: projectstate.DevelopmentStateEnabled, RelativePath: ".repo/repo.yaml", SourceDigest: "sha256:must-not-render-repo-source"},
			},
		},
	}}
	handler := projectRepositoryHTTPTestHandler(observer, &projectRepositoryAuthorizerStub{projectID: "project_archived"})

	listRequest := httptest.NewRequest(http.MethodGet, "/v1/projects/archived-project/repos?limit=1", nil)
	listRecorder := httptest.NewRecorder()
	handler.ServeHTTP(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var listEnvelope response.Envelope[projectRepositoryListResult]
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatal(err)
	}
	if listEnvelope.Data.Project.Lifecycle != "archived" || len(listEnvelope.Data.Repositories) != 1 || listEnvelope.Data.Repositories[0].RepositoryID != "repo_01" {
		t.Fatalf("list=%#v", listEnvelope.Data)
	}
	if !listEnvelope.Data.Page.HasMore || listEnvelope.Data.Page.NextAfter != "repo_01" {
		t.Fatalf("page=%#v", listEnvelope.Data.Page)
	}

	inspectRequest := httptest.NewRequest(http.MethodGet, "/v1/projects/archived-project/repos/inspect/second", nil)
	inspectRecorder := httptest.NewRecorder()
	handler.ServeHTTP(inspectRecorder, inspectRequest)
	if inspectRecorder.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", inspectRecorder.Code, inspectRecorder.Body.String())
	}
	var inspectEnvelope response.Envelope[projectRepositoryInspectResult]
	if err := json.Unmarshal(inspectRecorder.Body.Bytes(), &inspectEnvelope); err != nil {
		t.Fatal(err)
	}
	git := inspectEnvelope.Data.Repository.Git
	if git == nil || !git.Worktree || git.WorktreeCanonicalProjectState {
		t.Fatalf("surface git=%#v", git)
	}
	for _, forbidden := range []string{"Sensitive Name", "must-not-render", "project_root", "source_snapshot", "registration_plan"} {
		if strings.Contains(inspectRecorder.Body.String(), forbidden) {
			t.Fatalf("inspect response exposed %q: %s", forbidden, inspectRecorder.Body.String())
		}
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/v1/projects/archived-project/repos/status?after=repo_01&limit=20", nil)
	statusRecorder := httptest.NewRecorder()
	handler.ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", statusRecorder.Code, statusRecorder.Body.String())
	}
	var statusEnvelope response.Envelope[projectRepositoryStatusResult]
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &statusEnvelope); err != nil {
		t.Fatal(err)
	}
	if statusEnvelope.Data.Observation.MemberCount != 2 || len(statusEnvelope.Data.Repositories) != 1 || statusEnvelope.Data.Repositories[0].RepositoryID != "repo_02" {
		t.Fatalf("status data=%#v", statusEnvelope.Data)
	}
	if observer.calls != 3 {
		t.Fatalf("observer calls=%d, want 3", observer.calls)
	}
	if observer.ref != "project_archived" {
		t.Fatalf("observer ref=%q, want exact authorized project id", observer.ref)
	}
}

func TestProjectRepositoryStatusUsesReposFacetObservationAndPreservesRelativePath(t *testing.T) {
	projectRoot := filepath.Join(string(filepath.Separator), "registered", "project")
	paths := &projectRepositoryPathResolverStub{resolved: projectstate.ResolvedPath{Path: filepath.Join(projectRoot, "repos", "backend"), Exists: true, Directory: true}}
	git := &projectRepositoryGitObserverStub{projection: projectstate.GitProjection{RootMatchesMember: true, HeadPosture: projectstate.GitHeadObserved}}
	observer := projectstate.Service{
		Reader: projectRepositoryReaderStub{model: projects.ProjectRepositoryReadModel{
			Project: projects.ProjectRepositoryReadProject{ProjectID: "project_observed", Slug: "observed", Status: "active"},
			Source: &projects.ProjectRepositoryReadSource{
				ProjectContractSchemaVersion: projects.ProjectRepositoryProjectSchemaV04,
				ReposContractSchemaVersion:   projects.ProjectRepositoryReposSchemaV04,
				ProjectRoot:                  projectRoot, OwnerNode: "main",
				ProjectContractPath:          filepath.Join(projectRoot, ".loom", "project.yaml"),
				ReposContractPath:            filepath.Join(projectRoot, ".loom", "contracts", "repos.yaml"),
			},
			Members: []projects.ProjectRepositoryReadMember{{
				RepositoryID: "repo_backend", RepositoryOwnerProjectID: "project_observed", Key: "backend", Path: "backend",
				Role: projects.ProjectRepositoryRolePrimary, MembershipLifecycle: projects.RepositoryLifecycleActive,
				RepositoryLifecycle: projects.RepositoryLifecycleActive,
			}},
		}},
		Paths: paths, Git: git, LocalNode: "main",
	}
	handler := projectRepositoryHTTPTestHandler(observer, &projectRepositoryAuthorizerStub{projectID: "project_observed"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/projects/observed/repos/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope response.Envelope[projectRepositoryStatusResult]
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Repositories) != 1 || envelope.Data.Repositories[0].RelativePath != "backend" || envelope.Data.Repositories[0].ObservationPosture != string(projects.ProjectRepositoryObservationObserved) {
		t.Fatalf("status repository=%#v", envelope.Data.Repositories)
	}
	if paths.root != projectRoot || paths.relative != filepath.Join("repos", "backend") {
		t.Fatalf("observation path root=%q relative=%q", paths.root, paths.relative)
	}
	if git.root != filepath.Join(projectRoot, "repos", "backend") {
		t.Fatalf("Git observation root=%q", git.root)
	}
}

func TestProjectRepositorySurfaceRejectsUnsupportedMethodsAndMissingMembers(t *testing.T) {
	observer := &projectRepositoryObserverStub{projection: projectstate.ProjectProjection{
		SchemaVersion: projectstate.SchemaVersion,
		Project:       projectstate.ProjectIdentityProjection{ProjectID: "project_test", Slug: "test", Lifecycle: "active"},
		Source:        projectstate.SourceProjection{Posture: projectstate.SourcePostureNotRegistered},
		Members:       []projectstate.RepositoryProjection{},
		ObservedAt:    time.Now().UTC(),
	}}
	handler := projectRepositoryHTTPTestHandler(observer, &projectRepositoryAuthorizerStub{projectID: "project_test"})

	post := httptest.NewRecorder()
	handler.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/v1/projects/test/repos", nil))
	if post.Code != http.StatusMethodNotAllowed || observer.calls != 0 {
		t.Fatalf("post status=%d observer=%d", post.Code, observer.calls)
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/projects/test/repos/inspect/repo_missing", nil))
	if missing.Code != http.StatusNotFound || observer.calls != 1 {
		t.Fatalf("missing status=%d observer=%d body=%s", missing.Code, observer.calls, missing.Body.String())
	}
}

func projectRepositoryHTTPTestHandler(observer ProjectRepositoryStateObserver, authorizer ProjectRepositoryReadAuthorizer) http.Handler {
	return NewServer(Services{
		ProjectRepos: ProjectRepositoryServices{
			State:      observer,
			Authorizer: authorizer,
			RequestResolver: func(context.Context, string) (requestctx.Context, error) {
				return requestctx.Context{ActorID: "actor_test", OriginNodeID: "node_test"}, nil
			},
		},
	}, slog.Default()).Handler()
}

func TestProjectRepositoryRequestContextFailurePrecedesAuthorizationAndObservation(t *testing.T) {
	observer := &projectRepositoryObserverStub{}
	authorizer := &projectRepositoryAuthorizerStub{}
	handler := NewServer(Services{
		ProjectRepos: ProjectRepositoryServices{
			State:      observer,
			Authorizer: authorizer,
			RequestResolver: func(context.Context, string) (requestctx.Context, error) {
				return requestctx.Context{}, errors.New("auth unavailable")
			},
		},
	}, slog.Default()).Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/projects/test/repos", nil))
	if recorder.Code != http.StatusServiceUnavailable || authorizer.calls != 0 || observer.calls != 0 {
		t.Fatalf("status=%d authorizer=%d observer=%d", recorder.Code, authorizer.calls, observer.calls)
	}
}

type projectRepositoryReaderStub struct {
	model projects.ProjectRepositoryReadModel
}

func (stub projectRepositoryReaderStub) ReadProjectRepositoryState(context.Context, string) (projects.ProjectRepositoryReadModel, error) {
	return stub.model, nil
}

type projectRepositoryPathResolverStub struct {
	resolved projectstate.ResolvedPath
	root     string
	relative string
}

func (stub *projectRepositoryPathResolverStub) ResolveWithin(root, relative string) (projectstate.ResolvedPath, error) {
	stub.root = root
	stub.relative = relative
	return stub.resolved, nil
}

type projectRepositoryGitObserverStub struct {
	projection projectstate.GitProjection
	root       string
}

func (stub *projectRepositoryGitObserverStub) Observe(_ context.Context, root string) (projectstate.GitProjection, error) {
	stub.root = root
	return stub.projection, nil
}
