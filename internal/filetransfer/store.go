package filetransfer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type sqlScanner interface {
	Scan(dest ...any) error
}

type Store struct {
	DB *sql.DB
}

func NewStore(db *sql.DB) Store {
	return Store{DB: db}
}

func (s Store) UpsertManifest(ctx context.Context, manifest Manifest) (Manifest, error) {
	if s.DB == nil {
		return Manifest{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	manifest, err := NormalizeManifest(manifest, time.Now().UTC())
	if err != nil {
		return Manifest{}, err
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.file_transfers (
			file_transfer_id,
			idempotency_key,
			source_node_id,
			source_node_key,
			source_root_key,
			source_relative_path,
			destination_logical_path,
			transfer_kind,
			custody_mode,
			file_size_bytes,
			mtime,
			checksum_algorithm,
			checksum_hex,
			chunk_size_bytes,
			chunk_count,
			status,
			retry_count,
			last_error_code,
			last_error_message,
			accepted_path,
			storage_entry_id,
			storage_physical_ref_id,
			dropzone_transfer_id,
			watched_root_id,
			watched_root_backup_batch_id,
			watched_root_backup_item_id,
			private_backup_operation_id,
			metadata,
			created_at,
			updated_at,
			started_at,
			completed_at,
			failed_at,
			aborted_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
			$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34
		)
		ON CONFLICT (file_transfer_id) DO UPDATE SET
			idempotency_key = EXCLUDED.idempotency_key,
			source_node_id = EXCLUDED.source_node_id,
			source_node_key = EXCLUDED.source_node_key,
			source_root_key = EXCLUDED.source_root_key,
			source_relative_path = EXCLUDED.source_relative_path,
			destination_logical_path = EXCLUDED.destination_logical_path,
			transfer_kind = EXCLUDED.transfer_kind,
			custody_mode = EXCLUDED.custody_mode,
			file_size_bytes = EXCLUDED.file_size_bytes,
			mtime = EXCLUDED.mtime,
			checksum_algorithm = EXCLUDED.checksum_algorithm,
			checksum_hex = EXCLUDED.checksum_hex,
			chunk_size_bytes = EXCLUDED.chunk_size_bytes,
			chunk_count = EXCLUDED.chunk_count,
			status = EXCLUDED.status,
			retry_count = EXCLUDED.retry_count,
			last_error_code = EXCLUDED.last_error_code,
			last_error_message = EXCLUDED.last_error_message,
			accepted_path = EXCLUDED.accepted_path,
			storage_entry_id = EXCLUDED.storage_entry_id,
			storage_physical_ref_id = EXCLUDED.storage_physical_ref_id,
			dropzone_transfer_id = EXCLUDED.dropzone_transfer_id,
			watched_root_id = EXCLUDED.watched_root_id,
			watched_root_backup_batch_id = EXCLUDED.watched_root_backup_batch_id,
			watched_root_backup_item_id = EXCLUDED.watched_root_backup_item_id,
			private_backup_operation_id = EXCLUDED.private_backup_operation_id,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at,
			started_at = EXCLUDED.started_at,
			completed_at = EXCLUDED.completed_at,
			failed_at = EXCLUDED.failed_at,
			aborted_at = EXCLUDED.aborted_at
		RETURNING `+manifestSelectColumns(),
		manifest.TransferID,
		manifest.IdempotencyKey,
		nullableString(manifest.SourceNodeID),
		manifest.SourceNodeKey,
		manifest.SourceRootKey,
		manifest.SourceRelativePath,
		manifest.DestinationLogicalPath,
		manifest.TransferKind,
		manifest.CustodyMode,
		manifest.FileSizeBytes,
		nullableTime(manifest.ModTime),
		manifest.ChecksumAlgorithm,
		manifest.ChecksumHex,
		manifest.ChunkSizeBytes,
		manifest.ChunkCount,
		manifest.Status,
		manifest.RetryCount,
		manifest.LastErrorCode,
		manifest.LastErrorMessage,
		manifest.AcceptedPath,
		nullableString(manifest.StorageEntryID),
		nullableString(manifest.StoragePhysicalRefID),
		manifest.DropzoneTransferID,
		nullableString(manifest.WatchedRootID),
		nullableString(manifest.WatchedRootBackupBatchID),
		nullableString(manifest.WatchedRootBackupItemID),
		nullableString(manifest.PrivateBackupOperationID),
		manifest.Metadata,
		manifest.CreatedAt,
		manifest.UpdatedAt,
		nullableTime(manifest.StartedAt),
		nullableTime(manifest.CompletedAt),
		nullableTime(manifest.FailedAt),
		nullableTime(manifest.AbortedAt),
	)
	if err := row.Err(); err != nil {
		return Manifest{}, err
	}
	return scanManifest(row)
}

func (s Store) GetManifest(ctx context.Context, transferID string) (Manifest, error) {
	if s.DB == nil {
		return Manifest{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if err := ValidateTransferID(transferID); err != nil {
		return Manifest{}, err
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+manifestSelectColumns()+` FROM storage.file_transfers WHERE file_transfer_id = $1`, transferID)
	if err := row.Err(); err != nil {
		return Manifest{}, err
	}
	manifest, err := scanManifest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Manifest{}, ErrNotFound
	}
	return manifest, err
}

func (s Store) GetManifestByIdempotencyKey(ctx context.Context, idempotencyKey string) (Manifest, error) {
	if s.DB == nil {
		return Manifest{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return Manifest{}, ErrNotFound
	}
	row := s.DB.QueryRowContext(ctx, `SELECT `+manifestSelectColumns()+` FROM storage.file_transfers WHERE idempotency_key = $1`, idempotencyKey)
	if err := row.Err(); err != nil {
		return Manifest{}, err
	}
	manifest, err := scanManifest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Manifest{}, ErrNotFound
	}
	return manifest, err
}

func (s Store) ListManifests(ctx context.Context, filter ListFilter) ([]Manifest, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+manifestSelectColumns()+`
		FROM storage.file_transfers
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR transfer_kind = $2)
		  AND ($3 = '' OR source_node_key = $3)
		  AND ($4 = '' OR source_root_key = $4)
		ORDER BY updated_at DESC, created_at DESC
		LIMIT $5`,
		strings.TrimSpace(filter.Status),
		strings.TrimSpace(filter.TransferKind),
		strings.TrimSpace(filter.SourceNodeKey),
		strings.TrimSpace(filter.SourceRootKey),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var manifests []Manifest
	for rows.Next() {
		manifest, err := scanManifest(rows)
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return manifests, nil
}

func (s Store) UpsertChunk(ctx context.Context, chunk Chunk) (Chunk, error) {
	if s.DB == nil {
		return Chunk{}, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if chunk.TransferID == "" {
		return Chunk{}, fmt.Errorf("%w: chunk transfer_id is required", ErrInvalid)
	}
	manifest, err := s.GetManifest(ctx, chunk.TransferID)
	if err != nil {
		return Chunk{}, err
	}
	chunk, err = NormalizeChunk(manifest, chunk, time.Now().UTC())
	if err != nil {
		planned, planErr := PlanChunks(chunk.TransferID, manifest.FileSizeBytes, manifest.ChunkSizeBytes)
		if planErr != nil || chunk.Index < 0 || chunk.Index >= int64(len(planned)) {
			return Chunk{}, err
		}
		expected := planned[chunk.Index]
		chunk.OffsetBytes = expected.OffsetBytes
		chunk.SizeBytes = expected.SizeBytes
		chunk, err = NormalizeChunk(manifest, chunk, time.Now().UTC())
		if err != nil {
			return Chunk{}, err
		}
	}
	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.file_transfer_chunks (
			file_transfer_chunk_id,
			file_transfer_id,
			chunk_index,
			offset_bytes,
			size_bytes,
			checksum_algorithm,
			checksum_hex,
			status,
			received_bytes,
			staging_path,
			last_error_code,
			last_error_message,
			metadata,
			created_at,
			updated_at,
			received_at,
			accepted_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
		)
		ON CONFLICT (file_transfer_id, chunk_index) DO UPDATE SET
			size_bytes = EXCLUDED.size_bytes,
			checksum_algorithm = EXCLUDED.checksum_algorithm,
			checksum_hex = EXCLUDED.checksum_hex,
			status = EXCLUDED.status,
			received_bytes = EXCLUDED.received_bytes,
			staging_path = EXCLUDED.staging_path,
			last_error_code = EXCLUDED.last_error_code,
			last_error_message = EXCLUDED.last_error_message,
			metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at,
			received_at = EXCLUDED.received_at,
			accepted_at = EXCLUDED.accepted_at
		RETURNING `+chunkSelectColumns(),
		chunk.ChunkID,
		chunk.TransferID,
		chunk.Index,
		chunk.OffsetBytes,
		chunk.SizeBytes,
		chunk.ChecksumAlgorithm,
		chunk.ChecksumHex,
		chunk.Status,
		chunk.ReceivedBytes,
		chunk.StagingPath,
		chunk.LastErrorCode,
		chunk.LastErrorMessage,
		chunk.Metadata,
		chunk.CreatedAt,
		chunk.UpdatedAt,
		nullableTime(chunk.ReceivedAt),
		nullableTime(chunk.AcceptedAt),
	)
	if err := row.Err(); err != nil {
		return Chunk{}, err
	}
	return scanChunk(row)
}

func (s Store) ListChunks(ctx context.Context, transferID string) ([]Chunk, error) {
	if s.DB == nil {
		return nil, fmt.Errorf("%w: database is required", ErrInvalid)
	}
	if err := ValidateTransferID(transferID); err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+chunkSelectColumns()+` FROM storage.file_transfer_chunks WHERE file_transfer_id = $1 ORDER BY chunk_index`, transferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return chunks, nil
}

func manifestSelectColumns() string {
	return `file_transfer_id, idempotency_key, source_node_id, source_node_key, source_root_key,
		source_relative_path, destination_logical_path, transfer_kind, custody_mode, file_size_bytes,
		mtime, checksum_algorithm, checksum_hex, chunk_size_bytes, chunk_count, status, retry_count,
		last_error_code, last_error_message, accepted_path, storage_entry_id, storage_physical_ref_id,
		dropzone_transfer_id, watched_root_id, watched_root_backup_batch_id, watched_root_backup_item_id,
		private_backup_operation_id, metadata, created_at, updated_at, started_at, completed_at, failed_at, aborted_at`
}

func chunkSelectColumns() string {
	return `file_transfer_chunk_id, file_transfer_id, chunk_index, offset_bytes, size_bytes,
		checksum_algorithm, checksum_hex, status, received_bytes, staging_path, last_error_code,
		last_error_message, metadata, created_at, updated_at, received_at, accepted_at`
}

func scanManifest(row sqlScanner) (Manifest, error) {
	var manifest Manifest
	var sourceNodeID sql.NullString
	var mtime sql.NullTime
	var storageEntryID sql.NullString
	var storagePhysicalRefID sql.NullString
	var watchedRootID sql.NullString
	var watchedRootBackupBatchID sql.NullString
	var watchedRootBackupItemID sql.NullString
	var privateBackupOperationID sql.NullString
	var metadata []byte
	var startedAt sql.NullTime
	var completedAt sql.NullTime
	var failedAt sql.NullTime
	var abortedAt sql.NullTime
	if err := row.Scan(
		&manifest.TransferID,
		&manifest.IdempotencyKey,
		&sourceNodeID,
		&manifest.SourceNodeKey,
		&manifest.SourceRootKey,
		&manifest.SourceRelativePath,
		&manifest.DestinationLogicalPath,
		&manifest.TransferKind,
		&manifest.CustodyMode,
		&manifest.FileSizeBytes,
		&mtime,
		&manifest.ChecksumAlgorithm,
		&manifest.ChecksumHex,
		&manifest.ChunkSizeBytes,
		&manifest.ChunkCount,
		&manifest.Status,
		&manifest.RetryCount,
		&manifest.LastErrorCode,
		&manifest.LastErrorMessage,
		&manifest.AcceptedPath,
		&storageEntryID,
		&storagePhysicalRefID,
		&manifest.DropzoneTransferID,
		&watchedRootID,
		&watchedRootBackupBatchID,
		&watchedRootBackupItemID,
		&privateBackupOperationID,
		&metadata,
		&manifest.CreatedAt,
		&manifest.UpdatedAt,
		&startedAt,
		&completedAt,
		&failedAt,
		&abortedAt,
	); err != nil {
		return Manifest{}, err
	}
	manifest.SchemaVersion = ManifestSchemaVersion
	manifest.SourceNodeID = stringFromNull(sourceNodeID)
	manifest.ModTime = timeFromNull(mtime)
	manifest.StorageEntryID = stringFromNull(storageEntryID)
	manifest.StoragePhysicalRefID = stringFromNull(storagePhysicalRefID)
	manifest.WatchedRootID = stringFromNull(watchedRootID)
	manifest.WatchedRootBackupBatchID = stringFromNull(watchedRootBackupBatchID)
	manifest.WatchedRootBackupItemID = stringFromNull(watchedRootBackupItemID)
	manifest.PrivateBackupOperationID = stringFromNull(privateBackupOperationID)
	manifest.Metadata = json.RawMessage(metadata)
	manifest.StartedAt = timeFromNull(startedAt)
	manifest.CompletedAt = timeFromNull(completedAt)
	manifest.FailedAt = timeFromNull(failedAt)
	manifest.AbortedAt = timeFromNull(abortedAt)
	return manifest, nil
}

func scanChunk(row sqlScanner) (Chunk, error) {
	var chunk Chunk
	var metadata []byte
	var receivedAt sql.NullTime
	var acceptedAt sql.NullTime
	if err := row.Scan(
		&chunk.ChunkID,
		&chunk.TransferID,
		&chunk.Index,
		&chunk.OffsetBytes,
		&chunk.SizeBytes,
		&chunk.ChecksumAlgorithm,
		&chunk.ChecksumHex,
		&chunk.Status,
		&chunk.ReceivedBytes,
		&chunk.StagingPath,
		&chunk.LastErrorCode,
		&chunk.LastErrorMessage,
		&metadata,
		&chunk.CreatedAt,
		&chunk.UpdatedAt,
		&receivedAt,
		&acceptedAt,
	); err != nil {
		return Chunk{}, err
	}
	chunk.SchemaVersion = ChunkSchemaVersion
	chunk.Metadata = json.RawMessage(metadata)
	chunk.ReceivedAt = timeFromNull(receivedAt)
	chunk.AcceptedAt = timeFromNull(acceptedAt)
	return chunk, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

func stringFromNull(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func timeFromNull(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}
