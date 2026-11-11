package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectCreateDevelopmentContext(t *testing.T) {
	opts := ScaffoldOptions{Name: "WebDAV", Slug: "webdav", OwnerNode: "main", Directory: t.TempDir(), Mode: ScaffoldModeDeclaration, DryRun: true}
	plan, err := ScaffoldProject(opts)
	if err != nil || !plan.OK || len(plan.Files) != 8 || plan.ProjectID != "" {
		t.Fatalf("dry run: %+v %v", plan, err)
	}
	if _, err := os.Lstat(plan.ProjectRoot); !os.IsNotExist(err) {
		t.Fatalf("dry run created project: %v", err)
	}
	opts.DryRun = false
	created, err := ScaffoldProject(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"AGENTS.md", ".project/OVERVIEW.md", ".project/STATE.md", ".project/ROADMAP.md", ".project/MAP.md", ".project/CONVERSATIONS.md", ".project/protocols/WORKFLOW.md"} {
		raw, err := os.ReadFile(filepath.Join(created.ProjectRoot, path))
		if err != nil || len(raw) == 0 || strings.Contains(string(raw), "{{") {
			t.Fatalf("%s: %s %v", path, raw, err)
		}
	}
	for _, path := range []string{".git", ".repo", ".project/project.yaml", ".loom/.project"} {
		if _, err := os.Lstat(filepath.Join(created.ProjectRoot, path)); !os.IsNotExist(err) {
			t.Fatalf("unexpected %s: %v", path, err)
		}
	}
	agents := readFile(t, created.ProjectRoot, "AGENTS.md")
	workflow := readFile(t, created.ProjectRoot, ".project/protocols/WORKFLOW.md")
	for _, want := range []string{"Read known files directly", "not a prerequisite", "loom provenance candidate register", "actual producer/task", "Project workers do not adopt MINA"} {
		if !strings.Contains(agents, want) {
			t.Fatalf("minimal agents missing %q: %s", want, agents)
		}
	}
	if !strings.Contains(workflow, "including a CLI opened through ORCA") || strings.Contains(workflow, "An ORCA session uses ORCA lifecycle tools") {
		t.Fatalf("incorrect native harness owner: %s", workflow)
	}
	for _, path := range []string{"AGENTS.md", ".project/STATE.md"} {
		if err := os.WriteFile(filepath.Join(created.ProjectRoot, path), []byte("User edited context\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	replay, err := ScaffoldProject(opts)
	if err != nil || replay.ProjectID != created.ProjectID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	for _, path := range []string{"AGENTS.md", ".project/STATE.md"} {
		raw, err := os.ReadFile(filepath.Join(created.ProjectRoot, path))
		if err != nil || string(raw) != "User edited context\n" {
			t.Fatalf("replaced %s", path)
		}
	}
}

func TestProjectCreatePreservesCustomDevelopmentTree(t *testing.T) {
	opts := ScaffoldOptions{Name: "Existing", Slug: "existing", OwnerNode: "main", Directory: t.TempDir(), Mode: ScaffoldModeDeclaration}
	root := filepath.Join(opts.Directory, opts.Slug)
	if err := os.MkdirAll(filepath.Join(root, ".project"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ".project/PROJECT.md")
	if err := os.WriteFile(path, []byte("Existing mature overview"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ScaffoldProject(opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, ".project"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "PROJECT.md" {
		t.Fatalf("custom tree changed: %v %v", entries, err)
	}
}
