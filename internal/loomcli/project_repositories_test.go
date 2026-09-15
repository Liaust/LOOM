package loomcli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectstate"
	"loom.local/loom/internal/response"
)

func TestProjectRepositoriesCommandsRenderOnlyBoundedState(t *testing.T) {
	observedAt := time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)
	repository := localclient.ProjectRepositoryItem{
		RepositoryID:             "repo_backend",
		RepositoryOwnerProjectID: "project_demo",
		Key:                      "backend",
		RelativePath:             "services/backend",
		Role:                     "primary",
		StateRoot:                ".repo",
		MembershipLifecycle:      "active",
		RepositoryLifecycle:      "active",
		ObservationPosture:       "observed",
		ObservedAt:               &observedAt,
		DevelopmentState:         localclient.ProjectRepositoryDevelopmentState{Posture: projectstate.DevelopmentStateEnabled, RelativePath: ".repo/repo.yaml"},
		Git: &localclient.ProjectRepositoryGit{
			CurrentBranch:                 "main",
			DefaultBranch:                 "main",
			DefaultBranchPosture:          projectstate.GitDefaultBranchObserved,
			Head:                          "0123456789abcdef",
			HeadPosture:                   projectstate.GitHeadObserved,
			Upstream:                      "origin/main",
			AheadBehindObserved:           true,
			Dirty:                         localclient.ProjectRepositoryGitDirty{Dirty: true, TrackedChanges: true},
			Worktree:                      true,
			WorktreeCanonicalProjectState: false,
		},
	}
	project := localclient.ProjectRepositoryProject{ProjectID: "project_demo", Slug: "demo", Lifecycle: "active"}
	source := localclient.ProjectRepositorySource{
		Posture:                      projectstate.SourcePostureRegistered,
		ProjectContractSchemaVersion: "project.contract.v0.4",
		ReposContractSchemaVersion:   "repos.contract.v0.4",
		SourceRevision:               2,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/projects/demo/repos":
			if r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("after") != "repo_before" {
				t.Fatalf("list query=%s", r.URL.RawQuery)
			}
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repos", localclient.ProjectRepositoryListResult{
				SchemaVersion: projectstate.SchemaVersion,
				Project:       project,
				Source:        source,
				Repositories:  []localclient.ProjectRepositoryItem{repository},
				Page:          localclient.ProjectRepositoryPage{Limit: 1, NextAfter: "repo_backend", HasMore: true},
				ObservedAt:    observedAt,
			}))
		case "/v1/projects/demo/repos/inspect/backend":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repos", localclient.ProjectRepositoryInspectResult{
				SchemaVersion: projectstate.SchemaVersion,
				Project:       project,
				Source:        source,
				Repository:    repository,
				ObservedAt:    observedAt,
			}))
		case "/v1/projects/demo/repos/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_repos", localclient.ProjectRepositoryStatusResult{
				SchemaVersion: projectstate.SchemaVersion,
				Project:       project,
				Source:        source,
				Observation: projectstate.ObservationSummary{
					Posture:     projects.ProjectRepositoryObservationObserved,
					MemberCount: 1,
					Observed:    1,
				},
				Repositories: []localclient.ProjectRepositoryItem{repository},
				Page:         localclient.ProjectRepositoryPage{Limit: 50},
				ObservedAt:   observedAt,
			}))
		default:
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)

	listOutput, listError, err := executeRootCommand("--config", configPath, "project", "repos", "list", "demo", "--limit", "1", "--after", "repo_before")
	if err != nil {
		t.Fatalf("list err=%v stderr=%s", err, listError)
	}
	for _, want := range []string{"Project: demo (project_demo)", "project.contract.v0.4", "repo_backend", "services/backend", "Next page: --after repo_backend --limit 1"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("list missing %q:\n%s", want, listOutput)
		}
	}

	inspectOutput, inspectError, err := executeRootCommand("--config", configPath, "project", "repos", "inspect", "demo", "backend")
	if err != nil {
		t.Fatalf("inspect err=%v stderr=%s", err, inspectError)
	}
	for _, want := range []string{"Repository ID: repo_backend", "Role: primary", "Freshness: observed", ".repo posture: enabled", "Git posture: main/dirty", "canonical_project_state=false"} {
		if !strings.Contains(inspectOutput, want) {
			t.Fatalf("inspect missing %q:\n%s", want, inspectOutput)
		}
	}
	for _, forbidden := range []string{"source_snapshot", "registration_plan", "project_root"} {
		if strings.Contains(inspectOutput, forbidden) {
			t.Fatalf("inspect exposed %q:\n%s", forbidden, inspectOutput)
		}
	}

	statusOutput, statusError, err := executeRootCommand("--config", configPath, "project", "repos", "status", "demo")
	if err != nil {
		t.Fatalf("status err=%v stderr=%s", err, statusError)
	}
	for _, want := range []string{"Status: observed (members=1 observed=1", "repo_backend", "main/dirty", "enabled"} {
		if !strings.Contains(statusOutput, want) {
			t.Fatalf("status missing %q:\n%s", want, statusOutput)
		}
	}
}

func TestProjectRepositoriesArchivedOutputIsReadOnlyAndCommandsHaveNoMutation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response.WriteJSON(w, http.StatusOK, response.Success("corr_archived", localclient.ProjectRepositoryStatusResult{
			SchemaVersion: projectstate.SchemaVersion,
			Project:       localclient.ProjectRepositoryProject{ProjectID: "project_archived", Slug: "archived", Lifecycle: "archived"},
			Source:        localclient.ProjectRepositorySource{Posture: projectstate.SourcePostureRegistered},
			Observation:   projectstate.ObservationSummary{Posture: projects.ProjectRepositoryObservationNotObserved},
			Repositories:  []localclient.ProjectRepositoryItem{},
			Page:          localclient.ProjectRepositoryPage{Limit: 50},
		}))
	}))
	defer server.Close()
	configPath := writeProjectBackendInstallManifest(t, server.URL)
	output, stderr, err := executeRootCommand("--config", configPath, "project", "repos", "status", "archived")
	if err != nil {
		t.Fatalf("status err=%v stderr=%s", err, stderr)
	}
	if !strings.Contains(output, "Access: read-only archived repository state; member mutation actions are unavailable") {
		t.Fatalf("archived output missing read-only posture:\n%s", output)
	}
	root := NewRootCommand()
	projectCommand, _, err := root.Find([]string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	reposCommand, _, err := projectCommand.Find([]string{"repos"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, command := range reposCommand.Commands() {
		got[command.Name()] = true
	}
	if len(got) != 3 || !got["list"] || !got["inspect"] || !got["status"] {
		t.Fatalf("repository subcommands=%v", got)
	}
}
