package loomdocs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixtureCorpus(t *testing.T) *Corpus {
	t.Helper()
	root, err := ResolveRoot(ResolveOptions{ExplicitPath: filepath.Join("testdata", "docs"), LoomVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return corpus
}

func TestLoadParsesMetadataHeadingsAndHash(t *testing.T) {
	corpus := loadFixtureCorpus(t)
	if len(corpus.Documents) != 4 {
		t.Fatalf("document count = %d, want 4", len(corpus.Documents))
	}
	if corpus.CorpusHash == "" {
		t.Fatal("missing corpus hash")
	}
	document, err := corpus.resolveDocument("Project Lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if document.Title != "Projects And Scopes" || len(document.Headings) < 3 || document.FileHash == "" {
		t.Fatalf("incomplete parsed document: %#v", document)
	}
	if status := corpus.Status(); status.ParseErrors != 0 || status.UnresolvedWikilinks != 0 {
		t.Fatalf("unexpected fixture issues: %#v", status)
	}
}

func TestLoadReportsMalformedAndMissingMetadata(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "bad.md"), []byte("not frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "missing.md"), []byte("---\ntitle: Missing\n---\n# Missing\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	corpus, err := Load(ResolvedRoot{Path: rootPath, Source: SourceExplicit})
	if err != nil {
		t.Fatal(err)
	}
	status := corpus.Status()
	if status.ParseErrors != 2 {
		t.Fatalf("parse errors = %d, want 2; issues=%#v", status.ParseErrors, status.Issues)
	}
}

func TestLoadEmptyCorpus(t *testing.T) {
	corpus, err := Load(ResolvedRoot{Path: t.TempDir(), Source: SourceExplicit})
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Documents) != 0 || corpus.CorpusHash == "" {
		t.Fatalf("unexpected empty corpus: %#v", corpus)
	}
}

func TestLoadExcludesDevelopmentTemplatesWithoutHidingMalformedPages(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "development", "templates"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, "user-guide"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "development", "templates", "page.md"), []byte("placeholder without frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "user-guide", "bad.md"), []byte("real malformed page"), 0o644); err != nil {
		t.Fatal(err)
	}
	unresolvedPage := `---
title: "Real Page"
description: "A real page with a broken documentation link."
audience:
  - user
tags:
  - loom
status: draft
---

# Real Page

See [[Missing Real Page]].
`
	if err := os.WriteFile(filepath.Join(rootPath, "user-guide", "unresolved.md"), []byte(unresolvedPage), 0o644); err != nil {
		t.Fatal(err)
	}

	corpus, err := Load(ResolvedRoot{Path: rootPath, Source: SourceExplicit})
	if err != nil {
		t.Fatal(err)
	}
	status := corpus.Status()
	if status.ParseErrors != 1 || status.UnresolvedWikilinks != 1 {
		t.Fatalf("status = %#v, want one real parse error and one real unresolved wikilink", status)
	}
	if status.DocumentCount != 1 {
		t.Fatalf("document count = %d, want only the valid non-template page", status.DocumentCount)
	}
	for _, issue := range status.Issues {
		if strings.HasPrefix(issue.Path, "development/templates/") {
			t.Fatalf("development template produced an issue: %#v", issue)
		}
	}
}

func TestRepositoryDocumentationCorpusHasNoErrors(t *testing.T) {
	root, err := ResolveRoot(ResolveOptions{ExplicitPath: filepath.Join("..", "..", "docs"), LoomVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	status := corpus.Status()
	if status.DocumentCount == 0 {
		t.Fatalf("repository docs status = %#v", status)
	}
	for _, issue := range status.Issues {
		if strings.HasPrefix(issue.Path, "development/templates/") {
			t.Fatalf("development template was interpreted as a documentation page: %#v", issue)
		}
		if issue.Severity == SeverityError {
			t.Fatalf("repository documentation error: %#v", issue)
		}
	}
	t.Logf("repository docs: documents=%d duplicate_titles=%d duplicate_aliases=%d unresolved_wikilinks=%d", status.DocumentCount, status.DuplicateTitles, status.DuplicateAliases, status.UnresolvedWikilinks)
}

func TestRepositoryDocumentationTemplatesUseHumanFillRFC3339Placeholders(t *testing.T) {
	templateRoot := filepath.Join("..", "..", ".project", "templates", "docs")
	for _, name := range []string{"page.md", "index.md", "command-reference.md"} {
		content, err := os.ReadFile(filepath.Join(templateRoot, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		text := string(content)
		if strings.Contains(text, "{{.CreatedAt}}") || strings.Contains(text, "{{.UpdatedAt}}") {
			t.Fatalf("%s retains unresolved Go template timestamp tokens", name)
		}
		for _, field := range []string{"created_at", "updated_at"} {
			want := field + `: "<RFC3339 UTC timestamp>"`
			if !strings.Contains(text, want) {
				t.Fatalf("%s missing explicit human-fill timestamp %q", name, want)
			}
		}
	}
}
