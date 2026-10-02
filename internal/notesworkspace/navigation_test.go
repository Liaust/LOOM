package notesworkspace

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNavigationObservesWithoutCreatingCustody(t *testing.T) {
	s, m, r := fixture(t)
	if err := os.WriteFile(filepath.Join(r.root, "same.md"), []byte("current"), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	visit := func(File, Base) error { called = true; return nil }
	if err := s.WithNavigation(t.Context(), "collection", "same.md", digest([]byte("current")), visit); !errors.Is(err, sql.ErrNoRows) || called || len(m.files) != 0 {
		t.Fatalf("unbound created identity: %v", err)
	}
	original := readBase(t, s, "same.md")
	before := len(m.bases)
	if err := s.WithNavigation(t.Context(), "collection", "same.md", original.Base.Hash, func(f File, b Base) error {
		called = true
		if f != original.File || b.ID != original.Base.ID {
			t.Fatal("identity changed")
		}
		return nil
	}); err != nil || !called || len(m.bases) != before {
		t.Fatalf("read-only navigation: %v", err)
	}
	for name, change := range map[string]func(){
		"withdrawn": func() { r.active = false },
		"changed bytes": func() {
			if err := os.WriteFile(filepath.Join(r.root, "same.md"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"path reused": func() {
			f := m.files[original.File.ID]
			f.RelativePath = "renamed.md"
			f.PathKey = "renamed.md"
			m.files[f.ID] = f
		},
	} {
		t.Run(name, func(t *testing.T) {
			change()
			called = false
			if err := s.WithNavigation(t.Context(), "collection", "same.md", original.Base.Hash, visit); err == nil || called {
				t.Fatalf("unsafe target: %v", err)
			}
			r.active = true
			m.files[original.File.ID] = original.File
			if err := os.WriteFile(filepath.Join(r.root, "same.md"), []byte("current"), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNavigationRenameKeepsExactIdentity(t *testing.T) {
	s, _, r := fixture(t)
	if err := os.WriteFile(filepath.Join(r.root, "before.md"), []byte("note"), 0600); err != nil {
		t.Fatal(err)
	}
	original := readBase(t, s, "before.md")
	req := Request{OperationID: "rename-navigation", FileID: original.File.ID, Generation: original.Base.Generation, BaseID: original.Base.ID, Kind: KindRename, DestinationPath: "after.md"}
	if _, err := s.Receive(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	op, err := s.Apply(t.Context(), req.OperationID)
	if err != nil || op.State != Accepted {
		t.Fatalf("rename: %+v %v", op, err)
	}
	called := false
	if err := s.WithNavigation(t.Context(), "collection", "before.md", original.Base.Hash, func(File, Base) error { called = true; return nil }); err == nil || called {
		t.Fatal("old path navigable")
	}
	if err := s.WithNavigation(t.Context(), "collection", "after.md", original.Base.Hash, func(f File, b Base) error {
		called = true
		if f.ID != original.File.ID || f.PathVersion != 1 || b.PathVersion != 1 {
			t.Fatal("rename lost identity")
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("current path: %v", err)
	}
}
