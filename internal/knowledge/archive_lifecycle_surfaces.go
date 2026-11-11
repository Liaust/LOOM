package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Shared roots can hold both kinds of objects. Status still describes the
// configured writer, not the custody of everything beneath the root.
type NotesLifecycleCounts struct {
	Active   int `json:"active"`
	Archived int `json:"archived"`
}

func (s Store) GetKnowledgeObjectWithLifecycle(ctx context.Context, ref string, filter SourceLifecycleFilter) (KnowledgeObject, error) {
	lifecycle, err := NormalizeSourceLifecycleFilter(filter)
	if err != nil {
		return KnowledgeObject{}, err
	}
	if s.db == nil {
		return KnowledgeObject{}, fmt.Errorf("knowledge store is not configured")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return KnowledgeObject{}, invalid("knowledge object ref is required")
	}
	return s.getKnowledgeObjectByRef(ctx, ref, lifecycle, true)
}

func knowledgeObjectReadColumns() string {
	return knowledgeObjectColumns() + `, COALESCE(canonical_file_id,''), COALESCE(lexical_version_id,''), COALESCE(semantic_version_id,''), (SELECT ` + notesReadContextSQL("read_root", "knowledge_objects") + `
	 FROM knowledge.notes_source_roots read_root WHERE read_root.notes_source_root_id=knowledge_objects.notes_source_root_id)`
}

type notesReadScannerFunc func(...any) error

func (f notesReadScannerFunc) Scan(dest ...any) error { return f(dest...) }

func scanReadableKnowledgeObject(scanner knowledgeObjectScanner) (KnowledgeObject, error) {
	var raw []byte
	var file, lexical, semantic string
	object, err := scanKnowledgeObject(notesReadScannerFunc(func(dest ...any) error {
		return scanner.Scan(append(dest, &file, &lexical, &semantic, &raw)...)
	}))
	if err != nil {
		return KnowledgeObject{}, err
	}
	custody, err := decodeNotesReadCustody(raw, object.SourcePath)
	if err != nil {
		return KnowledgeObject{}, err
	}
	object.NotesCustodyContext = &custody
	object.CanonicalFileID, object.LexicalVersionID, object.SemanticVersionID = file, lexical, semantic
	object.SourceContext = sourceContextFromRootJSON(raw, object.RelativePath)
	return object, nil
}

func notesReadableRootObjectsSQL(root string, filter SourceLifecycleFilter) string {
	return `SELECT read_object.knowledge_object_id FROM knowledge.knowledge_objects read_object
	 WHERE read_object.notes_source_root_id=` + root + `.notes_source_root_id
	 AND ` + visibleNotesCustodyObjectSQL("read_object", true) + `
	 AND ` + visibleNotesKnowledgeRelativePathSQL("read_object.relative_path") + `
	 AND ` + notesLifecycleSelectionSQL("read_object", filter)
}

// Archive authority is still authenticated by an immutable current custody
// stop. Only that root-level proof is shared; each object must prove its own
// custody, source metadata and privacy below.
func notesStatementRootAuthoritySQL(root string) string {
	stop := `EXISTS (SELECT 1 FROM knowledge.knowledge_objects authority_object
	 WHERE authority_object.notes_source_root_id=` + root + `.notes_source_root_id
	 AND ` + notesProjectArchiveStopSQL(root, "authority_object", notesReadArchiveOperationSQL("authority_object")) + `)`
	archive := stop + ` AND accepted.registration_status IN ('registered','archived')`
	return `(` + declarationLegacyNotesAuthoritySQL(root, "true", stop) + ` OR (` + declarationUnversionedRootSQL(root) + `)
	 OR ` + declarationRegistrationAuthoritySQL(root, archive) + `)`
}

func notesStatementLegacyObjectSQL(root, object string) string {
	return `(` + root + `.statement_authority AND ` + root + `.statement_declaration->>'schema_version' IN ('project.contract.v0.5','project.contract.v0.6')
	 AND ` + root + `.status='active' AND ` + root + `.root_kind='project_notes'
	 AND NOT COALESCE((` + root + `.metadata#>'{registration_metadata,knowledge_source}') ? 'schema_version',false)
	 AND NOT COALESCE((` + object + `.metadata#>'{source_root,knowledge_source}') ? 'schema_version',false)
	 AND ` + object + `.source_node_key=` + root + `.node_key AND ` + object + `.project_id=` + root + `.project_id
	 AND ` + object + `.metadata#>>'{source_root,source_path}'=` + root + `.source_path
	 AND ` + object + `.metadata#>>'{source_root,root_relative_path}'=` + root + `.root_relative_path
	 AND (EXISTS (SELECT 1 FROM projects.projects legacy_current WHERE legacy_current.project_id=` + root + `.project_id AND legacy_current.status='active')
	 OR ` + notesProjectArchiveStopSQL(root, object, notesReadArchiveOperationSQL(object)) + `))`
}

func notesStatementEnrollmentSQL(root, object string, archiveRead bool) string {
	archiveSource := "true"
	if archiveRead {
		archiveSource = `(NOT (` + notesProjectArchiveStopSQL(root, object, notesReadArchiveOperationSQL(object)) + `) OR ` +
			notesStatementWatchSourceMatchesSQL(root, object+`.metadata#>'{source_root,knowledge_source}'`, root+`.statement_declaration->'source'`) + `)`
	}
	return `(` + root + `.statement_authority AND CASE
	 WHEN ` + root + `.root_kind='project_material' AND ` + root + `.statement_declaration->>'schema_version' IN ('project.contract.v0.5','project.contract.v0.6')
	 THEN ` + object + `.metadata#>>'{source_root,knowledge_source,schema_version}'=` + root + `.statement_declaration->>'schema_version'
	 AND ` + root + `.statement_declaration#>>'{source,schema_version}'=` + root + `.statement_declaration->>'schema_version'
	 AND ` + archiveSource + ` ELSE
	 NOT COALESCE((` + object + `.metadata#>'{source_root,knowledge_source}') ? 'schema_version',false)
	 AND (COALESCE(` + root + `.statement_declaration->>'schema_version','') NOT IN ('project.contract.v0.5','project.contract.v0.6')
	 OR ` + notesStatementLegacyObjectSQL(root, object) + `) END)`
}

func notesStatementVisibleObjectSQL(object string) string {
	return `(` + object + `.deleted_at IS NULL AND ` + notesCustodyReadCaughtUpSQL(object) + ` AND CASE
	 WHEN NOT ` + notesHasCustodySQL(object) + ` THEN ` + notesKnowledgeVisibilityPolicyWithAuthoritySQL(object, true, false, true, "authorized_roots") + `
	 ELSE EXISTS (` + notesCurrentCustodySQL(object) + `) AND ` + notesKnowledgeVisibilityPolicyWithAuthoritySQL(object, false, true, true, "authorized_roots") + ` END)`
}

// Use only after a same-snapshot probe proves the exact candidate set has no
// custody history. Keep the negative history check and projection-lag fence.
func notesStatementUnmovedObjectSQL(object string) string {
	return `(` + object + `.deleted_at IS NULL AND ` + notesCustodyReadCaughtUpSQL(object) +
		` AND NOT ` + notesHasCustodySQL(object) + ` AND ` +
		notesKnowledgeVisibilityPolicyWithAuthoritySQL(object, true, false, true, "authorized_roots") + `)`
}

func notesReadableRootsQuerySQL(selection string, filter SourceLifecycleFilter, includeInactive bool) string {
	objects := "coalesce(counts.active,0)>0"
	if filter == SourceLifecycleFilterArchived {
		objects = "coalesce(counts.archived,0)>0"
	} else if filter == SourceLifecycleFilterAll {
		objects = "(coalesce(counts.active,0)+coalesce(counts.archived,0))>0"
	}
	return `WITH selected_roots AS MATERIALIZED (
	 SELECT * FROM knowledge.notes_source_roots WHERE ` + selection + `),
	 authorized_roots AS MATERIALIZED (
	 SELECT r.*,` + notesStatementRootAuthoritySQL("r") + ` AS statement_authority,
	 projects.declaration_watch_read_sources(r.project_id,r.node_id,r.backend_root_key) AS statement_read_sources,
	 (SELECT jsonb_build_object('schema_version',accepted.contract_schema_version,'source',expected.value#>'{metadata,knowledge_source}')
	 FROM projects.project_watched_root_registrations writer
	 JOIN projects.project_contract_registrations accepted USING(project_contract_registration_id)
	 LEFT JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(accepted.registration_plan_json->'watched_roots')='array'
	 THEN accepted.registration_plan_json->'watched_roots' ELSE '[]'::jsonb END) expected
	 ON expected.value->>'backend_root_key'=r.backend_root_key
	 WHERE writer.project_watched_root_registration_id=r.project_watched_root_registration_id LIMIT 1) AS statement_declaration
	 FROM selected_roots r),
	 visible_objects AS MATERIALIZED (
	 SELECT o.notes_source_root_id,custody.source_lifecycle FROM knowledge.knowledge_objects o
	 JOIN selected_roots requested USING(notes_source_root_id)
	 JOIN knowledge.notes_object_custody custody USING(knowledge_object_id)
	 WHERE ` + notesStatementVisibleObjectSQL("o") + ` AND ` + notesObservedSourcePresentSQL("o") + `
	 AND ` + visibleNotesKnowledgeRelativePathSQL("o.relative_path") + `),
	 lifecycle_counts AS MATERIALIZED (
	 SELECT notes_source_root_id,count(*) FILTER(WHERE source_lifecycle='active') AS active,
	 count(*) FILTER(WHERE source_lifecycle='archived') AS archived FROM visible_objects GROUP BY notes_source_root_id)
	 SELECT ` + sourceRootColumns() + `,jsonb_build_object('active',coalesce(counts.active,0),'archived',coalesce(counts.archived,0))
	 FROM selected_roots notes_source_roots LEFT JOIN lifecycle_counts counts USING(notes_source_root_id)
	 WHERE ` + notesReadableRootWithObjectsSQL("notes_source_roots", filter, includeInactive, objects) + `
	 ORDER BY root_kind,node_key,project_id NULLS FIRST,backend_root_key`
}

func notesReadableRootSQL(root string, filter SourceLifecycleFilter, includeInactive bool) string {
	objects := `EXISTS (` + notesReadableRootObjectsSQL(root, filter) + `)`
	return notesReadableRootWithObjectsSQL(root, filter, includeInactive, objects)
}

// Callers that already evaluated visibility can reuse those statement-local rows.
func notesReadableRootWithObjectsSQL(root string, filter SourceLifecycleFilter, includeInactive bool, objects string) string {
	if filter == SourceLifecycleFilterArchived {
		return objects
	}
	status := root + `.status='active'`
	if includeInactive {
		status = root + `.status<>'deleted'`
	}
	// An empty configured root is navigation, not proof of indexed content.
	empty := `(` + status + ` AND (` + root + `.project_id IS NULL OR EXISTS (SELECT 1 FROM projects.projects empty_project
	 WHERE empty_project.project_id=` + root + `.project_id AND empty_project.status='active'))
	 AND NOT EXISTS (SELECT 1 FROM knowledge.knowledge_objects any_object WHERE any_object.notes_source_root_id=` + root + `.notes_source_root_id))`
	return `(` + objects + ` OR ` + empty + `)`
}

func notesRootLifecycleCountsSQL(root string) string {
	return `jsonb_build_object('active',(SELECT count(*) FROM (` + notesReadableRootObjectsSQL(root, SourceLifecycleFilterActive) + `) active_objects),
	 'archived',(SELECT count(*) FROM (` + notesReadableRootObjectsSQL(root, SourceLifecycleFilterArchived) + `) archived_objects))`
}

func scanReadableSourceRoot(scanner sourceRootScanner) (SourceRoot, error) {
	var raw []byte
	root, err := scanSourceRoot(notesReadScannerFunc(func(dest ...any) error { return scanner.Scan(append(dest, &raw)...) }))
	if err != nil {
		return SourceRoot{}, err
	}
	var counts NotesLifecycleCounts
	if err := json.Unmarshal(raw, &counts); err != nil {
		return SourceRoot{}, err
	}
	root.LifecycleCounts = &counts
	return root, nil
}
