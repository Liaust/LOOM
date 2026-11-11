package watchedroots

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOutputPagesBoundReadsAndCoverEveryPath(t *testing.T) {
	store := NewStore(t.TempDir())
	config := NormalizeRootConfig(RootConfig{RootKey: "notes"})
	root := ValidatedRoot{Config: config, ConfigHash: ConfigHash(config), RootReachable: true}
	modified := time.Now().UTC()
	for i := range 129 {
		if err := store.SavePathState(PathState{RootKey: "notes", RelativePath: fmt.Sprintf("%03d.md", i), Status: PathStatusIncluded, Kind: PathKindFile, ModifiedAt: &modified}); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	after := ""
	for turn := 0; ; turn++ {
		// Reopening the store must not lose progress or require an in-memory index.
		page, err := PlanOutputsPage(NewStore(store.DataDir), root, "test", after, 17, 5)
		if err != nil || page.Page == nil || page.Page.StatesRead > 17 || len(page.Actions) > 5 {
			t.Fatal(page, err)
		}
		for _, action := range page.Actions {
			if seen[action.RelativePath] {
				t.Fatal("duplicate path across pages", action.RelativePath)
			}
			seen[action.RelativePath] = true
		}
		if !page.Page.More {
			break
		}
		if page.Page.Next <= after || turn > 129 {
			t.Fatal("cursor did not advance", page.Page)
		}
		after = page.Page.Next
	}
	if len(seen) != 129 {
		t.Fatalf("only %d paths covered", len(seen))
	}
	// Corruption beyond a page must not be decoded by that page. Exact cursor
	// validation still prevents arbitrary path traversal or identity mismatches.
	states, _, err := store.ListPathStatesPage("notes", "", 129)
	if err != nil {
		t.Fatal(err)
	}
	last := states[len(states)-1]
	if err := os.WriteFile(filepath.Join(store.RootPaths("notes").PathsDir, last.PathKey[:2], last.PathKey+".json"), []byte("broken outside page"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanOutputsPage(store, root, "test", "", 17, 5); err != nil {
		t.Fatal("page decoded unrelated retained state", err)
	}
	if _, _, err := store.ListPathStatesPage("notes", "../../escape", 5); err == nil {
		t.Fatal("accepted invalid cursor")
	}
}

func TestOutputCursorResetsOnlyWhenInventoryOrConfigurationChanges(t *testing.T) {
	store := NewStore(t.TempDir())
	previous := RootCheckpoint{ConfigHash: "policy", PathStateSnapshotHash: "inventory"}
	if err := store.SaveOutputAfter("notes", previous, "cursor"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, config, inventory, want string
	}{
		{"continuation", "policy", "inventory", "cursor"},
		{"changed inventory", "policy", "new-inventory", ""},
		{"new policy", "new-policy", "inventory", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := previous
			changed.ConfigHash = tc.config
			changed.PathStateSnapshotHash = tc.inventory
			got, err := NewStore(store.DataDir).LoadOutputAfter("notes", changed)
			if err != nil || got != tc.want {
				t.Fatal(got, err)
			}
		})
	}
}
