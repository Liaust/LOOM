//go:build darwin

package notesworkspace

import (
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinACLHoldsBeforeLossyReplacement(t *testing.T) {
	s, _, r := fixture(t)
	name := filepath.Join(r.root, "note.md")
	write(t, name, "A")
	actor, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/bin/chmod", "+a", "user:"+actor.Username+" allow read", name).CombinedOutput(); err != nil {
		t.Fatalf("owned ACL fixture: %s %v", out, err)
	}
	receive(t, s, "acl", readBase(t, s, "note.md"), "B")
	held := apply(t, s, "acl", Held)
	if !strings.Contains(held.Reason, "Darwin extended ACL/security") {
		t.Fatalf("missing ACL hold: %s", held.Reason)
	}
	contents(t, name, "A")
	contents(t, filepath.Join(r.root, held.Journal, "proposed"), "B")
}
