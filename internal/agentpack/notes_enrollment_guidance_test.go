package agentpack

import (
	"strings"
	"testing"
)

func TestNotesEnrollmentGuidanceSeparatesSelectionAndPlacement(t *testing.T) {
	entry := projectProtocolRead(t, "skills/manage-loom-projects/SKILL.md")
	if !strings.Contains(entry, "references/notes-pipeline.md") {
		t.Fatal("project enrollment entry point must route to Notes selection and placement guidance")
	}
	ref := strings.Join(strings.Fields(projectProtocolRead(t, "skills/manage-loom-projects/references/notes-pipeline.md")), " ")
	for _, boundary := range []string{
		"Existing collections retain their earlier client roots",
		"category: research",
		"does not shorten it",
		"previews each declared knowledge source's indexing and client selection",
		"optional `workspace.path`",
		"Correct `workspace.path`, inspect the fresh plan",
		"workspace: {include: ['**/*'], exclude: ['**/*']}",
		"Empty includes default to all files, not none",
		"Do not delete/recreate the collection",
		"not client placement",
	} {
		if !strings.Contains(ref, boundary) {
			t.Errorf("Notes enrollment guidance lacks %q", boundary)
		}
	}
}
