package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
)

func TestRepairDryRunDoesNotCreatePaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow},
		FixIDs:      []string{RepairCreateMissingBoxRoot},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 1 || result.Changed[0].Status != "would_create" {
		t.Fatalf("changed = %#v", result.Changed)
	}
	if _, err := os.Stat(manifest.BoxPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create Box path, stat err=%v", err)
	}
}

func TestRepairCreatesBoxRootAndLoomDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow},
		FixIDs:      []string{RepairCreateMissingBoxRoot, RepairCreateMissingBoxLoomDir},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Fatalf("changed = %#v", result.Changed)
	}
	if info, err := os.Stat(filepath.Join(manifest.BoxPath, ".loom")); err != nil || !info.IsDir() {
		t.Fatalf("expected .loom dir, info=%v err=%v", info, err)
	}
}

func TestRepairMainBoxServiceACLDryRunPlansMaskAndUserEntries(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := WriteManifest(manifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairMainBoxServiceACL},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 3 {
		t.Fatalf("changed = %#v", result.Changed)
	}
	for _, change := range result.Changed {
		if change.Status != "would_set_acl" || !strings.Contains(change.Message, "u:loom:") || !strings.Contains(change.Message, ",m::") {
			t.Fatalf("unexpected ACL dry-run change: %#v", change)
		}
	}
}

func TestRepairAllSafeSelectsSafeFindingRepairs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	if err := WriteManifest(path, testWorkspaceManifest(dir)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow},
		FixIDs:      []string{RepairAllSafe},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Actions) == 0 {
		t.Fatal("expected all-safe to select actions")
	}
	for _, action := range result.Actions {
		if !action.Safe || action.Destructive {
			t.Fatalf("unsafe action selected: %#v", action)
		}
	}
}

func TestRepairRewriteManifestRedactsSecrets(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "install.yaml")
	raw := []byte(`schema_version: loom.install.v0.5
install_id: install_secret
installed_at: 2026-05-31T12:00:00Z
updated_at: 2026-05-31T12:00:00Z
setup_version: test
node_key: macbook
display_name: MacBook
node_kind: workspace
node_role: primary_workspace
runtime_class: workspace_full
main_url: http://10.44.0.2:8080
authority_profile: primary_workspace_default
runtime_profile: workspace_full
install_mode: user
service_manager: none
package_mode: local-build
metadata:
  credential_token: not-redacted
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(t.TempDir(), "home")), Now: fixedNow},
		FixIDs:      []string{RepairRewriteRedactedManifest},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 1 || result.Changed[0].Status != "written" {
		t.Fatalf("changed = %#v", result.Changed)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if strings.Contains(string(after), "not-redacted") {
		t.Fatalf("secret was not redacted:\n%s", string(after))
	}
	if !strings.Contains(string(after), "[REDACTED]") {
		t.Fatalf("redacted marker missing:\n%s", string(after))
	}
}

func TestRepairNonInteractiveWithoutYesRefusesMutation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	result, err := Repair(RepairInput{
		StatusInput:   StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow},
		FixIDs:        []string{RepairCreateMissingBoxRoot},
		NoInteractive: true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "--yes") {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(manifest.BoxPath); !os.IsNotExist(err) {
		t.Fatalf("refused repair should not create path, stat err=%v", err)
	}
}

func TestRepairRefreshesWorkspaceBinarySymlinks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifest := testWorkspaceManifest(dir)
	manifest.InstallMode = InstallModeService
	manifest.ServiceManager = ServiceManagerLaunchd
	manifest.PackageMode = PackageModePrebuilt
	path := filepath.Join(dir, "install.yaml")
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	for _, binary := range []string{"loom", "loom-node-agent"} {
		target := filepath.Join(manifest.DataDir, "current", "bin", binary)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir target bin: %v", err)
		}
		if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write target binary: %v", err)
		}
	}

	report, err := Doctor(DoctorInput{StatusInput: StatusInput{
		ManifestPath: path,
		Facts:        testMacFacts(manifest.HomeDir),
		Now:          fixedNow,
	}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.binary.loom.missing")
	if finding.RepairID != RepairRefreshWorkspaceBinaryLinks {
		t.Fatalf("repair id = %q", finding.RepairID)
	}

	dryRun, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testMacFacts(manifest.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairRefreshWorkspaceBinaryLinks},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Repair dry-run returned error: %v", err)
	}
	if len(dryRun.Changed) != 2 || dryRun.Changed[0].Status != "would_link" {
		t.Fatalf("dry-run changed = %#v", dryRun.Changed)
	}

	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: path, Facts: testMacFacts(manifest.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairRefreshWorkspaceBinaryLinks},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Fatalf("changed = %#v skipped=%#v", result.Changed, result.Skipped)
	}
	for _, binary := range []string{"loom", "loom-node-agent"} {
		link := filepath.Join(manifest.HomeDir, ".local", "bin", binary)
		want := filepath.Join(manifest.DataDir, "current", "bin", binary)
		if got, err := os.Readlink(link); err != nil || got != want {
			t.Fatalf("%s symlink = %q err=%v, want %s", binary, got, err, want)
		}
	}
}

func TestRepairMainHumanLinksCreatesCanonicalLinksWithoutApplyingCleanupInventory(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := os.MkdirAll(spec.StorageExportRoot, 0o755); err != nil {
		t.Fatalf("mkdir storage export: %v", err)
	}
	adminQuoted := filepath.Join(spec.HomeDir, "'LOOM BOX'")
	adminLegacyStorage := filepath.Join(spec.HomeDir, box.LegacyStorageLinkName)
	adminLegacyMainBox := filepath.Join(spec.HomeDir, box.LegacyMainBoxLinkName)
	loomdeskHome := filepath.Join(filepath.Dir(spec.HomeDir), "loomdesk")
	deskBroken := filepath.Join(loomdeskHome, "LOOM BOX")
	deskLegacyStorage := filepath.Join(loomdeskHome, box.LegacyStorageLinkName)
	deskLegacyMainBox := filepath.Join(loomdeskHome, box.LegacyMainBoxLinkName)
	if err := os.MkdirAll(spec.HomeDir, 0o755); err != nil {
		t.Fatalf("mkdir admin home: %v", err)
	}
	if err := os.MkdirAll(loomdeskHome, 0o755); err != nil {
		t.Fatalf("mkdir loomdesk home: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "missing-box"), adminQuoted); err != nil {
		t.Fatalf("symlink admin obsolete: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "legacy-storage"), adminLegacyStorage); err != nil {
		t.Fatalf("symlink admin legacy storage: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "legacy-main-box"), adminLegacyMainBox); err != nil {
		t.Fatalf("symlink admin legacy main box: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "missing-desk-box"), deskBroken); err != nil {
		t.Fatalf("symlink desk obsolete: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "desk-legacy-storage"), deskLegacyStorage); err != nil {
		t.Fatalf("symlink desk legacy storage: %v", err)
	}
	if err := os.Symlink(filepath.Join(filepath.Dir(spec.HomeDir), "desk-legacy-main-box"), deskLegacyMainBox); err != nil {
		t.Fatalf("symlink desk legacy main box: %v", err)
	}
	if err := WriteManifest(manifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.human_link.main_box_loomadmin.missing")
	if finding.RepairID != RepairMainHumanLinks {
		t.Fatalf("repair = %q", finding.RepairID)
	}

	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairMainHumanLinks},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Fatalf("expected only canonical main Box link changes, got %#v skipped=%#v", result.Changed, result.Skipped)
	}
	for _, home := range []string{spec.HomeDir, loomdeskHome} {
		storageLink := filepath.Join(home, box.DefaultStorageLinkName)
		if _, err := os.Lstat(storageLink); !os.IsNotExist(err) {
			t.Fatalf("retired loom-storage link %s must not be created, err=%v", storageLink, err)
		}
		mainBoxLink := filepath.Join(home, box.DefaultMainBoxLinkName)
		if got, err := os.Readlink(mainBoxLink); err != nil || got != spec.BoxPath {
			t.Fatalf("loom-main-box link %s = %q err=%v, want %s", mainBoxLink, got, err, spec.BoxPath)
		}
		legacyStorage := filepath.Join(home, box.LegacyStorageLinkName)
		if _, err := os.Lstat(legacyStorage); err != nil {
			t.Fatalf("legacy storage evidence %s should remain untouched: %v", legacyStorage, err)
		}
		legacyMainBox := filepath.Join(home, box.LegacyMainBoxLinkName)
		if _, err := os.Lstat(legacyMainBox); err != nil {
			t.Fatalf("ordinary repair must preserve cleanup candidate %s: %v", legacyMainBox, err)
		}
	}
	if _, err := os.Lstat(adminQuoted); err != nil {
		t.Fatalf("ordinary repair must preserve quoted cleanup candidate: %v", err)
	}
	if _, err := os.Lstat(deskBroken); err != nil {
		t.Fatalf("ordinary repair must preserve loomdesk cleanup candidate: %v", err)
	}
}

func TestRepairMainHumanLinksIgnoresNonEmptyCleanupCandidate(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := os.MkdirAll(spec.StorageExportRoot, 0o755); err != nil {
		t.Fatalf("mkdir storage export: %v", err)
	}
	adminQuoted := filepath.Join(spec.HomeDir, "'LOOM BOX'")
	if err := os.MkdirAll(adminQuoted, 0o755); err != nil {
		t.Fatalf("mkdir obsolete dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(adminQuoted, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	if err := WriteManifest(manifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairMainHumanLinks},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	foundCleanupChange := false
	for _, change := range result.Changed {
		if change.Path == adminQuoted {
			foundCleanupChange = true
		}
	}
	if foundCleanupChange {
		t.Fatalf("ordinary repair must not act on cleanup candidates, changed=%#v skipped=%#v", result.Changed, result.Skipped)
	}
	if _, err := os.Stat(filepath.Join(adminQuoted, "keep.txt")); err != nil {
		t.Fatalf("non-empty obsolete directory should be preserved: %v", err)
	}
}

func TestRepairMainHumanLinksLeavesLegacyStorageArtifactUntouched(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := os.MkdirAll(spec.StorageExportRoot, 0o755); err != nil {
		t.Fatalf("mkdir storage export: %v", err)
	}
	legacyStorage := filepath.Join(spec.HomeDir, box.LegacyStorageLinkName)
	if err := os.MkdirAll(legacyStorage, 0o755); err != nil {
		t.Fatalf("mkdir legacy storage dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyStorage, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	if err := WriteManifest(manifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	result, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow},
		FixIDs:      []string{RepairMainHumanLinks},
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	foundMutation := false
	for _, change := range result.Changed {
		if change.Path == legacyStorage {
			foundMutation = true
		}
	}
	if foundMutation {
		t.Fatalf("repair must not mutate retired storage evidence, changed=%#v skipped=%#v", result.Changed, result.Skipped)
	}
	if _, err := os.Stat(filepath.Join(legacyStorage, "keep.txt")); err != nil {
		t.Fatalf("non-empty legacy storage directory should be preserved: %v", err)
	}
}
