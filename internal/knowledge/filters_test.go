package knowledge

import (
	"strings"
	"testing"
)

func TestNotesRecoveryJournalPathsAreNotKnowledge(t *testing.T) {
	journal := ".loom-notes-" + strings.Repeat("a", 64)
	for _, name := range []string{journal, journal + "/original", "nested/" + journal + "/displaced", `nested\` + journal + `\intent`} {
		if !ignoredNotesKnowledgeRelativePath(name) {
			t.Errorf("journal path admitted: %s", name)
		}
	}
	for _, name := range []string{"notes.md", ".loom-notes-guide.md", "nested/loom-notes-" + strings.Repeat("a", 64) + "/original", journal + "x/original"} {
		if ignoredNotesKnowledgeRelativePath(name) {
			t.Errorf("ordinary path excluded: %s", name)
		}
	}
}
