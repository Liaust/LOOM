package box

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
	loomsync "loom.local/loom/internal/sync"
)

func TestBoxKnowledgeSourcePoliciesExplicitOptIn(t *testing.T) {
	root := initializedWatchBox(t, ProfileMain)
	resolved := Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "macbook"}
	before, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	if err != nil {
		t.Fatal(err)
	}
	contract := *Inspect(resolved).Contract
	for _, area := range []string{areaTopics, areaLibrary} {
		contract.Policies[area] = ".loom/policies/" + area + ".watch.yaml"
		policy := "schema_version: loom.box.watch_policy.v0.6\narea: " + area + "\nmode: watched_root\nenabled: true\ntext:\n  enabled: true\n"
		if err := os.WriteFile(filepath.Join(root, contract.Policies[area]), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := yaml.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, contractRelPath), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := BuildWatchPlan(WatchStatusInput{Resolved: resolved})
	if err != nil {
		t.Fatalf("%v: %#v", err, after.Diagnostics)
	}
	if len(after.WatchedRoots) != len(before.WatchedRoots)+2 {
		t.Fatalf("unexpected roots: %#v", after.WatchedRoots)
	}
	for _, old := range before.WatchedRoots {
		found := false
		for _, item := range after.WatchedRoots {
			if item.BackendRootKey == old.BackendRootKey {
				found = true
				if string(item.ConfigJSON) != string(old.ConfigJSON) {
					t.Fatalf("legacy config changed: %s", old.BackendRootKey)
				}
			}
		}
		if !found {
			t.Fatal("lost legacy root")
		}
	}
	for _, area := range []string{areaTopics, areaLibrary} {
		item, diagnostics := watchRootItemForArea(contract, area)
		if hasErrorDiagnostics(diagnostics) {
			t.Fatal(diagnostics)
		}
		decl := item.Metadata["knowledge_source"].(map[string]any)
		if decl["enabled"] != true || decl["root_kind"] != "box_"+area || item.SyncMode != agentwatchedroots.SyncModeSelectedFiles || item.BackupMode != agentwatchedroots.BackupModeNone {
			t.Fatalf("wrong opt-in: %#v", item)
		}
		for _, path := range []string{"Projects", "../outside"} {
			policy := "schema_version: loom.box.watch_policy.v0.6\narea: " + area + "\nmode: watched_root\nenabled: true\npath: " + path + "\n"
			if err := os.WriteFile(filepath.Join(root, contract.Policies[area]), []byte(policy), 0o600); err != nil {
				t.Fatal(err)
			}
			_, diagnostics := watchRootItemForArea(contract, area)
			if !hasErrorDiagnostics(diagnostics) {
				t.Fatalf("accepted area escape %s", path)
			}
		}
		policy := "schema_version: loom.box.watch_policy.v0.6\narea: " + area + "\nmode: watched_root\nenabled: true\nmetadata:\n  knowledge_source:\n    enabled: true\n"
		if err := os.WriteFile(filepath.Join(root, contract.Policies[area]), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
		item, diagnostics = watchRootItemForArea(contract, area)
		if hasErrorDiagnostics(diagnostics) || item.Metadata["knowledge_source"].(map[string]any)["enabled"] != false {
			t.Fatalf("free-form metadata enabled indexing: %#v %v", item, diagnostics)
		}
		policy = strings.Replace(policy, "enabled: true", "enabled: false", 1)
		if err := os.WriteFile(filepath.Join(root, contract.Policies[area]), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
		item, diagnostics = watchRootItemForArea(contract, area)
		if item.BackendRootKey != "" || hasErrorDiagnostics(diagnostics) {
			t.Fatalf("disabled policy emitted root: %#v", item)
		}
	}
}

func TestBuildWatchPlanIncludesNotesAndDocumentsOnly(t *testing.T) {
	root := initializedWatchBox(t, ProfileWorkspace)
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	if len(plan.WatchedRoots) != 2 {
		t.Fatalf("expected notes and documents watched roots, got %#v", plan.WatchedRoots)
	}
	keys := []string{plan.WatchedRoots[0].BackendRootKey, plan.WatchedRoots[1].BackendRootKey}
	if strings.Join(keys, ",") != "loom_box__documents,loom_box__notes" {
		t.Fatalf("watched root keys = %v", keys)
	}
	for _, root := range plan.WatchedRoots {
		if root.BackendRootKey == "loom_box__dropzone" || root.BackendRootKey == "loom_box__projects" {
			t.Fatalf("Box watch plan should exclude %s", root.BackendRootKey)
		}
		if root.SafeRootKey != "loom_box" {
			t.Fatalf("safe root = %q, want loom_box", root.SafeRootKey)
		}
		if root.ActivationStatus != BoxWatchStatusPendingAgentApply {
			t.Fatalf("activation status = %q", root.ActivationStatus)
		}
		if len(root.ConfigJSON) == 0 {
			t.Fatalf("missing config JSON for %#v", root)
		}
	}
	notes := plan.WatchedRoots[1]
	if notes.RootRelativePath != "Notes" || notes.SyncMode != agentwatchedroots.SyncModeSelectedFiles || notes.BackupMode != agentwatchedroots.BackupModeIncrementalRaw || notes.IndexMode != agentwatchedroots.IndexModeMarkdownText {
		t.Fatalf("unexpected notes root: %#v", notes)
	}
	documents := plan.WatchedRoots[0]
	if documents.RootRelativePath != "Documents" || documents.SyncMode != agentwatchedroots.SyncModeNone || documents.BackupMode != agentwatchedroots.BackupModeIncrementalRaw || documents.IndexMode != agentwatchedroots.IndexModeMetadataOnly {
		t.Fatalf("unexpected documents root: %#v", documents)
	}
	if len(plan.Excluded) != 2 || plan.Excluded[0].Area != AreaProjects || plan.Excluded[1].Area != AreaLane {
		t.Fatalf("expected current workspace projects and Lane exclusions: %#v", plan.Excluded)
	}
	var notesConfig agentwatchedroots.RootConfig
	if err := json.Unmarshal(notes.ConfigJSON, &notesConfig); err != nil {
		t.Fatalf("decode notes config: %v", err)
	}
	if notesConfig.Scan.FullRescanInterval != boxWatchFullRescanInterval {
		t.Fatalf("notes full_rescan_interval = %q, want %q", notesConfig.Scan.FullRescanInterval, boxWatchFullRescanInterval)
	}
	if notesConfig.IgnorePolicy.Profile != string(filepolicy.ProfileManaged) || !notesConfig.IgnorePolicy.DiscoverUserRules || notesConfig.IgnorePolicy.PolicyRootRelativePath != "." || notesConfig.Scan.HiddenPolicy != agentwatchedroots.HiddenPolicyPolicyControlled {
		t.Fatalf("notes ignore policy = %#v hidden=%q", notesConfig.IgnorePolicy, notesConfig.Scan.HiddenPolicy)
	}
	if len(notesConfig.Exclude) != 0 {
		t.Fatalf("managed defaults must not be expanded into Box excludes: %#v", notesConfig.Exclude)
	}
	if notesConfig.Scan.MaxHashFileBytes < filesystemconnector.DefaultMaxFileBytes {
		t.Fatalf("notes max_hash_file_bytes = %d, below safe-root default %d", notesConfig.Scan.MaxHashFileBytes, filesystemconnector.DefaultMaxFileBytes)
	}
	if strings.Join(notesConfig.Include, ",") != "**/*" {
		t.Fatalf("notes should broadly include files for backup/catalog, got %#v", notesConfig.Include)
	}
	if notesConfig.SyncPolicy.MaxFileBytes != int64(loomsync.MaxInlineObjectUploadBytes) {
		t.Fatalf("notes sync max_file_bytes = %d, want inline sync limit %d", notesConfig.SyncPolicy.MaxFileBytes, loomsync.MaxInlineObjectUploadBytes)
	}
	if notesConfig.BackupPolicy.MaxFileBytes != boxWatchNotesMaxFileBytes || notesConfig.BackupPolicy.MaxBatchBytes != boxWatchNotesMaxFileBytes {
		t.Fatalf("notes backup limits = %#v, want %d", notesConfig.BackupPolicy, boxWatchNotesMaxFileBytes)
	}
	var documentsConfig agentwatchedroots.RootConfig
	if err := json.Unmarshal(documents.ConfigJSON, &documentsConfig); err != nil {
		t.Fatalf("decode documents config: %v", err)
	}
	if documentsConfig.Scan.FullRescanInterval != boxWatchFullRescanInterval {
		t.Fatalf("documents full_rescan_interval = %q, want %q", documentsConfig.Scan.FullRescanInterval, boxWatchFullRescanInterval)
	}
	if documentsConfig.Scan.MaxHashFileBytes != boxWatchDocsMaxFileBytes {
		t.Fatalf("documents max_hash_file_bytes = %d, want %d", documentsConfig.Scan.MaxHashFileBytes, boxWatchDocsMaxFileBytes)
	}
	if documentsConfig.BackupPolicy.MaxFileBytes != boxWatchDocsMaxFileBytes || documentsConfig.BackupPolicy.MaxBatchBytes != boxWatchDocsMaxFileBytes {
		t.Fatalf("documents backup limits = %#v, want %d", documentsConfig.BackupPolicy, boxWatchDocsMaxFileBytes)
	}
}

func TestBoxWatchScopePlansUseStableBoxAreaScopeRefs(t *testing.T) {
	root := initializedWatchBoxWithID(t, ProfileWorkspace, "box_test")
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}

	scopes, err := boxWatchScopePlans(plan, nodes.Node{NodeID: "node_test", NodeKey: "macbook"})
	if err != nil {
		t.Fatalf("boxWatchScopePlans returned error: %v", err)
	}
	if len(scopes) != 1 {
		t.Fatalf("scope plans = %#v, want notes only because documents is backup/catalog-only", scopes)
	}
	if scopes[0].ScopeKey != "loom_box:box_test:notes" || scopes[0].Slug != "box-test-notes" {
		t.Fatalf("unexpected notes scope plan: %#v", scopes[0])
	}
}

func TestBuildWatchPlanIncludesActiveBackupContracts(t *testing.T) {
	root := initializedWatchBoxWithID(t, ProfileWorkspace, "box_test")
	if err := os.MkdirAll(filepath.Join(root, "Archive"), 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if _, err := backupcontracts.Create(backupcontracts.MutateInput{
		BoxRoot: root,
		Contract: backupcontracts.Contract{
			Key:         "archive",
			DisplayName: "Archive Backup",
			OwnerNode:   "macbook",
			Target: backupcontracts.TargetSpec{
				Scope: backupcontracts.TargetScopeBoxRelative,
				Path:  "Archive",
			},
		},
	}); err != nil {
		t.Fatalf("Create backup contract: %v", err)
	}

	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	if len(plan.WatchedRoots) != 3 {
		t.Fatalf("expected notes, documents, and backup contract roots, got %#v", plan.WatchedRoots)
	}
	backup := projectcontracts.ProjectWatchedRootItem{}
	for _, root := range plan.WatchedRoots {
		if root.BackendRootKey == "loom_box_backup__archive" {
			backup = root
			break
		}
	}
	if backup.BackendRootKey != "loom_box_backup__archive" || backup.Key != "backup_archive" {
		t.Fatalf("unexpected backup root keys: %#v", backup)
	}
	if backup.SafeRootKey != "loom_box" || backup.RootRelativePath != "Archive" {
		t.Fatalf("unexpected backup root path: %#v", backup)
	}
	if backup.SyncMode != agentwatchedroots.SyncModeNone ||
		backup.BackupMode != agentwatchedroots.BackupModeIncrementalRaw ||
		backup.IndexMode != agentwatchedroots.IndexModeNone ||
		backup.DeleteMode != agentwatchedroots.DeleteModeLocalStateOnly {
		t.Fatalf("unexpected backup root modes: %#v", backup)
	}
	if backup.Metadata["source"] != backupcontracts.MetadataSource || backup.Metadata["contract_key"] != "archive" {
		t.Fatalf("unexpected backup metadata: %#v", backup.Metadata)
	}
	if len(backup.AgentCommands) != 1 {
		t.Fatalf("backup root should include an owner-agent apply command: %#v", backup.AgentCommands)
	}
	commandsJSON, err := json.Marshal(backup.AgentCommands)
	if err != nil {
		t.Fatalf("encode backup agent commands: %v", err)
	}
	if len(commandsJSON) == 0 || commandsJSON[0] != '[' {
		t.Fatalf("backup agent commands should encode as a JSON array, got %s", commandsJSON)
	}

	scopes, err := boxWatchScopePlans(plan, nodes.Node{NodeID: "node_test", NodeKey: "macbook"})
	if err != nil {
		t.Fatalf("boxWatchScopePlans returned error: %v", err)
	}
	if len(scopes) != 1 || scopes[0].AreaKey != AreaNotes {
		t.Fatalf("backup-only root should not create scope: %#v", scopes)
	}
}

func TestBuildWatchPlanFallsBackToLegacyLaunchpad(t *testing.T) {
	root := initializedLegacyLaunchpadWatchBox(t, ProfileWorkspace)
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	if len(plan.WatchedRoots) != 2 {
		t.Fatalf("expected notes and legacy launchpad watched roots, got %#v", plan.WatchedRoots)
	}
	if plan.WatchedRoots[0].BackendRootKey != "loom_box__launchpad" || plan.WatchedRoots[0].RootRelativePath != "Launchpad" {
		t.Fatalf("unexpected legacy launchpad root: %#v", plan.WatchedRoots[0])
	}
	foundLegacyDropzone := false
	for _, excluded := range plan.Excluded {
		if excluded.Area == AreaDropzone {
			foundLegacyDropzone = true
		}
	}
	if !foundLegacyDropzone {
		t.Fatalf("legacy Dropzone should remain explicitly inspectable as an excluded area: %#v", plan.Excluded)
	}
}

func TestBoxWatchScopePlansRejectUnexpectedScopeRef(t *testing.T) {
	root := initializedWatchBoxWithID(t, ProfileWorkspace, "box_test")
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	var config agentwatchedroots.RootConfig
	if err := json.Unmarshal(plan.WatchedRoots[0].ConfigJSON, &config); err != nil {
		t.Fatalf("decode root config: %v", err)
	}
	config.SyncPolicy.ScopeRef = "project:wrong"
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("encode root config: %v", err)
	}
	plan.WatchedRoots[0].ConfigJSON = encoded

	if _, err := boxWatchScopePlans(plan, nodes.Node{NodeID: "node_test", NodeKey: "macbook"}); err == nil {
		t.Fatal("expected unexpected scope ref to fail")
	}
}

func TestBuildWatchPlanRejectsInvalidPolicy(t *testing.T) {
	root := initializedWatchBox(t, ProfileWorkspace)
	policyPath := filepath.Join(root, ".loom", "policies", "notes.watch.yaml")
	if err := os.WriteFile(policyPath, []byte("schema_version: nope\narea: notes\nmode: watched_root\n"), 0o644); err != nil {
		t.Fatalf("write invalid policy: %v", err)
	}
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err == nil {
		t.Fatal("expected invalid policy to fail")
	}
	if len(plan.Diagnostics) == 0 {
		t.Fatalf("expected diagnostics in plan: %#v", plan)
	}
	found := false
	for _, diagnostic := range plan.Diagnostics {
		if diagnostic.Code == "box.watch.schema_version_invalid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected schema diagnostic, got %#v", plan.Diagnostics)
	}
}

func TestBuildWatchPlanRequiresInitializedBox(t *testing.T) {
	root := filepath.Join(t.TempDir(), "LOOM Box")
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err == nil {
		t.Fatal("expected missing Box to fail")
	}
	if len(plan.WatchedRoots) != 0 || len(plan.Diagnostics) == 0 || plan.Diagnostics[0].Code != "box.watch.box_not_ready" {
		t.Fatalf("unexpected missing Box plan: %#v", plan)
	}
}

func TestBuildWatchPlanMainProfileHasNoRetiredIntakeExclusions(t *testing.T) {
	root := initializedWatchBox(t, ProfileMain)
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileMain, OwnerNode: "main"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	for _, watched := range plan.WatchedRoots {
		if strings.Contains(watched.BackendRootKey, "dropzone") {
			t.Fatalf("main profile must not watch Dropzone: %#v", watched)
		}
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Area != AreaProjects {
		t.Fatalf("current main plan should exclude only project-owned roots: %#v", plan.Excluded)
	}
}

func initializedWatchBox(t *testing.T, profile string) string {
	t.Helper()
	return initializedWatchBoxWithID(t, profile, "")
}

func initializedWatchBoxWithID(t *testing.T, profile, boxID string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "LOOM Box")
	input := InitInput{Resolved: Resolved{RootPath: root, Profile: profile, OwnerNode: "macbook"}}
	if boxID != "" {
		input.NewBoxID = func() string { return boxID }
	}
	if _, err := Init(input); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	return root
}

func initializedLegacyLaunchpadWatchBox(t *testing.T, profile string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "LOOM Box")
	for _, rel := range []string{
		"Projects",
		"Notes",
		"Launchpad",
		"Dropzone",
		".loom/policies",
		".loom/state/dropzone",
		".loom/state/lane",
		".loom/state/lane/batches",
		".loom/state/lane/sent",
		".loom/state/lane/logs",
	} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("mkdir legacy %s: %v", rel, err)
		}
	}
	payload := validLegacyLaunchpadContractPayload(t, root, profile)
	if err := os.WriteFile(filepath.Join(root, ".loom", "box.yaml"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write legacy contract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "policies", "notes.watch.yaml"), []byte(notesPolicy(DefaultContract(Resolved{RootPath: root, Profile: profile, OwnerNode: "macbook"}))), 0o644); err != nil {
		t.Fatalf("write notes policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "policies", "launchpad.watch.yaml"), []byte(launchpadPolicy(DefaultContract(Resolved{RootPath: root, Profile: profile, OwnerNode: "macbook"}))), 0o644); err != nil {
		t.Fatalf("write launchpad policy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".loom", "policies", "dropzone.transfer.yaml"), []byte(dropzonePolicy(DefaultContract(Resolved{RootPath: root, Profile: profile, OwnerNode: "macbook"}))), 0o644); err != nil {
		t.Fatalf("write dropzone policy: %v", err)
	}
	return root
}

func validLegacyLaunchpadContractPayload(t *testing.T, root string, profile string) string {
	t.Helper()
	return `schema_version: loom.box.v0.4.1
box_id: box_test
owner_node: macbook
profile: ` + profile + `
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
    transfer_status: scaffolded_for_v0.4.2
default_project_path: Projects
policies:
  notes: .loom/policies/notes.watch.yaml
  launchpad: .loom/policies/launchpad.watch.yaml
  dropzone: .loom/policies/dropzone.transfer.yaml
`
}
