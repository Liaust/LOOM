package config

import (
	"path/filepath"
	"testing"
)

func TestBoxesFileConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	t.Setenv("LOOM_BOXES_FILE", path)
	cfg, err := Load(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BoxesFile != path || cfg.SafeFields()["boxes_file"] != path {
		t.Fatal("catalog configuration not propagated")
	}
	other := filepath.Join(t.TempDir(), "other.json")
	cfg, err = Load(Overrides{BoxesFile: other})
	if err != nil || cfg.BoxesFile != other {
		t.Fatalf("override: %s %v", cfg.BoxesFile, err)
	}
	if _, err := Load(Overrides{BoxesFile: "relative.json"}); err == nil {
		t.Fatal("relative catalog path accepted")
	}
}
