-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX search_notes_body_substring_idx
ON search.search_documents USING GIN (lower(body) gin_trgm_ops)
WHERE source_kind IN ('knowledge_chunk', 'knowledge_object_metadata')
  AND index_version = 'knowledge_bm25_v1';

CREATE INDEX search_notes_tags_idx
ON search.search_documents USING GIN ((metadata->'tags'))
WHERE source_kind IN ('knowledge_chunk', 'knowledge_object_metadata')
  AND index_version = 'knowledge_bm25_v1';

ANALYZE search.search_documents;

-- +goose Down
DROP INDEX IF EXISTS search.search_notes_tags_idx;
DROP INDEX IF EXISTS search.search_notes_body_substring_idx;
-- pg_trgm may be shared by other indexes; do not remove the extension.
