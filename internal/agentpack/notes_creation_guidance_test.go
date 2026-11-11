package agentpack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeNoteCreationGuidance(t *testing.T) {
	root := loadRepositoryPack(t).Root.Path
	for _, path := range []string{"host-instructions/codex/AGENTS.md", "templates/mina/AGENTS.md", "templates/project/AGENTS.md", "templates/box/Notes/AGENTS.md"} {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"creating a durable knowledge note", "subject/", "people", "related", "compulsory global search", "owning-project context", "original dates"} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s missing creation convention %q", path, want)
			}
		}
	}
}
