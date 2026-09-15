package nodeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/communication"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

func TestProtectedFolderReconcileAddUpdateRollbackStaleAndDelete(t *testing.T) {
	base := t.TempDir()
	store := Store{ConfigPath: filepath.Join(base, "config.json"), StatePath: filepath.Join(base, "state.json"), DataDir: filepath.Join(base, "data")}
	if err := store.EnsureDataDirs(); err != nil {
		t.Fatal(err)
	}
	manualPath := t.TempDir()
	config := Config{MainURL: "http://main.test", NodeKey: "workspace-test", DisplayName: "Workspace Test", Filesystem: filesystemconnector.Config{SafeRoots: []filesystemconnector.SafeRoot{filesystemconnector.DefaultSafeRoot("manual", manualPath)}}}
	if err := store.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	runtimeStore := noderuntime.NewStore(store.DataDir)
	manualConfig := watchedroots.NormalizeRootConfig(watchedroots.RootConfig{RootKey: "manual-root", SafeRootKey: "manual", RootRelativePath: "."})
	manualJSON, _ := json.Marshal(manualConfig)
	manualInstance := noderuntime.WorkerInstance{WorkerKey: noderuntime.WatchedRootWorkerKey("manual-root"), Kind: noderuntime.KindWatchedRoot, DisplayName: "Manual", Enabled: true, ConfigHash: watchedroots.ConfigHash(manualConfig), ConfigJSON: manualJSON}
	if err := runtimeStore.SaveInstance(manualInstance); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	sourceFile := filepath.Join(target, "source.txt")
	if err := os.WriteFile(sourceFile, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := desiredRootFixture(t, "photos", target, 10*1024*1024)
	payload1 := reconcilePayload(t, "node_test", 1, []backupcontracts.ProtectedFolderDesiredRoot{root})
	outcome := reconcileProtectedFolders(store, config, payload1)
	if outcome.AckStatus != communication.AckStatusCompleted || outcome.Ack.Evidence.Outcome != communication.ControlOutcomeCompleted {
		t.Fatalf("add outcome %#v", outcome)
	}
	loadedConfig, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	managedRoot, ok := filesystemconnector.FindSafeRoot(loadedConfig.Filesystem, root.SafeRootKey)
	if !ok || !managedRoot.PrivateBackupOnly {
		t.Fatalf("managed safe root missing: %#v", loadedConfig.Filesystem)
	}
	if _, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeStore.LoadInstance(manualInstance.WorkerKey); err != nil {
		t.Fatalf("unrelated worker removed: %v", err)
	}

	idempotent := reconcileProtectedFolders(store, loadedConfig, payload1)
	if idempotent.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("idempotent outcome %#v", idempotent)
	}
	driftedConfig := mustLoadNodeConfig(t, store)
	driftedConfig.Filesystem = filesystemconnector.RemoveSafeRoot(driftedConfig.Filesystem, root.SafeRootKey)
	if err := store.SaveConfig(driftedConfig); err != nil {
		t.Fatalf("remove managed safe root: %v", err)
	}
	if err := runtimeStore.DeleteInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); err != nil {
		t.Fatalf("remove managed worker: %v", err)
	}
	recovered := reconcileProtectedFolders(store, driftedConfig, payload1)
	if recovered.AckStatus != communication.AckStatusCompleted || recovered.Ack.Evidence.Outcome != communication.ControlOutcomeCompleted {
		t.Fatalf("idempotent drift recovery outcome %#v", recovered)
	}
	loadedConfig = mustLoadNodeConfig(t, store)
	if _, ok := filesystemconnector.FindSafeRoot(loadedConfig.Filesystem, root.SafeRootKey); !ok {
		t.Fatalf("idempotent replay did not reconstruct managed safe root: %#v", loadedConfig.Filesystem.SafeRoots)
	}
	if _, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); err != nil {
		t.Fatalf("idempotent replay did not reconstruct worker: %v", err)
	}
	updatedRoot := desiredRootFixture(t, "photos", target, 20*1024*1024)
	payload2 := reconcilePayload(t, "node_test", 2, []backupcontracts.ProtectedFolderDesiredRoot{updatedRoot})
	updated := reconcileProtectedFolders(store, loadedConfig, payload2)
	if updated.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("update outcome %#v", updated)
	}
	stateStore := watchedroots.NewStore(store.DataDir)
	localState, err := stateStore.LoadProtectedFolderReconcileState()
	if err != nil || localState.AppliedRevision != 2 {
		t.Fatalf("local state %#v err=%v", localState, err)
	}

	stale := reconcileProtectedFolders(store, mustLoadNodeConfig(t, store), payload1)
	if stale.AckStatus != communication.AckStatusCompleted || stale.Ack.ErrorCode != "reconcile.stale_revision" {
		t.Fatalf("stale outcome %#v", stale)
	}
	conflict := payload2
	conflict.Evidence.ConfigHash, _ = communication.ConfigHash([]string{"conflict"})
	conflicted := reconcileProtectedFolders(store, mustLoadNodeConfig(t, store), conflict)
	if conflicted.AckStatus != communication.AckStatusFailedPermanent || conflicted.Ack.ErrorCode != "reconcile.revision_conflict" {
		t.Fatalf("conflict outcome %#v", conflicted)
	}

	invalidRoot := updatedRoot
	invalidRoot.ContractKey = "bad"
	invalidRoot.ConfigHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	invalidPayload := reconcilePayload(t, "node_test", 3, []backupcontracts.ProtectedFolderDesiredRoot{updatedRoot, invalidRoot})
	invalid := reconcileProtectedFolders(store, mustLoadNodeConfig(t, store), invalidPayload)
	if invalid.AckStatus != communication.AckStatusFailedPermanent {
		t.Fatalf("invalid set outcome %#v", invalid)
	}
	localState, _ = stateStore.LoadProtectedFolderReconcileState()
	if localState.AppliedRevision != 2 {
		t.Fatalf("partial invalid set advanced state: %#v", localState)
	}

	emptyPayload := reconcilePayload(t, "node_test", 3, nil)
	removed := reconcileProtectedFolders(store, mustLoadNodeConfig(t, store), emptyPayload)
	if removed.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("delete outcome %#v", removed)
	}
	if _, err := runtimeStore.LoadInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); !os.IsNotExist(err) {
		t.Fatalf("managed worker still present: %v", err)
	}
	if _, err := runtimeStore.LoadInstance(manualInstance.WorkerKey); err != nil {
		t.Fatalf("unrelated worker removed: %v", err)
	}
	finalConfig := mustLoadNodeConfig(t, store)
	if _, ok := filesystemconnector.FindSafeRoot(finalConfig.Filesystem, root.SafeRootKey); ok {
		t.Fatalf("unreferenced managed safe root retained: %#v", finalConfig.Filesystem)
	}
	if _, ok := filesystemconnector.FindSafeRoot(finalConfig.Filesystem, "manual"); !ok {
		t.Fatalf("manual safe root removed: %#v", finalConfig.Filesystem)
	}
	if raw, err := os.ReadFile(sourceFile); err != nil || string(raw) != "preserve me" {
		t.Fatalf("source data changed: %q err=%v", raw, err)
	}
	if replay := reconcileProtectedFolders(store, finalConfig, payload2); replay.AckStatus != communication.AckStatusCompleted || replay.Ack.ErrorCode != "reconcile.stale_revision" {
		t.Fatalf("stale pre-delete message was not rejected after tombstone: %#v", replay)
	}
	restartedStore := Store{ConfigPath: store.ConfigPath, StatePath: store.StatePath, DataDir: store.DataDir}
	restartedConfig := mustLoadNodeConfig(t, restartedStore)
	if _, ok := filesystemconnector.FindSafeRoot(restartedConfig.Filesystem, root.SafeRootKey); ok {
		t.Fatalf("deleted safe root returned after restart: %#v", restartedConfig.Filesystem)
	}
	if _, err := noderuntime.NewStore(restartedStore.DataDir).LoadInstance(noderuntime.WatchedRootWorkerKey(root.RootKey)); !os.IsNotExist(err) {
		t.Fatalf("deleted worker returned after restart: %v", err)
	}
	if replay := reconcileProtectedFolders(store, finalConfig, emptyPayload); replay.AckStatus != communication.AckStatusCompleted {
		t.Fatalf("restart replay %#v", replay)
	}
}

func desiredRootFixture(t *testing.T, key, target string, maxBytes int64) backupcontracts.ProtectedFolderDesiredRoot {
	t.Helper()
	contract := backupcontracts.Normalize(backupcontracts.Contract{SchemaVersion: backupcontracts.SchemaVersion, Key: key, OwnerNode: "node_test", Status: backupcontracts.StatusActive, Target: backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: target}, Ignore: &backupcontracts.IgnorePolicy{Profile: "managed", DiscoverUserRules: true}, Backup: backupcontracts.BackupPolicy{Mode: backupcontracts.BackupModeIncrementalRaw, MaxFileBytes: maxBytes, MaxBatchBytes: maxBytes}})
	item, err := backupcontracts.WatchedRootItem(contract, "/box/.loom/contracts/backup/"+key+".yaml", backupcontracts.WatchPlanOptions{BoxRoot: "/box", BoxID: "box_test", OwnerNode: "node_test"})
	if err != nil {
		t.Fatal(err)
	}
	return backupcontracts.ProtectedFolderDesiredRoot{ContractKey: key, RootKey: item.BackendRootKey, SafeRootKey: item.SafeRootKey, DisplayName: item.DisplayName, TargetPath: target, Enabled: true, ConfigHash: item.ConfigHash, ConfigJSON: item.ConfigJSON, Backup: contract.Backup, Ignore: *contract.Ignore, Include: contract.Include, Exclude: contract.Exclude}
}

func reconcilePayload(t *testing.T, nodeID string, revision int64, roots []backupcontracts.ProtectedFolderDesiredRoot) backupcontracts.ProtectedFolderReconcilePayload {
	t.Helper()
	if roots == nil {
		roots = []backupcontracts.ProtectedFolderDesiredRoot{}
	}
	hash, err := communication.ConfigHash(roots)
	if err != nil {
		t.Fatal(err)
	}
	return backupcontracts.ProtectedFolderReconcilePayload{SchemaVersion: backupcontracts.ProtectedFolderControlSchemaVersion, Evidence: communication.NewDesiredStateEvidence(nodeID, revision, hash), Roots: roots}
}

func mustLoadNodeConfig(t *testing.T, store Store) Config {
	t.Helper()
	config, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return config
}
