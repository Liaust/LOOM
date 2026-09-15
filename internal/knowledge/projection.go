package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/storagecatalog"
)

const defaultProjectionSourceLimit = 5000

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
	limit := input.Limit
	if limit <= 0 {
		limit = defaultProjectionSourceLimit
	}
	// One extra row lets automatic refresh refuse a truncated inventory before
	// the materializer prunes anything from the generated view.
	if limit > maxListKnowledgeObjectsLimit+1 {
		limit = maxListKnowledgeObjectsLimit + 1
	}
	rows, err := s.db.QueryContext(ctx, `
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
		         o.knowledge_object_id
		LIMIT $9`,
		SourceRootStatusActive,
		storagecatalog.FileClassDirectory,
		storagecatalog.PhysicalRefKindLocalPath,
		storagecatalog.PhysicalRefKindDropzoneFile,
		storagecatalog.PhysicalRefKindLaneFile,
		storagecatalog.PhysicalRefKindRetentionPayload,
		storagecatalog.PhysicalRefKindArchiveFile,
		storagecatalog.PhysicalRefKindBackupArtifact,
		limit)
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
