package storagecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
)

type Service struct {
	DB *sql.DB
}

const (
	defaultListEntriesLimit = 50
	maxListEntriesLimit     = 5000
	bulkLookupChunkSize     = 5000
)

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

// RebindPaths applies a reviewed filesystem manifest as one PostgreSQL
// transaction. Each row is locked and compared with its planned prior value;
// a changed row aborts the whole operation.
func (s Service) RebindPaths(ctx context.Context, input RebindPathsInput) (RebindPathsResult, error) {
	normalized, err := normalizeRebindPathsInput(input)
	if err != nil {
		return RebindPathsResult{}, err
	}
	return rebindPaths(ctx, s.DB, normalized)
}

// RebindPathBatches applies every bounded child of one reviewed migration set
// in a single PostgreSQL transaction. The loader is streamed once for complete
// structural preflight and again inside the transaction; a reload failure or
// changed row in any later child rolls back mutations made by earlier children.
func (s Service) RebindPathBatches(ctx context.Context, batchCount int, load RebindPathsBatchLoader) (RebindPathsResult, error) {
	if batchCount <= 0 {
		return RebindPathsResult{}, fmt.Errorf("%w: at least one path-rebind batch is required", ErrInvalid)
	}
	if load == nil {
		return RebindPathsResult{}, fmt.Errorf("%w: path-rebind batch loader is required", ErrInvalid)
	}
	if err := preflightRebindPathBatches(batchCount, load); err != nil {
		return RebindPathsResult{}, err
	}
	return rebindPathBatches(ctx, s.DB, batchCount, load)
}

func (s Service) RegisterEntry(ctx context.Context, input RegisterEntryInput) (Entry, error) {
	normalized, err := normalizeRegisterEntryInput(input)
	if err != nil {
		return Entry{}, err
	}

	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.storage_entries (
			storage_entry_id,
			storage_class,
			source_area,
			origin_node_id,
			origin_node_key,
			project_id,
			watched_root_id,
			watched_root_key,
			dropzone_transfer_id,
			private_backup_operation_id,
			private_backup_item_id,
			object_id,
			object_version_id,
			archive_manifest_id,
			logical_path,
			original_source_path,
			current_view_path,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			mime_type,
			file_class,
			processing_state,
			availability_state,
			retention_state,
			metadata
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
			$21, $22, $23, $24, $25, $26
		)
		ON CONFLICT (storage_entry_id) DO UPDATE
		SET storage_class = EXCLUDED.storage_class,
		    source_area = EXCLUDED.source_area,
		    origin_node_id = EXCLUDED.origin_node_id,
		    origin_node_key = EXCLUDED.origin_node_key,
		    project_id = EXCLUDED.project_id,
		    watched_root_id = EXCLUDED.watched_root_id,
		    watched_root_key = EXCLUDED.watched_root_key,
		    dropzone_transfer_id = EXCLUDED.dropzone_transfer_id,
		    private_backup_operation_id = EXCLUDED.private_backup_operation_id,
		    private_backup_item_id = EXCLUDED.private_backup_item_id,
		    object_id = EXCLUDED.object_id,
		    object_version_id = EXCLUDED.object_version_id,
		    archive_manifest_id = EXCLUDED.archive_manifest_id,
		    logical_path = EXCLUDED.logical_path,
		    original_source_path = EXCLUDED.original_source_path,
		    current_view_path = EXCLUDED.current_view_path,
		    checksum_algorithm = EXCLUDED.checksum_algorithm,
		    checksum_hex = EXCLUDED.checksum_hex,
		    size_bytes = EXCLUDED.size_bytes,
		    mime_type = EXCLUDED.mime_type,
		    file_class = EXCLUDED.file_class,
		    processing_state = EXCLUDED.processing_state,
		    availability_state = EXCLUDED.availability_state,
		    retention_state = EXCLUDED.retention_state,
		    metadata = EXCLUDED.metadata,
		    updated_at = now(),
		    deleted_at = NULL
		RETURNING
			storage_entry_id,
			storage_class,
			source_area,
			origin_node_id,
			origin_node_key,
			project_id,
			watched_root_id,
			watched_root_key,
			dropzone_transfer_id,
			private_backup_operation_id,
			private_backup_item_id,
			object_id,
			object_version_id,
			archive_manifest_id,
			logical_path,
			original_source_path,
			current_view_path,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			mime_type,
			file_class,
			processing_state,
			availability_state,
			retention_state,
			metadata,
			created_at,
			updated_at,
			deleted_at
	`,
		normalized.StorageEntryID,
		normalized.StorageClass,
		normalized.SourceArea,
		nullableStringArg(normalized.OriginNodeID),
		normalized.OriginNodeKey,
		nullableStringArg(normalized.ProjectID),
		nullableStringArg(normalized.WatchedRootID),
		normalized.WatchedRootKey,
		normalized.DropzoneTransferID,
		nullableStringArg(normalized.PrivateBackupOperationID),
		nullableStringArg(normalized.PrivateBackupItemID),
		nullableStringArg(normalized.ObjectID),
		nullableStringArg(normalized.ObjectVersionID),
		nullableStringArg(normalized.ArchiveManifestID),
		normalized.LogicalPath,
		normalized.OriginalSourcePath,
		normalized.CurrentViewPath,
		normalized.ChecksumAlgorithm,
		normalized.ChecksumHex,
		nullableInt64Arg(normalized.SizeBytes),
		normalized.MimeType,
		normalized.FileClass,
		normalized.ProcessingState,
		normalized.AvailabilityState,
		normalized.RetentionState,
		normalized.Metadata,
	)
	return scanEntry(row)
}

func (s Service) RegisterPhysicalRef(ctx context.Context, input RegisterPhysicalRefInput) (PhysicalRef, error) {
	normalized, err := normalizeRegisterPhysicalRefInput(input)
	if err != nil {
		return PhysicalRef{}, err
	}

	row := s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.storage_physical_refs (
			storage_physical_ref_id,
			storage_entry_id,
			storage_entry_version_id,
			ref_kind,
			uri,
			node_id,
			node_key,
			content_address,
			status,
			metadata
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (storage_physical_ref_id) DO UPDATE
		SET storage_entry_id = EXCLUDED.storage_entry_id,
		    storage_entry_version_id = EXCLUDED.storage_entry_version_id,
		    ref_kind = EXCLUDED.ref_kind,
		    uri = EXCLUDED.uri,
		    node_id = EXCLUDED.node_id,
		    node_key = EXCLUDED.node_key,
		    content_address = EXCLUDED.content_address,
		    status = EXCLUDED.status,
		    metadata = EXCLUDED.metadata,
		    updated_at = now()
		RETURNING
			storage_physical_ref_id,
			storage_entry_id,
			storage_entry_version_id,
			ref_kind,
			uri,
			node_id,
			node_key,
			content_address,
			status,
			metadata,
			created_at,
			updated_at
	`,
		normalized.StoragePhysicalRefID,
		normalized.StorageEntryID,
		nullableStringArg(normalized.StorageEntryVersionID),
		normalized.RefKind,
		normalized.URI,
		nullableStringArg(normalized.NodeID),
		normalized.NodeKey,
		normalized.ContentAddress,
		normalized.Status,
		normalized.Metadata,
	)
	return scanPhysicalRef(row)
}

// FindAvailablePhysicalRefsByContentAddresses returns the newest available
// physical reference for each exact kind/content pair. The lookup is chunked
// and bounded so archive deduplication does not issue one full catalog scan per
// file on production-sized archive trees.
func (s Service) FindAvailablePhysicalRefsByContentAddresses(ctx context.Context, refKind string, contentAddresses []string) (map[string]PhysicalRef, error) {
	refKind = strings.TrimSpace(refKind)
	if err := requireValid("ref_kind", refKind, ValidPhysicalRefKind); err != nil {
		return nil, err
	}
	unique := make(map[string]struct{}, len(contentAddresses))
	for _, contentAddress := range contentAddresses {
		contentAddress = strings.TrimSpace(contentAddress)
		if contentAddress == "" {
			return nil, fmt.Errorf("%w: content_address is required", ErrInvalid)
		}
		unique[contentAddress] = struct{}{}
	}
	addresses := make([]string, 0, len(unique))
	for contentAddress := range unique {
		addresses = append(addresses, contentAddress)
	}
	sort.Strings(addresses)
	result := make(map[string]PhysicalRef, len(addresses))
	for start := 0; start < len(addresses); start += bulkLookupChunkSize {
		end := start + bulkLookupChunkSize
		if end > len(addresses) {
			end = len(addresses)
		}
		args := []any{refKind, PhysicalRefStatusAvailable}
		for _, contentAddress := range addresses[start:end] {
			args = append(args, contentAddress)
		}
		rows, err := s.DB.QueryContext(ctx, physicalRefSelectSQL()+`
			WHERE ref_kind = $1
			  AND status = $2
			  AND content_address IN (`+sqlPlaceholders(3, end-start)+`)
			ORDER BY content_address, updated_at DESC, storage_physical_ref_id
		`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			ref, err := scanPhysicalRef(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, exists := result[ref.ContentAddress]; !exists {
				result[ref.ContentAddress] = ref
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s Service) ListEntries(ctx context.Context, filter ListFilter) ([]Entry, error) {
	if filter.Limit <= 0 {
		filter.Limit = defaultListEntriesLimit
	} else if filter.Limit > maxListEntriesLimit {
		filter.Limit = maxListEntriesLimit
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalid)
	}
	query := entrySelectSQL() + ` WHERE 1 = 1`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if value := strings.TrimSpace(filter.StorageClass); value != "" {
		if err := requireValid("storage_class", value, ValidStorageClass); err != nil {
			return nil, err
		}
		add("storage_class =", value)
	}
	if value := strings.TrimSpace(filter.SourceArea); value != "" {
		if err := requireValid("source_area", value, ValidSourceArea); err != nil {
			return nil, err
		}
		add("source_area =", value)
	}
	if value := strings.TrimSpace(filter.OriginNodeKey); value != "" {
		add("origin_node_key =", value)
	}
	pathPrefixes := make([]string, 0, len(filter.PathPrefixes))
	seenPathPrefixes := map[string]struct{}{}
	for _, raw := range filter.PathPrefixes {
		value, err := NormalizeLogicalPath(raw)
		if err != nil {
			return nil, fmt.Errorf("path prefix: %w", err)
		}
		if _, duplicate := seenPathPrefixes[value]; duplicate {
			continue
		}
		seenPathPrefixes[value] = struct{}{}
		pathPrefixes = append(pathPrefixes, value)
	}
	if len(pathPrefixes) > 0 {
		clauses := make([]string, 0, len(pathPrefixes))
		for _, value := range pathPrefixes {
			exactIndex := len(args) + 1
			args = append(args, value)
			likeIndex := len(args) + 1
			args = append(args, escapeSQLLike(value)+"/%")
			clauses = append(clauses, fmt.Sprintf(`(
				current_view_path = $%d OR current_view_path LIKE $%d ESCAPE E'\\'
				OR logical_path = $%d OR logical_path LIKE $%d ESCAPE E'\\'
				OR original_source_path = $%d OR original_source_path LIKE $%d ESCAPE E'\\'
			)`, exactIndex, likeIndex, exactIndex, likeIndex, exactIndex, likeIndex))
		}
		query += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	if value := strings.TrimSpace(filter.FileClass); value != "" {
		if err := requireValid("file_class", value, ValidFileClass); err != nil {
			return nil, err
		}
		add("file_class =", value)
	}
	if value := strings.TrimSpace(filter.ProcessingState); value != "" {
		if err := requireValid("processing_state", value, ValidProcessingState); err != nil {
			return nil, err
		}
		add("processing_state =", value)
	}
	if value := strings.TrimSpace(filter.AvailabilityState); value != "" {
		if err := requireValid("availability_state", value, ValidAvailabilityState); err != nil {
			return nil, err
		}
		add("availability_state =", value)
	}
	if !filter.IncludeDeleted {
		query += " AND deleted_at IS NULL"
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY updated_at DESC, logical_path, storage_entry_id LIMIT $%d", len(args))
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func escapeSQLLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func (s Service) ListAllEntries(ctx context.Context, filter ListFilter) ([]Entry, error) {
	filter.Limit = maxListEntriesLimit
	filter.Offset = 0
	var entries []Entry
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := s.ListEntries(ctx, filter)
		if err != nil {
			return nil, err
		}
		entries = append(entries, page...)
		if len(page) < maxListEntriesLimit {
			break
		}
		filter.Offset += len(page)
	}
	return entries, nil
}

func (s Service) InspectEntry(ctx context.Context, ref string) (EntryDetail, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return EntryDetail{}, fmt.Errorf("%w: storage entry ref is required", ErrInvalid)
	}

	entry, err := scanEntry(s.DB.QueryRowContext(ctx, entrySelectSQL()+`
		WHERE storage_entry_id = $1
		   OR logical_path = $1
		   OR current_view_path = $1
		   OR original_source_path = $1
		ORDER BY
			CASE
				WHEN storage_entry_id = $1 THEN 0
				WHEN current_view_path = $1 THEN 1
				WHEN logical_path = $1 THEN 2
				ELSE 3
			END,
			updated_at DESC
		LIMIT 1
	`, ref))
	if err != nil {
		return EntryDetail{}, err
	}

	refs, err := s.listPhysicalRefs(ctx, entry.StorageEntryID)
	if err != nil {
		return EntryDetail{}, err
	}
	return EntryDetail{Entry: entry, PhysicalRefs: refs}, nil
}

func (s Service) ListPhysicalRefsForEntries(ctx context.Context, storageEntryIDs []string) (map[string][]PhysicalRef, error) {
	normalized, err := normalizeStorageEntryIDs(storageEntryIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]PhysicalRef, len(normalized))
	for _, id := range normalized {
		result[id] = nil
	}
	if len(normalized) == 0 {
		return result, nil
	}
	for start := 0; start < len(normalized); start += bulkLookupChunkSize {
		end := start + bulkLookupChunkSize
		if end > len(normalized) {
			end = len(normalized)
		}
		args := storageEntryIDArgs(normalized[start:end])
		rows, err := s.DB.QueryContext(ctx, physicalRefSelectSQL()+`
			WHERE storage_entry_id IN (`+sqlPlaceholders(1, len(args))+`)
			ORDER BY storage_entry_id, created_at DESC, storage_physical_ref_id
		`, args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			ref, err := scanPhysicalRef(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[ref.StorageEntryID] = append(result[ref.StorageEntryID], ref)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s Service) ListEntryDetails(ctx context.Context, storageEntryIDs []string) (map[string]EntryDetail, error) {
	normalized, err := normalizeStorageEntryIDs(storageEntryIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string]EntryDetail, len(normalized))
	if len(normalized) == 0 {
		return result, nil
	}
	for start := 0; start < len(normalized); start += bulkLookupChunkSize {
		end := start + bulkLookupChunkSize
		if end > len(normalized) {
			end = len(normalized)
		}
		args := storageEntryIDArgs(normalized[start:end])
		rows, err := s.DB.QueryContext(ctx, entrySelectSQL()+`
			WHERE storage_entry_id IN (`+sqlPlaceholders(1, len(args))+`)
			ORDER BY updated_at DESC, logical_path, storage_entry_id
		`, args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			entry, err := scanEntry(rows)
			if err != nil {
				_ = rows.Close()
				return nil, err
			}
			result[entry.StorageEntryID] = EntryDetail{Entry: entry}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	refs, err := s.ListPhysicalRefsForEntries(ctx, normalized)
	if err != nil {
		return nil, err
	}
	for id, detail := range result {
		detail.PhysicalRefs = refs[id]
		result[id] = detail
	}
	return result, nil
}

func (s Service) InspectByViewPath(ctx context.Context, viewPath string, opts InspectOptions) (EntryDetail, error) {
	viewPath = strings.Trim(strings.TrimSpace(strings.ReplaceAll(viewPath, "\\", "/")), "/")
	var err error
	viewPath, err = NormalizeLogicalPath(viewPath)
	if err != nil {
		return EntryDetail{}, fmt.Errorf("storage view path: %w", err)
	}
	query := entrySelectSQL() + `
		WHERE (
		      current_view_path = $1
		   OR logical_path = $1
		   OR original_source_path = $1
		)
	`
	if !opts.IncludeDeleted {
		query += `
		   AND deleted_at IS NULL
		   AND availability_state NOT IN ('deleted', 'tombstoned')
		`
	}
	query += `
		ORDER BY
			CASE
				WHEN deleted_at IS NULL
				 AND availability_state NOT IN ('deleted', 'tombstoned') THEN 0
				ELSE 1
			END,
			CASE
				WHEN current_view_path = $1 THEN 0
				WHEN logical_path = $1 THEN 1
				ELSE 2
			END,
			updated_at DESC
		LIMIT 1
	`
	entry, err := scanEntry(s.DB.QueryRowContext(ctx, query, viewPath))
	if err != nil {
		return EntryDetail{}, err
	}
	refs, err := s.listPhysicalRefs(ctx, entry.StorageEntryID)
	if err != nil {
		return EntryDetail{}, err
	}
	return EntryDetail{Entry: entry, PhysicalRefs: refs}, nil
}

func (s Service) InspectMainDocumentByPath(ctx context.Context, relativePath string, opts InspectOptions) (EntryDetail, error) {
	relativePath = strings.Trim(strings.TrimSpace(relativePath), "/")
	if strings.HasPrefix(relativePath, "main/Documents/") {
		relativePath = strings.TrimPrefix(relativePath, "main/Documents/")
	}
	if relativePath == "" {
		return EntryDetail{}, fmt.Errorf("%w: main document path is required", ErrInvalid)
	}
	return s.InspectByViewPath(ctx, MainDocumentViewPath(relativePath), InspectOptions{IncludeDeleted: true})
}

func (s Service) CreateRetentionSnapshot(ctx context.Context, input RetentionSnapshotInput) (RetentionSnapshotResult, error) {
	normalized, err := normalizeRetentionSnapshotInput(input)
	if err != nil {
		return RetentionSnapshotResult{}, err
	}
	detail, err := s.InspectEntry(ctx, normalized.StorageEntryID)
	if err != nil {
		return RetentionSnapshotResult{}, err
	}
	refID := normalized.StoragePhysicalRefID
	if refID == "" {
		refID = latestAvailablePhysicalRefID(detail.PhysicalRefs)
	}
	var nextVersion int
	if err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(version_number), 0) + 1
		FROM storage.storage_entry_versions
		WHERE storage_entry_id = $1
	`, normalized.StorageEntryID).Scan(&nextVersion); err != nil {
		return RetentionSnapshotResult{}, err
	}
	version, err := scanEntryVersion(s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.storage_entry_versions (
			storage_entry_version_id,
			storage_entry_id,
			version_number,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			physical_ref_id,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,nullif($7,''),$8)
		RETURNING
			storage_entry_version_id,
			storage_entry_id,
			version_number,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			physical_ref_id,
			metadata,
			created_at
	`, normalized.StorageEntryVersionID,
		normalized.StorageEntryID,
		nextVersion,
		detail.Entry.ChecksumAlgorithm,
		detail.Entry.ChecksumHex,
		nullableInt64Arg(detail.Entry.SizeBytes),
		refID,
		normalized.Metadata,
	))
	if err != nil {
		return RetentionSnapshotResult{}, err
	}
	retention, err := scanRetentionEntry(s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.retention_entries (
			storage_retention_entry_id,
			storage_entry_id,
			policy_key,
			retention_state,
			retained_until,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING
			storage_retention_entry_id,
			storage_entry_id,
			policy_key,
			retention_state,
			retained_until,
			metadata,
			created_at,
			updated_at
	`, normalized.StorageRetentionEntryID,
		normalized.StorageEntryID,
		normalized.PolicyKey,
		normalized.RetentionState,
		nullableTimeArg(normalized.RetainedUntil),
		normalized.Metadata,
	))
	if err != nil {
		return RetentionSnapshotResult{}, err
	}
	entry, err := scanEntry(s.DB.QueryRowContext(ctx, `
		UPDATE storage.storage_entries
		SET retention_state = $2,
		    updated_at = now()
		WHERE storage_entry_id = $1
		RETURNING
			storage_entry_id,
			storage_class,
			source_area,
			origin_node_id,
			origin_node_key,
			project_id,
			watched_root_id,
			watched_root_key,
			dropzone_transfer_id,
			private_backup_operation_id,
			private_backup_item_id,
			object_id,
			object_version_id,
			archive_manifest_id,
			logical_path,
			original_source_path,
			current_view_path,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			mime_type,
			file_class,
			processing_state,
			availability_state,
			retention_state,
			metadata,
			created_at,
			updated_at,
			deleted_at
	`, normalized.StorageEntryID, normalized.RetentionState))
	if err != nil {
		return RetentionSnapshotResult{}, err
	}
	return RetentionSnapshotResult{Entry: entry, Version: version, Retention: retention}, nil
}

func (s Service) CreateTombstone(ctx context.Context, input TombstoneInput) (Tombstone, error) {
	normalized, err := normalizeTombstoneInput(input)
	if err != nil {
		return Tombstone{}, err
	}
	tombstone, err := scanTombstone(s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.tombstones (
			storage_tombstone_id,
			storage_entry_id,
			tombstone_kind,
			reason,
			created_by,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING
			storage_tombstone_id,
			storage_entry_id,
			tombstone_kind,
			reason,
			created_by,
			metadata,
			created_at
	`, normalized.StorageTombstoneID,
		normalized.StorageEntryID,
		normalized.TombstoneKind,
		normalized.Reason,
		normalized.CreatedBy,
		normalized.Metadata,
	))
	if err != nil {
		return Tombstone{}, err
	}
	if normalized.MarkEntry {
		if _, err := scanEntry(s.DB.QueryRowContext(ctx, `
			UPDATE storage.storage_entries
			SET availability_state = $2,
			    updated_at = now()
			WHERE storage_entry_id = $1
			RETURNING
				storage_entry_id,
				storage_class,
				source_area,
				origin_node_id,
				origin_node_key,
				project_id,
				watched_root_id,
				watched_root_key,
				dropzone_transfer_id,
				private_backup_operation_id,
				private_backup_item_id,
				object_id,
				object_version_id,
				archive_manifest_id,
				logical_path,
				original_source_path,
				current_view_path,
				checksum_algorithm,
				checksum_hex,
				size_bytes,
				mime_type,
				file_class,
				processing_state,
				availability_state,
				retention_state,
				metadata,
				created_at,
				updated_at,
				deleted_at
		`, normalized.StorageEntryID, AvailabilityStateTombstoned)); err != nil {
			return Tombstone{}, err
		}
	}
	return tombstone, nil
}

func (s Service) RetentionStatus(ctx context.Context) (RetentionStatus, error) {
	entries, err := s.ListAllEntries(ctx, ListFilter{IncludeDeleted: true})
	if err != nil {
		return RetentionStatus{}, err
	}
	status := RetentionStatus{Entries: len(entries), GeneratedAt: time.Now().UTC()}
	for _, entry := range entries {
		switch entry.RetentionState {
		case RetentionStateRetained:
			status.Retained++
		case RetentionStateSnapshot:
			status.Snapshots++
		case RetentionStatePending:
			status.Pending++
		case RetentionStateExpired:
			status.Expired++
		}
		switch entry.AvailabilityState {
		case AvailabilityStateTombstoned, AvailabilityStateDeleted:
			status.Tombstoned++
		case AvailabilityStateFailed:
			status.Failed++
		}
		if entry.AvailabilityState == AvailabilityStateAvailable &&
			entry.ProcessingState != ProcessingStateFailed &&
			entry.ProcessingState != ProcessingStateExcluded {
			status.SafeCandidates++
		} else {
			status.UnsafeCandidates++
		}
	}
	return status, nil
}

func (s Service) CreateArchiveManifest(ctx context.Context, input CreateArchiveManifestInput) (ArchiveManifest, error) {
	normalized, err := normalizeCreateArchiveManifestInput(input)
	if err != nil {
		return ArchiveManifest{}, err
	}
	return scanArchiveManifest(s.DB.QueryRowContext(ctx, `
		INSERT INTO storage.archive_manifests (
			archive_manifest_id,
			archive_key,
			archive_kind,
			owner_node_id,
			owner_node_key,
			source_ref,
			status,
			manifest_json,
			finalized_at
		)
		VALUES ($1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9)
		RETURNING
			archive_manifest_id,
			archive_key,
			archive_kind,
			owner_node_id,
			owner_node_key,
			source_ref,
			status,
			manifest_json,
			created_at,
			finalized_at
	`, normalized.ArchiveManifestID,
		normalized.ArchiveKey,
		normalized.ArchiveKind,
		normalized.OwnerNodeID,
		normalized.OwnerNodeKey,
		normalized.SourceRef,
		normalized.Status,
		normalized.ManifestJSON,
		nullableTimeArg(normalized.FinalizedAt),
	))
}

// CommitArchive publishes one complete archive catalog generation. Every
// insert and optional source disposition is evidence-checked and committed by
// one PostgreSQL transaction, so callers never expose a partial archive.
func (s Service) CommitArchive(ctx context.Context, input ArchiveCommitInput) (ArchiveCommitResult, error) {
	normalized, err := normalizeArchiveCommitInput(input)
	if err != nil {
		return ArchiveCommitResult{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ArchiveCommitResult{}, err
	}
	defer tx.Rollback()

	manifest := normalized.Manifest
	if err := execExactArchiveMutation(ctx, tx, "archive manifest", `
		INSERT INTO storage.archive_manifests (
			archive_manifest_id, archive_key, archive_kind, owner_node_id,
			owner_node_key, source_ref, status, manifest_json, finalized_at
		) VALUES ($1,$2,$3,nullif($4,''),$5,$6,$7,$8,$9)
		ON CONFLICT (archive_manifest_id) DO UPDATE
		SET archive_manifest_id = EXCLUDED.archive_manifest_id
		WHERE storage.archive_manifests.archive_key IS NOT DISTINCT FROM EXCLUDED.archive_key
		  AND storage.archive_manifests.archive_kind IS NOT DISTINCT FROM EXCLUDED.archive_kind
		  AND storage.archive_manifests.owner_node_id IS NOT DISTINCT FROM EXCLUDED.owner_node_id
		  AND storage.archive_manifests.owner_node_key IS NOT DISTINCT FROM EXCLUDED.owner_node_key
		  AND storage.archive_manifests.source_ref IS NOT DISTINCT FROM EXCLUDED.source_ref
		  AND storage.archive_manifests.status IS NOT DISTINCT FROM EXCLUDED.status
		  AND storage.archive_manifests.manifest_json IS NOT DISTINCT FROM EXCLUDED.manifest_json
		  AND storage.archive_manifests.finalized_at IS NOT DISTINCT FROM EXCLUDED.finalized_at
	`, manifest.ArchiveManifestID, manifest.ArchiveKey, manifest.ArchiveKind,
		manifest.OwnerNodeID, manifest.OwnerNodeKey, manifest.SourceRef,
		manifest.Status, manifest.ManifestJSON, nullableTimeArg(manifest.FinalizedAt)); err != nil {
		return ArchiveCommitResult{}, err
	}

	result := ArchiveCommitResult{
		Manifest: archiveManifestFromCommit(manifest),
		Entries:  make([]Entry, 0, len(normalized.Entries)),
		Refs:     make([]PhysicalRef, 0, len(normalized.Entries)),
	}
	for _, tuple := range normalized.Entries {
		entry := tuple.Entry
		if err := execExactArchiveMutation(ctx, tx, "archive entry", `
			INSERT INTO storage.storage_entries (
				storage_entry_id, storage_class, source_area, origin_node_id,
				origin_node_key, project_id, watched_root_id, watched_root_key,
				dropzone_transfer_id, private_backup_operation_id, private_backup_item_id,
				object_id, object_version_id, archive_manifest_id, logical_path,
				original_source_path, current_view_path, checksum_algorithm, checksum_hex,
				size_bytes, mime_type, file_class, processing_state, availability_state,
				retention_state, metadata
			) VALUES (
				$1,$2,$3,nullif($4,''),$5,nullif($6,''),nullif($7,''),$8,$9,
				nullif($10,''),nullif($11,''),nullif($12,''),nullif($13,''),nullif($14,''),
				$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26
			)
			ON CONFLICT (storage_entry_id) DO UPDATE
			SET storage_entry_id = EXCLUDED.storage_entry_id
			WHERE storage.storage_entries.storage_class IS NOT DISTINCT FROM EXCLUDED.storage_class
			  AND storage.storage_entries.source_area IS NOT DISTINCT FROM EXCLUDED.source_area
			  AND storage.storage_entries.origin_node_id IS NOT DISTINCT FROM EXCLUDED.origin_node_id
			  AND storage.storage_entries.origin_node_key IS NOT DISTINCT FROM EXCLUDED.origin_node_key
			  AND storage.storage_entries.project_id IS NOT DISTINCT FROM EXCLUDED.project_id
			  AND storage.storage_entries.watched_root_id IS NOT DISTINCT FROM EXCLUDED.watched_root_id
			  AND storage.storage_entries.watched_root_key IS NOT DISTINCT FROM EXCLUDED.watched_root_key
			  AND storage.storage_entries.dropzone_transfer_id IS NOT DISTINCT FROM EXCLUDED.dropzone_transfer_id
			  AND storage.storage_entries.private_backup_operation_id IS NOT DISTINCT FROM EXCLUDED.private_backup_operation_id
			  AND storage.storage_entries.private_backup_item_id IS NOT DISTINCT FROM EXCLUDED.private_backup_item_id
			  AND storage.storage_entries.object_id IS NOT DISTINCT FROM EXCLUDED.object_id
			  AND storage.storage_entries.object_version_id IS NOT DISTINCT FROM EXCLUDED.object_version_id
			  AND storage.storage_entries.archive_manifest_id IS NOT DISTINCT FROM EXCLUDED.archive_manifest_id
			  AND storage.storage_entries.logical_path IS NOT DISTINCT FROM EXCLUDED.logical_path
			  AND storage.storage_entries.original_source_path IS NOT DISTINCT FROM EXCLUDED.original_source_path
			  AND storage.storage_entries.current_view_path IS NOT DISTINCT FROM EXCLUDED.current_view_path
			  AND storage.storage_entries.checksum_algorithm IS NOT DISTINCT FROM EXCLUDED.checksum_algorithm
			  AND storage.storage_entries.checksum_hex IS NOT DISTINCT FROM EXCLUDED.checksum_hex
			  AND storage.storage_entries.size_bytes IS NOT DISTINCT FROM EXCLUDED.size_bytes
			  AND storage.storage_entries.mime_type IS NOT DISTINCT FROM EXCLUDED.mime_type
			  AND storage.storage_entries.file_class IS NOT DISTINCT FROM EXCLUDED.file_class
			  AND storage.storage_entries.processing_state IS NOT DISTINCT FROM EXCLUDED.processing_state
			  AND storage.storage_entries.availability_state IS NOT DISTINCT FROM EXCLUDED.availability_state
			  AND storage.storage_entries.retention_state IS NOT DISTINCT FROM EXCLUDED.retention_state
			  AND storage.storage_entries.metadata IS NOT DISTINCT FROM EXCLUDED.metadata
			  AND storage.storage_entries.deleted_at IS NULL
		`, entry.StorageEntryID, entry.StorageClass, entry.SourceArea, entry.OriginNodeID,
			entry.OriginNodeKey, entry.ProjectID, entry.WatchedRootID, entry.WatchedRootKey,
			entry.DropzoneTransferID, entry.PrivateBackupOperationID, entry.PrivateBackupItemID,
			entry.ObjectID, entry.ObjectVersionID, entry.ArchiveManifestID, entry.LogicalPath,
			entry.OriginalSourcePath, entry.CurrentViewPath, entry.ChecksumAlgorithm, entry.ChecksumHex,
			nullableInt64Arg(entry.SizeBytes), entry.MimeType, entry.FileClass, entry.ProcessingState,
			entry.AvailabilityState, entry.RetentionState, entry.Metadata); err != nil {
			return ArchiveCommitResult{}, err
		}

		ref := tuple.Ref
		if err := execExactArchiveMutation(ctx, tx, "archive physical ref", `
			INSERT INTO storage.storage_physical_refs (
				storage_physical_ref_id, storage_entry_id, storage_entry_version_id,
				ref_kind, uri, node_id, node_key, content_address, status, metadata
			) VALUES ($1,$2,nullif($3,''),$4,$5,nullif($6,''),$7,$8,$9,$10)
			ON CONFLICT (storage_physical_ref_id) DO UPDATE
			SET storage_physical_ref_id = EXCLUDED.storage_physical_ref_id
			WHERE storage.storage_physical_refs.storage_entry_id IS NOT DISTINCT FROM EXCLUDED.storage_entry_id
			  AND storage.storage_physical_refs.storage_entry_version_id IS NOT DISTINCT FROM EXCLUDED.storage_entry_version_id
			  AND storage.storage_physical_refs.ref_kind IS NOT DISTINCT FROM EXCLUDED.ref_kind
			  AND storage.storage_physical_refs.uri IS NOT DISTINCT FROM EXCLUDED.uri
			  AND storage.storage_physical_refs.node_id IS NOT DISTINCT FROM EXCLUDED.node_id
			  AND storage.storage_physical_refs.node_key IS NOT DISTINCT FROM EXCLUDED.node_key
			  AND storage.storage_physical_refs.content_address IS NOT DISTINCT FROM EXCLUDED.content_address
			  AND storage.storage_physical_refs.status IS NOT DISTINCT FROM EXCLUDED.status
			  AND storage.storage_physical_refs.metadata IS NOT DISTINCT FROM EXCLUDED.metadata
		`, ref.StoragePhysicalRefID, ref.StorageEntryID, ref.StorageEntryVersionID,
			ref.RefKind, ref.URI, ref.NodeID, ref.NodeKey, ref.ContentAddress, ref.Status, ref.Metadata); err != nil {
			return ArchiveCommitResult{}, err
		}

		item := tuple.Item
		if err := execExactArchiveMutation(ctx, tx, "archive item", `
			INSERT INTO storage.archive_items (
				archive_manifest_id, storage_entry_id, archive_path, metadata
			) VALUES ($1,$2,$3,$4)
			ON CONFLICT (archive_manifest_id, storage_entry_id) DO UPDATE
			SET archive_manifest_id = EXCLUDED.archive_manifest_id
			WHERE storage.archive_items.archive_path IS NOT DISTINCT FROM EXCLUDED.archive_path
			  AND storage.archive_items.metadata IS NOT DISTINCT FROM EXCLUDED.metadata
		`, item.ArchiveManifestID, item.StorageEntryID, item.ArchivePath, item.Metadata); err != nil {
			return ArchiveCommitResult{}, err
		}
		result.Entries = append(result.Entries, entryFromCommit(entry))
		result.Refs = append(result.Refs, physicalRefFromCommit(ref))
	}

	for _, disposition := range normalized.SourceDispositions {
		metadataKey := "archived_by_manifest_id"
		if disposition.AvailabilityState == AvailabilityStateSuperseded {
			metadataKey = "superseded_by_manifest_id"
		}
		if err := execExactArchiveMutation(ctx, tx, "archive source disposition", `
			UPDATE storage.storage_entries
			SET availability_state = $3,
			    metadata = metadata || jsonb_build_object($4, $5),
			    updated_at = now()
			WHERE storage_entry_id = $1
			  AND (
				availability_state = $2
				OR (availability_state = $3 AND metadata ->> $4 = $5)
			  )
		`, disposition.StorageEntryID, disposition.ExpectedAvailability,
			disposition.AvailabilityState, metadataKey, manifest.ArchiveManifestID); err != nil {
			return ArchiveCommitResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ArchiveCommitResult{}, err
	}
	return result, nil
}

func execExactArchiveMutation(ctx context.Context, tx *sql.Tx, label, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: inspect affected rows: %w", label, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %s conflicts with existing catalog evidence", ErrInvalid, label)
	}
	return nil
}

func (s Service) AddArchiveItem(ctx context.Context, input ArchiveItemInput) error {
	normalized, err := normalizeArchiveItemInput(input)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO storage.archive_items (
			archive_manifest_id,
			storage_entry_id,
			archive_path,
			metadata
		)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (archive_manifest_id, storage_entry_id) DO UPDATE
		SET archive_path = EXCLUDED.archive_path,
		    metadata = EXCLUDED.metadata
	`, normalized.ArchiveManifestID,
		normalized.StorageEntryID,
		normalized.ArchivePath,
		normalized.Metadata,
	)
	return err
}

func (s Service) MarkEntriesArchived(ctx context.Context, storageEntryIDs []string, archiveManifestID string) error {
	return s.markEntriesArchiveDisposition(ctx, storageEntryIDs, archiveManifestID, AvailabilityStateArchived, "archived_by_manifest_id")
}

func (s Service) MarkEntriesSuperseded(ctx context.Context, storageEntryIDs []string, archiveManifestID string) error {
	return s.markEntriesArchiveDisposition(ctx, storageEntryIDs, archiveManifestID, AvailabilityStateSuperseded, "superseded_by_manifest_id")
}

func (s Service) markEntriesArchiveDisposition(ctx context.Context, storageEntryIDs []string, archiveManifestID, availabilityState, metadataKey string) error {
	archiveManifestID = strings.TrimSpace(archiveManifestID)
	if err := ids.Validate(ids.StorageArchiveManifestPrefix, archiveManifestID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	for _, storageEntryID := range storageEntryIDs {
		storageEntryID = strings.TrimSpace(storageEntryID)
		if storageEntryID == "" {
			continue
		}
		if err := ids.Validate(ids.StorageEntryPrefix, storageEntryID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if _, err := s.DB.ExecContext(ctx, `
			UPDATE storage.storage_entries
			SET availability_state = $2,
			    metadata = metadata || jsonb_build_object($3, $4),
			    updated_at = now()
			WHERE storage_entry_id = $1
		`, storageEntryID, availabilityState, metadataKey, archiveManifestID); err != nil {
			return err
		}
	}
	return nil
}

func (s Service) listPhysicalRefs(ctx context.Context, storageEntryID string) ([]PhysicalRef, error) {
	rows, err := s.DB.QueryContext(ctx, physicalRefSelectSQL()+`
		WHERE storage_entry_id = $1
		ORDER BY created_at DESC, storage_physical_ref_id
	`, storageEntryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []PhysicalRef
	for rows.Next() {
		ref, err := scanPhysicalRef(rows)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func latestAvailablePhysicalRefID(refs []PhysicalRef) string {
	for _, ref := range refs {
		if ref.Status == "" || ref.Status == PhysicalRefStatusAvailable {
			return ref.StoragePhysicalRefID
		}
	}
	return ""
}

func latestAvailablePhysicalRefIDByKind(refs []PhysicalRef, kind string) string {
	for _, ref := range refs {
		if ref.RefKind != kind {
			continue
		}
		if ref.Status == "" || ref.Status == PhysicalRefStatusAvailable {
			return ref.StoragePhysicalRefID
		}
	}
	return ""
}

func normalizeStorageEntryIDs(values []string) ([]string, error) {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		if err := ids.Validate(ids.StorageEntryPrefix, value); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		seen[value] = true
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func storageEntryIDArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}

func sqlPlaceholders(start, count int) string {
	parts := make([]string, count)
	for i := 0; i < count; i++ {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ", ")
}

func normalizeRegisterEntryInput(input RegisterEntryInput) (RegisterEntryInput, error) {
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if input.StorageEntryID == "" {
		input.StorageEntryID = ids.NewStorageEntryID()
	}
	if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
		return RegisterEntryInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	input.StorageClass = strings.TrimSpace(input.StorageClass)
	if err := requireValid("storage_class", input.StorageClass, ValidStorageClass); err != nil {
		return RegisterEntryInput{}, err
	}

	input.SourceArea = strings.TrimSpace(input.SourceArea)
	if input.SourceArea == "" {
		input.SourceArea = SourceAreaUnknown
	}
	if err := requireValid("source_area", input.SourceArea, ValidSourceArea); err != nil {
		return RegisterEntryInput{}, err
	}
	if input.StorageClass == StorageClassDropzoneCustody || input.SourceArea == SourceAreaDropzone || strings.TrimSpace(input.DropzoneTransferID) != "" {
		return RegisterEntryInput{}, fmt.Errorf("%w; storage catalog Dropzone values are decode-only", ErrDropzoneRetired)
	}

	logicalPath, err := NormalizeLogicalPath(input.LogicalPath)
	if err != nil {
		return RegisterEntryInput{}, err
	}
	input.LogicalPath = logicalPath
	if input.CurrentViewPath, err = NormalizeOptionalViewPath(input.CurrentViewPath); err != nil {
		return RegisterEntryInput{}, err
	}
	input.OriginalSourcePath = NormalizeSourcePath(input.OriginalSourcePath)

	input.OriginNodeID = strings.TrimSpace(input.OriginNodeID)
	input.OriginNodeKey = strings.TrimSpace(input.OriginNodeKey)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.WatchedRootID = strings.TrimSpace(input.WatchedRootID)
	input.WatchedRootKey = strings.TrimSpace(input.WatchedRootKey)
	input.DropzoneTransferID = strings.TrimSpace(input.DropzoneTransferID)
	input.PrivateBackupOperationID = strings.TrimSpace(input.PrivateBackupOperationID)
	input.PrivateBackupItemID = strings.TrimSpace(input.PrivateBackupItemID)
	input.ObjectID = strings.TrimSpace(input.ObjectID)
	input.ObjectVersionID = strings.TrimSpace(input.ObjectVersionID)
	input.ArchiveManifestID = strings.TrimSpace(input.ArchiveManifestID)
	input.ChecksumAlgorithm = strings.TrimSpace(input.ChecksumAlgorithm)
	input.ChecksumHex = strings.TrimSpace(input.ChecksumHex)
	input.MimeType = strings.TrimSpace(input.MimeType)

	if input.SizeBytes != nil && *input.SizeBytes < 0 {
		return RegisterEntryInput{}, fmt.Errorf("%w: size_bytes cannot be negative", ErrInvalid)
	}
	if input.ChecksumHex != "" && input.ChecksumAlgorithm == "" {
		return RegisterEntryInput{}, fmt.Errorf("%w: checksum_algorithm is required when checksum_hex is set", ErrInvalid)
	}

	input.FileClass = strings.TrimSpace(input.FileClass)
	if input.FileClass == "" {
		input.FileClass = ClassifyPath(input.LogicalPath, input.MimeType)
	}
	if err := requireValid("file_class", input.FileClass, ValidFileClass); err != nil {
		return RegisterEntryInput{}, err
	}

	input.ProcessingState = strings.TrimSpace(input.ProcessingState)
	if input.ProcessingState == "" {
		input.ProcessingState = ProcessingStateMetadataOnly
	}
	if err := requireValid("processing_state", input.ProcessingState, ValidProcessingState); err != nil {
		return RegisterEntryInput{}, err
	}

	input.AvailabilityState = strings.TrimSpace(input.AvailabilityState)
	if input.AvailabilityState == "" {
		input.AvailabilityState = AvailabilityStateAvailable
	}
	if err := requireValid("availability_state", input.AvailabilityState, ValidAvailabilityState); err != nil {
		return RegisterEntryInput{}, err
	}

	input.RetentionState = strings.TrimSpace(input.RetentionState)
	if input.RetentionState == "" {
		input.RetentionState = RetentionStateNone
	}
	if err := requireValid("retention_state", input.RetentionState, ValidRetentionState); err != nil {
		return RegisterEntryInput{}, err
	}

	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return RegisterEntryInput{}, fmt.Errorf("%w: metadata must be a JSON object: %v", ErrInvalid, err)
	}
	input.Metadata = metadata
	return input, nil
}

func normalizeRegisterPhysicalRefInput(input RegisterPhysicalRefInput) (RegisterPhysicalRefInput, error) {
	input.StoragePhysicalRefID = strings.TrimSpace(input.StoragePhysicalRefID)
	if input.StoragePhysicalRefID == "" {
		input.StoragePhysicalRefID = ids.NewStoragePhysicalRefID()
	}
	if err := ids.Validate(ids.StoragePhysicalRefPrefix, input.StoragePhysicalRefID); err != nil {
		return RegisterPhysicalRefInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
		return RegisterPhysicalRefInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	input.StorageEntryVersionID = strings.TrimSpace(input.StorageEntryVersionID)
	if input.StorageEntryVersionID != "" {
		if err := ids.Validate(ids.StorageEntryVersionPrefix, input.StorageEntryVersionID); err != nil {
			return RegisterPhysicalRefInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}

	input.RefKind = strings.TrimSpace(input.RefKind)
	if err := requireValid("ref_kind", input.RefKind, ValidPhysicalRefKind); err != nil {
		return RegisterPhysicalRefInput{}, err
	}
	if input.RefKind == PhysicalRefKindDropzoneFile {
		return RegisterPhysicalRefInput{}, fmt.Errorf("%w; Dropzone physical refs are decode-only", ErrDropzoneRetired)
	}
	input.URI = strings.TrimSpace(input.URI)
	if input.URI == "" {
		return RegisterPhysicalRefInput{}, fmt.Errorf("%w: uri is required", ErrInvalid)
	}
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	input.ContentAddress = strings.TrimSpace(input.ContentAddress)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = PhysicalRefStatusAvailable
	}
	if err := requireValid("status", input.Status, ValidPhysicalRefStatus); err != nil {
		return RegisterPhysicalRefInput{}, err
	}

	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return RegisterPhysicalRefInput{}, fmt.Errorf("%w: metadata must be a JSON object: %v", ErrInvalid, err)
	}
	input.Metadata = metadata
	return input, nil
}

func normalizeRetentionSnapshotInput(input RetentionSnapshotInput) (RetentionSnapshotInput, error) {
	input.StorageEntryVersionID = strings.TrimSpace(input.StorageEntryVersionID)
	if input.StorageEntryVersionID == "" {
		input.StorageEntryVersionID = ids.NewStorageEntryVersionID()
	}
	if err := ids.Validate(ids.StorageEntryVersionPrefix, input.StorageEntryVersionID); err != nil {
		return RetentionSnapshotInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageRetentionEntryID = strings.TrimSpace(input.StorageRetentionEntryID)
	if input.StorageRetentionEntryID == "" {
		input.StorageRetentionEntryID = ids.NewStorageRetentionEntryID()
	}
	if err := ids.Validate(ids.StorageRetentionEntryPrefix, input.StorageRetentionEntryID); err != nil {
		return RetentionSnapshotInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
		return RetentionSnapshotInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StoragePhysicalRefID = strings.TrimSpace(input.StoragePhysicalRefID)
	if input.StoragePhysicalRefID != "" {
		if err := ids.Validate(ids.StoragePhysicalRefPrefix, input.StoragePhysicalRefID); err != nil {
			return RetentionSnapshotInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.PolicyKey = strings.TrimSpace(input.PolicyKey)
	if input.PolicyKey == "" {
		input.PolicyKey = "default"
	}
	input.RetentionState = strings.TrimSpace(input.RetentionState)
	if input.RetentionState == "" {
		input.RetentionState = RetentionStateSnapshot
	}
	if input.RetentionState == RetentionStateNone {
		return RetentionSnapshotInput{}, fmt.Errorf("%w: retention_state cannot be none for a retention snapshot", ErrInvalid)
	}
	if err := requireValid("retention_state", input.RetentionState, ValidRetentionState); err != nil {
		return RetentionSnapshotInput{}, err
	}
	if input.RetainedUntil != nil {
		retainedUntil := input.RetainedUntil.UTC()
		input.RetainedUntil = &retainedUntil
	}
	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return RetentionSnapshotInput{}, fmt.Errorf("%w: metadata must be a JSON object: %v", ErrInvalid, err)
	}
	input.Metadata = metadata
	return input, nil
}

func normalizeTombstoneInput(input TombstoneInput) (TombstoneInput, error) {
	input.StorageTombstoneID = strings.TrimSpace(input.StorageTombstoneID)
	if input.StorageTombstoneID == "" {
		input.StorageTombstoneID = ids.NewStorageTombstoneID()
	}
	if err := ids.Validate(ids.StorageTombstonePrefix, input.StorageTombstoneID); err != nil {
		return TombstoneInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
		return TombstoneInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.TombstoneKind = strings.TrimSpace(input.TombstoneKind)
	if input.TombstoneKind == "" {
		input.TombstoneKind = TombstoneKindSourceDeleted
	}
	if err := requireValid("tombstone_kind", input.TombstoneKind, ValidTombstoneKind); err != nil {
		return TombstoneInput{}, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return TombstoneInput{}, fmt.Errorf("%w: metadata must be a JSON object: %v", ErrInvalid, err)
	}
	input.Metadata = metadata
	return input, nil
}

func normalizeCreateArchiveManifestInput(input CreateArchiveManifestInput) (CreateArchiveManifestInput, error) {
	input.ArchiveManifestID = strings.TrimSpace(input.ArchiveManifestID)
	if input.ArchiveManifestID == "" {
		input.ArchiveManifestID = ids.NewStorageArchiveManifestID()
	}
	if err := ids.Validate(ids.StorageArchiveManifestPrefix, input.ArchiveManifestID); err != nil {
		return CreateArchiveManifestInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.ArchiveKey = strings.TrimSpace(input.ArchiveKey)
	if input.ArchiveKey == "" {
		input.ArchiveKey = input.ArchiveManifestID
	}
	input.ArchiveKind = strings.TrimSpace(input.ArchiveKind)
	if input.ArchiveKind == "" {
		input.ArchiveKind = "manual_archive"
	}
	switch input.ArchiveKind {
	case "project_archive", "document_archive", "notes_archive", "manual_archive":
	default:
		return CreateArchiveManifestInput{}, fmt.Errorf("%w: unsupported archive_kind %q", ErrInvalid, input.ArchiveKind)
	}
	input.OwnerNodeID = strings.TrimSpace(input.OwnerNodeID)
	if input.OwnerNodeID != "" {
		if err := ids.Validate(ids.NodePrefix, input.OwnerNodeID); err != nil {
			return CreateArchiveManifestInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	input.OwnerNodeKey = strings.TrimSpace(input.OwnerNodeKey)
	if input.OwnerNodeKey == "" {
		input.OwnerNodeKey = "main"
	}
	input.SourceRef = strings.TrimSpace(input.SourceRef)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = "complete"
	}
	switch input.Status {
	case "pending", "building", "complete", "failed", "superseded":
	default:
		return CreateArchiveManifestInput{}, fmt.Errorf("%w: unsupported archive status %q", ErrInvalid, input.Status)
	}
	if input.FinalizedAt != nil {
		finalizedAt := input.FinalizedAt.UTC()
		input.FinalizedAt = &finalizedAt
	}
	manifestJSON, err := normalizeJSONObject(input.ManifestJSON)
	if err != nil {
		return CreateArchiveManifestInput{}, fmt.Errorf("%w: manifest_json must be a JSON object: %v", ErrInvalid, err)
	}
	input.ManifestJSON = manifestJSON
	return input, nil
}

func normalizeArchiveItemInput(input ArchiveItemInput) (ArchiveItemInput, error) {
	input.ArchiveManifestID = strings.TrimSpace(input.ArchiveManifestID)
	if err := ids.Validate(ids.StorageArchiveManifestPrefix, input.ArchiveManifestID); err != nil {
		return ArchiveItemInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	if err := ids.Validate(ids.StorageEntryPrefix, input.StorageEntryID); err != nil {
		return ArchiveItemInput{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	archivePath, err := NormalizeLogicalPath(input.ArchivePath)
	if err != nil {
		return ArchiveItemInput{}, fmt.Errorf("%w: archive_path is invalid: %v", ErrInvalid, err)
	}
	input.ArchivePath = archivePath
	metadata, err := normalizeJSONObject(input.Metadata)
	if err != nil {
		return ArchiveItemInput{}, fmt.Errorf("%w: metadata must be a JSON object: %v", ErrInvalid, err)
	}
	input.Metadata = metadata
	return input, nil
}

func normalizeArchiveCommitInput(input ArchiveCommitInput) (ArchiveCommitInput, error) {
	manifest, err := normalizeCreateArchiveManifestInput(input.Manifest)
	if err != nil {
		return ArchiveCommitInput{}, err
	}
	if manifest.Status != "complete" || manifest.FinalizedAt == nil {
		return ArchiveCommitInput{}, fmt.Errorf("%w: archive commit requires a finalized complete manifest", ErrInvalid)
	}
	if len(input.Entries) == 0 {
		return ArchiveCommitInput{}, fmt.Errorf("%w: archive commit requires at least one entry", ErrInvalid)
	}
	input.Manifest = manifest
	entryIDs := make(map[string]struct{}, len(input.Entries))
	refIDs := make(map[string]struct{}, len(input.Entries))
	for index := range input.Entries {
		entry, err := normalizeRegisterEntryInput(input.Entries[index].Entry)
		if err != nil {
			return ArchiveCommitInput{}, fmt.Errorf("archive entry %d: %w", index, err)
		}
		if entry.ArchiveManifestID != manifest.ArchiveManifestID || entry.AvailabilityState != AvailabilityStateAvailable {
			return ArchiveCommitInput{}, fmt.Errorf("%w: archive entry %d does not belong to the available committed manifest", ErrInvalid, index)
		}
		if _, duplicate := entryIDs[entry.StorageEntryID]; duplicate {
			return ArchiveCommitInput{}, fmt.Errorf("%w: duplicate archive storage entry %s", ErrInvalid, entry.StorageEntryID)
		}
		entryIDs[entry.StorageEntryID] = struct{}{}

		ref, err := normalizeRegisterPhysicalRefInput(input.Entries[index].Ref)
		if err != nil {
			return ArchiveCommitInput{}, fmt.Errorf("archive ref %d: %w", index, err)
		}
		if ref.StorageEntryID != entry.StorageEntryID || ref.RefKind != PhysicalRefKindArchiveFile || ref.Status != PhysicalRefStatusAvailable {
			return ArchiveCommitInput{}, fmt.Errorf("%w: archive ref %d does not match its available archive entry", ErrInvalid, index)
		}
		if _, duplicate := refIDs[ref.StoragePhysicalRefID]; duplicate {
			return ArchiveCommitInput{}, fmt.Errorf("%w: duplicate archive physical ref %s", ErrInvalid, ref.StoragePhysicalRefID)
		}
		refIDs[ref.StoragePhysicalRefID] = struct{}{}

		item, err := normalizeArchiveItemInput(input.Entries[index].Item)
		if err != nil {
			return ArchiveCommitInput{}, fmt.Errorf("archive item %d: %w", index, err)
		}
		if item.ArchiveManifestID != manifest.ArchiveManifestID || item.StorageEntryID != entry.StorageEntryID {
			return ArchiveCommitInput{}, fmt.Errorf("%w: archive item %d does not match its manifest and entry", ErrInvalid, index)
		}
		input.Entries[index] = ArchiveCommitEntryInput{Entry: entry, Ref: ref, Item: item}
	}
	sourceIDs := map[string]struct{}{}
	for index := range input.SourceDispositions {
		disposition := &input.SourceDispositions[index]
		disposition.StorageEntryID = strings.TrimSpace(disposition.StorageEntryID)
		if err := ids.Validate(ids.StorageEntryPrefix, disposition.StorageEntryID); err != nil {
			return ArchiveCommitInput{}, fmt.Errorf("%w: source disposition %d: %v", ErrInvalid, index, err)
		}
		disposition.ExpectedAvailability = strings.TrimSpace(disposition.ExpectedAvailability)
		if !ValidAvailabilityState(disposition.ExpectedAvailability) {
			return ArchiveCommitInput{}, fmt.Errorf("%w: source disposition %d has invalid expected state", ErrInvalid, index)
		}
		disposition.AvailabilityState = strings.TrimSpace(disposition.AvailabilityState)
		if disposition.AvailabilityState != AvailabilityStateArchived && disposition.AvailabilityState != AvailabilityStateSuperseded {
			return ArchiveCommitInput{}, fmt.Errorf("%w: source disposition %d must be archived or superseded", ErrInvalid, index)
		}
		if _, duplicate := sourceIDs[disposition.StorageEntryID]; duplicate {
			return ArchiveCommitInput{}, fmt.Errorf("%w: duplicate source disposition %s", ErrInvalid, disposition.StorageEntryID)
		}
		sourceIDs[disposition.StorageEntryID] = struct{}{}
	}
	return input, nil
}

func archiveManifestFromCommit(input CreateArchiveManifestInput) ArchiveManifest {
	now := time.Now().UTC()
	return ArchiveManifest{
		ArchiveManifestID: input.ArchiveManifestID,
		ArchiveKey:        input.ArchiveKey,
		ArchiveKind:       input.ArchiveKind,
		OwnerNodeKey:      input.OwnerNodeKey,
		SourceRef:         input.SourceRef,
		Status:            input.Status,
		ManifestJSON:      input.ManifestJSON,
		CreatedAt:         now,
		FinalizedAt:       input.FinalizedAt,
	}
}

func entryFromCommit(input RegisterEntryInput) Entry {
	now := time.Now().UTC()
	entry := Entry{
		StorageEntryID:     input.StorageEntryID,
		StorageClass:       input.StorageClass,
		SourceArea:         input.SourceArea,
		OriginNodeKey:      input.OriginNodeKey,
		WatchedRootKey:     input.WatchedRootKey,
		DropzoneTransferID: input.DropzoneTransferID,
		LogicalPath:        input.LogicalPath,
		OriginalSourcePath: input.OriginalSourcePath,
		CurrentViewPath:    input.CurrentViewPath,
		ChecksumAlgorithm:  input.ChecksumAlgorithm,
		ChecksumHex:        input.ChecksumHex,
		SizeBytes:          input.SizeBytes,
		MimeType:           input.MimeType,
		FileClass:          input.FileClass,
		ProcessingState:    input.ProcessingState,
		AvailabilityState:  input.AvailabilityState,
		RetentionState:     input.RetentionState,
		Metadata:           input.Metadata,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if input.OriginNodeID != "" {
		entry.OriginNodeID = &input.OriginNodeID
	}
	if input.ProjectID != "" {
		entry.ProjectID = &input.ProjectID
	}
	if input.WatchedRootID != "" {
		entry.WatchedRootID = &input.WatchedRootID
	}
	if input.PrivateBackupOperationID != "" {
		entry.PrivateBackupOperationID = &input.PrivateBackupOperationID
	}
	if input.PrivateBackupItemID != "" {
		entry.PrivateBackupItemID = &input.PrivateBackupItemID
	}
	if input.ObjectID != "" {
		entry.ObjectID = &input.ObjectID
	}
	if input.ObjectVersionID != "" {
		entry.ObjectVersionID = &input.ObjectVersionID
	}
	if input.ArchiveManifestID != "" {
		entry.ArchiveManifestID = &input.ArchiveManifestID
	}
	return entry
}

func physicalRefFromCommit(input RegisterPhysicalRefInput) PhysicalRef {
	now := time.Now().UTC()
	ref := PhysicalRef{
		StoragePhysicalRefID: input.StoragePhysicalRefID,
		StorageEntryID:       input.StorageEntryID,
		RefKind:              input.RefKind,
		URI:                  input.URI,
		NodeKey:              input.NodeKey,
		ContentAddress:       input.ContentAddress,
		Status:               input.Status,
		Metadata:             input.Metadata,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if input.StorageEntryVersionID != "" {
		ref.StorageEntryVersionID = &input.StorageEntryVersionID
	}
	if input.NodeID != "" {
		ref.NodeID = &input.NodeID
	}
	return ref
}

func normalizeJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return nil, fmt.Errorf("must be an object")
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(normalized), nil
}
