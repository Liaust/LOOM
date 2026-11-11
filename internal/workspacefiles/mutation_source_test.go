package workspacefiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

func TestFilesystemMutationSourceWithoutEnrollment(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{Roots: []Root{{Ref: "box", SourcePath: root, Node: "main"}}}
	src, err := service.MutationSource(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if src.Path != root || src.CollectionID == "" || src.Generation == "" {
		t.Fatal(src)
	}
	resolver := MutationResolver{Files: service, Source: src}
	called := false
	if err = resolver.WithSource(context.Background(), src.CollectionID, "unindexed.py", func(notesworkspace.Source) error { called = true; return nil }); err != nil || !called {
		t.Fatal("ordinary file requires enrollment", err)
	}
	if err = os.Mkdir(filepath.Join(root, "another"), 0700); err != nil {
		t.Fatal(err)
	}
	if after, e := service.MutationSource(t.Context(), "", ""); e != nil || after != src {
		t.Fatal("ordinary directory change invalidates root generation", e)
	}
	for _, relative := range []string{"../escape.md", ".loom/state/journal.json", "node_modules/pkg/index.js"} {
		if e := resolver.WithSource(t.Context(), src.CollectionID, relative, func(notesworkspace.Source) error { return nil }); e == nil {
			t.Fatalf("unsafe mutation path admitted: %s", relative)
		}
	}
}
