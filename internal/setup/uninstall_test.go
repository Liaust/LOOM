package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUninstallPurgeRequiresProductionConfirmation(t *testing.T) {
	tmp := t.TempDir()
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:       "main",
		NodeID:        "node_main",
		DisplayName:   "Main",
		NodeKind:      "main",
		NodeRole:      "main",
		RuntimeClass:  "main_full",
		BootstrapMode: "production",
		ConfigDir:     filepath.Join(tmp, "config"),
		StateDir:      filepath.Join(tmp, "state"),
		DataDir:       filepath.Join(tmp, "data"),
		LogDir:        filepath.Join(tmp, "log"),
		InstallMode:   InstallModeUser,
		PackageMode:   PackageModeLocalBuild,
	})

	plan, err := PlanUninstall(UninstallInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp}},
		Mode:        UninstallModePurge,
		ConfirmNode: "main",
	})
	if err != nil {
		t.Fatalf("PlanUninstall returned error: %v", err)
	}
	if !plan.Production {
		t.Fatalf("plan.Production = false, want true")
	}
	if !uninstallPlanHasUnsatisfiedGuard(plan, "allow_production_main") {
		t.Fatalf("expected allow_production_main guardrail")
	}
	if !uninstallPlanHasUnsatisfiedGuard(plan, "production_backup_ref") {
		t.Fatalf("expected production_backup_ref guardrail")
	}
}

func TestMainPurgeCanRemoveRuntimeDatabaseButProtectsCanonicalBoxAndStorage(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "runtime-data")
	boxRoot := filepath.Join(tmp, "canonical-box")
	storageRoot := filepath.Join(tmp, "physical-storage")
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:       "main",
		NodeID:        "node_main",
		DisplayName:   "Main",
		NodeKind:      "main",
		NodeRole:      "main",
		RuntimeClass:  "main_full",
		BootstrapMode: "production",
		ConfigDir:     filepath.Join(tmp, "config"),
		StateDir:      filepath.Join(tmp, "state"),
		DataDir:       dataDir,
		LogDir:        filepath.Join(tmp, "log"),
		BoxPath:       boxRoot,
		BoxStateRoot:  filepath.Join(dataDir, "box-state"),
		StorageRoot:   storageRoot,
		InstallMode:   InstallModeUser,
		PackageMode:   PackageModeLocalBuild,
	})

	plan, err := PlanUninstall(UninstallInput{
		StatusInput:         StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp}},
		Mode:                UninstallModePurge,
		ConfirmNode:         "main",
		RemoveDB:            true,
		RemoveBox:           true,
		AllowProductionMain: true,
		BackupRef:           "reviewed-backup-ref",
	})
	if err != nil {
		t.Fatalf("PlanUninstall returned error: %v", err)
	}
	actions := map[string]string{}
	for _, action := range plan.Paths {
		actions[action.ID] = action.Action
	}
	if actions["data_dir"] != UninstallActionRemove {
		t.Fatalf("runtime data action = %q, want remove", actions["data_dir"])
	}
	for _, id := range []string{"box", "storage_root"} {
		if actions[id] != UninstallActionPreserve {
			t.Fatalf("%s action = %q, want preserve", id, actions[id])
		}
	}
	if _, ok := actions["box_state"]; ok {
		t.Fatalf("Box state nested under explicitly removable runtime data must not be reported as separately preserved: %#v", actions)
	}
}

func TestUninstallMissingManifestBlocksMutation(t *testing.T) {
	tmp := t.TempDir()
	plan, err := PlanUninstall(UninstallInput{
		StatusInput: StatusInput{
			ManifestPath: filepath.Join(tmp, "missing-install.yaml"),
			Spec:         SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp},
		},
		Mode: UninstallModePreserveData,
	})
	if err != nil {
		t.Fatalf("PlanUninstall returned error: %v", err)
	}
	if !uninstallPlanHasUnsatisfiedGuard(plan, "manifest_loaded") {
		t.Fatalf("expected manifest_loaded guardrail, got %#v", plan.Guardrails)
	}
}

func TestPreserveDataWithoutCredentialRemovalKeepsNodeAgentState(t *testing.T) {
	tmp := t.TempDir()
	nodeConfig := filepath.Join(tmp, "node-agent", "config.json")
	nodeState := filepath.Join(tmp, "node-agent", "state.json")
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:             "workspace",
		NodeID:              "node_workspace",
		DisplayName:         "Workspace",
		NodeKind:            "workspace",
		NodeRole:            "primary_workspace",
		RuntimeClass:        "workspace_full",
		HomeDir:             tmp,
		ConfigDir:           filepath.Join(tmp, "config"),
		StateDir:            filepath.Join(tmp, "state"),
		DataDir:             filepath.Join(tmp, "data"),
		LogDir:              filepath.Join(tmp, "log"),
		NodeAgentConfigPath: nodeConfig,
		NodeAgentStatePath:  nodeState,
		NodeAgentDataDir:    filepath.Dir(nodeConfig),
		InstallMode:         InstallModeUser,
		PackageMode:         PackageModeLocalBuild,
		Credential: CredentialManifest{
			Configured:       true,
			NodeCredentialID: "node_cred_test",
			CredentialHint:   "node_cred_abc",
		},
	})
	plan, err := PlanUninstall(UninstallInput{
		StatusInput:    StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp}},
		Mode:           UninstallModePreserveData,
		SkipMainRevoke: true,
	})
	if err != nil {
		t.Fatalf("PlanUninstall returned error: %v", err)
	}
	for _, action := range plan.Paths {
		if action.ID == "node_agent_state" && action.Action != UninstallActionPreserve {
			t.Fatalf("node_agent_state action = %s, want preserve", action.Action)
		}
	}
}

func TestUninstallPurgeRefusesSuspiciousHomePath(t *testing.T) {
	tmp := t.TempDir()
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:      "workspace",
		NodeID:       "node_workspace",
		DisplayName:  "Workspace",
		NodeKind:     "workspace",
		NodeRole:     "primary_workspace",
		RuntimeClass: "workspace_full",
		HomeDir:      tmp,
		ConfigDir:    filepath.Join(tmp, "config"),
		StateDir:     filepath.Join(tmp, "state"),
		DataDir:      tmp,
		LogDir:       filepath.Join(tmp, "log"),
		InstallMode:  InstallModeUser,
		PackageMode:  PackageModeLocalBuild,
	})

	result, err := ApplyUninstall(UninstallInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp}},
		Mode:        UninstallModePurge,
		ConfirmNode: "workspace",
		RemoveDB:    true,
		Yes:         true,
	})
	if err != nil {
		t.Fatalf("ApplyUninstall returned error: %v", err)
	}
	if len(result.Blocked) == 0 {
		t.Fatalf("expected purge to be blocked")
	}
	if !uninstallResultBlocked(result, "safe_path_data_dir") {
		t.Fatalf("expected safe_path_data_dir block, got %#v", result.Blocked)
	}
}

func TestUninstallOfflineDecommissionPendingRemovesCredentialMetadata(t *testing.T) {
	tmp := t.TempDir()
	nodeConfig := filepath.Join(tmp, "node-agent", "config.json")
	nodeState := filepath.Join(tmp, "node-agent", "state.json")
	if err := os.MkdirAll(filepath.Dir(nodeConfig), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeConfig, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeState, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:             "workspace",
		NodeID:              "node_workspace",
		DisplayName:         "Workspace",
		NodeKind:            "workspace",
		NodeRole:            "primary_workspace",
		RuntimeClass:        "workspace_full",
		HomeDir:             tmp,
		ConfigDir:           filepath.Join(tmp, "config"),
		StateDir:            filepath.Join(tmp, "state"),
		DataDir:             filepath.Join(tmp, "data"),
		LogDir:              filepath.Join(tmp, "log"),
		NodeAgentConfigPath: nodeConfig,
		NodeAgentStatePath:  nodeState,
		NodeAgentDataDir:    filepath.Dir(nodeConfig),
		InstallMode:         InstallModeUser,
		PackageMode:         PackageModeLocalBuild,
		Credential: CredentialManifest{
			Configured:       true,
			NodeCredentialID: "node_cred_test",
			CredentialHint:   "node_cred_abc",
		},
	})

	result, err := ApplyUninstall(UninstallInput{
		StatusInput:       StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: tmp}},
		Mode:              UninstallModePreserveData,
		SkipMainRevoke:    true,
		RemoveCredentials: true,
		Yes:               true,
		Now: func() time.Time {
			return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("ApplyUninstall returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("unexpected blocked result: %#v", result.Blocked)
	}
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("ReadManifest returned error: %v", err)
	}
	if manifest.Credential.Configured {
		t.Fatalf("credential remained configured")
	}
	if manifest.LastStatus.Status != "decommission_pending" {
		t.Fatalf("last status = %q, want decommission_pending", manifest.LastStatus.Status)
	}
	if _, err := os.Stat(nodeConfig); !os.IsNotExist(err) {
		t.Fatalf("node config still exists or stat failed unexpectedly: %v", err)
	}
	if result.RecordPath == "" {
		t.Fatalf("record path is empty")
	}
}

func TestUninstallLaunchdDisableOnlyStopsByLabelAndPreservesPlist(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home", "leonardo")
	plistPath := LaunchAgentPlistPath(home)
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:             "macbook",
		NodeID:              "node_macbook",
		DisplayName:         "MacBook",
		NodeKind:            "workspace",
		NodeRole:            "primary_workspace",
		RuntimeClass:        "workspace_full",
		MainURL:             "http://10.44.0.2:8080",
		HomeDir:             home,
		ConfigDir:           filepath.Join(home, ".config", "loom"),
		StateDir:            filepath.Join(home, ".local", "state", "loom"),
		DataDir:             filepath.Join(home, ".local", "share", "loom"),
		LogDir:              filepath.Join(home, ".local", "state", "loom", "logs"),
		NodeAgentConfigPath: filepath.Join(home, ".config", "loom-node-agent", "config.json"),
		NodeAgentStatePath:  filepath.Join(home, ".local", "state", "loom-node-agent", "state.json"),
		NodeAgentDataDir:    filepath.Join(home, ".local", "state", "loom-node-agent"),
		InstallMode:         InstallModeService,
		ServiceManager:      ServiceManagerLaunchd,
		PackageMode:         PackageModeLocalBuild,
		Services:            []InstalledService{{Name: "loom-node-agent", Manager: ServiceManagerLaunchd, Label: LaunchAgentLabel, Path: plistPath}},
	})
	runner := &fakeUninstallServiceRunner{}
	result, err := ApplyUninstall(UninstallInput{
		StatusInput:   StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeService, ServiceManager: ServiceManagerLaunchd, HomeDir: home}},
		Mode:          UninstallModeDisableOnly,
		Yes:           true,
		ServiceRunner: runner,
	})
	if err != nil {
		t.Fatalf("ApplyUninstall returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("unexpected blocked result: %#v", result.Blocked)
	}
	if len(runner.calls) != 1 || runner.calls[0].service != LaunchAgentLabel {
		t.Fatalf("service runner calls = %#v", runner.calls)
	}
	foundPreservedPlist := false
	for _, change := range result.Preserved {
		if change.ID == "launch_agent_plist" && change.Path == plistPath {
			foundPreservedPlist = true
		}
	}
	if !foundPreservedPlist {
		t.Fatalf("LaunchAgent plist was not preserved: %#v", result.Preserved)
	}
}

func TestUninstallLaunchdPreserveDataRemovesGeneratedIntegrationAndKeepsBox(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home", "leonardo")
	boxPath := filepath.Join(home, "LOOM Box")
	plistPath := LaunchAgentPlistPath(home)
	nodeConfig := filepath.Join(home, ".config", "loom-node-agent", "config.json")
	nodeState := filepath.Join(home, ".local", "state", "loom-node-agent", "state.json")
	for _, path := range []string{filepath.Join(boxPath, ".loom"), filepath.Dir(plistPath), filepath.Dir(nodeConfig), filepath.Dir(nodeState)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	for _, path := range []string{plistPath, nodeConfig, nodeState} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:             "macbook",
		DisplayName:         "MacBook",
		NodeKind:            "workspace",
		NodeRole:            "primary_workspace",
		RuntimeClass:        "workspace_full",
		MainURL:             "http://10.44.0.2:8080",
		HomeDir:             home,
		ConfigDir:           filepath.Join(home, ".config", "loom"),
		StateDir:            filepath.Join(home, ".local", "state", "loom"),
		DataDir:             filepath.Join(home, ".local", "share", "loom"),
		LogDir:              filepath.Join(home, ".local", "state", "loom", "logs"),
		BoxPath:             boxPath,
		NodeAgentConfigPath: nodeConfig,
		NodeAgentStatePath:  nodeState,
		NodeAgentDataDir:    filepath.Dir(nodeState),
		InstallMode:         InstallModeService,
		ServiceManager:      ServiceManagerLaunchd,
		PackageMode:         PackageModeLocalBuild,
		Services:            []InstalledService{{Name: "loom-node-agent", Manager: ServiceManagerLaunchd, Label: LaunchAgentLabel, Path: plistPath}},
	})
	runner := &fakeUninstallServiceRunner{}
	result, err := ApplyUninstall(UninstallInput{
		StatusInput:   StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeService, ServiceManager: ServiceManagerLaunchd, HomeDir: home}},
		Mode:          UninstallModePreserveData,
		Yes:           true,
		ServiceRunner: runner,
	})
	if err != nil {
		t.Fatalf("ApplyUninstall returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("unexpected blocked result: %#v", result.Blocked)
	}
	for _, path := range []string{plistPath, nodeConfig} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("expected generated integration to be removed: %s stat=%v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(boxPath, ".loom")); err != nil {
		t.Fatalf("Box should be preserved: %v", err)
	}
}

func TestUninstallWorkspacePurgeRequiresConfirmAndExplicitBoxRemoval(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home", "leonardo")
	boxPath := filepath.Join(home, "LOOM Box")
	manifestPath := writeUninstallTestManifest(t, tmp, InstallManifest{
		NodeKey:      "macbook",
		DisplayName:  "MacBook",
		NodeKind:     "workspace",
		NodeRole:     "primary_workspace",
		RuntimeClass: "workspace_full",
		HomeDir:      home,
		ConfigDir:    filepath.Join(home, ".config", "loom"),
		StateDir:     filepath.Join(home, ".local", "state", "loom"),
		DataDir:      filepath.Join(home, ".local", "share", "loom"),
		LogDir:       filepath.Join(home, ".local", "state", "loom", "logs"),
		BoxPath:      boxPath,
		InstallMode:  InstallModeService,
		PackageMode:  PackageModeLocalBuild,
	})
	withoutConfirm, err := PlanUninstall(UninstallInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeService, HomeDir: home}},
		Mode:        UninstallModePurge,
	})
	if err != nil {
		t.Fatalf("PlanUninstall without confirm returned error: %v", err)
	}
	if !uninstallPlanHasUnsatisfiedGuard(withoutConfirm, "confirm_node") {
		t.Fatalf("expected confirm_node guardrail: %#v", withoutConfirm.Guardrails)
	}

	withConfirm, err := PlanUninstall(UninstallInput{
		StatusInput: StatusInput{ManifestPath: manifestPath, Spec: SetupSpec{InstallMode: InstallModeService, HomeDir: home}},
		Mode:        UninstallModePurge,
		ConfirmNode: "macbook",
	})
	if err != nil {
		t.Fatalf("PlanUninstall with confirm returned error: %v", err)
	}
	for _, path := range withConfirm.Paths {
		if path.ID == "box" && path.Action != UninstallActionPreserve {
			t.Fatalf("Box action = %s, want preserve without --remove-box", path.Action)
		}
	}
}

func writeUninstallTestManifest(t *testing.T, tmp string, manifest InstallManifest) string {
	t.Helper()
	if manifest.SchemaVersion == "" {
		manifest.SchemaVersion = ManifestSchemaVersion
	}
	if manifest.InstallID == "" {
		manifest.InstallID = "install_test"
	}
	if manifest.InstalledAt.IsZero() {
		manifest.InstalledAt = time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	}
	if manifest.UpdatedAt.IsZero() {
		manifest.UpdatedAt = manifest.InstalledAt
	}
	if manifest.SetupVersion == "" {
		manifest.SetupVersion = "test"
	}
	if manifest.AuthorityProfile == "" {
		manifest.AuthorityProfile = "workspace_owner"
	}
	if manifest.RuntimeProfile == "" {
		manifest.RuntimeProfile = "workspace_full"
	}
	path := filepath.Join(tmp, "config", "install.yaml")
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	return path
}

func uninstallPlanHasUnsatisfiedGuard(plan UninstallPlan, id string) bool {
	for _, guard := range plan.Guardrails {
		if guard.ID == id && guard.Required && !guard.Satisfied {
			return true
		}
	}
	return false
}

func uninstallResultBlocked(result UninstallResult, id string) bool {
	for _, change := range result.Blocked {
		if change.ID == id {
			return true
		}
	}
	return false
}

type fakeUninstallServiceRunner struct {
	calls []fakeUninstallServiceCall
}

type fakeUninstallServiceCall struct {
	manager string
	service string
	dryRun  bool
}

func (r *fakeUninstallServiceRunner) StopDisable(ctx context.Context, manager, service string, dryRun bool) (string, error) {
	r.calls = append(r.calls, fakeUninstallServiceCall{manager: manager, service: service, dryRun: dryRun})
	return "stopped " + service, nil
}
