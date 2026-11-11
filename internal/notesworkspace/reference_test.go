package notesworkspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReferenceSnapshotAboveInlineLimitKeepsJournalSmall(t *testing.T) {
	s, store, resolver := fixture(t)
	f, err := os.Create(filepath.Join(resolver.root, "large.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxReferenceBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	spool := filepath.Join(resolver.root, ".spool")
	read, snapshot, err := s.ReadReference(t.Context(), "collection", "large.pdf", spool)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(snapshot)
	info, err := os.Stat(snapshot)
	if err != nil || info.Size() != MaxReferenceBytes+1 || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	base, err := store.Base(t.Context(), read.Base.ID)
	if err != nil || len(base.Content) != 0 || base.ReferenceSize != MaxReferenceBytes+1 || base.Hash == digest(nil) {
		t.Fatal("reference journal retained payload", base, err)
	}
	second, path, err := s.ReadReference(t.Context(), "collection", "large.pdf", spool)
	if err != nil || second.Base.ID != read.Base.ID || second.File.ID != read.File.ID {
		t.Fatal("unstable reference identity", second, err)
	}
	os.Remove(path)
}

func TestReferenceAbsentMoveBaseIsMetadataOnly(t *testing.T) {
	s, _, resolver := fixture(t)
	spool := filepath.Join(resolver.root, ".spool")
	for _, path := range []string{"missing.pdf", "absent-parent/missing.png"} {
		read, snapshot, err := s.ReadReference(t.Context(), "collection", path, spool)
		if err != nil || snapshot != "" || read.Base.Exists || len(read.Base.Content) != 0 || read.File.RelativePath != path {
			t.Fatal("absent reference proof", read, snapshot, err)
		}
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("absent proof created payload spool", err)
	}
}

func TestReferenceSnapshotSizePathAndCancellation(t *testing.T) {
	s, _, resolver := fixture(t)
	spool := filepath.Join(resolver.root, ".spool")
	file, err := os.Create(filepath.Join(resolver.root, "too-big.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	file.Truncate(MaxStreamReferenceBytes + 1)
	file.Close()
	if _, snapshot, err := s.ReadReference(t.Context(), "collection", "too-big.pdf", spool); !errors.Is(err, ErrReferenceLimit) || snapshot != "" {
		t.Fatal("size was not explicit", snapshot, err)
	}
	if err := os.Symlink("too-big.pdf", filepath.Join(resolver.root, "linked.pdf")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadReference(t.Context(), "collection", "linked.pdf", spool); err == nil {
		t.Fatal("followed a link")
	}
	os.WriteFile(filepath.Join(resolver.root, "small.pdf"), []byte("PDF"), 0600)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, snapshot, err := s.ReadReference(ctx, "collection", "small.pdf", spool); !errors.Is(err, context.Canceled) || snapshot != "" {
		t.Fatal("cancelled snapshot retained", snapshot, err)
	}
	entries, _ := os.ReadDir(spool)
	if len(entries) != 0 {
		t.Fatal("leaked snapshot", entries)
	}
}

func TestReferenceSnapshotRestoresOwnedPrivateSpoolAfterHostACLReconciliation(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "spool")
	if err := os.Mkdir(spool, 0750); err != nil {
		t.Fatal(err)
	}
	file, err := newReferenceSnapshot(spool)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	dirInfo, _ := os.Stat(spool)
	fileInfo, _ := file.Stat()
	if dirInfo.Mode().Perm() != 0700 || fileInfo.Mode().Perm() != 0600 {
		t.Fatal("native snapshot privacy contract not restored", dirInfo.Mode(), fileInfo.Mode())
	}
	linked := filepath.Join(filepath.Dir(spool), "linked")
	if err := os.Symlink(spool, linked); err != nil {
		t.Fatal(err)
	}
	if file, err := newReferenceSnapshot(linked); err == nil {
		file.Close()
		t.Fatal("spool symlink accepted")
	}
}
