package httpapi

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/serviceregistry"
	"loom.local/loom/internal/workspacefiles"
)

func navigationBoxContext(ids ...string) context.Context {
	return boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: ids})
}

func registeredBoxProjectRow(id, home, status string) []driver.Value {
	metadata, _ := json.Marshal(map[string]string{"home_box_id": home})
	now := time.Now().UTC()
	return []driver.Value{id, "scope_test", "project:test", "test", "Test", "", "actor_owner", "actor_owner", nil, nil, nil, status, "normal", "native", []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{}`), []byte(`{"original_box_id":"box_company"}`), metadata, now, now}
}

func TestBoxNavigationRootMembershipUsesCatalogAndRegisteredProject(t *testing.T) {
	personal, company, storage := t.TempDir(), t.TempDir(), t.TempDir()
	archive := filepath.Join(storage, "Boxes", "company")
	catalog := boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", RootPath: personal, Primary: true, ArchiveRoot: storage, Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", RootPath: company, ArchiveRoot: archive, Available: true},
	}}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{
		{Ref: "source_documents", SourcePath: filepath.Join(company, "Documents")},
		{Ref: "company_archive", SourcePath: filepath.Join(archive, "Projects"), ReadOnly: true},
		{Ref: "global_storage", SourcePath: storage, ReadOnly: true},
		{Ref: "archive_neighbor", SourcePath: archive + "-other", ReadOnly: true},
		{Ref: "unassigned_app", SourcePath: filepath.Join(personal, "Applications", "unknown"), SourceKind: "application_data"},
		{Ref: "appdata_cached", SourcePath: filepath.Join(personal, "Applications", "unknown")},
		{Ref: "archived_project", SourcePath: filepath.Join(storage, "Projects", "old-company-project"), ProjectID: "project_registered", ReadOnly: true},
	}}
	db, query := newHTTPStorageFakeDB(t)
	t.Cleanup(func() { _ = db.Close(); _ = files.Close() })
	server := NewServer(Services{Files: files, Boxes: &catalog, DB: db, Projects: projects.NewService(db)}, slog.Default())
	for ref, want := range map[string]string{"box": "box_personal", "box:box_company": "box_company", "source_documents": "box_company", "company_archive": "box_company", "global_storage": "", "archive_neighbor": "", "unassigned_app": ""} {
		got, err := server.workspaceLinks().RootBoxID(navigationBoxContext("box_company"), ref)
		if err != nil || got != want {
			t.Fatal(ref, got, err)
		}
	}
	if _, err := server.workspaceRootBoxID(navigationBoxContext("box_company"), "appdata_cached"); !errors.Is(err, workspacefiles.ErrUnknownRoot) {
		t.Fatal("cached application root bypassed validated owner", err)
	}
	query.expect("p.metadata->>'home_box_id'", func(args []driver.NamedValue) {
		if args[0].Value != "project_registered" || args[1].Value != `["box_company"]` {
			t.Fatal(args)
		}
	}, registeredBoxProjectRow("project_registered", "box_company", "archived"))
	if got, err := server.workspaceLinks().RootBoxID(navigationBoxContext("box_company"), "archived_project"); err != nil || got != "box_company" {
		t.Fatal("archive custody changed attribution", got, err)
	}
	if _, err := server.workspaceRootBoxID(context.Background(), ""); !errors.Is(err, workspacefiles.ErrUnknownRoot) {
		t.Fatal("empty root selected Personal", err)
	}
	query.requireDone()
}

type navigationApplicationFixture struct {
	root    serviceregistry.ApplicationBrowseRoot
	calls   int
	request serviceregistry.ApplicationBrowseRead
}

func (f *navigationApplicationFixture) ReadBrowse(_ context.Context, request serviceregistry.ApplicationBrowseRead) (serviceregistry.ApplicationBrowseResult, error) {
	f.calls++
	f.request = request
	if request.Operation != "resolve" || request.Ref != f.root.Ref {
		return serviceregistry.ApplicationBrowseResult{}, errors.New("unexpected application read")
	}
	return serviceregistry.ApplicationBrowseResult{Root: &f.root}, nil
}
func (*navigationApplicationFixture) OpenBrowse(context.Context, serviceregistry.ApplicationBrowseRead) (*os.File, serviceregistry.ApplicationBrowseRoot, error) {
	return nil, serviceregistry.ApplicationBrowseRoot{}, errors.New("membership must not open application content")
}

func TestBoxNavigationApplicationMembershipNeverDefaultsToPersonal(t *testing.T) {
	personal := t.TempDir()
	for _, home := range []string{"box_company", ""} {
		db, query := newHTTPStorageFakeDB(t)
		fixture := &navigationApplicationFixture{root: serviceregistry.ApplicationBrowseRoot{Ref: "appdata_known", ProjectID: "project_registered", SourcePath: filepath.Join(personal, "Applications", "app")}}
		files := &workspacefiles.Service{Applications: fixture}
		server := NewServer(Services{DB: db, Projects: projects.NewService(db), Files: files, Boxes: &boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{{ID: "box_personal", RootPath: personal, RootRef: "box", Primary: true, Available: true}}}}, slog.Default())
		query.expect("FROM projects.projects p", nil, registeredBoxProjectRow("project_registered", home, "archived"))
		got, err := server.workspaceLinks().RootBoxID(context.Background(), "appdata_known")
		if err != nil || got != home {
			t.Fatal("application inherited physical placement/default", got, err)
		}
		if _, err := server.workspaceRootBoxID(navigationBoxContext("box_personal"), "appdata_known"); !errors.Is(err, workspacefiles.ErrApplicationBoxOwnership) || fixture.calls != 1 {
			t.Fatal("foreign/unknown project broadened", err)
		}
		fixture.root.ProjectID = ""
		if got, err := server.workspaceRootBoxID(context.Background(), "appdata_known"); err != nil || got != "" {
			t.Fatal("projectless application defaulted", got, err)
		}
		query.requireDone()
		_ = db.Close()
	}
}

func TestBoxNavigationApplicationRootUsesValidatedBox(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAW"
	raw, _ := json.Marshal(struct {
		Owner serviceregistry.ApplicationOwner `json:"owner"`
		Key   string                           `json:"key"`
	}{serviceregistry.ApplicationOwner{ProjectID: projectID, NodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV", Resource: "reader"}, "exports"})
	ref := "appdata_" + base64.RawURLEncoding.EncodeToString(raw)
	fixture := &navigationApplicationFixture{root: serviceregistry.ApplicationBrowseRoot{Ref: ref, ProjectID: projectID}}
	home := "box_company"
	files := &workspacefiles.Service{Applications: fixture, ApplicationProjectBoxes: func(ctx context.Context, ids []string) (map[string]string, error) {
		selection, selected := boxregistry.FromContext(ctx)
		if !selected || !selection.Contains("box_company") || len(ids) != 1 || ids[0] != projectID {
			t.Fatal("membership lost exact scoped project", ids)
		}
		return map[string]string{projectID: home}, nil
	}}
	server := NewServer(Services{Files: files}, slog.Default())
	ctx := navigationBoxContext("box_company")
	if got, err := server.workspaceLinks().RootBoxID(ctx, ref); err != nil || got != "box_company" {
		t.Fatal("validated BoxID was ignored", got, err)
	}
	if fixture.calls != 1 || !fixture.request.RestrictProjects || fixture.request.ProjectID != projectID || len(fixture.request.ProjectIDs) != 1 || fixture.request.ProjectIDs[0] != projectID {
		t.Fatal("resolve bypassed scoped browse owner", fixture.request)
	}
	home = "box_personal"
	if _, err := server.workspaceRootBoxID(ctx, ref); !errors.Is(err, fs.ErrPermission) || fixture.calls != 1 {
		t.Fatal("foreign root reached raw helper", err, fixture.calls)
	}
	home = ""
	if _, err := server.workspaceRootBoxID(ctx, ref); !errors.Is(err, workspacefiles.ErrApplicationBoxOwnership) || fixture.calls != 1 {
		t.Fatal("unknown ownership reached raw helper", err, fixture.calls)
	}
	files.ApplicationProjectBoxes = nil
	if _, err := server.workspaceRootBoxID(ctx, ref); !errors.Is(err, workspacefiles.ErrApplicationBoxOwnership) || fixture.calls != 1 {
		t.Fatal("missing ownership callback fell back", err, fixture.calls)
	}
}

func TestBoxNavigationNavigateFlagDoesNotBroadenSelection(t *testing.T) {
	personal, company := t.TempDir(), t.TempDir()
	for _, root := range []string{personal, company} {
		for _, name := range []string{"start.md", "same.md"} {
			if err := os.WriteFile(filepath.Join(root, name), []byte("same title"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{
		{Ref: "box", BoxID: "box_personal", SourcePath: personal},
		{Ref: "box:box_company", BoxID: "box_company", SourcePath: company, SourceKind: "box"},
	}}
	t.Cleanup(func() { _ = files.Close() })
	server := NewServer(Services{Files: files, DB: new(sql.DB)}, slog.Default())
	for _, tc := range []struct {
		navigate string
		broad    bool
		status   int
	}{
		{"", false, 409}, {"&navigate=false", false, 409}, {"&navigate=true", false, 409},
		{"", true, 409}, {"&navigate=false", true, 409}, {"&navigate=true", true, 200},
		{"&navigate=1", true, 400}, {"&navigate=true&navigate=false", true, 400}, {"&navigate=", true, 400},
	} {
		ctx := navigationBoxContext("box_company")
		if tc.broad {
			ctx = navigationBoxContext("box_company", "box_personal")
		}
		ctx = context.WithValue(ctx, localFileTransportKey{}, true)
		values := url.Values{"action": {"resolve"}, "root": {"box:box_company"}, "path": {"start.md"}, "reference": {filepath.Join(personal, "same.md")}}
		request := httptest.NewRequest(http.MethodGet, "/v1/files/links?"+values.Encode()+tc.navigate, nil).WithContext(ctx)
		out := httptest.NewRecorder()
		server.handleFileLinks(out, request)
		if out.Code != tc.status {
			t.Fatal(tc, out.Code, out.Body.String())
		}
		if out.Code == 200 && !strings.Contains(out.Body.String(), `"root":"box"`) {
			t.Fatal(out.Body.String())
		}
	}
}

func TestBoxNavigationCatalogConsumesBoundSelectorsWithoutBroadening(t *testing.T) {
	files := &workspacefiles.Service{Roots: []workspacefiles.Root{{Ref: "box", BoxID: "box_personal", SourcePath: t.TempDir()}}}
	t.Cleanup(func() { _ = files.Close() })
	server := NewServer(Services{Files: files, DB: new(sql.DB)}, slog.Default())
	ctx := context.WithValue(navigationBoxContext("box_company"), localFileTransportKey{}, true)
	request := httptest.NewRequest(http.MethodGet, "/v1/files/links?action=catalog&root=box&box_id=box_company", nil).WithContext(ctx)
	out := httptest.NewRecorder()
	server.handleFileLinks(out, request)
	if out.Code != http.StatusForbidden {
		t.Fatal("catalog selector was rejected or widened", out.Code, out.Body.String())
	}
}

func TestBoxProvenanceSyncWiresRegisteredArchivedHome(t *testing.T) {
	projectID := "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	for _, inSelection := range []bool{true, false} {
		db, query := newHTTPStorageFakeDB(t)
		source := &provenanceProjectSourceStub{source: projectstate.ProvenanceProjectSource{Projection: projectstate.ProjectProjection{Project: projectstate.ProjectIdentityProjection{ProjectID: projectID}}}}
		transport := &provenanceTransportStub{}
		resolve := func(_ context.Context, cid string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", CorrelationID: cid}, nil
		}
		server := NewServer(Services{DB: db, Projects: projects.NewService(db), Provenance: transport, ProvenanceAuthorizer: &provenanceAuthorizerStub{}, Idempotency: &provenanceIdempotencyStub{}, ProvenanceRequestResolver: resolve,
			ProjectRepos: ProjectRepositoryServices{Provenance: source, RepositoryState: &provenanceRepositoryProjectorStub{}, Authorizer: &provenanceProjectReadAuthorizerStub{projectID: projectID}, RequestResolver: resolve},
		}, slog.Default())
		if inSelection {
			query.expect("p.metadata->>'home_box_id'", nil, registeredBoxProjectRow(projectID, "box_company", "archived"))
		} else {
			query.expect("p.metadata->>'home_box_id'", nil)
		}
		r := httptest.NewRequest(http.MethodPost, "/v1/provenance/projects/archived/sync", strings.NewReader(`{}`)).WithContext(navigationBoxContext("box_company"))
		r.Header.Set(idempotency.Header, "scoped-sync")
		out := httptest.NewRecorder()
		server.handleProvenanceProjectProjection(out, r)
		if inSelection {
			if out.Code != 200 || transport.repoSyncInput.HomeBoxID != "box_company" {
				t.Fatal(out.Code, out.Body.String(), transport.repoSyncInput)
			}
		} else if out.Code != 409 || transport.repoSyncInput.Project.Project.ProjectID != "" || !strings.Contains(out.Body.String(), "provenance.box_selection_mismatch") {
			t.Fatal("unknown scoped sync continued", out.Code, out.Body.String())
		}
		query.requireDone()
		_ = db.Close()
	}
}

func TestBoxProvenanceExactContextAcceptsOnlyBoundSelectors(t *testing.T) {
	transport := &projectContextTransport{provenanceTransportStub: &provenanceTransportStub{}}
	server := NewServer(Services{Provenance: transport, ProvenanceAuthorizer: &provenanceAuthorizerStub{},
		ProvenanceRequestResolver: func(_ context.Context, cid string) (requestctx.Context, error) {
			return requestctx.Context{ActorID: "actor_authenticated", OriginNodeID: "node_authenticated", CorrelationID: cid}, nil
		},
	}, slog.Default())
	path := "/v1/provenance/projects/project_01ARZ3NDEKTSV4RRFFQ69G5FAV?box_id=box_company"
	for _, scoped := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if scoped {
			r = r.WithContext(navigationBoxContext("box_company"))
		}
		out := httptest.NewRecorder()
		server.handleProvenanceProjectContext(out, r)
		want := 400
		if scoped {
			want = 200
		}
		if out.Code != want {
			t.Fatal(scoped, out.Code, out.Body.String())
		}
	}
	if transport.calls != 1 {
		t.Fatal(transport.calls)
	}
}
