package workspacefiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectionDoesNotGrantStorageWriteAuthority(t *testing.T) {
	root := t.TempDir()
	s := &Service{Roots: []Root{{Ref: "storage", SourcePath: root, ReadOnly: true}, {Ref: "box", SourcePath: root}}}
	t.Cleanup(func() { _ = s.Close() })
	got, err := s.InspectTarget("storage", "Projects/new")
	if err != nil || got != filepath.Join(root, "Projects/new") {
		t.Fatalf("logical inspection: %s %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, "Projects")); !os.IsNotExist(err) {
		t.Fatal("inspection created a directory")
	}
	if _, err := s.WritableTarget(context.Background(), "storage", "Projects/new"); err == nil {
		t.Fatal("inspection permitted a retained write")
	}
	for _, p := range []string{"../outside", "/absolute"} {
		if _, err := s.InspectTarget("storage", p); err == nil {
			t.Fatalf("unsafe path %q admitted", p)
		}
	}
}
