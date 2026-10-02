package knowledge

import (
	"encoding/json"
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
	for _, relative := range []string{"excluded/a.md", "private/a.md", "reference.pdf"} {
		if _, err := workspacePathPolicy(root, raw, relative); err == nil {
			t.Fatalf("accepted %s", relative)
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
