package storagecatalog

import (
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/filesystemmeta"
)

func TestValidationHelpers(t *testing.T) {
	if !ValidStorageClass(StorageClassMainDocument) || ValidStorageClass("main") {
		t.Fatal("storage class validation mismatch")
	}
	if !ValidSourceArea(SourceAreaDocuments) || ValidSourceArea("files") {
		t.Fatal("source area validation mismatch")
	}
	if !ValidFileClass(FileClassMarkdown) || !ValidFileClass(FileClassOfficeDocument) || ValidFileClass("doc") {
		t.Fatal("file class validation mismatch")
	}
	if !ValidProcessingState(ProcessingStateMetadataOnly) || ValidProcessingState("done") {
		t.Fatal("processing state validation mismatch")
	}
	if !ValidAvailabilityState(AvailabilityStateAvailable) || ValidAvailabilityState("present") {
		t.Fatal("availability state validation mismatch")
	}
	if !ValidRetentionState(RetentionStateNone) || ValidRetentionState("keep") {
		t.Fatal("retention state validation mismatch")
	}
	if !ValidPhysicalRefKind(PhysicalRefKindLocalPath) || ValidPhysicalRefKind("file") {
		t.Fatal("physical ref kind validation mismatch")
	}
	if !ValidPhysicalRefStatus(PhysicalRefStatusAvailable) || ValidPhysicalRefStatus("ok") {
		t.Fatal("physical ref status validation mismatch")
	}
	if !ValidFilesystemObjectKind(filesystemmeta.ObjectKindDirectory) || ValidFilesystemObjectKind("folderish") {
		t.Fatal("filesystem object kind validation mismatch")
	}
	if !ValidFidelityRisk(filesystemmeta.FidelityRiskSkipped) || ValidFidelityRisk("maybe") {
		t.Fatal("fidelity risk validation mismatch")
	}
	if !ValidFidelityFindingSeverity(filesystemmeta.FindingSeverityWarning) || ValidFidelityFindingSeverity("bad") {
		t.Fatal("fidelity severity validation mismatch")
	}
	if !ValidFidelityFindingStatus(filesystemmeta.FindingStatusOpen) || ValidFidelityFindingStatus("done") {
		t.Fatal("fidelity finding status validation mismatch")
	}
}

func TestNormalizeRegisterEntryInputDefaults(t *testing.T) {
	input, err := normalizeRegisterEntryInput(RegisterEntryInput{
		StorageClass: StorageClassMainDocument,
		LogicalPath:  "Documents/Report.md",
		Metadata:     json.RawMessage(`{"source":"test"}`),
	})
	if err != nil {
		t.Fatalf("normalizeRegisterEntryInput returned error: %v", err)
	}
	if input.StorageEntryID == "" {
		t.Fatal("storage entry id was not generated")
	}
	if input.SourceArea != SourceAreaUnknown {
		t.Fatalf("source area = %q, want %q", input.SourceArea, SourceAreaUnknown)
	}
	if input.FileClass != FileClassMarkdown {
		t.Fatalf("file class = %q, want %q", input.FileClass, FileClassMarkdown)
	}
	if input.ProcessingState != ProcessingStateMetadataOnly {
		t.Fatalf("processing state = %q, want %q", input.ProcessingState, ProcessingStateMetadataOnly)
	}
	if input.AvailabilityState != AvailabilityStateAvailable {
		t.Fatalf("availability state = %q, want %q", input.AvailabilityState, AvailabilityStateAvailable)
	}
	if input.RetentionState != RetentionStateNone {
		t.Fatalf("retention state = %q, want %q", input.RetentionState, RetentionStateNone)
	}
}

func TestNormalizeRegisterEntryInputRejectsUnsafePath(t *testing.T) {
	_, err := normalizeRegisterEntryInput(RegisterEntryInput{
		StorageClass: StorageClassMainDocument,
		LogicalPath:  "../escape.md",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRegisterEntryInputRejectsNonObjectMetadata(t *testing.T) {
	_, err := normalizeRegisterEntryInput(RegisterEntryInput{
		StorageClass: StorageClassMainDocument,
		LogicalPath:  "Documents/Report.md",
		Metadata:     json.RawMessage(`[]`),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRegisterPhysicalRefInputDefaults(t *testing.T) {
	entry, err := normalizeRegisterEntryInput(RegisterEntryInput{
		StorageClass: StorageClassPrivateBackup,
		LogicalPath:  "macbook/Backups/Documents/a.txt",
	})
	if err != nil {
		t.Fatalf("normalize entry: %v", err)
	}

	ref, err := normalizeRegisterPhysicalRefInput(RegisterPhysicalRefInput{
		StorageEntryID: entry.StorageEntryID,
		RefKind:        PhysicalRefKindLocalPath,
		URI:            "/var/lib/loom/storage/a.txt",
	})
	if err != nil {
		t.Fatalf("normalizeRegisterPhysicalRefInput returned error: %v", err)
	}
	if ref.StoragePhysicalRefID == "" {
		t.Fatal("storage physical ref id was not generated")
	}
	if ref.Status != PhysicalRefStatusAvailable {
		t.Fatalf("status = %q, want %q", ref.Status, PhysicalRefStatusAvailable)
	}
}

func TestNormalizeRegisterFilesystemObservationInputDefaults(t *testing.T) {
	input, err := normalizeRegisterFilesystemObservationInput(RegisterFilesystemObservationInput{
		SourceArea:    SourceAreaDocuments,
		SourceNodeKey: "macbook",
		SourceRef:     "box-documents",
		LogicalPath:   "Documents/Empty Folder",
		ObjectKind:    filesystemmeta.ObjectKindDirectory,
		RawJSON:       json.RawMessage(`{"source":"unit"}`),
	})
	if err != nil {
		t.Fatalf("normalizeRegisterFilesystemObservationInput returned error: %v", err)
	}
	if input.StorageFilesystemObservationID == "" {
		t.Fatal("filesystem observation id was not generated")
	}
	if input.ObjectKind != filesystemmeta.ObjectKindDirectory {
		t.Fatalf("object kind = %q", input.ObjectKind)
	}
	if input.ObservedAt == nil {
		t.Fatal("observed_at was not defaulted")
	}
	if string(input.RawJSON) != `{"source":"unit"}` {
		t.Fatalf("raw json = %s", input.RawJSON)
	}
}

func TestNormalizeRegisterFilesystemObservationInputRejectsUnsafePath(t *testing.T) {
	_, err := normalizeRegisterFilesystemObservationInput(RegisterFilesystemObservationInput{
		SourceArea:  SourceAreaDocuments,
		LogicalPath: "../escape",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestNormalizeRegisterFidelityFindingInputDefaults(t *testing.T) {
	input, err := normalizeRegisterFidelityFindingInput(RegisterFidelityFindingInput{
		SourceArea:  SourceAreaDocuments,
		LogicalPath: "Documents/report.md",
		FindingKind: "permission_denied",
		DetailJSON:  json.RawMessage(`{"path":"Documents/report.md"}`),
	})
	if err != nil {
		t.Fatalf("normalizeRegisterFidelityFindingInput returned error: %v", err)
	}
	if input.StorageFidelityFindingID == "" {
		t.Fatal("fidelity finding id was not generated")
	}
	if input.Severity != filesystemmeta.FindingSeverityWarning {
		t.Fatalf("severity = %q", input.Severity)
	}
	if input.Status != filesystemmeta.FindingStatusOpen {
		t.Fatalf("status = %q", input.Status)
	}
}
