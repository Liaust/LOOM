package loomcli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/response"
)

func TestProjectReadRelativePathsPreserveExplicitOwnerAndIDs(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, tc := range []struct{ ref, node, want string }{
		{".", "", root}, {"./notes", "", filepath.Join(root, "notes")},
		{"..", "", filepath.Dir(root)}, {"../other", "", filepath.Join(filepath.Dir(root), "other")},
		{".", "main", "."}, {"./remote", "main", "./remote"},
		{"project_01ARZ3NDEKTSV4RRFFQ69G5FAV", "", "project_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		{"named-project", "", "named-project"}, {"/remote/project", "", "/remote/project"},
	} {
		got, err := declarationReadProjectRef(tc.ref, tc.node)
		if err != nil || got != tc.want {
			t.Fatalf("%q owner %q: got %q %v, want %q", tc.ref, tc.node, got, err, tc.want)
		}
	}
}

func TestProjectStatusAndContextSendCanonicalDotRead(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	for _, command := range []string{"status", "context"} {
		t.Run(command, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/v1/projects/"+root+"/declaration-status" || r.URL.Query().Get("node_ref") != "" {
					t.Errorf("unexpected owner read: %s %s", r.Method, r.RequestURI)
				}
				response.WriteJSON(w, 200, response.Success("read", contextFixture()))
			}))
			defer server.Close()
			cfg := writeProjectBackendInstallManifest(t, server.URL)
			_, stderr, err := executeRootCommand("--config", cfg, "--json", "project", command, ".")
			if err != nil || calls != 1 {
				t.Fatalf("reads=%d err=%v %s", calls, err, stderr)
			}
		})
	}
}
