package loomcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func projectCLIReview(restore bool) storagearchive.ProjectPhysicalPlanReview {
	digest := "sha256:" + strings.Repeat("a", 64)
	w := storagearchive.WorkspaceMovePlanReview{SchemaVersion: storagearchive.WorkspaceMovePlanReviewSchemaVersion, OperationID: "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBD", OperationKind: storagearchive.WorkspaceOperationArchive, Kind: storagearchive.WorkspaceKindProject, ObjectID: "project_test", Slug: "test", Source: storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootBox, RelativePath: "Projects/test"}, Destination: storagearchive.WorkspacePathRef{Root: storagearchive.WorkspaceRootStorage, RelativePath: "archive/projects/test/project"}, InventoryDigest: digest, ActorID: "actor_cli", Reason: "reviewed", PlannedAt: time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC), PlanDigest: digest}
	if restore {
		w.OperationKind = storagearchive.WorkspaceOperationRestore
		w.ArchiveOperationID = "workspace_archive_operation_01ARZ3NDEKTSV4RRFFQ69G5FBF"
		w.ArchiveManifestDigest = digest
		w.Source, w.Destination = w.Destination, w.Source
	}
	return storagearchive.ProjectPhysicalPlanReview{SchemaVersion: storagearchive.ProjectPhysicalPlanReviewSchemaVersion, ProjectID: "project_test", Request: requestctx.Context{ActorID: "actor_cli", OriginNodeID: "node_main", ScopeID: "scope_test", CorrelationID: "corr_plan"}, Workspace: w, RegistrationRevision: 1, PlanDigest: digest, ActivationState: storagearchive.WorkspaceActivationInactive}
}

func TestProjectPhysicalCLIReviewApplyRecoverParity(t *testing.T) {
	for _, restore := range []bool{false, true} {
		review := projectCLIReview(restore)
		var paths []string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			paths = append(paths, r.URL.Path)
			if strings.HasSuffix(r.URL.Path, "/plan") {
				response.WriteJSON(w, 200, response.Success("corr", review))
				return
			}
			if strings.HasSuffix(r.URL.Path, "/apply") {
				var in storagearchive.ProjectPhysicalApplyRequest
				_ = json.NewDecoder(r.Body).Decode(&in)
				if !in.Confirm || in.PlanDigest != review.PlanDigest || in.Plan.Workspace.OperationKind != review.Workspace.OperationKind {
					t.Error("lost exact review")
				}
			}
			if strings.HasSuffix(r.URL.Path, "/recover") {
				var in storagearchive.ProjectPhysicalRecoverRequest
				_ = json.NewDecoder(r.Body).Decode(&in)
				if !in.Confirm || in.OperationID != review.Workspace.OperationID || in.PlanDigest != review.PlanDigest {
					t.Error("lost exact recovery")
				}
			}
			response.WriteJSON(w, 409, struct {
				response.ErrorEnvelope
				Data storagearchive.ProjectPhysicalMutationSummary `json:"data"`
			}{response.ErrorEnvelope{Error: response.ErrorBody{Code: "project_archive.operation_failed"}}, storagearchive.ProjectPhysicalMutationSummary{OperationID: review.Workspace.OperationID, Phase: "project_state_pending", MutationBlocked: true, Recoverable: true, ActivationState: storagearchive.WorkspaceActivationInactive}})
		}))
		config := writeProjectBackendInstallManifest(t, server.URL)
		prefix := []string{"--config", config, "--json", "project", "archive"}
		wantPath := "/v1/projects/test/archive/"
		if restore {
			prefix = append(prefix, "restore")
			wantPath += "restore/"
		}
		out, stderr, err := executeRootCommand(append(append([]string{}, prefix...), "plan", "test", "--reason", "reviewed")...)
		if err != nil {
			t.Fatalf("plan %v %s", err, stderr)
		}
		path := filepath.Join(t.TempDir(), "review.json")
		if err := os.WriteFile(path, []byte(out), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProjectPhysicalReview(path); err != nil {
			t.Fatalf("plan output cannot be applied: %v", err)
		}
		for _, action := range []string{"apply", "recover"} {
			target := path
			if action == "recover" {
				target = review.Workspace.OperationID
			}
			out, _, err = executeRootCommand(append(append([]string{}, prefix...), action, "test", target, "--plan-digest", review.PlanDigest, "--yes")...)
			var failed struct {
				OK    bool
				Data  storagearchive.ProjectPhysicalMutationSummary
				Error response.ErrorBody
			}
			if json.Unmarshal([]byte(out), &failed) != nil || err == nil || failed.OK || failed.Data.Phase != "project_state_pending" || !failed.Data.Recoverable || failed.Error.Code != "project_archive.operation_failed" {
				t.Fatalf("partial CLI result lost: %s %v", out, err)
			}
		}
		if strings.Join(paths, ",") != wantPath+"plan,"+wantPath+"apply,"+wantPath+"recover" {
			t.Fatalf("routes %v", paths)
		}
		server.Close()
	}
}

func TestProjectPhysicalCLIRequiresReviewAndConfirmation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	config := writeProjectBackendInstallManifest(t, server.URL)
	review := projectCLIReview(false)
	raw, _ := json.Marshal(review)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, raw, 0600)
	for _, restore := range []bool{false, true} {
		for _, action := range []string{"apply", "recover"} {
			prefix := []string{"--config", config, "project", "archive"}
			if restore {
				prefix = append(prefix, "restore")
			}
			if _, _, err := executeRootCommand(append(prefix, action, "test", good)...); err == nil {
				t.Fatal("missing confirmation accepted")
			}
		}
	}
	withPath := review
	withPath.Workspace.Source.AbsolutePath = "/private/source"
	privateRaw, _ := json.Marshal(withPath)
	for name, body := range map[string][]byte{"unknown": []byte(`{"unknown":true}`), "trailing": append(append([]byte{}, raw...), []byte(`{}`)...), "oversize": []byte(strings.Repeat(" ", storagearchive.MaximumWorkspaceSurfaceRequestBytes+1)), "raw_path": privateRaw} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProjectPhysicalReview(path); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectPhysicalReview(link); err == nil {
		t.Fatal("followed link")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProjectPhysicalReview(fifo); err == nil {
		t.Fatal("opened fifo as review")
	}
	if _, _, err := executeRootCommand("--config", config, "project", "archive", "restore", "apply", "test", good, "--plan-digest", review.PlanDigest, "--yes"); err == nil {
		t.Fatal("wrong-kind review accepted")
	}
	if requests != 0 {
		t.Fatal("invalid CLI input reached transport")
	}
}

func TestProjectPhysicalCLIHumanReviewShowsOnlyProjectDigest(t *testing.T) {
	review := projectCLIReview(false)
	review.Workspace.PlanDigest = "sha256:" + strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		response.WriteJSON(w, 200, response.Success("corr", review))
	}))
	defer server.Close()
	config := writeProjectBackendInstallManifest(t, server.URL)
	out, stderr, err := executeRootCommand("--config", config, "project", "archive", "plan", "test", "--reason", "reviewed")
	if err != nil || !strings.Contains(out, "Project plan digest: "+review.PlanDigest) || strings.Contains(out, review.Workspace.PlanDigest) || !strings.Contains(out, "Resulting runtime: inactive") {
		t.Fatalf("ambiguous review %s %s %v", out, stderr, err)
	}
}
