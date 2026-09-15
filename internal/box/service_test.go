package box

import (
	"path/filepath"
	"testing"

	"loom.local/loom/internal/backupcontracts"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/projectcontracts"
)

func TestGroupWatchRootsByOwnerKeepsMixedContractNodes(t *testing.T) {
	plan := WatchPlan{OwnerNode: "main", WatchedRoots: []projectcontracts.ProjectWatchedRootItem{
		{BackendRootKey: "loom_box__notes", OwnerNode: "main", SourceKinds: []string{"box_policy"}},
		{Key: "backup_mac", BackendRootKey: "loom_box_backup__mac", OwnerNode: "macbook", SourceKinds: []string{backupcontracts.SourceKindBoxBackupContract}},
		{Key: "backup_main", BackendRootKey: "loom_box_backup__main", OwnerNode: "main", SourceKinds: []string{backupcontracts.SourceKindBoxBackupContract}},
	}}
	groups, err := groupWatchRootsByOwner(plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || len(groups["main"].Items) != 2 || len(groups["macbook"].Items) != 1 {
		t.Fatalf("unexpected owner groups: %#v", groups)
	}
	if got := activeBackendKeysForSource(groups["main"].Items, backupcontracts.SourceKindBoxBackupContract); len(got) != 1 || got[0] != "loom_box_backup__main" {
		t.Fatalf("main backup source keys = %#v", got)
	}
}

func TestGroupWatchRootsByOwnerRetainsDeletedContractNodeForScopedStale(t *testing.T) {
	groups, err := groupWatchRootsByOwner(WatchPlan{OwnerNode: "main", WatchedRoots: []projectcontracts.ProjectWatchedRootItem{
		{BackendRootKey: "loom_box__notes", OwnerNode: "main"},
	}}, []WatchRootRegistration{{NodeID: "node_mac", OwnerNodeKey: "macbook", AreaKey: "backup_deleted", SourceKind: backupcontracts.SourceKindBoxBackupContract}})
	if err != nil {
		t.Fatal(err)
	}
	group := groups["macbook"]
	if group == nil || len(group.Items) != 0 || !group.SourceKinds[backupcontracts.SourceKindBoxBackupContract] {
		t.Fatalf("deleted contract node not retained for scoped stale: %#v", groups)
	}
}

func TestGroupWatchRootsByOwnerDoesNotSplitExistingNodeIDFromOwnerKey(t *testing.T) {
	plan := WatchPlan{OwnerNode: "main", WatchedRoots: []projectcontracts.ProjectWatchedRootItem{
		{Key: "backup_main", BackendRootKey: "loom_box_backup__main", OwnerNode: "main", SourceKinds: []string{backupcontracts.SourceKindBoxBackupContract}},
	}}
	existing := []WatchRootRegistration{{NodeID: "node_main", OwnerNodeKey: "main", AreaKey: "backup_main", SourceKind: backupcontracts.SourceKindBoxBackupContract}}
	groups, err := groupWatchRootsByOwner(plan, existing)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups["main"] == nil || groups["node_main"] != nil {
		t.Fatalf("existing registration created a duplicate owner group: %#v", groups)
	}
}

func TestBoxWatchScopePlansSkipsBackupOnlyRoots(t *testing.T) {
	root := initializedWatchBoxWithID(t, ProfileWorkspace, "box_test")
	plan, err := BuildWatchPlan(WatchStatusInput{Resolved: Resolved{RootPath: root, Profile: ProfileWorkspace, OwnerNode: "macbook"}})
	if err != nil {
		t.Fatalf("BuildWatchPlan returned error: %v", err)
	}
	backupItem, err := backupcontracts.WatchedRootItem(validContractWithBackupTarget("field-data", root), "", backupcontracts.WatchPlanOptions{BoxRoot: root, BoxID: "box_test", OwnerNode: "macbook"})
	if err != nil {
		t.Fatalf("WatchedRootItem returned error: %v", err)
	}
	plan.WatchedRoots = append(plan.WatchedRoots, backupItem)

	scopes, err := boxWatchScopePlans(plan, testNode("macbook"))
	if err != nil {
		t.Fatalf("boxWatchScopePlans returned error: %v", err)
	}
	if len(scopes) != 1 || scopes[0].AreaKey != AreaNotes {
		t.Fatalf("backup-only root should not create sync scope: %#v", scopes)
	}
}

func TestBoxWatchRegistrationAreaKeyAcceptsBackupContracts(t *testing.T) {
	item := projectcontracts.ProjectWatchedRootItem{
		Key:            "backup_field-data",
		BackendRootKey: "loom_box_backup__field-data",
		SyncMode:       agentwatchedroots.SyncModeNone,
	}
	area, err := boxWatchRegistrationAreaKey(item)
	if err != nil {
		t.Fatalf("boxWatchRegistrationAreaKey returned error: %v", err)
	}
	if area != "backup_field-data" {
		t.Fatalf("area = %q", area)
	}

	item.Key = ""
	area, err = boxWatchRegistrationAreaKey(item)
	if err != nil {
		t.Fatalf("boxWatchRegistrationAreaKey inferred backup area returned error: %v", err)
	}
	if area != "backup_field-data" {
		t.Fatalf("inferred area = %q", area)
	}

	if _, err := boxWatchRegistrationAreaKey(projectcontracts.ProjectWatchedRootItem{Key: "backup_bad key", BackendRootKey: "loom_box_backup__bad key"}); err == nil {
		t.Fatal("expected invalid backup area key to fail")
	}
}

func validContractWithBackupTarget(key string, root string) backupcontracts.Contract {
	return backupcontracts.Contract{
		SchemaVersion: backupcontracts.SchemaVersion,
		Key:           key,
		DisplayName:   "Field Data",
		OwnerNode:     "macbook",
		Status:        backupcontracts.StatusActive,
		Target: backupcontracts.TargetSpec{
			Scope: backupcontracts.TargetScopeOwnerNodeAbsolute,
			Path:  filepath.Join(root, "Field Data"),
		},
		Include: []string{"**/*"},
		Exclude: []string{".loom/**"},
		Backup: backupcontracts.BackupPolicy{
			Mode:          backupcontracts.BackupModeIncrementalRaw,
			MaxFileBytes:  backupcontracts.DefaultMaxFileBytes,
			MaxBatchBytes: backupcontracts.DefaultMaxBatchBytes,
		},
	}
}

func testNode(key string) nodes.Node {
	return nodes.Node{NodeID: "node_test", NodeKey: key}
}
