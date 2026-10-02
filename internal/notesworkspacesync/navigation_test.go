package notesworkspacesync

import (
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

func TestNavigationExactBinding(t *testing.T) {
	scope := Scope{Workspace: "pilot", Collection: "personal", SourceCollection: "source-personal", Generation: "generation", Root: "Personal"}
	file := notesworkspace.File{ID: "notes_file_personal", CollectionID: scope.SourceCollection, RelativePath: "same.md", PathVersion: 1}
	base := notesworkspace.Base{ID: "notes_base_exact", FileID: file.ID, Generation: scope.Generation, Hash: "sha256:" + strings.Repeat("a", 64), PathVersion: 1, Exists: true}
	b := Binding{Header: Header{1, "binding", "binding_exact"}, Workspace: scope.Workspace, Collection: scope.Collection, Generation: scope.Generation, FileID: "file_personal", CollectionRoot: scope.Root, Path: "Personal/same.md", SourceBase: "base_exact", NativeRevision: "1-abc", SHA256: base.Hash, Writable: true}
	mapping := FileMapping{Scope: scope, ClientFileID: b.FileID, SourceFileID: file.ID, Latest: &b}
	record := BindingRecord{Binding: b, SourceFileID: file.ID, SourceBaseID: base.ID}
	if _, ok := verifiedNavigationBinding(scope, file, base, mapping, record); !ok {
		t.Fatal("exact binding rejected")
	}
	for name, change := range map[string]func(*Scope, *notesworkspace.File, *notesworkspace.Base, *FileMapping, *BindingRecord){
		"same-name project": func(s *Scope, _ *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			s.SourceCollection = "source-project"
			s.Collection = "project"
			s.Root = "Project"
		},
		"renamed binding lag": func(_ *Scope, f *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			f.RelativePath = "renamed.md"
		},
		"deleted": func(_ *Scope, f *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			f.Deleted = true
		},
		"reenrolled": func(s *Scope, _ *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			s.Generation = "new-generation"
		},
		"old path version": func(_ *Scope, _ *notesworkspace.File, b *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			b.PathVersion = 0
		},
		"old source bytes": func(_ *Scope, _ *notesworkspace.File, b *notesworkspace.Base, _ *FileMapping, _ *BindingRecord) {
			b.Hash = "sha256:" + strings.Repeat("b", 64)
		},
		"unbound": func(_ *Scope, _ *notesworkspace.File, _ *notesworkspace.Base, m *FileMapping, _ *BindingRecord) {
			m.Latest = nil
		},
		"different original identity": func(_ *Scope, _ *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, r *BindingRecord) {
			r.SourceFileID = "notes_file_project"
		},
		"different base": func(_ *Scope, _ *notesworkspace.File, _ *notesworkspace.Base, _ *FileMapping, r *BindingRecord) {
			r.SourceBaseID = "notes_base_other"
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, a, m, r := scope, file, base, clone(mapping), record
			change(&s, &f, &a, &m, &r)
			if _, ok := verifiedNavigationBinding(s, f, a, m, r); ok {
				t.Fatal("mismatched binding accepted")
			}
		})
	}
	file.RelativePath = "scan.pdf"
	b.Path = "Personal/scan.pdf"
	b.Writable = false
	mapping.Latest = &b
	record.Binding = b
	if got, ok := verifiedNavigationBinding(scope, file, base, mapping, record); !ok || got.Writable {
		t.Fatal("original reference cannot be located read-only")
	}
	b.Writable = true
	record.Binding = b
	if _, ok := verifiedNavigationBinding(scope, file, base, mapping, record); ok {
		t.Fatal("OCR source became writable")
	}
}

func TestNavigationDoesNotConflateReadiness(t *testing.T) {
	out := navigationResult(knowledge.NotesPassageInput{})
	if out.Binding != nil || out.Sync != "not_observed" || out.LocalSearch != "device_local_not_observed" || out.SemanticIndex != "not_observed" {
		t.Fatalf("invented readiness: %+v", out)
	}
}

func TestNavigationHistoricalAndArchivedHaveNoTarget(t *testing.T) {
	for _, test := range []struct {
		passage knowledge.NotesPassage
		status  string
	}{
		{knowledge.NotesPassage{Historical: true}, "stale_citation"},
		{knowledge.NotesPassage{Custody: knowledge.NotesCustodyContext{SourceLifecycle: knowledge.SourceLifecycleArchived}}, "archived"},
	} {
		out := navigationResult(knowledge.NotesPassageInput{})
		if navigationCurrentPassage(&out, test.passage) || out.Status != test.status || out.Binding != nil {
			t.Fatalf("historical target: %+v", out)
		}
	}
}
