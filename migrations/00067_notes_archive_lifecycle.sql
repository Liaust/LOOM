-- +goose Up
-- Custody is per object: one archived topic must not hide a shared Box root.
CREATE TABLE knowledge.notes_custody_transitions (
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE RESTRICT,
    workspace_lifecycle_event_id text NOT NULL REFERENCES storage.workspace_lifecycle_events(workspace_lifecycle_event_id) ON DELETE RESTRICT,
    archive_operation_id text NOT NULL REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id) ON DELETE RESTRICT,
    project_event_id text REFERENCES events.events(event_id) ON DELETE RESTRICT,
    previous_event_id text,
    original_path text NOT NULL CHECK (original_path = btrim(original_path) AND octet_length(original_path) BETWEEN 1 AND 8192 AND original_path !~ '[[:cntrl:]]'),
    canonical_path text NOT NULL CHECK (canonical_path = btrim(canonical_path) AND octet_length(canonical_path) BETWEEN 1 AND 8192 AND canonical_path !~ '[[:cntrl:]]'),
    workspace_relative_path text NOT NULL CHECK (
        octet_length(workspace_relative_path) BETWEEN 1 AND 4096
        AND workspace_relative_path = btrim(workspace_relative_path)
        AND workspace_relative_path !~ '(^/|/$|\\|//|[[:cntrl:]]|(^|/)\.{1,2}(/|$))'
    ),
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[a-f0-9]{64}$'),
    transition_digest text NOT NULL CHECK (transition_digest ~ '^sha256:[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (knowledge_object_id, workspace_lifecycle_event_id),
    FOREIGN KEY (knowledge_object_id, previous_event_id)
        REFERENCES knowledge.notes_custody_transitions(knowledge_object_id, workspace_lifecycle_event_id) ON DELETE RESTRICT,
    CHECK (previous_event_id IS DISTINCT FROM workspace_lifecycle_event_id)
);

CREATE TABLE knowledge.notes_current_custody (
    knowledge_object_id text PRIMARY KEY REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE RESTRICT,
    workspace_lifecycle_event_id text NOT NULL,
    FOREIGN KEY (knowledge_object_id, workspace_lifecycle_event_id)
        REFERENCES knowledge.notes_custody_transitions(knowledge_object_id, workspace_lifecycle_event_id) ON DELETE RESTRICT
);

-- No object, root, version, chunk, embedding or processing queue is backfilled.
-- The trusted event supplies lifecycle; Notes does not invent another state.
CREATE VIEW knowledge.notes_object_custody AS
SELECT o.knowledge_object_id, o.notes_source_root_id,
       COALESCE(e.to_state, 'active') AS source_lifecycle,
       COALESCE(t.original_path, o.source_path) AS original_path,
       COALESCE(t.canonical_path, o.source_path) AS canonical_path,
       e.workspace_kind, e.object_id AS workspace_object_id, e.slug,
       t.workspace_relative_path, t.archive_operation_id,
       t.workspace_lifecycle_event_id, t.project_event_id, t.previous_event_id,
       t.manifest_digest, t.transition_digest,
       archived.occurred_at AS archived_at, e.occurred_at
FROM knowledge.knowledge_objects o
LEFT JOIN knowledge.notes_current_custody c USING (knowledge_object_id)
LEFT JOIN knowledge.notes_custody_transitions t
    ON t.knowledge_object_id = c.knowledge_object_id AND t.workspace_lifecycle_event_id = c.workspace_lifecycle_event_id
LEFT JOIN storage.workspace_lifecycle_events e
    ON e.workspace_lifecycle_event_id = t.workspace_lifecycle_event_id
LEFT JOIN storage.workspace_lifecycle_events archived
    ON archived.workspace_archive_operation_id = t.archive_operation_id;

-- +goose StatementBegin
CREATE FUNCTION knowledge.reject_notes_custody_history_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Notes custody transition history is immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_custody_history_immutable
BEFORE UPDATE OR DELETE ON knowledge.notes_custody_transitions
FOR EACH ROW EXECUTE FUNCTION knowledge.reject_notes_custody_history_mutation();

-- +goose Down
-- Do not silently make archived objects active by removing retained custody.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM knowledge.notes_custody_transitions) THEN
        RAISE EXCEPTION 'Notes custody history must be preserved; downgrade refused';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP VIEW knowledge.notes_object_custody;
DROP TABLE knowledge.notes_current_custody;
DROP TABLE knowledge.notes_custody_transitions;
DROP FUNCTION knowledge.reject_notes_custody_history_mutation();
