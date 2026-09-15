package portal

import (
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/lane"
)

func TestPortalLaneHomeAndStyleStatusMatrix(t *testing.T) {
	tests := []struct {
		status        string
		wantAttention bool
		wantTerminal  bool
		wantClass     statusClass
	}{
		{status: lane.BatchStatusCataloged, wantTerminal: true, wantClass: statusClassSuccess},
		{status: lane.BatchStatusPromotionFailed, wantAttention: true, wantTerminal: true, wantClass: statusClassDanger},
		{status: lane.BatchStatusAcceptedOnMain, wantAttention: true, wantTerminal: true, wantClass: statusClassProgress},
		{status: lane.BatchStatusCatalogFailed, wantAttention: true, wantTerminal: true, wantClass: statusClassDanger},
		{status: lane.BatchStatusSourceCleanupFailed, wantAttention: true, wantTerminal: true, wantClass: statusClassDanger},
		{status: lane.BatchStatusObsoleteAcceptedMigrationRequired, wantAttention: true, wantTerminal: true, wantClass: statusClassDanger},
		{status: lane.BatchStatusLocalCleanupWithheld, wantAttention: true, wantTerminal: true, wantClass: statusClassDanger},
		{status: lane.BatchStatusLocalCleanupDone, wantTerminal: true, wantClass: statusClassSuccess},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := homeStatusNeedsAttention(test.status); got != test.wantAttention {
				t.Fatalf("homeStatusNeedsAttention(%q) = %t, want %t", test.status, got, test.wantAttention)
			}
			if got := homeTerminalActivityStatus(test.status); got != test.wantTerminal {
				t.Fatalf("homeTerminalActivityStatus(%q) = %t, want %t", test.status, got, test.wantTerminal)
			}
			if got := classifyStatus(test.status); got != test.wantClass {
				t.Fatalf("classifyStatus(%q) = %s, want %s", test.status, got, test.wantClass)
			}
			snapshot := Snapshot{BoxStatus: box.Status{Profile: box.ProfileWorkspace, Lane: &lane.Status{LastTransfer: &lane.TransferSummary{BatchID: "lane_matrix", Status: test.status}}}}
			if running := buildHomeRunning(snapshot); len(running) != 0 {
				t.Fatalf("stopped Lane status appeared as running: %#v", running)
			}
			if recent := buildHomeRecent(snapshot); len(recent) != 1 || recent[0].Status != test.status {
				t.Fatalf("stopped Lane status missing from recent activity: %#v", recent)
			}
		})
	}
}

func TestPortalLaneTransferActionMatrix(t *testing.T) {
	status := box.Status{SchemaVersion: "loom.box.status.test", RootPath: "/tmp/loom-box", Profile: box.ProfileWorkspace}
	tests := []struct {
		status        string
		transport     lane.TransportMode
		wantRepair    bool
		wantAttention bool
	}{
		{status: lane.BatchStatusFailed, wantAttention: true},
		{status: lane.BatchStatusPromotionFailed, wantRepair: true, wantAttention: true},
		{status: lane.BatchStatusAcceptedOnMain, wantRepair: true, wantAttention: true},
		{status: lane.BatchStatusCatalogFailed, wantRepair: true, wantAttention: true},
		{status: lane.BatchStatusSourceCleanupFailed, wantRepair: true, wantAttention: true},
		{status: lane.BatchStatusObsoleteAcceptedMigrationRequired, wantAttention: true},
		{status: lane.BatchStatusLocalCleanupWithheld, transport: lane.TransportModeBundleSeed, wantRepair: true, wantAttention: true},
		{status: lane.BatchStatusCataloged},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			transfer := lane.TransferSummary{
				BatchID: "lane_matrix", Status: test.status, SelectedTransport: test.transport,
				AttentionStatus: lane.AttentionStatusActive,
			}
			repair := NewBoxLaneTransferRepairAction(transfer, status)
			if got := repair.State == ActionAvailable; got != test.wantRepair {
				t.Fatalf("repair availability = %t, want %t: %#v", got, test.wantRepair, repair)
			}
			if test.status == lane.BatchStatusSourceCleanupFailed {
				description := strings.ToLower(repair.Description)
				if !strings.Contains(description, "original keep-local or safe local-quarantine policy") || strings.Contains(description, "retry only") {
					t.Fatalf("source-cleanup repair description lost durable cleanup intent: %q", repair.Description)
				}
			}
			for _, archive := range []bool{false, true} {
				action := NewBoxLaneTransferAttentionAction(transfer, status, archive)
				if got := action.State == ActionAvailable; got != test.wantAttention {
					t.Fatalf("attention archive=%t availability = %t, want %t: %#v", archive, got, test.wantAttention, action)
				}
			}
			statusWithTransfer := status
			statusWithTransfer.Lane = &lane.Status{LastTransfer: &transfer}
			items := boxSelectableItems(ScreenState{Screen: ScreenBox, Data: ScreenData{Box: BoxData{Status: statusWithTransfer}}})
			var transferItem *SelectableItem
			for index := range items {
				if items[index].RecordKind == "box_lane_transfer" {
					transferItem = &items[index]
					break
				}
			}
			if transferItem == nil || transferItem.PrimaryAction == nil || !strings.Contains(transferItem.PrimaryAction.ID, ".inspect") {
				t.Fatalf("transfer inspect action missing: %#v", items)
			}
			gotRepair := false
			gotAck := false
			gotArchive := false
			for _, action := range transferItem.RelatedActions {
				gotRepair = gotRepair || strings.HasSuffix(action.ID, ".repair")
				gotAck = gotAck || strings.HasSuffix(action.ID, ".acknowledge-transfer")
				gotArchive = gotArchive || strings.HasSuffix(action.ID, ".archive-transfer")
			}
			if gotRepair != test.wantRepair || gotAck != test.wantAttention || gotArchive != test.wantAttention {
				t.Fatalf("selector actions repair=%t ack=%t archive=%t, want %t/%t: %#v", gotRepair, gotAck, gotArchive, test.wantRepair, test.wantAttention, transferItem.RelatedActions)
			}
		})
	}
}

func TestPortalLaneAttentionLanguagePreservesCustodySemantics(t *testing.T) {
	status := box.Status{RootPath: "/tmp/loom-box", Profile: box.ProfileWorkspace}
	for _, transfer := range []lane.TransferSummary{
		{BatchID: "lane_accepted", Status: lane.BatchStatusAcceptedOnMain, AttentionStatus: lane.AttentionStatusActive},
		{BatchID: "lane_withheld", Status: lane.BatchStatusLocalCleanupWithheld, AttentionStatus: lane.AttentionStatusActive},
	} {
		action := NewBoxLaneTransferAttentionAction(transfer, status, false)
		if strings.Contains(strings.ToLower(action.Description), "published") {
			t.Fatalf("obsolete publication language: %q", action.Description)
		}
		if transfer.Status == lane.BatchStatusLocalCleanupWithheld && !strings.Contains(action.Description, "promoted and cataloged") {
			t.Fatalf("cleanup-withheld description lost canonical custody: %q", action.Description)
		}
	}
}
