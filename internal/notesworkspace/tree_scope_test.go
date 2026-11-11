package notesworkspace

import "testing"

func TestTreeContainsSourceRoot(t *testing.T) {
	for _, tc := range []struct {
		name, from, registered string
		want                   bool
	}{
		{"registered root itself", "Notes", "/box/Notes", true},
		{"registered descendant", "Notes", "/box/Notes/nested", true},
		{"ordinary child of root", "Notes/child", "/box/Notes", false},
		{"sibling prefix", "Notes", "/box/Notes-other", false},
		{"unresolved retained path", "Notes", "", false},
		{"relative historical path", "Notes", "Notes/nested", false},
		{"normalized alias", "Notes", "/box/NOTES/nested/.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := treeContainsSourceRoot("/box", tc.from, tc.registered); got != tc.want {
				t.Fatalf("contains registered root: got=%t want=%t", got, tc.want)
			}
		})
	}
}

func TestScopePathKeyDoesNotInventHistoricalLocation(t *testing.T) {
	for _, tc := range []struct{ root, relative string }{
		{"", "note.md"}, {"unresolved", "note.md"}, {"/box", ""}, {"/box", "/note.md"}, {"/box", "../note.md"},
	} {
		if got, known := scopePathKey(tc.root, tc.relative); known || got != "" {
			t.Fatalf("invented physical location from %q + %q: %q", tc.root, tc.relative, got)
		}
	}
	if got, known := scopePathKey("/box/Notes", "drafts/Note.md"); !known || got != pathKey("/box/Notes/drafts/Note.md") {
		t.Fatalf("known physical alias: %q %t", got, known)
	}
}
