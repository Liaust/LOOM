package notesworkspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/knowledge"
)

func TestFileReaderCurrentNativeSource(t *testing.T) {
	root := t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "note.md")
	if err := os.WriteFile(path, []byte("new source text"), 0600); err != nil {
		t.Fatal(err)
	}
	r := FileReader{NodeKey: "main"}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ObserveNotesFile(t.Context(), "main", root, "folder"); !errors.Is(err, knowledge.ErrFileSourceNotRegular) {
		t.Fatalf("directory observation: %v", err)
	}
	got, err := r.ReadNotesFile(t.Context(), "main", root, "note.md", 0, 4)
	if err != nil || got.Text != "new " || !got.Truncated || got.NextOffset != 4 {
		t.Fatalf("%+v %v", got, err)
	}
	got, err = r.ReadNotesFile(t.Context(), "main", root, "note.md", 4, 64)
	if err != nil || got.Text != "source text" || got.Truncated {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err = r.ReadNotesFile(t.Context(), "offline", root, "note.md", 0, 64); !errors.Is(err, knowledge.ErrFileSourceUnavailable) {
		t.Fatal(err)
	}
	if err = os.Symlink(path, filepath.Join(root, "alias.md")); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ReadNotesFile(t.Context(), "main", root, "alias.md", 0, 64); err == nil {
		t.Fatal("symlink followed")
	}
	if _, err = r.ReadNotesFile(t.Context(), "main", root, "../note.md", 0, 64); err == nil {
		t.Fatal("escaped declared root")
	}
}
