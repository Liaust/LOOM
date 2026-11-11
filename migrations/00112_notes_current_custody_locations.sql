-- +goose Up
-- Historical transitions remain immutable. Only the current read projection
-- follows the completed custody location; a prepared move still fails closed.
CREATE OR REPLACE VIEW knowledge.notes_object_custody AS
SELECT o.knowledge_object_id, o.notes_source_root_id,
       COALESCE(e.to_state, 'active') AS source_lifecycle,
       COALESCE(t.original_path, o.source_path) AS original_path,
       CASE WHEN e.to_state='archived' THEN
         storage.resolve_custody_path('workspace:'||t.archive_operation_id,t.canonical_path)
       ELSE COALESCE(t.canonical_path, o.source_path) END AS canonical_path,
       e.workspace_kind, e.object_id AS workspace_object_id, e.slug,
       t.workspace_relative_path, t.archive_operation_id,
       t.workspace_lifecycle_event_id, t.project_event_id, t.previous_event_id,
       t.manifest_digest, t.transition_digest,
       archived.occurred_at AS archived_at, e.occurred_at
FROM knowledge.knowledge_objects o
LEFT JOIN knowledge.notes_current_custody c USING (knowledge_object_id)
LEFT JOIN knowledge.notes_custody_transitions t
 ON t.knowledge_object_id=c.knowledge_object_id AND t.workspace_lifecycle_event_id=c.workspace_lifecycle_event_id
LEFT JOIN storage.workspace_lifecycle_events e ON e.workspace_lifecycle_event_id=t.workspace_lifecycle_event_id
LEFT JOIN storage.workspace_lifecycle_events archived ON archived.workspace_archive_operation_id=t.archive_operation_id;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'retain current custody resolution while relocated Notes exist; downgrade refused';
END $$;
-- +goose StatementEnd
