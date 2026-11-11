package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Keep the canonical identity and display path in the admission snapshot too.
func (s *Service) loadNotesFileIdentities(ctx context.Context, input NotesSearchInput, results []NotesSearchResult) error {
	if len(results) == 0 {
		return nil
	}
	ids := make([]string, 0, len(results))
	for _, hit := range results {
		ids = append(ids, hit.KnowledgeObjectID)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := s.queryNotesSearch(ctx, input, notesSearchQuery{SQL: `SELECT ko.knowledge_object_id,COALESCE(ko.canonical_file_id,ko.knowledge_object_id),COALESCE(p.name,''),
	 COALESCE(f.managed_source_path,CASE WHEN starts_with(r.source_path,'/') THEN rtrim(r.source_path,'/')||'/'||ko.relative_path ELSE ko.source_path END)
	 FROM knowledge.knowledge_objects ko LEFT JOIN projects.projects p ON p.project_id=ko.project_id
	 JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 LEFT JOIN files.file_metadata f ON f.object_id=ko.canonical_file_id
	 WHERE ko.knowledge_object_id IN (SELECT jsonb_array_elements_text($1::jsonb))`, Args: []any{string(encoded)}})
	if err != nil {
		return err
	}
	defer rows.Close()
	type identity struct{ file, project, path string }
	mapping := map[string]identity{}
	for rows.Next() {
		var id string
		var value identity
		if err := rows.Scan(&id, &value.file, &value.project, &value.path); err != nil {
			return err
		}
		mapping[id] = value
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range results {
		value := mapping[results[i].KnowledgeObjectID]
		results[i].FileID, results[i].ProjectName, results[i].filePath = value.file, value.project, value.path
	}
	return nil
}

// Withdrawal needs positive current-owner evidence, unlike the conservative
// serving fence below. Unreachable roots and unreadable files are not deletions.
func notesObservedSourceMissingSQL(object string) string {
	return `EXISTS (SELECT 1 FROM knowledge.notes_source_roots missing_root
	 JOIN watched_roots.roots missing_report ON missing_report.node_id=missing_root.node_id
	 AND missing_report.root_key=missing_root.backend_root_key
	 JOIN files.file_metadata missing_file ON missing_file.source_node_id=missing_root.node_id
	 AND missing_file.source_root_key=missing_root.backend_root_key
	 AND missing_file.source_relative_path=` + object + `.relative_path
	 AND missing_file.managed_source_path=rtrim(missing_root.source_path,'/') || '/' || ` + object + `.relative_path
	 AND missing_file.managed_source_path=` + object + `.source_path
	 AND (missing_file.object_id=COALESCE(` + object + `.canonical_file_id,` + object + `.metadata->'synced_object'->>'object_id')
	 OR ` + object + `.canonical_file_id IS NULL)
	 WHERE missing_root.notes_source_root_id=` + object + `.notes_source_root_id
	 AND missing_root.node_id=` + object + `.source_node_id AND missing_root.status='active'
	 AND missing_report.metadata->>'root_reachable'='true'
	 AND missing_file.source_generation<>'' AND missing_file.source_generation=missing_report.config_hash
	 AND missing_file.source_sequence>0 AND missing_file.source_observed_at IS NOT NULL
	 AND missing_file.source_presence='missing')`
}

// Filesystem presence is independent of retained bytes. Verified archive
// custody has its own current path and must not be withdrawn by a Box scan.
func notesObservedSourcePresentSQL(object string) string {
	rootKey := `(SELECT backend_root_key FROM knowledge.notes_source_roots identity_root
	 WHERE identity_root.notes_source_root_id=` + object + `.notes_source_root_id)`
	// Separate negative identity checks expose the existing indexes instead of
	// rescanning file metadata for every object. Keep empty legacy roots too.
	identities := []string{
		`observed_file.object_id=COALESCE(` + object + `.canonical_file_id,` + object + `.metadata->'synced_object'->>'object_id')`,
		`observed_file.managed_source_path=` + object + `.source_path`,
		`observed_file.source_relative_path=` + object + `.relative_path AND observed_file.source_root_key<>'' AND observed_file.source_root_key=` + rootKey,
		`observed_file.source_relative_path=` + object + `.relative_path AND observed_file.source_root_key='' AND ` + rootKey + `=''`,
	}
	checks := make([]string, 0, len(identities))
	for _, identity := range identities {
		checks = append(checks, `NOT EXISTS (SELECT 1 FROM files.file_metadata observed_file
	 WHERE observed_file.source_node_id=`+object+`.source_node_id
	 AND observed_file.source_presence IN ('missing','unavailable') AND `+identity+`)`)
	}
	return `(EXISTS (SELECT 1 FROM (` + notesCurrentCustodySQL(object) + `) current
	 JOIN storage.workspace_lifecycle_events e USING(workspace_lifecycle_event_id)
	 WHERE e.to_state='archived') OR ` + strings.Join(checks, " AND ") + `
	 AND NOT EXISTS (SELECT 1 FROM knowledge.notes_source_roots identity_root
	 JOIN watched_roots.roots unavailable_root ON unavailable_root.node_id=identity_root.node_id
	 AND unavailable_root.root_key=identity_root.backend_root_key
	 WHERE identity_root.notes_source_root_id=` + object + `.notes_source_root_id
	 AND unavailable_root.metadata->>'root_reachable'='false'))`
}

func linkCanonicalFileTx(ctx context.Context, tx *sql.Tx, objectID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE knowledge.knowledge_objects ko SET canonical_file_id=f.object_id
	 FROM files.file_metadata f WHERE ko.knowledge_object_id=$1
	 AND f.source_node_id=ko.source_node_id
	 AND f.object_id=ko.metadata->'synced_object'->>'object_id'
	 AND ko.canonical_file_id IS DISTINCT FROM f.object_id`, objectID)
	return err
}

// The technical Objects view consumes the same admitted current Notes body.
// Aliases are fixed by search.Service; this is composed at the application root
// to avoid a search -> knowledge package cycle.
func CurrentFileBodyDocumentSQL() string {
	return visibleNotesKnowledgeObjectSQL("ko") + ` AND
	 (sd.source_kind='knowledge_object_metadata' OR (` + notesPublishedVersionSQL("ko", "kc", "kv", false, true) + `))`
}

// Multiple memberships may retain their own receipts, but only the oldest
// eligible processing owner for a canonical file may build its current body.
func notesCanonicalBodyOwnerSQL(object string) string {
	return `(` + object + `.canonical_file_id IS NULL OR NOT EXISTS (
	 SELECT 1 FROM knowledge.knowledge_objects peer WHERE peer.canonical_file_id=` + object + `.canonical_file_id
	 AND peer.knowledge_object_id<` + object + `.knowledge_object_id AND peer.deleted_at IS NULL
	 AND ` + visibleNotesKnowledgeObjectSQL("peer") + `))`
}
