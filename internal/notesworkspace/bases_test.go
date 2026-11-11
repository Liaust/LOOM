package notesworkspace

import (
	"path/filepath"
	"testing"
)

func TestBaseSourceLifecycle(t *testing.T) {
	s, _, r := fixture(t)
	if IsReferencePath("People.base") {
		t.Fatal("Base must be editable text")
	}
	content := "views:\r\n  - type: table\r\n    name: People\r\n"
	b := readBase(t, s, "People.base")
	receive(t, s, "create", b, content)
	apply(t, s, "create", Accepted)
	contents(t, filepath.Join(r.root, "People.base"), content)
	b = readBase(t, s, "People.base")
	receivePath(t, s, "rename", KindRename, "Profiles.base", b)
	apply(t, s, "rename", Accepted)
	b = readBase(t, s, "Profiles.base")
	receive(t, s, "edit", b, content+"    limit: 50\r\n")
	apply(t, s, "edit", Accepted)
	b = readBase(t, s, "Profiles.base")
	receivePath(t, s, "delete", KindDelete, "", b)
	apply(t, s, "delete", Accepted)
}
