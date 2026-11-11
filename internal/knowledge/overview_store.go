package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func (s Store) ListNotesOverviewRows(ctx context.Context, input NotesOverviewInput) ([]notesOverviewObjectRow, error) {
	if _, err := NormalizeSourceLifecycleFilter(input.SourceLifecycle); err != nil {
		return nil, err
	}
	if err := validateSourceCategory(input.SourceCategory); err != nil {
		return nil, err
	}
	if s.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	clauses, args := notesOverviewRootClauses(input)
	clauses = append(clauses, notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args))
	clauses[0] = notesReadableRootWithObjectsSQL("r", input.SourceLifecycle, input.IncludeInactive,
		`EXISTS (SELECT 1 FROM object_rows visible WHERE visible.notes_source_root_id=r.notes_source_root_id)`)
	selectedRoots := "true"
	if len(clauses) > 1 {
		selectedRoots = strings.Join(clauses[1:], " AND ")
	}
	query := `
		WITH selected_roots AS MATERIALIZED (
			SELECT r.notes_source_root_id FROM knowledge.notes_source_roots r WHERE ` + selectedRoots + `
		), object_rows AS MATERIALIZED (
			SELECT o.notes_source_root_id,
			       COALESCE(NULLIF(o.file_class, ''), 'unknown') AS file_class,
			       COALESCE(NULLIF(o.processing_state, ''), 'metadata_only') AS processing_state,
			       COALESCE(
			         NULLIF(o.metadata->'extraction'->>'status', ''),
			         NULLIF(o.metadata->'text_pipeline'->>'extraction_status', ''),
			         CASE COALESCE(NULLIF(o.processing_state, ''), 'metadata_only')
			           WHEN 'text_extracted' THEN 'extracted'
			           WHEN 'chunked' THEN 'extracted'
			           WHEN 'indexed' THEN 'extracted'
			           WHEN 'embedded' THEN 'extracted'
			           WHEN 'failed' THEN 'failed'
			           WHEN 'metadata_only' THEN 'metadata_only'
			           ELSE 'not_started'
			         END
			       ) AS extraction_status,
			       o.knowledge_object_id,
			       ` + notesLifecycleSelectionSQL("o", SourceLifecycleFilterArchived) + ` AS is_archived,
			       o.size_bytes,
			       object_search.search_document_count,
			       o.last_seen_at,
			       o.last_processed_at,
			       object_search.last_indexed_at,
			       ps.status AS pipeline_status, ps.updated_at AS pipeline_updated_at, ps.failed_at AS pipeline_failed_at
			FROM knowledge.knowledge_objects o
			JOIN selected_roots USING (notes_source_root_id)
			LEFT JOIN LATERAL (
				SELECT COUNT(sd.search_document_id)::int AS search_document_count,
				       MAX(sd.indexed_at) AS last_indexed_at
				FROM knowledge.knowledge_chunks kc
				LEFT JOIN search.search_documents sd
				  ON sd.source_kind = 'knowledge_chunk'
				 AND sd.source_id = kc.knowledge_chunk_id
				WHERE kc.knowledge_object_id = o.knowledge_object_id
			) object_search ON true
			LEFT JOIN LATERAL (
				SELECT current_run.status, current_run.updated_at,
				       CASE WHEN current_run.status = 'failed' THEN current_run.updated_at END AS failed_at
				FROM (
					SELECT status, updated_at FROM knowledge.pipeline_runs
					WHERE knowledge_object_id = o.knowledge_object_id
					ORDER BY generation DESC, updated_at DESC, knowledge_pipeline_run_id DESC LIMIT 1
				) current_run
				UNION ALL
				SELECT legacy.status, legacy.updated_at, legacy.failed_at
				FROM (
					SELECT status, updated_at, failed_at FROM knowledge.pipeline_statuses
					WHERE knowledge_object_id = o.knowledge_object_id
					ORDER BY updated_at DESC, knowledge_pipeline_status_id DESC LIMIT 1
				) legacy
				WHERE NOT EXISTS (SELECT 1 FROM knowledge.pipeline_runs WHERE knowledge_object_id = o.knowledge_object_id)
			) ps ON true
			WHERE o.deleted_at IS NULL
			  AND ` + visibleNotesCustodyObjectSQL("o", true) + `
			  AND ` + notesLifecycleSelectionSQL("o", input.SourceLifecycle) + `
			  AND ` + visibleNotesKnowledgeRelativePathSQL("o.relative_path") + `
		),
		object_stats AS (
			SELECT notes_source_root_id,
			       file_class,
			       processing_state,
			       extraction_status,
			       pipeline_status,
			       COUNT(knowledge_object_id)::int AS object_count,
			       COUNT(*) FILTER (WHERE NOT is_archived)::int AS active_count,
			       COUNT(*) FILTER (WHERE is_archived)::int AS archived_count,
			       COUNT(knowledge_object_id) FILTER (WHERE file_class <> 'directory')::int AS file_count,
			       COUNT(knowledge_object_id) FILTER (WHERE file_class = 'directory')::int AS directory_count,
			       COALESCE(SUM(size_bytes), 0)::bigint AS size_bytes,
			       COALESCE(SUM(search_document_count), 0)::int AS search_document_count,
			       MAX(last_seen_at) AS last_seen_at,
			       MAX(last_processed_at) AS last_processed_at,
			       MAX(last_indexed_at) AS last_indexed_at,
			       MAX(pipeline_updated_at) AS pipeline_updated_at,
			       MAX(pipeline_failed_at) AS pipeline_failed_at
			FROM object_rows
			GROUP BY notes_source_root_id, file_class, processing_state, extraction_status, pipeline_status
		)
		SELECT r.notes_source_root_id,
		       r.root_kind,
		       COALESCE(r.node_id, ''),
		       r.node_key,
		       COALESCE(r.project_id, ''),
		       COALESCE(p.slug, ''),
		       COALESCE(p.name, ''),
		       r.backend_root_key,
		       r.display_name,
		       r.source_path,
		       r.root_relative_path,
		       r.status,
		       COALESCE(object_stats.file_class, ''),
		       COALESCE(object_stats.processing_state, ''),
		       COALESCE(object_stats.extraction_status, ''),
		       COALESCE(object_stats.object_count, 0),
		       COALESCE(object_stats.file_count, 0),
		       COALESCE(object_stats.directory_count, 0),
		       COALESCE(object_stats.size_bytes, 0),
		       COALESCE(object_stats.search_document_count, 0),
		       object_stats.last_seen_at,
		       object_stats.last_processed_at,
		       object_stats.last_indexed_at,
		       ` + sourceContextRootSQL("r") + `,
		       COALESCE(object_stats.active_count,0), COALESCE(object_stats.archived_count,0),
		       COALESCE(object_stats.pipeline_status,''), object_stats.pipeline_updated_at, object_stats.pipeline_failed_at
		FROM knowledge.notes_source_roots r
		LEFT JOIN projects.projects p
		  ON p.project_id = r.project_id
		LEFT JOIN object_stats
		  ON object_stats.notes_source_root_id = r.notes_source_root_id
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY r.node_key, r.root_kind, COALESCE(p.slug, r.project_id, ''), r.backend_root_key,
		         COALESCE(object_stats.file_class, ''), COALESCE(object_stats.processing_state, ''),
		         COALESCE(object_stats.extraction_status, '')`
	rows, err := s.queryNotesOverview(ctx, input, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notesOverviewObjectRow
	for rows.Next() {
		row, err := scanNotesOverviewObjectRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s Store) ListNotesOverviewPipelineRows(ctx context.Context, input NotesOverviewInput) ([]notesOverviewPipelineRow, error) {
	rows, err := s.ListNotesOverviewRows(ctx, input)
	if err != nil {
		return nil, err
	}
	return notesOverviewPipelineRows(rows), nil
}

// The object aggregates also partition by current pipeline status, so both
// summaries share one visibility evaluation and the same database snapshot.
func notesOverviewPipelineRows(rows []notesOverviewObjectRow) []notesOverviewPipelineRow {
	var out []notesOverviewPipelineRow
	groups := make(map[[2]string]int)
	for _, row := range rows {
		if row.PipelineStatus != "" && row.ObjectCount > 0 {
			key := [2]string{row.NotesSourceRootID, normalizeNotesPipelineStatus(row.PipelineStatus)}
			if index, ok := groups[key]; ok {
				out[index].Count += row.ObjectCount
				out[index].LastUpdatedAt = latestTimePtr(out[index].LastUpdatedAt, row.PipelineUpdatedAt)
				out[index].LastFailedAt = latestTimePtr(out[index].LastFailedAt, row.PipelineFailedAt)
				continue
			}
			groups[key] = len(out)
			out = append(out, notesOverviewPipelineRow{NotesSourceRootID: row.NotesSourceRootID,
				Status: key[1], Count: row.ObjectCount,
				LastUpdatedAt: row.PipelineUpdatedAt, LastFailedAt: row.PipelineFailedAt})
		}
	}
	return out
}

func notesOverviewRootClauses(input NotesOverviewInput) ([]string, []any) {
	clauses := []string{notesReadableRootSQL("r", input.SourceLifecycle, input.IncludeInactive)}
	args := []any{}
	if input.SourceCategory != "" {
		args = append(args, input.SourceCategory)
		clauses = append(clauses, fmt.Sprintf("(%s) = $%d", sourceCategorySQL("r.root_kind"), len(args)))
	}
	if value := strings.TrimSpace(input.NodeKey); value != "" {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("r.node_key = $%d", len(args)))
	}
	if value := strings.TrimSpace(input.ProjectID); value != "" {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf("r.project_id = $%d", len(args)))
	}
	return clauses, args
}

type notesOverviewScanner interface {
	Scan(dest ...any) error
}

func (s Store) queryNotesOverview(ctx context.Context, input NotesOverviewInput, query string, args ...any) (*sql.Rows, error) {
	if input.readTx != nil {
		return input.readTx.QueryContext(ctx, query, args...)
	}
	return s.db.QueryContext(ctx, query, args...)
}

func scanNotesOverviewObjectRow(scanner notesOverviewScanner) (notesOverviewObjectRow, error) {
	var contextRoot []byte
	var row notesOverviewObjectRow
	var lastSeenAt, lastProcessedAt, lastIndexedAt sql.NullTime
	var pipelineUpdatedAt, pipelineFailedAt sql.NullTime
	if err := scanner.Scan(
		&row.NotesSourceRootID,
		&row.RootKind,
		&row.NodeID,
		&row.NodeKey,
		&row.ProjectID,
		&row.ProjectSlug,
		&row.ProjectName,
		&row.BackendRootKey,
		&row.DisplayName,
		&row.SourcePath,
		&row.RootRelativePath,
		&row.Status,
		&row.FileClass,
		&row.ProcessingState,
		&row.ExtractionStatus,
		&row.ObjectCount,
		&row.FileCount,
		&row.DirectoryCount,
		&row.SizeBytes,
		&row.SearchDocumentCount,
		&lastSeenAt,
		&lastProcessedAt,
		&lastIndexedAt,
		&contextRoot,
		&row.LifecycleCounts.Active,
		&row.LifecycleCounts.Archived,
		&row.PipelineStatus,
		&pipelineUpdatedAt,
		&pipelineFailedAt,
	); err != nil {
		return notesOverviewObjectRow{}, err
	}
	row.LastSeenAt = nullTimePtr(lastSeenAt)
	row.SourceContext = sourceContextFromRootJSON(contextRoot, "")
	row.LastProcessedAt = nullTimePtr(lastProcessedAt)
	row.LastIndexedAt = nullTimePtr(lastIndexedAt)
	row.PipelineUpdatedAt = nullTimePtr(pipelineUpdatedAt)
	row.PipelineFailedAt = nullTimePtr(pipelineFailedAt)
	return normalizeNotesOverviewObjectRow(row), nil
}

func scanNotesOverviewPipelineRow(scanner notesOverviewScanner) (notesOverviewPipelineRow, error) {
	var row notesOverviewPipelineRow
	var lastUpdatedAt, lastFailedAt sql.NullTime
	if err := scanner.Scan(
		&row.NotesSourceRootID,
		&row.Status,
		&row.Count,
		&lastUpdatedAt,
		&lastFailedAt,
	); err != nil {
		return notesOverviewPipelineRow{}, err
	}
	row.NotesSourceRootID = strings.TrimSpace(row.NotesSourceRootID)
	row.Status = normalizeNotesPipelineStatus(row.Status)
	row.LastUpdatedAt = nullTimePtr(lastUpdatedAt)
	row.LastFailedAt = nullTimePtr(lastFailedAt)
	return row, nil
}
