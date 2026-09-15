package box

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestInitCreatesCanonicalBoxAndIsIdempotent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := Resolved{
		RootPath:      root,
		PathSource:    "test",
		Profile:       ProfileWorkspace,
		ProfileSource: "test",
		OwnerNode:     "macbook",
		NodeRole:      "workspace",
	}
	result, err := Init(InitInput{
		Resolved: resolved,
		Now:      func() time.Time { return time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC) },
		NewBoxID: func() string { return "box_test" },
	})
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if result.DryRun || result.StatusAfter.State != "ok" {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Documents",
		DefaultLaneDirName,
		".loom",
		".loom/policies",
		".loom/contracts",
		DefaultBackupContractsRelDir,
	} {
		assertDir(t, root, rel)
	}
	for _, rel := range []string{
		"README.md",
		".loom/README.md",
		"Projects/README.md",
		"Notes/README.md",
		"Notes/AGENTS.md",
		"Documents/README.md",
		"Documents/AGENTS.md",
		DefaultLaneDirName + "/README.md",
		".loom/box.yaml",
		".loom/policies/notes.watch.yaml",
		".loom/policies/documents.watch.yaml",
		".loom/policies/lane.transfer.yaml",
	} {
		assertFile(t, root, rel)
	}
	if _, err := os.Lstat(filepath.Join(root, LegacyLaneDirName)); !os.IsNotExist(err) {
		t.Fatalf("init must not create legacy Lane alias, stat err=%v", err)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("LoadContract after init: %v", err)
	}
	if contract.BoxID != "box_test" || contract.Profile != ProfileWorkspace {
		t.Fatalf("unexpected contract: %#v", contract)
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		t.Fatalf("workspace contract must omit Dropzone: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		t.Fatalf("workspace contract must omit Dropzone policy: %#v", contract.Policies)
	}
	if contract.Areas[AreaLane].Path != DefaultLaneDirName || !contract.Areas[AreaLane].Enabled {
		t.Fatalf("lane area not initialized: %#v", contract.Areas[AreaLane])
	}
	if contract.Policies[AreaLane] != ".loom/policies/lane.transfer.yaml" {
		t.Fatalf("lane policy not initialized: %#v", contract.Policies)
	}
	if contract.Policies[PolicyBackupContracts] != DefaultBackupContractsRelDir {
		t.Fatalf("backup contract pointer not initialized: %#v", contract.Policies)
	}
	for _, rel := range []string{".loom/README.md", ".loom/policies/lane.transfer.yaml"} {
		payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read fresh Box material %s: %v", rel, err)
		}
		if strings.Contains(string(payload), ".loom/state") {
			t.Fatalf("fresh Box material %s advertises visible runtime state:\n%s", rel, payload)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".loom", "state")); !os.IsNotExist(err) {
		t.Fatalf("fresh workspace Box created visible runtime state: %v", err)
	}
	for _, rel := range []string{"Dropzone", ".loom/policies/dropzone.transfer.yaml"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("fresh workspace Box must not create %s, stat err=%v", rel, err)
		}
	}
	rootReadme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read root README: %v", err)
	}
	if strings.Contains(string(rootReadme), "Dropzone") {
		t.Fatalf("fresh workspace README advertises retired Dropzone:\n%s", rootReadme)
	}
	if !strings.Contains(string(rootReadme), "`loom-lane/`") {
		t.Fatalf("fresh workspace README does not advertise the canonical Lane path:\n%s", rootReadme)
	}
	laneReadme, err := os.ReadFile(filepath.Join(root, DefaultLaneDirName, "README.md"))
	if err != nil {
		t.Fatalf("read fresh Lane README: %v", err)
	}
	if !strings.Contains(string(laneReadme), "# loom-lane") || !strings.Contains(string(laneReadme), "`loom-lane/`") {
		t.Fatalf("fresh Lane README does not use the canonical Lane path:\n%s", laneReadme)
	}
	lanePolicy := loadGeneratedLanePolicy(t, root)
	if lanePolicy.Path != DefaultLaneDirName {
		t.Fatalf("fresh Lane policy path = %q, want %q", lanePolicy.Path, DefaultLaneDirName)
	}
	if !strings.HasPrefix(lanePolicy.Notes, DefaultLaneDirName+" is explicit user-triggered transfer.") {
		t.Fatalf("fresh Lane policy notes do not use the canonical Lane path: %q", lanePolicy.Notes)
	}

	second, err := Init(InitInput{Resolved: resolved})
	if err != nil {
		t.Fatalf("second Init returned error: %v", err)
	}
	if len(second.CreatedDirs) != 0 || len(second.CreatedFiles) != 0 {
		t.Fatalf("second init should be idempotent, got created dirs=%v files=%v", second.CreatedDirs, second.CreatedFiles)
	}
	if len(second.SkippedDirs) == 0 || len(second.SkippedFiles) == 0 {
		t.Fatalf("second init should report existing paths: %#v", second)
	}
}

func TestInitRebindsMovedBoxContractToSelectedRoot(t *testing.T) {
	parent := t.TempDir()
	legacyRoot := filepath.Join(parent, "legacy-box")
	canonicalRoot := filepath.Join(parent, "canonical-box")
	legacyResolved := Resolved{
		RootPath:  legacyRoot,
		Profile:   ProfileMain,
		OwnerNode: "main",
	}
	if _, err := Init(InitInput{
		Resolved: legacyResolved,
		Now:      func() time.Time { return time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC) },
		NewBoxID: func() string { return "box_moved" },
	}); err != nil {
		t.Fatalf("initialize legacy Box: %v", err)
	}
	if err := os.Rename(legacyRoot, canonicalRoot); err != nil {
		t.Fatalf("move Box root: %v", err)
	}

	canonicalResolved := legacyResolved
	canonicalResolved.RootPath = canonicalRoot
	result, err := Init(InitInput{
		Resolved: canonicalResolved,
		Now:      func() time.Time { return time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("rebind moved Box: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("rebound Box status = %q, diagnostics=%#v", result.StatusAfter.State, result.StatusAfter.Diagnostics)
	}
	if len(result.BackupFiles) != 1 {
		t.Fatalf("expected contract backup before root rebind, got %#v", result.BackupFiles)
	}
	contract, err := LoadContract(filepath.Join(canonicalRoot, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("load rebound contract: %v", err)
	}
	if contract.RootPath != canonicalRoot || contract.BoxID != "box_moved" {
		t.Fatalf("unexpected rebound contract: %#v", contract)
	}
	if contract.Metadata.UpdatedAt != "2026-08-28T05:00:00Z" {
		t.Fatalf("root rebind did not update metadata: %#v", contract.Metadata)
	}
	if _, err := os.Stat(legacyRoot); !os.IsNotExist(err) {
		t.Fatalf("root rebind recreated legacy path: %v", err)
	}
}

func TestNextAvailableBackupPathFailsOnUninspectableParent(t *testing.T) {
	blockingParent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockingParent, []byte("blocked\n"), 0o600); err != nil {
		t.Fatalf("write blocking parent: %v", err)
	}
	if _, err := nextAvailableBackupPath(filepath.Join(blockingParent, "box.yaml.bak")); err == nil {
		t.Fatal("expected an error for a backup path below a non-directory parent")
	}
}

func TestInitFreshMainBoxUsesActiveWorkspaceTopology(t *testing.T) {
	root := filepath.Join(t.TempDir(), "main-box")
	result, err := Init(InitInput{
		Resolved: Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "main", RuntimeStateRoot: filepath.Join(t.TempDir(), "box-state")},
		NewBoxID: func() string { return "box_main_test" },
	})
	if err != nil {
		t.Fatalf("Init main Box returned error: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("main Box status unexpected: %#v", result.StatusAfter)
	}
	if result.StatusAfter.LaneState != "" || result.StatusAfter.Lane != nil || result.StatusAfter.DropzoneState != "" || result.StatusAfter.DropzoneTransfers != nil {
		t.Fatalf("fresh main status retained retired intake state: %#v", result.StatusAfter)
	}
	for _, rel := range []string{"Topics", "Library", "Topics/README.md", "Library/README.md"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("fresh main Box missing %s: %v", rel, err)
		}
	}
	for _, path := range []string{
		filepath.Join(root, DefaultLaneDirName),
		filepath.Join(root, "Dropzone"),
		filepath.Join(root, ".loom", "state"),
		filepath.Join(root, ".loom", "policies", "lane.transfer.yaml"),
		filepath.Join(root, ".loom", "policies", "dropzone.transfer.yaml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("fresh main Box must omit %s, stat err=%v", path, err)
		}
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("load fresh main contract: %v", err)
	}
	assertCurrentProfileTopology(t, contract)
}

func TestInitUpgradesLegacyBoxWithBackupContractDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Documents",
		DefaultLaneDirName,
		"Dropzone",
		".loom/policies",
		".loom/state/dropzone",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}
	legacyDropzoneFile := filepath.Join(root, "Dropzone", "keep.txt")
	if err := os.WriteFile(legacyDropzoneFile, []byte("keep legacy dropzone content\n"), 0o644); err != nil {
		t.Fatalf("write legacy Dropzone content: %v", err)
	}
	legacyContract := `schema_version: loom.box.v0.4.1
box_id: box_legacy_backup_contracts
owner_node: macbook
profile: workspace
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  documents:
    path: Documents
    enabled: true
  lane:
    path: loom-lane
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
    transfer_status: scaffolded_for_v0.4.2
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  documents: .loom/policies/documents.watch.yaml
  lane: .loom/policies/lane.transfer.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
metadata:
  created_at: "2026-05-30T12:00:00Z"
  updated_at: "2026-05-30T12:00:00Z"
  created_by: loom box init
`
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacyContract), 0o644); err != nil {
		t.Fatalf("write legacy contract: %v", err)
	}

	result, err := Init(InitInput{
		Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"},
		Now:      func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	assertDir(t, root, DefaultBackupContractsRelDir)
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("LoadContract after upgrade: %v", err)
	}
	if contract.Policies[PolicyBackupContracts] != DefaultBackupContractsRelDir {
		t.Fatalf("backup contract pointer missing after upgrade: %#v", contract.Policies)
	}
	assertCurrentProfileTopology(t, contract)
	if payload, err := os.ReadFile(legacyDropzoneFile); err != nil || string(payload) != "keep legacy dropzone content\n" {
		t.Fatalf("repair changed legacy Dropzone content: payload=%q err=%v", payload, err)
	}
	if contract.Metadata.UpdatedAt != "2026-07-07T12:00:00Z" {
		t.Fatalf("updated_at was not refreshed: %#v", contract.Metadata)
	}
	if len(result.BackupFiles) != 1 {
		t.Fatalf("expected contract backup before upgrade, got %#v", result.BackupFiles)
	}
}

func TestInitLeavesLegacyDropzonePolicyUntouchedOutsideCurrentContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := Resolved{
		RootPath:  root,
		Profile:   ProfileWorkspace,
		OwnerNode: "macbook",
	}
	if _, err := Init(InitInput{Resolved: resolved, NewBoxID: func() string { return "box_test" }}); err != nil {
		t.Fatalf("initial Init returned error: %v", err)
	}
	policyPath := filepath.Join(root, ".loom", "policies", "dropzone.transfer.yaml")
	legacyPolicy := `schema_version: loom.box.transfer_policy.v0.4.1
area: dropzone
path: Dropzone
enabled: false
mode: custody_transfer
runtime_status: scaffolded_for_v0.4.2
target: main
status_dir: .loom/state/dropzone
automatic_delete_after_safe: false
delete_semantics: main_becomes_authoritative_after_verified_commit
`
	if err := os.WriteFile(policyPath, []byte(legacyPolicy), 0o644); err != nil {
		t.Fatalf("write legacy policy: %v", err)
	}

	result, err := Init(InitInput{
		Resolved: resolved,
		Now:      func() time.Time { return time.Date(2026, 6, 7, 13, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("upgrade Init returned error: %v", err)
	}
	if len(result.BackupFiles) != 0 {
		t.Fatalf("current contract must not update or back up an orphaned Dropzone policy: %#v", result.BackupFiles)
	}
	payload, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read upgraded policy: %v", err)
	}
	if string(payload) != legacyPolicy {
		t.Fatalf("repair rewrote legacy Dropzone policy:\n%s", payload)
	}
}

func TestInitRepairsLegacyWorkspaceWithoutDuplicatingVisibleLane(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	for _, rel := range []string{"Projects", "Notes", "Documents", LegacyLaneDirName, "Dropzone", ".loom/policies"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", rel, err)
		}
	}
	legacyLaneFile := filepath.Join(root, LegacyLaneDirName, "pending.dat")
	legacyDropzoneFile := filepath.Join(root, "Dropzone", "pending.dat")
	for path, content := range map[string]string{
		legacyLaneFile:     "keep pending Lane content\n",
		legacyDropzoneFile: "keep pending Dropzone content\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write legacy content %s: %v", path, err)
		}
	}
	legacyContract := strings.Replace(legacyContractPayloadForRoot(root, ProfileWorkspace), "path: loom-lane", "path: LOOM Lane", 1)
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacyContract), 0o644); err != nil {
		t.Fatalf("write legacy workspace contract: %v", err)
	}

	result, err := Init(InitInput{
		Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"},
		Now:      func() time.Time { return time.Date(2026, 8, 30, 12, 30, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("repair legacy workspace Box: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("repaired legacy workspace status = %#v", result.StatusAfter)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("load repaired workspace contract: %v", err)
	}
	assertCurrentProfileTopology(t, contract)
	if contract.Areas[AreaLane].Path != LegacyLaneDirName {
		t.Fatalf("repair changed the existing legacy Lane path: %#v", contract.Areas[AreaLane])
	}
	if _, err := os.Stat(filepath.Join(root, DefaultLaneDirName)); !os.IsNotExist(err) {
		t.Fatalf("repair duplicated legacy Lane as %s, stat err=%v", DefaultLaneDirName, err)
	}
	generatedLaneMaterial := map[string][]string{
		"README.md": {"`LOOM Lane/`"},
		filepath.Join(LegacyLaneDirName, "README.md"): {"# LOOM Lane", "`LOOM Lane/`"},
	}
	for rel, expected := range generatedLaneMaterial {
		payload, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil {
			t.Fatalf("read repaired legacy Lane material %s: %v", rel, readErr)
		}
		for _, want := range expected {
			if !strings.Contains(string(payload), want) {
				t.Fatalf("repaired legacy Lane material %s does not reference retained path %q:\n%s", rel, want, payload)
			}
		}
		if strings.Contains(string(payload), "loom-lane") {
			t.Fatalf("repaired legacy Lane material %s advertises a second Lane path:\n%s", rel, payload)
		}
	}
	lanePolicy := loadGeneratedLanePolicy(t, root)
	if lanePolicy.Path != LegacyLaneDirName {
		t.Fatalf("repaired legacy Lane policy path = %q, want %q", lanePolicy.Path, LegacyLaneDirName)
	}
	wantLaneNotes := LegacyLaneDirName + " is explicit user-triggered transfer. Slice 09 only scans pending files and preflights rsync/SSH; Slice 10 implements send-to-main."
	if lanePolicy.Notes != wantLaneNotes {
		t.Fatalf("repaired legacy Lane policy notes = %q, want %q", lanePolicy.Notes, wantLaneNotes)
	}
	for path, want := range map[string]string{
		legacyLaneFile:     "keep pending Lane content\n",
		legacyDropzoneFile: "keep pending Dropzone content\n",
	} {
		payload, readErr := os.ReadFile(path)
		if readErr != nil || string(payload) != want {
			t.Fatalf("repair changed legacy content %s: payload=%q err=%v", path, payload, readErr)
		}
	}
}

func TestInitRepairsYAMLSignificantLegacyLanePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	lanePath := "Lane: legacy"
	for _, rel := range []string{"Projects", "Notes", "Documents", lanePath, "Dropzone", ".loom/policies"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", rel, err)
		}
	}
	legacyLaneFile := filepath.Join(root, lanePath, "pending.dat")
	if err := os.WriteFile(legacyLaneFile, []byte("keep YAML-significant Lane content\n"), 0o644); err != nil {
		t.Fatalf("write legacy Lane content: %v", err)
	}
	legacyContract := strings.Replace(
		legacyContractPayloadForRoot(root, ProfileWorkspace),
		"path: loom-lane",
		`path: "Lane: legacy"`,
		1,
	)
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacyContract), 0o644); err != nil {
		t.Fatalf("write YAML-significant legacy workspace contract: %v", err)
	}

	result, err := Init(InitInput{
		Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"},
		Now:      func() time.Time { return time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("repair YAML-significant legacy workspace Box: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("repaired YAML-significant workspace status = %#v", result.StatusAfter)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("load repaired YAML-significant workspace contract: %v", err)
	}
	if contract.Areas[AreaLane].Path != lanePath {
		t.Fatalf("repair changed YAML-significant Lane path: %#v", contract.Areas[AreaLane])
	}
	assertFile(t, root, filepath.Join(lanePath, "README.md"))
	if _, err := os.Stat(filepath.Join(root, DefaultLaneDirName)); !os.IsNotExist(err) {
		t.Fatalf("repair duplicated YAML-significant Lane as %s, stat err=%v", DefaultLaneDirName, err)
	}
	policy := loadGeneratedLanePolicy(t, root)
	if policy.Path != lanePath {
		t.Fatalf("generated Lane policy path = %q, want %q", policy.Path, lanePath)
	}
	wantNotes := lanePath + " is explicit user-triggered transfer. Slice 09 only scans pending files and preflights rsync/SSH; Slice 10 implements send-to-main."
	if policy.Notes != wantNotes {
		t.Fatalf("generated Lane policy notes = %q, want %q", policy.Notes, wantNotes)
	}
	payload, err := os.ReadFile(legacyLaneFile)
	if err != nil || string(payload) != "keep YAML-significant Lane content\n" {
		t.Fatalf("repair changed YAML-significant Lane content: payload=%q err=%v", payload, err)
	}
}

func TestInitDryRunDoesNotCreateFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}
	result, err := Init(InitInput{Resolved: resolved, DryRun: true, NewBoxID: func() string { return "box_test" }})
	if err != nil {
		t.Fatalf("dry-run Init returned error: %v", err)
	}
	if !result.DryRun || len(result.PlannedDirs) == 0 || len(result.PlannedFiles) == 0 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create root, stat err=%v", err)
	}
}

func TestInitPreservesExistingUserFilesAndCustomDocs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	userFiles := []string{
		filepath.Join("Projects", "user-project.md"),
		filepath.Join("Notes", "user-note.md"),
		filepath.Join("Documents", "user-doc.pdf"),
		filepath.Join(DefaultLaneDirName, "user-large-transfer.dat"),
		filepath.Join("Dropzone", "user-drop.dat"),
	}
	for _, rel := range userFiles {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("keep me\n"), 0o644); err != nil {
			t.Fatalf("write user file %s: %v", rel, err)
		}
	}
	customReadme := filepath.Join(root, "README.md")
	if err := os.WriteFile(customReadme, []byte("custom readme\n"), 0o644); err != nil {
		t.Fatalf("write custom readme: %v", err)
	}
	customAgents := map[string]string{
		filepath.Join(root, "Notes", "AGENTS.md"):     "custom notes guidance\n",
		filepath.Join(root, "Documents", "AGENTS.md"): "custom documents guidance\n",
	}
	for path, content := range customAgents {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write custom agent guidance: %v", err)
		}
	}

	resolved := Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}
	if _, err := Init(InitInput{Resolved: resolved, NewBoxID: func() string { return "box_test" }}); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	for _, rel := range userFiles {
		payload, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read user file %s: %v", rel, err)
		}
		if string(payload) != "keep me\n" {
			t.Fatalf("user file %s was changed: %q", rel, payload)
		}
	}
	payload, err := os.ReadFile(customReadme)
	if err != nil {
		t.Fatalf("read custom readme: %v", err)
	}
	if string(payload) != "custom readme\n" {
		t.Fatalf("custom README was overwritten: %q", payload)
	}
	for path, want := range customAgents {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read custom agent guidance: %v", err)
		}
		if string(payload) != want {
			t.Fatalf("custom agent guidance was overwritten at %s: %q", path, payload)
		}
	}
}

func TestInitUpgradesLegacyLaunchpadBoxToDocuments(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Launchpad",
		"Dropzone",
		".loom/policies",
		".loom/state/dropzone",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Launchpad", "legacy.txt"), []byte("keep legacy\n"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	legacyContract := `schema_version: loom.box.v0.4.1
box_id: box_legacy
owner_node: macbook
profile: workspace
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  launchpad:
    path: Launchpad
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
metadata:
  created_at: "2026-05-30T12:00:00Z"
  updated_at: "2026-05-30T12:00:00Z"
  created_by: loom box init
`
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacyContract), 0o644); err != nil {
		t.Fatalf("write legacy contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "policies", "launchpad.watch.yaml"), []byte(launchpadPolicy(Contract{})), 0o644); err != nil {
		t.Fatalf("write legacy launchpad policy: %v", err)
	}
	resolved := Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}
	result, err := Init(InitInput{
		Resolved: resolved,
		Now:      func() time.Time { return time.Date(2026, 6, 7, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if _, ok := result.StatusBefore.Contract.Areas[AreaDocuments]; ok {
		t.Fatalf("status_before should show the legacy contract before migration: %#v", result.StatusBefore.Contract.Areas)
	}
	for _, rel := range []string{"Documents", "Documents/README.md", DefaultLaneDirName, DefaultLaneDirName + "/README.md", ".loom/policies/documents.watch.yaml", ".loom/policies/lane.transfer.yaml"} {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migrated path %s: %v", rel, err)
		}
	}
	if len(result.BackupFiles) != 1 {
		t.Fatalf("expected contract backup before migration, got %#v", result.BackupFiles)
	}
	backupPayload, err := os.ReadFile(result.BackupFiles[0])
	if err != nil {
		t.Fatalf("read contract backup: %v", err)
	}
	if !strings.Contains(string(backupPayload), "schema_version: loom.box.v0.4.1") || !strings.Contains(string(backupPayload), "launchpad:") {
		t.Fatalf("contract backup does not preserve legacy body:\n%s", backupPayload)
	}
	if payload, err := os.ReadFile(filepath.Join(root, "Launchpad", "legacy.txt")); err != nil || string(payload) != "keep legacy\n" {
		t.Fatalf("legacy Launchpad file was not preserved: payload=%q err=%v", payload, err)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("LoadContract after migration: %v", err)
	}
	if contract.BoxID != "box_legacy" {
		t.Fatalf("box id changed during migration: %#v", contract)
	}
	if _, ok := contract.Areas[AreaDocuments]; !ok {
		t.Fatalf("documents area missing after migration: %#v", contract.Areas)
	}
	if _, ok := contract.Areas[AreaLaunchpad]; !ok {
		t.Fatalf("legacy launchpad area should be preserved: %#v", contract.Areas)
	}
	if contract.Policies[AreaDocuments] != ".loom/policies/documents.watch.yaml" {
		t.Fatalf("documents policy missing after migration: %#v", contract.Policies)
	}
	if contract.Areas[AreaLane].Path != DefaultLaneDirName || contract.Policies[AreaLane] != ".loom/policies/lane.transfer.yaml" {
		t.Fatalf("lane area/policy missing after migration: areas=%#v policies=%#v", contract.Areas, contract.Policies)
	}
	if _, ok := contract.Areas[AreaDropzone]; ok {
		t.Fatalf("repaired legacy workspace contract retained Dropzone: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		t.Fatalf("repaired legacy workspace contract retained Dropzone policy: %#v", contract.Policies)
	}
	if contract.Metadata.UpdatedAt != "2026-06-07T10:00:00Z" {
		t.Fatalf("updated_at was not refreshed: %#v", contract.Metadata)
	}
	if len(result.CreatedDirs) == 0 || len(result.CreatedFiles) == 0 {
		t.Fatalf("migration should create Documents paths: %#v", result)
	}

	second, err := Init(InitInput{Resolved: resolved})
	if err != nil {
		t.Fatalf("second Init returned error: %v", err)
	}
	if len(second.CreatedDirs) != 0 || len(second.CreatedFiles) != 0 {
		t.Fatalf("second migration should be idempotent, got dirs=%v files=%v", second.CreatedDirs, second.CreatedFiles)
	}
}

func TestInitDropsMissingLegacyLaunchpadDuringDocumentsMigration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Dropzone",
		".loom/policies",
		".loom/state/dropzone",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
	}
	legacyContract := `schema_version: loom.box.v0.4.1
box_id: box_legacy_missing_launchpad
owner_node: macbook
profile: workspace
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  launchpad:
    path: Launchpad
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
metadata:
  created_at: "2026-05-30T12:00:00Z"
  updated_at: "2026-05-30T12:00:00Z"
  created_by: loom box init
`
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacyContract), 0o644); err != nil {
		t.Fatalf("write legacy contract: %v", err)
	}

	resolved := Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}
	result, err := Init(InitInput{
		Resolved: resolved,
		Now:      func() time.Time { return time.Date(2026, 6, 7, 10, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("missing legacy Launchpad should not keep Box partial: %#v", result.StatusAfter)
	}
	if _, err := os.Stat(filepath.Join(root, "Launchpad")); !os.IsNotExist(err) {
		t.Fatalf("missing legacy Launchpad should not be recreated, stat err=%v", err)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("LoadContract after migration: %v", err)
	}
	if _, ok := contract.Areas[AreaLaunchpad]; ok {
		t.Fatalf("missing legacy launchpad area should be dropped: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaLaunchpad]; ok {
		t.Fatalf("missing legacy launchpad policy should be dropped: %#v", contract.Policies)
	}
	if _, ok := contract.Areas[AreaDocuments]; !ok || contract.Policies[AreaDocuments] == "" {
		t.Fatalf("documents migration missing: areas=%#v policies=%#v", contract.Areas, contract.Policies)
	}
	if len(result.BackupFiles) != 1 {
		t.Fatalf("expected contract backup before migration, got %#v", result.BackupFiles)
	}
}

func TestInitFailsWhenDirectoryBlockedByFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "Notes"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}
	result, err := Init(InitInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err == nil {
		t.Fatal("expected blocked directory error")
	}
	if len(result.Diagnostics) == 0 || result.Diagnostics[0].Code != "box.directory_blocked" {
		t.Fatalf("expected directory blocked diagnostic, got %#v", result.Diagnostics)
	}
}

func TestInitRepairsLegacyMainTopologyWithoutDeletingIntakeContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	resolved := Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "main"}
	for _, rel := range []string{"Projects", "Notes", "Documents", DefaultLaneDirName, "Dropzone", ".loom/policies"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", rel, err)
		}
	}
	for rel, payload := range map[string]string{
		filepath.Join(DefaultLaneDirName, "pending.dat"):             "pending lane content\n",
		filepath.Join("Dropzone", "pending-dropzone.dat"):            "pending dropzone content\n",
		filepath.Join(".loom", "policies", "lane.transfer.yaml"):     "legacy lane policy\n",
		filepath.Join(".loom", "policies", "dropzone.transfer.yaml"): "legacy dropzone policy\n",
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(payload), 0o644); err != nil {
			t.Fatalf("write legacy %s: %v", rel, err)
		}
	}
	legacy := legacyContractPayloadForRoot(root, ProfileMain)
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy main contract: %v", err)
	}
	result, err := Init(InitInput{Resolved: resolved, Now: func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatalf("repair legacy main Box: %v", err)
	}
	if result.StatusAfter.State != "ok" {
		t.Fatalf("repaired main status = %#v", result.StatusAfter)
	}
	contract, err := LoadContract(filepath.Join(root, ".loom", "box.yaml"))
	if err != nil {
		t.Fatalf("LoadContract: %v", err)
	}
	assertCurrentProfileTopology(t, contract)
	for rel, want := range map[string]string{
		filepath.Join(DefaultLaneDirName, "pending.dat"):             "pending lane content\n",
		filepath.Join("Dropzone", "pending-dropzone.dat"):            "pending dropzone content\n",
		filepath.Join(".loom", "policies", "lane.transfer.yaml"):     "legacy lane policy\n",
		filepath.Join(".loom", "policies", "dropzone.transfer.yaml"): "legacy dropzone policy\n",
	} {
		payload, readErr := os.ReadFile(filepath.Join(root, rel))
		if readErr != nil || string(payload) != want {
			t.Fatalf("repair changed legacy %s: payload=%q err=%v", rel, payload, readErr)
		}
	}
}

func legacyContractPayloadForRoot(root, profile string) string {
	dropzoneStatus := DropzoneTransferFuture
	if profile == ProfileMain {
		dropzoneStatus = DropzoneTransferInactive
	}
	return `schema_version: loom.box.v0.4.1
box_id: box_legacy_topology
owner_node: main
profile: ` + profile + `
root_path: ` + root + `
areas:
  projects:
    path: Projects
    enabled: true
  notes:
    path: Notes
    enabled: true
  documents:
    path: Documents
    enabled: true
  lane:
    path: loom-lane
    enabled: true
  dropzone:
    path: Dropzone
    enabled: false
    transfer_status: ` + dropzoneStatus + `
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  documents: .loom/policies/documents.watch.yaml
  lane: .loom/policies/lane.transfer.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
`
}

func assertCurrentProfileTopology(t *testing.T, contract Contract) {
	t.Helper()
	if _, ok := contract.Areas[AreaDropzone]; ok {
		t.Fatalf("current contract retained Dropzone: %#v", contract.Areas)
	}
	if _, ok := contract.Policies[AreaDropzone]; ok {
		t.Fatalf("current contract retained Dropzone policy: %#v", contract.Policies)
	}
	if contract.Profile == ProfileMain {
		if _, ok := contract.Areas[AreaLane]; ok {
			t.Fatalf("current main contract retained Lane: %#v", contract.Areas)
		}
		if _, ok := contract.Policies[AreaLane]; ok {
			t.Fatalf("current main contract retained Lane policy: %#v", contract.Policies)
		}
		if contract.Areas[areaTopics].Path != "Topics" || contract.Areas[areaLibrary].Path != "Library" {
			t.Fatalf("current main contract missing Topics/Library: %#v", contract.Areas)
		}
		return
	}
	laneArea, ok := contract.Areas[AreaLane]
	if !ok || !laneArea.Enabled || laneArea.Path == "" || contract.Policies[AreaLane] == "" {
		t.Fatalf("current workspace contract missing enabled Lane: areas=%#v policies=%#v", contract.Areas, contract.Policies)
	}
}

type generatedLanePolicyFixture struct {
	SchemaVersion       string `yaml:"schema_version"`
	Area                string `yaml:"area"`
	Path                string `yaml:"path"`
	Enabled             bool   `yaml:"enabled"`
	Mode                string `yaml:"mode"`
	RuntimeStatus       string `yaml:"runtime_status"`
	Target              string `yaml:"target"`
	TargetMainNode      string `yaml:"target_main_node"`
	DestinationTemplate string `yaml:"destination_template"`
	Engine              string `yaml:"engine"`
	Preflight           struct {
		RequireRsync bool   `yaml:"require_rsync"`
		RequireSSH   bool   `yaml:"require_ssh"`
		MainHost     string `yaml:"main_host"`
	} `yaml:"preflight"`
	SettleDuration  string   `yaml:"settle_duration"`
	IgnorePatterns  []string `yaml:"ignore_patterns"`
	LocalSafetyCopy struct {
		Enabled          bool   `yaml:"enabled"`
		DefaultRetention string `yaml:"default_retention"`
	} `yaml:"local_safety_copy"`
	DeleteSemantics string `yaml:"delete_semantics"`
	Notes           string `yaml:"notes"`
}

func loadGeneratedLanePolicy(t *testing.T, root string) generatedLanePolicyFixture {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, ".loom", "policies", "lane.transfer.yaml"))
	if err != nil {
		t.Fatalf("read generated Lane policy: %v", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	var policy generatedLanePolicyFixture
	if err := decoder.Decode(&policy); err != nil {
		t.Fatalf("strictly decode generated Lane policy: %v\n%s", err, payload)
	}
	return policy
}

func assertDir(t *testing.T, root string, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", rel)
	}
}

func assertFile(t *testing.T, root string, rel string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat %s: %v", rel, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s is not a regular file", rel)
	}
}
