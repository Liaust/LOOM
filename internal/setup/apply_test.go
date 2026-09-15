package setup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/enrollmentflow"
	"loom.local/loom/internal/filesystemconnector"
	"loom.local/loom/internal/nodeagent"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodeprofiles"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/response"
)

func TestApplyDryRunDoesNotCreatePaths(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		DryRun:       true,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply dry-run returned error: %v", err)
	}
	if !result.DryRun || len(result.Changed) == 0 {
		t.Fatalf("dry-run result missing changes: %#v", result)
	}
	for _, path := range []string{spec.ConfigDir, spec.DataDir, spec.BoxPath, spec.MainDocumentsPath, spec.StorageExportRoot, filepath.Join(spec.HomeDir, box.DefaultStorageLinkName), filepath.Join(spec.HomeDir, box.DefaultMainBoxLinkName), filepath.Join(spec.HomeDir, box.LegacyStorageLinkName), filepath.Join(spec.HomeDir, box.LegacyMainBoxLinkName), manifestPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("dry-run should not create %s, stat err=%v", path, err)
		}
	}
}

func TestApplyMainCreatesPathsEnvManifestAndIsIdempotent(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("apply refused/blocked: %#v", result)
	}
	for _, path := range []string{
		spec.ConfigDir,
		spec.DataDir,
		spec.ServiceRoot,
		spec.StorageRoot,
		spec.ImportsRoot,
		spec.UserBackupsRoot,
		spec.ArchiveRoot,
		spec.GeneratedRoot,
		spec.BoxStateRoot,
		spec.CloudBorgCacheDir,
		spec.CloudBorgSecurityDir,
		filepath.Join(spec.ObjectStorePath, "temp"),
		filepath.Join(spec.CloudStateDir, "borg"),
		filepath.Join(spec.BoxPath, "Projects"),
		filepath.Join(spec.BoxPath, "Topics"),
		filepath.Join(spec.BoxPath, "Library"),
	} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected directory %s, info=%#v err=%v", path, info, err)
		}
	}
	for _, path := range []string{
		filepath.Join(spec.BoxPath, ".loom", "state"),
		filepath.Join(spec.BoxPath, ".loom", "storage"),
		filepath.Join(spec.BoxPath, ".loom", "logs"),
		filepath.Join(spec.BoxPath, box.DefaultLaneDirName),
		filepath.Join(spec.BoxPath, "Dropzone"),
		filepath.Join(spec.BoxPath, ".loom", "policies", "lane.transfer.yaml"),
		filepath.Join(spec.BoxPath, ".loom", "policies", "dropzone.transfer.yaml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("main Box must not contain volatile or retired intake path %s, stat err=%v", path, err)
		}
	}
	if info, err := os.Stat(spec.ConfigDir); err != nil {
		t.Fatalf("stat config dir: %v", err)
	} else if got := info.Mode().Perm(); got != 0o750 {
		t.Fatalf("config dir mode = %o, want 750", got)
	}
	envPath := filepath.Join(spec.ConfigDir, "loom.env")
	env, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	envBody := string(env)
	for _, want := range []string{
		"LOOM_NODE_KIND=main",
		"LOOM_NODE_ROLE=main",
		"LOOM_RUNTIME_CLASS=main_full",
		"LOOM_BOX_PATH=" + spec.BoxPath,
		"LOOM_BOX_PROFILE=main",
		"LOOM_MAIN_DOCUMENTS_ROOT=" + spec.MainDocumentsPath,
		"LOOM_BOOTSTRAP_MODE=production",
		"LOOM_CONFIG_FILE=" + envPath,
	} {
		if !strings.Contains(envBody, want) {
			t.Fatalf("env missing %q:\n%s", want, envBody)
		}
	}
	if strings.Contains(envBody, "LOOM_STORAGE_EXPORT_ROOT=") {
		t.Fatalf("new install env must not activate the retired storage export:\n%s", envBody)
	}
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.NodeKind != "main" || manifest.RuntimeClass != nodeprofiles.RuntimeMainFull {
		t.Fatalf("manifest identity unexpected: %#v", manifest)
	}
	if manifest.BoxPath != spec.BoxPath || manifest.BoxProfile != box.ProfileMain {
		t.Fatalf("manifest Box fields unexpected: %#v", manifest)
	}
	if manifest.MainDocumentsPath != spec.MainDocumentsPath {
		t.Fatalf("manifest main Documents path = %q, want %q", manifest.MainDocumentsPath, spec.MainDocumentsPath)
	}
	if manifest.StorageExportRoot != spec.StorageExportRoot {
		t.Fatalf("manifest storage export root = %q, want %q", manifest.StorageExportRoot, spec.StorageExportRoot)
	}
	if manifest.CloudSnapshotBackend != spec.CloudSnapshotBackend || manifest.CloudBorgPassphraseFile != spec.CloudBorgPassphraseFile {
		t.Fatalf("manifest Borg fields unexpected: %#v", manifest)
	}
	for _, home := range []string{spec.HomeDir, filepath.Join(filepath.Dir(spec.HomeDir), "loomdesk")} {
		storageLink := filepath.Join(home, box.DefaultStorageLinkName)
		if _, err := os.Lstat(storageLink); !os.IsNotExist(err) {
			t.Fatalf("retired loom-storage link %s must not be created, err=%v", storageLink, err)
		}
		mainBoxLink := filepath.Join(home, box.DefaultMainBoxLinkName)
		if got, err := os.Readlink(mainBoxLink); err != nil || got != spec.BoxPath {
			t.Fatalf("loom-main-box link %s = %q err=%v, want %s", mainBoxLink, got, err, spec.BoxPath)
		}
		for _, legacyLink := range []string{filepath.Join(home, box.LegacyStorageLinkName), filepath.Join(home, box.LegacyMainBoxLinkName)} {
			if _, err := os.Lstat(legacyLink); !os.IsNotExist(err) {
				t.Fatalf("legacy human link %s should not be created, err=%v", legacyLink, err)
			}
		}
	}
	if !statusPath(result.Status.Paths, "cloud_borg_cache_dir", spec.CloudBorgCacheDir) || !statusPath(result.Status.Paths, "cloud_borg_security_dir", spec.CloudBorgSecurityDir) {
		t.Fatalf("status paths do not include Borg dirs: %#v", result.Status.Paths)
	}
	if manifest.LastStatus.Status != SummaryPartial {
		t.Fatalf("manifest last status = %q, want partial because db_url is absent", manifest.LastStatus.Status)
	}

	second, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("second Apply returned error: %v", err)
	}
	if changedPath(second.Changed, filepath.Join(spec.BoxPath, "Projects")) {
		t.Fatalf("second apply should not recreate Box project path: %#v", second.Changed)
	}
	if !skippedID(second.Skipped, "write_loomd_env") {
		t.Fatalf("second apply should report service env already satisfied: %#v", second.Skipped)
	}
}

func TestApplyRefusesWithoutYes(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	result, err := Apply(ApplyInput{
		Spec:          spec,
		Facts:         testFacts(spec.HomeDir),
		ManifestPath:  manifestPath,
		NoInteractive: true,
		Now:           fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "--yes") {
		t.Fatalf("expected --yes refusal, got %#v", result)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("refused apply should not write manifest, stat err=%v", err)
	}
}

func TestApplyWorkspaceCreatesBoxNodeAgentRuntimeAndManifest(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	// Older manifests may still decode this field as true; normalization and
	// apply must nevertheless refuse to seed the retired runtime.
	spec.EnableDropzone = true
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply workspace returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("workspace apply refused/blocked: %#v", result)
	}
	manifest := result.Manifest
	if manifest.NodeKind != "workspace" || manifest.Enrollment.Status != "skipped" || manifest.Credential.Configured {
		t.Fatalf("workspace manifest enrollment unexpected: %#v", manifest)
	}
	if manifest.ProviderMode != "enabled" {
		t.Fatalf("provider mode = %q", manifest.ProviderMode)
	}
	for _, path := range []string{
		filepath.Join(spec.BoxPath, "Projects"),
		filepath.Join(spec.BoxPath, "Notes"),
		filepath.Join(spec.BoxPath, "Documents"),
		filepath.Join(spec.BoxPath, box.DefaultLaneDirName),
		manifest.BoxStateRoot,
		manifest.NodeAgentConfigPath,
		manifest.NodeAgentStatePath,
		filepath.Join(manifest.NodeAgentDataDir, "workers", "instances"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(spec.BoxPath, ".loom", "state")); !os.IsNotExist(err) {
		t.Fatalf("workspace Box must not contain volatile runtime state, stat err=%v", err)
	}
	for _, path := range []string{
		filepath.Join(spec.BoxPath, "Dropzone"),
		filepath.Join(spec.BoxPath, ".loom", "policies", "dropzone.transfer.yaml"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("workspace Box must not contain retired Dropzone path %s, stat err=%v", path, err)
		}
	}

	store := nodeagent.Store{
		ConfigPath: manifest.NodeAgentConfigPath,
		StatePath:  manifest.NodeAgentStatePath,
		DataDir:    manifest.NodeAgentDataDir,
	}
	agentConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load node-agent config: %v", err)
	}
	if agentConfig.NodeKey != spec.NodeKey || agentConfig.MainURL != spec.MainURL {
		t.Fatalf("node-agent config identity unexpected: %#v", agentConfig)
	}
	if root, ok := filesystemconnector.FindSafeRoot(agentConfig.Filesystem, "loom_box"); !ok || root.AbsolutePath != spec.BoxPath || !root.AllowIngest || root.MaxFileBytes != boxSafeRootMaxFileBytes {
		t.Fatalf("loom_box safe root missing or wrong: %#v", agentConfig.Filesystem.SafeRoots)
	}

	runtimeStore := noderuntime.NewStore(manifest.NodeAgentDataDir)
	for _, workerKey := range []string{
		noderuntime.WorkerKeyHeartbeat,
		noderuntime.WatchedRootWorkerKey("loom_box__notes"),
		noderuntime.WatchedRootWorkerKey("loom_box__documents"),
	} {
		if _, err := runtimeStore.LoadInstance(workerKey); err != nil {
			t.Fatalf("expected worker %s: %v", workerKey, err)
		}
	}
	if _, err := runtimeStore.LoadInstance(noderuntime.WorkerKeyDropzoneTransfer); !os.IsNotExist(err) {
		t.Fatalf("setup seeded retired Dropzone worker, err=%v", err)
	}
	documentsWorker, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey("loom_box__documents"))
	if err != nil {
		t.Fatalf("expected documents worker: %v", err)
	}
	if documentsWorker.IntervalSeconds != 60 {
		t.Fatalf("documents worker interval = %d, want 60", documentsWorker.IntervalSeconds)
	}
	var documentsConfig agentwatchedroots.RootConfig
	if err := json.Unmarshal(documentsWorker.ConfigJSON, &documentsConfig); err != nil {
		t.Fatalf("decode documents worker config: %v", err)
	}
	if documentsConfig.Scan.FullRescanInterval != "1m" || documentsConfig.Scan.MaxHashFileBytes != boxSafeRootMaxFileBytes || documentsConfig.BackupPolicy.MaxFileBytes != boxSafeRootMaxFileBytes {
		t.Fatalf("documents watched-root config not responsive/high-ceiling enough: %#v", documentsConfig)
	}

	second, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("second workspace Apply returned error: %v", err)
	}
	if !skippedID(second.Skipped, "write_node_agent_config") {
		t.Fatalf("second apply should report node-agent config already satisfied: %#v", second.Skipped)
	}
}

func TestApplyWorkspaceCanonicalBoxDoesNotCreateLegacyAliases(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.BoxPath = filepath.Join(spec.HomeDir, box.DefaultRootDirName)
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply workspace returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("workspace apply refused/blocked: %#v", result)
	}
	for _, legacyPath := range []string{
		filepath.Join(spec.HomeDir, box.LegacyRootDirName),
		filepath.Join(spec.BoxPath, box.LegacyLaneDirName),
	} {
		if _, err := os.Lstat(legacyPath); !os.IsNotExist(err) {
			t.Fatalf("setup must not create legacy alias %s, stat err=%v", legacyPath, err)
		}
	}
}

func TestApplyWorkspaceLegacyOnlyStateKeepsOneEffectiveRoot(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.BoxStateRoot = filepath.Join(spec.DataDir, "box-state")
	legacyStateRoot := filepath.Join(spec.BoxPath, ".loom", "state")
	legacyRecord := filepath.Join(legacyStateRoot, "lane", "logs", "legacy.log")
	if err := os.MkdirAll(filepath.Dir(legacyRecord), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyRecord, []byte("legacy runtime state\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply legacy-only workspace returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("legacy-only workspace apply refused/blocked: %#v", result)
	}
	if _, err := os.Stat(spec.BoxStateRoot); !os.IsNotExist(err) {
		t.Fatalf("legacy-only apply created an empty canonical split root: %v", err)
	}
	status := box.Inspect(box.Resolved{
		RootPath:         spec.BoxPath,
		Profile:          box.ProfileWorkspace,
		RuntimeStateRoot: spec.BoxStateRoot,
		LegacyStateRoot:  legacyStateRoot,
	})
	if status.RuntimeStateReadRoot != legacyStateRoot || status.RuntimeStateWriteRoot != legacyStateRoot || !status.RuntimeStateMigrationRequired {
		t.Fatalf("legacy-only apply did not preserve one effective read/write root: %#v", status)
	}
	if result.Manifest.BoxStateRoot != spec.BoxStateRoot || result.Status.Box.MigrationState != "ready" {
		t.Fatalf("legacy target/migration status not recorded truthfully: manifest=%#v status=%#v", result.Manifest, result.Status.Box)
	}
	for _, pathStatus := range result.Status.Paths {
		if pathStatus.Key == "box_state_root" && pathStatus.Required {
			t.Fatalf("legacy-only canonical target must not be offered as a required repair: %#v", pathStatus)
		}
	}
}

func TestApplyWorkspacePreservesExistingBoxSafeRootCeiling(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	plan, err := Plan(PlannerInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	store := nodeagent.Store{
		ConfigPath: plan.Paths.NodeAgentConfigPath,
		StatePath:  plan.Paths.NodeAgentStatePath,
		DataDir:    plan.Paths.NodeAgentDataDir,
	}
	largerMax := int64(80 * 1024 * 1024 * 1024)
	if err := store.SaveConfig(nodeagent.Config{
		MainURL:      spec.MainURL,
		NodeKey:      spec.NodeKey,
		DisplayName:  spec.DisplayName,
		NodeKind:     spec.NodeKind,
		NodeRole:     spec.NodeRole,
		RuntimeClass: spec.RuntimeClass,
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{{
			RootKey:       "loom_box",
			DisplayName:   "Existing LOOM Box",
			AbsolutePath:  spec.BoxPath,
			AllowList:     true,
			AllowMetadata: true,
			AllowIngest:   true,
			MaxFileBytes:  largerMax,
		}}},
	}); err != nil {
		t.Fatalf("seed node-agent config: %v", err)
	}

	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply workspace returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("workspace apply refused/blocked: %#v", result)
	}
	agentConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load node-agent config: %v", err)
	}
	root, ok := filesystemconnector.FindSafeRoot(agentConfig.Filesystem, "loom_box")
	if !ok {
		t.Fatalf("loom_box safe root missing: %#v", agentConfig.Filesystem.SafeRoots)
	}
	if root.MaxFileBytes != largerMax {
		t.Fatalf("loom_box safe root max_file_bytes downgraded to %d, want %d", root.MaxFileBytes, largerMax)
	}
}

func TestApplyWorkspacePreservesManagedProtectedFolderSafeRootOnly(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	store := nodeagent.Store{ConfigPath: plan.Paths.NodeAgentConfigPath, StatePath: plan.Paths.NodeAgentStatePath, DataDir: plan.Paths.NodeAgentDataDir}
	managedPath := filepath.Join(spec.HomeDir, "Protected Folder")
	manualPath := filepath.Join(spec.HomeDir, "Manual Root")
	if err := os.MkdirAll(managedPath, 0o700); err != nil {
		t.Fatalf("mkdir managed root: %v", err)
	}
	if err := os.MkdirAll(manualPath, 0o700); err != nil {
		t.Fatalf("mkdir manual root: %v", err)
	}
	managed := filesystemconnector.DefaultSafeRoot("backup_field-data", managedPath)
	managed.PrivateBackupOnly = true
	managed.Metadata, _ = json.Marshal(map[string]any{"source": backupcontracts.MetadataSource, "contract_key": "field-data"})
	manual := filesystemconnector.DefaultSafeRoot("manual", manualPath)
	if err := store.SaveConfig(nodeagent.Config{
		MainURL: spec.MainURL, NodeKey: spec.NodeKey, DisplayName: spec.DisplayName,
		NodeKind: spec.NodeKind, NodeRole: spec.NodeRole, RuntimeClass: spec.RuntimeClass,
		Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{managed, manual}},
	}); err != nil {
		t.Fatalf("seed node-agent config: %v", err)
	}

	result, err := Apply(ApplyInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Yes: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Apply workspace returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("workspace apply refused/blocked: %#v", result)
	}
	config, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load node-agent config: %v", err)
	}
	got, ok := filesystemconnector.FindSafeRoot(config.Filesystem, managed.RootKey)
	if !ok || got.AbsolutePath != managed.AbsolutePath || !got.PrivateBackupOnly {
		t.Fatalf("managed protected-folder root was not preserved: %#v", config.Filesystem.SafeRoots)
	}
	if _, ok := filesystemconnector.FindSafeRoot(config.Filesystem, manual.RootKey); ok {
		t.Fatalf("undeclared manual safe root was implicitly preserved: %#v", config.Filesystem.SafeRoots)
	}
}

func TestFilesystemConfigFromPlanDoesNotLetExplicitSafeRootDowngradeBox(t *testing.T) {
	t.Parallel()

	spec, _ := testWorkspaceApplySpec(t)
	spec.SafeRoots = []SafeRootSpec{{Name: "loom_box", Path: spec.BoxPath, Mode: safeRootModeReadWrite}}
	spec.EnableBox = true
	config, err := filesystemConfigFromPlan(SetupPlan{
		Spec:  spec,
		Paths: PathPlan{BoxPath: spec.BoxPath},
	})
	if err != nil {
		t.Fatalf("filesystemConfigFromPlan returned error: %v", err)
	}
	root, ok := filesystemconnector.FindSafeRoot(config, "loom_box")
	if !ok {
		t.Fatalf("loom_box safe root missing: %#v", config.SafeRoots)
	}
	if root.MaxFileBytes != boxSafeRootMaxFileBytes {
		t.Fatalf("loom_box safe root max_file_bytes = %d, want %d", root.MaxFileBytes, boxSafeRootMaxFileBytes)
	}
}

func TestApplyWorkspaceLightDisablesProvidersAndDropzone(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.NodeRole = "secondary_workspace"
	spec.RuntimeClass = nodeprofiles.RuntimeWorkspaceLight
	spec.NodeKey = "macbook-light"
	spec.DisplayName = "MacBook Light"
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply workspace light returned error: %v", err)
	}
	if result.Manifest.ProviderMode != "disabled" {
		t.Fatalf("provider mode = %q", result.Manifest.ProviderMode)
	}
	runtimeStore := noderuntime.NewStore(result.Manifest.NodeAgentDataDir)
	if _, err := runtimeStore.LoadInstance(noderuntime.WorkerKeyDropzoneTransfer); !os.IsNotExist(err) {
		t.Fatalf("workspace_light should not configure dropzone worker, err=%v", err)
	}
}

func TestApplyHardwareCreatesNodeAgentButNoBox(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	home := filepath.Join(root, "home", "sensor")
	safeRoot := filepath.Join(root, "sensor-data")
	spec := SetupSpec{
		NodeKey:        "sensor",
		DisplayName:    "Sensor",
		NodeKind:       "hardware",
		NodeRole:       "capability_node",
		RuntimeClass:   nodeprofiles.RuntimeHardwareAgent,
		MainURL:        "http://10.44.0.2:8080",
		InstallMode:    InstallModeUser,
		ServiceManager: ServiceManagerNone,
		PackageMode:    PackageModeLocalBuild,
		HomeDir:        home,
		UserName:       "sensor",
		ConfigDir:      filepath.Join(root, "config"),
		DataDir:        filepath.Join(root, "data"),
		StateDir:       filepath.Join(root, "state"),
		LogDir:         filepath.Join(root, "logs"),
		SafeRoots:      []SafeRootSpec{{Name: "sensor", Path: safeRoot, Mode: safeRootModeReadWrite}},
	}
	manifestPath := filepath.Join(root, "config", "install.yaml")
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(home),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply hardware returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("hardware apply refused/blocked: %#v", result)
	}
	if result.Manifest.BoxPath != "" || result.Manifest.BoxProfile != box.ProfileWorkspace {
		t.Fatalf("hardware should not create Box path but should keep workspace default profile metadata: %#v", result.Manifest)
	}
	if _, err := os.Stat(filepath.Join(home, "LOOM Box")); !os.IsNotExist(err) {
		t.Fatalf("hardware should not create a Box, stat err=%v", err)
	}
	store := nodeagent.Store{
		ConfigPath: result.Manifest.NodeAgentConfigPath,
		StatePath:  result.Manifest.NodeAgentStatePath,
		DataDir:    result.Manifest.NodeAgentDataDir,
	}
	agentConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatalf("load hardware node-agent config: %v", err)
	}
	if root, ok := filesystemconnector.FindSafeRoot(agentConfig.Filesystem, "sensor"); !ok || root.AbsolutePath != safeRoot || !root.AllowIngest {
		t.Fatalf("safe root missing or wrong: %#v", agentConfig.Filesystem.SafeRoots)
	}
	if _, err := noderuntime.NewStore(result.Manifest.NodeAgentDataDir).LoadInstance(noderuntime.WorkerKeyHeartbeat); err != nil {
		t.Fatalf("hardware runtime defaults missing: %v", err)
	}
}

func TestApplyNonMainComputeAndStorageProfilesDryRun(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		role         string
		runtimeClass string
	}{
		{name: "compute", role: "compute_node", runtimeClass: nodeprofiles.RuntimeComputeRunner},
		{name: "storage", role: "storage_node", runtimeClass: nodeprofiles.RuntimeStorageEdge},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			home := filepath.Join(root, "home", tt.name)
			result, err := Apply(ApplyInput{
				Spec: SetupSpec{
					NodeKey:        tt.name,
					DisplayName:    tt.name,
					NodeKind:       "hardware",
					NodeRole:       tt.role,
					RuntimeClass:   tt.runtimeClass,
					MainURL:        "http://10.44.0.2:8080",
					InstallMode:    InstallModeUser,
					ServiceManager: ServiceManagerNone,
					HomeDir:        home,
					ConfigDir:      filepath.Join(root, "config"),
					DataDir:        filepath.Join(root, "data"),
					StateDir:       filepath.Join(root, "state"),
					LogDir:         filepath.Join(root, "logs"),
				},
				Facts:        testFacts(home),
				ManifestPath: filepath.Join(root, "config", "install.yaml"),
				DryRun:       true,
				Yes:          true,
				Now:          fixedNow,
			})
			if err != nil {
				t.Fatalf("Apply %s dry-run returned error: %v", tt.name, err)
			}
			if len(result.Blocked) > 0 || result.Manifest.RuntimeClass != tt.runtimeClass {
				t.Fatalf("unexpected %s dry-run result: %#v", tt.name, result)
			}
		})
	}
}

func TestApplyWorkspaceEnrollmentWithInjectedRunners(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.RunEnrollment = true
	spec.SkipEnroll = false
	spec.EnrollmentTTLSeconds = 1800
	spec.ApproveEnrollment = true
	spec.VerifyHeartbeat = true
	mainRunner := &setupEnrollmentMainFake{}
	targetRunner := &setupEnrollmentTargetFake{}

	result, err := Apply(ApplyInput{
		Spec:                   spec,
		Facts:                  testFacts(spec.HomeDir),
		ManifestPath:           manifestPath,
		Yes:                    true,
		Now:                    fixedNow,
		EnrollmentMainRunner:   mainRunner,
		EnrollmentTargetRunner: targetRunner,
	})
	if err != nil {
		t.Fatalf("Apply workspace enroll returned error: %v", err)
	}
	if len(result.Blocked) > 0 || result.Enrollment == nil || result.Enrollment.Status != enrollmentflow.StateVerifiedOnMain {
		t.Fatalf("enrollment result unexpected: %#v", result)
	}
	manifest := result.Manifest
	if manifest.Enrollment.Status != "approved" || manifest.Enrollment.EnrollmentRequestID != "node_enrollment_request_test" || !manifest.Enrollment.VerifiedOnMain {
		t.Fatalf("manifest enrollment unexpected: %#v", manifest.Enrollment)
	}
	if manifest.NodeID != "node_test" || !manifest.Credential.Configured || manifest.Credential.NodeCredentialID != "node_credential_test" || manifest.Credential.CredentialHint != "hintcred" {
		t.Fatalf("manifest credential unexpected: %#v", manifest)
	}
	if manifest.LastStatus.LastHeartbeatAt == nil {
		t.Fatalf("manifest did not record heartbeat time: %#v", manifest.LastStatus)
	}
	raw := mustJSONForSetupTest(t, result)
	if strings.Contains(raw, "node_enroll_secret") || strings.Contains(raw, "node_cred_secret") {
		t.Fatalf("setup apply result leaked secret material: %s", raw)
	}
}

func TestApplyWorkspaceEnrollmentDefaultsToHTTPMainURL(t *testing.T) {
	t.Parallel()

	approvedAt := time.Unix(1234, 0).UTC()
	receivedAt := time.Unix(4321, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-tokens":
			response.WriteJSON(w, http.StatusCreated, response.Success("test", nodes.CreateEnrollmentTokenResult{
				Token: nodes.EnrollmentToken{
					NodeEnrollmentTokenID: "node_enrollment_token_test",
					TokenHint:             "hint",
					Status:                nodes.EnrollmentTokenActive,
					ExpiresAt:             time.Unix(3600, 0).UTC(),
				},
				TokenValue: "node_enroll_secret",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-agent/enroll":
			response.WriteJSON(w, http.StatusCreated, response.Success("test", nodes.EnrollmentRequest{
				NodeEnrollmentRequestID: "node_enrollment_request_test",
				Status:                  nodes.EnrollmentRequestPending,
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-enrollment-requests/node_enrollment_request_test/approve":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.ApproveEnrollmentResult{
				Request: nodes.EnrollmentRequest{
					NodeEnrollmentRequestID: "node_enrollment_request_test",
					Status:                  nodes.EnrollmentRequestApproved,
					ApprovedAt:              &approvedAt,
				},
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				Credential: nodes.NodeCredential{
					NodeCredentialID: "node_credential_test",
					NodeID:           "node_test",
					CredentialHint:   "cred_hint",
					Status:           nodes.NodeCredentialActive,
				},
				CredentialToken: "node_cred_secret",
			}))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/node-agent/heartbeat":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.Heartbeat{
				NodeHeartbeatID: "node_heartbeat_test",
				NodeID:          "node_test",
				PresenceState:   nodes.PresenceOnline,
				ReportedStatus:  "ok",
				ReceivedAt:      receivedAt,
			}))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/nodes/node_test/health":
			response.WriteJSON(w, http.StatusOK, response.Success("test", nodes.NodeHealth{
				Node: nodes.Node{
					NodeID:        "node_test",
					NodeKey:       "macbook",
					PresenceState: nodes.PresenceOnline,
				},
				LastHeartbeat: &nodes.Heartbeat{
					NodeHeartbeatID: "node_heartbeat_test",
					NodeID:          "node_test",
					PresenceState:   nodes.PresenceOnline,
					ReceivedAt:      receivedAt,
				},
			}))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.MainURL = server.URL
	spec.RunEnrollment = true
	spec.SkipEnroll = false
	spec.EnrollmentTTLSeconds = 1800
	spec.ApproveEnrollment = true
	spec.VerifyHeartbeat = true
	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply workspace enroll returned error: %v", err)
	}
	if len(result.Blocked) > 0 || result.Enrollment == nil || result.Enrollment.Status != enrollmentflow.StateVerifiedOnMain {
		t.Fatalf("enrollment result unexpected: %#v", result)
	}
	if result.Manifest.Enrollment.Status != "approved" || !result.Manifest.Enrollment.VerifiedOnMain {
		t.Fatalf("manifest enrollment unexpected: %#v", result.Manifest.Enrollment)
	}
	if result.Manifest.NodeID != "node_test" || !result.Manifest.Credential.Configured || result.Manifest.Credential.NodeCredentialID != "node_credential_test" {
		t.Fatalf("manifest credential unexpected: %#v", result.Manifest)
	}
}

func TestApplyWorkspaceEnrollmentFailureWritesRedactedManifest(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.RunEnrollment = true
	spec.SkipEnroll = false
	spec.EnrollmentTTLSeconds = 1800
	spec.ApproveEnrollment = true
	spec.VerifyHeartbeat = true
	mainRunner := &setupEnrollmentMainFake{approveErr: errString("approval failed credential_token=node_cred_secret")}
	targetRunner := &setupEnrollmentTargetFake{}

	result, err := Apply(ApplyInput{
		Spec:                   spec,
		Facts:                  testFacts(spec.HomeDir),
		ManifestPath:           manifestPath,
		Yes:                    true,
		Now:                    fixedNow,
		EnrollmentMainRunner:   mainRunner,
		EnrollmentTargetRunner: targetRunner,
	})
	if err != nil {
		t.Fatalf("Apply workspace enroll failure returned error: %v", err)
	}
	if len(result.Blocked) == 0 || result.Manifest.Enrollment.Status != "failed" || result.Manifest.Enrollment.FailureCode != enrollmentflow.FailureApproval {
		t.Fatalf("expected blocked redacted enrollment failure: %#v", result)
	}
	if strings.Contains(result.Manifest.Enrollment.FailureMessage, "node_cred_secret") {
		t.Fatalf("manifest failure leaked credential token: %#v", result.Manifest.Enrollment)
	}
	manifest, err := ReadManifest(manifestPath)
	if err != nil {
		t.Fatalf("read failed enrollment manifest: %v", err)
	}
	if manifest.Enrollment.Status != "failed" || strings.Contains(mustJSONForSetupTest(t, manifest), "node_cred_secret") {
		t.Fatalf("written manifest was not redacted: %#v", manifest)
	}
}

func TestApplyWorkspaceEnrollmentMainVerificationFailurePreservesCredential(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testWorkspaceApplySpec(t)
	spec.RunEnrollment = true
	spec.SkipEnroll = false
	spec.EnrollmentTTLSeconds = 1800
	spec.ApproveEnrollment = true
	spec.VerifyHeartbeat = true
	mainRunner := &setupEnrollmentMainFake{healthErr: errString("main health timeout")}
	targetRunner := &setupEnrollmentTargetFake{}

	result, err := Apply(ApplyInput{
		Spec:                   spec,
		Facts:                  testFacts(spec.HomeDir),
		ManifestPath:           manifestPath,
		Yes:                    true,
		Now:                    fixedNow,
		EnrollmentMainRunner:   mainRunner,
		EnrollmentTargetRunner: targetRunner,
	})
	if err != nil {
		t.Fatalf("Apply workspace enroll verification failure returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("main verification timeout should not block local credential preservation: %#v", result.Blocked)
	}
	if result.Enrollment == nil || result.Enrollment.Status != enrollmentflow.StateFailed || result.Enrollment.FailureCode != enrollmentflow.FailureMainVerification {
		t.Fatalf("enrollment result unexpected: %#v", result.Enrollment)
	}
	if result.Manifest.Enrollment.Status != "approved" || result.Manifest.Enrollment.VerifiedOnMain {
		t.Fatalf("manifest enrollment should remain locally approved but not verified: %#v", result.Manifest.Enrollment)
	}
	if !result.Manifest.Credential.Configured || result.Manifest.Credential.NodeCredentialID != "node_credential_test" {
		t.Fatalf("manifest credential was not preserved: %#v", result.Manifest.Credential)
	}
}

func TestProductionInputFromPlanUsesSetupIdentity(t *testing.T) {
	t.Parallel()

	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	manifest := ManifestFromPlan(plan)
	input := productionInputFromPlan(plan, manifest)
	if input.NodeKey != spec.NodeKey || input.RuntimeClass != nodeprofiles.RuntimeMainFull {
		t.Fatalf("production input identity unexpected: %#v", input)
	}
	if input.AuthorityProfileKey != nodeprofiles.AuthorityMainNodeDefault || input.RuntimeProfileKey != nodeprofiles.RuntimeMainFull {
		t.Fatalf("production input profiles unexpected: %#v", input)
	}
	if input.InstallID != manifest.InstallID || input.PlanHash != plan.PlanHash {
		t.Fatalf("production input plan identity unexpected: %#v", input)
	}
}

func TestServiceEnvDBURLDoesNotRenderObviousSecrets(t *testing.T) {
	t.Parallel()

	if serviceEnvDBURL("postgres://loom:secret@example.local/loom") != "" {
		t.Fatal("URL password should not be rendered into service env")
	}
	if serviceEnvDBURL("user=loom password=secret dbname=loom") != "" {
		t.Fatal("password field should not be rendered into service env")
	}
	if got := serviceEnvDBURL("user=loom dbname=loom host=/run/postgresql sslmode=disable"); got == "" {
		t.Fatal("local socket DSN without password should remain renderable")
	}
}

func TestApplyRunsProductionBootstrapWithDB(t *testing.T) {
	dbURL := strings.TrimSpace(os.Getenv("LOOM_SETUP_APPLY_TEST_DB_URL"))
	if dbURL == "" {
		t.Skip("set LOOM_SETUP_APPLY_TEST_DB_URL to run setup apply database integration test")
	}

	spec, manifestPath := testMainApplySpec(t)
	spec.NodeKey = "setup-main-" + strings.ToLower(strings.ReplaceAll(t.Name(), "_", "-"))
	spec.DisplayName = "Setup Apply Main"
	spec.DBURL = dbURL
	spec.MigrationsDir = repoMigrationsDirForSetup(t)

	result, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply with DB returned error: %v", err)
	}
	if result.Manifest.ProductionBootstrap.Checked != true || result.Manifest.ProductionBootstrap.Ready != true {
		t.Fatalf("production bootstrap was not recorded ready: %#v", result.Manifest.ProductionBootstrap)
	}
	second, err := Apply(ApplyInput{
		Spec:         spec,
		Facts:        testFacts(spec.HomeDir),
		ManifestPath: manifestPath,
		Yes:          true,
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("second Apply with DB returned error: %v", err)
	}
	if second.Manifest.ProductionBootstrap.EventCount != 1 {
		t.Fatalf("production bootstrap should stay idempotent, event count=%d", second.Manifest.ProductionBootstrap.EventCount)
	}
}

func testMainApplySpec(t *testing.T) (SetupSpec, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home", "loomadmin")
	spec := SetupSpec{
		NodeKey:           "main",
		DisplayName:       "Main",
		NodeKind:          "main",
		NodeRole:          "main",
		RuntimeClass:      nodeprofiles.RuntimeMainFull,
		InstallMode:       InstallModeUser,
		ServiceManager:    ServiceManagerNone,
		PackageMode:       PackageModeLocalBuild,
		HomeDir:           home,
		UserName:          "loomadmin",
		ConfigDir:         filepath.Join(root, "config"),
		DataDir:           filepath.Join(root, "data"),
		StateDir:          filepath.Join(root, "state"),
		LogDir:            filepath.Join(root, "logs"),
		ServiceRoot:       filepath.Join(root, "service"),
		StorageRoot:       filepath.Join(root, "service", "storage"),
		ImportsRoot:       filepath.Join(root, "service", "storage", "imports"),
		UserBackupsRoot:   filepath.Join(root, "service", "storage", "backups"),
		ArchiveRoot:       filepath.Join(root, "service", "storage", "archive"),
		GeneratedRoot:     filepath.Join(root, "data", "generated"),
		BoxStateRoot:      filepath.Join(root, "data", "box-state"),
		ObjectStorePath:   filepath.Join(root, "object-store"),
		MainDocumentsPath: filepath.Join(root, "main-documents"),
		StorageExportRoot: filepath.Join(root, "storage-export"),
		SocketPath:        filepath.Join(root, "run", "loomd.sock"),
		BoxPath:           filepath.Join(home, "LOOM Box"),
		BoxProfile:        box.ProfileMain,
		MigrationsDir:     "migrations",
	}
	normalized, diagnostics, err := NormalizeSpec(spec, testFacts(home))
	if err != nil {
		t.Fatalf("NormalizeSpec returned error: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("NormalizeSpec diagnostics = %#v", diagnostics)
	}
	return normalized, filepath.Join(root, "config", "install.yaml")
}

func testWorkspaceApplySpec(t *testing.T) (SetupSpec, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home", "loomadmin")
	spec := SetupSpec{
		NodeKey:        "macbook",
		DisplayName:    "MacBook",
		NodeKind:       "workspace",
		NodeRole:       "primary_workspace",
		RuntimeClass:   nodeprofiles.RuntimeWorkspaceFull,
		MainURL:        "http://10.44.0.2:8080",
		InstallMode:    InstallModeUser,
		ServiceManager: ServiceManagerNone,
		PackageMode:    PackageModeLocalBuild,
		HomeDir:        home,
		UserName:       "loomadmin",
		ConfigDir:      filepath.Join(root, "config"),
		DataDir:        filepath.Join(root, "data"),
		StateDir:       filepath.Join(root, "state"),
		LogDir:         filepath.Join(root, "logs"),
		BoxPath:        filepath.Join(home, "LOOM Box"),
		BoxProfile:     box.ProfileWorkspace,
		MigrationsDir:  "migrations",
	}
	return spec, filepath.Join(root, "config", "install.yaml")
}

func repoMigrationsDirForSetup(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "migrations"))
}

func changedPath(changes []ApplyChange, path string) bool {
	for _, change := range changes {
		if change.Path == path && change.Status == ApplyStatusChanged {
			return true
		}
	}
	return false
}

func statusPath(paths []SetupPathStatus, key string, path string) bool {
	for _, status := range paths {
		if status.Key == key && status.Path == path {
			return true
		}
	}
	return false
}

func skippedID(changes []ApplyChange, id string) bool {
	for _, change := range changes {
		if change.ID == id && change.Status == ApplyStatusAlreadySatisfied {
			return true
		}
	}
	return false
}

type setupEnrollmentMainFake struct {
	approveErr error
	healthErr  error
}

func (f *setupEnrollmentMainFake) CreateEnrollmentToken(context.Context, enrollmentflow.CreateTokenInput) (enrollmentflow.CreateTokenResult, error) {
	return enrollmentflow.CreateTokenResult{
		TokenID:    "node_enrollment_token_test",
		TokenHint:  "hintenroll",
		TokenValue: "node_enroll_secret",
		ExpiresAt:  fixedNow().Add(time.Hour),
	}, nil
}

func (f *setupEnrollmentMainFake) ApproveEnrollment(context.Context, string) (enrollmentflow.ApprovalResult, error) {
	if f.approveErr != nil {
		return enrollmentflow.ApprovalResult{}, f.approveErr
	}
	return enrollmentflow.ApprovalResult{
		EnrollmentRequestID: "node_enrollment_request_test",
		NodeID:              "node_test",
		NodeCredentialID:    "node_credential_test",
		CredentialHint:      "hintcred",
		CredentialToken:     "node_cred_secret",
		ApprovedAt:          fixedNow(),
	}, nil
}

func (f *setupEnrollmentMainFake) GetNodeHealth(context.Context, string) (enrollmentflow.NodeHealthResult, error) {
	if f.healthErr != nil {
		return enrollmentflow.NodeHealthResult{}, f.healthErr
	}
	seen := fixedNow()
	return enrollmentflow.NodeHealthResult{
		NodeID:        "node_test",
		NodeKey:       "macbook",
		PresenceState: "online",
		HeartbeatID:   "node_heartbeat_test",
		LastSeenAt:    &seen,
	}, nil
}

type setupEnrollmentTargetFake struct{}

func (f *setupEnrollmentTargetFake) EnsureNodeAgentInitialized(context.Context, enrollmentflow.NodeAgentInitInput) error {
	return nil
}

func (f *setupEnrollmentTargetFake) SubmitEnrollmentRequest(context.Context, string) (enrollmentflow.EnrollmentRequestResult, error) {
	return enrollmentflow.EnrollmentRequestResult{EnrollmentRequestID: "node_enrollment_request_test", Status: "pending"}, nil
}

func (f *setupEnrollmentTargetFake) ImportCredential(context.Context, enrollmentflow.CredentialImportInput) error {
	return nil
}

func (f *setupEnrollmentTargetFake) HeartbeatOnce(context.Context) (enrollmentflow.HeartbeatResult, error) {
	return enrollmentflow.HeartbeatResult{
		HeartbeatID:   "node_heartbeat_test",
		NodeID:        "node_test",
		PresenceState: "online",
		ReceivedAt:    fixedNow(),
	}, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func mustJSONForSetupTest(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal test JSON: %v", err)
	}
	return string(payload)
}
