package watchedroots

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"loom.local/loom/internal/filesystemmeta"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/storagecatalog"
)

type backupStorageContext struct {
	SourceArea   string
	ProjectID    string
	ProjectSlug  string
	LocalRootKey string
	BoxAreaKey   string
}

func resolveBackupStorageContextTx(ctx context.Context, tx *sql.Tx, node nodes.Node, root WatchedRoot) (backupStorageContext, error) {
	if projectContext, ok, err := lookupProjectBackupStorageContextTx(ctx, tx, node.NodeID, root.RootKey); err != nil {
		return backupStorageContext{}, err
	} else if ok {
		return projectContext, nil
	}
	if boxContext, ok, err := lookupBoxBackupStorageContextTx(ctx, tx, node.NodeID, root.RootKey); err != nil {
		return backupStorageContext{}, err
	} else if ok {
		return boxContext, nil
	}
	return fallbackBackupStorageContext(root), nil
}

func lookupProjectBackupStorageContextTx(ctx context.Context, tx *sql.Tx, nodeID, rootKey string) (backupStorageContext, bool, error) {
	var projectID, projectSlug, localRootKey string
	err := tx.QueryRowContext(ctx, `
		SELECT pwr.project_id, p.slug, pwr.local_root_key
		FROM projects.project_watched_root_registrations pwr
		JOIN projects.projects p ON p.project_id = pwr.project_id
		WHERE pwr.node_id = $1
		  AND pwr.backend_root_key = $2
		ORDER BY pwr.updated_at DESC
		LIMIT 1
	`, nodeID, rootKey).Scan(&projectID, &projectSlug, &localRootKey)
	if err != nil {
		if err == sql.ErrNoRows {
			return backupStorageContext{}, false, nil
		}
		return backupStorageContext{}, false, err
	}
	return backupStorageContext{
		SourceArea:   storagecatalog.SourceAreaProjects,
		ProjectID:    projectID,
		ProjectSlug:  projectSlug,
		LocalRootKey: localRootKey,
	}, true, nil
}

func lookupBoxBackupStorageContextTx(ctx context.Context, tx *sql.Tx, nodeID, rootKey string) (backupStorageContext, bool, error) {
	var areaKey, localRootKey string
	err := tx.QueryRowContext(ctx, `
		SELECT area_key, local_root_key
		FROM box.watch_root_registrations
		WHERE node_id = $1
		  AND backend_root_key = $2
		ORDER BY updated_at DESC
		LIMIT 1
	`, nodeID, rootKey).Scan(&areaKey, &localRootKey)
	if err != nil {
		if err == sql.ErrNoRows {
			return backupStorageContext{}, false, nil
		}
		return backupStorageContext{}, false, err
	}
	return backupStorageContext{
		SourceArea:   sourceAreaForBoxArea(areaKey),
		LocalRootKey: localRootKey,
		BoxAreaKey:   areaKey,
	}, true, nil
}

func fallbackBackupStorageContext(root WatchedRoot) backupStorageContext {
	key := strings.ToLower(strings.TrimSpace(root.RootKey))
	switch {
	case key == "loom_box__notes" || key == "notes":
		return backupStorageContext{SourceArea: storagecatalog.SourceAreaNotes, LocalRootKey: "notes", BoxAreaKey: "notes"}
	case key == "loom_box__documents" || key == "loom_box__launchpad" || key == "documents" || key == "launchpad":
		return backupStorageContext{SourceArea: storagecatalog.SourceAreaDocuments, LocalRootKey: "documents", BoxAreaKey: "documents"}
	default:
		return backupStorageContext{SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, LocalRootKey: root.RootKey}
	}
}

func sourceAreaForBoxArea(areaKey string) string {
	switch strings.ToLower(strings.TrimSpace(areaKey)) {
	case "notes":
		return storagecatalog.SourceAreaNotes
	case "documents", "launchpad":
		return storagecatalog.SourceAreaDocuments
	default:
		return storagecatalog.SourceAreaExternalWatchedRoot
	}
}

func registerBackupItemStorageCatalogTx(ctx context.Context, tx *sql.Tx, node nodes.Node, root WatchedRoot, batch BackupBatch, item BackupItem, storageContext backupStorageContext) error {
	if item.WatchedRootBackupItemID == "" {
		return nil
	}
	exists, err := storageEntryExistsForBackupItemTx(ctx, tx, item.WatchedRootBackupItemID)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if item.ItemKind == BackupItemKindDeletionMarker {
		if err := moveCurrentBackupEntriesToHistoryTx(ctx, tx, root.WatchedRootID, item.RelativePath, storagecatalog.AvailabilityStateDeleted); err != nil {
			return err
		}
		return insertBackupStorageEntryTx(ctx, tx, node, root, batch, item, storageContext, backupEntryTombstone)
	}
	if item.Status == BackupItemStatusSkipped || item.Status == BackupItemStatusFailed {
		return insertBackupStorageEntryTx(ctx, tx, node, root, batch, item, storageContext, backupEntryDiagnostic)
	}
	if item.Status != BackupItemStatusAccepted && item.Status != BackupItemStatusDuplicate {
		return nil
	}
	if strings.TrimSpace(item.RelativePath) == "" {
		return insertBackupStorageEntryTx(ctx, tx, node, root, batch, item, storageContext, backupEntryDiagnostic)
	}
	if err := moveCurrentBackupEntriesToHistoryTx(ctx, tx, root.WatchedRootID, item.RelativePath, storagecatalog.AvailabilityStateSuperseded); err != nil {
		return err
	}
	return insertBackupStorageEntryTx(ctx, tx, node, root, batch, item, storageContext, backupEntryCurrent)
}

type backupEntryDisposition string

const (
	backupEntryCurrent    backupEntryDisposition = "current"
	backupEntryTombstone  backupEntryDisposition = "tombstone"
	backupEntryDiagnostic backupEntryDisposition = "diagnostic"
)

func storageEntryExistsForBackupItemTx(ctx context.Context, tx *sql.Tx, backupItemID string) (bool, error) {
	var existing string
	err := tx.QueryRowContext(ctx, `
		SELECT storage_entry_id
		FROM storage.storage_entries
		WHERE private_backup_item_id = $1
		LIMIT 1
	`, backupItemID).Scan(&existing)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func moveCurrentBackupEntriesToHistoryTx(ctx context.Context, tx *sql.Tx, watchedRootID, relativePath, availabilityState string) error {
	relativePath = strings.TrimSpace(relativePath)
	if watchedRootID == "" || relativePath == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE storage.storage_entries
		SET availability_state = $1,
		    updated_at = now()
		WHERE storage_class = $2
		  AND watched_root_id = $3
		  AND original_source_path = $4
		  AND availability_state = $5
	`, availabilityState, storagecatalog.StorageClassPrivateBackup, watchedRootID, relativePath, storagecatalog.AvailabilityStateAvailable)
	return err
}

func insertBackupStorageEntryTx(ctx context.Context, tx *sql.Tx, node nodes.Node, root WatchedRoot, batch BackupBatch, item BackupItem, storageContext backupStorageContext, disposition backupEntryDisposition) error {
	entryID := ids.NewStorageEntryID()
	logicalPath := backupLogicalPath(item)
	currentViewPath := backupCurrentViewPath(node.NodeKey, root, batch, item, storageContext, disposition)
	checksumAlgorithm, checksumHex := checksumParts(item.ContentHashURI)
	sizeBytes := nullablePositiveOrZeroSize(item)
	processingState, availabilityState := backupCatalogStates(item, disposition)
	fileClass := backupItemFileClass(item, logicalPath)
	metadata, err := backupStorageMetadata(node, root, batch, item, storageContext, disposition)
	if err != nil {
		return err
	}
	privateBackupOperationID := ""
	if item.PrivateBackupOperationID != nil {
		privateBackupOperationID = *item.PrivateBackupOperationID
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO storage.storage_entries (
			storage_entry_id,
			storage_class,
			source_area,
			origin_node_id,
			origin_node_key,
			project_id,
			watched_root_id,
			watched_root_key,
			private_backup_operation_id,
			private_backup_item_id,
			logical_path,
			original_source_path,
			current_view_path,
			checksum_algorithm,
			checksum_hex,
			size_bytes,
			file_class,
			processing_state,
			availability_state,
			retention_state,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,nullif($6,''),$7,$8,nullif($9,''),$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
	`, entryID,
		storagecatalog.StorageClassPrivateBackup,
		storageContext.SourceArea,
		node.NodeID,
		node.NodeKey,
		storageContext.ProjectID,
		root.WatchedRootID,
		root.RootKey,
		privateBackupOperationID,
		item.WatchedRootBackupItemID,
		logicalPath,
		item.RelativePath,
		currentViewPath,
		checksumAlgorithm,
		checksumHex,
		sizeBytes,
		fileClass,
		processingState,
		availabilityState,
		storagecatalog.RetentionStateNone,
		metadata,
	); err != nil {
		return err
	}

	if disposition == backupEntryTombstone {
		tombstoneMetadata, err := json.Marshal(map[string]any{
			"schema_version":               "storage.watched_root_tombstone.v0.6",
			"source":                       "watched_roots.backup_batches",
			"source_node_key":              node.NodeKey,
			"watched_root_id":              root.WatchedRootID,
			"watched_root_key":             root.RootKey,
			"watched_root_backup_batch_id": batch.WatchedRootBackupBatchID,
			"watched_root_backup_item_id":  item.WatchedRootBackupItemID,
			"relative_path":                item.RelativePath,
			"previous_hash_uri":            item.PreviousHashURI,
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO storage.tombstones (
				storage_tombstone_id,
				storage_entry_id,
				tombstone_kind,
				reason,
				created_by,
				metadata
			)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, ids.NewStorageTombstoneID(),
			entryID,
			storagecatalog.TombstoneKindSourceDeleted,
			"watched root reported source deletion",
			node.NodeKey,
			tombstoneMetadata,
		); err != nil {
			return err
		}
	}

	if privateBackupOperationID == "" || disposition != backupEntryCurrent {
		if item.ArtifactKind == BackupArtifactKindFileTransfer && disposition == backupEntryCurrent {
			return insertFileTransferBackupPhysicalRefTx(ctx, tx, node, entryID, item)
		}
		return nil
	}
	storageRef, err := privateBackupStorageRefTx(ctx, tx, privateBackupOperationID)
	if err != nil {
		return err
	}
	if storageRef == "" {
		return nil
	}
	refMetadata, err := json.Marshal(map[string]any{
		"schema_version":              "storage.watched_root_backup_ref.v0.6",
		"source":                      "watched_roots.backup_batches",
		"private_backup_operation_id": privateBackupOperationID,
		"watched_root_backup_item_id": item.WatchedRootBackupItemID,
		"artifact_format":             "watched_root_tar.v0",
		"content_member":              "content",
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO storage.storage_physical_refs (
			storage_physical_ref_id,
			storage_entry_id,
			ref_kind,
			uri,
			node_id,
			node_key,
			content_address,
			status,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, ids.NewStoragePhysicalRefID(),
		entryID,
		storagecatalog.PhysicalRefKindBackupArtifact,
		storageRef,
		node.NodeID,
		node.NodeKey,
		item.ContentHashURI,
		storagecatalog.PhysicalRefStatusAvailable,
		refMetadata,
	)
	return err
}

func backupItemFileClass(item BackupItem, logicalPath string) string {
	if item.ItemKind == BackupItemKindDirectory {
		return storagecatalog.FileClassDirectory
	}
	observation := backupItemFilesystemObservation(item)
	if observation != nil {
		result := storagecatalog.Classify(storagecatalog.ClassificationInput{
			Path:              firstNonEmpty(item.RelativePath, logicalPath),
			IsDirectory:       observation.Kind == filesystemmeta.ObjectKindDirectory || observation.Kind == filesystemmeta.ObjectKindPackage,
			IsPackage:         observation.IsPackage || observation.Kind == filesystemmeta.ObjectKindPackage,
			GeneratedMetadata: observation.GeneratedMetadata,
			PermissionDenied:  observation.PermissionDenied,
			SizeBytes:         item.SizeBytes,
		})
		return result.FileClass
	}
	return storagecatalog.ClassifyPath(firstNonEmpty(item.RelativePath, logicalPath), "")
}

func backupItemFilesystemObservation(item BackupItem) *filesystemmeta.Observation {
	if len(item.Metadata) == 0 {
		return nil
	}
	var payload struct {
		FilesystemObservation *filesystemmeta.Observation `json:"filesystem_observation"`
	}
	if err := json.Unmarshal(item.Metadata, &payload); err != nil {
		return nil
	}
	return payload.FilesystemObservation
}

func insertFileTransferBackupPhysicalRefTx(ctx context.Context, tx *sql.Tx, node nodes.Node, storageEntryID string, item BackupItem) error {
	sourceRef, err := fileTransferStorageRefTx(ctx, tx, item.ArtifactRef)
	if err != nil {
		return err
	}
	if sourceRef.URI == "" {
		return nil
	}
	refMetadata, err := json.Marshal(map[string]any{
		"schema_version":                   "storage.watched_root_file_transfer_ref.v0.6.2",
		"source":                           "watched_roots.backup_batches",
		"file_transfer_id":                 item.ArtifactRef,
		"watched_root_backup_item_id":      item.WatchedRootBackupItemID,
		"source_storage_entry_id":          sourceRef.StorageEntryID,
		"source_storage_physical_ref_id":   sourceRef.StoragePhysicalRefID,
		"source_storage_physical_ref_kind": sourceRef.RefKind,
		"source_storage_uri":               sourceRef.URI,
	})
	if err != nil {
		return err
	}
	refKind := sourceRef.RefKind
	if !storagecatalog.ValidPhysicalRefKind(refKind) {
		refKind = storagecatalog.PhysicalRefKindLocalPath
	}
	contentAddress := sourceRef.ContentAddress
	if contentAddress == "" {
		contentAddress = item.ContentHashURI
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO storage.storage_physical_refs (
			storage_physical_ref_id,
			storage_entry_id,
			ref_kind,
			uri,
			node_id,
			node_key,
			content_address,
			status,
			metadata
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, ids.NewStoragePhysicalRefID(),
		storageEntryID,
		refKind,
		sourceRef.URI,
		node.NodeID,
		node.NodeKey,
		contentAddress,
		storagecatalog.PhysicalRefStatusAvailable,
		refMetadata,
	)
	return err
}

type fileTransferStorageRef struct {
	StorageEntryID       string
	StoragePhysicalRefID string
	RefKind              string
	URI                  string
	ContentAddress       string
}

func fileTransferStorageRefTx(ctx context.Context, tx *sql.Tx, transferID string) (fileTransferStorageRef, error) {
	var ref fileTransferStorageRef
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(ft.storage_entry_id, ''),
		       COALESCE(ft.storage_physical_ref_id, ''),
		       COALESCE(spr.ref_kind, ''),
		       COALESCE(spr.uri, ''),
		       COALESCE(spr.content_address, '')
		FROM storage.file_transfers ft
		LEFT JOIN storage.storage_physical_refs spr
		  ON spr.storage_physical_ref_id = ft.storage_physical_ref_id
		WHERE ft.file_transfer_id = $1
		  AND ft.status = $2
	`, transferID, filetransfer.StatusAccepted).Scan(
		&ref.StorageEntryID,
		&ref.StoragePhysicalRefID,
		&ref.RefKind,
		&ref.URI,
		&ref.ContentAddress,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return fileTransferStorageRef{}, fmt.Errorf("%w: accepted file transfer %s was not found", ErrInvalid, transferID)
		}
		return fileTransferStorageRef{}, err
	}
	return ref, nil
}

func privateBackupStorageRefTx(ctx context.Context, tx *sql.Tx, operationID string) (string, error) {
	var storageRef string
	err := tx.QueryRowContext(ctx, `
		SELECT storage_ref
		FROM sync.private_backup_operations
		WHERE private_backup_operation_id = $1
	`, operationID).Scan(&storageRef)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: private backup operation %s was not found", ErrInvalid, operationID)
		}
		return "", err
	}
	return strings.TrimSpace(storageRef), nil
}

func backupCatalogStates(item BackupItem, disposition backupEntryDisposition) (string, string) {
	switch disposition {
	case backupEntryDiagnostic:
		if item.Status == BackupItemStatusFailed {
			return storagecatalog.ProcessingStateFailed, storagecatalog.AvailabilityStateFailed
		}
		return storagecatalog.ProcessingStateExcluded, storagecatalog.AvailabilityStateDiscovered
	case backupEntryTombstone:
		return storagecatalog.ProcessingStateMetadataOnly, storagecatalog.AvailabilityStateTombstoned
	default:
		if item.ItemKind == BackupItemKindMetadata {
			return storagecatalog.ProcessingStateMetadataOnly, storagecatalog.AvailabilityStateAvailable
		}
		return storagecatalog.ProcessingStateBackupOnly, storagecatalog.AvailabilityStateAvailable
	}
}

func backupLogicalPath(item BackupItem) string {
	if strings.TrimSpace(item.RelativePath) != "" {
		return item.RelativePath
	}
	return path.Join("_system", "watched-root-backups", firstNonEmpty(item.LocalItemRef, item.WatchedRootBackupItemID)+".json")
}

func backupCurrentViewPath(nodeKey string, root WatchedRoot, batch BackupBatch, item BackupItem, _ backupStorageContext, disposition backupEntryDisposition) string {
	switch disposition {
	case backupEntryDiagnostic, backupEntryTombstone:
		return ""
	}
	base := path.Join("backups", sanitizeBackupLabel(nodeKey), sanitizeBackupLabel(root.RootKey), sanitizeBackupLabel(backupBatchKey(batch)))
	if item.ArtifactKind == BackupArtifactKindPrivateBackupOperation && item.PrivateBackupOperationID != nil {
		return path.Join(base, "artifacts", sanitizeBackupLabel(*item.PrivateBackupOperationID), "payload.tar")
	}
	return path.Join(base, "payload", item.RelativePath)
}

func checksumParts(hashURI string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(hashURI), ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	algorithm := strings.ToLower(strings.TrimSpace(parts[0]))
	value := strings.ToLower(strings.TrimSpace(parts[1]))
	if algorithm == "" || value == "" {
		return "", ""
	}
	return algorithm, value
}

func nullablePositiveOrZeroSize(item BackupItem) any {
	if item.ItemKind == BackupItemKindDeletionMarker || item.Status == BackupItemStatusSkipped {
		return nil
	}
	if item.SizeBytes < 0 {
		return nil
	}
	return item.SizeBytes
}

func backupStorageMetadata(node nodes.Node, root WatchedRoot, batch BackupBatch, item BackupItem, storageContext backupStorageContext, disposition backupEntryDisposition) (json.RawMessage, error) {
	var itemMetadata map[string]any
	_ = json.Unmarshal(item.Metadata, &itemMetadata)
	return json.Marshal(map[string]any{
		"schema_version":               "storage.watched_root_backup_entry.v0.6",
		"source":                       "watched_roots.backup_batches",
		"source_node_key":              node.NodeKey,
		"watched_root_id":              root.WatchedRootID,
		"watched_root_key":             root.RootKey,
		"watched_root_backup_batch_id": batch.WatchedRootBackupBatchID,
		"watched_root_backup_item_id":  item.WatchedRootBackupItemID,
		"local_item_ref":               item.LocalItemRef,
		"item_kind":                    item.ItemKind,
		"item_status":                  item.Status,
		"backup_mode":                  item.BackupMode,
		"relative_path":                item.RelativePath,
		"content_hash_uri":             item.ContentHashURI,
		"previous_hash_uri":            item.PreviousHashURI,
		"artifact_kind":                item.ArtifactKind,
		"artifact_ref":                 item.ArtifactRef,
		"error_code":                   item.ErrorCode,
		"error_message":                item.ErrorMessage,
		"catalog_disposition":          disposition,
		"source_area":                  storageContext.SourceArea,
		"project_id":                   storageContext.ProjectID,
		"project_slug":                 storageContext.ProjectSlug,
		"local_root_key":               storageContext.LocalRootKey,
		"box_area_key":                 storageContext.BoxAreaKey,
		"client_item_metadata":         itemMetadata,
	})
}

func sanitizeBackupLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." {
		return "untitled"
	}
	replacer := strings.NewReplacer("/", " ", "\\", " ", "\x00", "")
	value = strings.Join(strings.Fields(replacer.Replace(value)), " ")
	if value == "" || value == "." || value == ".." {
		return "untitled"
	}
	return value
}

func shortRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "entry"
	}
	if idx := strings.LastIndex(value, "_"); idx >= 0 && idx+1 < len(value) {
		value = value[idx+1:]
	}
	if len(value) <= 10 {
		return value
	}
	return value[len(value)-10:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
