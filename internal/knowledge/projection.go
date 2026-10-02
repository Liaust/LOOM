package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/storagecatalog"
)

const projectionSourcePageSize = 256

func (s *Service) ListProjectionSources(ctx context.Context, input notesprojection.SourceListInput) ([]notesprojection.SourceObject, error) {
	if s == nil {
		return nil, fmt.Errorf("knowledge service is not configured")
	}
	return s.store.ListProjectionSources(ctx, input)
}

func (s Store) ListProjectionSources(ctx context.Context, input notesprojection.SourceListInput) ([]notesprojection.SourceObject, error) {
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	// Keep membership, custody joins and mutable ordering fields in one snapshot.
	// A cursor bounds each database fetch without OFFSET rescans or a global cap.
	// End the transaction before any payload copying or filesystem pruning.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := declareProjectionSources(ctx, tx); err != nil {
		return nil, err
	}
	sources, err := collectProjectionSources(ctx, input.Limit, func(ctx context.Context) ([]notesprojection.SourceObject, error) {
		return fetchProjectionSources(ctx, tx)
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return sources, nil
}

func declareProjectionSources(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DECLARE notes_projection_sources NO SCROLL CURSOR FOR
		SELECT o.knowledge_object_id,
		       o.notes_source_root_id,
		       root.root_kind,
		       COALESCE(NULLIF(o.source_node_key, ''), root.node_key, '') AS source_node_key,
		       o.project_id,
		       COALESCE(project.slug, '') AS project_slug,
		       o.storage_entry_id,
		       o.source_path,
		       o.relative_path,
		       o.title,
		       o.file_class,
		       o.mime_type,
		       o.size_bytes,
		       o.source_hash,
		       o.source_revision,
		       COALESCE(source_ref.ref_kind,
		         CASE WHEN COALESCE(NULLIF(o.metadata->'synced_object'->>'blob_storage_path', ''), '') <> '' THEN $3 ELSE '' END
		       ) AS source_ref_kind,
		       COALESCE(source_ref.uri, NULLIF(o.metadata->'synced_object'->>'blob_storage_path', ''), '') AS source_ref_uri,
		       COALESCE(source_ref.metadata->>'content_member', '') AS source_ref_member,
	       o.last_seen_at,
	       `+sourceContextRootSQL("root")+`, root.root_relative_path
		FROM knowledge.knowledge_objects o
		JOIN knowledge.notes_source_roots root
		  ON root.notes_source_root_id = o.notes_source_root_id
		LEFT JOIN projects.projects project
		  ON project.project_id = o.project_id
		LEFT JOIN LATERAL (
			SELECT ref.ref_kind,
			       ref.uri,
			       ref.metadata
			FROM storage.storage_physical_refs ref
			WHERE ref.storage_entry_id = o.storage_entry_id
			  AND (ref.status = '' OR ref.status = 'available')
			  AND ref.ref_kind IN ($3, $4, $5, $6, $7, $8)
			ORDER BY CASE ref.ref_kind
			         WHEN $3 THEN 0
			         WHEN $8 THEN 1
			         WHEN $6 THEN 2
			         WHEN $7 THEN 3
			         ELSE 4
			         END,
			         ref.created_at DESC,
			         ref.storage_physical_ref_id
			LIMIT 1
		) source_ref ON TRUE
		WHERE root.status = $1
		  AND o.deleted_at IS NULL
		  AND `+visibleNotesKnowledgeObjectSQL("o")+`
		  AND `+visibleNotesCustodyObjectSQL("o", true)+`
		  AND `+notesLifecycleSelectionSQL("o", SourceLifecycleFilterActive)+`
		  AND `+notesCustodyWriteAllowedSQL("o")+`
		  AND (
		    COALESCE(o.source_path, '') <> ''
		    OR source_ref.uri IS NOT NULL
		    OR COALESCE(NULLIF(o.metadata->'synced_object'->>'blob_storage_path', ''), '') <> ''
		  )
		  AND COALESCE(o.file_class, '') <> $2
		  AND `+visibleNotesKnowledgeRelativePathSQL("o.relative_path")+`
		ORDER BY root.root_kind,
		         COALESCE(NULLIF(o.source_node_key, ''), root.node_key, ''),
		         COALESCE(project.slug, o.project_id, ''),
		         o.relative_path,
		         o.knowledge_object_id`,
		SourceRootStatusActive,
		storagecatalog.FileClassDirectory,
		storagecatalog.PhysicalRefKindLocalPath,
		storagecatalog.PhysicalRefKindDropzoneFile,
		storagecatalog.PhysicalRefKindLaneFile,
		storagecatalog.PhysicalRefKindRetentionPayload,
		storagecatalog.PhysicalRefKindArchiveFile,
		storagecatalog.PhysicalRefKindBackupArtifact)
	return err
}

func fetchProjectionSources(ctx context.Context, tx *sql.Tx) ([]notesprojection.SourceObject, error) {
	rows, err := tx.QueryContext(ctx, fmt.Sprintf("FETCH FORWARD %d FROM notes_projection_sources", projectionSourcePageSize))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sources []notesprojection.SourceObject
	for rows.Next() {
		source, err := scanProjectionSource(rows)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// A short page proves exhaustion only for this single cursor, never for a series
// of independently executed queries. On any failure discard the entire inventory.
func collectProjectionSources(ctx context.Context, limit int, fetch func(context.Context) ([]notesprojection.SourceObject, error)) ([]notesprojection.SourceObject, error) {
	var sources []notesprojection.SourceObject
	seen := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(page) > projectionSourcePageSize {
			return nil, fmt.Errorf("notes projection source page exceeds fetch bound")
		}
		for _, source := range page {
			id := strings.TrimSpace(source.KnowledgeObjectID)
			if _, duplicate := seen[id]; id == "" || duplicate {
				return nil, fmt.Errorf("notes projection source inventory has empty or duplicate identity %q", id)
			}
			seen[id] = struct{}{}
			if limit > 0 && len(sources) >= limit {
				return nil, fmt.Errorf("notes projection source inventory exceeds explicit limit %d; refusing incomplete inventory", limit)
			}
			sources = append(sources, source)
		}
		if len(page) < projectionSourcePageSize {
			return sources, nil
		}
	}
}

func scanProjectionSource(scanner interface{ Scan(dest ...any) error }) (notesprojection.SourceObject, error) {
	var contextRoot []byte
	var source notesprojection.SourceObject
	var projectID, projectSlug, storageEntryID, sourcePath, title, mimeType, sourceHash, sourceRevision, sourceRefKind, sourceRefURI, sourceRefMember sql.NullString
	var sizeBytes sql.NullInt64
	if err := scanner.Scan(
		&source.KnowledgeObjectID,
		&source.NotesSourceRootID,
		&source.RootKind,
		&source.SourceNodeKey,
		&projectID,
		&projectSlug,
		&storageEntryID,
		&sourcePath,
		&source.RelativePath,
		&title,
		&source.FileClass,
		&mimeType,
		&sizeBytes,
		&sourceHash,
		&sourceRevision,
		&sourceRefKind,
		&sourceRefURI,
		&sourceRefMember,
		&source.LastSeenAt,
		&contextRoot,
		&source.RootRelativePath,
	); err != nil {
		return notesprojection.SourceObject{}, err
	}
	source.ProjectID = nullStringPtr(projectID)
	context := sourceContextFromRootJSON(contextRoot, source.RelativePath)
	source.SourceCategory, source.SourcePosture, source.Declaration = context.SourceCategory, context.SourcePosture, context.Declaration
	source.TopicKey, source.CollectionKey = context.TopicKey, context.CollectionKey
	source.ProjectSlug = strings.TrimSpace(projectSlug.String)
	source.StorageEntryID = nullStringPtr(storageEntryID)
	source.SourcePath = strings.TrimSpace(sourcePath.String)
	source.Title = strings.TrimSpace(title.String)
	source.MimeType = strings.TrimSpace(mimeType.String)
	source.SizeBytes = nullInt64Ptr(sizeBytes)
	source.SourceHash = strings.TrimSpace(sourceHash.String)
	source.SourceRevision = strings.TrimSpace(sourceRevision.String)
	source.SourceRefKind = strings.TrimSpace(sourceRefKind.String)
	source.SourceRefURI = strings.TrimSpace(sourceRefURI.String)
	source.SourceRefMember = strings.TrimSpace(sourceRefMember.String)
	return source, nil
}
