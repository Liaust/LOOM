package notesworkspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSourceMetadataReadersNoFollow(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "a.md"), []byte("A"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := ObserveSourcePath(root, "nested/a.md")
	if err != nil || info.Size() != 1 {
		t.Fatal(info, err)
	}
	entries, err := ReadSourceDirectory(root, "nested")
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"link/a.md", "../secret.md", "nested/../nested/a.md"} {
		if _, err := ObserveSourcePath(root, relative); err == nil {
			t.Fatal("unsafe metadata read", relative)
		}
	}
	for _, relative := range []string{"link", "../", "nested/.."} {
		if _, err := ReadSourceDirectory(root, relative); err == nil {
			t.Fatal("unsafe directory read", relative)
		}
	}
}
