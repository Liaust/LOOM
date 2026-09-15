package portal

import (
	"testing"

	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/nodes"
)

func TestProtectedFolderSelectionMapsToRenderedRecord(t *testing.T) {
	record := backupcontracts.ProtectedFolderRecord{
		Key: "research", Lifecycle: backupcontracts.ProtectedFolderStatusProtected, Status: backupcontracts.ProtectedFolderStatusProtected, OwnerNodeKey: "workspace",
		Contract: backupcontracts.Contract{Key: "research", DisplayName: "Research Archive", OwnerNode: "workspace", Target: backupcontracts.TargetSpec{Scope: backupcontracts.TargetScopeOwnerNodeAbsolute, Path: "/Users/leonardo/Research"}},
	}
	state := ScreenState{Screen: ScreenNodes, Status: ScreenLoadLoaded, Data: ScreenData{Nodes: NodesData{
		Nodes:            []nodes.Node{{NodeKey: "workspace", DisplayName: "Studio Mac", Status: "active"}},
		ProtectedFolders: []backupcontracts.ProtectedFolderRecord{record},
	}}}
	items := ScreenSelectableItems(state)
	protectIndex, recordIndex := -1, -1
	for index, item := range items {
		switch {
		case item.RecordKind == portalActionDirectRecordKind && item.ActionID == "backup.protected_folder.protect":
			protectIndex = index
		case item.RecordKind == "protected_folder" && item.RecordRef == "research":
			recordIndex = index
		}
	}
	if protectIndex < 0 || recordIndex < 0 {
		t.Fatalf("missing protected-folder selection surfaces: %#v", items)
	}
	state.SelectedIndex = protectIndex
	protect, ok := SelectedPortalAction(state)
	if !ok || protect.Executor.Kind != PortalExecutorProtectedFolderProtect {
		t.Fatalf("protect row selected %#v ok=%v", protect, ok)
	}
	state.SelectedIndex = recordIndex
	inspect, ok := SelectedPortalAction(state)
	if !ok || inspect.Executor.Kind != PortalExecutorProtectedFolderInspect || inspect.TargetRef != "research" {
		t.Fatalf("record row shifted to %#v ok=%v", inspect, ok)
	}
}
