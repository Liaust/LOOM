package box

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestMultiBoxWatchIdentitiesPreservePersonal(t *testing.T) {
	root := initializedWatchBoxWithID(t, ProfileMain, "box_company")
	legacy := Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "macbook"}
	personal, err := BuildWatchPlan(WatchStatusInput{Resolved: legacy})
	if err != nil {
		t.Fatal(err)
	}
	selected := legacy
	selected.BoxID, selected.RootRef = "box_company", "box:box_company"
	selected.RuntimeStateRoot = filepath.Join(t.TempDir(), "company-state")
	company, err := BuildWatchPlan(WatchStatusInput{Resolved: selected})
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range company.WatchedRoots {
		if !strings.HasPrefix(personal.WatchedRoots[i].BackendRootKey, "loom_box__") || personal.WatchedRoots[i].SafeRootKey != "loom_box" {
			t.Fatal("legacy worker identity changed")
		}
		if item.BackendRootKey == personal.WatchedRoots[i].BackendRootKey || item.SafeRootKey != WatchSafeRootKey(selected.BoxID, selected.RootRef) {
			t.Fatal("Box worker identity collides")
		}
		var cfg watchedroots.RootConfig
		if err := json.Unmarshal(item.ConfigJSON, &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg.RootKey != item.BackendRootKey || cfg.SafeRootKey != item.SafeRootKey || item.ConfigHash != watchedroots.ConfigHash(cfg) {
			t.Fatal("root/config binding mismatch")
		}
	}
	if WatchSafeRootKey("box_Company", "box:box_Company") == WatchSafeRootKey("box_company", "box:box_company") {
		t.Fatal("case-sensitive IDs collapsed")
	}
	selected.BoxID = "box_foreign"
	if _, err := BuildWatchPlan(WatchStatusInput{Resolved: selected}); err == nil {
		t.Fatal("descriptor identity mismatch accepted")
	}
}

func TestMultiBoxResolveRequiresBoundState(t *testing.T) {
	base := t.TempDir()
	input := ResolveInput{BoxID: "box_company", RootRef: "box:box_company", ConfiguredPath: filepath.Join(base, "company"), RuntimeStateRoot: filepath.Join(base, "state", "box_company"), NodeRole: "main"}
	resolved, err := Resolve(input)
	if err != nil || resolved.BoxID != input.BoxID || resolved.RuntimeStateRoot != input.RuntimeStateRoot {
		t.Fatal(resolved, err)
	}
	input.ExplicitPath = filepath.Join(base, "personal")
	if _, err := Resolve(input); err == nil {
		t.Fatal("explicit path retargeted selected Box")
	}
	input.ExplicitPath, input.RuntimeStateRoot = "", ""
	if _, err := Resolve(input); err == nil {
		t.Fatal("new Box fell back to global state")
	}
	resolved.RuntimeStateRoot = ""
	if _, err := ResolveRuntimeState(resolved); err == nil {
		t.Fatal("new Box state fell back at runtime")
	}
}
