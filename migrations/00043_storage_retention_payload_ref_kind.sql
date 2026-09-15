-- +goose Up

ALTER TABLE storage.storage_physical_refs
    DROP CONSTRAINT IF EXISTS storage_physical_refs_ref_kind_check;

ALTER TABLE storage.storage_physical_refs
    ADD CONSTRAINT storage_physical_refs_ref_kind_check
    CHECK (ref_kind IN (
        'local_path',
        'object_blob',
        'backup_artifact',
        'dropzone_file',
        'lane_file',
        'retention_payload',
        'archive_file',
        'external_uri',
        'cloud_object'
    ));

-- +goose Down

ALTER TABLE storage.storage_physical_refs
    DROP CONSTRAINT IF EXISTS storage_physical_refs_ref_kind_check;

ALTER TABLE storage.storage_physical_refs
    ADD CONSTRAINT storage_physical_refs_ref_kind_check
    CHECK (ref_kind IN (
        'local_path',
        'object_blob',
        'backup_artifact',
        'dropzone_file',
        'lane_file',
        'archive_file',
        'external_uri',
        'cloud_object'
    ));
