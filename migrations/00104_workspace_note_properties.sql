-- +goose Up
-- Old rows remain unknown until an explicit Seed or exact Observe supplies values.
-- In particular, an unchanged source revision cannot bypass that first Observe.
ALTER TABLE knowledge.workspace_note_documents
    ADD COLUMN property_values jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(property_values) = 'object'),
    ADD COLUMN metadata_version smallint NOT NULL DEFAULT 0 CHECK (metadata_version >= 0);
CREATE INDEX workspace_note_documents_path_prefix_idx
    ON knowledge.workspace_note_documents(root_ref, path text_pattern_ops) WHERE NOT retired;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Workspace property metadata is retained; automatic destructive downgrade is not supported.'; END $$;
-- +goose StatementEnd
