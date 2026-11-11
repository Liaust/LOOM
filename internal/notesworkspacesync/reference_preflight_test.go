package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type countedReferenceSource struct {
	NotesSource
	snapshots int
}

func (s *countedReferenceSource) ReadReference(ctx context.Context, c, p, spool string) (notesworkspace.ReadResult, string, error) {
	s.snapshots++
	return s.NotesSource.ReadReference(ctx, c, p, spool)
}

type preflightNative struct {
	Native
	hold                 string
	probes, publications int
}

func (n *preflightNative) Call(ctx context.Context, req map[string]any, out any) error {
	var value any
	switch req["method"] {
	case "publication.preflight":
		n.probes++
		if req["operationId"] != nil && n.hold != "" {
			value = map[string]string{"status": "held", "reason": n.hold}
		} else {
			value = map[string]string{"status": "ready"}
		}
	case "publish.reference.file":
		n.publications++
		if n.hold != "" {
			value = Evidence{Status: "held", Reason: n.hold}
		} else {
			value = Evidence{Status: "published", Revision: "1-source", SHA256: req["sha256"].(string)}
		}
	default:
		return n.Native.Call(ctx, req, out)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestReferenceDeliveryKnownOutcomeDoesNotRestageBytes(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "book.pdf"), []byte("reference bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &countedReferenceSource{NotesSource: s.Source.(NotesSource)}
	s.Source = reader
	s.ReferenceSpool = filepath.Join(t.TempDir(), "spool")
	probe := &preflightNative{Native: native, hold: "publication_outcome_unknown"}
	s.Native = probe
	scope := s.Scopes[0]
	input := referenceAttemptInput{Fingerprint: "source-stat-fingerprint"}
	read, snapshot, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, snapshot, input)
	os.Remove(snapshot)
	if !errors.Is(err, ErrHeld) || reader.snapshots != 1 || deliveryFailureReason(err, "") != "reference_publication_outcome_unknown" {
		t.Fatal("first attempt lost exact outcome", err, reader.snapshots)
	}
	for n := 0; n < 4; n++ {
		_, snapshot, err = s.readExportReference(t.Context(), scope, "book.pdf", input)
		if !errors.Is(err, ErrHeld) || snapshot != "" || reader.snapshots != 1 || !metadataOnlyReferenceFailure(err) {
			t.Fatal("unchanged failed publication restaged PDF", err, reader.snapshots)
		}
	}
	if s.ReferenceMetadataHeld != 4 || s.ReferenceStagedBytes != int64(len("reference bytes")) {
		t.Fatal("failed reference I/O disappeared from diagnostics", s.ReferenceMetadataHeld, s.ReferenceStagedBytes)
	}
	// A newly completed exact receipt makes this same attempt eligible again.
	probe.hold = ""
	read, snapshot, err = s.readExportReference(t.Context(), scope, "book.pdf", input)
	if err != nil || reader.snapshots != 2 {
		t.Fatal("changed publication outcome did not release hold", err, reader.snapshots)
	}
	if snapshot != "" {
		defer os.Remove(snapshot)
	}
	if _, err := s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, snapshot, input); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceMappingRefusalPrecedesSnapshot(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "image.png"), []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.Read(t.Context(), "source", "image.png")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader := &countedReferenceSource{NotesSource: s.Source.(NotesSource)}
	s.Source = reader
	s.Native = &preflightNative{Native: native}
	if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
		id := mapKey(Intent{Workspace: binding.Workspace, Collection: binding.Collection, Generation: binding.Generation, FileID: binding.FileID})
		mapping, err := load[FileMapping](t.Context(), r, "file", id)
		if err != nil {
			return err
		}
		mapping.Latest.Path = "Notes/different.png"
		return r.Put(t.Context(), "file", id, mapping)
	}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 3; n++ {
		_, snapshot, err := s.readExportReference(t.Context(), s.Scopes[0], "image.png")
		if !errors.Is(err, ErrHeld) || snapshot != "" || reader.snapshots != 0 {
			t.Fatal("known inconsistent mapping read source bytes", err, reader.snapshots)
		}
	}
}

func TestMetadataOnlyHoldsDoNotConsumeImaginaryGigabyteBudget(t *testing.T) {
	i, scope, now := inventoryFixture(t)
	for _, name := range []string{"one.pdf", "two.pdf", "three.pdf"} {
		if err := i.Store.WithInventory(t.Context(), scope, func(r InventoryRecords) error {
			return r.Put(t.Context(), InventoryEntry{Path: name, Kind: "asset", Present: true, Fingerprint: name, Size: deliverySoftBytes + 1, Sequence: 1, WaitingSince: now})
		}); err != nil {
			t.Fatal(err)
		}
	}
	turn, err := deliverInventoryTurn(t.Context(), i, scope, DeliveryCursor{}, func(InventoryEntry) (deliveryPublication, error) {
		return deliveryPublication{Reason: "reference_path_has_history", MetadataOnly: true}, ErrHeld
	}, func() time.Time { return now })
	if err != nil || turn.Held != 3 || turn.Bytes != 0 || turn.Pressure != "" {
		t.Fatal("metadata refusals consumed byte-transfer turns", turn, err)
	}
}

func TestReferenceExportEvidenceHoldUsesExactDependencies(t *testing.T) {
	s, native, _, root, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "book.pdf"), []byte("reference bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	reader := &countedReferenceSource{NotesSource: s.Source.(NotesSource)}
	s.Source = reader
	s.ReferenceSpool = filepath.Join(t.TempDir(), "spool")
	s.Native = &preflightNative{Native: native}
	scope := s.Scopes[0]
	input := referenceAttemptInput{Fingerprint: "source-stat-fingerprint"}
	read, snapshot, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(snapshot)
	id := "export_" + key(scope.Workspace, scope.Collection, scope.Generation, read.File.ID, read.Base.ID)
	bad := exportRecord{Scope: scope, FileID: read.File.ID, BaseID: read.Base.ID, Path: scope.Root + "/book.pdf", ClientFileID: "file_" + key(scope.Workspace, scope.Collection, read.File.ID)}
	bad.Scope.Root = "Notes/previous"
	if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error { return r.Put(t.Context(), "export", id, bad) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, snapshot, input); deliveryFailureReason(err, "") != "reference_export_evidence_held" {
		t.Fatal("late export guard did not retain its metadata dependencies", err)
	}
	for n := 0; n < 3; n++ {
		_, next, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
		if !errors.Is(err, ErrHeld) || next != "" || reader.snapshots != 1 {
			t.Fatal("unchanged Go refusal restaged PDF", err, reader.snapshots)
		}
	}
	cache := s.dependencyCache()
	if cache.invalidate("record:export:unrelated", false, false) {
		t.Fatal("unrelated receipt woke the reference hold")
	}
	// Reconnection discards only the accelerator. Persisted evidence still
	// refuses unchanged work before the bytes are staged.
	s.layoutDependencies = nil
	if _, next, err := s.readExportReference(t.Context(), scope, "book.pdf", input); !errors.Is(err, ErrHeld) || next != "" || reader.snapshots != 1 {
		t.Fatal("restart lost refusal evidence", err, reader.snapshots)
	}
	good := bad
	good.Scope = scope
	if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error { return r.Put(t.Context(), "export", id, good) }); err != nil {
		t.Fatal(err)
	}
	if !s.dependencyCache().invalidate("record:export:"+id, false, false) {
		t.Fatal("exact missing/corrected receipt did not invalidate the refusal")
	}
	read, next, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
	if err != nil || reader.snapshots != 2 {
		t.Fatal("changed exact dependency did not release reference", err, reader.snapshots)
	}
	defer os.Remove(next)
	if _, err := s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, next, input); err != nil {
		t.Fatal("ordinary guarded export did not resume", err)
	}
}

type completedReferenceReceiptNative struct {
	Native
	expected                     map[string]any
	published                    Evidence
	completed, compacted         bool
	probes, replays, parentReads int
}

func (n *completedReferenceReceiptNative) Call(ctx context.Context, request map[string]any, out any) error {
	var result any
	switch request["method"] {
	case "publication.preflight", "publish.reference.file":
		for key, expected := range n.expected {
			if !reflect.DeepEqual(request[key], expected) {
				return errors.New("reference receipt request mismatch: " + key)
			}
		}
		if request["method"] == "publication.preflight" {
			n.probes++
			result = map[string]string{"status": "ready"}
			if n.completed {
				result = map[string]string{"status": "ready", "outcome": "published"}
			}
		} else {
			n.replays++
			if !n.completed {
				return errors.New("publication without a completed receipt")
			}
			result = n.published
		}
	case "read.path":
		n.parentReads++
		result = Evidence{Status: "held", Reason: "revision_unavailable"}
		if !n.compacted {
			path := n.expected["path"].(string)
			result = Evidence{ID: path, Path: path, Revision: n.expected["baseRevision"].(string), Status: "held", Reason: "reference_binary"}
		}
	case "leaves":
		result = map[string]any{"leaves": []Evidence{{Revision: n.published.Revision}, {Revision: "2-other"}}}
	default:
		return n.Native.Call(ctx, request, out)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestReferenceCompletedCrossGenerationReceiptReplaysPastOldParent(t *testing.T) {
	for _, parent := range []string{"compacted", "diverged"} {
		for _, receipt := range []string{"completed", "absent"} {
			t.Run(parent+"/"+receipt, func(t *testing.T) {
				s, native, store, root, _ := fixture(t)
				if err := os.WriteFile(filepath.Join(root, "book.pdf"), []byte("unchanged reference"), 0600); err != nil {
					t.Fatal(err)
				}
				original, err := s.Source.Read(t.Context(), "source", "book.pdf")
				if err != nil {
					t.Fatal(err)
				}
				old, err := s.Export(t.Context(), "c", original.File.ID, original.Base.ID)
				if err != nil {
					t.Fatal(err)
				}
				s.AdoptExisting = true
				s.Scopes[0].Generation, s.Scopes[0].PreviousGeneration = "expanded", old.Generation
				s.Source = NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "expanded"}, Store: store}}
				s.ReferenceSpool = filepath.Join(t.TempDir(), "spool")
				read, snapshot, err := readExportSource(t.Context(), s.Source, "source", "book.pdf", s.ReferenceSpool)
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(snapshot)
				scope := s.Scopes[0]
				id := "export_" + key(scope.Workspace, scope.Collection, scope.Generation, read.File.ID, read.Base.ID)
				pending := exportRecord{Scope: scope, FileID: read.File.ID, BaseID: read.Base.ID, Path: old.Path, ClientFileID: old.FileID, Parent: old.NativeRevision}
				if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error { return r.Put(t.Context(), "export", id, pending) }); err != nil {
					t.Fatal(err)
				}
				probe := &completedReferenceReceiptNative{Native: native, completed: receipt == "completed", compacted: parent == "compacted",
					expected: map[string]any{"operationId": id, "path": old.Path, "baseRevision": old.NativeRevision,
						"sha256": strings.TrimPrefix(read.Base.Hash, "sha256:"), "size": read.Base.ReferenceSize},
					published: Evidence{Status: "published", Revision: "2-completed", SHA256: strings.TrimPrefix(read.Base.Hash, "sha256:")}}
				s.Native = probe
				binding, err := s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, snapshot)
				if probe.probes != 1 {
					t.Fatal("exact receipt was not probed", probe.probes, err)
				}
				if receipt == "absent" {
					want := "reference_base_diverged"
					if parent == "compacted" {
						want = "reference_base_unavailable"
					}
					if !errors.Is(err, ErrHeld) || deliveryFailureReason(err, "") != want || probe.replays != 0 || probe.parentReads != 1 {
						t.Fatal("plain readiness bypassed old-parent authority", binding, err, probe.replays, probe.parentReads)
					}
					return
				}
				if err != nil || binding.Generation != scope.Generation || binding.FileID != old.FileID || binding.Writable ||
					binding.NativeRevision != probe.published.Revision || probe.replays != 1 || probe.parentReads != 0 {
					t.Fatal("completed receipt was fenced out or used unchanged shortcut", binding, err, probe.replays, probe.parentReads)
				}
			})
		}
	}
}

func TestReferenceCrossGenerationRefusalRetainsExactPendingParent(t *testing.T) {
	s, native, store, root, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "book.pdf"), []byte("reference bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := s.Source.Read(t.Context(), "source", "book.pdf")
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Export(t.Context(), "c", original.File.ID, original.Base.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.AdoptExisting = true
	s.Scopes[0].Generation, s.Scopes[0].PreviousGeneration = "expanded", old.Generation
	reader := &countedReferenceSource{NotesSource: NotesSource{Service: notesworkspace.Service{Sources: generationResolver{root, "expanded"}, Store: store}}}
	s.Source = reader
	s.ReferenceSpool = filepath.Join(t.TempDir(), "spool")
	// The current mapped parent is healthy; the immutable pending request is
	// not. Its exact request, not the proposed latest parent, must gate retries.
	s.Native = &preflightNative{Native: native, hold: "base_diverged"}
	input := referenceAttemptInput{Fingerprint: "unchanged-source"}
	scope := s.Scopes[0]
	read, snapshot, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(snapshot)
	_, err = s.export(t.Context(), scope.Collection, read.File.ID, read.Base.ID, snapshot, input)
	if deliveryFailureReason(err, "") != "reference_base_diverged" || reader.snapshots != 1 {
		t.Fatal("first exact pending-parent refusal", err, reader.snapshots)
	}
	for n := 0; n < 3; n++ {
		_, next, err := s.readExportReference(t.Context(), scope, "book.pdf", input)
		if deliveryFailureReason(err, "") != "reference_base_diverged" || next != "" || reader.snapshots != 1 {
			t.Fatal("retry checked proposed parent instead of saved request", err, reader.snapshots)
		}
	}
}
