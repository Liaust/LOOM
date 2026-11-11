package workspacefiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreatePreservesExistingAndStorage(t *testing.T) {
	root := t.TempDir()
	s := Service{Roots: []Root{{Ref: "box", SourcePath: root}, {Ref: "storage", SourcePath: root, ReadOnly: true}}}
	defer s.Close()
	if _, err := s.Create(t.Context(), CreateInput{Root: "box", Path: "empty", Directory: true}); err != nil {
		t.Fatal(err)
	}
	input := CreateInput{Root: "box", Path: "empty/note.md", Text: "preserve me"}
	if _, err := s.Create(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	input.Text = "replacement"
	if _, err := s.Create(t.Context(), input); err == nil {
		t.Fatal("overwrote existing note")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "empty/note.md")); string(data) != "preserve me" {
		t.Fatal("lost original")
	}
	for _, input := range []CreateInput{{Root: "storage", Path: "bad.md"}, {Root: "box", Path: "../bad.md"}, {Root: "box", Path: ".env"}, {Root: "box", Path: "binary.pdf"}} {
		if _, err := s.Create(t.Context(), input); err == nil {
			t.Fatalf("unsafe create: %+v", input)
		}
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(t.Context(), CreateInput{Root: "box", Path: "escape/note.md"}); err == nil {
		t.Fatal("followed substituted parent")
	}
}
