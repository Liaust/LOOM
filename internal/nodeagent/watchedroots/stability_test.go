package watchedroots

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEffectiveHashStability(t *testing.T) {
	for _, test := range []struct {
		path, kind, window string
		size               int64
		want               time.Duration
	}{
		{"note.md", PathKindFile, "2s", 794, smallTextStability},
		{"note.MARKDOWN", PathKindFile, "2s", smallTextMaxBytes, smallTextStability},
		{"note.txt", PathKindFile, "100ms", 794, 100 * time.Millisecond},
		{"note.md", PathKindFile, "0s", 794, 0},
		{"large.md", PathKindFile, "2s", smallTextMaxBytes + 1, 2 * time.Second},
		{"book.pdf", PathKindFile, "2s", 794, 2 * time.Second},
		{"image.png", PathKindFile, "2s", 794, 2 * time.Second},
		{"unknown", PathKindFile, "2s", 794, 2 * time.Second},
		{"link.md", PathKindSymlink, "2s", 794, 2 * time.Second},
	} {
		if got := effectiveHashStability(ScanConfig{StabilityWindow: test.window}, test.path, test.size, test.kind); got != test.want {
			t.Errorf("%s %s %d: %v != %v", test.path, test.window, test.size, got, test.want)
		}
	}
}

func TestSmallTextNativeDeadlineAndHashAgree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("small note"), 0600); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Scan: ScanConfig{StabilityWindow: "2s"}})
	store := NewStore(t.TempDir())
	now := time.Now().UTC()
	full := now.Add(-time.Minute)
	if err := store.SaveCheckpoint("notes", RootCheckpoint{ConfigHash: root.ConfigHash, LastFullRescanAt: &full, OutputsCurrent: true}); err != nil {
		t.Fatal(err)
	}
	observation := ObservePath(root, "note.md")
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "note.md", Source: "native", LastSeenAt: now, Observation: &observation}); err != nil {
		t.Fatal(err)
	}
	due, err := NextNativeReconcile(store, root, now, now.Add(-time.Second), true, time.Minute)
	if err != nil || !due.Equal(now.Add(smallTextStability)) {
		t.Fatalf("small-text deadline: %v %v", due, err)
	}
	result, err := Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeAuto})
	if err != nil || result.Counts.HashDeferred != 1 {
		t.Fatalf("premature hash: %+v %v", result.Counts, err)
	}
	state, err := store.LoadPathState("notes", "note.md")
	if err != nil || state.HashObservedAt == nil || result.Checkpoint.NextHashReadyAt == nil || !result.Checkpoint.NextHashReadyAt.Equal(state.HashObservedAt.Add(smallTextStability)) {
		t.Fatalf("deferred deadline differs: %+v %v", result.Checkpoint, err)
	}
	// A matching observation older than 250ms, but younger than the old 2s,
	// may proceed. A subsequent write is covered by the fingerprint regression.
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "note.md", Source: "native", LastSeenAt: now.Add(-time.Second), Observation: &observation}); err != nil {
		t.Fatal(err)
	}
	result, err = Reconcile(t.Context(), store, ScanRequest{Root: root, Mode: ScanModeAuto})
	if err != nil || result.Counts.HashComputed != 1 {
		t.Fatalf("small-text hash delayed: %+v %v", result.Counts, err)
	}
}
