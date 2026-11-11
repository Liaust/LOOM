package config

import "testing"

func TestNotesSearchBackendExplicitSelection(t *testing.T) {
	for _, backend := range []string{"native", "pg_search"} {
		t.Setenv("LOOM_NOTES_SEARCH_BACKEND", backend)
		cfg, err := Load(Overrides{})
		if err != nil || cfg.NotesSearchBackend != backend {
			t.Fatalf("backend %s: %s %v", backend, cfg.NotesSearchBackend, err)
		}
	}
	t.Setenv("LOOM_NOTES_SEARCH_BACKEND", "automatic-fallback")
	if _, err := Load(Overrides{}); err == nil {
		t.Fatal("accepted unsupported backend")
	}
}
