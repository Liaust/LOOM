-- +goose Up
-- Match metadataFileResults without a worker, body extraction or catalog rewrite.
SET LOCAL lock_timeout = '5s';
CREATE INDEX files_file_metadata_search_idx ON files.file_metadata USING GIN (
    to_tsvector('simple', regexp_replace(logical_name || ' ' || managed_source_path, '[[:punct:]]', ' ', 'g'))
)
WHERE source_presence = 'present'
    AND index_policy IS DISTINCT FROM 'private_no_index'
    AND metadata->>'private_no_index' IS DISTINCT FROM 'true'
    AND metadata->>'index_policy' IS DISTINCT FROM 'private_no_index';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP INDEX files.files_file_metadata_search_idx;
