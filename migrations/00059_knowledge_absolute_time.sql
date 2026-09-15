-- +goose Up

ALTER TABLE knowledge.knowledge_objects
    ADD COLUMN source_created_at timestamptz NULL,
    ADD COLUMN source_modified_at timestamptz NULL,
    ADD COLUMN recency_at timestamptz NULL,
    ADD COLUMN recency_basis text NOT NULL DEFAULT '',
    ADD COLUMN absolute_time_metadata jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(absolute_time_metadata) = 'object');

UPDATE knowledge.knowledge_objects
SET recency_at = last_seen_at,
    recency_basis = 'observed_at_fallback'
WHERE recency_at IS NULL;

ALTER TABLE knowledge.knowledge_objects
    ALTER COLUMN recency_at SET DEFAULT now(),
    ALTER COLUMN recency_at SET NOT NULL,
    ALTER COLUMN recency_basis SET DEFAULT 'observed_at_fallback',
    ADD CONSTRAINT knowledge_objects_recency_basis_check CHECK (
        recency_basis IN (
            'source_filesystem_mtime',
            'source_filesystem_birthtime',
            'frontmatter_updated_at',
            'frontmatter_created_at',
            'embedded_modified_at',
            'embedded_created_at',
            'source_object_metadata',
            'observed_at_fallback'
        )
    );

CREATE INDEX knowledge_objects_recency_idx
ON knowledge.knowledge_objects (recency_at DESC, knowledge_object_id)
WHERE deleted_at IS NULL;

ALTER TABLE knowledge.knowledge_object_versions
    ADD COLUMN source_created_at timestamptz NULL,
    ADD COLUMN source_modified_at timestamptz NULL,
    ADD COLUMN recency_at timestamptz NULL,
    ADD COLUMN recency_basis text NOT NULL DEFAULT '',
    ADD COLUMN absolute_time_metadata jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(absolute_time_metadata) = 'object');

UPDATE knowledge.knowledge_object_versions
SET recency_at = observed_at,
    recency_basis = 'observed_at_fallback'
WHERE recency_at IS NULL;

ALTER TABLE knowledge.knowledge_object_versions
    ALTER COLUMN recency_at SET DEFAULT now(),
    ALTER COLUMN recency_at SET NOT NULL,
    ALTER COLUMN recency_basis SET DEFAULT 'observed_at_fallback',
    ADD CONSTRAINT knowledge_object_versions_recency_basis_check CHECK (
        recency_basis IN (
            'source_filesystem_mtime',
            'source_filesystem_birthtime',
            'frontmatter_updated_at',
            'frontmatter_created_at',
            'embedded_modified_at',
            'embedded_created_at',
            'source_object_metadata',
            'observed_at_fallback'
        )
    );

CREATE INDEX knowledge_object_versions_recency_idx
ON knowledge.knowledge_object_versions (knowledge_object_id, recency_at DESC, version_number DESC);

ALTER TABLE search.search_documents
    ADD COLUMN source_created_at timestamptz NULL,
    ADD COLUMN source_modified_at timestamptz NULL,
    ADD COLUMN recency_at timestamptz NULL,
    ADD COLUMN recency_basis text NOT NULL DEFAULT '';

UPDATE search.search_documents AS document
SET source_created_at = object.source_created_at,
    source_modified_at = object.source_modified_at,
    source_updated_at = object.source_modified_at,
    recency_at = object.recency_at,
    recency_basis = object.recency_basis
FROM knowledge.knowledge_objects AS object
WHERE document.source_kind = 'knowledge_object_metadata'
  AND document.source_id = object.knowledge_object_id;

UPDATE search.search_documents AS document
SET source_created_at = version.source_created_at,
    source_modified_at = version.source_modified_at,
    source_updated_at = version.source_modified_at,
    recency_at = version.recency_at,
    recency_basis = version.recency_basis
FROM knowledge.knowledge_object_versions AS version
WHERE document.source_kind = 'knowledge_chunk'
  AND document.source_version_id = version.knowledge_object_version_id;

ALTER TABLE search.search_documents
    ADD CONSTRAINT search_documents_recency_basis_check CHECK (
        recency_basis = '' OR recency_basis IN (
            'source_filesystem_mtime',
            'source_filesystem_birthtime',
            'frontmatter_updated_at',
            'frontmatter_created_at',
            'embedded_modified_at',
            'embedded_created_at',
            'source_object_metadata',
            'observed_at_fallback'
        )
    );

CREATE INDEX search_search_documents_recency_idx
ON search.search_documents (recency_at DESC, search_document_id)
WHERE recency_at IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS search.search_search_documents_recency_idx;

-- Restore the pre-migration source_updated_at contract before removing the
-- normalized source-time columns. The old notes index used the knowledge
-- object's row update time for both chunk and metadata documents.
UPDATE search.search_documents AS document
SET source_updated_at = object.updated_at
FROM knowledge.knowledge_objects AS object
WHERE document.source_kind = 'knowledge_object_metadata'
  AND document.source_id = object.knowledge_object_id;

UPDATE search.search_documents AS document
SET source_updated_at = object.updated_at
FROM knowledge.knowledge_chunks AS chunk
JOIN knowledge.knowledge_objects AS object
  ON object.knowledge_object_id = chunk.knowledge_object_id
WHERE document.source_kind = 'knowledge_chunk'
  AND document.source_id = chunk.knowledge_chunk_id;

ALTER TABLE search.search_documents
    DROP CONSTRAINT IF EXISTS search_documents_recency_basis_check,
    DROP COLUMN IF EXISTS recency_basis,
    DROP COLUMN IF EXISTS recency_at,
    DROP COLUMN IF EXISTS source_modified_at,
    DROP COLUMN IF EXISTS source_created_at;

DROP INDEX IF EXISTS knowledge.knowledge_object_versions_recency_idx;

ALTER TABLE knowledge.knowledge_object_versions
    DROP CONSTRAINT IF EXISTS knowledge_object_versions_recency_basis_check,
    DROP COLUMN IF EXISTS absolute_time_metadata,
    DROP COLUMN IF EXISTS recency_basis,
    DROP COLUMN IF EXISTS recency_at,
    DROP COLUMN IF EXISTS source_modified_at,
    DROP COLUMN IF EXISTS source_created_at;

DROP INDEX IF EXISTS knowledge.knowledge_objects_recency_idx;

ALTER TABLE knowledge.knowledge_objects
    DROP CONSTRAINT IF EXISTS knowledge_objects_recency_basis_check,
    DROP COLUMN IF EXISTS absolute_time_metadata,
    DROP COLUMN IF EXISTS recency_basis,
    DROP COLUMN IF EXISTS recency_at,
    DROP COLUMN IF EXISTS source_modified_at,
    DROP COLUMN IF EXISTS source_created_at;
