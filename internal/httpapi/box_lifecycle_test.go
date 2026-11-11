package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workspacefiles"
)

func lifecycleTestCatalog() *boxregistry.Catalog {
	return &boxregistry.Catalog{DefaultBox: "personal", Boxes: []boxregistry.Box{
		{ID: "box_personal", Key: "personal", RootRef: "box", RootPath: "/srv/loom/box", ArchiveRoot: "/srv/loom/storage", Primary: true, Available: true},
		{ID: "box_company", Key: "company", RootRef: "box:box_company", RootPath: "/srv/loom/boxes/company", ArchiveRoot: "/srv/loom/storage/Boxes/company", Available: true},
	}}
}

func lifecycleTestContext(id string) context.Context {
	return boxregistry.WithSelection(context.Background(), boxregistry.ResolvedSelection{BoxIDs: []string{id}})
}

func lifecycleTestRoots(box boxregistry.Box) storagearchive.TrustedWorkspaceRoots {
	return storagearchive.TrustedWorkspaceRoots{BoxID: box.ID, Primary: box.Primary, BoxRoot: box.RootPath, StorageRoot: box.ArchiveRoot}
}

type boxLifecycleArchiveFake struct {
	workspaceArchiveHTTPServiceFake
	boxID, receiptBoxID string
}

func (f *boxLifecycleArchiveFake) ReviewArchivePlan(ctx context.Context, in storagearchive.WorkspaceArchivePlanInput) (storagearchive.WorkspaceMovePlanReview, error) {
	review, err := f.workspaceArchiveHTTPServiceFake.ReviewArchivePlan(ctx, in)
	review.BoxID = f.boxID
	return review, err
}
func (f *boxLifecycleArchiveFake) ReviewRestorePlan(ctx context.Context, in storagearchive.WorkspaceRestorePlanInput) (storagearchive.WorkspaceMovePlanReview, error) {
	review, err := f.workspaceArchiveHTTPServiceFake.ReviewRestorePlan(ctx, in)
	review.BoxID = f.boxID
	return review, err
}
func (f *boxLifecycleArchiveFake) ReplanReviewed(ctx context.Context, in storagearchive.WorkspaceMovePlanReview) (storagearchive.WorkspaceArchivePlan, error) {
	plan, err := f.workspaceArchiveHTTPServiceFake.ReplanReviewed(ctx, in)
	plan.BoxID = f.boxID
	return plan, err
}
func (f *boxLifecycleArchiveFake) InspectOperation(ctx context.Context, id string) (storagearchive.WorkspaceArchiveInspection, error) {
	inspection, err := f.workspaceArchiveHTTPServiceFake.InspectOperation(ctx, id)
	inspection.Operation.BoxID, inspection.Plan.BoxID = f.boxID, f.boxID
	if f.receiptBoxID != "" {
		inspection.Operation.BoxID = f.receiptBoxID
	}
	return inspection, err
}

func TestBoxLifecycleArchiveDispatchUsesSelectedOwnerForEveryRoute(t *testing.T) {
	for _, action := range []string{"plan", "apply", "inspect", "recover", "restore-plan", "restore-apply"} {
		t.Run(action, func(t *testing.T) {
			primary, company := &workspaceArchiveHTTPServiceFake{}, &boxLifecycleArchiveFake{boxID: "box_company"}
			s := workspaceArchiveTestServer(primary, &workspaceArchiveAuthorizerFake{})
			s.services.Boxes = lifecycleTestCatalog()
			s.services.BoxWorkspaceArchives = map[string]WorkspaceArchiveLifecycleService{"box_company": company}
			method, query, body := http.MethodPost, "?box_id=box_company", any(nil)
			switch action {
			case "plan":
				body = storagearchive.WorkspaceArchivePlanRequest{Kind: storagearchive.WorkspaceKindTopic, Slug: "topic-one", ObjectID: "topic_object", Reason: "archive"}
			case "restore-plan":
				body = storagearchive.WorkspaceRestorePlanRequest{ArchiveOperationID: workspaceHTTPArchive, Reason: "restore"}
			case "inspect":
				method, query = http.MethodGet, query+"&operation_id="+workspaceHTTPOperation
			case "recover":
				body = storagearchive.WorkspaceMoveRecoverRequest{OperationID: workspaceHTTPOperation, PlanDigest: workspaceHTTPDigest, Confirm: true}
			default:
				kind := storagearchive.WorkspaceOperationArchive
				if action == "restore-apply" {
					kind = storagearchive.WorkspaceOperationRestore
				}
				review := workspaceArchiveHTTPReview(kind)
				review.BoxID = "box_company"
				body = storagearchive.WorkspaceMoveApplyRequest{Plan: review, PlanDigest: workspaceHTTPDigest, Confirm: true}
			}
			payload, _ := json.Marshal(body)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/v1/storage/workspace-archive/"+action+query, bytes.NewReader(payload)))
			if w.Code != http.StatusOK || primary.archivePlanActor != "" || primary.restorePlanActor != "" || primary.replanCalls+primary.applyCalls+primary.inspectCalls+primary.recoverCalls != 0 {
				t.Fatalf("dispatch status=%d primary=%+v company=%+v body=%s", w.Code, primary, company, w.Body.String())
			}
		})
	}
}

func TestBoxLifecycleSelectionAndUnavailableOwnersNeverFallBack(t *testing.T) {
	for _, query := range []string{"all_boxes=true", "box_id=box_personal&box_id=box_company", "box_id=unknown", "box_id=box_company"} {
		t.Run(query, func(t *testing.T) {
			primary := &workspaceArchiveHTTPServiceFake{}
			s := workspaceArchiveTestServer(primary, &workspaceArchiveAuthorizerFake{})
			s.services.Boxes = lifecycleTestCatalog()
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/plan?"+query, strings.NewReader(`{"kind":"topic","object_id":"topic_object","slug":"topic-one","reason":"archive"}`)))
			if w.Code < 400 || primary.archivePlanActor != "" {
				t.Fatalf("fallback: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestBoxLifecycleSavedArchiveCannotRecoverThroughForeignOwner(t *testing.T) {
	company := &boxLifecycleArchiveFake{boxID: "box_company", receiptBoxID: "box_personal"}
	s := workspaceArchiveTestServer(nil, &workspaceArchiveAuthorizerFake{})
	s.services.Boxes = lifecycleTestCatalog()
	s.services.BoxWorkspaceArchives = map[string]WorkspaceArchiveLifecycleService{"box_company": company}
	body, _ := json.Marshal(storagearchive.WorkspaceMoveRecoverRequest{OperationID: workspaceHTTPOperation, PlanDigest: workspaceHTTPDigest, Confirm: true})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/recover?box_id=box_company", bytes.NewReader(body)))
	if w.Code != http.StatusConflict || company.recoverCalls != 0 {
		t.Fatalf("foreign recovery: %d %s", w.Code, w.Body.String())
	}
	if err := s.validateLifecycleBoxID(lifecycleTestContext("box_company"), ""); err == nil {
		t.Fatal("Company adopted identity-free Personal history")
	}
	if err := s.validateLifecycleBoxID(lifecycleTestContext("box_personal"), ""); err != nil {
		t.Fatal("legacy Personal history refused", err)
	}
}

func TestBoxLifecycleOwnerFactoriesMustMatchCatalogRoots(t *testing.T) {
	s := workspaceArchiveTestServer(nil, &workspaceArchiveAuthorizerFake{})
	s.services.Boxes = lifecycleTestCatalog()
	primary, company := s.services.Boxes.Boxes[0], s.services.Boxes.Boxes[1]
	s.services.WorkspacePaths = &storagearchive.WorkspacePathService{Roots: lifecycleTestRoots(primary)}
	s.services.BoxWorkspacePaths = map[string]*storagearchive.WorkspacePathService{"box_company": {Roots: lifecycleTestRoots(company)}}
	owner, err := s.lifecycleWorkspacePaths(lifecycleTestContext(company.ID))
	if err != nil || owner.Roots.BoxID != company.ID {
		t.Fatal(owner, err)
	}
	s.services.BoxWorkspacePaths[company.ID].Roots = lifecycleTestRoots(primary)
	if _, err := s.lifecycleWorkspacePaths(lifecycleTestContext(company.ID)); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatal("wrong roots accepted", err)
	}
	s.services.BoxWorkspaceArchives = map[string]WorkspaceArchiveLifecycleService{company.ID: storagearchive.WorkspaceMoveService{Roots: lifecycleTestRoots(primary)}}
	if _, err := s.lifecycleWorkspaceArchive(lifecycleTestContext(company.ID)); !errors.Is(err, boxregistry.ErrUnavailable) {
		t.Fatal("wrong archive owner accepted", err)
	}
}

func TestBoxLifecycleStorageNamespaceRoundTripDoesNotChangeSavedPlan(t *testing.T) {
	s := workspaceArchiveTestServer(nil, nil)
	s.services.Boxes = lifecycleTestCatalog()
	ctx := lifecycleTestContext("box_company")
	prefix, err := s.lifecycleStoragePrefix(ctx)
	if err != nil || prefix != "Boxes/company" {
		t.Fatal(prefix, err)
	}
	global := "Boxes/company/Projects/retained-child"
	relative, err := boundStoragePath(prefix, global)
	if err != nil || relative != "Projects/retained-child" {
		t.Fatal(relative, err)
	}
	for _, foreign := range []string{"Projects/retained-child", "Boxes/personal/Projects/child", "Boxes/company-other/Projects/child", "Boxes/company/../personal/child", "/Boxes/company/child", "Boxes/company"} {
		if _, err := boundStoragePath(prefix, foreign); err == nil {
			t.Fatal("foreign namespace accepted", foreign)
		}
	}
	plan := storagearchive.WorkspacePathPlan{Kind: "retain_child", ProjectID: "project_one", Digest: "saved-hash", Move: storagearchive.RetainedRelocationPlan{ID: "saved-id", BoxID: "box_company", Source: storagearchive.WorkspacePathRef{Root: "box", RelativePath: "Projects/one/child"}, Destination: storagearchive.WorkspacePathRef{Root: "storage", RelativePath: relative}}}
	before, _ := json.Marshal(plan)
	summary, err := s.lifecyclePathSummary(ctx, plan, "planned")
	if err != nil || summary["from_root"] != "box:box_company" || summary["to_root"] != "storage" || summary["destination"] != global || summary["id"] != "saved-id" || summary["plan_hash"] != "saved-hash" {
		t.Fatalf("summary %#v %v", summary, err)
	}
	if !(workspacePathRequest{FromRoot: "box:box_company", ToRoot: "storage", From: "Projects/one/child", To: global}).matches(summary) {
		t.Fatal("outward request cannot replay its exact summary")
	}
	after, _ := json.Marshal(plan)
	if !bytes.Equal(before, after) {
		t.Fatal("summary rewrote the signed plan")
	}
	if _, err := s.lifecyclePathSummary(lifecycleTestContext("box_personal"), plan, "planned"); err == nil {
		t.Fatal("foreign plan projected as Personal")
	}
}

func TestBoxLifecycleWorkspacePathJSONRejectsForeignRootsAndStorageNamespace(t *testing.T) {
	s := workspaceArchiveTestServer(nil, &workspaceArchiveAuthorizerFake{})
	s.services.Boxes = lifecycleTestCatalog()
	s.services.Files = &workspacefiles.Service{}
	s.services.BoxWorkspacePaths = map[string]*storagearchive.WorkspacePathService{"box_company": {Roots: lifecycleTestRoots(s.services.Boxes.Boxes[1])}}
	for _, body := range []string{
		`{"action":"plan","kind":"retain_child","from_root":"box:box_company","to_root":"storage","from":"Projects/one/child","to":"Boxes/other/Projects/child"}`,
		`{"action":"plan","kind":"relocate_project","from_root":"box:box_company","to_root":"box","from":"Projects/one","to":"Projects/two"}`,
		`{"action":"plan","kind":"relocate_project","from_root":"box","to_root":"box:box_company","from":"Projects/one","to":"Projects/two"}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/files/workspace-path?box_id=box_company", strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), localFileTransportKey{}, true))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "workspace_archive.invalid_request") {
			t.Fatalf("additive root decoding or namespace check failed: %d %s", w.Code, w.Body.String())
		}
	}
}

type lifecycleProjectScopeFake struct {
	storagearchive.ProjectRuntimeProjectService
	calls int
	boxID string
}

func (f *lifecycleProjectScopeFake) GetProjectRegistrationStatus(ctx context.Context, _ string) (projects.ProjectRegistrationDetail, error) {
	f.calls++
	selection, _ := boxregistry.FromContext(ctx)
	if len(selection.BoxIDs) == 1 {
		f.boxID = selection.BoxIDs[0]
	}
	return projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", ProjectScopeID: "scope_project", ProjectScopeKey: "project:test"}}}, nil
}

func TestBoxLifecycleProjectPhysicalDispatchKeepsConcreteOptionalMethods(t *testing.T) {
	primary, policy, companyProjects := &projectPhysicalHTTPFake{}, &projectHTTPPolicy{}, &lifecycleProjectScopeFake{}
	s := projectHTTPServer(primary, policy)
	s.services.Boxes = lifecycleTestCatalog()
	company := s.services.Boxes.Boxes[1]
	s.services.BoxProjectArchives = map[string]storagearchive.ProjectRuntimeService{company.ID: {WorkspaceRoots: lifecycleTestRoots(company), Projects: companyProjects, RuntimeManifestRoot: "/state/company/runtime-manifests"}}
	owner, err := s.lifecycleProjectArchive(lifecycleTestContext(company.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := owner.(interface {
		ReactivateProject(context.Context, requestctx.Context, string, storagearchive.ProjectReactivationRequest) (storagearchive.ProjectReactivationResult, error)
	}); !ok {
		t.Fatal("concrete reactivation method lost")
	}
	for _, action := range []string{"plan", "apply", "recover", "reactivate", "retire-plan", "retire-apply"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/"+action+"?box_id="+company.ID, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.handleProjectPhysicalArchive(w, r, "test", storagearchive.WorkspaceOperationArchive, action)
		if w.Code < 400 || primary.calls != 0 || companyProjects.boxID != company.ID {
			t.Fatalf("wrong project owner action=%s status=%d calls=%d", action, w.Code, primary.calls)
		}
	}
	if companyProjects.calls != 6 {
		t.Fatal("bound owner was not used for every route", companyProjects.calls)
	}
}

func TestBoxLifecycleProjectPhysicalUnavailableAndBroadSelectionsRefuseBeforeDefaultOwner(t *testing.T) {
	for _, query := range []string{"box_id=box_company", "all_boxes=true", "box_id=box_company&box_id=box_personal"} {
		primary := &projectPhysicalHTTPFake{}
		s := projectHTTPServer(primary, &projectHTTPPolicy{})
		s.services.Boxes = lifecycleTestCatalog()
		r := httptest.NewRequest(http.MethodPost, "/v1/projects/test/archive/plan?"+query, strings.NewReader(`{"reason":"archive"}`))
		w := httptest.NewRecorder()
		s.handleProjectPhysicalArchive(w, r, "test", storagearchive.WorkspaceOperationArchive, "plan")
		if w.Code < 400 || primary.calls != 0 {
			t.Fatalf("default owner reached for %s: %d %s", query, w.Code, w.Body.String())
		}
	}
}

func TestBoxLifecycleRegisteredPrimaryKeepsLegacyDefaultArchiveOwner(t *testing.T) {
	primary := &workspaceArchiveHTTPServiceFake{}
	s := workspaceArchiveTestServer(primary, &workspaceArchiveAuthorizerFake{})
	s.services.Boxes = lifecycleTestCatalog()
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/storage/workspace-archive/plan?box_id=box_personal", strings.NewReader(`{"kind":"topic","object_id":"topic_object","slug":"topic-one","reason":"archive"}`)))
	if w.Code != http.StatusOK || primary.archivePlanActor != workspaceHTTPActorID {
		t.Fatalf("legacy primary owner refused: %d %s", w.Code, w.Body.String())
	}
}
