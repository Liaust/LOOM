package storagefidelity

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
)

func TestEvaluatePermissionDeniedBlocksSafeDelete(t *testing.T) {
	entry := fidelityEntry(t, "blocked/secret.md", 12)
	entry.ProcessingState = storagecatalog.IndexingStatePermissionDenied
	entry.Metadata = rawJSON(t, map[string]any{"permission_denied": true})

	eval := EvaluateEntry(entry, nil, time.Now())
	if eval.SafeToDelete || eval.Decision != DecisionNotSafe || !hasFinding(eval, "permission_denied") {
		t.Fatalf("expected permission denied blocker, got %#v", eval)
	}
}

func TestEvaluateSkippedTooLargeBlocksSafeDelete(t *testing.T) {
	entry := fidelityEntry(t, "Documents/video.mov", 1024)
	entry.ProcessingState = storagecatalog.ProcessingStateExcluded
	entry.Metadata = rawJSON(t, map[string]any{"backup_file_too_large": true})

	eval := EvaluateEntry(entry, nil, time.Now())
	if eval.SafeToDelete || !hasFinding(eval, "skipped_too_large") || !hasFinding(eval, "processing_excluded") {
		t.Fatalf("expected skipped-too-large blocker, got %#v", eval)
	}
}

func TestEvaluateRetainedZeroByteFileSafe(t *testing.T) {
	entry := fidelityEntry(t, "Documents/empty.txt", 0)
	entry.RetentionState = storagecatalog.RetentionStateRetained

	eval := EvaluateEntry(entry, nil, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionSafe || !eval.PayloadRetained {
		t.Fatalf("expected retained zero-byte file to be safe, got %#v", eval)
	}
}

func TestEvaluateTombstonedMarkerDoesNotRequirePayload(t *testing.T) {
	entry := fidelityEntry(t, "Documents/old.txt.deleted.json", 128)
	entry.ProcessingState = storagecatalog.ProcessingStateMetadataOnly
	entry.AvailabilityState = storagecatalog.AvailabilityStateTombstoned

	eval := EvaluateEntry(entry, nil, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionSafe || hasFinding(eval, "metadata_only_payload") || hasFinding(eval, "payload_not_retained") {
		t.Fatalf("expected tombstoned marker to avoid payload blockers, got %#v", eval)
	}
}

func TestEvaluateMainDocumentsViewMetadataOnlyPayloadDoesNotBlock(t *testing.T) {
	size := int64(39)
	entry := storageview.ViewEntry{
		ViewPath:          "main/Documents/report.txt",
		EntryKind:         storageview.EntryKindFile,
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       "report.txt",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassText,
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateSnapshot,
		Metadata:          rawJSON(t, map[string]any{}),
	}

	eval := EvaluateViewEntry(entry, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionSafe || hasFinding(eval, "metadata_only_payload") {
		t.Fatalf("main Documents view entry should not be blocked as metadata-only payload: %#v", eval)
	}
}

func TestEvaluateArchiveViewMetadataOnlyPayloadDoesNotBlock(t *testing.T) {
	size := int64(39)
	entry := storageview.ViewEntry{
		ViewPath:          "main/Archive/report.txt",
		EntryKind:         storageview.EntryKindFile,
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassArchiveEntry,
		SourceArea:        storagecatalog.SourceAreaMainArchive,
		OriginNodeKey:     "main",
		LogicalPath:       "report.txt",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassText,
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
		Metadata:          rawJSON(t, map[string]any{}),
	}

	eval := EvaluateViewEntry(entry, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionSafe || hasFinding(eval, "metadata_only_payload") {
		t.Fatalf("archive view entry should not be blocked as metadata-only payload: %#v", eval)
	}
}

func TestEvaluateDropzoneViewBackupOnlyPayloadDoesNotBlock(t *testing.T) {
	size := int64(128)
	entry := storageview.ViewEntry{
		ViewPath:          "macbook/Dropzone/payload.bin",
		EntryKind:         storageview.EntryKindFile,
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassDropzoneCustody,
		SourceArea:        storagecatalog.SourceAreaDropzone,
		OriginNodeKey:     "macbook",
		LogicalPath:       "payload.bin",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassBinary,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateNone,
		Metadata:          rawJSON(t, map[string]any{}),
	}

	eval := EvaluateViewEntry(entry, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionSafe || hasFinding(eval, "metadata_only_payload") {
		t.Fatalf("Dropzone backup-only view entry should not be blocked as metadata-only payload: %#v", eval)
	}
}

func TestEvaluateDropzoneViewMetadataOnlyPayloadBlocks(t *testing.T) {
	size := int64(128)
	entry := storageview.ViewEntry{
		ViewPath:          "macbook/Dropzone/payload.bin",
		EntryKind:         storageview.EntryKindFile,
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassDropzoneCustody,
		SourceArea:        storagecatalog.SourceAreaDropzone,
		OriginNodeKey:     "macbook",
		LogicalPath:       "payload.bin",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassBinary,
		ProcessingState:   storagecatalog.ProcessingStateMetadataOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateNone,
		Metadata:          rawJSON(t, map[string]any{}),
	}

	eval := EvaluateViewEntry(entry, time.Now())
	if eval.SafeToDelete || eval.Decision != DecisionNotSafe || !hasFinding(eval, "metadata_only_payload") {
		t.Fatalf("Dropzone metadata-only payload should remain a fidelity blocker: %#v", eval)
	}
}

func TestEvaluateExecutableModeWarnsButDoesNotBlock(t *testing.T) {
	entry := fidelityEntry(t, "scripts/run.sh", 24)
	entry.FileClass = storagecatalog.FileClassCode
	entry.Metadata = rawJSON(t, map[string]any{
		"filesystem_observation": map[string]any{
			"executable":  true,
			"source_mode": 493,
		},
	})
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	}

	eval := EvaluateEntry(entry, []storagecatalog.PhysicalRef{ref}, time.Now())
	if !eval.SafeToDelete || eval.Decision != DecisionPartiallySafe || !hasFinding(eval, "executable_mode_not_restored") {
		t.Fatalf("expected executable warning without blocker, got %#v", eval)
	}
}

func TestEvaluateFalseFilesystemObservationBooleansDoNotCreateFindings(t *testing.T) {
	entry := fidelityEntry(t, "Documents/report.md", 24)
	entry.RetentionState = storagecatalog.RetentionStateRetained
	entry.Metadata = rawJSON(t, map[string]any{
		"filesystem_observation": map[string]any{
			"kind":               "regular_file",
			"generated_metadata": false,
			"permission_denied":  false,
			"executable":         false,
			"has_xattrs":         false,
			"has_acl":            false,
			"has_resource_fork":  false,
			"has_finder_tags":    false,
			"has_quarantine":     false,
			"is_package":         false,
			"source_mode":        420,
			"logical_size_bytes": 24,
			"xattr_names":        []string{},
		},
	})
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindBackupArtifact,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	}

	eval := EvaluateEntry(entry, []storagecatalog.PhysicalRef{ref}, time.Now())
	if len(eval.Findings) != 0 || eval.Decision != DecisionSafe {
		t.Fatalf("false observation booleans should not create findings, got %#v", eval)
	}
}

func fidelityEntry(t *testing.T, logicalPath string, size int64) storagecatalog.Entry {
	t.Helper()
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		LogicalPath:       logicalPath,
		CurrentViewPath:   "macbook/Backups/" + logicalPath,
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassText,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateNone,
		Metadata:          rawJSON(t, map[string]any{}),
	}
}

func rawJSON(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func hasFinding(eval Evaluation, kind string) bool {
	for _, finding := range eval.Findings {
		if finding.Kind == kind {
			return true
		}
	}
	return false
}
