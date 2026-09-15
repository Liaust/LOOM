package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatePathUsesXDGConfigHome(t *testing.T) {
	path := StatePath(Env{"XDG_CONFIG_HOME": "/tmp/xdg", "HOME": "/tmp/home"})
	want := filepath.Join("/tmp/xdg", "loom", "cli-state.json")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestLoadSaveStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loom", "cli-state.json")
	state := DefaultState()
	state.LastScreen = "background"
	state.RecentActions = []RecentAction{{ActionID: "raw.workers.list"}}
	if err := SaveState(path, state); err != nil {
		t.Fatalf("SaveState returned error: %v", err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState returned error: %v", err)
	}
	if loaded.SchemaVersion != StateSchemaVersion || loaded.Theme != ThemeCoffee || loaded.LastScreen != "background" {
		t.Fatalf("loaded unexpected state: %#v", loaded)
	}
	if len(loaded.RecentActions) != 1 || loaded.RecentActions[0].ActionID != "raw.workers.list" {
		t.Fatalf("loaded unexpected recent actions: %#v", loaded.RecentActions)
	}
}

func TestLoadStateBestEffortReturnsDefaultOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	state := LoadStateBestEffort(path)
	if state.SchemaVersion != StateSchemaVersion || state.Theme != ThemeCoffee {
		t.Fatalf("best effort default = %#v", state)
	}
}
