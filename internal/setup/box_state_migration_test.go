package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanBoxStateMigrationInventoriesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy")
	target := filepath.Join(root, "canonical")
	if err := os.MkdirAll(filepath.Join(source, "lane", "bundles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "lane", "batch.json"), []byte("batch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBoxStateMigration(source, target)
	if err != nil {
		t.Fatalf("PlanBoxStateMigration returned error: %v", err)
	}
	if !plan.DryRun || plan.State != "ready" || plan.FileCount != 1 || plan.DirectoryCount != 2 || plan.HashedFiles != 1 || plan.TotalBytes != 6 {
		t.Fatalf("migration inventory unexpected: %#v", plan)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("migration plan must not create target root, stat err=%v", err)
	}
}

func TestPlanBoxStateMigrationDetectsConflicts(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy")
	target := filepath.Join(root, "canonical")
	for _, path := range []string{source, target} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(source, "record.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "record.json"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBoxStateMigration(source, target)
	if err != nil {
		t.Fatalf("PlanBoxStateMigration returned error: %v", err)
	}
	if plan.State != "conflict" || len(plan.Conflicts) != 1 || plan.Conflicts[0].RelativePath != "record.json" {
		t.Fatalf("conflict plan unexpected: %#v", plan)
	}
}

func TestPlanBoxStateMigrationRejectsPartialDualRoots(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "legacy")
	target := filepath.Join(root, "canonical")
	for path, payload := range map[string]string{
		filepath.Join(source, "source-only.json"): "source",
		filepath.Join(target, "target-only.json"): "target",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PlanBoxStateMigration(source, target)
	if err != nil {
		t.Fatalf("PlanBoxStateMigration returned error: %v", err)
	}
	if plan.State != "conflict" || len(plan.Conflicts) != 2 {
		t.Fatalf("partial dual roots must conflict: %#v", plan)
	}
}

func TestPlanBoxStateMigrationRejectsUnsafeRoots(t *testing.T) {
	for _, roots := range [][2]string{{"", "/tmp/box-state"}, {"relative", "/tmp/box-state"}, {"/tmp/box-state", "/"}} {
		if _, err := PlanBoxStateMigration(roots[0], roots[1]); err == nil {
			t.Fatalf("PlanBoxStateMigration(%q, %q) accepted unsafe roots", roots[0], roots[1])
		}
	}
}

func TestPlanBoxStateMigrationIdenticalRootsSkipInventory(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "state-file")
	if err := os.WriteFile(shared, []byte("inventory would reject this non-directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := PlanBoxStateMigration(shared, filepath.Join(root, ".", "state-file"))
	if err != nil {
		t.Fatalf("identical roots must return before inventory or hashing: %v", err)
	}
	if !plan.DryRun || plan.State != "not_needed" || plan.SourceRoot != shared || plan.TargetRoot != shared {
		t.Fatalf("identical-root migration plan unexpected: %#v", plan)
	}
	if len(plan.Entries) != 0 || len(plan.Conflicts) != 0 || plan.FileCount != 0 || plan.HashedFiles != 0 || plan.TotalBytes != 0 {
		t.Fatalf("identical-root plan performed inventory work: %#v", plan)
	}
}

func TestPlanBoxStateMigrationRejectsDistinctSymlinkRoot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target-link")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanBoxStateMigration(source, target); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("distinct migration symlink root error = %v", err)
	}
}
