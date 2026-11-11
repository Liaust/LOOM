-- +goose Up
-- Live source observations are independent of protected content versions.
ALTER TABLE files.file_metadata
    ADD COLUMN managed_source_path text NULL,
    ADD COLUMN source_root_key text NOT NULL DEFAULT '',
    ADD COLUMN source_relative_path text NOT NULL DEFAULT '',
    ADD COLUMN source_generation text NOT NULL DEFAULT '',
    ADD COLUMN source_sequence bigint NOT NULL DEFAULT 0 CHECK (source_sequence >= 0),
    ADD COLUMN source_presence text NOT NULL DEFAULT 'unobserved'
        CHECK (source_presence IN ('unobserved', 'present', 'missing', 'unavailable')),
    ADD COLUMN source_observed_at timestamptz NULL,
    ADD COLUMN source_observed_size bigint NULL CHECK (source_observed_size >= 0);

CREATE UNIQUE INDEX file_managed_source_identity
    ON files.file_metadata (source_node_id, managed_source_path)
    WHERE managed_source_path IS NOT NULL;
CREATE INDEX file_observed_root_path
    ON files.file_metadata (source_node_id, source_root_key, source_relative_path)
    WHERE source_root_key <> '';
CREATE INDEX file_source_node_path
    ON files.file_metadata (source_node_id, source_path)
    WHERE source_path IS NOT NULL;

ALTER TABLE knowledge.knowledge_objects
    ADD COLUMN canonical_file_id text NULL REFERENCES files.file_metadata(object_id);
CREATE INDEX knowledge_canonical_file ON knowledge.knowledge_objects(canonical_file_id)
    WHERE canonical_file_id IS NOT NULL;

-- Link only exact, already recorded ownership; do not invent hash-based lineage.
UPDATE knowledge.knowledge_objects ko
SET canonical_file_id = f.object_id
FROM files.file_metadata f
WHERE f.object_id = ko.metadata->'synced_object'->>'object_id'
    AND f.source_node_id = ko.source_node_id;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    RAISE EXCEPTION 'File identity observations must survive code rollback; use compatible code';
END $$;
-- +goose StatementEnd
