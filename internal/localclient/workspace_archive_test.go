package localclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func TestWorkspaceArchiveClientUsesTypedLifecycleRoutes(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.Method == http.MethodPost {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode body: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/storage/workspace-archive/plan" || r.URL.Path == "/v1/storage/workspace-archive/restore-plan" {
			_ = json.NewEncoder(w).Encode(response.Success("corr_test", storagearchive.WorkspaceMovePlanReview{OperationID: "workspace_archive_operation_test"}))
			return
		}
		_ = json.NewEncoder(w).Encode(response.Success("corr_test", storagearchive.WorkspaceMoveInspectionSummary{OperationID: "workspace_archive_operation_test"}))
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan := storagearchive.WorkspaceMovePlanReview{PlanDigest: "sha256:test"}
	if _, err := client.PlanWorkspaceArchive(ctx, "corr_test", storagearchive.WorkspaceArchivePlanRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyWorkspaceArchive(ctx, "corr_test", storagearchive.WorkspaceMoveApplyRequest{Plan: plan, PlanDigest: plan.PlanDigest, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectWorkspaceArchive(ctx, "corr_test", "workspace_archive_operation_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RecoverWorkspaceArchive(ctx, "corr_test", storagearchive.WorkspaceMoveRecoverRequest{OperationID: "workspace_archive_operation_test", PlanDigest: plan.PlanDigest, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PlanWorkspaceRestore(ctx, "corr_test", storagearchive.WorkspaceRestorePlanRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ApplyWorkspaceRestore(ctx, "corr_test", storagearchive.WorkspaceMoveApplyRequest{Plan: plan, PlanDigest: plan.PlanDigest, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /v1/storage/workspace-archive/plan",
		"POST /v1/storage/workspace-archive/apply",
		"GET /v1/storage/workspace-archive/inspect?operation_id=workspace_archive_operation_test",
		"POST /v1/storage/workspace-archive/recover",
		"POST /v1/storage/workspace-archive/restore-plan",
		"POST /v1/storage/workspace-archive/restore-apply",
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}
