package storagearchive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
)

type projectSurfaceEvidenceStore struct {
	mu        sync.Mutex
	rows      map[string]projects.ProjectPlanEvidence
	failSave  bool
	afterSave func()
}

func (s *projectSurfaceEvidenceStore) SaveProjectPlanEvidence(_ context.Context, e projects.ProjectPlanEvidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSave {
		return errors.New("private database failure /private/do-not-publish")
	}
	if old, ok := s.rows[e.OperationID]; ok && !reflect.DeepEqual(old, e) {
		return projects.ErrProjectPlanEvidenceConflict
	}
	e.Payload = append([]byte(nil), e.Payload...)
	s.rows[e.OperationID] = e
	if s.afterSave != nil {
		s.afterSave()
	}
	return nil
}

func (s *projectSurfaceEvidenceStore) LoadProjectPlanEvidence(_ context.Context, projectID, op string) (projects.ProjectPlanEvidence, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.rows[op]
	if !ok || e.ProjectID != projectID {
		return projects.ProjectPlanEvidence{}, false, nil
	}
	e.Payload = append([]byte(nil), e.Payload...)
	return e, true, nil
}

func newProjectSurfaceEnvironment(t *testing.T) (projectArchivePlanEnvironment, *projectSurfaceEvidenceStore, requestctx.Context) {
	t.Helper()
	env, _ := newProjectArchiveApplyEnvironment(t)
	store := &projectSurfaceEvidenceStore{rows: make(map[string]projects.ProjectPlanEvidence)}
	env.service.PlanEvidence = store
	env.workspace.planner.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 30, 0, 0, time.UTC) }
	req := projectArchivePlanRequest()
	req.ScopeID = env.projects.detail.Project.Project.ProjectScopeID
	return env, store, req
}

func projectSurfaceReview(t *testing.T, env projectArchivePlanEnvironment, req requestctx.Context) ProjectPhysicalPlanReview {
	t.Helper()
	review, err := env.service.ReviewProjectPhysicalArchive(context.Background(), req, "canonical-many", ProjectPhysicalArchivePlanInput{Reason: "archive completed fixture"})
	if err != nil {
		_, cause := env.service.PlanProjectPhysicalArchive(context.Background(), req, "canonical-many", ProjectPhysicalArchivePlanInput{Reason: "archive completed fixture"})
		t.Fatalf("review: %v; underlying plan: %v", err, cause)
	}
	env.workspace.planner.Now = func() time.Time { return time.Date(2026, 9, 4, 10, 34, 0, 0, time.UTC) }
	return review
}

func TestProjectPhysicalReviewKeepsPlannerCausePrivate(t *testing.T) {
	env, _, req := newProjectSurfaceEnvironment(t)
	env.service.Activation = nil
	_, err := env.service.ReviewProjectPhysicalArchive(context.Background(), req, "canonical-many", ProjectPhysicalArchivePlanInput{})
	var failure ProjectPhysicalSurfaceError
	if !errors.As(err, &failure) || failure.Code != "plan_unavailable" || errors.Unwrap(err) == nil {
		t.Fatalf("planner cause was discarded: %v", err)
	}
	if got := errors.Unwrap(err).Error(); got != "project archive activation service is not configured" {
		t.Fatalf("unexpected cause: %s", got)
	}
	raw, marshalErr := json.Marshal(failure)
	if marshalErr != nil || strings.Contains(string(raw), "activation") || strings.Contains(err.Error(), "activation") {
		t.Fatalf("private cause exposed: %s / %v", raw, err)
	}
}

func applyProjectSurfaceReview(t *testing.T, env projectArchivePlanEnvironment, req requestctx.Context, review ProjectPhysicalPlanReview) ProjectPhysicalMutationSummary {
	t.Helper()
	result, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
	if err != nil {
		t.Fatalf("apply reviewed plan: %v (%+v)", err, result)
	}
	return result
}

func TestProjectPhysicalReviewPreservesArchiveAndRestoreAcrossServices(t *testing.T) {
	env, store, req := newProjectSurfaceEnvironment(t)
	review := projectSurfaceReview(t, env, req)
	if len(store.rows) != 0 || env.projects.transitions != 0 {
		t.Fatal("planning mutated persistence")
	}
	raw, _ := json.Marshal(review)
	for _, private := range []string{env.service.WorkspaceRoots.BoxRoot, env.service.WorkspaceRoots.StorageRoot, "registration_source", "runtime_config", "device_id", "inode", "\"entries\":["} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private review data: %q", private)
		}
	}
	if len(raw) > MaximumWorkspaceSurfaceRequestBytes {
		t.Fatal("review exceeded request bound")
	}
	// A real transport round trip must preserve every review field.
	if err := json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	live := req
	live.CorrelationID = "corr_apply_attempt"
	archived := applyProjectSurfaceReview(t, env, live, review)
	before := env.projects.transitions
	if _, err := env.service.RecoverReviewedProjectPhysicalKind(context.Background(), live, review.ProjectID, ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true}, WorkspaceOperationRestore); err == nil || env.projects.transitions != before {
		t.Fatal("archive evidence accepted on restore recovery route")
	}
	if archived.Phase != "complete" || !archived.MutationBlocked || archived.ActivationState != WorkspaceActivationInactive || len(store.rows) != 1 {
		t.Fatalf("archive result: %+v", archived)
	}
	if _, err := os.Stat(env.service.WorkspaceRoots.BoxRoot + "/Projects/canonical-many"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active source retained: %v", err)
	}
	// No private full plan is carried by the caller or retained service instance.
	env.service = NewProjectRuntimeService(ProjectRuntimeDeps{Projects: env.projects, RepositoryState: env.repositories, Activation: env.activation, WorkspaceMove: env.workspace, RuntimeQuiescence: env.quiescence, WorkspaceRoots: env.service.WorkspaceRoots, PlanEvidence: store})
	replayed := applyProjectSurfaceReview(t, env, live, review)
	if !replayed.Replay {
		t.Fatalf("archive replay: %+v", replayed)
	}
	planner := &projectRestorePlanTestPlanner{WorkspaceMoveService: env.workspace.planner}
	planner.Now = func() time.Time { return time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC) }
	env.service.WorkspaceMove = planner
	env.service.Now = planner.Now
	restoreReq := projectRestorePlanRequest()
	restoreReq.ScopeID = req.ScopeID
	restore, err := env.service.ReviewProjectPhysicalRestore(context.Background(), restoreReq, review.ProjectID, projectRestorePlanInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 1 {
		t.Fatal("restore planning wrote evidence")
	}
	restored := applyProjectSurfaceReview(t, env, restoreReq, restore)
	if restored.Phase != "complete" || !restored.MutationBlocked || restored.ActivationState != WorkspaceActivationInactive || len(store.rows) != 2 {
		t.Fatalf("restore: %+v", restored)
	}
	restoreReq.CorrelationID = "corr_restore_recovery_attempt"
	before = env.projects.transitions
	if _, err := env.service.RecoverReviewedProjectPhysicalKind(context.Background(), restoreReq, review.ProjectID, ProjectPhysicalRecoverRequest{OperationID: restore.Workspace.OperationID, PlanDigest: restore.PlanDigest, Confirm: true}, WorkspaceOperationArchive); err == nil || env.projects.transitions != before {
		t.Fatal("restore evidence accepted on archive recovery route")
	}
	recovered, err := env.service.RecoverReviewedProjectPhysicalKind(context.Background(), restoreReq, review.ProjectID, ProjectPhysicalRecoverRequest{OperationID: restore.Workspace.OperationID, PlanDigest: restore.PlanDigest, Confirm: true}, WorkspaceOperationRestore)
	if err != nil || !recovered.Replay || recovered.ActivationState != WorkspaceActivationInactive {
		t.Fatalf("restore recovery replay: %+v %v", recovered, err)
	}
}

func TestProjectPhysicalCanonicalScopeResolution(t *testing.T) {
	env, _, _ := newProjectSurfaceEnvironment(t)
	env.projects.detail.Project.Project.ProjectScopeKey = "project:canonical-many"
	id, scope, key, err := env.service.ProjectPhysicalScope(context.Background(), "canonical-many")
	if err != nil || id != env.projects.detail.Project.Project.ProjectID || scope != env.projects.detail.Project.Project.ProjectScopeID || key != "project:canonical-many" {
		t.Fatalf("canonical scope %s %s %s %v", id, scope, key, err)
	}
	env.projects.detail.Project.Project.ProjectScopeID = ""
	if _, _, _, err := env.service.ProjectPhysicalScope(context.Background(), "canonical-many"); err == nil {
		t.Fatal("missing scope accepted")
	}
	if _, _, _, err := (ProjectRuntimeService{}).ProjectPhysicalScope(context.Background(), "canonical-many"); err == nil {
		t.Fatal("unready resolver accepted")
	}
}

func TestProjectPhysicalReviewRejectsCallerAndFieldSubstitutionBeforeWrites(t *testing.T) {
	mutations := map[string]func(*ProjectPhysicalPlanReview){
		"schema":             func(r *ProjectPhysicalPlanReview) { r.SchemaVersion += "x" },
		"plan_digest":        func(r *ProjectPhysicalPlanReview) { r.PlanDigest = "sha256:" + strings.Repeat("a", 64) },
		"registration":       func(r *ProjectPhysicalPlanReview) { r.RegistrationRevision++ },
		"repository":         func(r *ProjectPhysicalPlanReview) { r.RepositorySourceRevision++ },
		"deactivation_count": func(r *ProjectPhysicalPlanReview) { r.DeactivationActions++ },
		"facet_count":        func(r *ProjectPhysicalPlanReview) { r.DeactivationFacets++ },
		"relative_path":      func(r *ProjectPhysicalPlanReview) { r.Workspace.Destination.RelativePath += "-other" },
		"absolute_path":      func(r *ProjectPhysicalPlanReview) { r.Workspace.Source.AbsolutePath = "/private/secret" },
		"inventory_count":    func(r *ProjectPhysicalPlanReview) { r.Workspace.InventoryEntries++ },
		"activation":         func(r *ProjectPhysicalPlanReview) { r.ActivationState = "active" },
		"actor":              func(r *ProjectPhysicalPlanReview) { r.Request.ActorID += "X" },
		"actor_key":          func(r *ProjectPhysicalPlanReview) { r.Request.ActorKey = "other" },
		"node":               func(r *ProjectPhysicalPlanReview) { r.Request.OriginNodeID += "X" },
		"node_key":           func(r *ProjectPhysicalPlanReview) { r.Request.OriginNodeKey = "other" },
		"scope":              func(r *ProjectPhysicalPlanReview) { r.Request.ScopeID += "X" },
		"scope_key":          func(r *ProjectPhysicalPlanReview) { r.Request.ScopeKey = "other" },
		"freshness":          func(r *ProjectPhysicalPlanReview) { r.Request.FreshnessMode = "other" },
		"source":             func(r *ProjectPhysicalPlanReview) { r.Request.Source = "other" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			env, store, req := newProjectSurfaceEnvironment(t)
			review := projectSurfaceReview(t, env, req)
			mutate(&review)
			_, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
			if err == nil || len(store.rows) != 0 || env.projects.transitions != 0 {
				t.Fatalf("substitution reached mutation: %v", err)
			}
		})
	}
}

func TestProjectPhysicalReviewEvidenceFailureAndCrashBeforeIntent(t *testing.T) {
	env, store, req := newProjectSurfaceEnvironment(t)
	review := projectSurfaceReview(t, env, req)
	input := ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true}
	store.failSave = true
	if _, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, input); err == nil || strings.Contains(err.Error(), "/private") {
		t.Fatalf("private failure: %v", err)
	}
	if env.projects.transitions != 0 {
		t.Fatal("save failure reached lifecycle")
	}
	store.failSave = false
	store.afterSave = func() { panic("fixture crash after durable evidence") }
	func() {
		defer func() {
			if recover() == nil {
				t.Error("crash not reached")
			}
		}()
		_, _ = env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, input)
	}()
	store.afterSave = nil
	if len(store.rows) != 1 || env.projects.transitions != 0 {
		t.Fatal("wrong evidence-only crash boundary")
	}
	_, err := env.service.RecoverReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, ProjectPhysicalRecoverRequest{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Confirm: true})
	if err == nil || env.projects.transitions != 0 {
		t.Fatal("recover started an operation with no intent")
	}
	applyProjectSurfaceReview(t, env, req, review)
}

func TestProjectPhysicalReviewPartialRecoveryAndEvidenceTamper(t *testing.T) {
	env, store, req := newProjectSurfaceEnvironment(t)
	review := projectSurfaceReview(t, env, req)
	env.service.ArchiveFailureHook = func(b ProjectPhysicalArchiveFailureBoundary) error {
		if b == ProjectArchiveBoundaryAfterWorkspaceMove {
			return errors.New("fixture stop")
		}
		return nil
	}
	result, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest, Confirm: true})
	if err == nil || !result.Recoverable || !result.MutationBlocked {
		t.Fatalf("partial truth: %+v %v", result, err)
	}
	env.service.ArchiveFailureHook = nil
	e := store.rows[review.Workspace.OperationID]
	store.rows[e.OperationID] = projects.ProjectPlanEvidence{OperationID: e.OperationID, ProjectID: e.ProjectID, Payload: []byte("bad")}
	before := env.projects.transitions
	recovery := ProjectPhysicalRecoverRequest{OperationID: e.OperationID, PlanDigest: e.PlanDigest, Confirm: true}
	if _, err := env.service.RecoverReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, recovery); err == nil || env.projects.transitions != before {
		t.Fatal("tampered evidence recovered")
	}
	store.rows[e.OperationID] = e
	result, err = env.service.RecoverReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, recovery)
	if err != nil || result.Phase != "complete" {
		t.Fatalf("recovery: %+v %v", result, err)
	}
}

func TestProjectPhysicalReviewNotReadyConfirmationAndStaleSource(t *testing.T) {
	env, store, req := newProjectSurfaceEnvironment(t)
	var absent *projectSurfaceEvidenceStore
	env.service.PlanEvidence = absent
	if _, err := env.service.ReviewProjectPhysicalArchive(context.Background(), req, "canonical-many", ProjectPhysicalArchivePlanInput{}); err == nil {
		t.Fatal("typed nil evidence store accepted")
	}
	env.service.PlanEvidence = store
	review := projectSurfaceReview(t, env, req)
	input := ProjectPhysicalApplyRequest{Plan: review, PlanDigest: review.PlanDigest}
	if _, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, input); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	input.Confirm = true
	if err := os.WriteFile(env.canonicalRoot+"/README.md", []byte("changed fixture source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.service.ApplyReviewedProjectPhysicalPlan(context.Background(), req, review.ProjectID, input); err == nil {
		t.Fatal("stale source accepted")
	}
	if len(store.rows) != 0 || env.projects.transitions != 0 {
		t.Fatal("refused request wrote evidence or intent")
	}
}

func TestProjectPhysicalReviewPreservesProjectsWithoutRepositorySource(t *testing.T) {
	env, _, req := newProjectSurfaceEnvironment(t)
	env.repositories.state.Source = nil
	env.repositories.state.Members = nil
	review := projectSurfaceReview(t, env, req)
	if review.RepositorySourceRevision != 0 {
		t.Fatal("invented repository source revision")
	}
	if result := applyProjectSurfaceReview(t, env, req, review); result.Phase != "complete" {
		t.Fatalf("archive without repository source: %+v", result)
	}
}
