package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"strings"

	"loom.local/loom/internal/filepolicy"
)

// FileAdmissionError identifies only an already-authorized managed reference.
// Unknown/private/ambiguous references keep the existing ErrNoRows refusal.
type FileAdmissionError struct {
	Reason       string
	SourceRootID string
}

func (e *FileAdmissionError) Error() string {
	switch e.Reason {
	case "source_root_blocked":
		return "managed source root is blocked"
	case "source_root_disabled":
		return "managed source root is disabled"
	case "source_authority_unavailable":
		return "managed source authority is unavailable"
	case "source_not_published":
		return "managed source is not published in Notes"
	case "custody_projection_pending":
		return "managed source lifecycle projection is pending"
	default:
		return "managed source is not readable in Notes"
	}
}

func (e *FileAdmissionError) Unwrap() error { return sql.ErrNoRows }

func fileAdmissionReason(root SourceRoot, authority bool, presence string) error {
	var reason string
	switch root.Status {
	case SourceRootStatusBlocked:
		reason = "source_root_blocked"
	case SourceRootStatusDisabled:
		reason = "source_root_disabled"
	case SourceRootStatusStale:
		reason = "source_authority_unavailable"
	case SourceRootStatusActive:
		if !authority {
			reason = "source_authority_unavailable"
		} else if presence == "missing" {
			return ErrFileSourceMissing
		} else if presence == "unavailable" {
			return ErrFileSourceUnavailable
		} else {
			reason = "source_not_published"
		}
	default:
		return sql.ErrNoRows
	}
	return &FileAdmissionError{Reason: reason, SourceRootID: root.NotesSourceRootID}
}

func (s *Service) classifyFileAdmission(ctx context.Context, ref string) error {
	args := []any{ref}
	objectSelection := notesSelectedRootSQL(notesBoxSelection(ctx), "visibility_root", &args)
	fileSelection := notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	rows, err := s.store.db.QueryContext(ctx, fileAdmissionReferenceSQL(objectSelection, fileSelection), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var root SourceRoot
	var relative, presence string
	var authority bool
	count := 0
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw, &relative, &presence, &authority); err != nil {
			return err
		}
		count++
		if count > 1 {
			return sql.ErrNoRows
		}
		if err = json.Unmarshal(raw, &root); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return s.classifyPendingFileCustody(ctx, ref)
	}
	if !fileAdmissionPathAllowed(root, relative) {
		return sql.ErrNoRows
	}
	if root.Status == SourceRootStatusActive && authority {
		// Retain the source owner's current ignore-rule/path authorization. A
		// swallowed metadata fallback denial must not become an existence oracle.
		err = s.WithReferenceNotesSource(ctx, root.NotesSourceRootID, root.NodeKey, relative, func(SourceRoot, string) error { return nil })
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, ErrNotesCustodyPaused) {
				return &FileAdmissionError{Reason: "source_authority_unavailable", SourceRootID: root.NotesSourceRootID}
			}
			return sql.ErrNoRows
		}
	}
	return fileAdmissionReason(root, authority, presence)
}

// Diagnose only an exact, non-private managed identity. This is not a source
// read grant: the normal custody reader remains fenced until its receipt exists.
func (s *Service) classifyPendingFileCustody(ctx context.Context, ref string) error {
	args := []any{ref}
	selection := notesSelectedRootSQL(notesBoxSelection(ctx), "r", &args)
	rows, err := s.store.db.QueryContext(ctx, `WITH exact AS MATERIALIZED (
	 SELECT ko.* FROM knowledge.knowledge_objects ko WHERE ko.knowledge_object_id=$1
	 OR ko.canonical_file_id=$1 OR ko.source_path=$1
	) SELECT to_jsonb(r),ko.relative_path FROM exact ko
	 JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 JOIN nodes.nodes n ON n.node_id=r.node_id AND n.node_key=r.node_key AND n.node_role='main' AND n.status='active'
	 LEFT JOIN files.file_metadata f ON f.object_id=coalesce(ko.canonical_file_id,ko.metadata#>>'{synced_object,object_id}')
	 CROSS JOIN LATERAL (
	 SELECT e.workspace_lifecycle_event_id FROM storage.workspace_archive_operations op
	 JOIN storage.workspace_lifecycle_events e USING(workspace_archive_operation_id)
	 WHERE op.terminal_status='complete' AND op.phase IN ('archive_complete','restore_complete')
	 AND op.created_at>=ko.created_at AND (
	 starts_with(ko.source_path,(CASE WHEN op.operation_kind='archive' THEN op.source_absolute_path ELSE op.destination_absolute_path END)||'/')
	 OR starts_with(r.source_path||'/'||ko.relative_path,(CASE WHEN op.operation_kind='archive' THEN op.source_absolute_path ELSE op.destination_absolute_path END)||'/'))
	 ORDER BY e.occurred_at DESC,e.workspace_lifecycle_event_id DESC LIMIT 1) latest
	 WHERE ko.deleted_at IS NULL AND r.status<>'deleted'
	 AND `+selection+`
	 AND ko.source_node_id=r.node_id AND ko.source_node_key=r.node_key
	 AND ko.project_id IS NOT DISTINCT FROM r.project_id
	 AND f.index_policy IS DISTINCT FROM 'private_no_index'
	 AND `+fileAdmissionNonPrivateSQL("ko.metadata", "ko.metadata->'file_metadata'", "ko.metadata->'object_metadata'", "ko.metadata->'version_metadata'", "ko.metadata->'storage_metadata'", "f.metadata")+`
	 AND NOT EXISTS (SELECT 1 FROM knowledge.notes_custody_projection_receipts receipt
	 WHERE receipt.node_id=r.node_id AND receipt.workspace_lifecycle_event_id=latest.workspace_lifecycle_event_id)
	 ORDER BY ko.knowledge_object_id LIMIT 2`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var root SourceRoot
	var relative string
	count := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw, &relative); err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 || !fileAdmissionPathAllowed(root, relative) {
		return sql.ErrNoRows
	}
	return &FileAdmissionError{Reason: "custody_projection_pending", SourceRootID: root.NotesSourceRootID}
}

func fileAdmissionPathAllowed(root SourceRoot, relative string) bool {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\\x00") || strings.HasPrefix(relative, "../") || ignoredNotesKnowledgeRelativePath(relative) || filepolicy.IsIndexingExcludedPath(relative) || expandedSourceExcluded(root, relative, nil) {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	switch strings.ToLower(path.Ext(relative)) {
	case ".md", ".markdown", ".txt", ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".canvas", ".base", ".wav", ".svg", ".csv", ".xlsx", ".dat", ".json":
		return true
	}
	return false
}

func fileAdmissionNonPrivateSQL(columns ...string) string {
	checks := make([]string, 0, len(columns))
	for _, column := range columns {
		checks = append(checks, "coalesce("+column+",'{}'::jsonb)->>'private_no_index' IS DISTINCT FROM 'true' AND coalesce("+column+",'{}'::jsonb)->>'index_policy' IS DISTINCT FROM 'private_no_index'")
	}
	return strings.Join(checks, " AND ")
}

func fileAdmissionReferenceSQL(selections ...string) string {
	objectSelection, fileSelection := "true", "true"
	if len(selections) == 2 {
		objectSelection, fileSelection = selections[0], selections[1]
	}
	return `WITH exact_objects AS MATERIALIZED (
	 SELECT ko.* FROM knowledge.knowledge_objects ko
	 JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 JOIN knowledge.notes_object_custody custody USING(knowledge_object_id)
	 LEFT JOIN files.file_metadata f ON f.object_id=ko.canonical_file_id
	 WHERE ko.knowledge_object_id=$1 OR ko.canonical_file_id=$1 OR ko.source_path=$1
	 OR custody.canonical_path=$1 OR f.managed_source_path=$1
	 OR (starts_with(r.source_path,'/') AND rtrim(r.source_path,'/')||'/'||ko.relative_path=$1)),
	 exact_files AS MATERIALIZED (
	 SELECT * FROM files.file_metadata WHERE object_id=$1 OR managed_source_path=$1),
	 known_references AS MATERIALIZED (
	 SELECT coalesce(ko.canonical_file_id,ko.metadata#>>'{synced_object,object_id}',ko.knowledge_object_id) AS file_id,visibility_root.notes_source_root_id,ko.relative_path,
	 coalesce(f.source_presence,'unobserved') AS presence
	 FROM exact_objects ko JOIN knowledge.notes_source_roots visibility_root USING(notes_source_root_id)
	 JOIN knowledge.notes_object_custody custody USING(knowledge_object_id)
	 LEFT JOIN files.file_metadata f ON f.object_id=coalesce(ko.canonical_file_id,ko.metadata#>>'{synced_object,object_id}')
	 WHERE ko.deleted_at IS NULL AND custody.source_lifecycle='active'
	 AND ` + objectSelection + `
	 AND ko.source_node_id=visibility_root.node_id AND ko.source_node_key=visibility_root.node_key
	 AND ko.project_id IS NOT DISTINCT FROM visibility_root.project_id
	 AND ko.metadata#>>'{source_root,source_path}'=visibility_root.source_path
	 AND ko.metadata#>>'{source_root,root_relative_path}'=visibility_root.root_relative_path
	 AND ` + notesCustodyReadCaughtUpSQL("ko") + `
	 AND (NOT ` + notesHasCustodySQL("ko") + ` OR EXISTS (` + notesCurrentCustodySQL("ko") + `))
	 AND ` + fileAdmissionNonPrivateSQL("ko.metadata", "ko.metadata->'file_metadata'", "ko.metadata->'object_metadata'", "ko.metadata->'version_metadata'", "ko.metadata->'storage_metadata'") + `
	 AND ` + notesKnowledgeSourceEvidenceSQL("ko", notesSyncedScopeSQL("visibility_root", "visibility_scope_owner"), true) + `
	 AND (visibility_root.project_id IS NULL OR EXISTS (SELECT 1 FROM projects.projects p WHERE p.project_id=visibility_root.project_id AND p.status='active'))
	 UNION
	 SELECT f.object_id,r.notes_source_root_id,substring(f.managed_source_path from length(rtrim(r.source_path,'/'))+2),f.source_presence
	 FROM exact_files f JOIN objects.objects o USING(object_id)
	 JOIN objects.object_versions v ON v.object_version_id=f.latest_version_id AND v.object_id=f.object_id
	 JOIN knowledge.notes_source_roots r ON r.node_id=f.source_node_id
	 AND starts_with(r.source_path,'/') AND starts_with(f.managed_source_path,rtrim(r.source_path,'/')||'/')
	 AND (f.source_root_key='' OR f.source_root_key=r.backend_root_key)
	 WHERE o.status='active' AND o.object_type='file' AND v.status='active'
	 AND ` + fileSelection + `
	 AND f.index_policy IS DISTINCT FROM 'private_no_index'
	 AND ` + fileAdmissionNonPrivateSQL("f.metadata", "o.metadata", "v.metadata") + `
	 AND EXISTS (SELECT 1 FROM objects.object_scope_links scope_link JOIN scopes.scopes scope_owner USING(scope_id)
	 WHERE scope_link.object_id=f.object_id AND scope_link.relevance_status='active'
	 AND ` + notesSyncedScopeSQL("r", "scope_owner") + `
	 AND ` + notesSyncedCurrentSourceSQL("scope_owner", "o", "v", "f") + `)
	 ORDER BY file_id,notes_source_root_id LIMIT 2)
	 SELECT to_jsonb(r),known.relative_path,known.presence,` + declarationEnrollmentMembershipSQL("r") + `
	 FROM known_references known JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 JOIN nodes.nodes node ON node.node_id=r.node_id AND node.node_key=r.node_key AND node.node_role='main' AND node.status='active'
	 WHERE r.status<>'deleted' AND r.root_kind IN ('box_notes','box_topics','box_library','project_notes','project_material')
	 ORDER BY known.file_id,r.notes_source_root_id LIMIT 2`
}
