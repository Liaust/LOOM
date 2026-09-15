package storageretention

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestSafeToDeleteAcceptedDropzoneIsSafe(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassDropzoneCustody, storagecatalog.StorageClassDropzoneCustody, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateMetadataOnly)
	entry.SourceArea = storagecatalog.SourceAreaDropzone
	source := filepath.Join(t.TempDir(), "accepted.bin")
	if err := os.WriteFile(source, []byte("accepted"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ref.URI = source
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionSafe || !result.Safe || len(result.Reasons) == 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSafeToDeletePendingBackupIsNotSafe(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStatePending, storagecatalog.ProcessingStateBackupOnly)
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionNotSafe || result.Safe || len(result.Blockers) == 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSafeToDeleteMetadataOnlyBackupIsNotSafe(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateMetadataOnly)
	source := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(source, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	ref.URI = source
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionNotSafe || result.Safe || !strings.Contains(strings.Join(result.Blockers, " "), "metadata-only") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSafeToDeleteSkippedTooLargeIsNotSafe(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateExcluded)
	entry.Metadata = json.RawMessage(`{"backup_file_too_large":true}`)
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionNotSafe || result.Safe || !strings.Contains(strings.Join(result.Blockers, " "), "skipped_too_large") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSafeToDeleteRetainedZeroByteBackupIsSafe(t *testing.T) {
	entry, _ := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateBackupOnly)
	zero := int64(0)
	entry.SizeBytes = &zero
	entry.RetentionState = storagecatalog.RetentionStateRetained
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionSafe || !result.Safe {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSafeToDeleteExecutableModeWarns(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateBackupOnly)
	entry.FileClass = storagecatalog.FileClassCode
	entry.Metadata = json.RawMessage(`{"filesystem_observation":{"executable":true,"source_mode":493}}`)
	ref.RefKind = storagecatalog.PhysicalRefKindBackupArtifact
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	result, err := service.SafeToDelete(context.Background(), SafeToDeleteInput{Ref: entry.StorageEntryID})
	if err != nil {
		t.Fatalf("SafeToDelete returned error: %v", err)
	}
	if result.Decision != DecisionPartiallySafe || !result.Safe || !strings.Contains(strings.Join(result.Warnings, " "), "executable_mode_not_restored") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestFetchCopiesRegularFileToRequestedPath(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("retained payload"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassDropzoneCustody, storagecatalog.SourceAreaDropzone, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateMetadataOnly)
	ref.URI = source
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)
	destination := filepath.Join(t.TempDir(), "nested", "fetched.txt")

	result, err := service.Fetch(context.Background(), FetchInput{Ref: entry.StorageEntryID, DestinationPath: destination})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if result.BytesWritten != int64(len("retained payload")) {
		t.Fatalf("bytes written = %d", result.BytesWritten)
	}
	if got := readRetentionTestFile(t, destination); got != "retained payload" {
		t.Fatalf("destination content = %q", got)
	}
}

func TestFetchExtractsWatchedRootBackupArtifact(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "artifact.tar")
	writeRetentionTarArtifact(t, artifact, "content", []byte("backup payload"))
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateBackupOnly)
	ref.RefKind = storagecatalog.PhysicalRefKindBackupArtifact
	ref.URI = artifact
	ref.Metadata = json.RawMessage(`{"content_member":"content"}`)
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)
	destination := filepath.Join(t.TempDir(), "restored.md")

	result, err := service.Fetch(context.Background(), FetchInput{Ref: entry.StorageEntryID, DestinationPath: destination})
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	if result.BytesWritten != int64(len("backup payload")) || readRetentionTestFile(t, destination) != "backup payload" {
		t.Fatalf("unexpected fetch result: %#v content=%q", result, readRetentionTestFile(t, destination))
	}
}

func TestPrepareDestinationForReplaceKeepsRollbackBackup(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "existing.txt")
	if err := os.WriteFile(destination, []byte("original"), 0o644); err != nil {
		t.Fatalf("write destination: %v", err)
	}

	backupPath, restore, err := prepareDestinationForReplace(destination, true)
	if err != nil {
		t.Fatalf("prepareDestinationForReplace returned error: %v", err)
	}
	if !restore {
		t.Fatalf("expected restore backup")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("destination should have been moved aside, err=%v", err)
	}
	if got := readRetentionTestFile(t, backupPath); got != "original" {
		t.Fatalf("backup content = %q", got)
	}
	if err := os.Rename(backupPath, destination); err != nil {
		t.Fatalf("restore backup: %v", err)
	}
	if got := readRetentionTestFile(t, destination); got != "original" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestRestoreRecreatesFile(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.md")
	if err := os.WriteFile(source, []byte("restorable"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassMainDocument, storagecatalog.SourceAreaMainDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateMetadataOnly)
	entry.RetentionState = storagecatalog.RetentionStateSnapshot
	ref.URI = source
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)
	destination := filepath.Join(t.TempDir(), "restored.md")

	result, err := service.Restore(context.Background(), RestoreInput{Ref: entry.StorageEntryID, DestinationPath: destination})
	if err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if result.FetchResult.BytesWritten != int64(len("restorable")) || readRetentionTestFile(t, destination) != "restorable" {
		t.Fatalf("unexpected restore result: %#v content=%q", result, readRetentionTestFile(t, destination))
	}
}

func TestRecordTombstoneAfterAcceptanceCreatesRestorableTombstone(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("accepted"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassPrivateBackup, storagecatalog.SourceAreaDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateBackupOnly)
	ref.URI = source
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)

	tombstone, err := service.RecordTombstone(context.Background(), RecordTombstoneInput{
		Ref:           entry.StorageEntryID,
		TombstoneKind: storagecatalog.TombstoneKindSourceDeleted,
		Reason:        "unit deletion",
		CreatedBy:     "test",
	})
	if err != nil {
		t.Fatalf("RecordTombstone returned error: %v", err)
	}
	if tombstone.Tombstone.StorageEntryID != entry.StorageEntryID || tombstone.Tombstone.TombstoneKind != storagecatalog.TombstoneKindSourceDeleted {
		t.Fatalf("unexpected tombstone: %#v", tombstone)
	}
	destination := filepath.Join(t.TempDir(), "restored.txt")
	if _, err := service.Restore(context.Background(), RestoreInput{Ref: entry.StorageEntryID, DestinationPath: destination}); err != nil {
		t.Fatalf("Restore after tombstone returned error: %v", err)
	}
	if got := readRetentionTestFile(t, destination); got != "accepted" {
		t.Fatalf("restored content = %q", got)
	}
}

func TestMainDocumentProtectionRequiresRetentionAndCloud(t *testing.T) {
	root := t.TempDir()
	payload := []byte("protected main document\n")
	checksum := sha256Hex(payload)
	source := filepath.Join(root, "main-documents", "report.md")
	retained := filepath.Join(root, "storage-retention", "main-documents", "by-sha256", checksum)
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(retained), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(retained, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	verifiedAt := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	entry, sourceRef := retentionEntryFixture(storagecatalog.StorageClassMainDocument, storagecatalog.SourceAreaMainDocuments, storagecatalog.AvailabilityStateAvailable, storagecatalog.ProcessingStateMetadataOnly)
	size := int64(len(payload))
	entry.LogicalPath = "report.md"
	entry.CurrentViewPath = "main/Documents/report.md"
	entry.ChecksumHex = checksum
	entry.SizeBytes = &size
	entry.RetentionState = storagecatalog.RetentionStateSnapshot
	sourceRef.URI = source
	sourceRef.ContentAddress = "sha256:" + checksum
	retentionRef := sourceRef
	retentionRef.StoragePhysicalRefID = ids.NewStoragePhysicalRefID()
	retentionRef.RefKind = storagecatalog.PhysicalRefKindRetentionPayload
	retentionRef.URI = retained
	retentionRef.Metadata = json.RawMessage(`{"retention_verified_at":"` + verifiedAt.Format(time.RFC3339Nano) + `"}`)
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{sourceRef, retentionRef}},
	}}
	service := NewService(catalog)

	status, err := service.MainDocumentProtection(context.Background(), storagecatalog.MainDocumentProtectionInput{RelativePath: "report.md"})
	if err != nil {
		t.Fatalf("MainDocumentProtection returned error: %v", err)
	}
	if !status.RetainedPayloadVerified || status.SafeToDeleteSource || status.UnsafeReason == "" {
		t.Fatalf("without cloud coverage status = %#v", status)
	}

	cloudAt := verifiedAt.Add(time.Minute)
	status, err = service.MainDocumentProtection(context.Background(), storagecatalog.MainDocumentProtectionInput{
		RelativePath: "report.md",
		CloudCoverage: storagecatalog.MainDocumentCloudBackup{
			Confirmed:              true,
			Ref:                    "snapshot_test",
			VerifiedAt:             &cloudAt,
			CoversStorageRetention: true,
		},
	})
	if err != nil {
		t.Fatalf("MainDocumentProtection with cloud returned error: %v", err)
	}
	if !status.SafeToDeleteSource || !status.CloudBackupConfirmed {
		t.Fatalf("with cloud coverage status = %#v", status)
	}
}

func TestMainDocumentProtectionReturnsTombstoneInsteadOfUnknown(t *testing.T) {
	entry, ref := retentionEntryFixture(storagecatalog.StorageClassMainDocument, storagecatalog.SourceAreaMainDocuments, storagecatalog.AvailabilityStateTombstoned, storagecatalog.ProcessingStateMetadataOnly)
	entry.CurrentViewPath = "main/Documents/removed.md"
	catalog := &retentionCatalogFake{details: map[string]storagecatalog.EntryDetail{
		entry.StorageEntryID: {Entry: entry, PhysicalRefs: []storagecatalog.PhysicalRef{ref}},
	}}
	service := NewService(catalog)
	status, err := service.MainDocumentProtection(context.Background(), storagecatalog.MainDocumentProtectionInput{RelativePath: "removed.md"})
	if err != nil {
		t.Fatalf("MainDocumentProtection returned error: %v", err)
	}
	if !status.AcceptedByCatalog || status.AvailabilityState != storagecatalog.AvailabilityStateTombstoned {
		t.Fatalf("status = %#v, want tombstoned catalog hit", status)
	}
	if status.SafeToDeleteSource {
		t.Fatalf("tombstone without retained cloud payload must not be safe: %#v", status)
	}
}

type retentionCatalogFake struct {
	details    map[string]storagecatalog.EntryDetail
	tombstones []storagecatalog.Tombstone
}

func (f *retentionCatalogFake) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	if detail, ok := f.details[ref]; ok {
		return detail, nil
	}
	for _, detail := range f.details {
		entry := detail.Entry
		if entry.LogicalPath == ref || entry.CurrentViewPath == ref || entry.OriginalSourcePath == ref {
			return detail, nil
		}
	}
	return storagecatalog.EntryDetail{}, fmt.Errorf("not found: %s", ref)
}

func (f *retentionCatalogFake) InspectMainDocumentByPath(_ context.Context, relativePath string, _ storagecatalog.InspectOptions) (storagecatalog.EntryDetail, error) {
	viewPath := storagecatalog.MainDocumentViewPath(strings.Trim(strings.TrimSpace(relativePath), "/"))
	for _, detail := range f.details {
		entry := detail.Entry
		if entry.CurrentViewPath == viewPath || entry.LogicalPath == relativePath || entry.OriginalSourcePath == relativePath {
			return detail, nil
		}
	}
	return storagecatalog.EntryDetail{}, fmt.Errorf("not found: %s", relativePath)
}

func (f *retentionCatalogFake) ListEntries(context.Context, storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	entries := make([]storagecatalog.Entry, 0, len(f.details))
	for _, detail := range f.details {
		entries = append(entries, detail.Entry)
	}
	return entries, nil
}

func (f *retentionCatalogFake) RetentionStatus(ctx context.Context) (storagecatalog.RetentionStatus, error) {
	entries, err := f.ListEntries(ctx, storagecatalog.ListFilter{})
	if err != nil {
		return storagecatalog.RetentionStatus{}, err
	}
	return storagecatalog.RetentionStatus{Entries: len(entries), GeneratedAt: time.Now().UTC()}, nil
}

func (f *retentionCatalogFake) CreateTombstone(_ context.Context, input storagecatalog.TombstoneInput) (storagecatalog.Tombstone, error) {
	tombstone := storagecatalog.Tombstone{
		StorageTombstoneID: ids.NewStorageTombstoneID(),
		StorageEntryID:     input.StorageEntryID,
		TombstoneKind:      input.TombstoneKind,
		Reason:             input.Reason,
		CreatedBy:          input.CreatedBy,
		Metadata:           input.Metadata,
		CreatedAt:          time.Now().UTC(),
	}
	f.tombstones = append(f.tombstones, tombstone)
	return tombstone, nil
}

func retentionEntryFixture(storageClass, sourceArea, availabilityState, processingState string) (storagecatalog.Entry, storagecatalog.PhysicalRef) {
	size := int64(32)
	entryID := ids.NewStorageEntryID()
	checksum := strings64("a")
	entry := storagecatalog.Entry{
		StorageEntryID:     entryID,
		StorageClass:       storageClass,
		SourceArea:         sourceArea,
		OriginNodeKey:      "macbook",
		LogicalPath:        "Documents/report.md",
		OriginalSourcePath: "/Users/me/LOOM BOX/Documents/report.md",
		CurrentViewPath:    "macbook/Backups/Documents/current/report.md",
		ChecksumAlgorithm:  "sha256",
		ChecksumHex:        checksum,
		SizeBytes:          &size,
		FileClass:          storagecatalog.FileClassMarkdown,
		ProcessingState:    processingState,
		AvailabilityState:  availabilityState,
		RetentionState:     storagecatalog.RetentionStateNone,
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entryID,
		RefKind:              storagecatalog.PhysicalRefKindLocalPath,
		URI:                  "/tmp/retained",
		ContentAddress:       "sha256:" + checksum,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		CreatedAt:            entry.CreatedAt,
		UpdatedAt:            entry.UpdatedAt,
	}
	return entry, ref
}

func writeRetentionTarArtifact(t *testing.T, pathValue, member string, payload []byte) {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: member, Mode: 0o444, Size: int64(len(payload))}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write tar payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := os.WriteFile(pathValue, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar file: %v", err)
	}
}

func readRetentionTestFile(t *testing.T, pathValue string) string {
	t.Helper()
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatalf("read %s: %v", pathValue, err)
	}
	return string(payload)
}

func strings64(value string) string {
	return value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value +
		value + value + value + value + value + value + value + value
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
