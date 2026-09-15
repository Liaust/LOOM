package supportbundle

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAliasPathUsesLongestKnownRoot(t *testing.T) {
	home := filepath.Clean("/Users/tester")
	box := filepath.Join(home, "loom-box")
	aliases := []PathAlias{
		{Label: "$HOME", Root: home},
		{Label: "$LOOM_BOX", Root: box},
	}
	got, ok := AliasPath(filepath.Join(box, "Notes", "a.md"), aliases, false)
	if !ok {
		t.Fatal("expected path alias")
	}
	if got != "$LOOM_BOX/Notes/a.md" {
		t.Fatalf("alias = %q, want longest root alias", got)
	}
}

func TestAliasTextRespectsIncludeAbsolutePaths(t *testing.T) {
	root := filepath.Clean("/Users/tester/loom-box")
	input := "path=" + filepath.Join(root, "Notes", "a.md")
	aliases := []PathAlias{{Label: "$LOOM_BOX", Root: root}}
	kept, aliasesCount := AliasText(input, aliases, true)
	if kept != input || aliasesCount != 0 {
		t.Fatalf("absolute paths should be preserved when requested: %q aliases=%d", kept, aliasesCount)
	}
	aliased, aliasesCount := AliasText(input, aliases, false)
	if aliasesCount != 1 {
		t.Fatalf("aliases = %d, want 1", aliasesCount)
	}
	if strings.Contains(aliased, root) || !strings.Contains(aliased, "$LOOM_BOX/Notes/a.md") {
		t.Fatalf("path was not aliased correctly: %q", aliased)
	}
}

func TestProjectAndNotesRootPathAliases(t *testing.T) {
	project := ProjectPathAlias("osint-tools", "/srv/loom/projects/osint-tools")
	noteRoot := NotesRootPathAlias("box", "/Users/tester/loom-box/Notes")
	aliases := []PathAlias{project, noteRoot}
	got, ok := AliasPath("/srv/loom/projects/osint-tools/scripts/run.sh", aliases, false)
	if !ok || got != "$PROJECT/osint-tools/scripts/run.sh" {
		t.Fatalf("project alias mismatch: got=%q ok=%v", got, ok)
	}
	got, ok = AliasPath("/Users/tester/loom-box/Notes/daily.md", aliases, false)
	if !ok || got != "$NOTES_ROOT/box/daily.md" {
		t.Fatalf("notes root alias mismatch: got=%q ok=%v", got, ok)
	}
}

func TestAliasTextDoesNotReplacePartialPathSegment(t *testing.T) {
	input := "path=/Users/tester2/loom-box/Notes/a.md real=/Users/tester/loom-box/Notes/b.md"
	aliased, aliasesCount := AliasText(input, []PathAlias{{Label: "$HOME", Root: "/Users/tester"}}, false)
	if aliasesCount != 1 {
		t.Fatalf("aliases = %d, want 1; text=%q", aliasesCount, aliased)
	}
	if strings.Contains(aliased, "$HOME2") {
		t.Fatalf("partial segment was aliased incorrectly: %q", aliased)
	}
	if !strings.Contains(aliased, "$HOME/loom-box/Notes/b.md") {
		t.Fatalf("real path was not aliased: %q", aliased)
	}
}

func TestDefaultAliasesIncludeKnownMainBoxRoots(t *testing.T) {
	input := "source=/home/loomadmin/loom-box/Projects/osint-tools/notes"
	aliased, aliasesCount := AliasText(input, DefaultPathAliases(), false)
	if aliasesCount != 1 {
		t.Fatalf("aliases = %d, want 1; text=%q", aliasesCount, aliased)
	}
	if strings.Contains(aliased, "/home/loomadmin") || !strings.Contains(aliased, "$LOOM_MAIN_BOX/Projects/osint-tools/notes") {
		t.Fatalf("main Box path was not aliased: %q", aliased)
	}
}
