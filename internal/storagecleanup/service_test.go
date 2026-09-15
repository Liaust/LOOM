package storagecleanup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInventoryOnlyAutoAppliesExplicitAcceptanceArtifacts(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteCleanupTestFile(t, filepath.Join(root, "Documents", "v0-6-2-slice-02-smoke.txt"), "user data")
	backing := filepath.Join(root, "_backing", "source.txt")
	mustWriteCleanupTestFile(t, backing, "backup")
	if err := os.MkdirAll(filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance"), 0o755); err != nil {
		t.Fatalf("mkdir backup view: %v", err)
	}
	if err := os.Symlink(backing, filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt")); err != nil {
		t.Fatalf("symlink backup view: %v", err)
	}

	result, err := Inventory(InventoryInput{Root: root, Production: true, AllowBroadProductionScan: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Inventory returned error: %v", err)
	}
	if result.Summary.Candidates != 2 {
		t.Fatalf("candidates = %d, want 2: %#v", result.Summary.Candidates, result.Items)
	}
	mainItem := cleanupTestCandidate(t, result.Items, "Documents/v0-6-2-slice-02-smoke.txt")
	if mainItem.AutoApply || mainItem.Action != ActionManualReview {
		t.Fatalf("canonical Documents item should require manual review: %#v", mainItem)
	}
	if mainItem.CleanupScope != CleanupScopeManualReview || mainItem.CatalogDisposition != CatalogDispositionNotTouched {
		t.Fatalf("canonical Documents item should not touch catalog or data automatically: %#v", mainItem)
	}
	viewItem := cleanupTestCandidate(t, result.Items, "macbook/Backups/Documents/current/.loom-acceptance/v0-6-2-slice-02-smoke.txt")
	if !viewItem.AutoApply || viewItem.Action != ActionRemoveSymlink {
		t.Fatalf("acceptance symlink should be auto-applicable: %#v", viewItem)
	}
	if viewItem.CleanupScope != CleanupScopeViewOnly || viewItem.CatalogDisposition != CatalogDispositionUnchanged {
		t.Fatalf("acceptance symlink should be path-only cleanup with unchanged catalog: %#v", viewItem)
	}
}

func TestApplyRemovesOnlyAutoApplyItemsAndSkipsManualReview(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainFile := filepath.Join(root, "Documents", "v0-6-2-slice-02-smoke.txt")
	mustWriteCleanupTestFile(t, mainFile, "main owned")
	viewFile := filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt")
	mustWriteCleanupTestFile(t, viewFile, "generated")
	plan, err := Plan(PlanInput{Root: root, Production: true, AllowBroadProductionScan: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}

	result, err := Apply(ApplyInput{Plan: &plan, Yes: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Summary.Quarantined != 1 || result.Summary.ManualReview != 1 {
		t.Fatalf("apply summary unexpected: %#v changes=%#v", result.Summary, result.Changes)
	}
	if _, err := os.Stat(viewFile); !os.IsNotExist(err) {
		t.Fatalf("acceptance file should be moved to quarantine, err=%v", err)
	}
	quarantined := filepath.Join(result.QuarantineDir, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt")
	if _, err := os.Lstat(quarantined); err != nil {
		t.Fatalf("acceptance file should exist in quarantine: %v", err)
	}
	if _, err := os.Stat(mainFile); err != nil {
		t.Fatalf("main Documents file should be preserved: %v", err)
	}
}

func TestApplyRefusesWithoutYes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWriteCleanupTestFile(t, filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt"), "generated")
	plan, err := Plan(PlanInput{Root: root, Production: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	result, err := Apply(ApplyInput{Plan: &plan, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Refused || result.Refusal == "" {
		t.Fatalf("expected refusal without yes: %#v", result)
	}
}

func TestApplyRequiresMetadataLossAcceptanceForSidecarCandidates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sidecar := filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-8-slice-07.loom-meta.json")
	mustWriteCleanupTestFile(t, sidecar, "{}")
	plan, err := Plan(PlanInput{Root: root, Production: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	item := cleanupTestCandidate(t, plan.Items, "macbook/Backups/Documents/current/.loom-acceptance/v0-6-8-slice-07.loom-meta.json")
	if !item.RequiresMetadataLossAcceptance || len(item.FidelityFindings) == 0 {
		t.Fatalf("expected sidecar metadata-loss finding: %#v", item)
	}

	blocked, err := Apply(ApplyInput{Plan: &plan, Yes: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if blocked.Summary.Blocked != 1 {
		t.Fatalf("expected blocked sidecar without acceptance: %#v changes=%#v", blocked.Summary, blocked.Changes)
	}
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar should be preserved after blocked apply: %v", err)
	}

	applied, err := Apply(ApplyInput{Plan: &plan, Yes: true, AcceptMetadataLoss: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply with acceptance returned error: %v", err)
	}
	if applied.Summary.Quarantined != 1 {
		t.Fatalf("expected accepted sidecar to be quarantined: %#v changes=%#v", applied.Summary, applied.Changes)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be quarantined after acceptance, err=%v", err)
	}
}

func TestApplyBlocksEscapingPlanItem(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "v0-6-2-slice-02-smoke.txt")
	mustWriteCleanupTestFile(t, outside, "outside")
	plan := CleanupPlan{
		SchemaVersion: SchemaVersion,
		Root:          root,
		GeneratedAt:   fixedCleanupNow(),
		Items: []Candidate{{
			RelativePath: "v0-6-2-slice-02-smoke.txt",
			FullPath:     outside,
			Kind:         KindFile,
			SizeBytes:    int64(len("outside")),
			ModTime:      mustCleanupStat(t, outside).ModTime().UTC(),
			Action:       ActionRemoveFile,
			AutoApply:    true,
		}},
	}
	result, err := Apply(ApplyInput{Plan: &plan, Yes: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Summary.Blocked != 1 {
		t.Fatalf("expected blocked escape, got %#v changes=%#v", result.Summary, result.Changes)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file should be preserved: %v", err)
	}
}

func TestApplyBlocksChangedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "macbook", "Backups", "Documents", "current", ".loom-acceptance", "v0-6-2-slice-02-smoke.txt")
	mustWriteCleanupTestFile(t, file, "generated")
	plan, err := Plan(PlanInput{Root: root, Production: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	mustWriteCleanupTestFile(t, file, "changed after plan")
	result, err := Apply(ApplyInput{Plan: &plan, Yes: true, Now: fixedCleanupNow})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Summary.Blocked != 1 {
		t.Fatalf("expected changed file to block, got %#v changes=%#v", result.Summary, result.Changes)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("changed file should be preserved: %v", err)
	}
}

func mustWriteCleanupTestFile(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func cleanupTestCandidate(t *testing.T, items []Candidate, rel string) Candidate {
	t.Helper()
	for _, item := range items {
		if item.RelativePath == rel {
			return item
		}
	}
	t.Fatalf("candidate %s not found: %#v", rel, items)
	return Candidate{}
}

func mustCleanupStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info
}

func fixedCleanupNow() time.Time {
	return time.Date(2026, 6, 7, 20, 0, 0, 0, time.UTC)
}
