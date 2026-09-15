package knowledge

import (
	"context"
	"database/sql"
	"fmt"
)

var ErrNotesCustodyPaused = fmt.Errorf("%w: Notes source custody pauses processing", ErrConflict)

// Paths only impose a conservative stop. They never grant custody or access.
// A completed local restore receipt clears the intent stop; ordinary admission
// and privacy checks remain independent, and project writers remain disabled.
func notesCustodyWriteAllowedSQL(object string) string {
	return `(NOT EXISTS (
	 SELECT 1 FROM knowledge.notes_current_custody current_custody
	 JOIN storage.workspace_lifecycle_events current_event USING(workspace_lifecycle_event_id)
	 WHERE current_custody.knowledge_object_id=` + object + `.knowledge_object_id AND current_event.to_state='archived'
	) AND NOT EXISTS (
	 SELECT 1 FROM knowledge.notes_source_roots custody_root JOIN nodes.nodes custody_node ON custody_node.node_id=custody_root.node_id
	 JOIN storage.workspace_archive_operations custody_op ON custody_op.operation_kind='archive'
	 WHERE custody_root.notes_source_root_id=` + object + `.notes_source_root_id
	 AND custody_node.node_key=custody_root.node_key AND custody_node.node_role='main'
	 AND (starts_with(` + object + `.source_path,(custody_op.plan_json#>>'{source,path,absolute_path}') || '/')
	 OR starts_with(custody_root.source_path || '/' || ` + object + `.relative_path,(custody_op.plan_json#>>'{source,path,absolute_path}') || '/'))
	 AND NOT EXISTS (
	 SELECT 1 FROM storage.workspace_archive_manifests custody_manifest
	 JOIN storage.workspace_archive_operations custody_restore ON custody_restore.workspace_archive_operation_id=custody_manifest.restore_operation_id
	 JOIN storage.workspace_lifecycle_events restored_event ON restored_event.workspace_archive_operation_id=custody_restore.workspace_archive_operation_id
	 JOIN knowledge.notes_custody_projection_receipts restored_receipt ON restored_receipt.workspace_lifecycle_event_id=restored_event.workspace_lifecycle_event_id
	 WHERE custody_manifest.workspace_archive_operation_id=custody_op.workspace_archive_operation_id
	 AND custody_restore.operation_kind='restore' AND custody_restore.phase='restore_complete' AND custody_restore.terminal_status='complete'
	 AND restored_event.to_state='active' AND restored_receipt.node_id=custody_root.node_id
	 )) AND NOT EXISTS (
	 SELECT 1 FROM knowledge.notes_source_roots project_root
	 WHERE project_root.notes_source_root_id=` + object + `.notes_source_root_id AND project_root.project_id IS NOT NULL
	 AND EXISTS (SELECT 1 FROM storage.workspace_archive_operations project_move
	 WHERE project_move.workspace_kind='project' AND project_move.object_id=project_root.project_id)
	 AND NOT (project_root.status='active' AND EXISTS (
	 SELECT 1 FROM projects.project_watched_root_registrations writer
	 JOIN projects.projects writer_project ON writer_project.project_id=writer.project_id
	 WHERE writer.project_watched_root_registration_id=project_root.project_watched_root_registration_id
	 AND writer.project_id=project_root.project_id AND writer.node_id=project_root.node_id
	 AND writer.owner_node_key=project_root.node_key AND writer.backend_root_key=project_root.backend_root_key
	 AND writer.root_relative_path=project_root.root_relative_path
	 AND writer.activation_status IN ('applied','reported') AND writer_project.status='active'))
	 ))`
}

// Caller holds the shared custody fence before acquiring any object/run locks.
func requireNotesCustodyWriteTx(ctx context.Context, tx *sql.Tx, object KnowledgeObject) error {
	var allowed bool
	err := tx.QueryRowContext(ctx, `SELECT `+notesCustodyWriteAllowedSQL("candidate")+` AND NOT EXISTS (
	 SELECT 1 FROM knowledge.knowledge_objects existing WHERE existing.storage_entry_id=$5::text
	 AND NOT `+notesCustodyWriteAllowedSQL("existing")+`)
	 FROM (SELECT $1::text AS knowledge_object_id,$2::text AS notes_source_root_id,$3::text AS source_path,
	 $4::text AS relative_path) candidate`, object.KnowledgeObjectID, object.NotesSourceRootID, object.SourcePath,
		object.RelativePath, object.StorageEntryID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotesCustodyPaused
	}
	return nil
}
