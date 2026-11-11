package loomcli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workspacefiles"
	"loom.local/loom/internal/workspacenotes"
)

func TestFilesRetainedOperatorRefusesNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root refusal check")
	}
	_, stderr, err := executeRootCommand("files", "relocate", "status", "saved", "--operator")
	if err == nil || !strings.Contains(stderr+err.Error(), "local root") {
		t.Fatalf("operator access was not refused: %s %v", stderr, err)
	}
}

func TestFilesMetadataCommandsUseBoundedOwnerActions(t *testing.T) {
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/links" {
			t.Errorf("wrong owner %s", r.URL.Path)
		}
		action := r.URL.Query().Get("action")
		if r.Method == http.MethodPost {
			var input map[string]string
			if e := json.NewDecoder(r.Body).Decode(&input); e != nil {
				t.Fatal(e)
			}
			action = input["action"]
			if action == "refresh_start" && input["path"] != "Notes" {
				t.Errorf("scope %v", input)
			}
		}
		actions = append(actions, action)
		response.WriteJSON(w, 200, response.Success("metadata", workspacenotes.Refresh{ID: "metadata_refresh_test", Root: "box", State: "ready"}))
	}))
	defer server.Close()
	config := writeProjectBackendInstallManifest(t, server.URL)
	for _, args := range [][]string{{"start", "--path", "Notes"}, {"step", "metadata_refresh_test"}, {"status", "metadata_refresh_test"}, {"retry", "metadata_refresh_test"}} {
		stdout, stderr, e := executeRootCommand(append([]string{"--config", config, "files", "metadata"}, args...)...)
		if e != nil || !strings.Contains(stdout, "metadata_refresh_test") {
			t.Fatalf("%s %s %v", stdout, stderr, e)
		}
	}
	if strings.Join(actions, ",") != "refresh_start,refresh_step,refresh_status,refresh_retry" {
		t.Fatal(actions)
	}
}

func TestFilesCommandsUseDirectFileAPI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/files/roots":
			response.WriteJSON(w, 200, response.Success("files", []workspacefiles.Root{{Ref: "box", Node: "main", Available: true, SourcePath: "/srv/loom/box"}}))
		case "/v1/files/entries":
			if r.URL.Query().Get("root") != "archive" || r.URL.Query().Get("path") != "old notes" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("show_ignored") != "true" {
				t.Errorf("list query: %s", r.URL.RawQuery)
			}
			response.WriteJSON(w, 200, response.Success("files", workspacefiles.Page{Root: "archive", Path: "old notes", Entries: []workspacefiles.Entry{{Name: "note.md", RelativePath: "old notes/note.md", Kind: "file", Size: 3}}, HasMore: true, NextCursor: "opaque_cursor"}))
		case "/v1/files/stat":
			response.WriteJSON(w, 200, response.Success("files", workspacefiles.Entry{Name: "note.md", RelativePath: "note.md", Kind: "file", Size: 3}))
		case "/v1/files/explain":
			if r.URL.Query().Get("root") != "archive" || r.URL.Query().Get("path") != "old notes/note.md" {
				t.Errorf("explain query: %s", r.URL.RawQuery)
			}
			response.WriteJSON(w, 200, response.Success("files", workspacefiles.Explanation{
				Root: "archive", Path: "old notes/note.md", SourcePath: "/archive/old notes/note.md", Source: workspacefiles.Entry{Kind: "file"}, ReadOnly: true, Verification: "not_checked",
				Intent:           workspacefiles.FileIntent{State: "unknown", Search: "unknown", Sources: map[string]workspacefiles.RuleSource{}},
				RootRegistration: "unknown", DeviceLocalAvailability: "unknown", AppliedConfig: "not_checked", BackupCoverage: "not_verified",
			}))
		case "/v1/files/content":
			if r.Header.Get("Range") != "bytes=2-4" {
				t.Errorf("range %q", r.Header.Get("Range"))
			}
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte{0, 1, 2})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	config := writeProjectBackendInstallManifest(t, server.URL)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"files", "roots"}, "/srv/loom/box"},
		{[]string{"files", "ls", "old notes", "--root", "archive", "--limit", "2", "--show-ignored"}, "Next page: --cursor opaque_cursor"},
		{[]string{"files", "stat", "note.md", "--json"}, `"kind":"file"`},
		{[]string{"files", "explain", "old notes/note.md", "--root", "archive"}, "Device-local availability: unknown"},
		{[]string{"files", "explain", "old notes/note.md", "--root", "archive", "--json"}, `"backup_coverage":"not_verified"`},
	} {
		args := append([]string{"--config", config}, tc.args...)
		stdout, stderr, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(stdout, tc.want) {
			t.Fatalf("%v: stdout=%q stderr=%q error=%v", tc.args, stdout, stderr, err)
		}
	}
	stdout, stderr, err := executeRootCommand("--config", config, "files", "read", "binary.pdf", "--offset", "2", "--length", "3")
	if err != nil || stdout != string([]byte{0, 1, 2}) {
		t.Fatalf("binary stream changed: %q %q %v", stdout, stderr, err)
	}
	if _, _, err := executeRootCommand("--config", config, "files", "read", "note.md", "--json"); err == nil {
		t.Fatal("binary content accepted JSON output")
	}
}

func TestFileExplainHumanOutputKeepsRulesAndEvidenceSeparate(t *testing.T) {
	stages, protect := []string{}, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/files/explain" || r.URL.Query().Get("root") != "box" || r.URL.Query().Get("path") != "Notes/private/paper.pdf" {
			t.Errorf("explain request: %s", r.URL)
		}
		response.WriteJSON(w, 200, response.Success("files", workspacefiles.Explanation{
			SourcePath: "/box/Notes/private/paper.pdf", Source: workspacefiles.Entry{Kind: "file"},
			Intent: workspacefiles.FileIntent{State: "known", Search: "metadata", Enrichment: &stages, Protect: &protect, Sources: map[string]workspacefiles.RuleSource{
				"search": {Descriptor: ".loom/box.yaml", Field: "files.rules.search", Pattern: "Notes/private"},
			}},
			RootRegistration: "unknown", DeviceLocalAvailability: "unknown", AppliedConfig: "not_checked", BackupCoverage: "not_verified",
			Qualification: "Descriptor intent is not evidence of protection.", NextAction: "Read the original directly.",
		}))
	}))
	defer server.Close()
	config := writeProjectBackendInstallManifest(t, server.URL)
	stdout, stderr, err := executeRootCommand("--config", config, "files", "explain", "Notes/private/paper.pdf")
	if err != nil {
		t.Fatalf("explain: %s %v", stderr, err)
	}
	for _, want := range []string{"Source: /box/Notes/private/paper.pdf", "Search intent: metadata", "Enrichment intent: none", "Protect intent: false", `search rule: .loom/box.yaml :: files.rules.search [path="Notes/private"]`, "Applied config: not_checked", "Backup coverage: not_verified", "Descriptor intent is not evidence of protection."} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in %s", want, stdout)
		}
	}
	if _, _, err := executeRootCommand("--config", config, "files", "explain"); err == nil {
		t.Fatal("explain accepted missing path")
	}
}
