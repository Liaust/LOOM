package config

import "testing"

func TestNotesWorkspaceConfigEnvironment(t *testing.T) {
	var cfg Config
	t.Setenv("LOOM_NOTES_WORKSPACE_CONFIG", "/etc/loom/notes-workspace.json")
	if err := applyEnv(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.NotesWorkspaceConfig != "/etc/loom/notes-workspace.json" {
		t.Fatalf("configuration path not loaded: %q", cfg.NotesWorkspaceConfig)
	}
}
