package loomcli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/loomdocs"
)

func docsFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "loomdocs", "testdata", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func executeDocsCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand()
	output := &bytes.Buffer{}
	cmd.SetOut(output)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestDocsStatusRunsWithoutDaemonConfiguration(t *testing.T) {
	output, err := executeDocsCommand(t, "docs", "status", "--docs-dir", docsFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "Documents: 4") || !strings.Contains(output, "Source: explicit") {
		t.Fatalf("unexpected status output:\n%s", output)
	}
}

func TestDocsSearchJSONIncludesStableCorpusAndResults(t *testing.T) {
	output, err := executeDocsCommand(t, "--json", "docs", "search", "project validation", "--tag", "projects", "--docs-dir", docsFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var response loomdocs.SearchResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatalf("decode JSON: %v\n%s", err, output)
	}
	if response.Root.Source != loomdocs.SourceExplicit || len(response.Results) != 1 || response.Results[0].RelativePath != "projects.md" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestDocsInspectAndRelated(t *testing.T) {
	output, err := executeDocsCommand(t, "--json", "docs", "inspect", "Project Lifecycle", "--heading", "Project Lifecycle", "--max-chars", "80", "--docs-dir", docsFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	var inspection loomdocs.Inspection
	if err := json.Unmarshal([]byte(output), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.RelativePath != "projects.md" || inspection.Heading != "Project Lifecycle" || !inspection.Truncated {
		t.Fatalf("unexpected inspection: %#v", inspection)
	}

	output, err = executeDocsCommand(t, "docs", "related", "projects.md", "--docs-dir", docsFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "LOOM Docs") || !strings.Contains(output, "Storage") {
		t.Fatalf("unexpected related output:\n%s", output)
	}
}

func TestDocsCommandTreeIsRegistered(t *testing.T) {
	cmd := NewRootCommand()
	for _, args := range [][]string{{"docs", "status"}, {"docs", "search", "query"}, {"docs", "inspect", "title"}, {"docs", "related", "title"}} {
		found, _, err := cmd.Find(args)
		if err != nil || found == nil {
			t.Fatalf("Find(%v) command=%v err=%v", args, found, err)
		}
	}
}
