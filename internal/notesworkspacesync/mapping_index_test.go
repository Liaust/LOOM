package notesworkspacesync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

func TestRetainedMappingIndexResumesAndRebindsWithoutPayload(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed"}[changed], func(t *testing.T) {
			s, native, source, root, old := fixture(t)
			records := s.Store.(*memoryRecords)
			// Simulate the installed legacy state, before locator publication.
			for id := range records.data {
				if len(id) >= len("source_file:retained_") && id[:len("source_file:retained_")] == "source_file:retained_" {
					delete(records.data, id)
				}
			}
			oldKey := "file:" + mapKey(Intent{Workspace: old.Workspace, Collection: old.Collection, Generation: old.Generation, FileID: old.FileID})
			oldRecord := string(records.data[oldKey])
			s.AdoptExisting = true
			s.Scopes[0].Generation = "current"
			s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "current"}, Store: source}}
			head := native.docs[old.Path+"@"+old.NativeRevision]
			s.Native = transitionNative{fakeNative: native, heads: []Evidence{head}}
			if changed {
				if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("B"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			read, err := s.Source.Read(t.Context(), "source", "a.md")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID); !errors.Is(err, ErrHeld) {
				t.Fatal("unindexed legacy file treated as a create", err)
			}
			if err := s.indexRetainedMappings(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
			cursor, err := load[mappingIndexCursor](t.Context(), records, "cursor", "mapping-index-v1")
			if err != nil || cursor.Complete || cursor.After == "" {
				t.Fatal("missing durable continuation", cursor, err)
			}
			if err := s.indexRetainedMappings(t.Context(), 1); err != nil {
				t.Fatal(err)
			}
			next, adopted, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil || next.FileID != old.FileID || next.Generation != "current" || next.SourceSequence != old.SourceSequence+1 || adopted == changed {
				t.Fatal("retained generation transition", next, adopted, err)
			}
			if !changed && (next.NativeRevision != old.NativeRevision || native.counter != 1) {
				t.Fatal("unchanged payload republished", next)
			}
			if changed && (next.NativeRevision == old.NativeRevision || native.counter != 2) {
				t.Fatal("changed payload not published from retained parent", next)
			}
			if string(records.data[oldKey]) != oldRecord {
				t.Fatal("historical mapping rewritten")
			}
			again, adopted, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil || !adopted || again != next {
				t.Fatal("non-idempotent adoption", again, adopted, err)
			}
			intent, _ := operation(old, "old_edit", "pending", "2-old")
			if _, ok := s.scope(intent); ok {
				t.Fatal("locator granted old-generation write authority")
			}
		})
	}
}

func TestRetainedMappingBranchesForCurrentTextButRefusesDifferentScope(t *testing.T) {
	s, native, source, root, old := fixture(t)
	s.AdoptExisting = true
	s.Scopes[0].Generation = "current"
	s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "current"}, Store: source}}
	read, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	head := native.docs[old.Path+"@"+old.NativeRevision]
	newer := head
	newer.Revision = "2-offline"
	native.docs[old.Path+"@"+newer.Revision] = newer
	s.Native = transitionNative{fakeNative: native, heads: []Evidence{newer}}
	if err := s.indexRetainedMappings(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	next, _, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || native.counter != 2 || next.Generation != "current" ||
		len(native.docs[next.Path+"@"+next.NativeRevision].Ancestors) != 1 ||
		native.docs[next.Path+"@"+next.NativeRevision].Ancestors[0] != old.NativeRevision ||
		native.docs[old.Path+"@"+newer.Revision].Revision != newer.Revision {
		t.Fatal("current-source sibling did not preserve outstanding client branch", next, err)
	}
	for _, change := range []func(*Scope){
		func(v *Scope) { v.Workspace = "other" },
		func(v *Scope) { v.SourceCollection = "other" },
		func(v *Scope) { v.Root = "Other" },
	} {
		candidate := s.Scopes[0]
		change(&candidate)
		if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
			_, err := retainedMapping(t.Context(), r, candidate, read.File.ID)
			if err == nil {
				t.Fatal("cross-scope adoption")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRetainedMappingRecoversPreviouslyFailedCreateIntent(t *testing.T) {
	s, native, source, root, old := fixture(t)
	s.AdoptExisting = true
	s.Scopes[0].Generation = "current"
	s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "current"}, Store: source}}
	read, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	id := "export_" + key("w", "c", "current", read.File.ID, read.Base.ID)
	records := s.Store.(*memoryRecords)
	failed := exportRecord{Scope: s.Scopes[0], FileID: read.File.ID, BaseID: read.Base.ID, Path: old.Path, ClientFileID: old.FileID}
	if err := records.Put(t.Context(), "export", id, failed); err != nil {
		t.Fatal(err)
	}
	head := native.docs[old.Path+"@"+old.NativeRevision]
	s.Native = transitionNative{fakeNative: native, heads: []Evidence{head}}
	next, adopted, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || !adopted || next.NativeRevision != old.NativeRevision || native.counter != 1 {
		t.Fatal("failed-create recovery", next, adopted, err)
	}
	retained, err := load[exportRecord](t.Context(), records, "export", id)
	if err != nil || retained != failed {
		t.Fatal("original intent modified", retained, err)
	}
}
