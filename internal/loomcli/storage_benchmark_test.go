package loomcli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageBenchmarkRejectsNonAcceptancePath(t *testing.T) {
	report := runStorageBenchmark(context.Background(), storageBenchmarkFlags{
		SizeMB:    1,
		LocalPath: filepath.Join(t.TempDir(), "scratch"),
		SkipSMB:   true,
		SkipSmall: true,
	})
	if len(report.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(report.Results))
	}
	if report.Results[0].Status != "failed" || !strings.Contains(report.Results[0].Error, ".loom-acceptance/debug") {
		t.Fatalf("expected acceptance path failure, got %#v", report.Results[0])
	}
}

func TestStorageBenchmarkDefaultPathsUseAcceptanceDebug(t *testing.T) {
	cmd := newStorageBenchmarkCommand(&options{})
	localFlag := cmd.Flag("local-path")
	smbFlag := cmd.Flag("smb-path")
	if localFlag == nil || smbFlag == nil {
		t.Fatalf("benchmark flags missing")
	}
	for _, value := range []string{localFlag.DefValue, smbFlag.DefValue} {
		if !storageBenchmarkPathHasAcceptanceDebug(value) {
			t.Fatalf("default benchmark path must stay under .loom-acceptance/debug: %s", value)
		}
	}
}

func TestStorageBenchmarkSmallFilesUnderAcceptanceDebug(t *testing.T) {
	base := filepath.Join(t.TempDir(), ".loom-acceptance", "debug", "bench")
	result := runSmallFilesBenchmark("small", base, 3, false)
	if result.Status != "succeeded" {
		t.Fatalf("small files benchmark failed: %#v", result)
	}
	entries, err := filepath.Glob(filepath.Join(base, "*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("small files benchmark did not clean up: %#v", entries)
	}
}
