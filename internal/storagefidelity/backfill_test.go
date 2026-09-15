package storagefidelity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestBackfillDryRunDoesNotWriteObservationOrFinding(t *testing.T) {
	catalog := newBackfillCatalogFake(t, 0o755)
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}

	result, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceMainDocuments, DryRun: true})
	if err != nil {
		t.Fatalf("Backfill returned error: %v", err)
	}
	if !result.DryRun || result.Applied || result.Scanned != 1 || result.Observed != 1 || result.FindingsPlanned == 0 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if len(catalog.observations) != 0 || len(catalog.findings) != 0 {
		t.Fatalf("dry-run wrote catalog rows: observations=%d findings=%d", len(catalog.observations), len(catalog.findings))
	}
	if result.PayloadRewrites != 0 || result.ExportRefreshRequested {
		t.Fatalf("backfill must not rewrite payloads or refresh exports: %#v", result)
	}
}

func TestBackfillApplyRecordsObservationAndFindingOnce(t *testing.T) {
	catalog := newBackfillCatalogFake(t, 0o755)
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}

	first, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceMainDocuments, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("Backfill apply returned error: %v", err)
	}
	if !first.Applied || first.DryRun || first.FindingsRecorded == 0 || first.FindingsAlreadyPresent != 0 {
		t.Fatalf("unexpected apply result: %#v", first)
	}
	if len(catalog.observations) != 1 || len(catalog.findings) == 0 {
		t.Fatalf("apply rows = observations=%d findings=%d", len(catalog.observations), len(catalog.findings))
	}
	if catalog.observations[0].ObjectKind != filesystemmeta.ObjectKindRegularFile || !catalog.observations[0].Executable {
		t.Fatalf("observation did not capture executable regular file: %#v", catalog.observations[0])
	}
	if !backfillFakeHasFinding(catalog.findings, "executable_mode_not_restored") {
		t.Fatalf("expected executable finding, got %#v", catalog.findings)
	}

	second, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceMainDocuments, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("second Backfill apply returned error: %v", err)
	}
	if second.FindingsRecorded != 0 || second.FindingsAlreadyPresent == 0 || len(catalog.findings) != first.FindingsRecorded {
		t.Fatalf("second apply should not duplicate findings: result=%#v findings=%d", second, len(catalog.findings))
	}
}

func TestBackfillApplyRequiresYes(t *testing.T) {
	catalog := newBackfillCatalogFake(t, 0o644)
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}
	if _, err := service.Backfill(context.Background(), BackfillInput{Apply: true}); err == nil {
		t.Fatal("expected --apply without --yes to fail")
	}
}

func TestBackfillDoesNotInspectObjectBlobMetadataAsSourceFidelity(t *testing.T) {
	catalog := newBackfillCatalogFake(t, 0o755)
	objectPath := filepath.Join(t.TempDir(), "object-blob")
	if err := os.WriteFile(objectPath, []byte("internal protection bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.refs = append([]storagecatalog.PhysicalRef{{
		StoragePhysicalRefID: "object",
		StorageEntryID:       catalog.entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindObjectBlob,
		URI:                  objectPath,
		Status:               storagecatalog.PhysicalRefStatusAvailable,
		UpdatedAt:            time.Now().Add(time.Hour),
	}}, catalog.refs...)
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}
	result, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceMainDocuments, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].PhysicalRefKind == storagecatalog.PhysicalRefKindObjectBlob || result.Items[0].PhysicalPath == objectPath {
		t.Fatalf("fidelity selected internal object blob: %#v", result.Items)
	}
}

func TestBackfillDryRunUsesEmbeddedDirectoryObservationWithoutPhysicalRef(t *testing.T) {
	catalog := newBackfillDirectoryMetadataCatalogFake()
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}

	result, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceWatchedRoots, DryRun: true})
	if err != nil {
		t.Fatalf("Backfill returned error: %v", err)
	}
	if !result.DryRun || result.Applied || result.Scanned != 1 || result.Observed != 1 || result.FindingsPlanned != 0 {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if len(result.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Skipped || item.Error != "" {
		t.Fatalf("metadata-only directory should not be skipped: %#v", item)
	}
	if item.ObjectKind != filesystemmeta.ObjectKindDirectory || item.SourceRef != "loom_box__documents" {
		t.Fatalf("unexpected directory item: %#v", item)
	}
	if len(catalog.observations) != 0 || len(catalog.findings) != 0 {
		t.Fatalf("dry-run wrote catalog rows: observations=%d findings=%d", len(catalog.observations), len(catalog.findings))
	}
}

func TestBackfillApplyRegistersEmbeddedDirectoryObservation(t *testing.T) {
	catalog := newBackfillDirectoryMetadataCatalogFake()
	service := BackfillService{Catalog: catalog, Now: fixedBackfillNow}

	result, err := service.Backfill(context.Background(), BackfillInput{Source: BackfillSourceWatchedRoots, Apply: true, Yes: true})
	if err != nil {
		t.Fatalf("Backfill apply returned error: %v", err)
	}
	if !result.Applied || result.Scanned != 1 || result.Observed != 1 || result.FindingsRecorded != 0 {
		t.Fatalf("unexpected apply result: %#v", result)
	}
	if len(catalog.observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(catalog.observations))
	}
	observation := catalog.observations[0]
	if observation.ObjectKind != filesystemmeta.ObjectKindDirectory {
		t.Fatalf("object kind = %q, want %q", observation.ObjectKind, filesystemmeta.ObjectKindDirectory)
	}
	if observation.SourceRef != "loom_box__documents" {
		t.Fatalf("source ref = %q, want loom_box__documents", observation.SourceRef)
	}
	if len(catalog.findings) != 0 {
		t.Fatalf("directory metadata should not record findings: %#v", catalog.findings)
	}
}

type backfillCatalogFake struct {
	entry        storagecatalog.Entry
	refs         []storagecatalog.PhysicalRef
	observations []storagecatalog.RegisterFilesystemObservationInput
	findings     []storagecatalog.RegisterFidelityFindingInput
}

func newBackfillCatalogFake(t *testing.T, mode os.FileMode) *backfillCatalogFake {
	t.Helper()
	root := t.TempDir()
	file := filepath.Join(root, "run.sh")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	size := int64(len("#!/bin/sh\nexit 0\n"))
	entry := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassMainDocument,
		SourceArea:        storagecatalog.SourceAreaMainDocuments,
		OriginNodeKey:     "main",
		LogicalPath:       "scripts/run.sh",
		CurrentViewPath:   "main/Documents/scripts/run.sh",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassCode,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
		Metadata:          json.RawMessage(`{}`),
	}
	ref := storagecatalog.PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              storagecatalog.PhysicalRefKindRetentionPayload,
		URI:                  file,
		NodeKey:              "main",
		Status:               storagecatalog.PhysicalRefStatusAvailable,
	}
	return &backfillCatalogFake{entry: entry, refs: []storagecatalog.PhysicalRef{ref}}
}

func newBackfillDirectoryMetadataCatalogFake() *backfillCatalogFake {
	size := int64(0)
	entry := storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      storagecatalog.StorageClassPrivateBackup,
		SourceArea:        storagecatalog.SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		WatchedRootKey:    "loom_box__documents",
		LogicalPath:       ".loom-acceptance/storage-user-actions/slice-01/empty-dir",
		CurrentViewPath:   "macbook/Backups/Documents/current/.loom-acceptance/storage-user-actions/slice-01/empty-dir",
		SizeBytes:         &size,
		FileClass:         storagecatalog.FileClassDirectory,
		ProcessingState:   storagecatalog.ProcessingStateBackupOnly,
		AvailabilityState: storagecatalog.AvailabilityStateAvailable,
		RetentionState:    storagecatalog.RetentionStateRetained,
		Metadata: json.RawMessage(`{
			"source": "watched_roots.backup_batches",
			"item_kind": "directory",
			"client_item_metadata": {
				"filesystem_observation": {
					"kind": "directory",
					"source_mode": 493,
					"executable": true,
					"logical_size_bytes": 0
				}
			}
		}`),
	}
	return &backfillCatalogFake{entry: entry}
}

func (f *backfillCatalogFake) ListEntries(_ context.Context, filter storagecatalog.ListFilter) ([]storagecatalog.Entry, error) {
	if filter.SourceArea != "" && filter.SourceArea != f.entry.SourceArea {
		return nil, nil
	}
	return []storagecatalog.Entry{f.entry}, nil
}

func (f *backfillCatalogFake) InspectEntry(_ context.Context, ref string) (storagecatalog.EntryDetail, error) {
	return storagecatalog.EntryDetail{Entry: f.entry, PhysicalRefs: append([]storagecatalog.PhysicalRef(nil), f.refs...)}, nil
}

func (f *backfillCatalogFake) RegisterFilesystemObservation(_ context.Context, input storagecatalog.RegisterFilesystemObservationInput) (storagecatalog.FilesystemObservation, error) {
	f.observations = append(f.observations, input)
	id := ids.NewStorageFilesystemObservationID()
	return storagecatalog.FilesystemObservation{
		StorageFilesystemObservationID: id,
		StorageEntryID:                 &input.StorageEntryID,
		SourceArea:                     input.SourceArea,
		SourceNodeKey:                  input.SourceNodeKey,
		SourceRef:                      input.SourceRef,
		LogicalPath:                    input.LogicalPath,
		ObjectKind:                     input.ObjectKind,
		Executable:                     input.Executable,
		ObservedAt:                     fixedBackfillNow(),
	}, nil
}

func (f *backfillCatalogFake) RegisterFidelityFinding(_ context.Context, input storagecatalog.RegisterFidelityFindingInput) (storagecatalog.FidelityFinding, error) {
	f.findings = append(f.findings, input)
	id := ids.NewStorageFidelityFindingID()
	return storagecatalog.FidelityFinding{
		StorageFidelityFindingID: id,
		StorageEntryID:           &input.StorageEntryID,
		NodeKey:                  input.NodeKey,
		SourceArea:               input.SourceArea,
		SourceRef:                input.SourceRef,
		LogicalPath:              input.LogicalPath,
		Severity:                 input.Severity,
		FindingKind:              input.FindingKind,
		Summary:                  input.Summary,
		Status:                   input.Status,
		CreatedAt:                fixedBackfillNow(),
		UpdatedAt:                fixedBackfillNow(),
	}, nil
}

func (f *backfillCatalogFake) ListFidelityFindings(_ context.Context, filter storagecatalog.FidelityFindingFilter) ([]storagecatalog.FidelityFinding, error) {
	out := []storagecatalog.FidelityFinding{}
	for _, input := range f.findings {
		if filter.StorageEntryID != "" && input.StorageEntryID != filter.StorageEntryID {
			continue
		}
		id := ids.NewStorageFidelityFindingID()
		out = append(out, storagecatalog.FidelityFinding{
			StorageFidelityFindingID: id,
			StorageEntryID:           &input.StorageEntryID,
			FindingKind:              input.FindingKind,
			Status:                   input.Status,
		})
	}
	return out, nil
}

func fixedBackfillNow() time.Time {
	return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
}

func backfillFakeHasFinding(findings []storagecatalog.RegisterFidelityFindingInput, kind string) bool {
	for _, finding := range findings {
		if finding.FindingKind == kind {
			return true
		}
	}
	return false
}
