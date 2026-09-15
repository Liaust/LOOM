package localclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"loom.local/loom/internal/response"
)

func TestProjectRepositoryClientOperationsUseBoundedProjectRoutes(t *testing.T) {
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		switch r.URL.EscapedPath() {
		case "/v1/projects/project%20one/repos":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", ProjectRepositoryListResult{
				Project:      ProjectRepositoryProject{ProjectID: "project_one", Slug: "one", Lifecycle: "active"},
				Repositories: []ProjectRepositoryItem{{RepositoryID: "repo_one", Key: "backend", RelativePath: "backend", Role: "primary"}},
				Page:         ProjectRepositoryPage{Limit: 20},
			}))
		case "/v1/projects/project%20one/repos/inspect/repo%2Fone":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", ProjectRepositoryInspectResult{
				Repository: ProjectRepositoryItem{RepositoryID: "repo_one", Key: "backend"},
			}))
		case "/v1/projects/project%20one/repos/status":
			response.WriteJSON(w, http.StatusOK, response.Success("corr_test", ProjectRepositoryStatusResult{
				Project:      ProjectRepositoryProject{ProjectID: "project_one", Lifecycle: "archived"},
				Repositories: []ProjectRepositoryItem{},
				Page:         ProjectRepositoryPage{Limit: 10, After: "repo_one"},
			}))
		default:
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
	}))
	defer server.Close()
	client, err := NewHTTP(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	listed, err := client.ListProjectRepositories(context.Background(), "corr_test", "project one", ProjectRepositoryPageOptions{Limit: 20})
	if err != nil || len(listed.Data.Repositories) != 1 || listed.Data.Repositories[0].RepositoryID != "repo_one" {
		t.Fatalf("list=%#v err=%v", listed.Data, err)
	}
	inspected, err := client.InspectProjectRepository(context.Background(), "corr_test", "project one", "repo/one")
	if err != nil || inspected.Data.Repository.Key != "backend" {
		t.Fatalf("inspect=%#v err=%v", inspected.Data, err)
	}
	status, err := client.GetProjectRepositoryStatus(context.Background(), "corr_test", "project one", ProjectRepositoryPageOptions{Limit: 10, After: "repo_one"})
	if err != nil || status.Data.Project.Lifecycle != "archived" {
		t.Fatalf("status=%#v err=%v", status.Data, err)
	}
	want := []string{
		"GET /v1/projects/project%20one/repos?limit=20",
		"GET /v1/projects/project%20one/repos/inspect/repo%2Fone",
		"GET /v1/projects/project%20one/repos/status?after=repo_one&limit=10",
	}
	if len(seen) != len(want) {
		t.Fatalf("seen=%#v", seen)
	}
	for index := range want {
		if seen[index] != want[index] {
			t.Fatalf("request[%d]=%q, want %q", index, seen[index], want[index])
		}
	}
}
