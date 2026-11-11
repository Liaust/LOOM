package watchedroots

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fsnotify/fsnotify"
)

func TestSelectedScanPrunesUnrelatedSubtreesAndPreservesHistory(t *testing.T) {
	dir := t.TempDir()
	write := func(relative string) {
		t.Helper()
		p := filepath.Join(dir, relative)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(relative), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("paper/current.pdf")
	write("toolchain/retained.md")
	store := NewStore(t.TempDir())
	config := RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*"}, Scan: ScanConfig{StabilityWindow: "0s"}}
	scan := func() ScanResult {
		t.Helper()
		r, err := Reconcile(t.Context(), store, ScanRequest{Mode: ScanModeFull, Root: testScanRoot(t, dir, config)})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	scan()
	for i := 0; i < 30; i++ {
		write(fmt.Sprintf("toolchain/dependencies/%02d.bin", i))
	}
	config.Include = []string{"paper/current.pdf"}
	config.Scan.MaxFilesPerRun = 4
	r := scan()
	if r.Counts.BudgetExhausted != 0 || r.Counts.Included != 1 || r.Counts.Deleted != 0 || r.Counts.Files != 1 {
		t.Fatalf("selected scan touched unrelated files or inferred deletion: %+v", r.Counts)
	}
	old, err := store.LoadPathState("notes", "toolchain/retained.md")
	if err != nil || old.Status != PathStatusExcluded || old.DeletedAt != nil || old.Classification.ReasonCode != ReasonExcludedNoInclude {
		t.Fatalf("retained source was deleted rather than excluded: %+v, %v", old, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "toolchain/retained.md")); err != nil {
		t.Fatal("source bytes were not preserved", err)
	}
	config.Include = append(config.Include, "toolchain/retained.md")
	r = scan()
	if r.Counts.BudgetExhausted != 0 || r.Counts.Included != 2 || r.Counts.Deleted != 0 {
		t.Fatalf("widened selection did not converge: %+v", r.Counts)
	}
	if err := os.Remove(filepath.Join(dir, "paper/current.pdf")); err != nil {
		t.Fatal(err)
	}
	r = scan()
	if r.Counts.Deleted != 1 {
		t.Fatalf("real selected-source deletion was lost: %+v", r.Counts)
	}
}

func TestNativeDirectoriesUseSelectedPrefixes(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"paper", "runs/selected", "runs/irrelevant/one/two/three"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"paper/current.pdf", "runs/selected/COVERAGE.md"}, Scan: ScanConfig{MaxDirsPerRun: 4}})
	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := addNativeDirectories(t.Context(), w, root, root.RootPath); err != nil {
		t.Fatal(err)
	}
	if len(w.WatchList()) != 4 {
		t.Fatalf("unexpected native registrations: %v", w.WatchList())
	}
}
