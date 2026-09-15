package filetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

type TransferStore interface {
	UpsertManifest(ctx context.Context, manifest Manifest) (Manifest, error)
	GetManifest(ctx context.Context, transferID string) (Manifest, error)
	GetManifestByIdempotencyKey(ctx context.Context, idempotencyKey string) (Manifest, error)
	ListManifests(ctx context.Context, filter ListFilter) ([]Manifest, error)
	UpsertChunk(ctx context.Context, chunk Chunk) (Chunk, error)
	ListChunks(ctx context.Context, transferID string) ([]Chunk, error)
}

type Catalog interface {
	RegisterEntry(ctx context.Context, input storagecatalog.RegisterEntryInput) (storagecatalog.Entry, error)
	RegisterPhysicalRef(ctx context.Context, input storagecatalog.RegisterPhysicalRefInput) (storagecatalog.PhysicalRef, error)
}

type ServiceConfig struct {
	StagingRoot              string
	AcceptedRoot             string
	StorageRoot              string
	CanonicalUserBackupsRoot string
	UserBackupsRoot          string
	MainNodeKey              string
}

type Service struct {
	Store   TransferStore
	Catalog Catalog
	Config  ServiceConfig
	Now     func() time.Time
}

func NewService(store TransferStore, catalog Catalog, config ServiceConfig) Service {
	config.StagingRoot = strings.TrimSpace(config.StagingRoot)
	config.AcceptedRoot = strings.TrimSpace(config.AcceptedRoot)
	config.StorageRoot = strings.TrimSpace(config.StorageRoot)
	config.CanonicalUserBackupsRoot = strings.TrimSpace(config.CanonicalUserBackupsRoot)
	config.UserBackupsRoot = strings.TrimSpace(config.UserBackupsRoot)
	config.MainNodeKey = strings.TrimSpace(config.MainNodeKey)
	if config.MainNodeKey == "" {
		config.MainNodeKey = "main"
	}
	return Service{Store: store, Catalog: catalog, Config: config}
}

func NewServiceWithDataDir(store TransferStore, catalog Catalog, dataDir, mainNodeKey string) Service {
	root := filepath.Join(strings.TrimSpace(dataDir), "file-transfers")
	return NewService(store, catalog, ServiceConfig{
		StagingRoot:  filepath.Join(root, "staging"),
		AcceptedRoot: filepath.Join(root, "accepted"),
		MainNodeKey:  mainNodeKey,
	})
}

func NewServiceWithRoots(store TransferStore, catalog Catalog, dataDir, storageRoot, canonicalUserBackupsRoot, userBackupsRoot, mainNodeKey string) Service {
	root := filepath.Join(strings.TrimSpace(dataDir), "file-transfers")
	return NewService(store, catalog, ServiceConfig{
		StagingRoot:              filepath.Join(root, "staging"),
		AcceptedRoot:             filepath.Join(root, "accepted"),
		StorageRoot:              storageRoot,
		CanonicalUserBackupsRoot: canonicalUserBackupsRoot,
		UserBackupsRoot:          userBackupsRoot,
		MainNodeKey:              mainNodeKey,
	})
}

func (s Service) Initiate(ctx context.Context, input Manifest) (Status, error) {
	if s.Store == nil {
		return Status{}, fmt.Errorf("%w: transfer store is required", ErrInvalid)
	}
	now := s.now()
	manifest, err := NormalizeManifest(input, now)
	if err != nil {
		return Status{}, err
	}
	if strings.TrimSpace(manifest.IdempotencyKey) != "" {
		existing, err := s.Store.GetManifestByIdempotencyKey(ctx, manifest.IdempotencyKey)
		if err == nil {
			return s.Get(ctx, existing.TransferID)
		}
		if !errors.Is(err, ErrNotFound) {
			return Status{}, err
		}
	}
	manifest.Status = StatusPending
	manifest.CreatedAt = now
	manifest.UpdatedAt = now
	manifest, err = s.Store.UpsertManifest(ctx, manifest)
	if err != nil {
		return Status{}, err
	}
	chunks, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		return Status{}, err
	}
	for i := range chunks {
		chunks[i].TransferID = manifest.TransferID
		chunks[i].Status = ChunkStatusPending
		chunks[i].CreatedAt = now
		chunks[i].UpdatedAt = now
		chunks[i], err = s.Store.UpsertChunk(ctx, chunks[i])
		if err != nil {
			return Status{}, err
		}
	}
	if err := s.writeManifestFile(manifest); err != nil {
		return Status{}, err
	}
	if err := s.writeChunkSetFile(manifest.TransferID, chunks); err != nil {
		return Status{}, err
	}
	return buildStatus(manifest, chunks), nil
}

func (s Service) Get(ctx context.Context, transferID string) (Status, error) {
	manifest, chunks, err := s.getManifestAndChunks(ctx, transferID)
	if err != nil {
		return Status{}, err
	}
	return buildStatus(manifest, chunks), nil
}

func (s Service) List(ctx context.Context, filter ListFilter) ([]Status, error) {
	if s.Store == nil {
		return nil, fmt.Errorf("%w: transfer store is required", ErrInvalid)
	}
	manifests, err := s.Store.ListManifests(ctx, filter)
	if err != nil {
		return nil, err
	}
	statuses := make([]Status, 0, len(manifests))
	for _, manifest := range manifests {
		chunks, err := s.Store.ListChunks(ctx, manifest.TransferID)
		if err != nil {
			return nil, err
		}
		sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
		statuses = append(statuses, buildStatus(manifest, chunks))
	}
	return statuses, nil
}

func (s Service) UploadChunk(ctx context.Context, input UploadChunkInput) (UploadChunkResult, error) {
	if s.Store == nil {
		return UploadChunkResult{}, fmt.Errorf("%w: transfer store is required", ErrInvalid)
	}
	manifest, chunks, err := s.getManifestAndChunks(ctx, input.TransferID)
	if err != nil {
		return UploadChunkResult{}, err
	}
	if err := rejectRetiredTransferMutation(manifest); err != nil {
		return UploadChunkResult{}, err
	}
	if manifest.Status == StatusAccepted || manifest.Status == StatusAborted || manifest.Status == StatusCompleting {
		return UploadChunkResult{}, fmt.Errorf("%w: transfer status %s cannot accept chunks", ErrInvalid, manifest.Status)
	}
	expected, ok := chunkByIndex(chunks, input.Index)
	if !ok {
		return UploadChunkResult{}, fmt.Errorf("%w: chunk index %d was not planned", ErrInvalid, input.Index)
	}
	if int64(len(input.Payload)) != expected.SizeBytes {
		return UploadChunkResult{}, fmt.Errorf("%w: chunk %d size = %d, want %d", ErrInvalid, input.Index, len(input.Payload), expected.SizeBytes)
	}
	checksumAlgorithm := strings.TrimSpace(input.ChecksumAlgorithm)
	checksumHex := strings.TrimSpace(input.ChecksumHex)
	if checksumHex != "" && checksumAlgorithm == "" {
		checksumAlgorithm = ChecksumSHA256
	}
	if checksumHex != "" {
		if err := VerifyChecksum(checksumAlgorithm, checksumHex, input.Payload); err != nil {
			_ = s.failTransfer(ctx, manifest, "chunk_checksum_mismatch", err.Error())
			return UploadChunkResult{}, err
		}
	} else {
		checksumAlgorithm = ChecksumSHA256
		checksumHex = SHA256Hex(input.Payload)
	}
	chunkPath, err := s.chunkPath(manifest.TransferID, input.Index)
	if err != nil {
		return UploadChunkResult{}, err
	}
	if err := atomicWriteBytes(chunkPath, input.Payload, 0o600); err != nil {
		return UploadChunkResult{}, err
	}
	now := s.now()
	expected.ChecksumAlgorithm = checksumAlgorithm
	expected.ChecksumHex = checksumHex
	expected.Status = ChunkStatusUploaded
	expected.ReceivedBytes = int64(len(input.Payload))
	expected.StagingPath = filepath.ToSlash(chunkPath)
	expected.LastErrorCode = ""
	expected.LastErrorMessage = ""
	expected.UpdatedAt = now
	expected.ReceivedAt = &now
	chunk, err := s.Store.UpsertChunk(ctx, expected)
	if err != nil {
		return UploadChunkResult{}, err
	}
	if manifest.Status != StatusUploading {
		manifest, err = TransitionManifestStatus(manifest, StatusUploading, now)
		if err != nil {
			return UploadChunkResult{}, err
		}
	}
	manifest.LastErrorCode = ""
	manifest.LastErrorMessage = ""
	manifest, err = s.Store.UpsertManifest(ctx, manifest)
	if err != nil {
		return UploadChunkResult{}, err
	}
	chunks, err = s.Store.ListChunks(ctx, manifest.TransferID)
	if err != nil {
		return UploadChunkResult{}, err
	}
	if err := s.writeManifestFile(manifest); err != nil {
		return UploadChunkResult{}, err
	}
	if err := s.writeChunkSetFile(manifest.TransferID, chunks); err != nil {
		return UploadChunkResult{}, err
	}
	status := buildStatus(manifest, chunks)
	return UploadChunkResult{
		Manifest:      manifest,
		Chunk:         chunk,
		Status:        status,
		StagingPath:   chunk.StagingPath,
		ChecksumHex:   chunk.ChecksumHex,
		ReceivedBytes: chunk.ReceivedBytes,
	}, nil
}

func (s Service) Complete(ctx context.Context, transferID string) (CompleteResult, error) {
	if s.Store == nil {
		return CompleteResult{}, fmt.Errorf("%w: transfer store is required", ErrInvalid)
	}
	manifest, chunks, err := s.getManifestAndChunks(ctx, transferID)
	if err != nil {
		return CompleteResult{}, err
	}
	if err := rejectRetiredTransferMutation(manifest); err != nil {
		return CompleteResult{}, err
	}
	if manifest.Status == StatusAccepted {
		status := buildStatus(manifest, chunks)
		return CompleteResult{
			Manifest:             manifest,
			Status:               status,
			AcceptedPath:         manifest.AcceptedPath,
			StorageEntryID:       manifest.StorageEntryID,
			StoragePhysicalRefID: manifest.StoragePhysicalRefID,
			ChecksumHex:          manifest.ChecksumHex,
		}, nil
	}
	if manifest.Status == StatusAborted {
		return CompleteResult{}, fmt.Errorf("%w: aborted transfers cannot be completed", ErrInvalid)
	}
	expected, err := PlanChunks(manifest.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
	if err != nil {
		return CompleteResult{}, err
	}
	chunksByIndex := map[int64]Chunk{}
	for _, chunk := range chunks {
		chunksByIndex[chunk.Index] = chunk
	}
	for _, planned := range expected {
		chunk, ok := chunksByIndex[planned.Index]
		if !ok || (chunk.Status != ChunkStatusUploaded && chunk.Status != ChunkStatusAccepted) {
			return CompleteResult{}, fmt.Errorf("%w: chunk %d is missing", ErrInvalid, planned.Index)
		}
		if err := s.verifyChunkFile(chunk); err != nil {
			_ = s.failTransfer(ctx, manifest, "chunk_verify_failed", err.Error())
			return CompleteResult{}, err
		}
	}
	if manifest.Status != StatusCompleting {
		if manifest.Status == StatusPending && len(expected) == 0 {
			manifest, err = TransitionManifestStatus(manifest, StatusUploading, s.now())
			if err != nil {
				return CompleteResult{}, err
			}
		}
		if manifest.Status == StatusFailed {
			manifest, err = TransitionManifestStatus(manifest, StatusUploading, s.now())
			if err != nil {
				return CompleteResult{}, err
			}
		}
		manifest, err = TransitionManifestStatus(manifest, StatusCompleting, s.now())
		if err != nil {
			return CompleteResult{}, err
		}
		manifest, err = s.Store.UpsertManifest(ctx, manifest)
		if err != nil {
			return CompleteResult{}, err
		}
	}
	acceptedRel, acceptedPath, err := s.acceptedPath(manifest)
	if err != nil {
		return CompleteResult{}, err
	}
	if s.Catalog != nil {
		if _, err := s.currentViewPathForManifest(manifest, acceptedPath); err != nil {
			_ = s.failTransfer(ctx, manifest, "catalog_path_invalid", err.Error())
			return CompleteResult{}, err
		}
	}
	actualChecksum, err := s.assemble(manifest, expected, chunksByIndex, acceptedPath)
	if err != nil {
		failureCode := "assemble_failed"
		if errors.Is(err, ErrChecksumMismatch) {
			failureCode = "file_checksum_mismatch"
		}
		_ = s.failTransfer(ctx, manifest, failureCode, err.Error())
		return CompleteResult{}, err
	}
	manifest.ChecksumAlgorithm = ChecksumSHA256
	manifest.ChecksumHex = actualChecksum
	manifest.AcceptedPath = filepath.ToSlash(acceptedRel)
	entryID, refID, err := s.registerCatalog(ctx, manifest, acceptedPath)
	if err != nil {
		_ = s.failTransfer(ctx, manifest, "catalog_register_failed", err.Error())
		return CompleteResult{}, err
	}
	manifest.StorageEntryID = entryID
	manifest.StoragePhysicalRefID = refID
	now := s.now()
	manifest, err = TransitionManifestStatus(manifest, StatusAccepted, now)
	if err != nil {
		return CompleteResult{}, err
	}
	manifest, err = s.Store.UpsertManifest(ctx, manifest)
	if err != nil {
		return CompleteResult{}, err
	}
	for _, chunk := range chunks {
		if chunk.Status == ChunkStatusUploaded {
			chunk.Status = ChunkStatusAccepted
			chunk.UpdatedAt = now
			chunk.AcceptedAt = &now
			_, _ = s.Store.UpsertChunk(ctx, chunk)
		}
	}
	chunks, err = s.Store.ListChunks(ctx, manifest.TransferID)
	if err != nil {
		return CompleteResult{}, err
	}
	if err := s.writeManifestFile(manifest); err != nil {
		return CompleteResult{}, err
	}
	if err := s.writeChunkSetFile(manifest.TransferID, chunks); err != nil {
		return CompleteResult{}, err
	}
	_ = os.RemoveAll(filepath.Join(s.transferDir(manifest.TransferID), "chunks"))
	status := buildStatus(manifest, chunks)
	return CompleteResult{
		Manifest:             manifest,
		Status:               status,
		AcceptedPath:         manifest.AcceptedPath,
		StorageEntryID:       entryID,
		StoragePhysicalRefID: refID,
		ChecksumHex:          actualChecksum,
	}, nil
}

func (s Service) Abort(ctx context.Context, input AbortInput) (Status, error) {
	manifest, chunks, err := s.getManifestAndChunks(ctx, input.TransferID)
	if err != nil {
		return Status{}, err
	}
	if err := rejectRetiredTransferMutation(manifest); err != nil {
		return Status{}, err
	}
	if manifest.Status == StatusAccepted {
		return Status{}, fmt.Errorf("%w: accepted transfers cannot be aborted", ErrInvalid)
	}
	if manifest.Status != StatusAborted {
		manifest.LastErrorCode = "aborted"
		manifest.LastErrorMessage = strings.TrimSpace(input.Reason)
		manifest, err = TransitionManifestStatus(manifest, StatusAborted, s.now())
		if err != nil {
			return Status{}, err
		}
		manifest, err = s.Store.UpsertManifest(ctx, manifest)
		if err != nil {
			return Status{}, err
		}
		if err := s.writeManifestFile(manifest); err != nil {
			return Status{}, err
		}
	}
	return buildStatus(manifest, chunks), nil
}

func (s Service) getManifestAndChunks(ctx context.Context, transferID string) (Manifest, []Chunk, error) {
	if s.Store == nil {
		return Manifest{}, nil, fmt.Errorf("%w: transfer store is required", ErrInvalid)
	}
	manifest, err := s.Store.GetManifest(ctx, transferID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Manifest{}, nil, ErrNotFound
		}
		return Manifest{}, nil, err
	}
	chunks, err := s.Store.ListChunks(ctx, manifest.TransferID)
	if err != nil {
		return Manifest{}, nil, err
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return manifest, chunks, nil
}

func rejectRetiredTransferMutation(manifest Manifest) error {
	if manifest.TransferKind == KindDropzoneCustody {
		return fmt.Errorf("%w: transfer_kind %q is preserved for historical inspection only", ErrTransferRetired, manifest.TransferKind)
	}
	return nil
}

func (s Service) verifyChunkFile(chunk Chunk) error {
	if strings.TrimSpace(chunk.StagingPath) == "" {
		return fmt.Errorf("%w: chunk %d has no staging path", ErrInvalid, chunk.Index)
	}
	raw, err := os.ReadFile(filepath.FromSlash(chunk.StagingPath))
	if err != nil {
		return err
	}
	if int64(len(raw)) != chunk.SizeBytes {
		return fmt.Errorf("%w: chunk %d staged size = %d, want %d", ErrInvalid, chunk.Index, len(raw), chunk.SizeBytes)
	}
	if chunk.ChecksumHex != "" {
		return VerifyChecksum(chunk.ChecksumAlgorithm, chunk.ChecksumHex, raw)
	}
	return nil
}

func (s Service) assemble(manifest Manifest, planned []Chunk, chunksByIndex map[int64]Chunk, acceptedPath string) (string, error) {
	if manifest.TransferKind == KindWatchedRootBackup && strings.TrimSpace(s.Config.UserBackupsRoot) != "" {
		return s.assembleConfined(manifest, planned, chunksByIndex, acceptedPath)
	}
	if err := os.MkdirAll(filepath.Dir(acceptedPath), 0o750); err != nil {
		return "", err
	}
	tmpPath := acceptedPath + ".tmp"
	out, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	multi := io.MultiWriter(out, hash)
	for _, plannedChunk := range planned {
		chunk := chunksByIndex[plannedChunk.Index]
		in, err := os.Open(filepath.FromSlash(chunk.StagingPath))
		if err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return "", err
		}
		if _, err := io.Copy(multi, in); err != nil {
			_ = in.Close()
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return "", err
		}
		if err := in.Close(); err != nil {
			_ = out.Close()
			_ = os.Remove(tmpPath)
			return "", err
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o640); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if manifest.FileSizeBytes == 0 {
		// Ensure zero-byte transfers still create an accepted object.
		hash = sha256.New()
	}
	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if err := verifyDeclaredManifestChecksum(manifest, actualChecksum); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := verifyExistingAcceptedFile(acceptedPath, actualChecksum); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := os.Rename(tmpPath, acceptedPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return actualChecksum, nil
}

func (s Service) assembleConfined(manifest Manifest, planned []Chunk, chunksByIndex map[int64]Chunk, acceptedPath string) (string, error) {
	rootPath := filepath.Clean(strings.TrimSpace(s.Config.UserBackupsRoot))
	if err := os.MkdirAll(rootPath, 0o750); err != nil {
		return "", err
	}
	rootInfo, err := os.Lstat(rootPath)
	if err != nil {
		return "", err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", fmt.Errorf("%w: user backups root must be a real directory", ErrInvalid)
	}
	relative, err := filepath.Rel(rootPath, filepath.Clean(acceptedPath))
	if err != nil || relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: accepted path escapes user backups root", ErrInvalid)
	}
	confined, err := os.OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer confined.Close()
	parent := filepath.Dir(relative)
	if err := confined.MkdirAll(parent, 0o750); err != nil {
		return "", err
	}
	current := ""
	for _, component := range strings.Split(parent, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := confined.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("%w: accepted path parent %q must be a real directory", ErrInvalid, current)
		}
	}
	tmpRelative := relative + ".tmp-" + manifest.TransferID
	out, err := confined.OpenFile(tmpRelative, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return "", err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = confined.Remove(tmpRelative)
		}
	}()
	hash := sha256.New()
	multi := io.MultiWriter(out, hash)
	for _, plannedChunk := range planned {
		chunk := chunksByIndex[plannedChunk.Index]
		in, err := os.Open(filepath.FromSlash(chunk.StagingPath))
		if err != nil {
			_ = out.Close()
			return "", err
		}
		_, copyErr := io.Copy(multi, in)
		closeErr := in.Close()
		if copyErr != nil {
			_ = out.Close()
			return "", copyErr
		}
		if closeErr != nil {
			_ = out.Close()
			return "", closeErr
		}
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if err := verifyDeclaredManifestChecksum(manifest, actualChecksum); err != nil {
		return "", err
	}
	if err := verifyExistingAcceptedFileRoot(confined, relative, actualChecksum); err != nil {
		return "", err
	}
	if err := confined.Rename(tmpRelative, relative); err != nil {
		return "", err
	}
	removeTemp = false
	return actualChecksum, nil
}

func verifyDeclaredManifestChecksum(manifest Manifest, actualChecksum string) error {
	if strings.TrimSpace(manifest.ChecksumHex) == "" {
		return nil
	}
	algorithm := strings.TrimSpace(manifest.ChecksumAlgorithm)
	if algorithm == "" {
		algorithm = ChecksumSHA256
	}
	if algorithm != ChecksumSHA256 || !strings.EqualFold(strings.TrimSpace(manifest.ChecksumHex), actualChecksum) {
		return fmt.Errorf("%w: expected %s:%s got %s:%s", ErrChecksumMismatch, algorithm, manifest.ChecksumHex, ChecksumSHA256, actualChecksum)
	}
	return nil
}

func verifyExistingAcceptedFileRoot(root *os.Root, relative, checksumHex string) error {
	info, err := root.Lstat(relative)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: accepted path is not a regular file", ErrInvalid)
	}
	file, err := root.Open(relative)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), checksumHex) {
		return fmt.Errorf("%w: accepted path already exists with different checksum", ErrChecksumMismatch)
	}
	return nil
}

func verifyExistingAcceptedFile(acceptedPath, checksumHex string) error {
	info, err := os.Lstat(acceptedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%w: accepted path is a directory: %s", ErrInvalid, acceptedPath)
	}
	got, err := fileSHA256Hex(acceptedPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, checksumHex) {
		return fmt.Errorf("%w: accepted path already exists with different checksum: %s", ErrChecksumMismatch, acceptedPath)
	}
	return nil
}

func fileSHA256Hex(pathValue string) (string, error) {
	file, err := os.Open(pathValue)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (s Service) registerCatalog(ctx context.Context, manifest Manifest, acceptedPath string) (string, string, error) {
	if s.Catalog == nil {
		return "", "", nil
	}
	size := manifest.FileSizeBytes
	sourceArea := sourceAreaForManifest(manifest)
	storageClass := storageClassForManifest(manifest)
	viewPath, err := s.currentViewPathForManifest(manifest, acceptedPath)
	if err != nil {
		return "", "", err
	}
	metadata, err := json.Marshal(map[string]any{
		"schema_version":               "storage.file_transfer.metadata.v0.6.2",
		"source":                       "filetransfer.receiver",
		"file_transfer_id":             manifest.TransferID,
		"transfer_kind":                manifest.TransferKind,
		"custody_mode":                 manifest.CustodyMode,
		"source_node_key":              manifest.SourceNodeKey,
		"source_root_key":              manifest.SourceRootKey,
		"source_relative_path":         manifest.SourceRelativePath,
		"destination_logical_path":     manifest.DestinationLogicalPath,
		"accepted_path":                manifest.AcceptedPath,
		"watched_root_backup_batch_id": manifest.WatchedRootBackupBatchID,
		"watched_root_backup_item_id":  manifest.WatchedRootBackupItemID,
	})
	if err != nil {
		return "", "", err
	}
	entry, err := s.Catalog.RegisterEntry(ctx, storagecatalog.RegisterEntryInput{
		StorageClass:             storageClass,
		SourceArea:               sourceArea,
		OriginNodeID:             manifest.SourceNodeID,
		OriginNodeKey:            manifest.SourceNodeKey,
		WatchedRootID:            manifest.WatchedRootID,
		WatchedRootKey:           manifest.SourceRootKey,
		DropzoneTransferID:       manifest.DropzoneTransferID,
		PrivateBackupOperationID: manifest.PrivateBackupOperationID,
		PrivateBackupItemID:      manifest.WatchedRootBackupItemID,
		LogicalPath:              manifest.DestinationLogicalPath,
		OriginalSourcePath:       manifest.SourceRelativePath,
		CurrentViewPath:          viewPath,
		ChecksumAlgorithm:        manifest.ChecksumAlgorithm,
		ChecksumHex:              manifest.ChecksumHex,
		SizeBytes:                &size,
		FileClass:                storagecatalog.ClassifyPath(manifest.DestinationLogicalPath, ""),
		ProcessingState:          processingStateForManifest(manifest),
		AvailabilityState:        storagecatalog.AvailabilityStateAvailable,
		RetentionState:           storagecatalog.RetentionStateNone,
		Metadata:                 metadata,
	})
	if err != nil {
		return "", "", err
	}
	refMetadata, err := json.Marshal(map[string]any{
		"schema_version":   "storage.file_transfer_ref.metadata.v0.6.2",
		"source":           "filetransfer.receiver",
		"file_transfer_id": manifest.TransferID,
		"accepted_path":    manifest.AcceptedPath,
	})
	if err != nil {
		return "", "", err
	}
	refKind := physicalRefKindForManifest(manifest)
	ref, err := s.Catalog.RegisterPhysicalRef(ctx, storagecatalog.RegisterPhysicalRefInput{
		StorageEntryID: entry.StorageEntryID,
		RefKind:        refKind,
		URI:            storagecatalog.NormalizeSourcePath(acceptedPath),
		NodeKey:        s.Config.MainNodeKey,
		ContentAddress: contentAddress(manifest.ChecksumAlgorithm, manifest.ChecksumHex),
		Status:         storagecatalog.PhysicalRefStatusAvailable,
		Metadata:       refMetadata,
	})
	if err != nil {
		return "", "", err
	}
	return entry.StorageEntryID, ref.StoragePhysicalRefID, nil
}

func physicalRefKindForManifest(manifest Manifest) string {
	// Chunked watched-root backups are directly inspectable files. The
	// backup_artifact ref kind is reserved for the small/direct tar transport;
	// operation-specific readers extract a member whenever they see that kind.
	return storagecatalog.PhysicalRefKindLocalPath
}

func (s Service) failTransfer(ctx context.Context, manifest Manifest, code, message string) error {
	failed, err := MarkFailure(manifest, code, message, s.now())
	if err != nil {
		return err
	}
	if _, err := s.Store.UpsertManifest(ctx, failed); err != nil {
		return err
	}
	return s.writeManifestFile(failed)
}

func (s Service) acceptedPath(manifest Manifest) (string, string, error) {
	root := strings.TrimSpace(s.Config.AcceptedRoot)
	if manifest.TransferKind == KindWatchedRootBackup && strings.TrimSpace(s.Config.UserBackupsRoot) != "" {
		root = strings.TrimSpace(s.Config.UserBackupsRoot)
	}
	if root == "" {
		return "", "", fmt.Errorf("%w: accepted root is required", ErrInvalid)
	}
	root = filepath.Clean(root)
	rel, err := acceptedRelativePath(manifest)
	if err != nil {
		return "", "", err
	}
	if manifest.TransferKind == KindWatchedRootBackup && strings.TrimSpace(s.Config.UserBackupsRoot) == "" {
		rel = filepath.ToSlash(filepath.Join("watched-root-backups", filepath.FromSlash(rel)))
	}
	scopeID := manifest.TransferID
	if manifest.TransferKind == KindWatchedRootBackup {
		scopeID = firstNonEmpty(manifestMetadataString(manifest.Metadata, "local_batch_id"), manifest.TransferID)
	}
	if !strings.Contains("/"+filepath.ToSlash(rel)+"/", "/"+safePathSegment(scopeID)+"/") {
		return "", "", fmt.Errorf("%w: accepted path must be transfer or backup-batch scoped", ErrInvalid)
	}
	fullPath := filepath.Join(root, filepath.FromSlash(rel))
	if !localPathWithin(root, fullPath) {
		return "", "", fmt.Errorf("%w: accepted path escapes accepted root", ErrInvalid)
	}
	return rel, fullPath, nil
}

func localPathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel != "." && rel != "" && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func acceptedRelativePath(manifest Manifest) (string, error) {
	destination, err := storagecatalog.NormalizeLogicalPath(manifest.DestinationLogicalPath)
	if err != nil {
		return "", err
	}
	nodeKey := safePathSegment(firstNonEmpty(manifest.SourceNodeKey, "unknown-node"))
	rootKey := safePathSegment(firstNonEmpty(manifest.SourceRootKey, "unknown-root"))
	switch manifest.TransferKind {
	case KindWatchedRootBackup:
		batchKey := safePathSegment(firstNonEmpty(manifestMetadataString(manifest.Metadata, "local_batch_id"), manifest.TransferID))
		return filepath.ToSlash(filepath.Join(nodeKey, rootKey, batchKey, "payload", filepath.FromSlash(destination))), nil
	case KindMainDocumentsImport, KindManualUpload:
		return filepath.ToSlash(filepath.Join("main-documents", manifest.TransferID, filepath.FromSlash(destination))), nil
	default:
		return "", fmt.Errorf("%w: unsupported transfer_kind %q", ErrInvalid, manifest.TransferKind)
	}
}

func manifestMetadataString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var metadata map[string]any
	if json.Unmarshal(raw, &metadata) != nil {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func sourceAreaForManifest(manifest Manifest) string {
	switch manifest.TransferKind {
	case KindMainDocumentsImport, KindManualUpload:
		return storagecatalog.SourceAreaMainDocuments
	case KindWatchedRootBackup:
		rootKey := strings.ToLower(strings.TrimSpace(manifest.SourceRootKey))
		switch {
		case strings.Contains(rootKey, "notes"):
			return storagecatalog.SourceAreaNotes
		case strings.Contains(rootKey, "documents"):
			return storagecatalog.SourceAreaDocuments
		case strings.Contains(rootKey, "project"):
			return storagecatalog.SourceAreaProjects
		default:
			return storagecatalog.SourceAreaExternalWatchedRoot
		}
	default:
		return storagecatalog.SourceAreaUnknown
	}
}

func storageClassForManifest(manifest Manifest) string {
	switch manifest.TransferKind {
	case KindMainDocumentsImport, KindManualUpload:
		return storagecatalog.StorageClassMainDocument
	default:
		return storagecatalog.StorageClassPrivateBackup
	}
}

func processingStateForManifest(manifest Manifest) string {
	switch manifest.TransferKind {
	case KindWatchedRootBackup:
		return storagecatalog.ProcessingStateBackupOnly
	default:
		return storagecatalog.ProcessingStateMetadataOnly
	}
}

func (s Service) currentViewPathForManifest(manifest Manifest, acceptedPath string) (string, error) {
	destination := strings.TrimSpace(manifest.DestinationLogicalPath)
	sourceArea := sourceAreaForManifest(manifest)
	if manifest.TransferKind == KindWatchedRootBackup {
		return s.watchedBackupViewPath(acceptedPath)
	}
	switch sourceArea {
	case storagecatalog.SourceAreaMainDocuments:
		return path.Join("main", "Documents", destination), nil
	default:
		return destination, nil
	}
}

func (s Service) watchedBackupViewPath(acceptedPath string) (string, error) {
	storageRoot := filepath.Clean(strings.TrimSpace(s.Config.StorageRoot))
	canonicalBackupsRoot := filepath.Clean(strings.TrimSpace(s.Config.CanonicalUserBackupsRoot))
	backupsRoot := filepath.Clean(strings.TrimSpace(s.Config.UserBackupsRoot))
	acceptedPath = filepath.Clean(strings.TrimSpace(acceptedPath))
	if strings.TrimSpace(s.Config.StorageRoot) == "" || strings.TrimSpace(s.Config.CanonicalUserBackupsRoot) == "" || strings.TrimSpace(s.Config.UserBackupsRoot) == "" {
		return "", fmt.Errorf("%w: canonical storage/backups roots and active user backups root are required for watched-root catalog custody", ErrInvalid)
	}
	if !filepath.IsAbs(storageRoot) || !filepath.IsAbs(canonicalBackupsRoot) || !filepath.IsAbs(backupsRoot) || !filepath.IsAbs(acceptedPath) {
		return "", fmt.Errorf("%w: watched-root catalog custody paths must be absolute", ErrInvalid)
	}
	if !localPathWithin(backupsRoot, acceptedPath) {
		return "", fmt.Errorf("%w: accepted watched-root path escapes user backups root", ErrInvalid)
	}
	canonicalPrefix, err := filepath.Rel(storageRoot, canonicalBackupsRoot)
	if err != nil || canonicalPrefix == "." || canonicalPrefix == "" || canonicalPrefix == ".." || strings.HasPrefix(canonicalPrefix, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: canonical user backups root cannot be represented below storage root", ErrInvalid)
	}
	activeSuffix, err := filepath.Rel(backupsRoot, acceptedPath)
	if err != nil || activeSuffix == "." || activeSuffix == "" || activeSuffix == ".." || strings.HasPrefix(activeSuffix, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: accepted watched-root path cannot be represented below active user backups root", ErrInvalid)
	}
	viewPath, err := storagecatalog.NormalizeLogicalPath(filepath.ToSlash(filepath.Join(canonicalPrefix, activeSuffix)))
	if err != nil {
		return "", fmt.Errorf("%w: accepted watched-root catalog path is invalid: %v", ErrInvalid, err)
	}
	return viewPath, nil
}

func (s Service) writeManifestFile(manifest Manifest) error {
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBytes(filepath.Join(s.transferDir(manifest.TransferID), "manifest.json"), raw, 0o600)
}

func (s Service) writeChunkSetFile(transferID string, chunks []Chunk) error {
	raw, err := json.MarshalIndent(ChunkSet{
		SchemaVersion: ChunksSchemaVersion,
		TransferID:    transferID,
		Chunks:        chunks,
		UpdatedAt:     s.now(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBytes(filepath.Join(s.transferDir(transferID), "chunks.json"), raw, 0o600)
}

func (s Service) chunkPath(transferID string, index int64) (string, error) {
	if err := ValidateTransferID(transferID); err != nil {
		return "", err
	}
	if index < 0 {
		return "", fmt.Errorf("%w: chunk index cannot be negative", ErrInvalid)
	}
	return filepath.Join(s.transferDir(transferID), "chunks", fmt.Sprintf("%06d.part", index)), nil
}

func (s Service) transferDir(transferID string) string {
	return filepath.Join(strings.TrimSpace(s.Config.StagingRoot), transferID)
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func buildStatus(manifest Manifest, chunks []Chunk) Status {
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	var missing []int64
	var uploaded int64
	var accepted int64
	for _, chunk := range chunks {
		switch chunk.Status {
		case ChunkStatusUploaded:
			uploaded++
		case ChunkStatusAccepted:
			uploaded++
			accepted++
		default:
			missing = append(missing, chunk.Index)
		}
	}
	return Status{
		Manifest:       manifest,
		Chunks:         chunks,
		MissingChunks:  missing,
		UploadedChunks: uploaded,
		AcceptedChunks: accepted,
		AcceptedPath:   manifest.AcceptedPath,
	}
}

func chunkByIndex(chunks []Chunk, index int64) (Chunk, bool) {
	for _, chunk := range chunks {
		if chunk.Index == index {
			return chunk, true
		}
	}
	return Chunk{}, false
}

func atomicWriteBytes(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func safePathSegment(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = strings.Trim(value, "/")
	if value == "" || value == "." || value == ".." {
		return "unknown"
	}
	value = strings.ReplaceAll(value, "/", "_")
	value = strings.ReplaceAll(value, ":", "_")
	return value
}

func contentAddress(algorithm, value string) string {
	algorithm = strings.TrimSpace(algorithm)
	value = strings.TrimSpace(value)
	if algorithm == "" || value == "" {
		return ""
	}
	return algorithm + ":" + value
}
