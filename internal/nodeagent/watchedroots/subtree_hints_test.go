package watchedroots

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestMergeDirtyHintsSubtreeAncestry(t *testing.T) {
	first := time.Now().UTC().Add(-time.Minute)
	observation := &PathObservation{RelativePath: "notes", Kind: PathKindDirectory, Exists: true, Safe: true}
	children := []DirtyHint{
		{RelativePath: "notes/sub/a.md", HintKind: DirtyHintModified, FirstSeenAt: first, LastSeenAt: first},
		{RelativePath: "notes/b.md", HintKind: DirtyHintDeleted},
		{RelativePath: "notes-other/a.md", HintKind: DirtyHintModified},
	}
	for _, ancestorFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(ancestorFirst), func(t *testing.T) {
			ancestor := DirtyHint{RelativePath: "notes/", HintKind: DirtyHintSubtree, Observation: observation}
			var hints map[string]DirtyHint
			if ancestorFirst {
				hints = MergeDirtyHints(nil, []DirtyHint{ancestor})
				hints = MergeDirtyHints(hints, children)
			} else {
				hints = MergeDirtyHints(nil, children)
				hints = MergeDirtyHints(hints, []DirtyHint{ancestor})
			}
			parent, ok := hints["notes"]
			if !ok || len(hints) != 2 || parent.HintKind != DirtyHintSubtree || parent.Count != 3 || parent.Observation != observation || !parent.FirstSeenAt.Equal(first) {
				t.Fatalf("ancestry was not coalesced: %#v", hints)
			}
			if _, ok := hints["notes-other/a.md"]; !ok {
				t.Fatal("a similarly prefixed sibling was absorbed")
			}
			before := hints["notes"]
			updated := MergeDirtyHints(hints, []DirtyHint{{RelativePath: "notes/sub", HintKind: DirtyHintSubtree}})
			if !reflect.DeepEqual(hints["notes"], before) || updated["notes"].Generation == before.Generation || updated["notes"].Count != 4 {
				t.Fatal("merge mutated input or lost the descendant arrival", hints, updated)
			}
		})
	}
}

func TestMergeDirtyHintsKeepsPublicKindsAndFullRescan(t *testing.T) {
	old := time.Now().UTC().Add(-time.Minute)
	observation := &PathObservation{RelativePath: "a.md", Kind: PathKindFile}
	hints := MergeDirtyHints(nil, []DirtyHint{{RelativePath: "a.md", HintKind: DirtyHintCreated, FirstSeenAt: old, LastSeenAt: old}})
	hints = MergeDirtyHints(hints, []DirtyHint{{RelativePath: "a.md", HintKind: DirtyHintModified, Observation: observation}})
	if hint := hints["a.md"]; hint.HintKind != DirtyHintModified || hint.Count != 2 || hint.Observation != observation || !hint.FirstSeenAt.Equal(old) {
		t.Fatal("exact public hint behavior changed", hint)
	}
	hints = MergeDirtyHints(hints, []DirtyHint{{HintKind: DirtyHintRescanRequired, LastErrorCode: "coverage_lost"}, {RelativePath: "notes", HintKind: DirtyHintSubtree}})
	if hint := hints["__rescan__"]; hint.HintKind != DirtyHintRescanRequired || hint.LastErrorCode != "coverage_lost" {
		t.Fatal("scoped work swallowed lost-coverage repair", hints)
	}
	for _, invalid := range []string{"", ".", "../escape", "/absolute"} {
		hints := MergeDirtyHints(nil, []DirtyHint{{RelativePath: invalid, HintKind: DirtyHintSubtree}})
		if len(hints) != 1 || hints["__rescan__"].HintKind != DirtyHintRescanRequired {
			t.Fatal("invalid subtree was not repaired conservatively", invalid, hints)
		}
	}
}

func TestSubtreeHintSnapshotRetainsAbsorbedArrivals(t *testing.T) {
	store := NewStore(t.TempDir())
	parent := DirtyHint{RelativePath: "notes", HintKind: DirtyHintSubtree,
		Observation: &PathObservation{RelativePath: "notes", Kind: PathKindDirectory, Exists: true, Safe: true}}
	if err := store.AddDirtyHint("root", parent); err != nil {
		t.Fatal(err)
	}
	observed, err := store.ListDirtyHints("root")
	if err != nil || len(observed) != 1 {
		t.Fatal(observed, err)
	}
	if err := store.AddDirtyHint("root", DirtyHint{RelativePath: "notes/late.md", HintKind: DirtyHintCreated,
		Observation: &PathObservation{RelativePath: "notes/late.md", Kind: PathKindFile}}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := store.ClearObservedDirtyHints("root", observed); err != nil || cleared != 0 {
		t.Fatal("an old scan cleared an absorbed arrival", cleared, err)
	}
	restarted := NewStore(store.DataDir)
	remaining, err := restarted.ListDirtyHints("root")
	if err != nil || len(remaining) != 1 || remaining[0].Count != 2 || remaining[0].Generation == observed[0].Generation || remaining[0].Observation.RelativePath != "notes" {
		t.Fatal("subtree identity did not survive restart", remaining, err)
	}
	if err := store.AddDirtyHint("root", DirtyHint{RelativePath: "notes", HintKind: DirtyHintDeleted}); err != nil {
		t.Fatal(err)
	}
	if cleared, err := store.ClearObservedDirtyHints("root", remaining); err != nil || cleared != 0 {
		t.Fatal("same-path replacement was acknowledged by an older walk", cleared, err)
	}
	remaining, err = store.ListDirtyHints("root")
	if err != nil || len(remaining) != 1 || remaining[0].HintKind != DirtyHintSubtree || remaining[0].Observation != nil {
		t.Fatal("an exact event downgraded pending subtree work", remaining, err)
	}
	if cleared, err := store.ClearObservedDirtyHints("root", remaining); err != nil || cleared != 1 {
		t.Fatal(cleared, err)
	}
}

func TestSubtreeHintCoalescesBeforeStoreOverflow(t *testing.T) {
	store := NewStore(t.TempDir())
	hints := make([]DirtyHint, 4098)
	for i := range hints[:4097] {
		hints[i] = DirtyHint{RelativePath: fmt.Sprintf("notes/note-%d.md", i), HintKind: DirtyHintCreated}
	}
	hints[4097] = DirtyHint{RelativePath: "notes", HintKind: DirtyHintSubtree}
	if err := store.AddDirtyHints("root", hints); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListDirtyHints("root")
	if err != nil || len(got) != 1 || got[0].RelativePath != "notes" || got[0].HintKind != DirtyHintSubtree || got[0].Count != len(hints) {
		t.Fatal("coalescible descendants forced full-root overflow", got, err)
	}
}

func subtreeWatchFixture(t *testing.T) (Store, ValidatedRoot, *NativeWatch) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root := testScanRoot(t, dir, RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*.md"},
		Watch: WatchConfig{Enabled: true, Backend: WatchBackendAuto}, Scan: ScanConfig{StabilityWindow: "0s"}})
	store := NewStore(t.TempDir())
	watch, err := StartNativeWatch(t.Context(), store, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = watch.Close() })
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	return store, root, watch
}

func awaitSubtreeWatch(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("subtree watch condition was not observed")
}

func TestNativeWatchSubtreeArrivalAndRemovalStayScoped(t *testing.T) {
	store, root, watch := subtreeWatchFixture(t)
	staged := filepath.Join(t.TempDir(), "staged")
	if err := os.MkdirAll(filepath.Join(staged, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "nested", "preexisting.md"), []byte("before registration"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root.RootPath, "incoming")
	if err := os.Rename(staged, target); err != nil {
		t.Fatal(err)
	}
	awaitHint := func(missing bool) {
		t.Helper()
		awaitSubtreeWatch(t, func() bool {
			hints, err := store.ListDirtyHints("notes")
			if err != nil {
				t.Fatal(err)
			}
			for _, hint := range hints {
				if hint.HintKind == DirtyHintRescanRequired {
					t.Fatal("directory event escalated to full root", hints)
				}
				if hint.RelativePath == "incoming" && hint.HintKind == DirtyHintSubtree && hint.Observation != nil && hint.Observation.Exists != missing {
					return true
				}
			}
			return false
		})
	}
	awaitHint(false)
	registered := false
	for _, path := range watch.watcher.WatchList() {
		registered = registered || path == filepath.Join(target, "nested")
	}
	if !registered {
		t.Fatal("arriving subtree was not watched before its catch-up hint")
	}
	if err := store.ClearDirtyHints("notes", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, staged); err != nil {
		t.Fatal(err)
	}
	awaitHint(true)
	if !watch.Healthy() {
		t.Fatal("a scoped directory removal lost healthy coverage")
	}
}

func TestNativeWatchSubtreeCoverageLossStillRequiresFullRoot(t *testing.T) {
	for _, cause := range []string{"overflow", "policy", "root-replaced"} {
		t.Run(cause, func(t *testing.T) {
			store, root, watch := subtreeWatchFixture(t)
			switch cause {
			case "overflow":
				select {
				case watch.watcher.Errors <- fsnotify.ErrEventOverflow:
				case <-time.After(3 * time.Second):
					t.Fatal("overflow was not delivered")
				}
			case "policy":
				if err := os.WriteFile(filepath.Join(root.RootPath, ".loomignore"), []byte("private/\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "root-replaced":
				if err := os.Rename(root.RootPath, root.RootPath+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(root.RootPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			awaitSubtreeWatch(t, func() bool {
				hints, err := store.ListDirtyHints("notes")
				if err != nil {
					t.Fatal(err)
				}
				return len(hints) == 1 && hints[0].HintKind == DirtyHintRescanRequired && !watch.Healthy()
			})
			if cause == "root-replaced" {
				restarted, err := StartNativeWatch(t.Context(), store, root, nil)
				if err != nil {
					t.Fatal("replacement root could not regain coverage", err)
				}
				defer restarted.Close()
				if !restarted.Healthy() {
					t.Fatal("replacement root was not watched")
				}
			}
		})
	}
}
