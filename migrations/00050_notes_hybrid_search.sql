-- +goose Up

ALTER TABLE search.search_documents
DROP CONSTRAINT IF EXISTS search_documents_source_kind_check;

ALTER TABLE search.search_documents
ADD CONSTRAINT search_documents_source_kind_check
CHECK (
    source_kind IN (
        'document_chunk',
        'object_metadata',
        'knowledge_chunk',
        'knowledge_object_metadata'
    )
);

CREATE TABLE IF NOT EXISTS search.lexical_documents (
    search_document_id text PRIMARY KEY
        REFERENCES search.search_documents(search_document_id) ON DELETE CASCADE,
    index_version text NOT NULL,
    source_kind text NOT NULL,
    source_id text NOT NULL,
    source_version_id text NULL,
    object_id text NULL,
    field_lengths jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(field_lengths) = 'object'),
    document_length integer NOT NULL DEFAULT 0 CHECK (document_length >= 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS search.lexical_terms (
    search_document_id text NOT NULL
        REFERENCES search.search_documents(search_document_id) ON DELETE CASCADE,
    field_key text NOT NULL CHECK (field_key <> ''),
    term text NOT NULL CHECK (term <> ''),
    term_frequency integer NOT NULL CHECK (term_frequency > 0),
    PRIMARY KEY (search_document_id, field_key, term)
);

CREATE INDEX IF NOT EXISTS search_lexical_documents_source_idx
ON search.lexical_documents (source_kind, source_id);

CREATE INDEX IF NOT EXISTS search_lexical_documents_object_idx
ON search.lexical_documents (object_id)
WHERE object_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS search_lexical_documents_index_version_idx
ON search.lexical_documents (index_version);

CREATE INDEX IF NOT EXISTS search_lexical_terms_term_idx
ON search.lexical_terms (term);

CREATE INDEX IF NOT EXISTS search_lexical_terms_term_document_idx
ON search.lexical_terms (term, search_document_id);

CREATE INDEX IF NOT EXISTS search_lexical_terms_field_term_idx
ON search.lexical_terms (field_key, term);

-- +goose Down

DELETE FROM search.search_documents
WHERE source_kind = 'knowledge_object_metadata';

DROP INDEX IF EXISTS search.search_lexical_terms_field_term_idx;
DROP INDEX IF EXISTS search.search_lexical_terms_term_document_idx;
DROP INDEX IF EXISTS search.search_lexical_terms_term_idx;
DROP INDEX IF EXISTS search.search_lexical_documents_index_version_idx;
DROP INDEX IF EXISTS search.search_lexical_documents_object_idx;
DROP INDEX IF EXISTS search.search_lexical_documents_source_idx;

DROP TABLE IF EXISTS search.lexical_terms;
DROP TABLE IF EXISTS search.lexical_documents;

ALTER TABLE search.search_documents
DROP CONSTRAINT IF EXISTS search_documents_source_kind_check;

ALTER TABLE search.search_documents
ADD CONSTRAINT search_documents_source_kind_check
CHECK (
    source_kind IN (
        'document_chunk',
        'object_metadata',
        'knowledge_chunk'
    )
);
