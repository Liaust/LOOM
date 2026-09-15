package portal

import (
	"testing"
	"time"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/lane"
)

func TestBoxLaneTransferInspectActionIncludesCleanupCustodyEvidence(t *testing.T) {
	expires := time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)
	transfer := lane.TransferSummary{
		BatchID:                         "lane_cleanup_withheld",
		Status:                          lane.BatchStatusLocalCleanupWithheld,
		AttentionStatus:                 lane.AttentionStatusActive,
		LocalCleanupQuarantinePath:      "/tmp/loom-box/.loom/state/lane/cleanup/lane_cleanup_withheld",
		LocalCleanupQuarantineState:     lane.CleanupQuarantineRetainedForRecovery,
		LocalCleanupQuarantineBytes:     42,
		LocalCleanupQuarantineExpiresAt: &expires,
		LocalSafetyCleanupState:         lane.SafetyArtifactRemovedAfterSuccess,
		LocalSafetyRemovalReason:        lane.SafetyRemovalReasonMainPublished,
		QuarantinedLocalItems:           []string{"a.txt"},
		RestoredLocalItems:              []string{"b.txt"},
		RemovedLocalItems:               []string{"a.txt"},
	}
	status := box.Status{
		SchemaVersion: "loom.box.status.test",
		RootPath:      "/tmp/loom-box",
		Profile:       box.ProfileWorkspace,
	}

	action := NewBoxLaneTransferInspectAction(transfer, status)
	want := map[string]string{
		"cleanup_quarantine":            transfer.LocalCleanupQuarantinePath,
		"cleanup_quarantine_state":      lane.CleanupQuarantineRetainedForRecovery,
		"cleanup_quarantine_bytes":      "42",
		"cleanup_quarantine_expires_at": expires.Format(time.RFC3339),
		"safety_cleanup_state":          lane.SafetyArtifactRemovedAfterSuccess,
		"safety_cleanup_reason":         lane.SafetyRemovalReasonMainPublished,
		"quarantined_items":             "1",
		"restored_items":                "1",
		"removed_items":                 "1",
	}
	for key, value := range want {
		if got := action.RawDetails[key]; got != value {
			t.Fatalf("Lane cleanup detail %s = %q, want %q", key, got, value)
		}
	}

	attention := NewBoxLaneTransferAttentionAction(transfer, status, false)
	if attention.State != ActionAvailable {
		t.Fatalf("cleanup-withheld attention action = %#v", attention)
	}
}
