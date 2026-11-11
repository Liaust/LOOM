package workspacefiles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesFreshReadsRejectReboundRoot(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "note.md", "authorized original")
	if _, err := s.Stat(context.Background(), "box", "note.md"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFile(t, outside, "note.md", "outside original root")
	if err := os.Rename(root, root+"-held"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root + "-held") })
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	file, _, err := s.Open(context.Background(), "box", "note.md")
	if err == nil {
		file.Close()
		t.Fatal("followed a rebound root")
	}
	if !errors.Is(err, ErrRootChanged) {
		t.Fatalf("root error: %v", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "note.md", "replacement directory")
	if _, _, err := s.Open(context.Background(), "box", "note.md"); !errors.Is(err, ErrRootChanged) {
		t.Fatalf("new inode adopted: %v", err)
	}
}

func TestFilesCursorRejectsLinkToMovedMandatoryDirectory(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "visible/a.md", "a")
	writeFile(t, root, "visible/b.md", "b")
	page, err := s.List(context.Background(), ListInput{Root: "box", Path: "visible", Limit: 1})
	if err != nil || !page.HasMore {
		t.Fatalf("first page: %+v %v", page, err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".loom/state"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "visible"), filepath.Join(root, ".loom/state/moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".loom/state/moved", filepath.Join(root, "visible")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ListInput{Root: "box", Path: "visible", Limit: 1, Cursor: page.NextCursor}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor followed substituted link: %v", err)
	}
	if len(s.leases) != 0 {
		t.Fatal("invalid cursor retained handles")
	}
}

func TestFilesCompleteListingsDoNotEvictActiveCursor(t *testing.T) {
	s, root := filesFixture(t)
	writeFile(t, root, "large/a.md", "a")
	writeFile(t, root, "large/b.md", "b")
	writeFile(t, root, "small/only.md", "one")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	s.MaxLeases = 1
	page, err := s.List(context.Background(), ListInput{Root: "box", Path: "large", Limit: 1})
	if err != nil || !page.HasMore {
		t.Fatalf("first page: %+v %v", page, err)
	}
	for _, relative := range []string{"small", "empty"} {
		result, err := s.List(context.Background(), ListInput{Root: "box", Path: relative, Limit: 1})
		if err != nil || result.HasMore {
			t.Fatalf("complete page: %+v %v", result, err)
		}
	}
	if len(s.leases) != 1 {
		t.Fatalf("unexpected pagination leases: %d", len(s.leases))
	}
	next, err := s.List(context.Background(), ListInput{Root: "box", Path: "large", Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Entries) != 1 || next.HasMore {
		t.Fatalf("active cursor lost: %+v %v", next, err)
	}
	for _, lease := range s.leases {
		if lease.directory != nil || lease.rootHandle != nil {
			t.Fatal("complete page retained file handles")
		}
	}
	replay, err := s.List(context.Background(), ListInput{Root: "box", Path: "large", Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(replay.Entries) != 1 {
		t.Fatalf("completed page replay: %+v %v", replay, err)
	}
}
