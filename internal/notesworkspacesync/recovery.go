package notesworkspacesync

import (
	"context"
	"errors"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesworkspace"
)

const RecoveryInventorySchema = "notes_workspace.recovery_enrollment.v1"

// PrepareRecoveryInventory refreshes only derived registration metadata. Unlike
// the root worker it performs no discovery scan, queue processing or delivery.
func (r *Runtime) PrepareRecoveryInventory(ctx context.Context) (EnrollmentInventory, error) {
	if r == nil || r.Knowledge == nil {
		return EnrollmentInventory{}, errors.New("Notes recovery preparation is not configured")
	}
	if _, err := r.Knowledge.ReconcileSourceRoots(ctx, knowledge.SourceRootReconcileInput{DiscoverFromStore: true}); err != nil {
		return EnrollmentInventory{}, err
	}
	return r.RecoveryInventory(ctx)
}

// RecoveryInventory resolves current membership only. It never opens the native
// replica, changes enrollment.json, processes edits or starts a delivery scan.
func (r *Runtime) RecoveryInventory(ctx context.Context) (EnrollmentInventory, error) {
	if r == nil || r.Config.Validate() != nil || r.DB == nil || r.Knowledge == nil || r.NodeKey == "" {
		return EnrollmentInventory{}, errors.New("Notes recovery preparation is not configured")
	}
	selections, previous, err := r.enrollmentSelections(ctx)
	if err != nil {
		return EnrollmentInventory{}, errors.New("Notes recovery membership unavailable")
	}
	out := EnrollmentInventory{SchemaVersion: RecoveryInventorySchema, Replica: r.Config.Replica,
		NodeKey: r.NodeKey, ConfigSHA256: enrollmentConfigHash(r.Config), Collections: []EnrollmentCollection{}}
	current := map[string]bool{}
	for _, selection := range selections {
		current[selection.SourceCollection] = true
		entry := EnrollmentCollection{RuntimeSelection: selection, Status: "held"}
		if selection.Archived {
			entry.Status = "reference_held"
			if r.Archives != nil && r.Config.LayoutPublication {
				err = r.Archives.WithRoot(ctx, selection.SourceCollection, func(archive knowledge.WorkspaceArchiveSource) error {
					entry.Status, entry.SourceGeneration = "reference", notesworkspace.ArchiveGeneration(archive.Generation)
					entry.ArchiveOperation, entry.ArchiveManifest = archive.ArchiveOperationID, archive.ManifestDigest
					for _, file := range archive.Files {
						if selection.contains(file.RelativePath) {
							entry.ReferenceFiles++
							entry.ReferenceBytes += file.SizeBytes
						}
					}
					return nil
				})
				if err != nil {
					entry = EnrollmentCollection{RuntimeSelection: selection, Status: "reference_held"}
				}
			}
			if entry.Status == "reference_held" {
				out.Holds = append(out.Holds, EnrollmentHold{SourceCollection: selection.SourceCollection, Reason: "archive_reference_unverified"})
			}
		} else {
			err = r.Knowledge.WithNotesRecoveryRoot(ctx, selection.SourceCollection, r.NodeKey, func(root knowledge.SourceRoot, generation string) error {
				entry.Status, entry.SourcePath, entry.SourceGeneration = "current_snapshot", root.SourcePath, generation
				entry.SourceIntakeStage, entry.RecoveryMissingDeferred = root.SourceIntakeStage, root.RecoveryMissingDeferred
				return nil
			})
			if err != nil {
				entry = EnrollmentCollection{RuntimeSelection: selection, Status: "held"}
				out.Holds = append(out.Holds, enrollmentAdmissionHold(selection.SourceCollection, err))
			}
		}
		out.Collections = append(out.Collections, entry)
	}
	for _, old := range previous.Collections {
		if !current[old.SourceCollection] {
			old.Status, old.SourcePath, old.SourceGeneration = "withdrawn", "", ""
			out.Collections = append(out.Collections, old)
		}
	}
	return out, ctx.Err()
}
