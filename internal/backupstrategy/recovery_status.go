package backupstrategy

import (
	"context"
	"fmt"
	"path/filepath"
	"time"
)

// Display-only producer inventory. It is dated, redacted and never an apply
// token, a fresh cloud assertion, or a guarantee about unsent device edits.
type RecoveryInventorySummary struct {
	Schema                   string    `json:"schema"`
	ObservedAt               time.Time `json:"observed_at"`
	EvidenceBasis            string    `json:"evidence_basis"`
	OperationalAvailable     bool      `json:"operational_inventory_available"`
	OperationalVerified      int       `json:"operational_verified_packages"`
	OperationalFindings      int       `json:"operational_findings"`
	NotesAvailable           bool      `json:"notes_inventory_available"`
	NotesCaptures            int       `json:"notes_capture_entries"`
	NotesProtected           int       `json:"notes_with_saved_cloud_evidence"`
	NotesHeld                int       `json:"notes_held"`
	NotesKnownAllocatedBytes int64     `json:"notes_known_allocated_bytes"`
	NotesAccountingComplete  bool      `json:"notes_accounting_complete"`
}

// Called by the existing privileged capture/cloud publishers, never by status.
func PublishRecoveryInventorySummary(ctx context.Context, dataDir string, input NotesRecoveryRetentionInput) error {
	if _, err := exactAbsolutePath(dataDir); err != nil || dataDir == "/" {
		return fmt.Errorf("exact configured recovery data directory required")
	}
	summary := RecoveryInventorySummary{Schema: "loom.recovery_inventory_summary.v1", ObservedAt: time.Now().UTC(), EvidenceBasis: "producer_metadata_inventory_v1"}
	op, err := PlanOperationalPackageRetention(ctx, filepath.Join(dataDir, "backups", "operational"), nil)
	if err == nil {
		summary.OperationalAvailable = true
		summary.OperationalVerified = len(op.Keep) + len(op.Remove)
		summary.OperationalFindings = len(op.Findings)
	}
	plan, err := PlanNotesRecoveryRetention(ctx, input)
	if err == nil {
		summary.NotesAvailable = true
		summary.NotesCaptures = len(plan.Entries)
		summary.NotesKnownAllocatedBytes = plan.CandidateBytes + plan.RetainedBytes
		summary.NotesAccountingComplete = plan.AccountingComplete
		for _, entry := range plan.Entries {
			if entry.ProtectionSHA256 != "" {
				summary.NotesProtected++
			}
			if entry.Action == "hold" {
				summary.NotesHeld++
			}
		}
	}
	return writeRecoveryRecord(filepath.Join(dataDir, "backups"), "recovery-inventory.json", summary, 0644)
}

// Reads exactly one bounded metadata file. It cannot walk a recovery tree.
func ReadRecoveryInventorySummary(dataDir string) (RecoveryInventorySummary, error) {
	var summary RecoveryInventorySummary
	if _, err := exactAbsolutePath(dataDir); err != nil || dataDir == "/" {
		return summary, fmt.Errorf("exact configured recovery data directory required")
	}
	root := filepath.Join(dataDir, "backups")
	if err := checkNotesRecoveryRecordCustody(root, "recovery-inventory"); err != nil {
		return summary, err
	}
	if _, err := notesRecoveryReadJSON(filepath.Join(root, "recovery-inventory.json"), 16<<10, &summary); err != nil {
		return summary, err
	}
	if summary.Schema != "loom.recovery_inventory_summary.v1" || summary.ObservedAt.IsZero() || summary.EvidenceBasis != "producer_metadata_inventory_v1" {
		return summary, fmt.Errorf("invalid recovery inventory summary")
	}
	return summary, nil
}
