package portal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

var _ projectPhysicalClient = localclient.Client{}

func TestProjectPhysicalConfirmationDoesNotChangeOtherActions(t *testing.T) {
	action := PortalAction{Executor: PortalActionExecutor{Kind: PortalExecutorWorkerRunOnce, Payload: map[string]string{"physical_archive": "true"}}, ConfirmationPolicy: ConfirmationPolicy{Required: true}}
	if !action.RequiresConfirmation() {
		t.Fatal("project metadata bypassed unrelated confirmation")
	}
}

func portalProjectReview(restore bool) storagearchive.ProjectPhysicalPlanReview {
	digest := "sha256:" + strings.Repeat("a", 64)
	w := storagearchive.WorkspaceMovePlanReview{SchemaVersion: storagearchive.WorkspaceMovePlanReviewSchemaVersion, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBD", OperationKind: storagearchive.WorkspaceOperationArchive, Kind: storagearchive.WorkspaceKindProject, ObjectID: "project_test", Slug: "test", Source: storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: "Projects/test"}, Destination: storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootStorage, RelativePath: "archive/projects/test/project"}, InventoryDigest: digest, ActorID: "actor_portal", Reason: "reviewed", PlannedAt: time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC), PlanDigest: digest}
	if restore {
		w.OperationKind = storagearchive.WorkspaceOperationRestore
		w.ArchiveOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBF"
		w.ArchiveManifestDigest = digest
		w.Source, w.Destination = w.Destination, w.Source
	}
	return storagearchive.ProjectPhysicalPlanReview{SchemaVersion: storagearchive.ProjectPhysicalPlanReviewSchemaVersion, ProjectID: "project_test", Request: requestctx.Context{ActorID: "actor_portal", OriginNodeID: "node_main", ScopeID: "scope_test", CorrelationID: "corr_plan"}, Workspace: w, RegistrationRevision: 1, PlanDigest: digest, ActivationState: storagearchive.WorkspaceActivationInactive}
}

func TestProjectPhysicalPortalReviewConfirmationAndPartialTruth(t *testing.T) {
	for _, restore := range []bool{false, true} {
		review := portalProjectReview(restore)
		plans, applies := 0, 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/plan") {
				plans++
				response.WriteJSON(w, 200, response.Success("corr_plan", review))
				return
			}
			if !strings.HasSuffix(r.URL.Path, "/apply") {
				t.Error("unexpected route")
				w.WriteHeader(400)
				return
			}
			applies++
			var in storagearchive.ProjectPhysicalApplyRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil || !in.Confirm || in.PlanDigest != review.PlanDigest || in.Plan.Workspace.OperationKind != review.Workspace.OperationKind {
				t.Error("unbound Portal apply")
			}
			response.WriteJSON(w, 409, struct {
				response.ErrorEnvelope
				Data storagearchive.ProjectPhysicalMutationSummary `json:"data"`
			}{response.ErrorEnvelope{Error: response.ErrorBody{Code: "project_archive.operation_failed"}}, storagearchive.ProjectPhysicalMutationSummary{OperationID: review.Workspace.OperationID, PlanDigest: review.PlanDigest, Phase: "project_state_pending", MutationBlocked: true, Recoverable: true, ActivationState: storagearchive.WorkspaceActivationInactive}})
		}))
		client, _ := localclient.NewHTTP(server.URL)
		detail := projects.ProjectRegistrationDetail{Project: projects.ProjectDetail{Project: projects.Project{ProjectID: "project_test", Slug: "test", Status: "active"}}, Registration: &projects.ProjectContractRegistration{}}
		action := NewProjectArchiveAction(detail, ScreenProjects)
		if restore {
			detail.Project.Project.Status = "archived"
			detail.Project.Project.ArchiveState = portalPhysicalArchiveState(t, "")
			action = NewProjectRestoreReactivateAction(detail, ScreenProjects)
		}
		action.InputValues["reason"] = "reviewed"
		if action.Disabled() || action.RequiresConfirmation() {
			t.Fatalf("read-only review should be available: %+v", action)
		}
		result, err := ExecutePortalAction(context.Background(), client, "corr_plan", action, false)
		if err != nil || result.Status != ActionLifecycleReview || result.Blocking || plans != 1 || applies != 0 || !hasActionField(result.Fields, "Runtime", "inactive") {
			t.Fatalf("review: %+v %v", result, err)
		}
		// Exercise the real model's NextInput propagation and review -> confirmation.
		model := Model{keymap: DefaultKeyMap(), actionPanel: ActionPanelState{Action: action}}
		next, _ := model.handleActionExecuted(actionExecutedMsg{Result: result})
		model = next.(Model)
		next, _ = model.updateActionPanel(tea.KeyMsg{Type: tea.KeyEnter})
		model = next.(Model)
		if model.actionPanel.Lifecycle != ActionLifecycleNeedsConfirmation || !model.actionPanel.Action.RequiresConfirmation() {
			t.Fatal("review did not require separate confirmation")
		}
		action = model.actionPanel.Action
		if _, err := ExecutePortalAction(context.Background(), client, "corr_apply", action, false); !errors.Is(err, ErrActionConfirmationRequired) || applies != 0 {
			t.Fatal("unconfirmed reviewed apply ran")
		}
		result, err = ExecutePortalAction(context.Background(), client, "corr_apply", action, true)
		if err != nil || result.Status != ActionLifecycleFailed || result.ErrorCode != "project_archive.operation_failed" || !hasActionField(result.Fields, "Phase", "project_state_pending") || !hasActionField(result.Fields, "Runtime", "inactive") || applies != 1 {
			t.Fatalf("partial result: %+v %v", result, err)
		}
		action.InputValues["plan_digest"] = "substituted"
		result, _ = ExecutePortalAction(context.Background(), client, "corr_retry", action, true)
		if result.Status != ActionLifecycleFailed || applies != 1 {
			t.Fatal("edited review reached mutation")
		}
		server.Close()
	}
}

func TestProjectPhysicalPortalRecoveryReviewAndConfirmation(t *testing.T) {
	for _, kind := range []string{"archive", "restore"} {
		recoveries := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/inspect") {
				response.WriteJSON(w, 200, response.Success("corr", storagearchive.ProjectArchiveInspectResult{Physical: &storagearchive.ProjectPhysicalArchiveInspection{ProjectID: "project_test", OperationID: "retained_op", PlanDigest: "retained_digest", Phase: "project_state_pending", EvidenceStatus: "pending", NextAction: "recover_" + kind, MutationBlocked: true}}))
				return
			}
			want := "/v1/projects/test/archive/"
			if kind == "restore" {
				want += "restore/"
			}
			want += "recover"
			if r.URL.Path != want {
				t.Errorf("route=%s want=%s", r.URL.Path, want)
			}
			var input storagearchive.ProjectPhysicalRecoverRequest
			_ = json.NewDecoder(r.Body).Decode(&input)
			if !input.Confirm || input.OperationID != "retained_op" || input.PlanDigest != "retained_digest" {
				t.Error("lost recovery binding")
			}
			recoveries++
			response.WriteJSON(w, 200, response.Success("corr", storagearchive.ProjectPhysicalMutationSummary{OperationID: "retained_op", Phase: "complete", ActivationState: storagearchive.WorkspaceActivationInactive, MutationBlocked: true}))
		}))
		client, _ := localclient.NewHTTP(server.URL)
		action := PortalAction{ID: "inspect", Domain: "projects", TargetRef: "test", Executor: PortalActionExecutor{Kind: PortalExecutorProjectArchiveInspect, Target: "test"}, InputValues: map[string]string{}}
		result, err := ExecutePortalAction(context.Background(), client, "corr", action, false)
		if err != nil || result.Status != ActionLifecycleReview || recoveries != 0 {
			t.Fatalf("inspection %v %+v", err, result)
		}
		for k, v := range result.NextInput {
			action.InputValues[k] = v
		}
		if _, err := ExecutePortalAction(context.Background(), client, "corr", action, false); !errors.Is(err, ErrActionConfirmationRequired) {
			t.Fatal("recovery ran without confirmation")
		}
		result, err = ExecutePortalAction(context.Background(), client, "corr", action, true)
		if err != nil || recoveries != 1 || result.Status != ActionLifecycleSucceeded || !hasActionField(result.Fields, "Runtime", "inactive") {
			t.Fatalf("recovery %v %+v", err, result)
		}
		server.Close()
	}
}
