package workspacefiles

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sr "loom.local/loom/internal/serviceregistry"
)

type applicationFilesFixture struct {
	root, revision string
	denied         bool
}

func (f *applicationFilesFixture) source() sr.ApplicationBrowseRoot {
	return sr.ApplicationBrowseRoot{Ref: "appdata_fixture", Label: "Exports", ProjectName: "Goodnotes", SourcePath: f.root, ReadOnly: true, Available: true, Revision: f.revision, Format: "files"}
}
func (f *applicationFilesFixture) ReadBrowse(ctx context.Context, q sr.ApplicationBrowseRead) (sr.ApplicationBrowseResult, error) {
	if f.denied {
		return sr.ApplicationBrowseResult{}, os.ErrPermission
	}
	root := f.source()
	out := sr.ApplicationBrowseResult{Root: &root}
	if q.Operation == "catalog" {
		out.Roots = []sr.ApplicationBrowseRoot{root}
	}
	if q.Operation == "children" {
		for _, name := range q.Names {
			info, e := os.Lstat(filepath.Join(f.root, q.Path, name))
			if e != nil {
				return out, e
			}
			out.Entries = append(out.Entries, sr.ApplicationBrowseEntry{Name: name, Path: filepath.ToSlash(filepath.Join(q.Path, name)), Kind: "file", Size: info.Size(), ModifiedAt: info.ModTime()})
		}
	}
	return out, nil
}
func (f *applicationFilesFixture) OpenBrowse(ctx context.Context, q sr.ApplicationBrowseRead) (*os.File, sr.ApplicationBrowseRoot, error) {
	root := f.source()
	if f.denied {
		return nil, root, os.ErrPermission
	}
	if q.Revision != "" && q.Revision != f.revision {
		return nil, root, ErrRootChanged
	}
	file, e := os.Open(filepath.Join(f.root, q.Path))
	return file, root, e
}
func TestApplicationFilesReadPaginationAndRevocation(t *testing.T) {
	f := &applicationFilesFixture{root: t.TempDir(), revision: "view-a"}
	for _, name := range []string{"a.pdf", "b.pdf", "c.pdf"} {
		if e := os.WriteFile(filepath.Join(f.root, name), []byte("test"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	s := &Service{Applications: f}
	defer s.Close()
	roots, e := s.ApplicationRoots(t.Context(), "", 0, 100)
	if e != nil || len(roots.Roots) != 1 || !roots.Roots[0].ReadOnly || roots.Roots[0].SourceKind != "application_data" {
		t.Fatalf("roots=%+v %v", roots, e)
	}
	page, e := s.List(t.Context(), ListInput{Root: "appdata_fixture", Limit: 1})
	if e != nil || len(page.Entries) != 1 || !page.HasMore {
		t.Fatalf("page=%+v %v", page, e)
	}
	next, e := s.List(t.Context(), ListInput{Root: "appdata_fixture", Limit: 1, Cursor: page.NextCursor})
	if e != nil || len(next.Entries) != 1 || next.Entries[0].Name == page.Entries[0].Name {
		t.Fatalf("next=%+v %v", next, e)
	}
	file, metadata, e := s.Open(t.Context(), "appdata_fixture", "a.pdf")
	if e != nil {
		t.Fatal(e)
	}
	file.Close()
	if metadata.Size != 4 {
		t.Fatal(metadata)
	}
	if _, _, e = s.Open(t.Context(), "appdata_fixture", "../outside"); e == nil {
		t.Fatal("traversal allowed")
	}
	f.revision = "view-b"
	if _, _, e = s.Open(WithApplicationView(t.Context(), "view-a"), "appdata_fixture", "a.pdf"); e == nil {
		t.Fatal("view handover silently opened a different scope")
	}
	if _, e = s.List(t.Context(), ListInput{Root: "appdata_fixture", Limit: 1, Cursor: next.NextCursor}); e == nil {
		t.Fatal("stale view cursor accepted")
	}
	_, newMetadata, e := s.Open(t.Context(), "appdata_fixture", "a.pdf")
	if e != nil {
		t.Fatal(e)
	}
	if newMetadata.Revision == metadata.Revision {
		t.Fatal("view change reused cache validator")
	}
	f.denied = true
	if _, _, e = s.Open(t.Context(), "appdata_fixture", "a.pdf"); e == nil {
		t.Fatal("revoked view read")
	}
}
