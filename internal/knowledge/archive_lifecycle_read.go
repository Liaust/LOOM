package knowledge

import "fmt"

func notesHasCustodySQL(object string) string {
	return `EXISTS (SELECT 1 FROM knowledge.notes_custody_transitions history WHERE history.knowledge_object_id=` + object + `.knowledge_object_id)`
}

func notesCurrentCustodySQL(object string) string {
	return `SELECT transition.* FROM knowledge.notes_current_custody pointer
	 JOIN knowledge.notes_custody_transitions transition USING(knowledge_object_id,workspace_lifecycle_event_id)
	 JOIN knowledge.notes_custody_projection_receipts receipt USING(workspace_lifecycle_event_id)
	 WHERE pointer.knowledge_object_id=` + object + `.knowledge_object_id
	 AND receipt.node_id=` + object + `.source_node_id AND receipt.manifest_digest=transition.manifest_digest
	 AND receipt.project_event_id IS NOT DISTINCT FROM transition.project_event_id
	 AND NOT EXISTS (SELECT 1 FROM knowledge.notes_custody_transitions later
	 WHERE later.knowledge_object_id=pointer.knowledge_object_id AND later.previous_event_id=pointer.workspace_lifecycle_event_id)`
}

func notesReadArchiveOperationSQL(object string) string {
	return `(SELECT current.archive_operation_id FROM (` + notesCurrentCustodySQL(object) + `) current)`
}

// This exception proves only the archive-induced writer stop. Current source
// privacy/declaration/evidence and the caller's read authorization still apply.
// Projection supplies an already-authenticated operation; reads require its
// immutable current Notes receipt before supplying that operation here.
func notesProjectArchiveStopSQL(root, object, operation string) string {
	return fmt.Sprintf(`EXISTS (
	 SELECT 1 FROM projects.projects stopped_project
	 JOIN projects.project_watched_root_registrations stopped_writer ON stopped_writer.project_id=stopped_project.project_id
	 JOIN storage.workspace_archive_operations stopped_archive ON stopped_archive.workspace_archive_operation_id=%[3]s
	 WHERE stopped_project.project_id=%[1]s.project_id AND %[2]s.project_id=%[1]s.project_id
	 AND stopped_writer.project_watched_root_registration_id=%[1]s.project_watched_root_registration_id
	 AND stopped_writer.node_id=%[1]s.node_id AND stopped_writer.owner_node_key=%[1]s.node_key
	 AND stopped_writer.backend_root_key=%[1]s.backend_root_key AND stopped_writer.root_relative_path=%[1]s.root_relative_path
	 AND %[2]s.source_node_id=%[1]s.node_id AND %[2]s.source_node_key=%[1]s.node_key
	 AND %[1]s.root_kind IN ('project_notes','project_material')
	 AND %[1]s.status IN ('active','disabled')
	 AND %[1]s.metadata->>'origin'='projects.project_watched_root_registration'
	 AND %[1]s.metadata->>'project_contract_registration_id'=stopped_writer.project_contract_registration_id
	 AND (%[1]s.status='active' OR (%[1]s.metadata->>'activation_status'='disabled'
	      AND %[1]s.metadata->'registration_metadata'=stopped_writer.metadata))
	 AND stopped_archive.operation_kind='archive' AND stopped_archive.workspace_kind='project'
	 AND stopped_archive.object_id=stopped_project.project_id AND stopped_archive.phase='archive_complete'
	 AND stopped_archive.terminal_status='complete'
	 AND stopped_project.archive_state->>'schema_version'='project.physical_archive_state.v1'
	 AND stopped_project.archive_state->>'phase'='complete'
	 AND stopped_project.archive_state->>'operation_id'=stopped_archive.workspace_archive_operation_id
	 AND stopped_project.archive_state->>'workspace_plan_digest'=stopped_archive.plan_digest
	 AND stopped_project.archive_state->>'project_id'=stopped_project.project_id
	 AND stopped_project.archive_state->>'project_slug'=stopped_project.slug
	 AND stopped_project.archive_state->>'actor_id'=stopped_archive.actor_id
	 AND stopped_project.archive_state->>'reason'=stopped_archive.reason
	 AND %[1]s.source_path=(stopped_project.archive_state->>'active_path') || '/' || stopped_writer.root_relative_path
	 AND stopped_project.archive_state->>'active_path'=stopped_archive.plan_json#>>'{source,path,absolute_path}'
	 AND stopped_writer.activation_status='disabled'
	 AND stopped_writer.last_applied_by_actor_id=stopped_archive.actor_id
	 AND stopped_writer.last_applied_at >= (stopped_project.archive_state->>'started_at')::timestamptz
	 AND stopped_writer.last_applied_at <= (stopped_project.archive_state->>'archived_at')::timestamptz
	 AND stopped_writer.updated_at=stopped_writer.last_applied_at
	 AND stopped_writer.metadata=jsonb_build_object('source','project.deactivate','project_id',stopped_project.project_id,
	     'project_slug',stopped_project.slug,'facet','watched_roots','reason',stopped_archive.reason)
	)`, root, object, operation)
}

func notesEffectiveDeclarationSQL(root, object, operation string) string {
	return `(CASE WHEN ` + root + `.metadata->'registration_metadata' ? 'knowledge_source'
	 THEN ` + root + `.metadata->'registration_metadata'->'knowledge_source'
	 WHEN ` + notesProjectArchiveStopSQL(root, object, operation) + ` THEN ` + object + `.metadata->'source_root'->'knowledge_source'
	 ELSE NULL END)`
}

func visibleNotesCustodyObjectSQL(object string, legacySearch bool) string {
	return `(` + object + `.deleted_at IS NULL AND ` + notesCustodyReadCaughtUpSQL(object) + ` AND ((NOT ` + notesHasCustodySQL(object) + ` AND ` + notesKnowledgeVisibilitySQL(object, legacySearch) + `)
	 OR (EXISTS (` + notesCurrentCustodySQL(object) + `) AND ` + notesKnowledgeArchiveVisibilitySQL(object) + `)))`
}

// A path match is a conservative stop, never positive custody evidence. New
// identities admitted after a historical intent are not members of that move.
func notesCustodyReadCaughtUpSQL(object string) string {
	return `NOT EXISTS (
	 SELECT 1 FROM knowledge.notes_source_roots lag_root
	 JOIN nodes.nodes lag_node ON lag_node.node_id=lag_root.node_id
	 JOIN storage.workspace_archive_operations lag_op ON lag_op.created_at>=` + object + `.created_at
	 WHERE lag_root.notes_source_root_id=` + object + `.notes_source_root_id
	 AND lag_node.node_key=lag_root.node_key AND lag_node.node_role='main'
	 AND (starts_with(` + object + `.source_path,(CASE WHEN lag_op.operation_kind='archive'
	 THEN lag_op.plan_json#>>'{source,path,absolute_path}' ELSE lag_op.plan_json#>>'{destination,path,absolute_path}' END) || '/')
	 OR starts_with(lag_root.source_path || '/' || ` + object + `.relative_path,(CASE WHEN lag_op.operation_kind='archive'
	 THEN lag_op.plan_json#>>'{source,path,absolute_path}' ELSE lag_op.plan_json#>>'{destination,path,absolute_path}' END) || '/'))
	 AND NOT EXISTS (
	 SELECT 1 FROM storage.workspace_lifecycle_events lag_event
	 JOIN knowledge.notes_custody_transitions lag_transition USING(workspace_lifecycle_event_id)
	 JOIN knowledge.notes_custody_projection_receipts lag_receipt USING(workspace_lifecycle_event_id)
	 WHERE lag_event.workspace_archive_operation_id=lag_op.workspace_archive_operation_id
	 AND lag_op.terminal_status='complete' AND lag_op.phase IN ('archive_complete','restore_complete')
	 AND lag_transition.knowledge_object_id=` + object + `.knowledge_object_id
	 AND lag_receipt.node_id=lag_root.node_id AND lag_receipt.node_id=` + object + `.source_node_id
	 AND lag_receipt.manifest_digest=lag_transition.manifest_digest
	 AND lag_receipt.project_event_id IS NOT DISTINCT FROM lag_transition.project_event_id))`
}

func notesCustodyContextSQL(object string) string {
	return `(SELECT jsonb_build_object('source_lifecycle',custody.source_lifecycle,
	 'original_path',custody.original_path,'canonical_path',custody.canonical_path,
	 'workspace_kind',custody.workspace_kind,'workspace_object_id',custody.workspace_object_id,
	 'archive_operation_id',custody.archive_operation_id,'workspace_lifecycle_event_id',custody.workspace_lifecycle_event_id,
	 'archived_at',custody.archived_at,'occurred_at',custody.occurred_at)
	 FROM knowledge.notes_object_custody custody WHERE custody.knowledge_object_id=` + object + `.knowledge_object_id)`
}

func notesLifecycleSelectionSQL(object string, filter SourceLifecycleFilter) string {
	if filter == SourceLifecycleFilterAll {
		return "true"
	}
	lifecycle := "active"
	if filter == SourceLifecycleFilterArchived {
		lifecycle = "archived"
	}
	return `EXISTS (SELECT 1 FROM knowledge.notes_object_custody selected_custody WHERE selected_custody.knowledge_object_id=` + object + `.knowledge_object_id AND selected_custody.source_lifecycle='` + lifecycle + `')`
}

func notesReadContextSQL(root, object string) string {
	return `jsonb_build_object('root_kind',` + root + `.root_kind,'root_relative_path',` + root + `.root_relative_path,
	 'metadata',jsonb_build_object('registration_metadata',jsonb_build_object('knowledge_source',` + notesEffectiveDeclarationSQL(root, object, notesReadArchiveOperationSQL(object)) + `)),
	 'custody',` + notesCustodyContextSQL(object) + `)`
}
