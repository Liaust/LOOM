package notesworkspacesync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/notesworkspace"
)

type outgoingLayoutNative struct {
	*layoutNative
	heads []Evidence
}

type exportAdmissionNative struct {
	*fakeNative
	requests []map[string]any
}

func (n *exportAdmissionNative) Call(ctx context.Context, request map[string]any, out any) error {
	if request["method"] == "publish" || request["method"] == "publish.reference" {
		n.requests = append(n.requests, clone(request))
	}
	return n.fakeNative.Call(ctx, request, out)
}

func TestIdenticalAdoptionOnlyForUnmappedTextSourceExports(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	spy := &exportAdmissionNative{fakeNative: native}
	s.Native, s.AdoptExisting = spy, true
	if err := s.indexRetainedMappings(t.Context(), 128); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new.md", "new.pdf", "a.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("source text"), 0600); err != nil {
			t.Fatal(err)
		}
		read, err := s.Source.Read(t.Context(), "source", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); err != nil {
			t.Fatal(err)
		}
		last := spy.requests[len(spy.requests)-1]
		if (last["adoptIdentical"] == true) != (name == "new.md") {
			t.Fatal("wrong adoption scope", name, last)
		}
	}
}

func (n outgoingLayoutNative) Call(ctx context.Context, request map[string]any, out any) error {
	if request["method"] == "leaves" {
		return (transitionNative{fakeNative: n.fakeNative, heads: n.heads}).Call(ctx, request, out)
	}
	return n.layoutNative.Call(ctx, request, out)
}

func TestOutgoingLayoutCatchesUpRetainedGenerationBeforeMove(t *testing.T) {
	for _, scenario := range []string{"same-hash", "different-hash", "unknown-root"} {
		t.Run(scenario, func(t *testing.T) {
			s, native, source, root, _ := fixture(t)
			path := filepath.Join(root, "notebook.pdf")
			if err := os.WriteFile(path, []byte("%PDF-old"), 0600); err != nil {
				t.Fatal(err)
			}
			read, err := s.Source.Read(t.Context(), "source", "notebook.pdf")
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil {
				t.Fatal(err)
			}
			records := s.Store.(*memoryRecords)
			oldKey := "file:" + mapKey(Intent{Workspace: old.Workspace, Collection: old.Collection, Generation: old.Generation, FileID: old.FileID})
			oldRecord := string(records.data[oldKey])
			prior := s.Scopes[0]
			s.Scopes[0].Generation, s.Scopes[0].Root = "current", "Projects/_Passive/Goodnotes/Notebooks"
			s.AdoptExisting, s.LayoutPublication = true, true
			s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "current"}, Store: source}}
			layout := &layoutNative{fakeNative: native}
			head := native.docs[old.Path+"@"+old.NativeRevision]
			head.Status, head.Reason, head.Content = "held", "reference_binary", nil
			native.docs[old.Path+"@"+old.NativeRevision] = head
			s.Native = outgoingLayoutNative{layoutNative: layout, heads: []Evidence{head}}
			selection := RuntimeSelection{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: "selection-generation"}
			if scenario == "unknown-root" {
				selection.LayoutFrom = "Unregistered"
				// Permit ordinary predecessor lookup, but not an unregistered root.
				s.Scopes[0].PreviousGeneration = prior.Generation
			}
			if err := s.prepareLayouts(t.Context(), []RuntimeSelection{selection}); err != nil {
				t.Fatal(err)
			}
			if scenario == "different-hash" {
				if err := os.WriteFile(path, []byte("%PDF-new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := s.Source.Read(t.Context(), "source", "notebook.pdf")
			if err != nil {
				t.Fatal(err)
			}
			count := native.counter
			caughtUp, _, err := s.ExportInventory(t.Context(), "c", fresh.File.ID, fresh.Base.ID)
			if scenario == "unknown-root" {
				if !errors.Is(err, ErrHeld) || native.counter != count || layout.layouts != 0 || s.outgoingLayoutScopeAllowed(t.Context(), records, prior, s.Scopes[0]) {
					t.Fatal("unregistered root admitted", caughtUp, err)
				}
				return
			}
			if err != nil || caughtUp.Generation != "current" || caughtUp.Path != old.Path || caughtUp.FileID != old.FileID || caughtUp.Writable ||
				caughtUp.SourceBase != "base_"+key(s.Replica, fresh.Base.ID) || caughtUp.SHA256 != fresh.Base.Hash || caughtUp.SourceSequence != old.SourceSequence+1 || layout.layouts != 0 {
				t.Fatal("did not catch up at the retained root", caughtUp, err)
			}
			if scenario == "same-hash" {
				if native.counter != count || caughtUp.NativeRevision != old.NativeRevision {
					t.Fatal("unchanged reference payload republished", caughtUp)
				}
			} else {
				leaf := native.docs[caughtUp.Path+"@"+caughtUp.NativeRevision]
				if native.counter != count+1 || len(leaf.Ancestors) != 1 || leaf.Ancestors[0] != old.NativeRevision {
					t.Fatal("changed reference lost its retained parent", leaf)
				}
			}
			count = native.counter
			// The layout worker must follow normal content catch-up's exact
			// successor instead of rereading against the obsolete predecessor.
			if more, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || more || held != 0 || moved != 1 || s.ReferenceReadAttempts != 0 {
				t.Fatal("layout catch-up did not use the current reference binding", more, moved, held, err)
			}
			moved, err := s.Export(t.Context(), "c", fresh.File.ID, fresh.Base.ID)
			if err != nil || moved.Path != s.Scopes[0].Root+"/notebook.pdf" || moved.Generation != caughtUp.Generation || moved.FileID != caughtUp.FileID ||
				moved.SourceBase != caughtUp.SourceBase || moved.SHA256 != caughtUp.SHA256 || moved.SourceSequence != caughtUp.SourceSequence+1 || native.counter != count || layout.layouts != 1 {
				t.Fatal("caught-up reference did not move without republishing", moved, err)
			}
			if string(records.data[oldKey]) != oldRecord {
				t.Fatal("historical generation mapping rewritten")
			}
			intent, _ := operation(old, "old_edit", "pending", "2-old")
			if _, ok := s.intentScope(t.Context(), records, intent); ok {
				t.Fatal("outgoing catch-up granted old-generation intent authority")
			}
		})
	}
}

func TestInventoryExportAdoptsCurrentRevisionAndResumesControl(t *testing.T) {
	s, native, _, root, binding := fixture(t)
	read, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	count, controls := native.counter, len(native.bindings)
	adopted, unchanged, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || !unchanged || adopted != binding || native.counter != count || len(native.bindings) != controls {
		t.Fatal("current export replayed", adopted, unchanged, err)
	}
	// Simulate a legacy binding without the additive control receipt.
	records := s.Store.(*memoryRecords)
	delete(records.data, "export:published_"+key(binding.ID))
	native.ackFailure = true
	if _, _, err := s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID); err == nil {
		t.Fatal("acknowledged unavailable control")
	}
	native.ackFailure = false
	adopted, _, err = s.ExportInventory(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil || adopted != binding || native.counter != count || len(native.bindings) != controls+1 {
		t.Fatal("legacy export changed identity", adopted, err)
	}
	// A changed canonical source advances exactly from the existing parent.
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("B"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Source.Read(t.Context(), "source", "a.md")
	if err != nil {
		t.Fatal(err)
	}
	next, unchanged, err := s.ExportInventory(t.Context(), "c", changed.File.ID, changed.Base.ID)
	if err != nil || unchanged || next.FileID != binding.FileID || next.NativeRevision == binding.NativeRevision || next.SourceSequence != binding.SourceSequence+1 {
		t.Fatal("changed revision identity", next, unchanged, err)
	}
	leaf := native.docs[next.Path+"@"+next.NativeRevision]
	if len(leaf.Ancestors) != 1 || leaf.Ancestors[0] != binding.NativeRevision {
		t.Fatal("wrong publication parent", leaf)
	}
}
