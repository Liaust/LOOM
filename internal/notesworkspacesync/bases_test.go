package notesworkspacesync

import "testing"

func TestBaseTextPath(t *testing.T) {
	for _, path := range []string{"Notes/People.base", "Projects/Research/Concepts.BASE"} {
		if !textPath(path) || referencePath(path) {
			t.Fatalf("Base must be editable UTF-8: %s", path)
		}
	}
	for _, path := range []string{"Notes/.private/People.base", "Notes/../People.base", "Notes/credentials/People.base"} {
		if textPath(path) {
			t.Fatalf("unsafe Base path accepted: %s", path)
		}
	}
}
