package loomdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphDetectsDuplicatesAndUnresolvedLinks(t *testing.T) {
	rootPath := t.TempDir()
	writeFixtureDoc(t, rootPath, "a.md", "Same", "Shared", "[[Missing Page]]")
	writeFixtureDoc(t, rootPath, "b.md", "Same", "Shared", "[[folder/]]")
	if err := os.MkdirAll(filepath.Join(rootPath, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureDoc(t, rootPath, "folder/page.md", "Nested", "Nested Alias", "")
	corpus, err := Load(ResolvedRoot{Path: rootPath, Source: SourceExplicit})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, issue := range corpus.Issues {
		kinds[issue.Kind] = true
	}
	for _, expected := range []string{"duplicate_title", "duplicate_alias", "unresolved_wikilink", "directory_wikilink"} {
		if !kinds[expected] {
			t.Errorf("missing graph issue %q: %#v", expected, corpus.Issues)
		}
	}
}

func TestResolveDocumentRejectsTraversalAndAmbiguity(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	if _, err := corpus.resolveDocument("../projects.md"); err == nil {
		t.Fatal("expected path traversal to fail")
	}
	corpus.Documents[0].Aliases = append(corpus.Documents[0].Aliases, "Project Lifecycle")
	corpus.rebuildIndexes()
	if _, err := corpus.resolveDocument("Project Lifecycle"); err == nil {
		t.Fatal("expected ambiguous alias to fail")
	} else if _, ok := err.(*AmbiguousTargetError); !ok {
		t.Fatalf("error = %T %v, want AmbiguousTargetError", err, err)
	}
}

func writeFixtureDoc(t *testing.T, root, relative, title, alias, related string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := strings.Join([]string{
		"---",
		"title: \"" + title + "\"",
		"description: fixture",
		"audience: [developer]",
		"tags: [loom]",
		"status: draft",
		"verified_at: \"2026-08-16\"",
		"aliases: [\"" + alias + "\"]",
		"related: [\"" + related + "\"]",
		"---",
		"# " + title,
	}, "\n")
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
}
