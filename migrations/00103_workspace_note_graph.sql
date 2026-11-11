-- +goose Up
-- Lightweight navigation metadata, independent of Notes extraction/embeddings.
CREATE TABLE knowledge.workspace_note_documents (
    document_id text PRIMARY KEY,
    root_ref text NOT NULL,
    path text NOT NULL,
    source_revision text NOT NULL DEFAULT '',
    source_hash text NOT NULL DEFAULT '',
    knowledge_object_id text NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE SET NULL,
    aliases jsonb NOT NULL DEFAULT '[]'::jsonb,
    tags jsonb NOT NULL DEFAULT '[]'::jsonb,
    properties jsonb NOT NULL DEFAULT '[]'::jsonb,
    observed_at timestamptz NULL,
    retired boolean NOT NULL DEFAULT false,
    UNIQUE(root_ref,path)
);
CREATE TABLE knowledge.workspace_note_names (
    document_id text NOT NULL REFERENCES knowledge.workspace_note_documents(document_id) ON DELETE CASCADE,
    root_ref text NOT NULL,
    kind text NOT NULL CHECK(kind IN ('link','tag','property')),
    name text NOT NULL,
    normalized text NOT NULL,
    PRIMARY KEY(document_id,kind,name)
);
CREATE INDEX workspace_note_names_prefix_idx ON knowledge.workspace_note_names(root_ref,kind,normalized text_pattern_ops);
CREATE INDEX workspace_note_names_contains_idx ON knowledge.workspace_note_names USING GIN(normalized gin_trgm_ops);
CREATE INDEX workspace_note_documents_active_idx ON knowledge.workspace_note_documents(root_ref,path) WHERE NOT retired;
CREATE INDEX workspace_note_documents_knowledge_idx ON knowledge.workspace_note_documents(knowledge_object_id) WHERE knowledge_object_id IS NOT NULL;
CREATE TABLE knowledge.workspace_note_catalogs (root_ref text PRIMARY KEY,source_path text NOT NULL,seeded_at timestamptz NOT NULL DEFAULT now(),seeded_count integer NOT NULL);
ALTER TABLE knowledge.object_links ALTER COLUMN source_knowledge_object_id DROP NOT NULL;
ALTER TABLE knowledge.object_links ADD COLUMN source_workspace_document_id text NULL REFERENCES knowledge.workspace_note_documents(document_id) ON DELETE CASCADE;
ALTER TABLE knowledge.object_links ADD COLUMN target_workspace_document_id text NULL REFERENCES knowledge.workspace_note_documents(document_id) ON DELETE SET NULL;
ALTER TABLE knowledge.object_links ADD CONSTRAINT object_links_source_owner CHECK(source_knowledge_object_id IS NOT NULL OR source_workspace_document_id IS NOT NULL);
CREATE INDEX object_links_workspace_source_idx ON knowledge.object_links(source_workspace_document_id) WHERE source_workspace_document_id IS NOT NULL;
CREATE INDEX object_links_target_name_idx ON knowledge.object_links(lower(regexp_replace(regexp_replace(split_part(normalized_target,'#',1),'^.*/',''),'\.(md|markdown)$','','i')));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'Workspace graph metadata is retained; automatic destructive downgrade is not supported.'; END $$;
-- +goose StatementEnd
