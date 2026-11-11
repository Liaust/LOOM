package workspacefiles

import "testing"

func TestNavigationKnownPathsUseEnumerationExclusions(t *testing.T) {
	s := &Service{Roots: []Root{{Ref: "box", SourcePath: t.TempDir()}}}
	for _, relative := range []string{"Projects/x/node_modules/readme.md", "Notes/.git/readme.md", "Projects/x/.venv/readme.md"} {
		if e := s.NavigationPath("box", relative); e == nil {
			t.Fatalf("ignored known path admitted: %s", relative)
		}
	}
	if e := s.NavigationPath("box", "Notes/readme.md"); e != nil {
		t.Fatal(e)
	}
	for _, root := range []string{"", "."} {
		if e := s.NavigationPath("box", root); e != nil {
			t.Fatalf("root traversal refused: %v", e)
		}
	}
}
