package filesystemconnector

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeConfigSortsAndDefaultsSafeRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := NormalizeConfig(Config{SafeRoots: []SafeRoot{
		{
			RootKey:      "Beta Root",
			AbsolutePath: filepath.Join(dir, "b", "..", "b"),
			AllowList:    true,
		},
		{
			RootKey:      "alpha",
			DisplayName:  "Alpha Root",
			AbsolutePath: filepath.Join(dir, "a"),
			AllowIngest:  true,
		},
	}})
	if len(config.SafeRoots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(config.SafeRoots))
	}
	if config.SafeRoots[0].RootKey != "alpha" || config.SafeRoots[1].RootKey != "beta-root" {
		t.Fatalf("roots were not normalized and sorted: %#v", config.SafeRoots)
	}
	if config.SafeRoots[0].MaxFileBytes != DefaultMaxFileBytes {
		t.Fatalf("expected default max file bytes, got %d", config.SafeRoots[0].MaxFileBytes)
	}
}

func TestValidateConfigRejectsDuplicateRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	err := ValidateConfig(Config{SafeRoots: []SafeRoot{
		DefaultSafeRoot("slice13", dir),
		DefaultSafeRoot("Slice13", dir),
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate root error, got %v", err)
	}
}

func TestPublicSummariesDoNotExposeAbsolutePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := Config{SafeRoots: []SafeRoot{DefaultSafeRoot("slice13", dir)}}
	summaries := PublicSummaries(config)
	if len(summaries) != 1 {
		t.Fatalf("expected one summary, got %d", len(summaries))
	}
	if strings.Contains(string(mustJSON(map[string]any{"roots": summaries})), dir) {
		t.Fatal("public summaries leaked the absolute safe root path")
	}
}

func TestAvailableSafeRootsExcludesPrivateBackupOnlyRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	config := Config{SafeRoots: []SafeRoot{
		DefaultSafeRoot("public", dir),
		{
			RootKey:           "private",
			AbsolutePath:      filepath.Join(dir, "private"),
			PrivateBackupOnly: true,
			AllowList:         true,
			AllowMetadata:     true,
			AllowIngest:       true,
		},
	}}
	roots := AvailableSafeRoots(config)
	if len(roots) != 1 || roots[0].RootKey != "public" {
		t.Fatalf("unexpected available roots: %#v", roots)
	}
}
