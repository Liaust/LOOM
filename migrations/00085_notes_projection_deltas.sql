-- Derived generated-view work only; canonical bytes and source journals retain ownership.
-- +goose Up
CREATE TABLE knowledge.notes_projection_dirty (
 dirty_key text PRIMARY KEY,
 knowledge_object_id text,
 sequence bigint NOT NULL DEFAULT 1 CHECK (sequence > 0),
 acknowledged_sequence bigint NOT NULL DEFAULT 0 CHECK (acknowledged_sequence >= 0 AND acknowledged_sequence <= sequence),
 waiting_since timestamptz NOT NULL DEFAULT now(),
 retry_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK ((dirty_key = 'full' AND knowledge_object_id IS NULL) OR
        (knowledge_object_id IS NOT NULL AND dirty_key = 'object:' || knowledge_object_id))
);
CREATE INDEX notes_projection_dirty_ready ON knowledge.notes_projection_dirty
 (retry_at, waiting_since, dirty_key) WHERE sequence > acknowledged_sequence;
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Projection delta downgrade retains pending work; use compatible code rollback';
END $$;
-- +goose StatementEnd
