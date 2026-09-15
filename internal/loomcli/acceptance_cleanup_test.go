package loomcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunAcceptanceCleanupArchivesOnlyAcceptanceChildren(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "Documents", ".loom-acceptance", "v0.9.5")
	normal := filepath.Join(root, "Documents", "keep.md")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "probe.md"), []byte("test"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(normal, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write normal file: %v", err)
	}

	dryRun, err := runAcceptanceCleanup(acceptanceCleanupInput{
		Root: root,
		Now:  time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("dry-run cleanup returned error: %v", err)
	}
	if dryRun.Status != "dry_run" || dryRun.Summary.WouldArchive != 1 {
		t.Fatalf("unexpected dry-run result: %#v", dryRun)
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Fatalf("dry-run should keep fixture: %v", err)
	}

	applied, err := runAcceptanceCleanup(acceptanceCleanupInput{
		Root: root,
		Yes:  true,
		Now:  time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("apply cleanup returned error: %v", err)
	}
	if applied.Status != "cleaned" || applied.Summary.Archived != 1 {
		t.Fatalf("unexpected apply result: %#v", applied)
	}
	if _, err := os.Stat(fixture); !os.IsNotExist(err) {
		t.Fatalf("fixture should be archived away, err=%v", err)
	}
	archived := filepath.Join(root, ".loom-acceptance-archive", "20260706T120000Z", "Documents", ".loom-acceptance", "v0.9.5", "probe.md")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archived fixture missing: %v", err)
	}
	if _, err := os.Stat(normal); err != nil {
		t.Fatalf("normal file should remain: %v", err)
	}
}

func TestSupportAcceptanceCleanupCommandJSONDryRun(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, ".loom-acceptance", "run")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	stdout, stderr, err := executeRootCommand("--json", "support", "acceptance", "cleanup", "--root", root)
	if err != nil {
		t.Fatalf("support acceptance cleanup returned error: %v stderr=%s", err, stderr)
	}
	var result acceptanceCleanupResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode cleanup result: %v output=%s", err, stdout)
	}
	if !result.DryRun || result.Summary.WouldArchive != 1 {
		t.Fatalf("unexpected command result: %#v", result)
	}
}
