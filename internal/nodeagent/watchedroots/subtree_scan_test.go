package watchedroots

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryHintReconcilesOnlyAffectedPrefix(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"incoming/a.md", "unrelated/b.md"} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*"}, Scan: ScanConfig{StabilityWindow: "0s"}})
	store := NewStore(t.TempDir())
	first, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeFull, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "incoming"), filepath.Join(dir, "renamed")); err != nil {
		t.Fatal(err)
	}
	if err := store.AddDirtyHints("notes", []DirtyHint{{RelativePath: "incoming", HintKind: DirtyHintSubtree}, {RelativePath: "renamed", HintKind: DirtyHintSubtree}}); err != nil {
		t.Fatal(err)
	}
	result, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeAuto, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != ScanModeDirty || result.Counts.Visited != 2 || !result.Checkpoint.LastFullRescanAt.Equal(*first.Checkpoint.LastFullRescanAt) {
		t.Fatal("directory event scanned unrelated root or invented full coverage", result)
	}
	for name, want := range map[string]string{"incoming/a.md": PathStatusDeleted, "renamed/a.md": PathStatusIncluded, "unrelated/b.md": PathStatusIncluded} {
		state, err := store.LoadPathState("notes", name)
		if err != nil || state.Status != want {
			t.Fatal(name, state.Status, err)
		}
	}
	hints, err := store.ListDirtyHints("notes")
	if err != nil || len(hints) != 0 {
		t.Fatal("finished prefix remained runnable", hints, err)
	}
}

func TestSubtreeBudgetRetainsCursorWithoutRootRescan(t *testing.T) {
	dir := t.TempDir()
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*"}, Scan: ScanConfig{MaxFilesPerRun: 1, StabilityWindow: "0s"}})
	store := NewStore(t.TempDir())
	if _, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeFull, Root: root}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new/a.md", "new/b.md", "new/c.md"} {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AddDirtyHint("notes", DirtyHint{RelativePath: "new", HintKind: DirtyHintSubtree}); err != nil {
		t.Fatal(err)
	}
	var after string
	complete := false
	for i := 0; i < 5; i++ {
		result, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeAuto, Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if result.Mode != ScanModeDirty || result.Checkpoint.PendingRescan {
			t.Fatal("scoped budget became a full-root repair", result)
		}
		if scan, ok := result.Checkpoint.SubtreeScans["new"]; ok {
			if scan.After == after {
				t.Fatal("subtree restarted same prefix", scan)
			}
			after = scan.After
		} else {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("bounded subtree never finished")
	}
	for _, name := range []string{"new/a.md", "new/b.md", "new/c.md"} {
		state, err := store.LoadPathState("notes", name)
		if err != nil || state.Status != PathStatusIncluded {
			t.Fatal("subtree lost an existing member", name, err)
		}
	}
}
