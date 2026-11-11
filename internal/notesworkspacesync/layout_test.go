package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type layoutNative struct {
	*fakeNative
	layouts int
}

func TestDirectoryLayoutAcrossGenerationSeparatesMoveFromWriteReadmission(t *testing.T) {
	s, n, store, root, _ := fixture(t)
	s.AdoptExisting, s.LayoutPublication = true, true
	if err := s.indexRetainedMappings(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	s.Native = &layoutNative{fakeNative: n}
	if err := os.Mkdir(filepath.Join(root, "paper"), 0700); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.(NotesSource).ReadDirectory(t.Context(), "source", "paper")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.ExportDirectory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior := s.Scopes[0]
	s.Source = NotesSource{Service: notesworkspace.Service{Store: store, Sources: generationResolver{root, "expanded"}}}
	s.Scopes[0].Generation, s.Scopes[0].Root = "expanded", "Projects/Example/Research"
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: prior.Generation}}); err != nil {
		t.Fatal(err)
	}
	// Older code published the current-root directory without retiring its old
	// container. That successor is not a completed layout receipt.
	pre, err := s.Source.(NotesSource).ReadDirectory(t.Context(), "source", "paper")
	if err != nil {
		t.Fatal(err)
	}
	s.AdoptExisting, s.LayoutPublication = false, false
	if _, err := s.ExportDirectory(t.Context(), "c", pre.File.ID, pre.Base.ID); err != nil {
		t.Fatal(err)
	}
	s.AdoptExisting, s.LayoutPublication = true, true
	_, moved, held, err := s.advanceLayouts(t.Context(), 32)
	if err != nil || moved != 1 || held != 0 {
		t.Fatal("directory handover failed", moved, held, err)
	}
	mapping, err := s.exportMapping(t.Context(), s.Store.(*memoryRecords), s.Scopes[0], read.File.ID)
	if err != nil || mapping.Latest == nil || mapping.Latest.Writable || mapping.Latest.FileID != old.FileID {
		t.Fatal("move silently renewed old write authority", mapping, err)
	}
	current, err := s.Source.(NotesSource).ReadDirectory(t.Context(), "source", "paper")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ExportDirectory(t.Context(), "c", current.File.ID, current.Base.ID)
	if err != nil || !b.Writable || b.FileID != old.FileID || b.SourceSequence <= mapping.Latest.SourceSequence {
		t.Fatal("current admission did not restore directory permission", b, err)
	}
	cursorID := "layout_scan_v3_" + key(s.Scopes[0].Workspace, s.Scopes[0].Collection, s.Scopes[0].Generation, s.Scopes[0].Root)
	if err := s.Store.(*memoryRecords).Put(t.Context(), "cursor", cursorID, layoutCursor{}); err != nil {
		t.Fatal(err)
	}
	if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 0 {
		t.Fatal("completed directory moved again", moved, held, err)
	}
	if info, err := os.Stat(filepath.Join(root, "paper")); err != nil || !info.IsDir() {
		t.Fatal("canonical directory moved", err)
	}
}

func TestDirectoryLayoutLegacyReceiptGenerationRecovery(t *testing.T) {
	s, n, store, root, _ := fixture(t)
	s.AdoptExisting, s.LayoutPublication = true, true
	s.Native = &layoutNative{fakeNative: n}
	if err := s.indexRetainedMappings(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "paper"), 0700); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.(NotesSource).ReadDirectory(t.Context(), "source", "paper")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.ExportDirectory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	prior := s.Scopes[0]
	s.Source = NotesSource{Service: notesworkspace.Service{Store: store, Sources: generationResolver{root, "moved"}}}
	s.Scopes[0].Generation, s.Scopes[0].Root = "moved", "Projects/Example/Research"
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: prior.Generation}}); err != nil {
		t.Fatal(err)
	}
	if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 1 || held != 0 {
		t.Fatal(moved, held, err)
	}
	records := s.Store.(*memoryRecords)
	origin, err := s.exportMapping(t.Context(), records, s.Scopes[0], read.File.ID)
	if err != nil || origin.Latest.FileID != old.FileID {
		t.Fatal(origin, err)
	}
	// Reproduce the old scope-only relabel without changing the intact origin
	// or its binding receipt. These are protocol metadata, not note contents.
	s.Scopes[0].Generation = "current"
	s.Source = NotesSource{Service: notesworkspace.Service{Store: store, Sources: generationResolver{root, "current"}}}
	broken := origin
	broken.Scope = s.Scopes[0]
	brokenKey := mapKey(Intent{Workspace: broken.Scope.Workspace, Collection: broken.Scope.Collection, Generation: broken.Scope.Generation, FileID: broken.ClientFileID})
	if err := records.Put(t.Context(), "file", brokenKey, broken); err != nil {
		t.Fatal(err)
	}
	if err := records.Put(t.Context(), "source_file", sourceKey(broken.Scope, broken.SourceFileID), broken.ClientFileID); err != nil {
		t.Fatal(err)
	}
	if err := records.Put(t.Context(), "source_file", mappingLocator(broken.Scope, broken.SourceFileID), brokenKey); err != nil {
		t.Fatal(err)
	}
	current, err := s.Source.(NotesSource).ReadDirectory(t.Context(), "source", "paper")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.ExportDirectory(t.Context(), "c", current.File.ID, current.Base.ID)
	if err != nil || next.Generation != "current" || next.FileID != old.FileID || !next.Writable || next.SourceSequence <= origin.Latest.SourceSequence {
		t.Fatal("exact historical receipt did not recover current export", next, err)
	}
	if again, err := s.ExportDirectory(t.Context(), "c", current.File.ID, current.Base.ID); err != nil || again != next {
		t.Fatal("recovered directory did not converge", again, err)
	}
	retained, err := load[FileMapping](t.Context(), records, "file", mapKey(Intent{Workspace: origin.Scope.Workspace, Collection: origin.Scope.Collection, Generation: origin.Scope.Generation, FileID: origin.ClientFileID}))
	if err != nil || retained.Latest == nil || *retained.Latest != *origin.Latest {
		t.Fatal("origin receipt was changed", retained, err)
	}
	broken.Latest = &next
	broken.Scope.Generation = "unverified"
	if _, err := recoverRetainedLayoutMapping(t.Context(), records, broken); !errors.Is(err, ErrHeld) {
		t.Fatal("non-layout mismatch was accepted", err)
	}
	broken.Latest = origin.Latest
	broken.Scope.Collection = "missing-origin"
	if _, err := recoverRetainedLayoutMapping(t.Context(), records, broken); !errors.Is(err, ErrHeld) {
		t.Fatal("missing origin was treated as permission to create another identity", err)
	}
}

func TestReadOnlyReferenceLayoutRechecksCurrentGenerationBytes(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			s, native, store, root, _ := fixture(t)
			if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("PDF bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			read, err := s.Source.Read(t.Context(), "source", "paper.pdf")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil {
				t.Fatal(err)
			}
			prior := s.Scopes[0]
			s.Source = NotesSource{Service: notesworkspace.Service{Store: store, Sources: generationResolver{root, "expanded"}}}
			s.Native, s.LayoutPublication, s.ReferenceSpool = &layoutNative{fakeNative: native}, true, t.TempDir()
			s.Scopes[0].Generation, s.Scopes[0].Root = "expanded", "Projects/Example/Research"
			if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: prior.Generation}}); err != nil {
				t.Fatal(err)
			}
			if changed {
				if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("new PDF"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, moved, held, err := s.advanceLayouts(t.Context(), 32)
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				if moved != 0 || held != 1 {
					t.Fatal("changed bytes were relabeled", moved, held)
				}
				return
			}
			if moved != 1 || held != 0 {
				t.Fatal("current reference did not move", moved, held)
			}
			mapping, err := s.exportMapping(t.Context(), s.Store.(*memoryRecords), s.Scopes[0], read.File.ID)
			if err != nil || mapping.Latest == nil || mapping.Latest.Writable || mapping.Latest.SHA256 != binding.SHA256 || mapping.ClientFileID != binding.FileID {
				t.Fatal("reference identity or read-only boundary changed", mapping, err)
			}
			cursorID := "layout_scan_v3_" + key(s.Scopes[0].Workspace, s.Scopes[0].Collection, s.Scopes[0].Generation, s.Scopes[0].Root)
			if err := s.Store.(*memoryRecords).Put(t.Context(), "cursor", cursorID, layoutCursor{}); err != nil {
				t.Fatal(err)
			}
			s.Source = failingReferenceSource{Source: s.Source}
			if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 0 {
				t.Fatal("completed successor reread historical PDF", moved, held, err)
			}
		})
	}
}

func TestLayoutHeldRetrySleepsUntilItsRetainedDeadline(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	s.LayoutPublication = true
	scope := s.Scopes[0]
	deadline := time.Now().Add(time.Minute)
	r := s.Store.(*memoryRecords)
	if err := r.Put(t.Context(), "cursor", layoutRootsKey(scope), layoutRoots{Roots: []string{scope.Root, "Previous/Notes"}}); err != nil {
		t.Fatal(err)
	}
	id := "layout_scan_v3_" + key(scope.Workspace, scope.Collection, scope.Generation, scope.Root)
	if err := r.Put(t.Context(), "cursor", id, layoutCursor{Complete: true, Retry: true, RetryAt: deadline}); err != nil {
		t.Fatal(err)
	}
	counter := &layoutScanStore{Store: s.Store}
	s.Store = counter
	more, moved, held, err := s.advanceLayouts(t.Context(), 32)
	if err != nil || more || moved != 0 || held != 0 || counter.fileLists != 0 || !s.LayoutRetryAt.Equal(deadline) {
		t.Fatal("held layout spun or lost retry deadline", more, moved, held, counter.fileLists, s.LayoutRetryAt, err)
	}
}

type layoutScanStore struct {
	Store
	fileLists int
}

type layoutScanRecords struct {
	Records
	store *layoutScanStore
}

func (s *layoutScanStore) WithReplica(ctx context.Context, replica, epoch string, fn func(Records) error) error {
	return s.Store.WithReplica(ctx, replica, epoch, func(r Records) error {
		return fn(layoutScanRecords{Records: r, store: s})
	})
}

func (r layoutScanRecords) List(ctx context.Context, kind, after string, limit int) ([]string, error) {
	if kind == "file" {
		r.store.fileLists++
	}
	return r.Records.List(ctx, kind, after, limit)
}

func TestLayoutScansOnlyScopesWithRetainedMoveEvidence(t *testing.T) {
	for _, evidence := range []string{"absent", "current-root-only", "registered-move"} {
		t.Run(evidence, func(t *testing.T) {
			s, native, _, _, _ := fixture(t)
			prior := s.Scopes[0]
			s.LayoutPublication = true
			s.Native = &layoutNative{fakeNative: native}
			for i := 1; i < 19; i++ {
				scope := prior
				scope.Collection, scope.SourceCollection, scope.Root = fmt.Sprintf("unchanged_%d", i), fmt.Sprintf("source_%d", i), fmt.Sprintf("Unchanged/%d", i)
				s.Scopes = append(s.Scopes, scope)
			}
			if evidence == "current-root-only" {
				for _, scope := range s.Scopes {
					if err := s.Store.(*memoryRecords).Put(t.Context(), "cursor", layoutRootsKey(scope), layoutRoots{Roots: []string{scope.Root}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if evidence == "registered-move" {
				s.Scopes[0].Root = "Projects/_Passive/Example/Notes"
				if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root}}); err != nil {
					t.Fatal(err)
				}
			}
			counter := &layoutScanStore{Store: s.Store}
			s.Store = counter
			more, moved, held, err := s.advanceLayouts(t.Context(), 32)
			want := 0
			if evidence == "registered-move" {
				want = 1
			}
			if err != nil || more || held != 0 || moved != want || counter.fileLists != want {
				t.Fatalf("scopes without a move scanned mappings: lists=%d moved=%d held=%d more=%v err=%v", counter.fileLists, moved, held, more, err)
			}
		})
	}
}

type retainedLayoutReferenceSource struct {
	NotesSource
	reads, exports int
}

func (s *retainedLayoutReferenceSource) Read(context.Context, string, string) (notesworkspace.ReadResult, error) {
	s.reads++
	return notesworkspace.ReadResult{}, errors.New("reference payload must not be read for a layout")
}

func (s *retainedLayoutReferenceSource) WithExport(ctx context.Context, fileID, baseID string, fn func(notesworkspace.File, notesworkspace.Base) error) error {
	s.exports++
	return s.NotesSource.WithExport(ctx, fileID, baseID, fn)
}

func TestLayoutStreamedReferenceUsesExactRetainedBaseWithoutPayload(t *testing.T) {
	for _, scenario := range []string{"current", "stale-generation", "wrong-binding-base", "changed-base-hash"} {
		t.Run(scenario, func(t *testing.T) {
			s, native, store, _, original := fixture(t)
			records := s.Store.(*memoryRecords)
			record, err := load[BindingRecord](t.Context(), records, "binding", original.ID)
			if err != nil {
				t.Fatal(err)
			}
			file := store.files[record.SourceFileID]
			file.RelativePath, file.PathKey = "notebook.pdf", "notebook.pdf"
			store.files[file.ID] = file
			base := notesworkspace.Base{ID: "streamed-reference-base", FileID: file.ID, PathVersion: file.PathVersion,
				Generation: s.Scopes[0].Generation, Exists: true, Hash: digest([]byte("persisted PDF digest")), ReferenceSize: (64 << 20) + 1}
			if scenario == "stale-generation" {
				base.Generation = "withdrawn-generation"
			}
			store.bases[base.ID] = base
			old := original
			old.Path, old.SHA256, old.Writable = old.CollectionRoot+"/"+file.RelativePath, base.Hash, false
			old.SourceBase = "base_" + key(s.Replica, base.ID)
			record.Binding, record.SourceBaseID = old, base.ID
			if scenario == "wrong-binding-base" {
				record.SourceBaseID = "unrelated-base"
			}
			if err := records.Put(t.Context(), "binding", old.ID, record); err != nil {
				t.Fatal(err)
			}
			mapping, err := s.exportMapping(t.Context(), records, s.Scopes[0], file.ID)
			// The old text binding and the PDF metadata are replaced together below.
			if err == nil {
				mapping.Latest = &old
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.saveMapping(t.Context(), records, Intent{Workspace: mapping.Scope.Workspace, Collection: mapping.Scope.Collection,
				Generation: mapping.Scope.Generation, FileID: mapping.ClientFileID}, mapping); err != nil {
				t.Fatal(err)
			}
			if scenario == "changed-base-hash" {
				changed := base
				changed.Hash = digest([]byte("another PDF"))
				store.bases[base.ID] = changed
			}
			native.docs[old.Path+"@"+old.NativeRevision] = Evidence{ID: old.Path, Path: old.Path, Revision: old.NativeRevision,
				Status: "held", Reason: "reference_binary", SHA256: strings.TrimPrefix(old.SHA256, "sha256:")}
			layout := &layoutNative{fakeNative: native}
			source := &retainedLayoutReferenceSource{NotesSource: s.Source.(NotesSource)}
			s.Source, s.Native, s.LayoutPublication = source, layout, true
			prior := s.Scopes[0]
			s.Scopes[0].Root = "Projects/_Passive/Goodnotes/Notebooks"
			if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root}}); err != nil {
				t.Fatal(err)
			}
			more, moved, held, err := s.advanceLayouts(t.Context(), 32)
			if err != nil || more || source.reads != 0 {
				t.Fatalf("reference layout read payload: more=%v moved=%d held=%d reads=%d err=%v", more, moved, held, source.reads, err)
			}
			if scenario != "current" {
				if moved != 0 || held != 1 || layout.layouts != 0 || store.bases[base.ID].Generation != base.Generation {
					t.Fatal("invalid retained base moved or was relabeled", moved, held, layout.layouts)
				}
				return
			}
			if moved != 1 || held != 0 || layout.layouts != 1 || source.exports != 1 {
				t.Fatal("reference move did not retain source authorization", moved, held, layout.layouts, source.exports)
			}
			updated, err := s.exportMapping(t.Context(), records, s.Scopes[0], file.ID)
			if err != nil || updated.Latest == nil || updated.Latest.Path != s.Scopes[0].Root+"/notebook.pdf" || updated.Latest.Writable ||
				updated.Latest.SHA256 != base.Hash || updated.Latest.SourceBase != old.SourceBase || updated.Latest.SourceSequence != old.SourceSequence+1 {
				t.Fatal("reference identity/base changed", updated, err)
			}
			if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 0 || layout.layouts != 1 || source.reads != 0 {
				t.Fatal("reference layout replay read or republished payload", moved, held, err)
			}
		})
	}
}

func TestArchivedRuntimeSelectionCannotReadOrExportOutsidePrefix(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	read, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	sources := runtimeSources{NotesSource: s.Source.(NotesSource), Archived: map[string]knowledge.WorkspaceArchiveSource{"source": {}},
		Selections: []RuntimeSelection{{Scope: Scope{SourceCollection: "source"}, PathPrefix: "selected"}}}
	if _, err := sources.Read(t.Context(), "source", "a.md"); !errors.Is(err, notesworkspace.ErrMembership) {
		t.Fatal("archive selection bypass", err)
	}
	called := false
	if err := sources.WithExport(t.Context(), read.File.ID, read.Base.ID, func(notesworkspace.File, notesworkspace.Base) error { called = true; return nil }); !errors.Is(err, notesworkspace.ErrMembership) || called {
		t.Fatal("archive export bypass", err)
	}
}

func TestLayoutControlsAreInvisibleToOlderStructuralClients(t *testing.T) {
	for _, h := range []Header{{1, "layout", "layout_test"}, {1, "binding", "layout_binding_test"}} {
		p := controlPath(h)
		if !strings.HasPrefix(p, LayoutPrefix) || strings.HasPrefix(p, StructurePrefix) || !strings.HasPrefix(p, Prefix+"payload/") {
			t.Fatal("old client could interpret a partial layout", p)
		}
	}
}

func (n *layoutNative) Call(ctx context.Context, request map[string]any, out any) error {
	if request["method"] == "layout.preflight" {
		raw, _ := json.Marshal(layoutProbe{Status: "ready"})
		return json.Unmarshal(raw, out)
	}
	if request["method"] == "source.move.status" {
		raw, _ := json.Marshal(Evidence{Status: "pending", Phase: "unpublished"})
		return json.Unmarshal(raw, out)
	}
	if request["method"] == "source.move" {
		raw, _ := json.Marshal(Evidence{Status: "published", Phase: "retired"})
		return json.Unmarshal(raw, out)
	}
	if request["method"] == "layout.publish" {
		old := request["previous"].(*Binding)
		path := request["path"].(string)
		original := n.docs[old.Path+"@"+old.NativeRevision]
		original.Path, original.ID, original.Revision = path, path, "1-layout"
		n.docs[path+"@1-layout"] = original
		n.layouts++
		raw, _ := json.Marshal(Evidence{Status: "published", Revision: "1-layout", SHA256: old.SHA256[7:]})
		return json.Unmarshal(raw, out)
	}
	if request["method"] == "layout.retire" {
		raw, _ := json.Marshal(Evidence{Status: "published"})
		return json.Unmarshal(raw, out)
	}
	return n.fakeNative.Call(ctx, request, out)
}

func TestLayoutMovesOnlyTheViewAndAcceptsExactOfflineOldPath(t *testing.T) {
	s, native, _, root, old := fixture(t)
	layout := &layoutNative{fakeNative: native}
	s.Native, s.LayoutPublication = layout, true
	prior := s.Scopes[0]
	s.Scopes[0].Root = "Projects/_Passive/Example/Notes"
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root}}); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	more, moved, held, err := s.advanceLayouts(t.Context(), 32)
	if err != nil || more || moved != 1 || held != 0 {
		t.Fatal(more, moved, held, err)
	}
	next, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || next.Path != s.Scopes[0].Root+"/a.md" || next.FileID != old.FileID || next.SourceBase != old.SourceBase || next.SourceSequence != old.SourceSequence+1 || layout.layouts != 1 {
		t.Fatal(next, err)
	}
	if _, moved, held, err = s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 0 || layout.layouts != 1 {
		t.Fatal("replayed completed layout", moved, held, err)
	}
	content, _ := os.ReadFile(filepath.Join(root, "a.md"))
	if string(content) != "A" {
		t.Fatal("layout changed source")
	}
	// An offline phone can still submit its exact retained old binding. The
	// canonical base comparison, not a guessed mapping winner, decides acceptance.
	i, p := operation(old, "offline-old-path", "offline edit", "2-offline")
	native.content(old.Path, "2-offline", old.NativeRevision, "offline edit")
	native.add(i)
	native.add(p)
	result, err := s.Step(t.Context(), 128)
	if err != nil || result.Applied != 1 {
		t.Fatal(result, err)
	}
	content, _ = os.ReadFile(filepath.Join(root, "a.md"))
	if string(content) != "offline edit" {
		t.Fatal("old path edit lost", string(content))
	}
}

func TestLayoutRequiresRegisteredPredecessorAndIsDefaultOff(t *testing.T) {
	s, _, _, _, _ := fixture(t)
	read, _ := s.Source.Read(t.Context(), "source", "a.md")
	s.Scopes[0].Root = "Projects/_Passive/Unreviewed"
	if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); err == nil {
		t.Fatal("unregistered move admitted")
	}
	s.LayoutPublication = true
	if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); err == nil {
		t.Fatal("missing alias admitted")
	}
}

func TestArchivedTextMoveIsReadOnlyAndRetainsOldIntent(t *testing.T) {
	s, native, _, _, old := fixture(t)
	s.Native, s.LayoutPublication = &layoutNative{fakeNative: native}, true
	prior := s.Scopes[0]
	s.Scopes[0].Root, s.Scopes[0].ReadOnly = "Projects/_Archived/Example/Notes", true
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: prior.Generation}}); err != nil {
		t.Fatal(err)
	}
	read, _ := s.Source.Read(t.Context(), "source", "a.md")
	binding, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || binding.Writable || binding.SHA256 != old.SHA256 {
		t.Fatal(binding, err)
	}
	i, p := operation(old, "archived-old-edit", "retained draft", "2-archive")
	native.content(old.Path, "2-archive", old.NativeRevision, "retained draft")
	native.add(i)
	native.add(p)
	progress, err := s.Step(t.Context(), 128)
	if err != nil || progress.Applied != 0 {
		t.Fatal(progress, err)
	}
	join, err := load[Join](t.Context(), s.Store.(*memoryRecords), "operation", i.ID)
	if err != nil || join.Reason != "archive_reference_read_only" || join.IntentRaw == "" {
		t.Fatal(join, err)
	}
}

func TestWorkStateRetainsConfiguredRootsAndReversesWithoutNewCollection(t *testing.T) {
	cfg := runtimeFixture()
	cfg.Scopes[0].Root = "Projects/Existing Display/Notes"
	candidate := enrollmentCandidate{ID: cfg.Scopes[0].SourceCollection, Root: "Projects/Renamed/Notes", WorkState: "passive"}
	selected, err := mergeEnrollmentSelections(cfg, EnrollmentInventory{}, []enrollmentCandidate{candidate})
	if err != nil || selected[0].Root != cfg.Scopes[0].Root || selected[0].DesiredRoot != "Projects/_Passive/Existing Display/Notes" {
		t.Fatal(selected, err)
	}
	cfg.LayoutPublication = true
	previous := EnrollmentInventory{Collections: []EnrollmentCollection{{RuntimeSelection: selected[0]}}}
	selected, err = mergeEnrollmentSelections(cfg, previous, []enrollmentCandidate{candidate})
	if err != nil || selected[0].Root != "Projects/_Passive/Existing Display/Notes" || selected[0].Collection != cfg.Scopes[0].Collection || selected[0].Generation != cfg.Scopes[0].Generation {
		t.Fatal(selected, err)
	}
	previous.Collections[0].RuntimeSelection = selected[0]
	candidate.WorkState = "working"
	selected, err = mergeEnrollmentSelections(cfg, previous, []enrollmentCandidate{candidate})
	if err != nil || selected[0].Root != cfg.Scopes[0].Root || selected[0].LayoutFrom != "Projects/_Passive/Existing Display/Notes" {
		t.Fatal(selected, err)
	}
}
