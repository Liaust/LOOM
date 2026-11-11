package loomcli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/response"
)

func TestBoxContextCLISelectionAndRevision(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	nested := filepath.Join(f.company, "Projects", "example", "repo", "src")
	archive := filepath.Join(f.catalog.Boxes[1].ArchiveRoot, "Projects", "archived", "repo")
	for _, path := range []string{nested, filepath.Join(filepath.Dir(nested), ".git"), archive, f.company + "-other"} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	alias, cross := filepath.Join(f.root, "company-alias"), filepath.Join(f.company, "personal-alias")
	for path, target := range map[string]string{alias: nested, cross: f.personal} {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}
	bodies := map[string]string{f.personal: "Personal test policy\n", f.company: "Company test policy\n"}
	for root, body := range bodies {
		writeBoxContextPolicy(t, root, body)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "context must remain offline", http.StatusInternalServerError)
	}))
	defer server.Close()
	t.Setenv("LOOM_MAIN_URL", server.URL)
	for _, test := range []struct {
		name, cwd, env, ref, key, root, origin string
	}{
		{name: "company", cwd: f.company, key: "company", root: f.company, origin: "cwd"},
		{name: "nested-git", cwd: nested, key: "company", root: f.company, origin: "cwd"},
		{name: "canonical-alias", cwd: alias, key: "company", root: f.company, origin: "cwd"},
		{name: "cross-box-alias", cwd: cross, key: "personal", root: f.personal, origin: "cwd"},
		{name: "company-archive", cwd: archive, key: "company", root: f.company, origin: "cwd"},
		{name: "personal", cwd: f.personal, key: "personal", root: f.personal, origin: "cwd"},
		{name: "outside", cwd: f.root, key: "personal", root: f.personal, origin: "default"},
		{name: "not-a-prefix-match", cwd: f.company + "-other", key: "personal", root: f.personal, origin: "default"},
		{name: "personal-storage-legacy", cwd: filepath.Join(f.root, "storage"), key: "personal", root: f.personal, origin: "default"},
		{name: "flag-over-env-cwd", cwd: f.company, env: "box_company", ref: "personal", key: "personal", root: f.personal, origin: "flag"},
		{name: "flag-over-archive", cwd: archive, ref: "box_personal", key: "personal", root: f.personal, origin: "flag"},
		{name: "env-over-cwd", cwd: f.company, env: " box_personal ", key: "personal", root: f.personal, origin: "env"},
		{name: "bound-company", cwd: f.personal, env: "box_company", key: "company", root: f.company, origin: "env"},
		{name: "immutable-root-ref", cwd: f.personal, ref: "box:box_company", key: "company", root: f.company, origin: "flag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(test.cwd)
			t.Setenv("LOOM_BOX_ID", test.env)
			args := []string{"box", "context", "--json", "--correlation-id", "box-context-test"}
			if test.ref != "" {
				args = append(args, "--box", test.ref)
			}
			stdout, stderr, err := executeRootCommand(args...)
			if err != nil {
				t.Fatalf("context: %v stderr=%s stdout=%s", err, stderr, stdout)
			}
			var got response.Envelope[boxContextResult]
			if err := json.Unmarshal([]byte(stdout), &got); err != nil || !got.OK {
				t.Fatalf("invalid context envelope: %s err=%v", stdout, err)
			}
			wantID, wantRef := "box_personal", "box"
			if test.key == "company" {
				wantID, wantRef = "box_company", "box:box_company"
			}
			data := got.Data
			if data.BoxID != wantID || data.BoxKey != test.key || data.RootPath != test.root || data.RootRef != wantRef || data.Node != "main" || data.SelectionOrigin != test.origin || data.Source != "catalog" {
				t.Fatalf("context identity/selection = %+v, want key=%s root=%s origin=%s", data, test.key, test.root, test.origin)
			}
			wantRevision := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bodies[test.root])))
			if data.PolicyPath != filepath.Join(test.root, ".loom", "agents", "WORKFLOW.md") || data.PolicyStatus != "available" || data.PolicyRevision != wantRevision {
				t.Fatalf("wrong policy path/revision: %+v", data)
			}
			if got.Meta.Source != "local-cli" || got.Meta.Freshness != "local" || got.Meta.CorrelationID != "box-context-test" {
				t.Fatalf("wrong local context metadata: %+v", got.Meta)
			}
			for _, private := range []string{"test policy", f.personalState, "account_id", "actor_id"} {
				if strings.Contains(stdout, private) {
					t.Fatalf("context exposed unrelated content %q: %s", private, stdout)
				}
			}
		})
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("context made %d remote requests", got)
	}
}

func TestBoxContextCLIMissingPolicyIsReadOnly(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	t.Chdir(f.company)
	contractBefore, err := os.ReadFile(box.ContractPath(f.company))
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := executeRootCommand("box", "context", "--json")
	if err != nil {
		t.Fatalf("missing policy blocked local context: %v %s", err, stderr)
	}
	var got response.Envelope[map[string]any]
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || !got.OK {
		t.Fatalf("invalid context envelope: %s err=%v", stdout, err)
	}
	for _, key := range []string{"box_id", "box_key", "root_path", "policy_path", "policy_revision", "selection_origin"} {
		if _, exists := got.Data[key]; !exists {
			t.Fatalf("context omitted required field %s: %s", key, stdout)
		}
	}
	if got.Data["policy_status"] != "missing" || got.Data["policy_revision"] != "" || got.Data["policy_path"] != filepath.Join(f.company, ".loom", "agents", "WORKFLOW.md") {
		t.Fatalf("missing policy not represented: %s", stdout)
	}
	if _, err := os.Stat(filepath.Join(f.company, ".loom", "agents")); !os.IsNotExist(err) {
		t.Fatalf("context initialized policy directory: %v", err)
	}
	contractAfter, err := os.ReadFile(box.ContractPath(f.company))
	if err != nil || !bytes.Equal(contractBefore, contractAfter) {
		t.Fatalf("context changed Box descriptor: %v", err)
	}
	for _, args := range [][]string{{"box", "context"}, {"box", "context", "--plain"}} {
		stdout, stderr, err := executeRootCommand(args...)
		if err != nil || !strings.Contains(stdout, f.company) {
			t.Fatalf("context output mode: %q %v %s", stdout, err, stderr)
		}
	}
}

func TestBoxContextCLIRejectsUnknownAndMultiSelection(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	t.Chdir(f.company)
	for _, test := range []struct {
		name, env string
		flags     []string
		want      error
	}{
		{name: "unknown-flag", flags: []string{"--box", "unknown"}, want: boxregistry.ErrUnknown},
		{name: "unknown-env", env: "unknown", want: boxregistry.ErrUnknown},
		{name: "invalid-flag-over-valid-env", env: "box_personal", flags: []string{"--box", "unknown"}, want: boxregistry.ErrUnknown},
		{name: "multiple-refs", flags: []string{"--box", "personal,company"}, want: boxregistry.ErrUnknown},
		{name: "all", flags: []string{"--all-boxes"}, want: boxregistry.ErrSelection},
		{name: "all-and-box", flags: []string{"--all-boxes", "--box", "company"}, want: boxregistry.ErrSelection},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LOOM_BOX_ID", test.env)
			stdout, _, err := executeRootCommand(append([]string{"box", "context", "--json"}, test.flags...)...)
			if !errors.Is(err, test.want) || stdout != "" {
				t.Fatalf("unsafe selection returned context: %s err=%v want=%v", stdout, err, test.want)
			}
		})
	}
}

func TestBoxContextCLIUnavailableCompanyNeverFallsBack(t *testing.T) {
	for _, state := range []string{"root_missing", "descriptor_missing"} {
		t.Run(state, func(t *testing.T) {
			f := newCLIBoxCatalogFixture(t, state)
			archive := filepath.Join(f.root, "storage", "Boxes", "company", "Projects", "archived")
			if err := os.MkdirAll(archive, 0700); err != nil {
				t.Fatal(err)
			}
			for _, test := range []struct {
				name, cwd, env string
				flags          []string
			}{
				{name: "flag", cwd: f.personal, flags: []string{"--box", "company"}},
				{name: "env", cwd: f.personal, env: "company"},
				{name: "archive", cwd: archive},
			} {
				t.Run(test.name, func(t *testing.T) {
					t.Chdir(test.cwd)
					t.Setenv("LOOM_BOX_ID", test.env)
					stdout, _, err := executeRootCommand(append([]string{"box", "context", "--json"}, test.flags...)...)
					if !errors.Is(err, boxregistry.ErrUnavailable) || stdout != "" {
						t.Fatalf("unavailable Company fell back: %s err=%v", stdout, err)
					}
				})
			}
			if state == "descriptor_missing" {
				t.Chdir(f.company)
				if _, _, err := executeRootCommand("box", "context", "--json"); !errors.Is(err, boxregistry.ErrUnavailable) {
					t.Fatalf("unavailable cwd fell back: %v", err)
				}
				if _, _, err := executeRootCommand("box", "context", "--json", "--box", "personal"); err != nil {
					t.Fatalf("explicit Personal selection lost precedence: %v", err)
				}
			}
			if _, err := os.Stat(box.ContractPath(f.company)); !os.IsNotExist(err) {
				t.Fatalf("context initialized unavailable Company: %v", err)
			}
		})
	}
}

func TestBoxContextCLILegacyOutsideCatalogScope(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	t.Chdir(f.root)
	t.Setenv("LOOM_BOXES_FILE", "")
	stdout, stderr, err := executeRootCommand("box", "context", "--json")
	if err != nil {
		t.Fatalf("legacy context: %v %s", err, stderr)
	}
	var got response.Envelope[boxContextResult]
	if err := json.Unmarshal([]byte(stdout), &got); err != nil || !got.OK || got.Data.BoxID != "box_personal" || got.Data.RootPath != f.personal || got.Data.Source != "legacy" || got.Data.SelectionOrigin != "default" {
		t.Fatalf("legacy Personal context changed: %s err=%v", stdout, err)
	}
	if _, _, err := executeRootCommand("box", "context", "--json", "--box", "company"); !errors.Is(err, boxregistry.ErrUnknown) {
		t.Fatalf("missing Company catalog silently selected Personal: %v", err)
	}
}

func TestBoxContextCLIPolicyRevisionRefreshAndFileValidation(t *testing.T) {
	f := newCLIBoxCatalogFixture(t, "available")
	t.Chdir(f.company)
	for _, body := range []string{"first policy\n", "revised policy\n"} {
		writeBoxContextPolicy(t, f.company, body)
		result, err := resolveBoxContextForCLI(&options{})
		if err != nil || result.PolicyRevision != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(body))) {
			t.Fatalf("stale policy revision: %+v err=%v", result, err)
		}
	}
	policyPath := filepath.Join(f.company, ".loom", "agents", "WORKFLOW.md")
	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(policyPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveBoxContextForCLI(&options{}); err == nil {
		t.Fatal("non-regular policy accepted")
	}
}

func writeBoxContextPolicy(t *testing.T, root, body string) {
	t.Helper()
	path := filepath.Join(root, ".loom", "agents", "WORKFLOW.md")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
