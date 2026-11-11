package watchedroots

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAutomaticInventoryBudgetContinuesInsteadOfRestarting(t *testing.T) {
	rootDir := t.TempDir()
	for _, name := range []string{"a/file.md", "a.txt", "z.md"} {
		full := filepath.Join(rootDir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root := testScanRoot(t, rootDir, RootConfig{RootKey: "notes", SafeRootKey: "slice09",
		Include: []string{"**/*"}, Scan: ScanConfig{MaxFilesPerRun: 1, StabilityWindow: "0s"}})
	store := NewStore(t.TempDir())
	var previous string
	complete := false
	for i := 0; i < 8; i++ {
		result, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeAuto, Root: root})
		if err != nil {
			t.Fatal(err)
		}
		cp := result.Checkpoint
		if cp.FullScanID == "" {
			complete = cp.LastFullRescanAt != nil
			break
		}
		if cp.LastFullRescanAt != nil || cp.FullScanAfter == previous {
			t.Fatal("partial sweep invented completion or repeated its prefix", cp)
		}
		previous = cp.FullScanAfter
	}
	if !complete {
		t.Fatal("bounded automatic inventory never completed")
	}
	for _, name := range []string{"a/file.md", "a.txt", "z.md"} {
		state, err := store.LoadPathState("notes", name)
		if err != nil || state.Status == PathStatusDeleted {
			t.Fatal("resumed sweep skipped or deleted an existing source", name, err)
		}
	}
	// A later bounded sweep also records visits for unchanged/reused hashes.
	cp, err := store.LoadCheckpoint("notes")
	if err != nil {
		t.Fatal(err)
	}
	cp.LastFullRescanAt = nil
	if err := store.SaveCheckpoint("notes", cp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		result, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeAuto, Root: root})
		if err != nil {
			t.Fatal(err)
		}
		if result.Counts.Deleted != 0 || result.Counts.MissingDeferred != 0 {
			t.Fatal("reused observation was lost across scan pages", result.Counts)
		}
		if result.Checkpoint.FullScanID == "" {
			break
		}
	}
}
