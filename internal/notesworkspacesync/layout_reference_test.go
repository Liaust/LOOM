package notesworkspacesync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type referenceLayoutNative struct {
	*layoutNative
	hold   string
	probes int
}

func (n *referenceLayoutNative) Call(ctx context.Context, req map[string]any, out any) error {
	if req["method"] != "layout.preflight" {
		return n.layoutNative.Call(ctx, req, out)
	}
	n.probes++
	probe := layoutProbe{Status: "ready"}
	if n.hold != "" {
		probe = layoutProbe{Status: "held", Reason: n.hold}
	}
	raw, _ := json.Marshal(probe)
	return json.Unmarshal(raw, out)
}

func referenceLayoutFixture(t *testing.T) (*Service, *countedReferenceSource, *referenceLayoutNative, string, string, *time.Time) {
	t.Helper()
	s, native, store, root, _ := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("PDF bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.Read(t.Context(), "source", "paper.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); err != nil {
		t.Fatal(err)
	}
	prior := s.Scopes[0]
	reader := &countedReferenceSource{NotesSource: NotesSource{Service: notesworkspace.Service{Store: store, Sources: generationResolver{root, "expanded"}}}}
	probe := &referenceLayoutNative{layoutNative: &layoutNative{fakeNative: native}}
	s.Source, s.Native, s.LayoutPublication, s.ReferenceSpool = reader, probe, true, t.TempDir()
	s.Scopes[0].Generation, s.Scopes[0].Root = "expanded", "Projects/Example/Research"
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root, LayoutGeneration: prior.Generation}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	return s, reader, probe, root, read.File.ID, &now
}

func TestReferenceLayoutStableRefusalSurvivesExpiryRestartAndSourceRead(t *testing.T) {
	s, reader, native, root, fileID, now := referenceLayoutFixture(t)
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("changed PDF bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || reader.snapshots != 1 || s.LayoutHoldReasons["reference_content_changed"] != 1 {
		t.Fatal("initial content mismatch", err, reader.snapshots, s.LayoutHoldReasons)
	}
	initial := retainedLayoutCursor(t, s)
	for i := 0; i < 3; i++ {
		*now = now.Add(layoutEvidenceRetry)
		// A genuinely cold reverse index, not a copied Service sharing its cache.
		s.layoutDependencies = nil
		s.Native = &referenceLayoutNative{layoutNative: native.layoutNative}
		if more, moved, _, err := s.advanceLayouts(t.Context(), 32); err != nil || more || moved != 0 || reader.snapshots != 1 {
			t.Fatal("unchanged repair restaged content", more, moved, err, reader.snapshots)
		}
	}
	after := retainedLayoutCursor(t, s)
	if after.After != initial.After || !after.Complete {
		t.Fatal("restart reset discovery", initial, after)
	}
	if err := os.WriteFile(filepath.Join(root, "paper.pdf"), []byte("PDF bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().invalidate("source:"+fileID, false, false)
	if _, moved, _, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 1 || reader.snapshots != 2 {
		t.Fatal("actual source change failed to release exact reference", moved, err, reader.snapshots)
	}
	if s.ReferenceReadBytes != int64(len("changed PDF bytes")+len("PDF bytes")) {
		t.Fatal("source read accounting lost refused bytes", s.ReferenceReadBytes)
	}
}

func TestReferenceLayoutNativeRefusalPrecedesSnapshotAndRecovers(t *testing.T) {
	s, reader, native, _, _, now := referenceLayoutFixture(t)
	native.hold = "layout_previous_changed"
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || reader.snapshots != 0 || s.LayoutHoldReasons["reference_layout_previous_changed"] != 1 {
		t.Fatal("native refusal copied payload", err, reader.snapshots, s.LayoutHoldReasons)
	}
	*now = now.Add(layoutEvidenceRetry)
	s.layoutDependencies = nil
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || reader.snapshots != 0 {
		t.Fatal("repair copied refused payload", err, reader.snapshots)
	}
	native.hold = ""
	*now = now.Add(layoutEvidenceRetry)
	if _, moved, _, err := s.advanceLayouts(t.Context(), 32); err != nil || reader.snapshots != 1 || moved != 1 {
		t.Fatal("metadata repair lost changed native evidence", moved, err, reader.snapshots)
	}
}

func TestLayoutReferenceBudgetAdmission(t *testing.T) {
	now := time.Now()
	s := &Service{Now: func() time.Time { return now }, layoutBudget: &referenceBudget{started: now}}
	if err := s.admitLayoutReference(deliverySoftBytes * 3); err != nil {
		t.Fatal("oversized attachment cannot make progress", err)
	}
	s.layoutBudget.attempts, s.layoutBudget.bytes = 1, deliverySoftBytes*3
	if err := s.admitLayoutReference(1); !errors.Is(err, errLayoutBudget) {
		t.Fatal("oversized turn failed to yield", err)
	}
	s.layoutBudget.bytes = 8
	if err := s.admitLayoutReference(deliverySoftBytes); !errors.Is(err, errLayoutBudget) {
		t.Fatal("next oversized attachment was not dedicated", err)
	}
	now = now.Add(deliveryAdmissionWindow)
	if err := s.admitLayoutReference(1); !errors.Is(err, errLayoutBudget) || s.LayoutPressure != "admission_window" {
		t.Fatal("admission window did not yield", err, s.LayoutPressure)
	}
}
