package storagecatalog

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/ids"
)

func TestServiceRegisterEntryCreatesCatalogEntry(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	entryID := ids.NewStorageEntryID()
	size := int64(42)
	entry := testEntry(now)
	entry.StorageEntryID = entryID
	entry.SizeBytes = &size

	store.expect("INSERT INTO storage.storage_entries", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:3], entryID, StorageClassPrivateBackup, SourceAreaDocuments)
		if got := fmt.Sprint(args[14].Value); got != "macbook/Backups/Documents/report.md" {
			t.Fatalf("logical path arg = %q", got)
		}
		if got := fmt.Sprint(args[21].Value); got != FileClassMarkdown {
			t.Fatalf("file class arg = %q", got)
		}
	}, entryDriverRow(entry))

	got, err := svc.RegisterEntry(context.Background(), RegisterEntryInput{
		StorageEntryID:    entryID,
		StorageClass:      StorageClassPrivateBackup,
		SourceArea:        SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "macbook/Backups/Documents/report.md",
		CurrentViewPath:   "macbook/Backups/Documents/report.md",
		SizeBytes:         &size,
		ProcessingState:   ProcessingStateBackupOnly,
		AvailabilityState: AvailabilityStateAvailable,
		Metadata:          json.RawMessage(`{"source":"unit"}`),
	})
	if err != nil {
		t.Fatalf("RegisterEntry returned error: %v", err)
	}
	if got.StorageEntryID != entryID || got.FileClass != FileClassMarkdown {
		t.Fatalf("unexpected entry %#v", got)
	}
	store.requireDone()
}

func TestServiceFindAvailablePhysicalRefsByContentAddresses(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)
	ref := testPhysicalRef(now, ids.NewStorageEntryID())
	ref.RefKind = PhysicalRefKindArchiveFile
	ref.URI = "/srv/loom/storage/archive/first/objects/sha256/aa/" + strings.Repeat("a", 64)
	ref.ContentAddress = "sha256:" + strings.Repeat("a", 64)
	store.expect("WHERE ref_kind = $1 AND status = $2 AND content_address IN ($3)", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, PhysicalRefKindArchiveFile, PhysicalRefStatusAvailable, ref.ContentAddress)
	}, physicalRefDriverRow(ref))

	got, err := svc.FindAvailablePhysicalRefsByContentAddresses(context.Background(), PhysicalRefKindArchiveFile, []string{ref.ContentAddress, ref.ContentAddress})
	if err != nil {
		t.Fatalf("FindAvailablePhysicalRefsByContentAddresses returned error: %v", err)
	}
	if selected, found := got[ref.ContentAddress]; !found || selected.StoragePhysicalRefID != ref.StoragePhysicalRefID || selected.URI != ref.URI {
		t.Fatalf("lookup = %#v, want %#v", got, ref)
	}
	store.requireDone()
}

func TestServiceFindAvailablePhysicalRefsByContentAddressesReportsMissing(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	store.expect("WHERE ref_kind = $1 AND status = $2 AND content_address IN ($3)", nil)

	got, err := svc.FindAvailablePhysicalRefsByContentAddresses(context.Background(), PhysicalRefKindArchiveFile, []string{"sha256:" + strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("FindAvailablePhysicalRefsByContentAddresses returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing content address reported as found: %#v", got)
	}
	store.requireDone()
}

func TestServiceRejectsNewDropzoneCustodyWithoutDatabaseMutation(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 5, 12, 30, 0, 0, time.UTC)
	size := int64(128)
	_, err := svc.RegisterDropzoneCustody(context.Background(), DropzoneCustodyInput{
		CustodyNodeKey:        "main",
		SessionID:             "drop_session_test",
		TransferID:            "drop_test_transfer",
		CustodyID:             "drop_custody_test",
		SourceNodeKey:         "macbook",
		SourceBoxID:           "box_workspace",
		RelativeDropzonePath:  "payload.bin",
		FileName:              "payload.bin",
		FileSizeBytes:         size,
		ChecksumAlgorithm:     "sha256",
		ChecksumValue:         strings.Repeat("a", 64),
		FinalCustodyPath:      "/var/lib/loom-box/.loom/storage/dropzone/macbook/2026-06-05/drop_test_transfer/payload.bin",
		CustodyMetadataPath:   "/var/lib/loom-box/.loom/storage/dropzone/macbook/2026-06-05/drop_test_transfer/custody.json",
		AcceptedAt:            now,
		UploadSessionJSONPath: "/var/lib/loom-box/.loom/state/dropzone/incoming/drop_session_test/session.json",
	})
	if !errors.Is(err, ErrDropzoneRetired) {
		t.Fatalf("RegisterDropzoneCustody error = %v, want ErrDropzoneRetired", err)
	}
	if _, err := normalizeRegisterEntryInput(RegisterEntryInput{
		StorageClass:       StorageClassDropzoneCustody,
		SourceArea:         SourceAreaDropzone,
		DropzoneTransferID: "drop_test_transfer",
	}); !errors.Is(err, ErrDropzoneRetired) {
		t.Fatalf("normalize dropzone entry error = %v, want ErrDropzoneRetired", err)
	}
	if _, err := normalizeRegisterPhysicalRefInput(RegisterPhysicalRefInput{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       ids.NewStorageEntryID(),
		RefKind:              PhysicalRefKindDropzoneFile,
	}); !errors.Is(err, ErrDropzoneRetired) {
		t.Fatalf("normalize dropzone ref error = %v, want ErrDropzoneRetired", err)
	}
	store.requireDone()
}

func TestHistoricalDropzoneCatalogFixtureRemainsDecodable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "historical", "dropzone-entry-v0.6.json"))
	if err != nil {
		t.Fatalf("read historical fixture: %v", err)
	}
	var detail EntryDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("decode historical fixture: %v", err)
	}
	if detail.Entry.StorageClass != StorageClassDropzoneCustody || detail.Entry.SourceArea != SourceAreaDropzone {
		t.Fatalf("historical entry classification = %s/%s", detail.Entry.StorageClass, detail.Entry.SourceArea)
	}
	if len(detail.PhysicalRefs) != 1 || detail.PhysicalRefs[0].RefKind != PhysicalRefKindDropzoneFile {
		t.Fatalf("historical physical refs = %#v", detail.PhysicalRefs)
	}
}

func TestServiceRegisterLaneCustodyCreatesEntryAndPhysicalRef(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 14, 12, 0, 0, 0, time.UTC)
	entryID := ids.NewStorageEntryID()
	refID := ids.NewStoragePhysicalRefID()
	size := int64(256)
	entry := Entry{
		StorageEntryID:     entryID,
		StorageClass:       StorageClassLaneCustody,
		SourceArea:         SourceAreaLane,
		OriginNodeKey:      "macbook",
		LogicalPath:        "2026-06-14/lane_20260614T120000Z/folder/report.md",
		OriginalSourcePath: "folder/report.md",
		CurrentViewPath:    "macbook/Lane/2026-06-14/lane_20260614T120000Z/folder/report.md",
		ChecksumAlgorithm:  "sha256",
		ChecksumHex:        strings.Repeat("c", 64),
		SizeBytes:          &size,
		FileClass:          FileClassMarkdown,
		ProcessingState:    ProcessingStateMetadataOnly,
		AvailabilityState:  AvailabilityStateAvailable,
		RetentionState:     RetentionStateNone,
		Metadata:           json.RawMessage(`{"source":"lane.rsync_accept"}`),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	ref := PhysicalRef{
		StoragePhysicalRefID: refID,
		StorageEntryID:       entryID,
		RefKind:              PhysicalRefKindLaneFile,
		URI:                  "/var/lib/loom/lane/accepted/macbook/2026-06-14/lane_20260614T120000Z/folder/report.md",
		NodeKey:              "main",
		ContentAddress:       "sha256:" + strings.Repeat("c", 64),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"lane.rsync_accept"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, "macbook/Lane/2026-06-14/lane_20260614T120000Z/folder/report.md")
	})
	store.expect("INSERT INTO storage.storage_entries", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:3], entryID, StorageClassLaneCustody, SourceAreaLane)
		if got := fmt.Sprint(args[4].Value); got != "macbook" {
			t.Fatalf("origin node key arg = %q", got)
		}
		if got := fmt.Sprint(args[22].Value); got != ProcessingStateBackupOnly {
			t.Fatalf("processing state arg = %q", got)
		}
		if got := fmt.Sprint(args[14].Value); got != "2026-06-14/lane_20260614T120000Z/folder/report.md" {
			t.Fatalf("logical path arg = %q", got)
		}
		if got := fmt.Sprint(args[16].Value); got != "macbook/Lane/2026-06-14/lane_20260614T120000Z/folder/report.md" {
			t.Fatalf("view path arg = %q", got)
		}
	}, entryDriverRow(entry))
	store.expect("INSERT INTO storage.storage_physical_refs", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:5], refID, entryID, "<nil>", PhysicalRefKindLaneFile, ref.URI)
		if got := fmt.Sprint(args[6].Value); got != "main" {
			t.Fatalf("node key arg = %q", got)
		}
		if got := fmt.Sprint(args[7].Value); got != ref.ContentAddress {
			t.Fatalf("content address arg = %q", got)
		}
	}, physicalRefDriverRow(ref))

	detail, err := svc.RegisterLaneCustody(context.Background(), LaneCustodyInput{
		StorageEntryID:       entryID,
		StoragePhysicalRefID: refID,
		CustodyNodeKey:       "main",
		SourceNodeKey:        "macbook",
		SourceBoxID:          "box_workspace",
		BatchID:              "lane_20260614T120000Z",
		RelativeLanePath:     "folder/report.md",
		FileSizeBytes:        size,
		ChecksumAlgorithm:    "sha256",
		ChecksumValue:        strings.Repeat("c", 64),
		FinalCustodyPath:     ref.URI,
		AcceptedAt:           now,
	})
	if err != nil {
		t.Fatalf("RegisterLaneCustody returned error: %v", err)
	}
	if detail.Entry.StorageEntryID != entryID || len(detail.PhysicalRefs) != 1 || detail.PhysicalRefs[0].StoragePhysicalRefID != refID {
		t.Fatalf("unexpected Lane detail: %#v", detail)
	}
	store.requireDone()
}

func TestServiceRegisterMainDocumentCreatesEntryAndPhysicalRef(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 5, 12, 45, 0, 0, time.UTC)
	entryID := ids.NewStorageEntryID()
	refID := ids.NewStoragePhysicalRefID()
	retentionRefID := ids.NewStoragePhysicalRefID()
	size := int64(64)
	entry := Entry{
		StorageEntryID:     entryID,
		StorageClass:       StorageClassMainDocument,
		SourceArea:         SourceAreaMainDocuments,
		OriginNodeKey:      "main",
		LogicalPath:        "Documents/report.md",
		OriginalSourcePath: "Documents/report.md",
		CurrentViewPath:    "main/Documents/Documents/report.md",
		ChecksumAlgorithm:  "sha256",
		ChecksumHex:        strings.Repeat("b", 64),
		SizeBytes:          &size,
		FileClass:          FileClassMarkdown,
		ProcessingState:    ProcessingStateMetadataOnly,
		AvailabilityState:  AvailabilityStateAvailable,
		RetentionState:     RetentionStateSnapshot,
		Metadata:           json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	pendingEntry := entry
	pendingEntry.RetentionState = RetentionStatePending
	ref := PhysicalRef{
		StoragePhysicalRefID: refID,
		StorageEntryID:       entryID,
		RefKind:              PhysicalRefKindLocalPath,
		URI:                  "/var/lib/loom/main-documents/Documents/report.md",
		NodeKey:              "main",
		ContentAddress:       "sha256:" + strings.Repeat("b", 64),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	retentionRef := PhysicalRef{
		StoragePhysicalRefID: retentionRefID,
		StorageEntryID:       entryID,
		RefKind:              PhysicalRefKindRetentionPayload,
		URI:                  "/var/lib/loom/storage-retention/main-documents/by-sha256/" + strings.Repeat("b", 64),
		NodeKey:              "main",
		ContentAddress:       "sha256:" + strings.Repeat("b", 64),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, "main/Documents/Documents/report.md")
	})
	store.expect("INSERT INTO storage.storage_entries", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:3], entryID, StorageClassMainDocument, SourceAreaMainDocuments)
		if got := fmt.Sprint(args[4].Value); got != "main" {
			t.Fatalf("origin node key arg = %q", got)
		}
		if got := fmt.Sprint(args[14].Value); got != "Documents/report.md" {
			t.Fatalf("logical path arg = %q", got)
		}
		if got := fmt.Sprint(args[16].Value); got != "main/Documents/Documents/report.md" {
			t.Fatalf("view path arg = %q", got)
		}
		if got := fmt.Sprint(args[24].Value); got != RetentionStatePending {
			t.Fatalf("retention arg = %q", got)
		}
	}, entryDriverRow(pendingEntry))
	store.expect("INSERT INTO storage.storage_physical_refs", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:5], refID, entryID, "<nil>", PhysicalRefKindLocalPath, ref.URI)
		if got := fmt.Sprint(args[6].Value); got != "main" {
			t.Fatalf("node key arg = %q", got)
		}
		if got := fmt.Sprint(args[7].Value); got != ref.ContentAddress {
			t.Fatalf("content address arg = %q", got)
		}
	}, physicalRefDriverRow(ref))
	store.expect("INSERT INTO storage.storage_physical_refs", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:5], retentionRefID, entryID, "<nil>", PhysicalRefKindRetentionPayload, retentionRef.URI)
		if got := fmt.Sprint(args[6].Value); got != "main" {
			t.Fatalf("retention node key arg = %q", got)
		}
		if got := fmt.Sprint(args[7].Value); got != retentionRef.ContentAddress {
			t.Fatalf("retention content address arg = %q", got)
		}
	}, physicalRefDriverRow(retentionRef))
	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entryID)
	}, entryDriverRow(pendingEntry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entryID)
	}, physicalRefDriverRow(ref), physicalRefDriverRow(retentionRef))
	version := EntryVersion{
		StorageEntryVersionID: ids.NewStorageEntryVersionID(),
		StorageEntryID:        entryID,
		VersionNumber:         1,
		ChecksumAlgorithm:     "sha256",
		ChecksumHex:           strings.Repeat("b", 64),
		SizeBytes:             &size,
		PhysicalRefID:         &retentionRefID,
		Metadata:              json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:             now,
	}
	store.expect("SELECT COALESCE(MAX(version_number), 0) + 1 FROM storage.storage_entry_versions", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entryID)
	}, []driver.Value{int64(1)})
	store.expect("INSERT INTO storage.storage_entry_versions", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[1:4], entryID, int64(1), "sha256")
		if got := fmt.Sprint(args[6].Value); got != retentionRefID {
			t.Fatalf("physical ref arg = %q", got)
		}
	}, entryVersionDriverRow(version))
	retention := RetentionEntry{
		StorageRetentionEntryID: ids.NewStorageRetentionEntryID(),
		StorageEntryID:          entryID,
		PolicyKey:               "main_documents.accepted_snapshot",
		RetentionState:          RetentionStateSnapshot,
		Metadata:                json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:               now,
		UpdatedAt:               now,
	}
	store.expect("INSERT INTO storage.retention_entries", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[1:4], entryID, "main_documents.accepted_snapshot", RetentionStateSnapshot)
	}, retentionEntryDriverRow(retention))
	store.expect("UPDATE storage.storage_entries SET retention_state = $2", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entryID, RetentionStateSnapshot)
	}, entryDriverRow(entry))

	detail, err := svc.RegisterMainDocument(context.Background(), MainDocumentInput{
		StorageEntryID:         entryID,
		StoragePhysicalRefID:   refID,
		RetentionPhysicalRefID: retentionRefID,
		NodeKey:                "main",
		BackingRoot:            "/var/lib/loom/main-documents",
		RelativePath:           "Documents/report.md",
		PhysicalPath:           ref.URI,
		RetentionPath:          retentionRef.URI,
		FileSizeBytes:          size,
		ChecksumAlgorithm:      "sha256",
		ChecksumValue:          strings.Repeat("b", 64),
		ModifiedAt:             now.Add(-time.Minute),
		ImportedAt:             now,
		RetentionVerifiedAt:    now,
	})
	if err != nil {
		t.Fatalf("RegisterMainDocument returned error: %v", err)
	}
	if detail.Entry.StorageEntryID != entryID || len(detail.PhysicalRefs) != 2 || detail.PhysicalRefs[1].StoragePhysicalRefID != retentionRefID {
		t.Fatalf("unexpected main document detail: %#v", detail)
	}
	store.requireDone()
}

func TestServiceRegisterMainDocumentRefreshesObservationForSameContentMTimeChange(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	oldObservedAt := time.Date(2025, 7, 20, 7, 15, 42, 877073600, time.UTC)
	newObservedAt := time.Date(2026, 6, 17, 10, 0, 27, 535906671, time.UTC)
	importedAt := time.Date(2026, 6, 17, 16, 35, 0, 0, time.UTC)
	entryID := ids.NewStorageEntryID()
	refID := ids.NewStoragePhysicalRefID()
	retentionRefID := ids.NewStoragePhysicalRefID()
	size := int64(180)
	checksum := strings.Repeat("c", 64)
	existing := Entry{
		StorageEntryID:     entryID,
		StorageClass:       StorageClassMainDocument,
		SourceArea:         SourceAreaMainDocuments,
		OriginNodeKey:      "main",
		LogicalPath:        "Documents sent from Mac/RainAI/Anby_MCP/.env",
		OriginalSourcePath: "Documents sent from Mac/RainAI/Anby_MCP/.env",
		CurrentViewPath:    "main/Documents/Documents sent from Mac/RainAI/Anby_MCP/.env",
		ChecksumAlgorithm:  "sha256",
		ChecksumHex:        checksum,
		SizeBytes:          &size,
		FileClass:          FileClassUnknown,
		ProcessingState:    ProcessingStateMetadataOnly,
		AvailabilityState:  AvailabilityStateAvailable,
		RetentionState:     RetentionStateSnapshot,
		Metadata: json.RawMessage(fmt.Sprintf(`{
			"schema_version":"storage.main_document.metadata.v0.6",
			"source":"mainstorage.importer",
			"node_key":"main",
			"relative_path":"Documents sent from Mac/RainAI/Anby_MCP/.env",
			"modified_at":%q,
			"import_state":"accepted"
		}`, oldObservedAt.Format(time.RFC3339Nano))),
		CreatedAt: importedAt.Add(-time.Hour),
		UpdatedAt: importedAt.Add(-time.Hour),
	}
	updated := existing
	updated.Metadata = json.RawMessage(fmt.Sprintf(`{
		"schema_version":"storage.main_document.metadata.v0.6",
		"source":"mainstorage.importer",
		"node_key":"main",
		"relative_path":"Documents sent from Mac/RainAI/Anby_MCP/.env",
		"physical_path":"/var/lib/loom/main-documents/Documents sent from Mac/RainAI/Anby_MCP/.env",
		"retention_path":"/var/lib/loom/storage-retention/main-documents/by-sha256/%s",
		"modified_at":%q,
		"imported_at":%q,
		"retention_verified_at":%q,
		"import_state":"accepted",
		"retention_snapshot":"accepted"
	}`, checksum, newObservedAt.Format(time.RFC3339Nano), importedAt.Format(time.RFC3339Nano), importedAt.Format(time.RFC3339Nano)))
	updated.UpdatedAt = importedAt
	ref := PhysicalRef{
		StoragePhysicalRefID: refID,
		StorageEntryID:       entryID,
		RefKind:              PhysicalRefKindLocalPath,
		URI:                  "/var/lib/loom/main-documents/Documents sent from Mac/RainAI/Anby_MCP/.env",
		NodeKey:              "main",
		ContentAddress:       "sha256:" + checksum,
		Status:               PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"mainstorage.importer"}`),
		CreatedAt:            importedAt.Add(-time.Hour),
		UpdatedAt:            importedAt.Add(-time.Hour),
	}
	retentionRef := ref
	retentionRef.StoragePhysicalRefID = retentionRefID
	retentionRef.RefKind = PhysicalRefKindRetentionPayload
	retentionRef.URI = "/var/lib/loom/storage-retention/main-documents/by-sha256/" + checksum

	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, "main/Documents/Documents sent from Mac/RainAI/Anby_MCP/.env")
	}, entryDriverRow(existing))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entryID)
	}, physicalRefDriverRow(ref), physicalRefDriverRow(retentionRef))
	store.expect("UPDATE storage.storage_entries SET origin_node_key = $2", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:5],
			entryID,
			"main",
			"Documents sent from Mac/RainAI/Anby_MCP/.env",
			"Documents sent from Mac/RainAI/Anby_MCP/.env",
			"main/Documents/Documents sent from Mac/RainAI/Anby_MCP/.env",
		)
	}, entryDriverRow(updated))

	detail, err := svc.RegisterMainDocument(context.Background(), MainDocumentInput{
		NodeKey:             "main",
		BackingRoot:         "/var/lib/loom/main-documents",
		RelativePath:        "Documents sent from Mac/RainAI/Anby_MCP/.env",
		PhysicalPath:        ref.URI,
		RetentionPath:       retentionRef.URI,
		FileSizeBytes:       size,
		ChecksumAlgorithm:   "sha256",
		ChecksumValue:       checksum,
		ModifiedAt:          newObservedAt,
		ImportedAt:          importedAt,
		RetentionVerifiedAt: importedAt,
	})
	if err != nil {
		t.Fatalf("RegisterMainDocument returned error: %v", err)
	}
	if got := mainDocumentMetadataModifiedAt(detail.Entry.Metadata); got == nil || !got.Equal(newObservedAt) {
		t.Fatalf("modified_at was not refreshed: %v", got)
	}
	if len(detail.PhysicalRefs) != 2 {
		t.Fatalf("physical refs changed unexpectedly: %#v", detail.PhysicalRefs)
	}
	store.requireDone()
}

func TestServiceListEntriesUsesFilters(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	entry := testEntry(time.Date(2026, 6, 5, 13, 0, 0, 0, time.UTC))

	store.expect("FROM storage.storage_entries WHERE 1 = 1 AND storage_class =", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args,
			StorageClassPrivateBackup,
			SourceAreaDocuments,
			"macbook",
			FileClassMarkdown,
			AvailabilityStateAvailable,
			int64(20),
		)
	}, entryDriverRow(entry))

	entries, err := svc.ListEntries(context.Background(), ListFilter{
		StorageClass:      StorageClassPrivateBackup,
		SourceArea:        SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		FileClass:         FileClassMarkdown,
		AvailabilityState: AvailabilityStateAvailable,
		Limit:             20,
	})
	if err != nil {
		t.Fatalf("ListEntries returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].StorageEntryID != entry.StorageEntryID {
		t.Fatalf("entries = %#v, want one matching entry", entries)
	}
	store.requireDone()
}

func TestServiceListEntriesUsesExactEscapedPathPrefixes(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	entry := testEntry(time.Date(2026, 8, 27, 19, 0, 0, 0, time.UTC))
	entry.CurrentViewPath = "macbook/Backups/Documents/current/Taxes_100%/receipt.txt"

	store.expect("current_view_path = $1 OR current_view_path LIKE $2 ESCAPE E'\\\\'", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args,
			"macbook/Backups/Documents/current/Taxes_100%",
			`macbook/Backups/Documents/current/Taxes\_100\%/%`,
			int64(17),
		)
	}, entryDriverRow(entry))

	entries, err := svc.ListEntries(context.Background(), ListFilter{
		PathPrefixes: []string{
			"macbook/Backups/Documents/current/Taxes_100%",
			"macbook/Backups/Documents/current/Taxes_100%",
		},
		Limit: 17,
	})
	if err != nil {
		t.Fatalf("ListEntries returned error: %v", err)
	}
	if len(entries) != 1 || entries[0].CurrentViewPath != entry.CurrentViewPath {
		t.Fatalf("entries = %#v, want one exact-prefix entry", entries)
	}
	store.requireDone()
}

func TestServiceListEntriesRejectsUnsafePathPrefixBeforeQuery(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	if _, err := svc.ListEntries(context.Background(), ListFilter{PathPrefixes: []string{"../outside"}}); err == nil {
		t.Fatal("ListEntries accepted an escaping path prefix")
	}
	store.requireDone()
}

func TestServiceListAllEntriesPaginatesBeyondSinglePage(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 6, 5, 13, 30, 0, 0, time.UTC)
	firstPage := make([][]driver.Value, 0, maxListEntriesLimit)
	for i := 0; i < maxListEntriesLimit; i++ {
		entry := testEntry(now)
		entry.StorageEntryID = fmt.Sprintf("storage_entry_%04d", i)
		entry.LogicalPath = fmt.Sprintf("doc-%04d.md", i)
		entry.CurrentViewPath = entry.LogicalPath
		firstPage = append(firstPage, entryDriverRow(entry))
	}
	last := testEntry(now)
	last.StorageEntryID = "storage_entry_5000"
	last.LogicalPath = "doc-5000.md"
	last.CurrentViewPath = last.LogicalPath

	store.expect("ORDER BY updated_at DESC, logical_path, storage_entry_id LIMIT $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, maxListEntriesLimit)
	}, firstPage...)
	store.expect("ORDER BY updated_at DESC, logical_path, storage_entry_id LIMIT $1 OFFSET $2", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, maxListEntriesLimit, maxListEntriesLimit)
	}, entryDriverRow(last))

	entries, err := svc.ListAllEntries(context.Background(), ListFilter{})
	if err != nil {
		t.Fatalf("ListAllEntries returned error: %v", err)
	}
	if len(entries) != maxListEntriesLimit+1 {
		t.Fatalf("entries = %d, want %d", len(entries), maxListEntriesLimit+1)
	}
	if got := entries[len(entries)-1].StorageEntryID; got != "storage_entry_5000" {
		t.Fatalf("last entry = %q, want storage_entry_5000", got)
	}
	store.requireDone()
}

func TestServiceInspectEntryReturnsPhysicalRefs(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	entry := testEntry(now)
	ref := testPhysicalRef(now, entry.StorageEntryID)

	store.expect("FROM storage.storage_entries WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.StorageEntryID)
	}, entryDriverRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.StorageEntryID)
	}, physicalRefDriverRow(ref))

	detail, err := svc.InspectEntry(context.Background(), entry.StorageEntryID)
	if err != nil {
		t.Fatalf("InspectEntry returned error: %v", err)
	}
	if detail.Entry.StorageEntryID != entry.StorageEntryID {
		t.Fatalf("entry id = %q, want %q", detail.Entry.StorageEntryID, entry.StorageEntryID)
	}
	if len(detail.PhysicalRefs) != 1 || detail.PhysicalRefs[0].StoragePhysicalRefID != ref.StoragePhysicalRefID {
		t.Fatalf("physical refs = %#v", detail.PhysicalRefs)
	}
	store.requireDone()
}

func TestServiceListEntryDetailsReturnsBulkPhysicalRefs(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 6, 5, 14, 30, 0, 0, time.UTC)
	first := testEntry(now)
	second := testEntry(now)
	second.StorageEntryID = ids.NewStorageEntryID()
	second.LogicalPath = "macbook/Backups/Documents/second.md"
	second.CurrentViewPath = second.LogicalPath
	firstRef := testPhysicalRef(now, first.StorageEntryID)
	secondRef := testPhysicalRef(now, second.StorageEntryID)

	store.expect("FROM storage.storage_entries WHERE storage_entry_id IN ($1, $2)", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, first.StorageEntryID, second.StorageEntryID)
	}, entryDriverRow(first), entryDriverRow(second))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id IN ($1, $2)", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, first.StorageEntryID, second.StorageEntryID)
	}, physicalRefDriverRow(firstRef), physicalRefDriverRow(secondRef))

	details, err := svc.ListEntryDetails(context.Background(), []string{first.StorageEntryID, second.StorageEntryID, first.StorageEntryID})
	if err != nil {
		t.Fatalf("ListEntryDetails returned error: %v", err)
	}
	if len(details) != 2 {
		t.Fatalf("details = %d, want 2: %#v", len(details), details)
	}
	if got := details[first.StorageEntryID].PhysicalRefs; len(got) != 1 || got[0].StoragePhysicalRefID != firstRef.StoragePhysicalRefID {
		t.Fatalf("first refs = %#v", got)
	}
	if got := details[second.StorageEntryID].PhysicalRefs; len(got) != 1 || got[0].StoragePhysicalRefID != secondRef.StoragePhysicalRefID {
		t.Fatalf("second refs = %#v", got)
	}
	store.requireDone()
}

func TestServiceInspectMainDocumentByPathReturnsTombstonedEntry(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	entry := testEntry(now)
	entry.StorageClass = StorageClassMainDocument
	entry.SourceArea = SourceAreaMainDocuments
	entry.LogicalPath = "removed.pdf"
	entry.CurrentViewPath = "main/Documents/removed.pdf"
	entry.AvailabilityState = AvailabilityStateTombstoned
	ref := testPhysicalRef(now, entry.StorageEntryID)
	ref.RefKind = PhysicalRefKindRetentionPayload

	store.expect("WHERE ( current_view_path = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, "main/Documents/removed.pdf")
	}, entryDriverRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.StorageEntryID)
	}, physicalRefDriverRow(ref))

	detail, err := svc.InspectMainDocumentByPath(context.Background(), "removed.pdf", InspectOptions{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("InspectMainDocumentByPath returned error: %v", err)
	}
	if detail.Entry.AvailabilityState != AvailabilityStateTombstoned {
		t.Fatalf("availability = %q, want tombstoned", detail.Entry.AvailabilityState)
	}
	store.requireDone()
}

func TestServiceInspectByViewPathExcludesDeletedRowsByDefault(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC)
	entry := testEntry(now)
	ref := testPhysicalRef(now, entry.StorageEntryID)

	store.expect("AND deleted_at IS NULL AND availability_state NOT IN ('deleted', 'tombstoned')", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.CurrentViewPath)
	}, entryDriverRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.StorageEntryID)
	}, physicalRefDriverRow(ref))

	detail, err := svc.InspectByViewPath(context.Background(), entry.CurrentViewPath, InspectOptions{})
	if err != nil || detail.Entry.StorageEntryID != entry.StorageEntryID {
		t.Fatalf("InspectByViewPath detail=%#v err=%v", detail, err)
	}
	store.requireDone()
}

func TestServiceInspectByViewPathIncludeDeletedOmitsActiveOnlyClause(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)
	now := time.Date(2026, 8, 27, 20, 5, 0, 0, time.UTC)
	entry := testEntry(now)
	entry.AvailabilityState = AvailabilityStateTombstoned

	store.expect("WHERE ( current_view_path = $1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, entry.CurrentViewPath)
	}, entryDriverRow(entry))
	store.expect("FROM storage.storage_physical_refs WHERE storage_entry_id = $1", nil)

	detail, err := svc.InspectByViewPath(context.Background(), entry.CurrentViewPath, InspectOptions{IncludeDeleted: true})
	if err != nil || detail.Entry.AvailabilityState != AvailabilityStateTombstoned {
		t.Fatalf("InspectByViewPath detail=%#v err=%v", detail, err)
	}
	store.requireDone()
}

func TestServiceInspectByViewPathRejectsEscapingPathBeforeQuery(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	if _, err := NewService(db).InspectByViewPath(context.Background(), "../outside", InspectOptions{}); err == nil {
		t.Fatal("InspectByViewPath accepted an escaping path")
	}
	store.requireDone()
}

func TestServiceRegisterFilesystemObservationCreatesObservationWithoutEntry(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	mode := 0o755
	observationID := ids.NewStorageFilesystemObservationID()
	observation := FilesystemObservation{
		StorageFilesystemObservationID: observationID,
		SourceArea:                     SourceAreaDocuments,
		SourceNodeKey:                  "macbook",
		SourceRef:                      "box-documents",
		LogicalPath:                    "Documents/Empty Folder",
		ObjectKind:                     filesystemmeta.ObjectKindDirectory,
		SourceMode:                     &mode,
		Executable:                     true,
		LogicalSizeBytes:               0,
		Risks:                          []string{filesystemmeta.FidelityRiskMetadataOnly},
		ObservedAt:                     now,
		RawJSON:                        json.RawMessage(`{"source":"unit"}`),
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}

	store.expect("INSERT INTO storage.storage_entry_filesystem_observations", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:8], observationID, "<nil>", SourceAreaDocuments, "<nil>", "macbook", "box-documents", "Documents/Empty Folder", filesystemmeta.ObjectKindDirectory)
		if got := fmt.Sprint(args[8].Value); got != "493" {
			t.Fatalf("source mode arg = %q, want 493", got)
		}
		if got := driverJSONValue(args[35].Value); got != `["metadata_only"]` {
			t.Fatalf("risks json arg = %q", got)
		}
	}, filesystemObservationDriverRow(observation))

	got, err := svc.RegisterFilesystemObservation(context.Background(), RegisterFilesystemObservationInput{
		StorageFilesystemObservationID: observationID,
		SourceArea:                     SourceAreaDocuments,
		SourceNodeKey:                  "macbook",
		SourceRef:                      "box-documents",
		LogicalPath:                    "Documents/Empty Folder",
		ObjectKind:                     filesystemmeta.ObjectKindDirectory,
		SourceMode:                     &mode,
		Executable:                     true,
		Risks:                          []string{filesystemmeta.FidelityRiskMetadataOnly},
		ObservedAt:                     &now,
		RawJSON:                        json.RawMessage(`{"source":"unit"}`),
	})
	if err != nil {
		t.Fatalf("RegisterFilesystemObservation returned error: %v", err)
	}
	if got.StorageFilesystemObservationID != observationID || got.StorageEntryID != nil || got.ObjectKind != filesystemmeta.ObjectKindDirectory {
		t.Fatalf("unexpected observation %#v", got)
	}
	store.requireDone()
}

func TestServiceListFilesystemObservationsFiltersBySource(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 19, 10, 15, 0, 0, time.UTC)
	observation := FilesystemObservation{
		StorageFilesystemObservationID: ids.NewStorageFilesystemObservationID(),
		SourceArea:                     SourceAreaDocuments,
		SourceNodeKey:                  "macbook",
		SourceRef:                      "box-documents",
		LogicalPath:                    "Documents/report.md",
		ObjectKind:                     filesystemmeta.ObjectKindRegularFile,
		LogicalSizeBytes:               12,
		ObservedAt:                     now,
		RawJSON:                        json.RawMessage(`{}`),
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}

	store.expect("FROM storage.storage_entry_filesystem_observations WHERE 1 = 1", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, SourceAreaDocuments, "macbook", "box-documents", filesystemmeta.ObjectKindRegularFile, int64(25))
	}, filesystemObservationDriverRow(observation))

	got, err := svc.ListFilesystemObservations(context.Background(), FilesystemObservationFilter{
		SourceArea:    SourceAreaDocuments,
		SourceNodeKey: "macbook",
		SourceRef:     "box-documents",
		ObjectKind:    filesystemmeta.ObjectKindRegularFile,
		Limit:         25,
	})
	if err != nil {
		t.Fatalf("ListFilesystemObservations returned error: %v", err)
	}
	if len(got) != 1 || got[0].LogicalPath != "Documents/report.md" {
		t.Fatalf("unexpected observations %#v", got)
	}
	store.requireDone()
}

func TestServiceRegisterAndResolveFidelityFinding(t *testing.T) {
	db, store := newStorageCatalogFakeDB(t)
	defer db.Close()
	svc := NewService(db)

	now := time.Date(2026, 6, 19, 10, 30, 0, 0, time.UTC)
	findingID := ids.NewStorageFidelityFindingID()
	observationID := ids.NewStorageFilesystemObservationID()
	finding := FidelityFinding{
		StorageFidelityFindingID:       findingID,
		StorageFilesystemObservationID: &observationID,
		NodeKey:                        "macbook",
		SourceArea:                     SourceAreaDocuments,
		SourceRef:                      "box-documents",
		LogicalPath:                    "Documents/private.pdf",
		Severity:                       filesystemmeta.FindingSeverityError,
		FindingKind:                    "permission_denied",
		Summary:                        "Source file could not be read.",
		DetailJSON:                     json.RawMessage(`{"errno":"EACCES"}`),
		Status:                         filesystemmeta.FindingStatusOpen,
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}

	store.expect("INSERT INTO storage.storage_fidelity_findings", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args[:11], findingID, observationID, "<nil>", "<nil>", "macbook", SourceAreaDocuments, "box-documents", "Documents/private.pdf", filesystemmeta.FindingSeverityError, "permission_denied", "Source file could not be read.")
	}, fidelityFindingDriverRow(finding))

	got, err := svc.RegisterFidelityFinding(context.Background(), RegisterFidelityFindingInput{
		StorageFidelityFindingID:       findingID,
		StorageFilesystemObservationID: observationID,
		NodeKey:                        "macbook",
		SourceArea:                     SourceAreaDocuments,
		SourceRef:                      "box-documents",
		LogicalPath:                    "Documents/private.pdf",
		Severity:                       filesystemmeta.FindingSeverityError,
		FindingKind:                    "permission_denied",
		Summary:                        "Source file could not be read.",
		DetailJSON:                     json.RawMessage(`{"errno":"EACCES"}`),
	})
	if err != nil {
		t.Fatalf("RegisterFidelityFinding returned error: %v", err)
	}
	if got.StorageFidelityFindingID != findingID || got.Status != filesystemmeta.FindingStatusOpen {
		t.Fatalf("unexpected finding %#v", got)
	}

	resolvedAt := now.Add(time.Minute)
	finding.Status = filesystemmeta.FindingStatusResolved
	finding.ResolvedAt = &resolvedAt
	store.expect("UPDATE storage.storage_fidelity_findings", func(args []driver.NamedValue) {
		requireStorageDriverArgs(t, args, findingID, filesystemmeta.FindingStatusResolved)
	}, fidelityFindingDriverRow(finding))

	if err := svc.ResolveFidelityFinding(context.Background(), findingID); err != nil {
		t.Fatalf("ResolveFidelityFinding returned error: %v", err)
	}
	store.requireDone()
}

type storageCatalogFakeStore struct {
	t       *testing.T
	mu      sync.Mutex
	queries []storageCatalogFakeQuery
}

type storageCatalogFakeQuery struct {
	snippet string
	check   func([]driver.NamedValue)
	rows    [][]driver.Value
}

func newStorageCatalogFakeDB(t *testing.T) (*sql.DB, *storageCatalogFakeStore) {
	t.Helper()
	store := &storageCatalogFakeStore{t: t}
	db := sql.OpenDB(storageCatalogFakeConnector{store: store})
	return db, store
}

func (s *storageCatalogFakeStore) expect(snippet string, check func([]driver.NamedValue), rows ...[]driver.Value) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, storageCatalogFakeQuery{snippet: compactStorageSQL(snippet), check: check, rows: rows})
}

func (s *storageCatalogFakeStore) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) == 0 {
		s.t.Fatalf("unexpected query: %s", query)
	}
	expected := s.queries[0]
	s.queries = s.queries[1:]
	if !strings.Contains(compactStorageSQL(query), expected.snippet) {
		s.t.Fatalf("query = %q, want to contain %q", compactStorageSQL(query), expected.snippet)
	}
	if expected.check != nil {
		expected.check(args)
	}
	return &storageCatalogFakeRows{columns: storageCatalogFakeColumns(expected.rows), rows: expected.rows}, nil
}

func (s *storageCatalogFakeStore) requireDone() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queries) != 0 {
		s.t.Fatalf("unconsumed queries: %d", len(s.queries))
	}
}

type storageCatalogFakeConnector struct {
	store *storageCatalogFakeStore
}

func (c storageCatalogFakeConnector) Connect(context.Context) (driver.Conn, error) {
	return storageCatalogFakeConn{store: c.store}, nil
}

func (c storageCatalogFakeConnector) Driver() driver.Driver {
	return storageCatalogFakeDriver{}
}

type storageCatalogFakeDriver struct{}

func (storageCatalogFakeDriver) Open(string) (driver.Conn, error) {
	return nil, fmt.Errorf("use sql.OpenDB with storageCatalogFakeConnector")
}

type storageCatalogFakeConn struct {
	store *storageCatalogFakeStore
}

func (c storageCatalogFakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepared statements are not supported by storageCatalogFakeConn")
}

func (c storageCatalogFakeConn) Close() error {
	return nil
}

func (c storageCatalogFakeConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions are not supported by storageCatalogFakeConn")
}

func (c storageCatalogFakeConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.store.query(query, args)
}

type storageCatalogFakeRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *storageCatalogFakeRows) Columns() []string {
	return r.columns
}

func (r *storageCatalogFakeRows) Close() error {
	return nil
}

func (r *storageCatalogFakeRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func requireStorageDriverArgs(t *testing.T, args []driver.NamedValue, want ...any) {
	t.Helper()
	if len(args) != len(want) {
		t.Fatalf("arg count = %d, want %d", len(args), len(want))
	}
	for i := range want {
		if fmt.Sprint(args[i].Value) != fmt.Sprint(want[i]) {
			t.Fatalf("arg %d = %v, want %v", i+1, args[i].Value, want[i])
		}
	}
}

func driverJSONValue(value any) string {
	switch typed := value.(type) {
	case json.RawMessage:
		return string(typed)
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return fmt.Sprint(value)
	}
}

func compactStorageSQL(query string) string {
	return strings.Join(strings.Fields(query), " ")
}

func storageCatalogFakeColumns(rows [][]driver.Value) []string {
	count := 0
	if len(rows) > 0 {
		count = len(rows[0])
	}
	columns := make([]string, count)
	for i := range columns {
		columns[i] = fmt.Sprintf("col_%d", i)
	}
	return columns
}

func testEntry(now time.Time) Entry {
	size := int64(42)
	return Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      StorageClassPrivateBackup,
		SourceArea:        SourceAreaDocuments,
		OriginNodeKey:     "macbook",
		LogicalPath:       "macbook/Backups/Documents/report.md",
		CurrentViewPath:   "macbook/Backups/Documents/report.md",
		SizeBytes:         &size,
		MimeType:          "text/markdown",
		FileClass:         FileClassMarkdown,
		ProcessingState:   ProcessingStateBackupOnly,
		AvailabilityState: AvailabilityStateAvailable,
		RetentionState:    RetentionStateNone,
		Metadata:          json.RawMessage(`{"source":"unit"}`),
		CreatedAt:         now,
		UpdatedAt:         now,
	}
}

func testPhysicalRef(now time.Time, storageEntryID string) PhysicalRef {
	return PhysicalRef{
		StoragePhysicalRefID: ids.NewStoragePhysicalRefID(),
		StorageEntryID:       storageEntryID,
		RefKind:              PhysicalRefKindLocalPath,
		URI:                  "/var/lib/loom/storage/report.md",
		NodeKey:              "loom-main",
		Status:               PhysicalRefStatusAvailable,
		Metadata:             json.RawMessage(`{"source":"unit"}`),
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func entryDriverRow(entry Entry) []driver.Value {
	return []driver.Value{
		entry.StorageEntryID,
		entry.StorageClass,
		entry.SourceArea,
		stringPtrDriverValue(entry.OriginNodeID),
		entry.OriginNodeKey,
		stringPtrDriverValue(entry.ProjectID),
		stringPtrDriverValue(entry.WatchedRootID),
		entry.WatchedRootKey,
		entry.DropzoneTransferID,
		stringPtrDriverValue(entry.PrivateBackupOperationID),
		stringPtrDriverValue(entry.PrivateBackupItemID),
		stringPtrDriverValue(entry.ObjectID),
		stringPtrDriverValue(entry.ObjectVersionID),
		stringPtrDriverValue(entry.ArchiveManifestID),
		entry.LogicalPath,
		entry.OriginalSourcePath,
		entry.CurrentViewPath,
		entry.ChecksumAlgorithm,
		entry.ChecksumHex,
		int64PtrDriverValue(entry.SizeBytes),
		entry.MimeType,
		entry.FileClass,
		entry.ProcessingState,
		entry.AvailabilityState,
		entry.RetentionState,
		[]byte(entry.Metadata),
		entry.CreatedAt,
		entry.UpdatedAt,
		timePtrDriverValue(entry.DeletedAt),
	}
}

func physicalRefDriverRow(ref PhysicalRef) []driver.Value {
	return []driver.Value{
		ref.StoragePhysicalRefID,
		ref.StorageEntryID,
		stringPtrDriverValue(ref.StorageEntryVersionID),
		ref.RefKind,
		ref.URI,
		stringPtrDriverValue(ref.NodeID),
		ref.NodeKey,
		ref.ContentAddress,
		ref.Status,
		[]byte(ref.Metadata),
		ref.CreatedAt,
		ref.UpdatedAt,
	}
}

func entryVersionDriverRow(version EntryVersion) []driver.Value {
	return []driver.Value{
		version.StorageEntryVersionID,
		version.StorageEntryID,
		int64(version.VersionNumber),
		version.ChecksumAlgorithm,
		version.ChecksumHex,
		int64PtrDriverValue(version.SizeBytes),
		stringPtrDriverValue(version.PhysicalRefID),
		[]byte(version.Metadata),
		version.CreatedAt,
	}
}

func retentionEntryDriverRow(retention RetentionEntry) []driver.Value {
	return []driver.Value{
		retention.StorageRetentionEntryID,
		retention.StorageEntryID,
		retention.PolicyKey,
		retention.RetentionState,
		timePtrDriverValue(retention.RetainedUntil),
		[]byte(retention.Metadata),
		retention.CreatedAt,
		retention.UpdatedAt,
	}
}

func filesystemObservationDriverRow(observation FilesystemObservation) []driver.Value {
	return []driver.Value{
		observation.StorageFilesystemObservationID,
		stringPtrDriverValue(observation.StorageEntryID),
		observation.SourceArea,
		stringPtrDriverValue(observation.SourceNodeID),
		observation.SourceNodeKey,
		observation.SourceRef,
		observation.LogicalPath,
		observation.ObjectKind,
		intPtrDriverValue(observation.SourceMode),
		observation.Executable,
		intPtrDriverValue(observation.UID),
		intPtrDriverValue(observation.GID),
		observation.UserName,
		observation.GroupName,
		observation.SymlinkTarget,
		int64PtrDriverValue(observation.DeviceID),
		int64PtrDriverValue(observation.Inode),
		int64PtrDriverValue(observation.LinkCount),
		observation.IsHardLink,
		observation.IsSparse,
		observation.LogicalSizeBytes,
		int64PtrDriverValue(observation.AllocatedBytes),
		observation.HasXattrs,
		[]byte(mustJSONStringArray(observation.XattrNames)),
		observation.HasACL,
		observation.HasResourceFork,
		observation.HasFinderTags,
		observation.HasQuarantine,
		observation.IsPackage,
		observation.PackageKind,
		observation.UnicodeForm,
		observation.CasefoldKey,
		observation.Hidden,
		observation.GeneratedMetadata,
		observation.PermissionDenied,
		[]byte(mustJSONStringArray(observation.Risks)),
		observation.ObservedAt,
		[]byte(observation.RawJSON),
		observation.CreatedAt,
		observation.UpdatedAt,
	}
}

func fidelityFindingDriverRow(finding FidelityFinding) []driver.Value {
	return []driver.Value{
		finding.StorageFidelityFindingID,
		stringPtrDriverValue(finding.StorageFilesystemObservationID),
		stringPtrDriverValue(finding.StorageEntryID),
		stringPtrDriverValue(finding.NodeID),
		finding.NodeKey,
		finding.SourceArea,
		finding.SourceRef,
		finding.LogicalPath,
		finding.Severity,
		finding.FindingKind,
		finding.Summary,
		[]byte(finding.DetailJSON),
		finding.Status,
		finding.CreatedAt,
		finding.UpdatedAt,
		timePtrDriverValue(finding.ResolvedAt),
	}
}

func stringPtrDriverValue(value *string) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func intPtrDriverValue(value *int) driver.Value {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func int64PtrDriverValue(value *int64) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func timePtrDriverValue(value *time.Time) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}
