package filetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
)

const (
	ManifestSchemaVersion = "loom.file_transfer.manifest.v0.6.2"
	ChunkSchemaVersion    = "loom.file_transfer.chunk.v0.6.2"
	ChunksSchemaVersion   = "loom.file_transfer.chunks.v0.6.2"

	ChecksumSHA256 = "sha256"

	DefaultChunkSizeBytes int64 = 64 * 1024 * 1024
	MaxChunkSizeBytes     int64 = 1024 * 1024 * 1024
)

const (
	KindWatchedRootBackup = "watched_root_backup"
	// KindDropzoneCustody is retained for decoding and filtering historical
	// manifests. New admission is rejected.
	KindDropzoneCustody     = "dropzone_custody"
	KindLaneCustody         = "lane_custody"
	KindMainDocumentsImport = "main_documents_import"
	KindManualUpload        = "manual_upload"
)

const (
	CustodyModeBackupCopy      = "backup_copy"
	CustodyModeCustodyTransfer = "custody_transfer"
	CustodyModeMainOwned       = "main_owned"
)

const (
	StatusPending    = "pending"
	StatusUploading  = "uploading"
	StatusPaused     = "paused"
	StatusCompleting = "completing"
	StatusAccepted   = "accepted"
	StatusFailed     = "failed"
	StatusAborted    = "aborted"
)

const (
	ChunkStatusPending   = "pending"
	ChunkStatusUploading = "uploading"
	ChunkStatusUploaded  = "uploaded"
	ChunkStatusAccepted  = "accepted"
	ChunkStatusFailed    = "failed"
	ChunkStatusSkipped   = "skipped"
)

var (
	ErrInvalid          = errors.New("invalid file transfer")
	ErrNotFound         = errors.New("file transfer not found")
	ErrChecksumMismatch = errors.New("file transfer checksum mismatch")
	ErrTransferRetired  = errors.New("file transfer kind is retired")
)

type Manifest struct {
	SchemaVersion            string          `json:"schema_version"`
	TransferID               string          `json:"transfer_id"`
	IdempotencyKey           string          `json:"idempotency_key,omitempty"`
	SourceNodeID             string          `json:"source_node_id,omitempty"`
	SourceNodeKey            string          `json:"source_node_key,omitempty"`
	SourceRootKey            string          `json:"source_root_key"`
	SourceRelativePath       string          `json:"source_relative_path"`
	DestinationLogicalPath   string          `json:"destination_logical_path"`
	TransferKind             string          `json:"transfer_kind"`
	CustodyMode              string          `json:"custody_mode"`
	FileSizeBytes            int64           `json:"file_size_bytes"`
	ModTime                  *time.Time      `json:"mtime,omitempty"`
	ChecksumAlgorithm        string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex              string          `json:"checksum_hex,omitempty"`
	ChunkSizeBytes           int64           `json:"chunk_size_bytes"`
	ChunkCount               int64           `json:"chunk_count"`
	Status                   string          `json:"status"`
	RetryCount               int             `json:"retry_count,omitempty"`
	LastErrorCode            string          `json:"last_error_code,omitempty"`
	LastErrorMessage         string          `json:"last_error_message,omitempty"`
	AcceptedPath             string          `json:"accepted_path,omitempty"`
	StorageEntryID           string          `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID     string          `json:"storage_physical_ref_id,omitempty"`
	DropzoneTransferID       string          `json:"dropzone_transfer_id,omitempty"`
	WatchedRootID            string          `json:"watched_root_id,omitempty"`
	WatchedRootBackupBatchID string          `json:"watched_root_backup_batch_id,omitempty"`
	WatchedRootBackupItemID  string          `json:"watched_root_backup_item_id,omitempty"`
	PrivateBackupOperationID string          `json:"private_backup_operation_id,omitempty"`
	Metadata                 json.RawMessage `json:"metadata,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	StartedAt                *time.Time      `json:"started_at,omitempty"`
	CompletedAt              *time.Time      `json:"completed_at,omitempty"`
	FailedAt                 *time.Time      `json:"failed_at,omitempty"`
	AbortedAt                *time.Time      `json:"aborted_at,omitempty"`
}

type Chunk struct {
	SchemaVersion     string          `json:"schema_version"`
	ChunkID           string          `json:"chunk_id,omitempty"`
	TransferID        string          `json:"transfer_id"`
	Index             int64           `json:"index"`
	OffsetBytes       int64           `json:"offset_bytes"`
	SizeBytes         int64           `json:"size_bytes"`
	ChecksumAlgorithm string          `json:"checksum_algorithm,omitempty"`
	ChecksumHex       string          `json:"checksum_hex,omitempty"`
	Status            string          `json:"status"`
	ReceivedBytes     int64           `json:"received_bytes,omitempty"`
	StagingPath       string          `json:"staging_path,omitempty"`
	LastErrorCode     string          `json:"last_error_code,omitempty"`
	LastErrorMessage  string          `json:"last_error_message,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	ReceivedAt        *time.Time      `json:"received_at,omitempty"`
	AcceptedAt        *time.Time      `json:"accepted_at,omitempty"`
}

type ChunkSet struct {
	SchemaVersion string    `json:"schema_version"`
	TransferID    string    `json:"transfer_id"`
	Chunks        []Chunk   `json:"chunks"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Status struct {
	Manifest       Manifest `json:"manifest"`
	Chunks         []Chunk  `json:"chunks"`
	MissingChunks  []int64  `json:"missing_chunks"`
	UploadedChunks int64    `json:"uploaded_chunks"`
	AcceptedChunks int64    `json:"accepted_chunks"`
	AcceptedPath   string   `json:"accepted_path,omitempty"`
}

type ListFilter struct {
	Status        string `json:"status,omitempty"`
	TransferKind  string `json:"transfer_kind,omitempty"`
	SourceNodeKey string `json:"source_node_key,omitempty"`
	SourceRootKey string `json:"source_root_key,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type UploadChunkInput struct {
	TransferID        string `json:"transfer_id"`
	Index             int64  `json:"index"`
	Payload           []byte `json:"-"`
	ChecksumAlgorithm string `json:"checksum_algorithm,omitempty"`
	ChecksumHex       string `json:"checksum_hex,omitempty"`
}

type UploadChunkResult struct {
	Manifest      Manifest `json:"manifest"`
	Chunk         Chunk    `json:"chunk"`
	Status        Status   `json:"status"`
	StagingPath   string   `json:"staging_path,omitempty"`
	ChecksumHex   string   `json:"checksum_hex,omitempty"`
	ReceivedBytes int64    `json:"received_bytes"`
}

type CompleteResult struct {
	Manifest             Manifest `json:"manifest"`
	Status               Status   `json:"status"`
	AcceptedPath         string   `json:"accepted_path"`
	StorageEntryID       string   `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID string   `json:"storage_physical_ref_id,omitempty"`
	ChecksumHex          string   `json:"checksum_hex,omitempty"`
}

type AbortInput struct {
	TransferID string `json:"transfer_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

func NewTransferID() string {
	return ids.NewFileTransferID()
}

func NewChunkID() string {
	return ids.NewFileTransferChunkID()
}

func ValidateTransferID(id string) error {
	if err := ids.Validate(ids.FileTransferPrefix, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func ValidateChunkID(id string) error {
	if err := ids.Validate(ids.FileTransferChunkPrefix, strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

func NormalizeManifest(input Manifest, now time.Time) (Manifest, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	input.SchemaVersion = firstNonEmpty(strings.TrimSpace(input.SchemaVersion), ManifestSchemaVersion)
	if input.SchemaVersion != ManifestSchemaVersion {
		return Manifest{}, fmt.Errorf("%w: unsupported schema_version %q", ErrInvalid, input.SchemaVersion)
	}
	if strings.TrimSpace(input.TransferID) == "" {
		input.TransferID = NewTransferID()
	}
	if err := ValidateTransferID(input.TransferID); err != nil {
		return Manifest{}, err
	}
	input.SourceNodeID = strings.TrimSpace(input.SourceNodeID)
	if input.SourceNodeID != "" && !strings.HasPrefix(input.SourceNodeID, ids.NodePrefix+"_") {
		return Manifest{}, fmt.Errorf("%w: source_node_id must use node_ prefix", ErrInvalid)
	}
	input.SourceNodeKey = strings.TrimSpace(input.SourceNodeKey)
	input.SourceRootKey = strings.TrimSpace(input.SourceRootKey)
	input.SourceRelativePath = normalizeRelativePath(input.SourceRelativePath)
	input.DestinationLogicalPath = normalizeRelativePath(input.DestinationLogicalPath)
	input.TransferKind = strings.TrimSpace(input.TransferKind)
	input.CustodyMode = strings.TrimSpace(input.CustodyMode)
	input.ChecksumAlgorithm = normalizeChecksumAlgorithm(input.ChecksumAlgorithm)
	input.ChecksumHex = normalizeChecksumHex(input.ChecksumHex)
	input.Status = firstNonEmpty(strings.TrimSpace(input.Status), StatusPending)
	input.AcceptedPath = normalizeRelativePath(input.AcceptedPath)
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	input.StoragePhysicalRefID = strings.TrimSpace(input.StoragePhysicalRefID)
	input.DropzoneTransferID = strings.TrimSpace(input.DropzoneTransferID)
	input.WatchedRootID = strings.TrimSpace(input.WatchedRootID)
	input.WatchedRootBackupBatchID = strings.TrimSpace(input.WatchedRootBackupBatchID)
	input.WatchedRootBackupItemID = strings.TrimSpace(input.WatchedRootBackupItemID)
	input.PrivateBackupOperationID = strings.TrimSpace(input.PrivateBackupOperationID)
	input.LastErrorCode = strings.TrimSpace(input.LastErrorCode)
	input.LastErrorMessage = strings.TrimSpace(input.LastErrorMessage)
	if input.FileSizeBytes < 0 {
		return Manifest{}, fmt.Errorf("%w: file_size_bytes must be non-negative", ErrInvalid)
	}
	if input.ChunkSizeBytes <= 0 {
		input.ChunkSizeBytes = DefaultChunkSizeBytes
	}
	if input.ChunkSizeBytes > MaxChunkSizeBytes {
		return Manifest{}, fmt.Errorf("%w: chunk_size_bytes exceeds maximum %d", ErrInvalid, MaxChunkSizeBytes)
	}
	wantChunks := chunkCount(input.FileSizeBytes, input.ChunkSizeBytes)
	if input.ChunkCount == 0 {
		input.ChunkCount = wantChunks
	}
	if input.ChunkCount != wantChunks {
		return Manifest{}, fmt.Errorf("%w: chunk_count = %d, want %d", ErrInvalid, input.ChunkCount, wantChunks)
	}
	if err := validateRequiredManifestFields(input); err != nil {
		return Manifest{}, err
	}
	if err := validateChecksum(input.ChecksumAlgorithm, input.ChecksumHex); err != nil {
		return Manifest{}, err
	}
	if err := validateStatus(input.Status); err != nil {
		return Manifest{}, err
	}
	if input.RetryCount < 0 {
		return Manifest{}, fmt.Errorf("%w: retry_count must be non-negative", ErrInvalid)
	}
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	if err := validateJSONObject("metadata", input.Metadata); err != nil {
		return Manifest{}, err
	}
	if input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey); input.IdempotencyKey == "" {
		input.IdempotencyKey = IdempotencyKey(input)
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = now
	} else {
		input.CreatedAt = input.CreatedAt.UTC()
	}
	if input.UpdatedAt.IsZero() {
		input.UpdatedAt = now
	} else {
		input.UpdatedAt = input.UpdatedAt.UTC()
	}
	return input, nil
}

func PlanChunks(transferID string, fileSizeBytes, chunkSizeBytes int64) ([]Chunk, error) {
	if strings.TrimSpace(transferID) != "" {
		if err := ValidateTransferID(transferID); err != nil {
			return nil, err
		}
	}
	if fileSizeBytes < 0 {
		return nil, fmt.Errorf("%w: file_size_bytes must be non-negative", ErrInvalid)
	}
	if chunkSizeBytes <= 0 {
		return nil, fmt.Errorf("%w: chunk_size_bytes must be positive", ErrInvalid)
	}
	if chunkSizeBytes > MaxChunkSizeBytes {
		return nil, fmt.Errorf("%w: chunk_size_bytes exceeds maximum %d", ErrInvalid, MaxChunkSizeBytes)
	}
	total := chunkCount(fileSizeBytes, chunkSizeBytes)
	if total > int64(math.MaxInt32) {
		return nil, fmt.Errorf("%w: chunk count exceeds supported maximum", ErrInvalid)
	}
	chunks := make([]Chunk, 0, total)
	for i := int64(0); i < total; i++ {
		offset := i * chunkSizeBytes
		size := chunkSizeBytes
		if remaining := fileSizeBytes - offset; remaining < size {
			size = remaining
		}
		chunks = append(chunks, Chunk{
			SchemaVersion: ChunkSchemaVersion,
			ChunkID:       NewChunkID(),
			TransferID:    transferID,
			Index:         i,
			OffsetBytes:   offset,
			SizeBytes:     size,
			Status:        ChunkStatusPending,
		})
	}
	return chunks, nil
}

func NormalizeChunks(manifest Manifest, chunks []Chunk, now time.Time) ([]Chunk, error) {
	manifest, err := NormalizeManifest(manifest, now)
	if err != nil {
		return nil, err
	}
	if int64(len(chunks)) != manifest.ChunkCount {
		return nil, fmt.Errorf("%w: chunk length = %d, want %d", ErrInvalid, len(chunks), manifest.ChunkCount)
	}
	return normalizeChunkList(manifest, chunks, now)
}

func NormalizeChunk(manifest Manifest, chunk Chunk, now time.Time) (Chunk, error) {
	manifest, err := NormalizeManifest(manifest, now)
	if err != nil {
		return Chunk{}, err
	}
	chunks, err := normalizeChunkList(manifest, []Chunk{chunk}, now)
	if err != nil {
		return Chunk{}, err
	}
	return chunks[0], nil
}

func normalizeChunkList(manifest Manifest, chunks []Chunk, now time.Time) ([]Chunk, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	seen := make(map[int64]bool, len(chunks))
	out := make([]Chunk, len(chunks))
	for i, chunk := range chunks {
		chunk.SchemaVersion = firstNonEmpty(strings.TrimSpace(chunk.SchemaVersion), ChunkSchemaVersion)
		if chunk.SchemaVersion != ChunkSchemaVersion {
			return nil, fmt.Errorf("%w: unsupported chunk schema_version %q", ErrInvalid, chunk.SchemaVersion)
		}
		if strings.TrimSpace(chunk.ChunkID) == "" {
			chunk.ChunkID = NewChunkID()
		}
		if err := ValidateChunkID(chunk.ChunkID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(chunk.TransferID) == "" {
			chunk.TransferID = manifest.TransferID
		}
		if chunk.TransferID != manifest.TransferID {
			return nil, fmt.Errorf("%w: chunk transfer_id %q does not match manifest %q", ErrInvalid, chunk.TransferID, manifest.TransferID)
		}
		if chunk.Index < 0 || chunk.Index >= manifest.ChunkCount {
			return nil, fmt.Errorf("%w: chunk index %d out of range", ErrInvalid, chunk.Index)
		}
		if seen[chunk.Index] {
			return nil, fmt.Errorf("%w: duplicate chunk index %d", ErrInvalid, chunk.Index)
		}
		seen[chunk.Index] = true
		wantOffset := chunk.Index * manifest.ChunkSizeBytes
		if chunk.OffsetBytes != wantOffset {
			return nil, fmt.Errorf("%w: chunk %d offset = %d, want %d", ErrInvalid, chunk.Index, chunk.OffsetBytes, wantOffset)
		}
		wantSize := manifest.ChunkSizeBytes
		if remaining := manifest.FileSizeBytes - chunk.OffsetBytes; remaining < wantSize {
			wantSize = remaining
		}
		if chunk.SizeBytes != wantSize {
			return nil, fmt.Errorf("%w: chunk %d size = %d, want %d", ErrInvalid, chunk.Index, chunk.SizeBytes, wantSize)
		}
		chunk.ChecksumAlgorithm = normalizeChecksumAlgorithm(chunk.ChecksumAlgorithm)
		chunk.ChecksumHex = normalizeChecksumHex(chunk.ChecksumHex)
		if err := validateChecksum(chunk.ChecksumAlgorithm, chunk.ChecksumHex); err != nil {
			return nil, err
		}
		chunk.Status = firstNonEmpty(strings.TrimSpace(chunk.Status), ChunkStatusPending)
		if err := validateChunkStatus(chunk.Status); err != nil {
			return nil, err
		}
		if chunk.ReceivedBytes < 0 || chunk.ReceivedBytes > chunk.SizeBytes {
			return nil, fmt.Errorf("%w: chunk %d received_bytes is invalid", ErrInvalid, chunk.Index)
		}
		if len(chunk.Metadata) == 0 {
			chunk.Metadata = json.RawMessage(`{}`)
		}
		if err := validateJSONObject("chunk metadata", chunk.Metadata); err != nil {
			return nil, err
		}
		if chunk.CreatedAt.IsZero() {
			chunk.CreatedAt = now
		} else {
			chunk.CreatedAt = chunk.CreatedAt.UTC()
		}
		if chunk.UpdatedAt.IsZero() {
			chunk.UpdatedAt = now
		} else {
			chunk.UpdatedAt = chunk.UpdatedAt.UTC()
		}
		out[i] = chunk
	}
	return out, nil
}

func IdempotencyKey(manifest Manifest) string {
	parts := []string{
		strings.TrimSpace(manifest.TransferKind),
		strings.TrimSpace(manifest.CustodyMode),
		strings.TrimSpace(manifest.SourceNodeID),
		strings.TrimSpace(manifest.SourceNodeKey),
		strings.TrimSpace(manifest.SourceRootKey),
		normalizeRelativePath(manifest.SourceRelativePath),
		normalizeRelativePath(manifest.DestinationLogicalPath),
		strconv.FormatInt(manifest.FileSizeBytes, 10),
		strings.TrimSpace(manifest.ChecksumAlgorithm),
		normalizeChecksumHex(manifest.ChecksumHex),
	}
	if manifest.ModTime != nil {
		parts = append(parts, manifest.ModTime.UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func SHA256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func VerifyChecksum(algorithm, expectedHex string, payload []byte) error {
	algorithm = normalizeChecksumAlgorithm(algorithm)
	expectedHex = normalizeChecksumHex(expectedHex)
	if algorithm == "" && expectedHex == "" {
		return nil
	}
	if algorithm != ChecksumSHA256 {
		return fmt.Errorf("%w: unsupported checksum algorithm %q", ErrInvalid, algorithm)
	}
	actual := SHA256Hex(payload)
	if !strings.EqualFold(actual, expectedHex) {
		return fmt.Errorf("%w: expected %s:%s got %s:%s", ErrChecksumMismatch, algorithm, expectedHex, algorithm, actual)
	}
	return nil
}

func TransitionManifestStatus(manifest Manifest, nextStatus string, now time.Time) (Manifest, error) {
	current := firstNonEmpty(strings.TrimSpace(manifest.Status), StatusPending)
	nextStatus = strings.TrimSpace(nextStatus)
	if err := validateStatus(current); err != nil {
		return Manifest{}, err
	}
	if err := validateStatus(nextStatus); err != nil {
		return Manifest{}, err
	}
	if !CanTransition(current, nextStatus) {
		return Manifest{}, fmt.Errorf("%w: cannot transition transfer from %s to %s", ErrInvalid, current, nextStatus)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	manifest.Status = nextStatus
	manifest.UpdatedAt = now
	switch nextStatus {
	case StatusUploading:
		if manifest.StartedAt == nil {
			manifest.StartedAt = &now
		}
	case StatusAccepted:
		manifest.CompletedAt = &now
		manifest.LastErrorCode = ""
		manifest.LastErrorMessage = ""
	case StatusFailed:
		manifest.FailedAt = &now
	case StatusAborted:
		manifest.AbortedAt = &now
	}
	return manifest, nil
}

func CanTransition(current, next string) bool {
	current = firstNonEmpty(strings.TrimSpace(current), StatusPending)
	next = strings.TrimSpace(next)
	if current == next {
		return true
	}
	switch current {
	case StatusPending:
		return next == StatusUploading || next == StatusPaused || next == StatusFailed || next == StatusAborted
	case StatusUploading:
		return next == StatusPaused || next == StatusCompleting || next == StatusFailed || next == StatusAborted
	case StatusPaused:
		return next == StatusUploading || next == StatusFailed || next == StatusAborted
	case StatusCompleting:
		return next == StatusAccepted || next == StatusFailed || next == StatusAborted
	case StatusFailed:
		return next == StatusPending || next == StatusUploading || next == StatusAborted
	case StatusAccepted, StatusAborted:
		return false
	default:
		return false
	}
}

func MarkFailure(manifest Manifest, code, message string, now time.Time) (Manifest, error) {
	manifest.LastErrorCode = strings.TrimSpace(code)
	manifest.LastErrorMessage = strings.TrimSpace(message)
	manifest.RetryCount++
	return TransitionManifestStatus(manifest, StatusFailed, now)
}

func validateRequiredManifestFields(manifest Manifest) error {
	if manifest.TransferKind == KindDropzoneCustody {
		return fmt.Errorf("%w: transfer_kind %q is decode-only", ErrTransferRetired, manifest.TransferKind)
	}
	if !validKinds[manifest.TransferKind] {
		return fmt.Errorf("%w: unsupported transfer_kind %q", ErrInvalid, manifest.TransferKind)
	}
	if !validCustodyModes[manifest.CustodyMode] {
		return fmt.Errorf("%w: unsupported custody_mode %q", ErrInvalid, manifest.CustodyMode)
	}
	if manifest.SourceRootKey == "" {
		return fmt.Errorf("%w: source_root_key is required", ErrInvalid)
	}
	if manifest.SourceRelativePath == "" {
		return fmt.Errorf("%w: source_relative_path is required", ErrInvalid)
	}
	if manifest.DestinationLogicalPath == "" {
		return fmt.Errorf("%w: destination_logical_path is required", ErrInvalid)
	}
	return nil
}

func validateStatus(status string) error {
	if !validStatuses[status] {
		return fmt.Errorf("%w: unsupported status %q", ErrInvalid, status)
	}
	return nil
}

func validateChunkStatus(status string) error {
	if !validChunkStatuses[status] {
		return fmt.Errorf("%w: unsupported chunk status %q", ErrInvalid, status)
	}
	return nil
}

func validateChecksum(algorithm, checksumHex string) error {
	if checksumHex == "" {
		if algorithm != "" {
			return fmt.Errorf("%w: checksum_hex is required when checksum_algorithm is set", ErrInvalid)
		}
		return nil
	}
	if algorithm == "" {
		return fmt.Errorf("%w: checksum_algorithm is required when checksum_hex is set", ErrInvalid)
	}
	if algorithm != ChecksumSHA256 {
		return fmt.Errorf("%w: unsupported checksum algorithm %q", ErrInvalid, algorithm)
	}
	if len(checksumHex) != 64 {
		return fmt.Errorf("%w: checksum_hex must be 64 hex characters", ErrInvalid)
	}
	if _, err := hex.DecodeString(checksumHex); err != nil {
		return fmt.Errorf("%w: checksum_hex must be hex: %v", ErrInvalid, err)
	}
	return nil
}

func validateJSONObject(label string, raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%w: %s must be valid JSON: %v", ErrInvalid, label, err)
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("%w: %s must be a JSON object", ErrInvalid, label)
	}
	return nil
}

func chunkCount(fileSizeBytes, chunkSizeBytes int64) int64 {
	if fileSizeBytes == 0 {
		return 0
	}
	return (fileSizeBytes + chunkSizeBytes - 1) / chunkSizeBytes
}

func normalizeChecksumAlgorithm(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeChecksumHex(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, ChecksumSHA256+":")
	return value
}

func normalizeRelativePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

var validKinds = map[string]bool{
	KindWatchedRootBackup:   true,
	KindMainDocumentsImport: true,
	KindManualUpload:        true,
}

var validCustodyModes = map[string]bool{
	CustodyModeBackupCopy:      true,
	CustodyModeCustodyTransfer: true,
	CustodyModeMainOwned:       true,
}

var validStatuses = map[string]bool{
	StatusPending:    true,
	StatusUploading:  true,
	StatusPaused:     true,
	StatusCompleting: true,
	StatusAccepted:   true,
	StatusFailed:     true,
	StatusAborted:    true,
}

var validChunkStatuses = map[string]bool{
	ChunkStatusPending:   true,
	ChunkStatusUploading: true,
	ChunkStatusUploaded:  true,
	ChunkStatusAccepted:  true,
	ChunkStatusFailed:    true,
	ChunkStatusSkipped:   true,
}
