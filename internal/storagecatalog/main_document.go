package storagecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

type MainDocumentInput struct {
	StorageEntryID         string                      `json:"storage_entry_id,omitempty"`
	StoragePhysicalRefID   string                      `json:"storage_physical_ref_id,omitempty"`
	RetentionPhysicalRefID string                      `json:"retention_physical_ref_id,omitempty"`
	NodeKey                string                      `json:"node_key,omitempty"`
	BackingRoot            string                      `json:"backing_root,omitempty"`
	RelativePath           string                      `json:"relative_path"`
	PhysicalPath           string                      `json:"physical_path"`
	RetentionPath          string                      `json:"retention_path,omitempty"`
	FileSizeBytes          int64                       `json:"file_size_bytes"`
	ChecksumAlgorithm      string                      `json:"checksum_algorithm"`
	ChecksumValue          string                      `json:"checksum_value"`
	ModifiedAt             time.Time                   `json:"modified_at,omitempty"`
	SourceModifiedAt       *time.Time                  `json:"source_modified_at,omitempty"`
	SourceModifiedBasis    string                      `json:"source_modified_basis,omitempty"`
	SourceCreatedAt        *time.Time                  `json:"source_created_at,omitempty"`
	SourceCreatedBasis     string                      `json:"source_created_basis,omitempty"`
	ImportedAt             time.Time                   `json:"imported_at,omitempty"`
	RetentionVerifiedAt    time.Time                   `json:"retention_verified_at,omitempty"`
	FilesystemObservation  *filesystemmeta.Observation `json:"filesystem_observation,omitempty"`
}

type MainDocumentTombstoneInput struct {
	StorageEntryID string    `json:"storage_entry_id,omitempty"`
	RelativePath   string    `json:"relative_path,omitempty"`
	TombstoneKind  string    `json:"tombstone_kind,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	CreatedBy      string    `json:"created_by,omitempty"`
	TombstonedAt   time.Time `json:"tombstoned_at,omitempty"`
}

func (s Service) RegisterMainDocument(ctx context.Context, input MainDocumentInput) (EntryDetail, error) {
	normalized, err := normalizeMainDocumentInput(input)
	if err != nil {
		return EntryDetail{}, err
	}
	viewPath := mainDocumentViewPath(normalized.RelativePath)
	existing, existingErr := s.InspectEntry(ctx, viewPath)
	switch {
	case existingErr == nil:
		normalized.StorageEntryID = existing.Entry.StorageEntryID
		if existing.Entry.AvailabilityState != AvailabilityStateAvailable || latestAvailablePhysicalRefID(existing.PhysicalRefs) == "" {
			break
		}
		if existing.Entry.ChecksumAlgorithm == normalized.ChecksumAlgorithm &&
			existing.Entry.ChecksumHex == normalized.ChecksumValue &&
			int64PtrEqual(existing.Entry.SizeBytes, normalized.FileSizeBytes) {
			retentionRefID := latestAvailablePhysicalRefIDByKind(existing.PhysicalRefs, PhysicalRefKindRetentionPayload)
			addedRetentionRef := false
			if retentionRefID == "" && normalized.RetentionPath != "" {
				ref, err := s.registerMainDocumentRetentionRef(ctx, existing.Entry.StorageEntryID, normalized)
				if err != nil {
					return EntryDetail{}, err
				}
				existing.PhysicalRefs = append(existing.PhysicalRefs, ref)
				retentionRefID = ref.StoragePhysicalRefID
				addedRetentionRef = true
			}
			if retentionRefID == "" {
				retentionRefID = latestAvailablePhysicalRefID(existing.PhysicalRefs)
			}
			if existing.Entry.RetentionState == RetentionStatePending || addedRetentionRef {
				snapshot, err := s.CreateRetentionSnapshot(ctx, RetentionSnapshotInput{
					StorageEntryID:       existing.Entry.StorageEntryID,
					StoragePhysicalRefID: retentionRefID,
					PolicyKey:            "main_documents.accepted_snapshot",
					RetentionState:       RetentionStateSnapshot,
					Metadata:             mainDocumentRetentionMetadata(normalized),
				})
				if err != nil {
					return EntryDetail{}, err
				}
				existing.Entry = snapshot.Entry
			}
			if mainDocumentObservationNeedsUpdate(existing.Entry, normalized, viewPath) {
				entry, err := s.updateMainDocumentObservation(ctx, existing.Entry.StorageEntryID, normalized, viewPath)
				if err != nil {
					return EntryDetail{}, err
				}
				existing.Entry = entry
			}
			return existing, nil
		}
	case existingErr != nil && !isStorageEntryNotFound(existingErr):
		return EntryDetail{}, existingErr
	}

	size := normalized.FileSizeBytes
	metadata, err := mainDocumentMetadata(normalized)
	if err != nil {
		return EntryDetail{}, err
	}
	entry, err := s.RegisterEntry(ctx, RegisterEntryInput{
		StorageEntryID:     normalized.StorageEntryID,
		StorageClass:       StorageClassMainDocument,
		SourceArea:         SourceAreaMainDocuments,
		OriginNodeKey:      normalized.NodeKey,
		LogicalPath:        normalized.RelativePath,
		OriginalSourcePath: normalized.RelativePath,
		CurrentViewPath:    viewPath,
		ChecksumAlgorithm:  normalized.ChecksumAlgorithm,
		ChecksumHex:        normalized.ChecksumValue,
		SizeBytes:          &size,
		FileClass:          ClassifyPath(normalized.RelativePath, ""),
		ProcessingState:    ProcessingStateMetadataOnly,
		AvailabilityState:  AvailabilityStateAvailable,
		RetentionState:     RetentionStatePending,
		Metadata:           metadata,
	})
	if err != nil {
		return EntryDetail{}, err
	}
	refMetadata, err := mainDocumentPhysicalRefMetadata(normalized)
	if err != nil {
		return EntryDetail{}, err
	}
	ref, err := s.RegisterPhysicalRef(ctx, RegisterPhysicalRefInput{
		StoragePhysicalRefID: normalized.StoragePhysicalRefID,
		StorageEntryID:       entry.StorageEntryID,
		RefKind:              PhysicalRefKindLocalPath,
		URI:                  normalized.PhysicalPath,
		NodeKey:              normalized.NodeKey,
		ContentAddress:       contentAddress(normalized.ChecksumAlgorithm, normalized.ChecksumValue),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             refMetadata,
	})
	if err != nil {
		return EntryDetail{}, err
	}
	refs := []PhysicalRef{ref}
	retentionRefID := ref.StoragePhysicalRefID
	if normalized.RetentionPath != "" {
		retentionRef, err := s.registerMainDocumentRetentionRef(ctx, entry.StorageEntryID, normalized)
		if err != nil {
			return EntryDetail{}, err
		}
		refs = append(refs, retentionRef)
		retentionRefID = retentionRef.StoragePhysicalRefID
	}
	snapshot, err := s.CreateRetentionSnapshot(ctx, RetentionSnapshotInput{
		StorageEntryID:       entry.StorageEntryID,
		StoragePhysicalRefID: retentionRefID,
		PolicyKey:            "main_documents.accepted_snapshot",
		RetentionState:       RetentionStateSnapshot,
		Metadata:             mainDocumentRetentionMetadata(normalized),
	})
	if err != nil {
		return EntryDetail{}, err
	}
	return EntryDetail{Entry: snapshot.Entry, PhysicalRefs: refs}, nil
}

func (s Service) registerMainDocumentRetentionRef(ctx context.Context, storageEntryID string, input MainDocumentInput) (PhysicalRef, error) {
	metadata, err := mainDocumentRetentionPhysicalRefMetadata(input)
	if err != nil {
		return PhysicalRef{}, err
	}
	return s.RegisterPhysicalRef(ctx, RegisterPhysicalRefInput{
		StoragePhysicalRefID: input.RetentionPhysicalRefID,
		StorageEntryID:       storageEntryID,
		RefKind:              PhysicalRefKindRetentionPayload,
		URI:                  input.RetentionPath,
		NodeKey:              input.NodeKey,
		ContentAddress:       contentAddress(input.ChecksumAlgorithm, input.ChecksumValue),
		Status:               PhysicalRefStatusAvailable,
		Metadata:             metadata,
	})
}

func (s Service) updateMainDocumentObservation(ctx context.Context, storageEntryID string, input MainDocumentInput, viewPath string) (Entry, error) {
	metadata, err := mainDocumentMetadata(input)
	if err != nil {
		return Entry{}, err
	}
	return scanEntry(s.DB.QueryRowContext(ctx, `
		UPDATE storage.storage_entries
		SET origin_node_key = $2,
		    logical_path = $3,
		    original_source_path = $4,
		    current_view_path = $5,
		    metadata = $6,
		    updated_at = now(),
		    deleted_at = NULL
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
	`, storageEntryID, input.NodeKey, input.RelativePath, input.RelativePath, viewPath, metadata))
}

func mainDocumentObservationNeedsUpdate(entry Entry, input MainDocumentInput, viewPath string) bool {
	if strings.TrimSpace(entry.OriginNodeKey) != input.NodeKey {
		return true
	}
	if strings.Trim(entry.LogicalPath, "/") != input.RelativePath {
		return true
	}
	if strings.Trim(entry.OriginalSourcePath, "/") != input.RelativePath {
		return true
	}
	if strings.Trim(entry.CurrentViewPath, "/") != strings.Trim(viewPath, "/") {
		return true
	}
	observedModifiedAt := mainDocumentMetadataModifiedAt(entry.Metadata)
	return observedModifiedAt == nil || !observedModifiedAt.UTC().Equal(input.ModifiedAt.UTC())
}

func (s Service) ListActiveMainDocuments(ctx context.Context, limit int) ([]Entry, error) {
	filter := ListFilter{
		StorageClass:      StorageClassMainDocument,
		SourceArea:        SourceAreaMainDocuments,
		AvailabilityState: AvailabilityStateAvailable,
	}
	if limit <= 0 || limit > maxListEntriesLimit {
		entries, err := s.ListAllEntries(ctx, filter)
		if err != nil {
			return nil, err
		}
		if limit > 0 && len(entries) > limit {
			return entries[:limit], nil
		}
		return entries, nil
	}
	filter.Limit = limit
	return s.ListEntries(ctx, filter)
}

func (s Service) TombstoneMainDocument(ctx context.Context, input MainDocumentTombstoneInput) (Tombstone, error) {
	normalized, err := normalizeMainDocumentTombstoneInput(input)
	if err != nil {
		return Tombstone{}, err
	}
	storageEntryID := normalized.StorageEntryID
	if storageEntryID == "" {
		detail, err := s.InspectEntry(ctx, mainDocumentViewPath(normalized.RelativePath))
		if err != nil {
			return Tombstone{}, err
		}
		storageEntryID = detail.Entry.StorageEntryID
	}
	metadata, err := mainDocumentTombstoneMetadata(normalized)
	if err != nil {
		return Tombstone{}, err
	}
	tombstone, err := s.CreateTombstone(ctx, TombstoneInput{
		StorageEntryID: storageEntryID,
		TombstoneKind:  normalized.TombstoneKind,
		Reason:         normalized.Reason,
		CreatedBy:      normalized.CreatedBy,
		Metadata:       metadata,
		MarkEntry:      true,
	})
	if err != nil {
		return Tombstone{}, err
	}
	if err := s.markMainDocumentLocalRefsMissing(ctx, storageEntryID); err != nil {
		return Tombstone{}, err
	}
	return tombstone, nil
}

func normalizeMainDocumentInput(input MainDocumentInput) (MainDocumentInput, error) {
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	input.StoragePhysicalRefID = strings.TrimSpace(input.StoragePhysicalRefID)
	input.RetentionPhysicalRefID = strings.TrimSpace(input.RetentionPhysicalRefID)
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	if input.NodeKey == "" {
		input.NodeKey = "main"
	}
	input.BackingRoot = NormalizeSourcePath(input.BackingRoot)
	relativePath, err := NormalizeLogicalPath(input.RelativePath)
	if err != nil {
		return MainDocumentInput{}, fmt.Errorf("%w: relative_path is invalid: %v", ErrInvalid, err)
	}
	input.RelativePath = relativePath
	input.PhysicalPath = NormalizeSourcePath(input.PhysicalPath)
	if input.PhysicalPath == "" {
		return MainDocumentInput{}, fmt.Errorf("%w: physical_path is required", ErrInvalid)
	}
	input.RetentionPath = NormalizeSourcePath(input.RetentionPath)
	if input.FileSizeBytes < 0 {
		return MainDocumentInput{}, fmt.Errorf("%w: file_size_bytes cannot be negative", ErrInvalid)
	}
	input.ChecksumAlgorithm = strings.ToLower(strings.TrimSpace(input.ChecksumAlgorithm))
	input.ChecksumValue = strings.ToLower(strings.TrimSpace(input.ChecksumValue))
	if input.ChecksumValue != "" && input.ChecksumAlgorithm == "" {
		return MainDocumentInput{}, fmt.Errorf("%w: checksum_algorithm is required when checksum_value is set", ErrInvalid)
	}
	if input.FilesystemObservation != nil {
		if input.FilesystemObservation.SourceModifiedAt != nil {
			value := input.FilesystemObservation.SourceModifiedAt.UTC()
			input.SourceModifiedAt = &value
			input.SourceModifiedBasis = input.FilesystemObservation.SourceModifiedBasis
		}
		if input.FilesystemObservation.SourceCreatedAt != nil {
			value := input.FilesystemObservation.SourceCreatedAt.UTC()
			input.SourceCreatedAt = &value
			input.SourceCreatedBasis = input.FilesystemObservation.SourceCreatedBasis
		}
	}
	if input.SourceModifiedAt == nil && !input.ModifiedAt.IsZero() {
		value := input.ModifiedAt.UTC()
		input.SourceModifiedAt = &value
	}
	if input.SourceModifiedAt != nil {
		value := input.SourceModifiedAt.UTC()
		input.SourceModifiedAt = &value
		input.SourceModifiedBasis = firstSourceTimeBasis(input.SourceModifiedBasis, filesystemmeta.SourceTimeBasisFilesystemMtime)
	}
	if input.SourceCreatedAt != nil {
		value := input.SourceCreatedAt.UTC()
		input.SourceCreatedAt = &value
		input.SourceCreatedBasis = firstSourceTimeBasis(input.SourceCreatedBasis, filesystemmeta.SourceTimeBasisFilesystemBirthtime)
	}
	if input.ModifiedAt.IsZero() && input.SourceModifiedAt != nil {
		input.ModifiedAt = input.SourceModifiedAt.UTC()
	} else if input.ModifiedAt.IsZero() {
		input.ModifiedAt = time.Now().UTC()
	} else {
		input.ModifiedAt = input.ModifiedAt.UTC()
	}
	if input.ImportedAt.IsZero() {
		input.ImportedAt = time.Now().UTC()
	} else {
		input.ImportedAt = input.ImportedAt.UTC()
	}
	if !input.RetentionVerifiedAt.IsZero() {
		input.RetentionVerifiedAt = input.RetentionVerifiedAt.UTC()
	} else if input.RetentionPath != "" {
		input.RetentionVerifiedAt = input.ImportedAt
	}
	return input, nil
}

func normalizeMainDocumentTombstoneInput(input MainDocumentTombstoneInput) (MainDocumentTombstoneInput, error) {
	input.StorageEntryID = strings.TrimSpace(input.StorageEntryID)
	input.RelativePath = strings.TrimSpace(input.RelativePath)
	if input.StorageEntryID == "" {
		relativePath, err := NormalizeLogicalPath(input.RelativePath)
		if err != nil {
			return MainDocumentTombstoneInput{}, fmt.Errorf("%w: relative_path is invalid: %v", ErrInvalid, err)
		}
		input.RelativePath = relativePath
	}
	input.TombstoneKind = strings.TrimSpace(input.TombstoneKind)
	if input.TombstoneKind == "" {
		input.TombstoneKind = TombstoneKindSourceDeleted
	}
	if err := requireValid("tombstone_kind", input.TombstoneKind, ValidTombstoneKind); err != nil {
		return MainDocumentTombstoneInput{}, err
	}
	input.Reason = strings.TrimSpace(input.Reason)
	input.CreatedBy = strings.TrimSpace(input.CreatedBy)
	if input.TombstonedAt.IsZero() {
		input.TombstonedAt = time.Now().UTC()
	} else {
		input.TombstonedAt = input.TombstonedAt.UTC()
	}
	return input, nil
}

func MainDocumentViewPath(relativePath string) string {
	return path.Join("main", "Documents", relativePath)
}

func mainDocumentViewPath(relativePath string) string {
	return MainDocumentViewPath(relativePath)
}

func mainDocumentMetadata(input MainDocumentInput) (json.RawMessage, error) {
	payload := map[string]any{
		"schema_version":         "storage.main_document.metadata.v0.6",
		"source":                 "mainstorage.importer",
		"node_key":               input.NodeKey,
		"backing_root":           input.BackingRoot,
		"relative_path":          input.RelativePath,
		"physical_path":          input.PhysicalPath,
		"retention_path":         input.RetentionPath,
		"modified_at":            input.ModifiedAt.Format(time.RFC3339Nano),
		"imported_at":            input.ImportedAt.Format(time.RFC3339Nano),
		"retention_verified_at":  optionalTimeString(input.RetentionVerifiedAt),
		"import_state":           "accepted",
		"retention_snapshot":     "accepted",
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(payload, input.FilesystemObservation, input.SourceModifiedAt, input.SourceCreatedAt, input.SourceModifiedBasis, input.SourceCreatedBasis)
	return json.Marshal(payload)
}

func mainDocumentMetadataModifiedAt(metadata json.RawMessage) *time.Time {
	if len(metadata) == 0 {
		return nil
	}
	var payload struct {
		SourceModifiedAt string `json:"source_modified_at"`
		ModifiedAt       string `json:"modified_at"`
	}
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return nil
	}
	value := firstNonEmptyLane(payload.SourceModifiedAt, payload.ModifiedAt)
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func mainDocumentPhysicalRefMetadata(input MainDocumentInput) (json.RawMessage, error) {
	payload := map[string]any{
		"schema_version":         "storage.main_document_physical_ref.metadata.v0.6",
		"source":                 "mainstorage.importer",
		"relative_path":          input.RelativePath,
		"imported_at":            input.ImportedAt.Format(time.RFC3339Nano),
		"import_state":           "accepted",
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(payload, input.FilesystemObservation, input.SourceModifiedAt, input.SourceCreatedAt, input.SourceModifiedBasis, input.SourceCreatedBasis)
	return json.Marshal(payload)
}

func mainDocumentRetentionPhysicalRefMetadata(input MainDocumentInput) (json.RawMessage, error) {
	payload := map[string]any{
		"schema_version":         "storage.main_document_retention_payload.metadata.v0.6.5",
		"source":                 "mainstorage.importer",
		"relative_path":          input.RelativePath,
		"retention_path":         input.RetentionPath,
		"retention_verified_at":  optionalTimeString(input.RetentionVerifiedAt),
		"checksum":               contentAddress(input.ChecksumAlgorithm, input.ChecksumValue),
		"imported_at":            input.ImportedAt.Format(time.RFC3339Nano),
		"import_state":           "accepted",
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(payload, input.FilesystemObservation, input.SourceModifiedAt, input.SourceCreatedAt, input.SourceModifiedBasis, input.SourceCreatedBasis)
	return json.Marshal(payload)
}

func mainDocumentRetentionMetadata(input MainDocumentInput) json.RawMessage {
	metadata := map[string]any{
		"schema_version":         "storage.main_document_retention.v0.6",
		"source":                 "mainstorage.importer",
		"relative_path":          input.RelativePath,
		"physical_path":          input.PhysicalPath,
		"retention_path":         input.RetentionPath,
		"checksum":               contentAddress(input.ChecksumAlgorithm, input.ChecksumValue),
		"imported_at":            input.ImportedAt.Format(time.RFC3339Nano),
		"retention_verified_at":  optionalTimeString(input.RetentionVerifiedAt),
		"import_state":           "accepted",
		"filesystem_observation": input.FilesystemObservation,
	}
	addSourceTimestampMetadata(metadata, input.FilesystemObservation, input.SourceModifiedAt, input.SourceCreatedAt, input.SourceModifiedBasis, input.SourceCreatedBasis)
	payload, err := json.Marshal(metadata)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func mainDocumentTombstoneMetadata(input MainDocumentTombstoneInput) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"schema_version":  "storage.main_document_tombstone.metadata.v0.6.3",
		"source":          "mainstorage.reconcile",
		"relative_path":   input.RelativePath,
		"tombstoned_at":   input.TombstonedAt.Format(time.RFC3339Nano),
		"tombstone_kind":  input.TombstoneKind,
		"retention_state": "preserve_existing_snapshots",
	})
}

func (s Service) markMainDocumentLocalRefsMissing(ctx context.Context, storageEntryID string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE storage.storage_physical_refs
		SET status = $2,
		    updated_at = now()
		WHERE storage_entry_id = $1
		  AND ref_kind = $3
		  AND status = $4
	`, storageEntryID, PhysicalRefStatusMissing, PhysicalRefKindLocalPath, PhysicalRefStatusAvailable)
	return err
}

func isStorageEntryNotFound(err error) bool {
	return err == sql.ErrNoRows
}

func int64PtrEqual(value *int64, want int64) bool {
	return value != nil && *value == want
}

func optionalTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
