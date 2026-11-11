package knowledge

import (
	"context"
	"database/sql"
	"fmt"
)

func (s Store) getKnowledgeObjectByRef(ctx context.Context, ref string, lifecycle SourceLifecycleFilter, readable bool) (KnowledgeObject, error) {
	args := []any{ref}
	selection := notesSelectedObjectSQL(ctx, "knowledge_objects", &args)
	columns, visibility, scan := knowledgeObjectColumns(), visibleNotesKnowledgeObjectSQL("knowledge_objects"), scanKnowledgeObject
	if readable {
		columns, scan = knowledgeObjectReadColumns(), scanReadableKnowledgeObject
		visibility = visibleNotesCustodyObjectSQL("knowledge_objects", true) + " AND " + notesLifecycleSelectionSQL("knowledge_objects", lifecycle) + " AND " + visibleNotesKnowledgeRelativePathSQL("relative_path")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM knowledge.knowledge_objects
 WHERE (knowledge_object_id=$1 OR storage_entry_id=$1 OR relative_path=$1) AND `+selection+` AND `+visibility+`
 ORDER BY CASE WHEN knowledge_object_id=$1 THEN 0 WHEN storage_entry_id=$1 THEN 1 ELSE 2 END,
 updated_at DESC, knowledge_object_id LIMIT 2`, args...)
	if err != nil {
		return KnowledgeObject{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return KnowledgeObject{}, err
		}
		return KnowledgeObject{}, sql.ErrNoRows
	}
	object, err := scan(rows)
	if err != nil {
		return KnowledgeObject{}, err
	}
	if object.KnowledgeObjectID != ref && derefString(object.StorageEntryID) != ref && rows.Next() {
		return KnowledgeObject{}, fmt.Errorf("%w: relative path is ambiguous in the selected Boxes; use an exact object ID", ErrInvalid)
	}
	if err := rows.Err(); err != nil {
		return KnowledgeObject{}, err
	}
	return object, nil
}
