package knowledge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	rootpolicy "loom.local/loom/internal/nodeagent/watchedroots"
)

func TestWorkspacePathUsesCurrentWatchAndUserExclusions(t *testing.T) {
	root := SourceRoot{SourcePath: t.TempDir(), BackendRootKey: "notes"}
	config := rootpolicy.RootConfig{RootKey: "notes", RootRelativePath: ".", Include: []string{"**/*.md"}, Exclude: []string{"excluded/**"}, IgnorePolicy: rootpolicy.IgnorePolicy{Profile: "managed", DiscoverUserRules: true, PolicyRootRelativePath: "."}}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(root.SourcePath, ".loomignore"), []byte("private/**\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := workspacePathPolicy(root, raw, "ordinary.md")
	if err != nil || before == "" {
		t.Fatalf("ordinary path: %s %v", before, err)
	}
	rootGeneration, err := workspacePathPolicy(root, raw, "")
	if err != nil || rootGeneration != before {
		t.Fatalf("empty collection must admit the same policy generation: %s %v", rootGeneration, err)
	}
	for _, relative := range []string{"excluded/a.md", "private/a.md", "reference.pdf"} {
		if _, err := workspacePathPolicy(root, raw, relative); !errors.Is(err, ErrNotesPathExcluded) {
			t.Fatalf("exclusion must be distinguishable for %s: %v", relative, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root.SourcePath, ".loomignore"), []byte("private/**\nother/**\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := workspacePathPolicy(root, raw, "ordinary.md")
	if err != nil || before == after {
		t.Fatal("policy generation did not change")
	}
}

func TestIndependentWorkspaceSelectionDoesNotEnableIndexing(t *testing.T) {
	root := SourceRoot{SourcePath: t.TempDir(), BackendRootKey: "notes", RootKind: RootKindProjectMaterial,
		Metadata: json.RawMessage(`{"registration_metadata":{"knowledge_source":{"schema_version":"project.contract.v0.5","include":["**/*.md"],"exclude":["historical/**"],"policy":{"workspace":{"include":["**/*.md","**/*.canvas"],"exclude":["not-shared/**"]}}}}}`)}
	config := rootpolicy.RootConfig{RootKey: "notes", RootRelativePath: ".", Include: []string{"**/*.md", "**/*.canvas"}, IgnorePolicy: rootpolicy.IgnorePolicy{Profile: "managed", DiscoverUserRules: true, PolicyRootRelativePath: "."}}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(root.SourcePath, ".loomignore"), []byte("private/**\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !expandedSourceExcluded(root, "historical/note.md", nil) || !projectionDeclaredPathExcluded(root.Metadata, "historical/note.md") {
		t.Fatal("index selection widened")
	}
	generation, err := workspacePathPolicy(root, raw, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"historical/note.md", "historical/board.canvas"} {
		if err := workspaceSourcePathAllowed(root, relative, false, false); err != nil {
			t.Fatal("sync-only source refused", relative, err)
		}
		if next, err := workspacePathPolicy(root, raw, relative); err != nil || next != generation {
			t.Fatal("sync-only policy/generation", next, err)
		}
	}
	if next, err := workspacePathPolicyKind(root, raw, "historical/empty", true); err != nil || next != generation {
		t.Fatal("directory generation", next, err)
	}
	for _, relative := range []string{"private/note.md", "not-shared/note.md"} {
		if _, err := workspacePathPolicy(root, raw, relative); !errors.Is(err, ErrNotesPathExcluded) {
			t.Fatal("visibility/privacy exclusion lost", relative, err)
		}
	}
	for _, relative := range []string{"historical/.env", "historical/private_no_index/a.md", "historical/credentials/a.md"} {
		if err := workspaceSourcePathAllowed(root, relative, false, false); err == nil {
			t.Fatal("protected path admitted", relative)
		}
	}
}

func TestWorkspaceDirectoryPolicyUsesRealPathAndSameGeneration(t *testing.T) {
	root := SourceRoot{SourcePath: t.TempDir(), BackendRootKey: "notes"}
	config := rootpolicy.RootConfig{RootKey: "notes", RootRelativePath: ".", Include: []string{"docs/**/*.md", "docs/**/*.canvas"}, Exclude: []string{"docs/excluded/**"}, IgnorePolicy: rootpolicy.IgnorePolicy{Profile: "managed", DiscoverUserRules: true, PolicyRootRelativePath: "."}}
	raw, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(root.SourcePath, ".loomignore"), []byte("docs/private/\ndocs/empty/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fileGeneration, err := workspacePathPolicy(root, raw, "docs/board.canvas")
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"", "docs", "docs/nested", "docs/nested/empty"} {
		generation, err := workspacePathPolicyKind(root, raw, relative, true)
		if err != nil || generation != fileGeneration {
			t.Fatalf("directory %q changed policy generation: %s %v", relative, generation, err)
		}
	}
	for _, relative := range []string{"outside", "docs/excluded", "docs/excluded/nested", "docs/private", "docs/empty"} {
		if _, err := workspacePathPolicyKind(root, raw, relative, true); !errors.Is(err, ErrNotesPathExcluded) {
			t.Fatalf("excluded actual directory admitted: %q %v", relative, err)
		}
	}
	// Legacy roots have no resolver profile but keep exact pattern fences.
	config.IgnorePolicy = rootpolicy.IgnorePolicy{}
	raw, _ = json.Marshal(config)
	if _, err := workspacePathPolicyKind(root, raw, "docs", true); err != nil {
		t.Fatal(err)
	}
	if _, err := workspacePathPolicyKind(root, raw, "docs/excluded", true); !errors.Is(err, ErrNotesPathExcluded) {
		t.Fatal("legacy directory exclusion lost", err)
	}
}

func TestWorkspaceSourceDirectoryAndCanvasAdmission(t *testing.T) {
	metadata := json.RawMessage(`{"registration_metadata":{"knowledge_source":{"include":["docs/**/*.md","docs/**/*.canvas"],"exclude":["docs/excluded/**"]}}}`)
	root := SourceRoot{RootKind: RootKindBoxTopics, Metadata: metadata}
	for _, relative := range []string{"docs", "docs/nested", "docs/nested/empty"} {
		if err := workspaceSourcePathAllowed(root, relative, false, true); err != nil {
			t.Fatalf("directory excluded: %q %v", relative, err)
		}
	}
	for _, relative := range []string{"", ".", "../escape", "outside", "docs/excluded", "docs/excluded/sub", "docs/credentials", "docs/private_no_index", "docs/.hidden"} {
		if err := workspaceSourcePathAllowed(root, relative, false, true); err == nil {
			t.Fatalf("excluded directory admitted: %q", relative)
		}
	}
	if err := workspaceSourcePathAllowed(root, "docs/board.canvas", false, false); err != nil {
		t.Fatal("Canvas not writable", err)
	}
	if err := workspaceSourcePathAllowed(root, "docs/reference.pdf", false, false); err == nil {
		t.Fatal("reference became writable")
	}
	for _, test := range []struct {
		patterns []string
		path     string
		want     bool
	}{
		{[]string{"docs/**/*.md"}, "docs", true},
		{[]string{"docs/**/*.md"}, "docs/nested/empty", true},
		{[]string{"docs/*.md"}, "docs/nested", false},
		{[]string{"docs/*/notes/*.canvas"}, "docs/one/notes", true},
		{[]string{"docs/*/notes/*.canvas"}, "docs/one/other", false},
		{[]string{"**/*.md"}, "any/empty/tree", true},
		{[]string{"docs/**"}, "outside", false},
		{nil, "docs", false},
	} {
		if got := workspaceDirectoryIncluded(test.patterns, test.path); got != test.want {
			t.Fatalf("directory include %v %q: got %v", test.patterns, test.path, got)
		}
	}
}
