package projectcontracts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutMigrationDryRunMakesNoWrites(t *testing.T) {
	root := legacyLayoutFixture(t)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("MigrateProjectLayout dry-run returned error: %v", err)
	}
	if !result.OK || !result.DryRun || result.Applied || len(result.Actions) == 0 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	assertFile(t, root, LegacyRootContractPath)
	assertNoFile(t, root, CanonicalRootContractPath)
	assertFile(t, root, "tests/validate_project.sh")
	assertNoFile(t, root, ".loom/tools/validate-project.sh")
}

func TestLayoutMigrationApplyRequiresConfirmation(t *testing.T) {
	root := legacyLayoutFixture(t)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true})
	if err == nil || result.OK {
		t.Fatalf("expected apply confirmation failure: result=%#v err=%v", result, err)
	}
	assertFile(t, root, LegacyRootContractPath)
	assertNoFile(t, root, CanonicalRootContractPath)
}

func TestLayoutMigrationArchivedProjectIsDryRunOnly(t *testing.T) {
	root := legacyLayoutFixture(t)
	contract := strings.Replace(readFile(t, root, LegacyRootContractPath), "status: draft", "status: archived", 1)
	writeFile(t, root, LegacyRootContractPath, contract, 0o600)
	if result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root}); err != nil || !result.DryRun {
		t.Fatalf("archived dry-run should remain available: result=%#v err=%v", result, err)
	}
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true, Yes: true})
	if err == nil || result.OK || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("expected archived apply rejection: result=%#v err=%v", result, err)
	}
	assertFile(t, root, LegacyRootContractPath)
	assertNoFile(t, root, CanonicalRootContractPath)
}

func TestLayoutMigrationAppliesAndIsIdempotent(t *testing.T) {
	root := legacyLayoutFixture(t)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("MigrateProjectLayout apply returned error: %v result=%#v", err, result)
	}
	if !result.OK || !result.Applied || result.DryRun || result.AfterLayout != ProjectLayoutCanonical || result.RecordPath == "" {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	for _, path := range []string{
		CanonicalRootContractPath,
		".loom/contracts/notes.yaml",
		".loom/contracts/backup.yaml",
		".loom/.gitignore",
		".loom/agents/project.md",
		".loom/agents/surfaces/notes.md",
		".loom/tools/validate-project.sh",
		result.RecordPath,
	} {
		assertFile(t, root, path)
	}
	for _, path := range []string{
		LegacyRootContractPath,
		"notes/loom.notes.yaml",
		"policies/backup.yaml",
		"notes/README.md",
		"notes/AGENTS.md",
		"policies/README.md",
		"policies/AGENTS.md",
		"tests/README.md",
		"tests/AGENTS.md",
		"tests/validate_project.sh",
		"policies",
		"tests",
	} {
		assertNoFile(t, root, path)
	}
	contract := readFile(t, root, CanonicalRootContractPath)
	if !strings.Contains(contract, "backup: .loom/contracts/backup.yaml") {
		t.Fatalf("migrated root contract did not rewrite policy reference:\n%s", contract)
	}
	analysis := Analyze(root)
	if !analysis.Report.OK || analysis.Loaded == nil || analysis.Loaded.Layout != ProjectLayoutCanonical {
		t.Fatalf("migrated project should validate canonically: %#v", analysis.Report.Diagnostics)
	}

	second, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root})
	if err != nil {
		t.Fatalf("second dry-run returned error: %v", err)
	}
	if len(second.Actions) != 0 || len(second.Collisions) != 0 {
		t.Fatalf("migration should be idempotent: %#v", second)
	}
}

func TestLayoutMigrationEquivalentDestinationRemovesLegacyCopy(t *testing.T) {
	root := legacyLayoutFixture(t)
	writeFile(t, root, ".loom/contracts/notes.yaml", readFile(t, root, "notes/loom.notes.yaml"), 0o600)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("migration returned error: %v result=%#v", err, result)
	}
	assertFile(t, root, ".loom/contracts/notes.yaml")
	assertNoFile(t, root, "notes/loom.notes.yaml")
	if !hasLayoutMigrationOperation(result.Actions, "remove_redundant", "notes_contract") {
		t.Fatalf("expected redundant legacy notes removal: %#v", result.Actions)
	}
}

func TestLayoutMigrationDivergentDestinationBlocksAllWrites(t *testing.T) {
	root := legacyLayoutFixture(t)
	writeFile(t, root, ".loom/contracts/notes.yaml", strings.Replace(readFile(t, root, "notes/loom.notes.yaml"), "backup: true", "backup: false", 1), 0o600)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true, Yes: true})
	if err == nil || result.OK || len(result.Collisions) != 1 {
		t.Fatalf("expected collision: result=%#v err=%v", result, err)
	}
	assertFile(t, root, LegacyRootContractPath)
	assertNoFile(t, root, CanonicalRootContractPath)
	assertFile(t, root, "notes/loom.notes.yaml")
}

func TestLayoutMigrationPreservesCustomRootAndFacetFiles(t *testing.T) {
	root := legacyLayoutFixture(t)
	writeFile(t, root, "README.md", "custom readme\n", 0o600)
	writeFile(t, root, "AGENTS.md", "custom agent entry\n", 0o600)
	writeFile(t, root, "notes/README.md", "custom notes guide\n", 0o600)
	result, err := MigrateProjectLayout(LayoutMigrationOptions{ProjectRoot: root, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("migration returned error: %v result=%#v", err, result)
	}
	if readFile(t, root, "README.md") != "custom readme\n" || readFile(t, root, "AGENTS.md") != "custom agent entry\n" || readFile(t, root, "notes/README.md") != "custom notes guide\n" {
		t.Fatal("custom root or facet files were changed")
	}
	for _, path := range []string{"README.md", "AGENTS.md", "notes/README.md"} {
		if !hasLayoutMigrationSkip(result.Skips, path) {
			t.Fatalf("missing preservation skip for %s: %#v", path, result.Skips)
		}
	}
}

func TestLayoutMigrationRejectsSymlinkedControlPath(t *testing.T) {
	root := legacyLayoutFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".loom")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	if _, err := PlanProjectLayoutMigration(root); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
	assertNoFile(t, outside, "project.yaml")
}

func TestLayoutMigrationPreflightRejectsChangedSource(t *testing.T) {
	root := legacyLayoutFixture(t)
	plan, err := PlanProjectLayoutMigration(root)
	if err != nil {
		t.Fatalf("plan returned error: %v", err)
	}
	writeFile(t, root, "notes/loom.notes.yaml", strings.Replace(validNotesContract(), "backup: true", "backup: false", 1), 0o600)
	if _, err := ApplyProjectLayoutMigration(plan, true); err == nil || !strings.Contains(err.Error(), "changed after planning") {
		t.Fatalf("expected changed-source rejection, got %v", err)
	}
	assertNoFile(t, root, CanonicalRootContractPath)
}

func TestLayoutMigrationPartialFailureWritesBoundedRecord(t *testing.T) {
	root := t.TempDir()
	plan := LayoutMigrationPlan{
		ProjectRoot: root,
		Layout:      ProjectLayoutLegacy,
		Actions: []LayoutMigrationAction{
			{Order: 1, Operation: "create", Kind: "test", Destination: ".loom/project.yaml", AfterHash: hashBytesURI([]byte("created\n")), Status: "planned"},
			{Order: 2, Operation: "unsupported", Kind: "test", Status: "planned"},
		},
		planned: []plannedLayoutAction{
			{Public: LayoutMigrationAction{Order: 1, Operation: "create", Kind: "test", Destination: ".loom/project.yaml", AfterHash: hashBytesURI([]byte("created\n")), Status: "planned"}, Content: []byte("created\n"), Mode: 0o644},
			{Public: LayoutMigrationAction{Order: 2, Operation: "unsupported", Kind: "test", Status: "planned"}},
		},
	}
	result, err := ApplyProjectLayoutMigration(plan, true)
	if err == nil || result.OK || result.RecordPath == "" {
		t.Fatalf("expected bounded partial failure: result=%#v err=%v", result, err)
	}
	assertFile(t, root, ".loom/project.yaml")
	assertFile(t, root, result.RecordPath)
	record := readFile(t, root, result.RecordPath)
	if !strings.Contains(record, "unsupported layout migration operation") || len(record) > 32*1024 {
		t.Fatalf("unexpected bounded migration record (%d bytes): %s", len(record), record)
	}
}

func legacyLayoutFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	contract := `
kind: loom.project
schema_version: project.contract.v0.3
project:
  slug: legacy-layout
  name: Legacy Layout
  owner_node: main
  status: draft
facets:
  notes: true
  backup_policy: true
  portal: true
policies:
  backup: policies/backup.yaml
	`
	writeLayoutContract(t, root, LegacyRootContractPath, contract)
	loaded, err := LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject returned error: %v", err)
	}
	data, err := scaffoldDataFromContract(NormalizeContract(loaded.Contract), enabledContractFacets(loaded.Contract.Facets))
	if err != nil {
		t.Fatalf("scaffold data: %v", err)
	}
	notesContract, err := renderScaffoldTemplate("notes/loom.notes.yaml", notesContractTemplate, data)
	if err != nil {
		t.Fatalf("render notes contract: %v", err)
	}
	writeFile(t, root, "notes/loom.notes.yaml", notesContract, 0o600)
	backupPolicy, err := renderScaffoldTemplate("policies/backup.yaml", backupPolicyTemplate, data)
	if err != nil {
		t.Fatalf("render backup policy: %v", err)
	}
	writeFile(t, root, "policies/backup.yaml", backupPolicy, 0o600)
	legacyRoot, err := renderedLegacyRootFiles(data)
	if err != nil {
		t.Fatalf("render legacy root files: %v", err)
	}
	for path, content := range legacyRoot {
		writeFile(t, root, path, string(content), 0o600)
	}
	legacyGenerated, err := renderedLegacyGeneratedFiles(data, loaded.Contract)
	if err != nil {
		t.Fatalf("render legacy generated files: %v", err)
	}
	for path, content := range legacyGenerated {
		writeFile(t, root, path, string(content), 0o600)
	}
	return root
}

func hasLayoutMigrationOperation(actions []LayoutMigrationAction, operation, kind string) bool {
	for _, action := range actions {
		if action.Operation == operation && action.Kind == kind {
			return true
		}
	}
	return false
}

func hasLayoutMigrationSkip(skips []LayoutMigrationSkip, path string) bool {
	for _, skip := range skips {
		if skip.Path == path {
			return true
		}
	}
	return false
}
