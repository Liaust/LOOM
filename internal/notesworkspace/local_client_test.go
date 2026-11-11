package notesworkspace

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalClientSaveMoveAndRetainedRemoval(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	var st unix.Stat_t
	unix.Stat(root, &st)
	rootID := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	apply := func(r LocalClientRequest) {
		t.Helper()
		r.RootIdentity = rootID
		if err := ApplyLocalClient(root, r); err != nil {
			t.Fatal(err)
		}
	}
	apply(LocalClientRequest{ID: "folder", Kind: "create_folder", Path: "work"})
	apply(LocalClientRequest{ID: "file", Kind: "create_note", Path: "work/note.md", Text: "original"})
	p := filepath.Join(root, "work/note.md")
	os.Chmod(p, 0640)
	apply(LocalClientRequest{ID: "edit", Kind: "save", Path: "work/note.md", Text: "new", Base: digest([]byte("original"))[7:]})
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0640 {
		t.Fatal("permissions lost")
	}
	found := false
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(p), e.Name(), "displaced"))
		if err == nil && string(b) == "original" {
			found = true
		}
	}
	if !found {
		t.Fatal("original inode not retained")
	}
	unix.Stat(filepath.Join(root, "work"), &st)
	apply(LocalClientRequest{ID: "move", Kind: "rename", Path: "work", Target: "moved", Identity: fmt.Sprintf("%d:%d", st.Dev, st.Ino)})
	b, _ := os.ReadFile(filepath.Join(root, "moved/note.md"))
	if string(b) != "new" {
		t.Fatal("populated folder lost")
	}
	apply(LocalClientRequest{ID: "remove", Kind: "trash", Path: "moved", Identity: fmt.Sprintf("%d:%d", st.Dev, st.Ino)})
	if _, err := os.Stat(filepath.Join(root, "moved")); !os.IsNotExist(err) {
		t.Fatal("folder still visible")
	}
	entries, _ = os.ReadDir(root)
	found = false
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(root, e.Name(), "removed/note.md"))
		if err == nil && string(b) == "new" {
			found = true
		}
	}
	if !found {
		t.Fatal("removed folder bytes not retained")
	}
}

func TestLocalClientRefusesStaleBaseCollisionAndLinks(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	var st unix.Stat_t
	unix.Stat(root, &st)
	id := fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	os.WriteFile(filepath.Join(root, "note.md"), []byte("external"), 0600)
	r := LocalClientRequest{RootIdentity: id, ID: "stale", Kind: "save", Path: "note.md", Text: "draft", Base: digest([]byte("old"))[7:]}
	if ApplyLocalClient(root, r) == nil {
		t.Fatal("stale base accepted")
	}
	r.Kind = "create_note"
	r.ID = "collision"
	if ApplyLocalClient(root, r) == nil {
		t.Fatal("collision accepted")
	}
	os.Symlink(root, filepath.Join(root, "link"))
	r.Path = "link/note.md"
	r.ID = "link"
	if ApplyLocalClient(root, r) == nil {
		t.Fatal("symlink accepted")
	}
	r.Path = "note.md"
	r.ID = "root"
	r.RootIdentity = "0:0"
	if ApplyLocalClient(root, r) == nil {
		t.Fatal("wrong root accepted")
	}
	b, _ := os.ReadFile(filepath.Join(root, "note.md"))
	if string(b) != "external" {
		t.Fatal("external bytes overwritten")
	}
}
