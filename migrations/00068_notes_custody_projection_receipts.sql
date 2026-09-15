-- +goose Up
-- Empty membership is still a completed projection, not an unconsumed event.
CREATE INDEX notes_custody_transitions_event_idx
ON knowledge.notes_custody_transitions(workspace_lifecycle_event_id, knowledge_object_id);
CREATE TABLE knowledge.notes_custody_projection_receipts (
    node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
    workspace_lifecycle_event_id text NOT NULL REFERENCES storage.workspace_lifecycle_events(workspace_lifecycle_event_id) ON DELETE RESTRICT,
    manifest_digest text NOT NULL CHECK (manifest_digest ~ '^sha256:[a-f0-9]{64}$'),
    project_event_id text REFERENCES events.events(event_id) ON DELETE RESTRICT,
    previous_event_id text,
    object_count integer NOT NULL CHECK (object_count BETWEEN 0 AND 250000),
    membership_digest text NOT NULL CHECK (membership_digest ~ '^sha256:[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, workspace_lifecycle_event_id),
    FOREIGN KEY (node_id, previous_event_id)
        REFERENCES knowledge.notes_custody_projection_receipts(node_id, workspace_lifecycle_event_id) ON DELETE RESTRICT,
    CHECK (previous_event_id IS DISTINCT FROM workspace_lifecycle_event_id)
);
CREATE TRIGGER notes_custody_receipt_immutable
BEFORE UPDATE OR DELETE ON knowledge.notes_custody_projection_receipts
FOR EACH ROW EXECUTE FUNCTION knowledge.reject_notes_custody_history_mutation();

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM knowledge.notes_custody_projection_receipts) THEN
        RAISE EXCEPTION 'Notes custody projection receipts must be preserved; downgrade refused';
    END IF;
END;
$$;
-- +goose StatementEnd
DROP TABLE knowledge.notes_custody_projection_receipts;
DROP INDEX knowledge.notes_custody_transitions_event_idx;
