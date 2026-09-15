package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const (
	defaultListKnowledgeObjectsLimit = 50
	maxListKnowledgeObjectsLimit     = 5000
)

type KnowledgeObjectFilter struct {
	SourceLifecycle   SourceLifecycleFilter `json:"source_lifecycle,omitempty"`
	SourceCategory    string                `json:"source_category,omitempty"`
	NotesSourceRootID string                `json:"notes_source_root_id,omitempty"`
	ProjectID         string                `json:"project_id,omitempty"`
	SourceNodeKey     string                `json:"source_node_key,omitempty"`
	ProcessingState   string                `json:"processing_state,omitempty"`
	IncludeDeleted    bool                  `json:"include_deleted,omitempty"`
	Limit             int                   `json:"limit,omitempty"`
	Offset            int                   `json:"offset,omitempty"`
}

func (s Store) ListKnowledgeObjects(ctx context.Context, filter KnowledgeObjectFilter) ([]KnowledgeObject, error) {
	lifecycle, err := NormalizeSourceLifecycleFilter(filter.SourceLifecycle)
	if err != nil {
		return nil, err
	}
	if err := validateSourceCategory(filter.SourceCategory); err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	if filter.Limit <= 0 {
		filter.Limit = defaultListKnowledgeObjectsLimit
	} else if filter.Limit > maxListKnowledgeObjectsLimit {
		filter.Limit = maxListKnowledgeObjectsLimit
	}
	if filter.Offset < 0 {
		return nil, fmt.Errorf("%w: offset cannot be negative", ErrInvalid)
	}
	clauses := []string{visibleNotesCustodyObjectSQL("knowledge_objects", true), notesLifecycleSelectionSQL("knowledge_objects", lifecycle), visibleNotesKnowledgeRelativePathSQL("relative_path")}
	args := []any{}
	if filter.SourceCategory != "" {
		args = append(args, filter.SourceCategory)
		clauses = append(clauses, fmt.Sprintf("EXISTS (SELECT 1 FROM knowledge.notes_source_roots category_root WHERE category_root.notes_source_root_id = knowledge_objects.notes_source_root_id AND (%s) = $%d)", sourceCategorySQL("category_root.root_kind"), len(args)))
	}
	if strings.TrimSpace(filter.NotesSourceRootID) != "" {
		args = append(args, strings.TrimSpace(filter.NotesSourceRootID))
		clauses = append(clauses, fmt.Sprintf("notes_source_root_id = $%d", len(args)))
	}
	if strings.TrimSpace(filter.ProjectID) != "" {
		args = append(args, strings.TrimSpace(filter.ProjectID))
		clauses = append(clauses, fmt.Sprintf("project_id = $%d", len(args)))
	}
	if strings.TrimSpace(filter.SourceNodeKey) != "" {
		args = append(args, strings.TrimSpace(filter.SourceNodeKey))
		clauses = append(clauses, fmt.Sprintf("source_node_key = $%d", len(args)))
	}
	if strings.TrimSpace(filter.ProcessingState) != "" {
		args = append(args, strings.TrimSpace(filter.ProcessingState))
		clauses = append(clauses, fmt.Sprintf("processing_state = $%d", len(args)))
	}
	if !filter.IncludeDeleted {
		clauses = append(clauses, "deleted_at IS NULL")
	}
	args = append(args, filter.Limit)
	limitArg := len(args)
	query := `SELECT ` + knowledgeObjectReadColumns() + `
		FROM knowledge.knowledge_objects
		WHERE ` + strings.Join(clauses, " AND ") + fmt.Sprintf(`
		ORDER BY updated_at DESC, relative_path, knowledge_object_id
		LIMIT $%d`, limitArg)
	if filter.Offset > 0 {
		args = append(args, filter.Offset)
		query += fmt.Sprintf(" OFFSET $%d", len(args))
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := []KnowledgeObject{}
	for rows.Next() {
		object, err := scanReadableKnowledgeObject(rows)
		if err != nil {
			return nil, err
		}
		objects = append(objects, object)
	}
	return objects, rows.Err()
}

func (s Store) GetKnowledgeObject(ctx context.Context, ref string) (KnowledgeObject, error) {
	if s.db == nil {
		return KnowledgeObject{}, fmt.Errorf("knowledge store is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return KnowledgeObject{}, fmt.Errorf("%w: knowledge object ref is required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+`
		FROM knowledge.knowledge_objects
		WHERE (knowledge_object_id = $1
		   OR storage_entry_id = $1
		   OR relative_path = $1)
		 AND `+visibleNotesKnowledgeObjectSQL("knowledge_objects")+`
		ORDER BY
			CASE
				WHEN knowledge_object_id = $1 THEN 0
				WHEN storage_entry_id = $1 THEN 1
				ELSE 2
			END,
			updated_at DESC
		LIMIT 1`, ref)
	return scanKnowledgeObject(row)
}

func (s Store) UpsertKnowledgeObject(ctx context.Context, object KnowledgeObject) (KnowledgeObject, error) {
	if s.db == nil {
		return KnowledgeObject{}, fmt.Errorf("knowledge store is not configured")
	}
	if err := ValidateKnowledgeObject(object); err != nil {
		return KnowledgeObject{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeObject{}, err
	}
	defer tx.Rollback()
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return KnowledgeObject{}, err
	}
	if err := requireNotesCustodyWriteTx(ctx, tx, object); err != nil {
		return KnowledgeObject{}, err
	}

	var existingID string
	err = tx.QueryRowContext(ctx, `
		SELECT knowledge_object_id
		FROM knowledge.knowledge_objects
		WHERE notes_source_root_id = $1
		  AND relative_path = $2
		ORDER BY CASE WHEN deleted_at IS NULL THEN 0 ELSE 1 END, updated_at DESC
		LIMIT 1
	`, object.NotesSourceRootID, object.RelativePath).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return KnowledgeObject{}, err
	}
	var out KnowledgeObject
	var contentChanged bool
	if existingID == "" {
		out, err = insertKnowledgeObjectTx(ctx, tx, object)
		contentChanged = true
	} else {
		existing, scanErr := scanKnowledgeObject(tx.QueryRowContext(ctx, `SELECT `+knowledgeObjectColumns()+`
			FROM knowledge.knowledge_objects
			WHERE knowledge_object_id = $1
			FOR UPDATE`, existingID))
		if scanErr != nil {
			return KnowledgeObject{}, scanErr
		}
		if err := requireNotesCustodyWriteTx(ctx, tx, existing); err != nil {
			return KnowledgeObject{}, err
		}
		contentChanged = knowledgeObjectContentChanged(existing, object)
		object.KnowledgeObjectID = existingID
		object = preserveKnowledgeObjectProcessing(existing, object)
		out, err = updateKnowledgeObjectTx(ctx, tx, object)
	}
	if err != nil {
		return KnowledgeObject{}, err
	}
	if err = replaceKnowledgeMetadataSearchDocumentForObjectTx(ctx, tx, out); err != nil {
		return KnowledgeObject{}, err
	}
	if contentChanged {
		if err = markEmbeddingObjectChangedTx(ctx, tx, out.KnowledgeObjectID, time.Now().UTC()); err != nil {
			return KnowledgeObject{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return KnowledgeObject{}, err
	}
	return out, nil
}

func knowledgeObjectContentChanged(existing, next KnowledgeObject) bool {
	if strings.TrimSpace(existing.SourceRevision) != strings.TrimSpace(next.SourceRevision) {
		return true
	}
	if strings.TrimSpace(existing.SourceHash) != strings.TrimSpace(next.SourceHash) {
		return true
	}
	if (existing.DeletedAt == nil) != (next.DeletedAt == nil) {
		return true
	}
	return false
}

func (s Store) ListStorageEntriesForNotesRoots(ctx context.Context, includeDeleted bool) ([]storagecatalog.Entry, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	catalog := storagecatalog.NewService(s.db)
	notes, err := catalog.ListAllEntries(ctx, storagecatalog.ListFilter{
		SourceArea:     storagecatalog.SourceAreaNotes,
		IncludeDeleted: includeDeleted,
	})
	if err != nil {
		return nil, err
	}
	projects, err := catalog.ListAllEntries(ctx, storagecatalog.ListFilter{
		SourceArea:     storagecatalog.SourceAreaProjects,
		IncludeDeleted: includeDeleted,
	})
	if err != nil {
		return nil, err
	}
	expanded, err := catalog.ListAllEntries(ctx, storagecatalog.ListFilter{
		SourceArea: storagecatalog.SourceAreaExternalWatchedRoot, IncludeDeleted: includeDeleted,
	})
	if err != nil {
		return nil, err
	}
	return append(append(notes, projects...), expanded...), nil
}

func (s Store) ListSyncedObjectEntriesForNotesRoots(ctx context.Context, sourceRootID string) ([]SyncedObjectEntry, error) {
	return s.listSyncedNotesPage(ctx, sourceRootID, [3]string{}, 0)
}

func (s Store) listSyncedNotesPage(ctx context.Context, sourceRootID string, after [3]string, limit int) ([]SyncedObjectEntry, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	clauses := []string{
		"nsr.root_kind IN ('box_notes', 'box_topics', 'box_library', 'project_notes', 'project_material')",
		"nsr.status = 'active'",
		notesSyncedBoxOriginSQL("nsr", "ov", "f", "sr"),
		notesSyncedCurrentSourceSQL("ss", "o", "ov", "f"),
		"f.index_policy IS DISTINCT FROM 'private_no_index'",
		"o.status = 'active'",
		"o.object_type = 'file'",
		"ov.status = 'active'",
		"sr.replicated_kind = 'object_version'",
		"sr.freshness_state = 'fresh'",
		`NOT EXISTS (SELECT 1 FROM sync.replicas preferred_replica
		 WHERE preferred_replica.replicated_kind = 'object_version'
		 AND preferred_replica.replicated_id = ov.object_version_id
		 AND preferred_replica.source_node_id = sr.source_node_id
		 AND preferred_replica.freshness_state = 'fresh'
		 AND preferred_replica.replica_id < sr.replica_id)`,
		`(
			COALESCE(ov.source_path, f.source_path, o.metadata->>'source_path', '') LIKE 'watched-root://' || nsr.backend_root_key || '/%'
			OR o.metadata->'metadata'->>'watched_root' = nsr.backend_root_key
		)`,
		`(
			nsr.node_id IS NULL
			OR nsr.node_id = COALESCE(ov.source_node_id, f.source_node_id, sr.source_node_id)
		)`,
		`(
			COALESCE(nsr.node_key, '') = ''
			OR nsr.node_key = COALESCE(n.node_key, '')
		)`,
	}
	args := []any{}
	if strings.TrimSpace(sourceRootID) != "" {
		args = append(args, strings.TrimSpace(sourceRootID))
		clauses = append(clauses, fmt.Sprintf("nsr.notes_source_root_id = $%d", len(args)))
	}
	if limit > 0 {
		n := len(args)
		args = append(args, after[0], after[1], after[2])
		clauses = append(clauses, fmt.Sprintf("(nsr.notes_source_root_id, ov.object_version_id, sr.replica_id) > ($%d::text, $%d::text, $%d::text)", n+1, n+2, n+3))
	}
	query := `
		SELECT
			nsr.notes_source_root_id,
			nsr.backend_root_key,
			sr.replica_id,
			sr.storage_ref,
			o.object_id,
			ov.object_version_id,
			COALESCE(b.storage_path, ''),
			COALESCE(ov.source_node_id, f.source_node_id, sr.source_node_id, ''),
			COALESCE(n.node_key, ''),
			COALESCE(nsr.project_id, ''),
			COALESCE(ov.source_path, f.source_path, o.metadata->>'source_path', ''),
			COALESCE(f.logical_name, o.name, ''),
			COALESCE(
				NULLIF(ov.metadata->>'file_class', ''),
				NULLIF(f.metadata->>'file_class', ''),
				NULLIF(o.metadata->>'file_class', ''),
				NULLIF(o.metadata->'metadata'->>'file_class', ''),
				'unknown'
			),
			COALESCE(ov.mime_type, f.mime_type, b.mime_type, ''),
			COALESCE(ov.size_bytes, b.size_bytes),
			COALESCE(ov.content_hash, b.hash_uri, sr.storage_ref, ''),
			COALESCE(f.index_policy, ''),
			COALESCE(f.raw_backup_policy, ''),
			o.metadata,
			ov.metadata,
			f.metadata,
			COALESCE(ov.synced_at, sr.updated_at, ov.created_at),
			ss.scope_key
		FROM knowledge.notes_source_roots nsr
		JOIN scopes.scopes ss ON ` + notesSyncedScopeSQL("nsr", "ss") + `
		JOIN objects.objects o
		  ON EXISTS (SELECT 1 FROM objects.object_scope_links osl
		     WHERE osl.scope_id = ss.scope_id AND osl.object_id = o.object_id AND osl.relevance_status = 'active')
		JOIN files.file_metadata f
		  ON f.object_id = o.object_id
		JOIN objects.object_versions ov
		  ON ov.object_version_id = f.latest_version_id
		 AND ov.object_id = o.object_id
		JOIN sync.replicas sr
		  ON sr.replicated_id = ov.object_version_id
		LEFT JOIN nodes.nodes n
		  ON n.node_id = COALESCE(ov.source_node_id, f.source_node_id, sr.source_node_id)
		LEFT JOIN files.blobs b
		  ON b.blob_id = ov.blob_id
		WHERE ` + strings.Join(clauses, " AND ")
	if limit > 0 {
		query += " ORDER BY nsr.notes_source_root_id, ov.object_version_id, sr.replica_id"
		args = append(args, limit)
		query += fmt.Sprintf(" LIMIT $%d::integer", len(args))
	} else {
		query += " ORDER BY nsr.backend_root_key, COALESCE(f.logical_name, o.name, ''), COALESCE(ov.synced_at, sr.updated_at, ov.created_at) DESC"
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []SyncedObjectEntry{}
	for rows.Next() {
		entry, err := scanSyncedObjectEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func insertKnowledgeObjectTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject) (KnowledgeObject, error) {
	row := tx.QueryRowContext(ctx, `
		INSERT INTO knowledge.knowledge_objects (
			knowledge_object_id, notes_source_root_id, storage_entry_id,
			source_node_id, source_node_key, project_id, source_path,
			relative_path, title, file_class, mime_type, size_bytes,
			source_hash, source_revision, processing_state, pipeline_key,
			pipeline_version, source_created_at, source_modified_at, recency_at,
			recency_basis, absolute_time_metadata, last_seen_at, last_processed_at,
			last_error_code, last_error_message, metadata, created_at, updated_at,
			deleted_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
		        $11,$12,$13,$14,$15,$16,$17,$18,$19,$20,
		        $21,$22,$23,$24,$25,$26,$27,$28,$29,$30)
		RETURNING `+knowledgeObjectColumns(),
		object.KnowledgeObjectID,
		object.NotesSourceRootID,
		nullableString(object.StorageEntryID),
		nullableString(object.SourceNodeID),
		object.SourceNodeKey,
		nullableString(object.ProjectID),
		object.SourcePath,
		object.RelativePath,
		object.Title,
		object.FileClass,
		object.MimeType,
		nullableInt64(object.SizeBytes),
		object.SourceHash,
		object.SourceRevision,
		object.ProcessingState,
		object.PipelineKey,
		object.PipelineVersion,
		nullableTime(object.SourceCreatedAt),
		nullableTime(object.SourceModifiedAt),
		object.RecencyAt,
		object.RecencyBasis,
		object.AbsoluteTimeMetadata,
		object.LastSeenAt,
		nullableTime(object.LastProcessedAt),
		object.LastErrorCode,
		object.LastErrorMessage,
		object.Metadata,
		object.CreatedAt,
		object.UpdatedAt,
		nullableTime(object.DeletedAt),
	)
	return scanKnowledgeObject(row)
}

func updateKnowledgeObjectTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject) (KnowledgeObject, error) {
	row := tx.QueryRowContext(ctx, `
		UPDATE knowledge.knowledge_objects
		SET storage_entry_id = $2,
		    source_node_id = $3,
		    source_node_key = $4,
		    project_id = $5,
		    source_path = $6,
		    title = $7,
		    file_class = $8,
		    mime_type = $9,
		    size_bytes = $10,
		    source_hash = $11,
		    source_revision = $12,
		    processing_state = $13,
		    pipeline_key = $14,
		    pipeline_version = $15,
		    source_created_at = $16,
		    source_modified_at = $17,
		    recency_at = $18,
		    recency_basis = $19,
		    absolute_time_metadata = $20,
		    last_seen_at = $21,
		    last_processed_at = $22,
		    last_error_code = $23,
		    last_error_message = $24,
		    metadata = $25,
		    updated_at = $26,
		    deleted_at = $27
		WHERE knowledge_object_id = $1
		RETURNING `+knowledgeObjectColumns(),
		object.KnowledgeObjectID,
		nullableString(object.StorageEntryID),
		nullableString(object.SourceNodeID),
		object.SourceNodeKey,
		nullableString(object.ProjectID),
		object.SourcePath,
		object.Title,
		object.FileClass,
		object.MimeType,
		nullableInt64(object.SizeBytes),
		object.SourceHash,
		object.SourceRevision,
		object.ProcessingState,
		object.PipelineKey,
		object.PipelineVersion,
		nullableTime(object.SourceCreatedAt),
		nullableTime(object.SourceModifiedAt),
		object.RecencyAt,
		object.RecencyBasis,
		object.AbsoluteTimeMetadata,
		object.LastSeenAt,
		nullableTime(object.LastProcessedAt),
		object.LastErrorCode,
		object.LastErrorMessage,
		object.Metadata,
		object.UpdatedAt,
		nullableTime(object.DeletedAt),
	)
	return scanKnowledgeObject(row)
}

func knowledgeObjectColumns() string {
	return `knowledge_object_id, notes_source_root_id, storage_entry_id,
	        source_node_id, source_node_key, project_id, source_path,
	        relative_path, title, file_class, mime_type, size_bytes,
	        source_hash, source_revision, processing_state, pipeline_key,
	        pipeline_version, source_created_at, source_modified_at, recency_at,
	        recency_basis, absolute_time_metadata, last_seen_at,
	        last_processed_at, last_error_code, last_error_message, metadata,
	        created_at, updated_at, deleted_at`
}

type knowledgeObjectScanner interface {
	Scan(dest ...any) error
}

type syncedObjectEntryScanner interface {
	Scan(dest ...any) error
}

func scanKnowledgeObject(scanner knowledgeObjectScanner) (KnowledgeObject, error) {
	var object KnowledgeObject
	var storageEntryID, sourceNodeID, projectID sql.NullString
	var sizeBytes sql.NullInt64
	var sourceCreatedAt, sourceModifiedAt, lastProcessedAt, deletedAt sql.NullTime
	var absoluteTimeMetadata, metadata []byte
	if err := scanner.Scan(
		&object.KnowledgeObjectID,
		&object.NotesSourceRootID,
		&storageEntryID,
		&sourceNodeID,
		&object.SourceNodeKey,
		&projectID,
		&object.SourcePath,
		&object.RelativePath,
		&object.Title,
		&object.FileClass,
		&object.MimeType,
		&sizeBytes,
		&object.SourceHash,
		&object.SourceRevision,
		&object.ProcessingState,
		&object.PipelineKey,
		&object.PipelineVersion,
		&sourceCreatedAt,
		&sourceModifiedAt,
		&object.RecencyAt,
		&object.RecencyBasis,
		&absoluteTimeMetadata,
		&object.LastSeenAt,
		&lastProcessedAt,
		&object.LastErrorCode,
		&object.LastErrorMessage,
		&metadata,
		&object.CreatedAt,
		&object.UpdatedAt,
		&deletedAt,
	); err != nil {
		return KnowledgeObject{}, err
	}
	object.StorageEntryID = nullStringPtr(storageEntryID)
	object.SourceNodeID = nullStringPtr(sourceNodeID)
	object.ProjectID = nullStringPtr(projectID)
	object.SizeBytes = nullInt64Ptr(sizeBytes)
	object.SourceCreatedAt = nullTimePtr(sourceCreatedAt)
	object.SourceModifiedAt = nullTimePtr(sourceModifiedAt)
	object.AbsoluteTimeMetadata = jsonObjectOrEmpty(absoluteTimeMetadata)
	object.LastProcessedAt = nullTimePtr(lastProcessedAt)
	object.DeletedAt = nullTimePtr(deletedAt)
	object.Metadata = jsonObjectOrEmpty(metadata)
	object.SourceContext = sourceContextFromObject(object.Metadata, object.RelativePath)
	return object, nil
}

func scanSyncedObjectEntry(scanner syncedObjectEntryScanner) (SyncedObjectEntry, error) {
	var entry SyncedObjectEntry
	var sizeBytes sql.NullInt64
	var objectMetadata, versionMetadata, fileMetadata []byte
	if err := scanner.Scan(
		&entry.NotesSourceRootID,
		&entry.BackendRootKey,
		&entry.ReplicaID,
		&entry.StorageRef,
		&entry.ObjectID,
		&entry.ObjectVersionID,
		&entry.BlobStoragePath,
		&entry.SourceNodeID,
		&entry.SourceNodeKey,
		&entry.ProjectID,
		&entry.SourcePath,
		&entry.LogicalName,
		&entry.FileClass,
		&entry.MimeType,
		&sizeBytes,
		&entry.SourceHash,
		&entry.IndexPolicy,
		&entry.RawBackupPolicy,
		&objectMetadata,
		&versionMetadata,
		&fileMetadata,
		&entry.LastSeenAt,
		&entry.ScopeKey,
	); err != nil {
		return SyncedObjectEntry{}, err
	}
	entry.SizeBytes = nullInt64Ptr(sizeBytes)
	entry.ObjectMetadata = jsonObjectOrEmpty(objectMetadata)
	entry.VersionMetadata = jsonObjectOrEmpty(versionMetadata)
	entry.FileMetadata = jsonObjectOrEmpty(fileMetadata)
	return entry, nil
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return *value
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	out := value.Time
	return &out
}
