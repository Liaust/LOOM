package storagearchive

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareRetainedRootsPreservesExistingAndRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	if err := PrepareRetainedRoots(root); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "Projects", "kept.md")
	if err := os.WriteFile(file, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRetainedRoots(root); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(file); err != nil || string(b) != "retained" {
		t.Fatalf("retained file changed: %v", err)
	}
	other := t.TempDir()
	if err := os.Symlink(root, filepath.Join(other, "Projects")); err != nil {
		t.Fatal(err)
	}
	if err := PrepareRetainedRoots(other); err == nil {
		t.Fatal("linked infrastructure root admitted")
	}
}
