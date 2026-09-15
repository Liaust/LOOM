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
	return scanReadableKnowledgeObject(s.db.QueryRowContext(ctx, `SELECT `+knowledgeObjectReadColumns()+`
	 FROM knowledge.knowledge_objects
	 WHERE (knowledge_object_id=$1 OR storage_entry_id=$1 OR relative_path=$1)
	 AND `+visibleNotesCustodyObjectSQL("knowledge_objects", true)+`
	 AND `+notesLifecycleSelectionSQL("knowledge_objects", lifecycle)+`
	 AND `+visibleNotesKnowledgeRelativePathSQL("relative_path")+`
	 ORDER BY CASE WHEN knowledge_object_id=$1 THEN 0 WHEN storage_entry_id=$1 THEN 1 ELSE 2 END,
	 updated_at DESC, knowledge_object_id LIMIT 1`, ref))
}

func knowledgeObjectReadColumns() string {
	return knowledgeObjectColumns() + `, (SELECT ` + notesReadContextSQL("read_root", "knowledge_objects") + `
	 FROM knowledge.notes_source_roots read_root WHERE read_root.notes_source_root_id=knowledge_objects.notes_source_root_id)`
}

type notesReadScannerFunc func(...any) error

func (f notesReadScannerFunc) Scan(dest ...any) error { return f(dest...) }

func scanReadableKnowledgeObject(scanner knowledgeObjectScanner) (KnowledgeObject, error) {
	var raw []byte
	object, err := scanKnowledgeObject(notesReadScannerFunc(func(dest ...any) error {
		return scanner.Scan(append(dest, &raw)...)
	}))
	if err != nil {
		return KnowledgeObject{}, err
	}
	custody, err := decodeNotesReadCustody(raw, object.SourcePath)
	if err != nil {
		return KnowledgeObject{}, err
	}
	object.NotesCustodyContext = &custody
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

func notesReadableRootSQL(root string, filter SourceLifecycleFilter, includeInactive bool) string {
	objects := `EXISTS (` + notesReadableRootObjectsSQL(root, filter) + `)`
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
