package watchedroots

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOperationalConfigDoesNotInvalidateCoverageOrWatch(t *testing.T) {
	root := testScanRoot(t, t.TempDir(), RootConfig{RootKey: "notes", SafeRootKey: "slice09", Include: []string{"**/*"}})
	before := root
	at := time.Now()
	cp := RootCheckpoint{ConfigHash: root.ConfigHash, ContentConfigHash: ContentConfigHash(root.Config),
		LastFullRescanAt: &at, StoredCounts: &ScanCounts{}, RootReachable: true}
	root.Config.Scan.FullRescanInterval, root.Config.Scan.MaxRuntime = "12h", "1m"
	root.Config.Watch.DebounceWindow, root.Config.DisplayName = "7s", "Other display"
	root.ConfigHash = ConfigHash(root.Config)
	if !coverageConfigMatches(root, cp) || autoScanMode(root, cp, nil, at.Add(time.Minute)) != RunStatusSkipped {
		t.Fatal("operational-only update requested another inventory")
	}
	w := &NativeWatch{}
	w.root.Store(&before)
	raw, _ := json.Marshal(root.Config)
	if !w.UpdateConfiguration(raw) || w.root.Load().ConfigHash != root.ConfigHash {
		t.Fatal("operational update did not refresh retained watch receipt")
	}
	root.Config.Include = []string{"restricted/**"}
	root.ConfigHash = ConfigHash(root.Config)
	raw, _ = json.Marshal(root.Config)
	if coverageConfigMatches(root, cp) || w.UpdateConfiguration(raw) {
		t.Fatal("material selection change preserved old topology/coverage")
	}
}
