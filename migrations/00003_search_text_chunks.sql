-- +goose Up
CREATE SCHEMA IF NOT EXISTS search;

CREATE TABLE IF NOT EXISTS search.extracted_text (
    extracted_text_id text PRIMARY KEY CHECK (extracted_text_id LIKE 'extracted_text_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    object_version_id text NOT NULL REFERENCES objects.object_versions(object_version_id),
    blob_id text NULL REFERENCES files.blobs(blob_id),
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    extraction_method text NOT NULL,
    extractor_version text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'pending',
            'extracted',
            'failed',
            'skipped_unsupported',
            'disabled_by_policy'
        )
    ),
    text_content text NULL,
    text_hash text NULL CHECK (text_hash IS NULL OR text_hash ~ '^sha256:[0-9a-f]{64}$'),
    language text NULL,
    data_classification text NOT NULL DEFAULT 'internal',
    trust_level text NOT NULL DEFAULT 'trusted_system',
    index_policy text NOT NULL DEFAULT 'metadata_only' CHECK (
        index_policy IN (
            'none',
            'metadata_only',
            'text_later',
            'semantic_later',
            'private_no_index'
        )
    ),
    extracted_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (object_version_id, extractor_version)
);

CREATE TABLE IF NOT EXISTS search.document_chunks (
    document_chunk_id text PRIMARY KEY CHECK (document_chunk_id LIKE 'document_chunk_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    object_version_id text NOT NULL REFERENCES objects.object_versions(object_version_id),
    extracted_text_id text NOT NULL REFERENCES search.extracted_text(extracted_text_id),
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    chunk_index integer NOT NULL CHECK (chunk_index > 0),
    chunk_text text NOT NULL,
    start_offset integer NULL CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset integer NULL CHECK (end_offset IS NULL OR end_offset >= 0),
    structural_path text NULL,
    token_count_estimate integer NULL CHECK (token_count_estimate IS NULL OR token_count_estimate >= 0),
    chunk_hash text NOT NULL CHECK (chunk_hash ~ '^sha256:[0-9a-f]{64}$'),
    chunker_version text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'created',
            'indexed',
            'stale',
            'disabled_by_policy',
            'failed'
        )
    ),
    data_classification text NOT NULL DEFAULT 'internal',
    trust_level text NOT NULL DEFAULT 'trusted_system',
    created_at timestamptz NOT NULL DEFAULT now(),
    indexed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (extracted_text_id, chunk_index)
);

CREATE TABLE IF NOT EXISTS search.search_documents (
    search_document_id text PRIMARY KEY CHECK (search_document_id LIKE 'search_document_%'),
    source_kind text NOT NULL CHECK (
        source_kind IN (
            'document_chunk',
            'object_metadata'
        )
    ),
    source_id text NOT NULL,
    source_version_id text NULL,
    object_id text NULL REFERENCES objects.objects(object_id),
    object_version_id text NULL REFERENCES objects.object_versions(object_version_id),
    document_chunk_id text NULL REFERENCES search.document_chunks(document_chunk_id),
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    title text NULL,
    summary text NULL,
    body text NOT NULL,
    body_hash text NOT NULL CHECK (body_hash ~ '^sha256:[0-9a-f]{64}$'),
    tsv tsvector NOT NULL,
    language_config text NOT NULL DEFAULT 'simple',
    data_classification text NOT NULL DEFAULT 'internal',
    trust_level text NOT NULL DEFAULT 'trusted_system',
    freshness_state text NOT NULL DEFAULT 'fresh' CHECK (
        freshness_state IN (
            'fresh',
            'possibly_stale',
            'stale',
            'historical',
            'live_required',
            'unknown'
        )
    ),
    source_updated_at timestamptz NULL,
    indexed_at timestamptz NOT NULL DEFAULT now(),
    index_version text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (source_kind, source_id, index_version)
);

CREATE TABLE IF NOT EXISTS search.index_status (
    index_status_id text PRIMARY KEY CHECK (index_status_id LIKE 'index_status_%'),
    source_kind text NOT NULL,
    source_id text NOT NULL,
    source_version_id text NULL,
    object_id text NULL REFERENCES objects.objects(object_id),
    object_version_id text NULL REFERENCES objects.object_versions(object_version_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    index_type text NOT NULL CHECK (
        index_type IN (
            'text_extraction',
            'chunking',
            'full_text'
        )
    ),
    status text NOT NULL CHECK (
        status IN (
            'not_indexed',
            'queued',
            'extracting',
            'chunking',
            'indexing',
            'indexed',
            'stale',
            'failed',
            'rebuilding',
            'disabled_by_policy',
            'skipped_unsupported'
        )
    ),
    index_version text NOT NULL,
    extractor_version text NULL,
    chunker_version text NULL,
    queued_at timestamptz NULL,
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    last_error_code text NULL,
    last_error_message text NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS search_extracted_text_object_version_idx ON search.extracted_text (object_id, object_version_id);
CREATE INDEX IF NOT EXISTS search_extracted_text_blob_idx ON search.extracted_text (blob_id);
CREATE INDEX IF NOT EXISTS search_extracted_text_scope_idx ON search.extracted_text (scope_id);
CREATE INDEX IF NOT EXISTS search_extracted_text_source_node_idx ON search.extracted_text (source_node_id);
CREATE INDEX IF NOT EXISTS search_extracted_text_status_idx ON search.extracted_text (status);

CREATE INDEX IF NOT EXISTS search_document_chunks_object_version_idx ON search.document_chunks (object_id, object_version_id);
CREATE INDEX IF NOT EXISTS search_document_chunks_scope_idx ON search.document_chunks (scope_id);
CREATE INDEX IF NOT EXISTS search_document_chunks_source_node_idx ON search.document_chunks (source_node_id);
CREATE INDEX IF NOT EXISTS search_document_chunks_status_idx ON search.document_chunks (status);
CREATE INDEX IF NOT EXISTS search_document_chunks_classification_idx ON search.document_chunks (data_classification);

CREATE INDEX IF NOT EXISTS search_search_documents_tsv_idx ON search.search_documents USING GIN (tsv);
CREATE INDEX IF NOT EXISTS search_search_documents_source_idx ON search.search_documents (source_kind, source_id);
CREATE INDEX IF NOT EXISTS search_search_documents_object_idx ON search.search_documents (object_id);
CREATE INDEX IF NOT EXISTS search_search_documents_object_version_idx ON search.search_documents (object_version_id);
CREATE INDEX IF NOT EXISTS search_search_documents_chunk_idx ON search.search_documents (document_chunk_id);
CREATE INDEX IF NOT EXISTS search_search_documents_scope_idx ON search.search_documents (scope_id);
CREATE INDEX IF NOT EXISTS search_search_documents_source_node_idx ON search.search_documents (source_node_id);
CREATE INDEX IF NOT EXISTS search_search_documents_classification_idx ON search.search_documents (data_classification);
CREATE INDEX IF NOT EXISTS search_search_documents_freshness_idx ON search.search_documents (freshness_state);
CREATE INDEX IF NOT EXISTS search_search_documents_indexed_at_idx ON search.search_documents (indexed_at);

CREATE UNIQUE INDEX IF NOT EXISTS search_index_status_unique_idx
ON search.index_status (
    source_kind,
    source_id,
    COALESCE(source_version_id, ''),
    index_type,
    index_version
);
CREATE INDEX IF NOT EXISTS search_index_status_object_idx ON search.index_status (object_id);
CREATE INDEX IF NOT EXISTS search_index_status_object_version_idx ON search.index_status (object_version_id);
CREATE INDEX IF NOT EXISTS search_index_status_scope_idx ON search.index_status (scope_id);
CREATE INDEX IF NOT EXISTS search_index_status_type_status_idx ON search.index_status (index_type, status);
CREATE INDEX IF NOT EXISTS search_index_status_updated_at_idx ON search.index_status (updated_at);

-- +goose Down
DROP TABLE IF EXISTS search.index_status;
DROP TABLE IF EXISTS search.search_documents;
DROP TABLE IF EXISTS search.document_chunks;
DROP TABLE IF EXISTS search.extracted_text;

DROP SCHEMA IF EXISTS search;
