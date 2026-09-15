package filetransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storageview"
)

func TestAssembleWritesAcceptedFileGroupReadable(t *testing.T) {
	root := t.TempDir()
	payload := []byte("large backup payload")
	stagingPath := filepath.Join(root, "staging", "000000.part")
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o750); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagingPath, payload, 0o600); err != nil {
		t.Fatalf("write staging chunk: %v", err)
	}

	manifest := Manifest{
		TransferID:        "file_transfer_test",
		FileSizeBytes:     int64(len(payload)),
		ChecksumAlgorithm: ChecksumSHA256,
	}
	chunk := Chunk{
		Index:             0,
		SizeBytes:         int64(len(payload)),
		ChecksumAlgorithm: ChecksumSHA256,
		ChecksumHex:       SHA256Hex(payload),
		StagingPath:       stagingPath,
		Status:            ChunkStatusUploaded,
	}
	acceptedPath := filepath.Join(root, "accepted", "backup.bin")

	actualChecksum, err := (Service{}).assemble(manifest, []Chunk{chunk}, map[int64]Chunk{0: chunk}, acceptedPath)
	if err != nil {
		t.Fatalf("assemble returned error: %v", err)
	}
	if actualChecksum != SHA256Hex(payload) {
		t.Fatalf("checksum = %s, want %s", actualChecksum, SHA256Hex(payload))
	}
	info, err := os.Stat(acceptedPath)
	if err != nil {
		t.Fatalf("stat accepted file: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o640); got != want {
		t.Fatalf("accepted file mode = %o, want %o", got, want)
	}
}

func TestAssembleRejectsExistingAcceptedFileWithDifferentChecksum(t *testing.T) {
	root := t.TempDir()
	payload := []byte("new payload")
	stagingPath := filepath.Join(root, "staging", "000000.part")
	if err := os.MkdirAll(filepath.Dir(stagingPath), 0o750); err != nil {
		t.Fatalf("mkdir staging: %v", err)
	}
	if err := os.WriteFile(stagingPath, payload, 0o600); err != nil {
		t.Fatalf("write staging chunk: %v", err)
	}
	acceptedPath := filepath.Join(root, "accepted", "backup.bin")
	if err := os.MkdirAll(filepath.Dir(acceptedPath), 0o750); err != nil {
		t.Fatalf("mkdir accepted: %v", err)
	}
	if err := os.WriteFile(acceptedPath, []byte("old payload"), 0o640); err != nil {
		t.Fatalf("write existing accepted file: %v", err)
	}

	manifest := Manifest{
		TransferID:        "file_transfer_test",
		FileSizeBytes:     int64(len(payload)),
		ChecksumAlgorithm: ChecksumSHA256,
	}
	chunk := Chunk{
		Index:             0,
		SizeBytes:         int64(len(payload)),
		ChecksumAlgorithm: ChecksumSHA256,
		ChecksumHex:       SHA256Hex(payload),
		StagingPath:       stagingPath,
		Status:            ChunkStatusUploaded,
	}

	_, err := (Service{}).assemble(manifest, []Chunk{chunk}, map[int64]Chunk{0: chunk}, acceptedPath)
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("assemble error = %v, want checksum mismatch", err)
	}
	if got, err := os.ReadFile(acceptedPath); err != nil || string(got) != "old payload" {
		t.Fatalf("accepted file changed: got=%q err=%v", string(got), err)
	}
}

func TestAssembleRejectsDeclaredChecksumBeforePublishingMultiChunkFile(t *testing.T) {
	root := t.TempDir()
	payloads := [][]byte{[]byte("first chunk"), []byte("second chunk")}
	planned := make([]Chunk, 0, len(payloads))
	chunks := map[int64]Chunk{}
	for index, payload := range payloads {
		staging := filepath.Join(root, "staging", string(rune('a'+index))+".part")
		if err := os.MkdirAll(filepath.Dir(staging), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(staging, payload, 0o600); err != nil {
			t.Fatal(err)
		}
		chunk := Chunk{Index: int64(index), SizeBytes: int64(len(payload)), StagingPath: staging, Status: ChunkStatusUploaded}
		planned = append(planned, chunk)
		chunks[chunk.Index] = chunk
	}
	accepted := filepath.Join(root, "accepted", "payload.bin")
	manifest := Manifest{
		TransferID: "file_transfer_test", FileSizeBytes: int64(len(payloads[0]) + len(payloads[1])),
		ChecksumAlgorithm: ChecksumSHA256, ChecksumHex: strings.Repeat("f", 64),
	}
	if _, err := (Service{}).assemble(manifest, planned, chunks, accepted); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("assemble error = %v, want ErrChecksumMismatch", err)
	}
	if _, err := os.Stat(accepted); !os.IsNotExist(err) {
		t.Fatalf("mismatching payload was published: %v", err)
	}
	if _, err := os.Stat(accepted + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("mismatching payload temp was retained: %v", err)
	}
}

func TestAssembleRejectsDeclaredChecksumForEmptyFile(t *testing.T) {
	accepted := filepath.Join(t.TempDir(), "accepted", "empty.bin")
	manifest := Manifest{
		TransferID: "file_transfer_test", FileSizeBytes: 0,
		ChecksumAlgorithm: ChecksumSHA256, ChecksumHex: strings.Repeat("0", 64),
	}
	if _, err := (Service{}).assemble(manifest, nil, map[int64]Chunk{}, accepted); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("empty assemble error = %v, want ErrChecksumMismatch", err)
	}
	if _, err := os.Stat(accepted); !os.IsNotExist(err) {
		t.Fatalf("mismatching empty payload was published: %v", err)
	}
}

func TestAssembleMismatchPreservesExistingDestinationMatchingDeclaredEvidence(t *testing.T) {
	root := t.TempDir()
	existing := []byte("existing valid payload")
	accepted := filepath.Join(root, "accepted", "payload.bin")
	if err := os.MkdirAll(filepath.Dir(accepted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(accepted, existing, 0o640); err != nil {
		t.Fatal(err)
	}
	uploaded := []byte("different uploaded payload")
	staging := filepath.Join(root, "chunk")
	if err := os.WriteFile(staging, uploaded, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		TransferID: "file_transfer_test", FileSizeBytes: int64(len(uploaded)),
		ChecksumAlgorithm: ChecksumSHA256, ChecksumHex: SHA256Hex(existing),
	}
	chunk := Chunk{Index: 0, SizeBytes: int64(len(uploaded)), StagingPath: staging, Status: ChunkStatusUploaded}
	if _, err := (Service{}).assemble(manifest, []Chunk{chunk}, map[int64]Chunk{0: chunk}, accepted); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("assemble error = %v, want ErrChecksumMismatch", err)
	}
	got, err := os.ReadFile(accepted)
	if err != nil || string(got) != string(existing) {
		t.Fatalf("existing destination changed: got=%q err=%v", got, err)
	}
}

func TestCompleteChecksumMismatchCreatesNoCatalogRefAndCanRetry(t *testing.T) {
	root := t.TempDir()
	good := []byte("good")
	bad := []byte("baad")
	manifest, err := NormalizeManifest(Manifest{
		SourceNodeKey: "workspace", SourceRootKey: "manual", SourceRelativePath: "payload.bin",
		DestinationLogicalPath: "payload.bin", TransferKind: KindManualUpload, CustodyMode: CustodyModeMainOwned,
		FileSizeBytes: int64(len(good)), ChunkSizeBytes: int64(len(good)),
		ChecksumAlgorithm: ChecksumSHA256, ChecksumHex: SHA256Hex(good),
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	manifest.Status = StatusUploading
	chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "chunk")
	if err := os.WriteFile(staging, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	chunks[0].Status = ChunkStatusUploaded
	chunks[0].ReceivedBytes = int64(len(bad))
	chunks[0].ChecksumAlgorithm = ChecksumSHA256
	chunks[0].ChecksumHex = SHA256Hex(bad)
	chunks[0].StagingPath = staging
	store := &memoryTransferStore{manifest: manifest, chunks: chunks}
	catalog := &countingTransferCatalog{}
	service := NewService(store, catalog, ServiceConfig{
		StagingRoot: filepath.Join(root, "state"), AcceptedRoot: filepath.Join(root, "accepted"), MainNodeKey: "main",
	})
	if _, err := service.Complete(context.Background(), manifest.TransferID); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("Complete error = %v, want ErrChecksumMismatch", err)
	}
	if catalog.entries != 0 || catalog.refs != 0 {
		t.Fatalf("checksum mismatch registered catalog evidence: entries=%d refs=%d", catalog.entries, catalog.refs)
	}
	if store.manifest.Status != StatusFailed || store.manifest.LastErrorCode != "file_checksum_mismatch" {
		t.Fatalf("retry state = %#v", store.manifest)
	}
	if strings.TrimSpace(store.manifest.AcceptedPath) != "" {
		t.Fatalf("checksum mismatch recorded accepted path: %q", store.manifest.AcceptedPath)
	}

	if err := os.WriteFile(staging, good, 0o600); err != nil {
		t.Fatal(err)
	}
	store.chunks[0].ChecksumHex = SHA256Hex(good)
	result, err := service.Complete(context.Background(), manifest.TransferID)
	if err != nil {
		t.Fatalf("Complete retry: %v", err)
	}
	if result.Status.Manifest.Status != StatusAccepted || catalog.entries != 1 || catalog.refs != 1 {
		t.Fatalf("retry result=%#v catalog entries=%d refs=%d", result, catalog.entries, catalog.refs)
	}
}

func TestDropzoneTransferCompatibilityIsReadOnly(t *testing.T) {
	manifest := Manifest{
		TransferID:             NewTransferID(),
		IdempotencyKey:         "historical-dropzone",
		SourceNodeKey:          "macbook",
		SourceRootKey:          "loom_box__dropzone",
		SourceRelativePath:     "Research/report.pdf",
		DestinationLogicalPath: "Research/report.pdf",
		TransferKind:           KindDropzoneCustody,
		CustodyMode:            CustodyModeCustodyTransfer,
		FileSizeBytes:          128,
		ChunkSizeBytes:         128,
		ChunkCount:             1,
		Status:                 StatusAccepted,
		DropzoneTransferID:     "drop_historical_transfer",
		CreatedAt:              time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC),
		UpdatedAt:              time.Date(2026, 6, 5, 12, 2, 0, 0, time.UTC),
	}
	store := &memoryTransferStore{manifest: manifest}
	service := NewService(store, &countingTransferCatalog{}, ServiceConfig{
		StagingRoot:  filepath.Join(t.TempDir(), "staging"),
		AcceptedRoot: filepath.Join(t.TempDir(), "accepted"),
		MainNodeKey:  "main",
	})

	status, err := service.Get(context.Background(), manifest.TransferID)
	if err != nil {
		t.Fatalf("inspect historical Dropzone transfer: %v", err)
	}
	if status.Manifest.TransferKind != KindDropzoneCustody || status.Manifest.DropzoneTransferID != manifest.DropzoneTransferID {
		t.Fatalf("historical Dropzone transfer changed during inspection: %#v", status.Manifest)
	}
	if _, err := service.Initiate(context.Background(), manifest); !errors.Is(err, ErrTransferRetired) {
		t.Fatalf("initiate error = %v, want ErrTransferRetired", err)
	}
	if _, err := service.UploadChunk(context.Background(), UploadChunkInput{TransferID: manifest.TransferID}); !errors.Is(err, ErrTransferRetired) {
		t.Fatalf("upload error = %v, want ErrTransferRetired", err)
	}
	if _, err := service.Complete(context.Background(), manifest.TransferID); !errors.Is(err, ErrTransferRetired) {
		t.Fatalf("complete error = %v, want ErrTransferRetired", err)
	}
	if _, err := service.Abort(context.Background(), AbortInput{TransferID: manifest.TransferID, Reason: "retired"}); !errors.Is(err, ErrTransferRetired) {
		t.Fatalf("abort error = %v, want ErrTransferRetired", err)
	}
	if store.manifest.Status != StatusAccepted || !store.manifest.UpdatedAt.Equal(manifest.UpdatedAt) {
		t.Fatalf("historical evidence was mutated: %#v", store.manifest)
	}
}

type memoryTransferStore struct {
	manifest Manifest
	chunks   []Chunk
}

func (s *memoryTransferStore) UpsertManifest(_ context.Context, manifest Manifest) (Manifest, error) {
	s.manifest = manifest
	return manifest, nil
}
func (s *memoryTransferStore) GetManifest(_ context.Context, transferID string) (Manifest, error) {
	if s.manifest.TransferID != transferID {
		return Manifest{}, ErrNotFound
	}
	return s.manifest, nil
}
func (s *memoryTransferStore) GetManifestByIdempotencyKey(_ context.Context, key string) (Manifest, error) {
	if s.manifest.IdempotencyKey != key {
		return Manifest{}, ErrNotFound
	}
	return s.manifest, nil
}
func (s *memoryTransferStore) ListManifests(context.Context, ListFilter) ([]Manifest, error) {
	return []Manifest{s.manifest}, nil
}
func (s *memoryTransferStore) UpsertChunk(_ context.Context, chunk Chunk) (Chunk, error) {
	for index := range s.chunks {
		if s.chunks[index].Index == chunk.Index {
			s.chunks[index] = chunk
			return chunk, nil
		}
	}
	s.chunks = append(s.chunks, chunk)
	return chunk, nil
}
func (s *memoryTransferStore) ListChunks(context.Context, string) ([]Chunk, error) {
	return append([]Chunk(nil), s.chunks...), nil
}

type countingTransferCatalog struct {
	entries     int
	refs        int
	entryInputs []storagecatalog.RegisterEntryInput
	refInputs   []storagecatalog.RegisterPhysicalRefInput
}

func (c *countingTransferCatalog) RegisterEntry(_ context.Context, input storagecatalog.RegisterEntryInput) (storagecatalog.Entry, error) {
	c.entries++
	c.entryInputs = append(c.entryInputs, input)
	return storagecatalog.Entry{
		StorageEntryID:    ids.NewStorageEntryID(),
		StorageClass:      input.StorageClass,
		SourceArea:        input.SourceArea,
		OriginNodeKey:     input.OriginNodeKey,
		LogicalPath:       input.LogicalPath,
		CurrentViewPath:   input.CurrentViewPath,
		FileClass:         input.FileClass,
		ProcessingState:   input.ProcessingState,
		AvailabilityState: input.AvailabilityState,
	}, nil
}
func (c *countingTransferCatalog) RegisterPhysicalRef(_ context.Context, input storagecatalog.RegisterPhysicalRefInput) (storagecatalog.PhysicalRef, error) {
	c.refs++
	c.refInputs = append(c.refInputs, input)
	return storagecatalog.PhysicalRef{StoragePhysicalRefID: ids.NewStoragePhysicalRefID(), StorageEntryID: input.StorageEntryID}, nil
}

func TestProcessingStateForManifestModelsRetainedCustodyPayloads(t *testing.T) {
	t.Parallel()
	if got := processingStateForManifest(Manifest{TransferKind: KindWatchedRootBackup}); got != storagecatalog.ProcessingStateBackupOnly {
		t.Fatalf("watched-root backup processing state = %q", got)
	}
	if got := processingStateForManifest(Manifest{TransferKind: KindMainDocumentsImport}); got != storagecatalog.ProcessingStateMetadataOnly {
		t.Fatalf("main Documents import processing state = %q", got)
	}
}

func TestPhysicalRefKindKeepsChunkedBackupAsDirectFile(t *testing.T) {
	manifest := Manifest{TransferKind: KindWatchedRootBackup}
	if got := physicalRefKindForManifest(manifest); got != storagecatalog.PhysicalRefKindLocalPath {
		t.Fatalf("chunked backup ref kind = %q, want directly readable local_path", got)
	}
}

func TestAcceptedPathUsesConfiguredUserBackupCustody(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, nil, ServiceConfig{
		AcceptedRoot:    filepath.Join(root, "runtime", "accepted"),
		UserBackupsRoot: filepath.Join(root, "storage", "backups"),
	})
	manifest := Manifest{
		TransferID:             "file_transfer_test",
		TransferKind:           KindWatchedRootBackup,
		SourceNodeKey:          "workspace-test",
		SourceRootKey:          "loom_box__documents",
		DestinationLogicalPath: "Reports/report.pdf",
		Metadata:               []byte(`{"local_batch_id":"local_backup_batch_test"}`),
	}

	relative, physical, err := service.acceptedPath(manifest)
	if err != nil {
		t.Fatalf("acceptedPath returned error: %v", err)
	}
	wantRelative := filepath.ToSlash(filepath.Join("workspace-test", "loom_box__documents", "local_backup_batch_test", "payload", "Reports", "report.pdf"))
	if relative != wantRelative {
		t.Fatalf("relative path = %q, want %q", relative, wantRelative)
	}
	wantPhysical := filepath.Join(root, "storage", "backups", filepath.FromSlash(wantRelative))
	if physical != wantPhysical {
		t.Fatalf("physical path = %q, want %q", physical, wantPhysical)
	}
	if strings.Contains(physical, filepath.Join("runtime", "accepted")) {
		t.Fatalf("watched-root backup used runtime accepted custody: %q", physical)
	}
}

func TestWatchedBackupCatalogPathUsesCanonicalStorageCustody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		rootKey    string
		sourceArea string
	}{
		{name: "notes", rootKey: "loom_box__notes", sourceArea: storagecatalog.SourceAreaNotes},
		{name: "documents", rootKey: "loom_box__documents", sourceArea: storagecatalog.SourceAreaDocuments},
		{name: "projects", rootKey: "loom_box__projects", sourceArea: storagecatalog.SourceAreaProjects},
		{name: "external", rootKey: "external_photos", sourceArea: storagecatalog.SourceAreaExternalWatchedRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			storageRoot := filepath.Join(root, "storage")
			backupsRoot := filepath.Join(storageRoot, "backups")
			catalog := &countingTransferCatalog{}
			service := NewService(nil, catalog, ServiceConfig{
				StorageRoot:              storageRoot,
				CanonicalUserBackupsRoot: backupsRoot,
				UserBackupsRoot:          backupsRoot,
				MainNodeKey:              "main",
			})
			manifest := Manifest{
				TransferID:             "file_transfer_test",
				TransferKind:           KindWatchedRootBackup,
				SourceNodeKey:          "workspace-test",
				SourceRootKey:          tt.rootKey,
				DestinationLogicalPath: "Reports/report.pdf",
				FileSizeBytes:          7,
				ChecksumAlgorithm:      ChecksumSHA256,
				ChecksumHex:            SHA256Hex([]byte("payload")),
				Metadata:               []byte(`{"local_batch_id":"local_backup_batch_test"}`),
			}
			relative, accepted, err := service.acceptedPath(manifest)
			if err != nil {
				t.Fatalf("acceptedPath: %v", err)
			}
			manifest.AcceptedPath = relative
			if _, _, err := service.registerCatalog(context.Background(), manifest, accepted); err != nil {
				t.Fatalf("registerCatalog: %v", err)
			}
			if catalog.entries != 1 || catalog.refs != 1 {
				t.Fatalf("catalog mutations = %d/%d, want 1/1", catalog.entries, catalog.refs)
			}
			wantViewPath := filepath.ToSlash(filepath.Join("backups", "workspace-test", tt.rootKey, "local_backup_batch_test", "payload", "Reports", "report.pdf"))
			entryInput := catalog.entryInputs[0]
			if entryInput.SourceArea != tt.sourceArea {
				t.Fatalf("source area = %q, want %q", entryInput.SourceArea, tt.sourceArea)
			}
			if entryInput.CurrentViewPath != wantViewPath {
				t.Fatalf("current view path = %q, want %q", entryInput.CurrentViewPath, wantViewPath)
			}
			if strings.Contains(entryInput.CurrentViewPath, "_System/Transfers") {
				t.Fatalf("current view path retained synthetic transfer namespace: %q", entryInput.CurrentViewPath)
			}
			if got := catalog.refInputs[0].URI; got != filepath.ToSlash(accepted) {
				t.Fatalf("physical ref URI = %q, want exact accepted path %q", got, filepath.ToSlash(accepted))
			}
			viewEntry, err := storageview.CatalogViewEntry(storagecatalog.Entry{
				StorageEntryID:    "storage_entry_test",
				StorageClass:      entryInput.StorageClass,
				SourceArea:        entryInput.SourceArea,
				OriginNodeKey:     entryInput.OriginNodeKey,
				LogicalPath:       entryInput.LogicalPath,
				CurrentViewPath:   entryInput.CurrentViewPath,
				FileClass:         entryInput.FileClass,
				ProcessingState:   entryInput.ProcessingState,
				AvailabilityState: entryInput.AvailabilityState,
			})
			if err != nil || viewEntry.ViewPath != wantViewPath {
				t.Fatalf("catalog view entry = %#v err=%v, want reachable path %q", viewEntry, err, wantViewPath)
			}
		})
	}
}

func TestWatchedBackupCatalogPathPreservesCustomNestedRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	storageRoot := filepath.Join(root, "canonical-storage")
	backupsRoot := filepath.Join(storageRoot, "protected", "copies", "user-backups")
	service := NewService(nil, nil, ServiceConfig{StorageRoot: storageRoot, CanonicalUserBackupsRoot: backupsRoot, UserBackupsRoot: backupsRoot})
	accepted := filepath.Join(backupsRoot, "macbook", "notes", "batch-42", "payload", "note.md")
	got, err := service.watchedBackupViewPath(accepted)
	if err != nil {
		t.Fatalf("watchedBackupViewPath: %v", err)
	}
	want := "protected/copies/user-backups/macbook/notes/batch-42/payload/note.md"
	if got != want {
		t.Fatalf("view path = %q, want %q", got, want)
	}
}

func TestWatchedBackupCatalogPathSupportsProductionPreCutoverCanonicalIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "var-lib-loom")
	backupsRoot := filepath.Join(dataRoot, "private-backups")
	catalog := &countingTransferCatalog{}
	storageRoot := filepath.Join(root, "srv-loom", "storage")
	service := NewService(nil, catalog, ServiceConfig{
		StorageRoot:              storageRoot,
		CanonicalUserBackupsRoot: filepath.Join(storageRoot, "backups"),
		UserBackupsRoot:          backupsRoot,
		MainNodeKey:              "main",
	})
	manifest := Manifest{
		TransferID:             "file_transfer_pre_cutover",
		TransferKind:           KindWatchedRootBackup,
		SourceNodeKey:          "workspace-test",
		SourceRootKey:          "loom_box__documents",
		DestinationLogicalPath: "Reports/report.pdf",
		FileSizeBytes:          7,
		ChecksumAlgorithm:      ChecksumSHA256,
		ChecksumHex:            SHA256Hex([]byte("payload")),
		Metadata:               []byte(`{"local_batch_id":"legacy_batch_test"}`),
	}
	relative, accepted, err := service.acceptedPath(manifest)
	if err != nil {
		t.Fatalf("acceptedPath: %v", err)
	}
	manifest.AcceptedPath = relative
	if _, _, err := service.registerCatalog(context.Background(), manifest, accepted); err != nil {
		t.Fatalf("registerCatalog: %v", err)
	}
	want := filepath.ToSlash(filepath.Join("backups", "workspace-test", "loom_box__documents", "legacy_batch_test", "payload", "Reports", "report.pdf"))
	if got := catalog.entryInputs[0].CurrentViewPath; got != want {
		t.Fatalf("pre-cutover current view path = %q, want %q", got, want)
	}
	if strings.Contains(catalog.entryInputs[0].CurrentViewPath, "storage-views/main-export") {
		t.Fatalf("pre-cutover catalog path used the legacy SMB export: %q", catalog.entryInputs[0].CurrentViewPath)
	}
}

func TestWatchedBackupCatalogPathRejectsUnprovableRootsBeforeMutation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	manifest := Manifest{
		TransferID:             "file_transfer_test",
		TransferKind:           KindWatchedRootBackup,
		SourceNodeKey:          "macbook",
		SourceRootKey:          "notes",
		DestinationLogicalPath: "note.md",
		FileSizeBytes:          7,
	}
	tests := []struct {
		name                 string
		storageRoot          string
		canonicalBackupsRoot string
		backupsRoot          string
		accepted             string
	}{
		{
			name:        "accepted escapes backups",
			storageRoot: filepath.Join(root, "storage-a"),
			backupsRoot: filepath.Join(root, "storage-a", "backups"),
			accepted:    filepath.Join(root, "storage-a", "archive", "note.md"),
		},
		{
			name:                 "backups outside storage",
			storageRoot:          filepath.Join(root, "storage-b"),
			canonicalBackupsRoot: filepath.Join(root, "other-canonical-backups"),
			backupsRoot:          filepath.Join(root, "separate-backups"),
			accepted:             filepath.Join(root, "separate-backups", "macbook", "notes", "batch", "payload", "note.md"),
		},
		{
			name:        "relative roots",
			storageRoot: "storage",
			backupsRoot: "storage/backups",
			accepted:    "storage/backups/macbook/notes/batch/payload/note.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := &countingTransferCatalog{}
			canonicalBackupsRoot := tt.canonicalBackupsRoot
			if canonicalBackupsRoot == "" {
				canonicalBackupsRoot = filepath.Join(tt.storageRoot, "backups")
			}
			service := NewService(nil, catalog, ServiceConfig{StorageRoot: tt.storageRoot, CanonicalUserBackupsRoot: canonicalBackupsRoot, UserBackupsRoot: tt.backupsRoot})
			if _, _, err := service.registerCatalog(context.Background(), manifest, tt.accepted); err == nil {
				t.Fatal("registerCatalog accepted an unprovable watched-root custody path")
			}
			if catalog.entries != 0 || catalog.refs != 0 {
				t.Fatalf("invalid path mutated catalog: entries=%d refs=%d", catalog.entries, catalog.refs)
			}
		})
	}
}

func TestAcceptedPathSeparatesUserPayloadFromInternalArtifactNamespace(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, nil, ServiceConfig{
		AcceptedRoot:    filepath.Join(root, "runtime", "accepted"),
		UserBackupsRoot: filepath.Join(root, "storage", "backups"),
	})
	manifest := Manifest{
		TransferID:             "file_transfer_test",
		TransferKind:           KindWatchedRootBackup,
		SourceNodeKey:          "workspace-test",
		SourceRootKey:          "notes",
		DestinationLogicalPath: "artifacts/.private_backup_valid.tmp/file.txt",
		Metadata:               []byte(`{"local_batch_id":"local_backup_batch_test"}`),
	}
	relative, _, err := service.acceptedPath(manifest)
	if err != nil {
		t.Fatalf("acceptedPath: %v", err)
	}
	want := "workspace-test/notes/local_backup_batch_test/payload/artifacts/.private_backup_valid.tmp/file.txt"
	if relative != want {
		t.Fatalf("relative path = %q, want %q", relative, want)
	}
}

func TestAcceptedPathKeepsNonBackupTransfersInRuntimeCustody(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, nil, ServiceConfig{
		AcceptedRoot:    filepath.Join(root, "runtime", "accepted"),
		UserBackupsRoot: filepath.Join(root, "storage", "backups"),
	})
	manifest := Manifest{
		TransferID:             "file_transfer_test",
		TransferKind:           KindManualUpload,
		SourceNodeKey:          "workspace-test",
		DestinationLogicalPath: "manual.txt",
	}
	_, physical, err := service.acceptedPath(manifest)
	if err != nil {
		t.Fatalf("acceptedPath returned error: %v", err)
	}
	if !strings.HasPrefix(physical, filepath.Join(root, "runtime", "accepted")+string(filepath.Separator)) {
		t.Fatalf("non-backup transfer escaped runtime custody: %q", physical)
	}
}

func TestAcceptedPathPreservesLegacyWatchedBackupRuntimeLayout(t *testing.T) {
	root := t.TempDir()
	service := NewService(nil, nil, ServiceConfig{AcceptedRoot: filepath.Join(root, "accepted")})
	manifest := Manifest{
		TransferID:             "file_transfer_test",
		TransferKind:           KindWatchedRootBackup,
		SourceNodeKey:          "workspace-test",
		SourceRootKey:          "documents",
		DestinationLogicalPath: "report.pdf",
	}
	relative, _, err := service.acceptedPath(manifest)
	if err != nil {
		t.Fatalf("acceptedPath returned error: %v", err)
	}
	if !strings.HasPrefix(relative, "watched-root-backups/") {
		t.Fatalf("legacy watched-root path = %q", relative)
	}
}

func TestAssembleConfinesCanonicalBackupCustody(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	staging := filepath.Join(root, "chunk")
	payload := []byte("canonical backup payload")
	if err := os.WriteFile(staging, payload, 0o600); err != nil {
		t.Fatalf("write staging: %v", err)
	}
	manifest := Manifest{
		TransferID:        "file_transfer_test",
		TransferKind:      KindWatchedRootBackup,
		FileSizeBytes:     int64(len(payload)),
		ChecksumAlgorithm: ChecksumSHA256,
	}
	chunk := Chunk{Index: 0, SizeBytes: int64(len(payload)), StagingPath: staging}
	accepted := filepath.Join(backupRoot, "workspace", "notes", "batch", "note.md")
	checksum, err := (Service{Config: ServiceConfig{UserBackupsRoot: backupRoot}}).assemble(manifest, []Chunk{chunk}, map[int64]Chunk{0: chunk}, accepted)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if checksum != SHA256Hex(payload) {
		t.Fatalf("checksum = %q", checksum)
	}
	if got, err := os.ReadFile(accepted); err != nil || string(got) != string(payload) {
		t.Fatalf("accepted payload = %q err=%v", got, err)
	}
}

func TestAssembleRejectsSymlinkedCanonicalBackupParent(t *testing.T) {
	root := t.TempDir()
	backupRoot := filepath.Join(root, "backups")
	external := filepath.Join(root, "external")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(external, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(external, "sentinel")
	if err := os.WriteFile(sentinel, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(backupRoot, "workspace")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	staging := filepath.Join(root, "chunk")
	if err := os.WriteFile(staging, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{TransferID: "file_transfer_test", TransferKind: KindWatchedRootBackup, FileSizeBytes: 7}
	chunk := Chunk{Index: 0, SizeBytes: 7, StagingPath: staging}
	accepted := filepath.Join(backupRoot, "workspace", "notes", "batch", "note.md")
	if _, err := (Service{Config: ServiceConfig{UserBackupsRoot: backupRoot}}).assemble(manifest, []Chunk{chunk}, map[int64]Chunk{0: chunk}, accepted); err == nil {
		t.Fatal("assemble followed hostile backup parent symlink")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "safe" {
		t.Fatalf("external sentinel changed: %q err=%v", got, err)
	}
}
