-- +goose Up
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

CREATE INDEX IF NOT EXISTS search_search_documents_metadata_idx
ON search.search_documents USING GIN (metadata);

CREATE INDEX IF NOT EXISTS search_search_documents_knowledge_source_idx
ON search.search_documents (source_version_id, source_id)
WHERE source_kind = 'knowledge_chunk';

-- +goose Down
DELETE FROM search.search_documents
WHERE source_kind = 'knowledge_chunk';

DROP INDEX IF EXISTS search.search_documents_knowledge_source_idx;
DROP INDEX IF EXISTS search.search_documents_metadata_idx;

ALTER TABLE search.search_documents
DROP CONSTRAINT IF EXISTS search_documents_source_kind_check;

ALTER TABLE search.search_documents
ADD CONSTRAINT search_documents_source_kind_check
CHECK (
    source_kind IN (
        'document_chunk',
        'object_metadata'
    )
);
