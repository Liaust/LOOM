package storagecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

func normalizeRebindPathsInput(input RebindPathsInput) (RebindPathsInput, error) {
	if len(input.PhysicalRefs) == 0 && len(input.Entries) == 0 {
		return RebindPathsInput{}, fmt.Errorf("%w: at least one path rebind is required", ErrInvalid)
	}
	refIDs := map[string]bool{}
	for index := range input.PhysicalRefs {
		item := &input.PhysicalRefs[index]
		item.StoragePhysicalRefID = strings.TrimSpace(item.StoragePhysicalRefID)
		item.StorageEntryID = strings.TrimSpace(item.StorageEntryID)
		if item.StoragePhysicalRefID == "" || item.StorageEntryID == "" || strings.TrimSpace(item.ExpectedURI) == "" || strings.TrimSpace(item.NewURI) == "" || item.ExpectedURI == item.NewURI {
			return RebindPathsInput{}, fmt.Errorf("%w: physical ref rebind %d is incomplete or unchanged", ErrInvalid, index)
		}
		if refIDs[item.StoragePhysicalRefID] {
			return RebindPathsInput{}, fmt.Errorf("%w: duplicate physical ref %q", ErrInvalid, item.StoragePhysicalRefID)
		}
		refIDs[item.StoragePhysicalRefID] = true
	}
	entryIDs := map[string]bool{}
	for index := range input.Entries {
		item := &input.Entries[index]
		item.StorageEntryID = strings.TrimSpace(item.StorageEntryID)
		if item.StorageEntryID == "" {
			return RebindPathsInput{}, fmt.Errorf("%w: entry path rebind %d has no entry id", ErrInvalid, index)
		}
		if item.ExpectedOriginalSourcePath == item.NewOriginalSourcePath && item.ExpectedCurrentViewPath == item.NewCurrentViewPath {
			return RebindPathsInput{}, fmt.Errorf("%w: entry path rebind %q is unchanged", ErrInvalid, item.StorageEntryID)
		}
		if entryIDs[item.StorageEntryID] {
			return RebindPathsInput{}, fmt.Errorf("%w: duplicate entry %q", ErrInvalid, item.StorageEntryID)
		}
		entryIDs[item.StorageEntryID] = true
	}
	sort.Slice(input.PhysicalRefs, func(i, j int) bool {
		return input.PhysicalRefs[i].StoragePhysicalRefID < input.PhysicalRefs[j].StoragePhysicalRefID
	})
	sort.Slice(input.Entries, func(i, j int) bool { return input.Entries[i].StorageEntryID < input.Entries[j].StorageEntryID })
	return input, nil
}

func rebindPaths(ctx context.Context, db *sql.DB, input RebindPathsInput) (result RebindPathsResult, err error) {
	if db == nil {
		return result, fmt.Errorf("storage catalog database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = rebindPathsTx(ctx, tx, input, &result); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

// validateRebindBatchOrder bounds cross-batch uniqueness state to one prior
// storage-entry ID. Manifest splitting keeps every entry's refs and display
// path mutation in one child and emits children in entry-ID order. Requiring
// the same order here rejects duplicate/split entries without retaining an
// artifact-sized in-memory set.
func validateRebindBatchOrder(input RebindPathsInput, previousEntryID *string) error {
	entryIDs := make(map[string]struct{}, len(input.PhysicalRefs)+len(input.Entries))
	for _, item := range input.PhysicalRefs {
		entryIDs[item.StorageEntryID] = struct{}{}
	}
	for _, item := range input.Entries {
		entryIDs[item.StorageEntryID] = struct{}{}
	}
	ordered := make([]string, 0, len(entryIDs))
	for entryID := range entryIDs {
		ordered = append(ordered, entryID)
	}
	sort.Strings(ordered)
	if len(ordered) == 0 {
		return fmt.Errorf("%w: empty path-rebind batch", ErrInvalid)
	}
	if *previousEntryID != "" && ordered[0] <= *previousEntryID {
		return fmt.Errorf("%w: path-rebind batches are not strictly ordered by storage entry; %q follows %q", ErrInvalid, ordered[0], *previousEntryID)
	}
	*previousEntryID = ordered[len(ordered)-1]
	return nil
}

func preflightRebindPathBatches(batchCount int, load RebindPathsBatchLoader) error {
	previousEntryID := ""
	for index := 0; index < batchCount; index++ {
		input, err := load(index)
		if err != nil {
			return fmt.Errorf("preflight path-rebind batch %d/%d: %w", index+1, batchCount, err)
		}
		normalized, err := normalizeRebindPathsInput(input)
		if err != nil {
			return fmt.Errorf("preflight path-rebind batch %d/%d: %w", index+1, batchCount, err)
		}
		if err := validateRebindBatchOrder(normalized, &previousEntryID); err != nil {
			return fmt.Errorf("preflight path-rebind batch %d/%d: %w", index+1, batchCount, err)
		}
	}
	return nil
}

func rebindPathBatches(ctx context.Context, db *sql.DB, batchCount int, load RebindPathsBatchLoader) (result RebindPathsResult, err error) {
	if db == nil {
		return result, fmt.Errorf("storage catalog database is required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	previousEntryID := ""
	for index := 0; index < batchCount; index++ {
		input, loadErr := load(index)
		if loadErr != nil {
			err = fmt.Errorf("reload path-rebind batch %d/%d: %w", index+1, batchCount, loadErr)
			return result, err
		}
		normalized, normalizeErr := normalizeRebindPathsInput(input)
		if normalizeErr != nil {
			err = fmt.Errorf("reload path-rebind batch %d/%d: %w", index+1, batchCount, normalizeErr)
			return result, err
		}
		if duplicateErr := validateRebindBatchOrder(normalized, &previousEntryID); duplicateErr != nil {
			err = fmt.Errorf("reload path-rebind batch %d/%d: %w", index+1, batchCount, duplicateErr)
			return result, err
		}
		if err = rebindPathsTx(ctx, tx, normalized, &result); err != nil {
			err = fmt.Errorf("apply path-rebind batch %d/%d: %w", index+1, batchCount, err)
			return result, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func rebindPathsTx(ctx context.Context, tx *sql.Tx, input RebindPathsInput, result *RebindPathsResult) (err error) {
	for _, item := range input.PhysicalRefs {
		var current string
		if err = tx.QueryRowContext(ctx, `
			SELECT uri FROM storage.storage_physical_refs
			WHERE storage_physical_ref_id = $1 AND storage_entry_id = $2
			FOR UPDATE
		`, item.StoragePhysicalRefID, item.StorageEntryID).Scan(&current); err != nil {
			return fmt.Errorf("lock physical ref %s: %w", item.StoragePhysicalRefID, err)
		}
		if current == item.NewURI {
			result.AlreadyApplied++
			continue
		}
		if current != item.ExpectedURI {
			return fmt.Errorf("physical ref %s changed since review: expected %q, found %q", item.StoragePhysicalRefID, item.ExpectedURI, current)
		}
		if _, err = tx.ExecContext(ctx, `
			UPDATE storage.storage_physical_refs SET uri = $1, updated_at = now()
			WHERE storage_physical_ref_id = $2 AND storage_entry_id = $3
		`, item.NewURI, item.StoragePhysicalRefID, item.StorageEntryID); err != nil {
			return fmt.Errorf("rebind physical ref %s: %w", item.StoragePhysicalRefID, err)
		}
		result.PhysicalRefsUpdated++
	}
	for _, item := range input.Entries {
		var currentOriginal, currentView string
		if err = tx.QueryRowContext(ctx, `
			SELECT original_source_path, current_view_path FROM storage.storage_entries
			WHERE storage_entry_id = $1 FOR UPDATE
		`, item.StorageEntryID).Scan(&currentOriginal, &currentView); err != nil {
			return fmt.Errorf("lock storage entry %s: %w", item.StorageEntryID, err)
		}
		if currentOriginal == item.NewOriginalSourcePath && currentView == item.NewCurrentViewPath {
			result.AlreadyApplied++
			continue
		}
		if currentOriginal != item.ExpectedOriginalSourcePath || currentView != item.ExpectedCurrentViewPath {
			return fmt.Errorf("storage entry %s paths changed since review", item.StorageEntryID)
		}
		if _, err = tx.ExecContext(ctx, `
			UPDATE storage.storage_entries
			SET original_source_path = $1, current_view_path = $2, updated_at = now()
			WHERE storage_entry_id = $3
		`, item.NewOriginalSourcePath, item.NewCurrentViewPath, item.StorageEntryID); err != nil {
			return fmt.Errorf("rebind storage entry %s: %w", item.StorageEntryID, err)
		}
		result.EntriesUpdated++
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func entrySelectSQL() string {
	return `
		SELECT
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
		FROM storage.storage_entries`
}

func physicalRefSelectSQL() string {
	return `
		SELECT
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
		FROM storage.storage_physical_refs`
}

func entryVersionSelectSQL() string {
	return `
		SELECT
			storage_entry_version_id,
			storage_entry_id,
			version_number,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			physical_ref_id,
			metadata,
			created_at
		FROM storage.storage_entry_versions`
}

func retentionEntrySelectSQL() string {
	return `
		SELECT
			storage_retention_entry_id,
			storage_entry_id,
			policy_key,
			retention_state,
			retained_until,
			metadata,
			created_at,
			updated_at
		FROM storage.retention_entries`
}

func tombstoneSelectSQL() string {
	return `
		SELECT
			storage_tombstone_id,
			storage_entry_id,
			tombstone_kind,
			reason,
			created_by,
			metadata,
			created_at
		FROM storage.tombstones`
}

func archiveManifestSelectSQL() string {
	return `
		SELECT
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
		FROM storage.archive_manifests`
}

func scanEntry(row scanner) (Entry, error) {
	var entry Entry
	var originNodeID sql.NullString
	var projectID sql.NullString
	var watchedRootID sql.NullString
	var privateBackupOperationID sql.NullString
	var privateBackupItemID sql.NullString
	var objectID sql.NullString
	var objectVersionID sql.NullString
	var archiveManifestID sql.NullString
	var sizeBytes sql.NullInt64
	var metadata []byte
	var deletedAt sql.NullTime

	if err := row.Scan(
		&entry.StorageEntryID,
		&entry.StorageClass,
		&entry.SourceArea,
		&originNodeID,
		&entry.OriginNodeKey,
		&projectID,
		&watchedRootID,
		&entry.WatchedRootKey,
		&entry.DropzoneTransferID,
		&privateBackupOperationID,
		&privateBackupItemID,
		&objectID,
		&objectVersionID,
		&archiveManifestID,
		&entry.LogicalPath,
		&entry.OriginalSourcePath,
		&entry.CurrentViewPath,
		&entry.ChecksumAlgorithm,
		&entry.ChecksumHex,
		&sizeBytes,
		&entry.MimeType,
		&entry.FileClass,
		&entry.ProcessingState,
		&entry.AvailabilityState,
		&entry.RetentionState,
		&metadata,
		&entry.CreatedAt,
		&entry.UpdatedAt,
		&deletedAt,
	); err != nil {
		return Entry{}, err
	}

	entry.OriginNodeID = nullableStringPtr(originNodeID)
	entry.ProjectID = nullableStringPtr(projectID)
	entry.WatchedRootID = nullableStringPtr(watchedRootID)
	entry.PrivateBackupOperationID = nullableStringPtr(privateBackupOperationID)
	entry.PrivateBackupItemID = nullableStringPtr(privateBackupItemID)
	entry.ObjectID = nullableStringPtr(objectID)
	entry.ObjectVersionID = nullableStringPtr(objectVersionID)
	entry.ArchiveManifestID = nullableStringPtr(archiveManifestID)
	if sizeBytes.Valid {
		entry.SizeBytes = &sizeBytes.Int64
	}
	if deletedAt.Valid {
		entry.DeletedAt = &deletedAt.Time
	}
	entry.Metadata = normalizedScannedJSON(metadata)
	return entry, nil
}

func scanPhysicalRef(row scanner) (PhysicalRef, error) {
	var ref PhysicalRef
	var versionID sql.NullString
	var nodeID sql.NullString
	var metadata []byte

	if err := row.Scan(
		&ref.StoragePhysicalRefID,
		&ref.StorageEntryID,
		&versionID,
		&ref.RefKind,
		&ref.URI,
		&nodeID,
		&ref.NodeKey,
		&ref.ContentAddress,
		&ref.Status,
		&metadata,
		&ref.CreatedAt,
		&ref.UpdatedAt,
	); err != nil {
		return PhysicalRef{}, err
	}

	ref.StorageEntryVersionID = nullableStringPtr(versionID)
	ref.NodeID = nullableStringPtr(nodeID)
	ref.Metadata = normalizedScannedJSON(metadata)
	return ref, nil
}

func scanEntryVersion(row scanner) (EntryVersion, error) {
	var version EntryVersion
	var physicalRefID sql.NullString
	var sizeBytes sql.NullInt64
	var metadata []byte

	if err := row.Scan(
		&version.StorageEntryVersionID,
		&version.StorageEntryID,
		&version.VersionNumber,
		&version.ChecksumAlgorithm,
		&version.ChecksumHex,
		&sizeBytes,
		&physicalRefID,
		&metadata,
		&version.CreatedAt,
	); err != nil {
		return EntryVersion{}, err
	}
	if sizeBytes.Valid {
		version.SizeBytes = &sizeBytes.Int64
	}
	version.PhysicalRefID = nullableStringPtr(physicalRefID)
	version.Metadata = normalizedScannedJSON(metadata)
	return version, nil
}

func scanRetentionEntry(row scanner) (RetentionEntry, error) {
	var retention RetentionEntry
	var retainedUntil sql.NullTime
	var metadata []byte

	if err := row.Scan(
		&retention.StorageRetentionEntryID,
		&retention.StorageEntryID,
		&retention.PolicyKey,
		&retention.RetentionState,
		&retainedUntil,
		&metadata,
		&retention.CreatedAt,
		&retention.UpdatedAt,
	); err != nil {
		return RetentionEntry{}, err
	}
	if retainedUntil.Valid {
		retention.RetainedUntil = &retainedUntil.Time
	}
	retention.Metadata = normalizedScannedJSON(metadata)
	return retention, nil
}

func scanTombstone(row scanner) (Tombstone, error) {
	var tombstone Tombstone
	var metadata []byte

	if err := row.Scan(
		&tombstone.StorageTombstoneID,
		&tombstone.StorageEntryID,
		&tombstone.TombstoneKind,
		&tombstone.Reason,
		&tombstone.CreatedBy,
		&metadata,
		&tombstone.CreatedAt,
	); err != nil {
		return Tombstone{}, err
	}
	tombstone.Metadata = normalizedScannedJSON(metadata)
	return tombstone, nil
}

func scanArchiveManifest(row scanner) (ArchiveManifest, error) {
	var manifest ArchiveManifest
	var ownerNodeID sql.NullString
	var manifestJSON []byte
	var finalizedAt sql.NullTime

	if err := row.Scan(
		&manifest.ArchiveManifestID,
		&manifest.ArchiveKey,
		&manifest.ArchiveKind,
		&ownerNodeID,
		&manifest.OwnerNodeKey,
		&manifest.SourceRef,
		&manifest.Status,
		&manifestJSON,
		&manifest.CreatedAt,
		&finalizedAt,
	); err != nil {
		return ArchiveManifest{}, err
	}
	manifest.OwnerNodeID = nullableStringPtr(ownerNodeID)
	manifest.ManifestJSON = normalizedScannedJSON(manifestJSON)
	if finalizedAt.Valid {
		manifest.FinalizedAt = &finalizedAt.Time
	}
	return manifest, nil
}

func nullableStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableStringArg(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64Arg(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimeArg(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func normalizedScannedJSON(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}
