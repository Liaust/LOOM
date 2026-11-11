package notesworkspacesync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type moveReferenceNative struct{ *fakeNative }

func (n moveReferenceNative) Call(ctx context.Context, request map[string]any, out any) error {
	if request["method"] == "publish.reference.file" {
		return (referenceFileNative{n.fakeNative}).Call(ctx, request, out)
	}
	return (&layoutNative{fakeNative: n.fakeNative}).Call(ctx, request, out)
}

func TestSourceMoveReferencesRequireAbsenceExactBytesAndWritableScopes(t *testing.T) {
	for _, scenario := range []string{"move", "copy", "changed", "readonly"} {
		t.Run(scenario, func(t *testing.T) {
			s, n, _, root, _ := fixture(t)
			s.LayoutPublication = true
			s.ReferenceSpool = filepath.Join(root, ".spool")
			s.Native = moveReferenceNative{n}
			if err := os.WriteFile(filepath.Join(root, "a.pdf"), []byte("binary\x00PDF"), 0600); err != nil {
				t.Fatal(err)
			}
			read, snapshot, err := readExportSource(t.Context(), s.Source, "source", "a.pdf", s.ReferenceSpool)
			if err != nil {
				t.Fatal(err)
			}
			old, err := s.export(t.Context(), "c", read.File.ID, read.Base.ID, snapshot)
			os.Remove(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.observeMoveExport(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "b.pdf"), []byte("binary\x00PDF"), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario != "copy" {
				if err := os.Remove(filepath.Join(root, "a.pdf")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "changed" {
				os.WriteFile(filepath.Join(root, "b.pdf"), []byte("different"), 0600)
			}
			read, snapshot, err = readExportSource(t.Context(), s.Source, "source", "b.pdf", s.ReferenceSpool)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "readonly" {
				s.Scopes[0].ReadOnly = true
			}
			next, err := s.export(t.Context(), "c", read.File.ID, read.Base.ID, snapshot)
			os.Remove(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "move" {
				records := s.Store.(*memoryRecords)
				a, _ := load[BindingRecord](t.Context(), records, "binding", old.ID)
				b, _ := load[BindingRecord](t.Context(), records, "binding", next.ID)
				if _, e := s.sourceMoveView(t.Context(), a, b); e != nil {
					t.Fatal("reference review", a, b, e)
				}
			}
			more, published, held, err := s.advanceSourceMoves(t.Context(), 32)
			want := 0
			if scenario == "move" {
				want = 1
			}
			if err != nil || more || published != want || held != 0 || old.Writable || next.Writable {
				t.Fatal(more, published, held, err)
			}
			entries, err := os.ReadDir(s.ReferenceSpool)
			if err != nil || len(entries) != 0 {
				t.Fatal("move left payload snapshots", entries, err)
			}
		})
	}
}

func TestMoveLookupDoesNotFallBackFromUnresolvedCurrentPointer(t *testing.T) {
	s, _, _, _, b := fixture(t)
	records := s.Store.(*memoryRecords)
	scope := s.Scopes[0]
	record, err := load[BindingRecord](t.Context(), records, "binding", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := records.Put(t.Context(), "source_file", sourceKey(scope, record.SourceFileID), "missing_successor"); err != nil {
		t.Fatal(err)
	}
	if _, err := liveMoveMapping(t.Context(), records, scope, record.SourceFileID); err == nil {
		t.Fatal("unresolved authoritative pointer fell back to retired locator")
	}
}

func TestSourceMoveAutomaticallyLinksOnlyVerifiedAbsentOriginal(t *testing.T) {
	for _, scenario := range []string{"move", "copy", "changed_destination"} {
		t.Run(scenario, func(t *testing.T) {
			s, n, _, root, old := fixture(t)
			s.LayoutPublication = true
			s.Native = &layoutNative{fakeNative: n}
			if err := s.observeMoveExport(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			if scenario == "copy" {
				if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("A"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "b.md")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "changed_destination" {
				if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			read, err := s.Source.Read(t.Context(), "source", "b.md")
			if err != nil {
				t.Fatal(err)
			}
			next, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
			if err != nil {
				t.Fatal(err)
			}
			more, published, held, err := s.advanceSourceMoves(t.Context(), 32)
			want := 0
			if scenario == "move" {
				want = 1
			}
			if err != nil || more || published != want || held != 0 {
				t.Fatal(more, published, held, err)
			}
			if scenario == "move" {
				records := s.Store.(*memoryRecords)
				move, err := load[Layout](t.Context(), records, "export", sourceMoveRetirement(old))
				if err != nil || !move.SourceMove || move.Previous != old || move.Binding != next {
					t.Fatal(move, err)
				}
				if _, published, _, err := s.advanceSourceMoves(t.Context(), 32); err != nil || published != 0 {
					t.Fatal("move replay", published, err)
				}
				// A late offline request must not recreate the absent original.
				i, p := operation(old, "late_after_source_move", "late edit", "2-late")
				n.content(old.Path, "2-late", old.NativeRevision, "late edit")
				n.add(i)
				n.add(p)
				if _, err := s.Step(t.Context(), 128); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(root, "a.md")); !os.IsNotExist(err) {
					t.Fatal("late edit recreated old source", err)
				}
				if len(n.acks) == 0 || n.acks[len(n.acks)-1].Reason != "source_relocated" {
					t.Fatal(n.acks)
				}
				create, publication := operation(old, "new_at_retired_path", "new note", "1-newnote")
				create.Operation, create.Base, create.FileID = "create", nil, "new_client_identity"
				raw, _ := json.Marshal(create)
				publication.IntentDigest = digest(raw)
				n.content(create.Path, "1-newnote", "", "new note")
				evidence := n.docs[create.Path+"@1-newnote"]
				evidence.Ancestors = nil
				n.docs[create.Path+"@1-newnote"] = evidence
				n.add(create)
				n.add(publication)
				if _, err := s.Step(t.Context(), 128); err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(filepath.Join(root, "a.md"))
				if err != nil || string(content) != "new note" {
					join, _ := load[Join](t.Context(), records, "operation", create.ID)
					t.Fatalf("new identity at retired path: %s %v %+v", content, err, join)
				}
				if err := os.WriteFile(filepath.Join(root, "a.md"), []byte("A"), 0600); err != nil {
					t.Fatal(err)
				}
				current, err := s.Source.Read(t.Context(), "source", "a.md")
				if err != nil {
					t.Fatal(err)
				}
				exported, err := s.Export(t.Context(), "c", current.File.ID, current.Base.ID)
				if err != nil || exported.FileID != create.FileID || exported.ID == old.ID {
					t.Fatalf("old content resurrected retired identity: %+v %v", exported, err)
				}
				retained, err := retainedMapping(t.Context(), records, s.Scopes[0], current.File.ID)
				if err != nil || retained.ClientFileID != create.FileID {
					t.Fatal("successor locator", retained, err)
				}
				late, latePublication := operation(old, "old_after_recreation", "late again", "2-lateagain")
				n.content(late.Path, "2-lateagain", old.NativeRevision, "late again")
				n.add(late)
				n.add(latePublication)
				if _, err := s.Step(t.Context(), 128); err != nil {
					t.Fatal(err)
				}
				content, _ = os.ReadFile(filepath.Join(root, "a.md"))
				if string(content) != "A" || n.acks[len(n.acks)-1].Reason != "source_relocated" {
					t.Fatal("late retired request replaced successor", string(content), n.acks)
				}
			}
		})
	}
}

func TestSourceMoveReviewBindsChangedDestinationAndRejectsLaterChange(t *testing.T) {
	s, _, _, root, old := fixture(t)
	if err := os.Rename(filepath.Join(root, "a.md"), filepath.Join(root, "b.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("corrected links"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.Read(t.Context(), "source", "b.md")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	records := s.Store.(*memoryRecords)
	a, err := load[BindingRecord](t.Context(), records, "binding", old.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := load[BindingRecord](t.Context(), records, "binding", next.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.sourceMoveView(t.Context(), a, b)
	if err != nil || !view.ContentChanged || view.Review == "" {
		t.Fatal(view, err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("another edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.sourceMoveView(t.Context(), a, b); err == nil {
		t.Fatal("stale destination authorized")
	}
}
