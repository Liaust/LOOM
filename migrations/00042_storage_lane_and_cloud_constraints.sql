-- +goose Up

ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_storage_class_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_storage_class_check
    CHECK (storage_class IN (
        'object_blob',
        'private_backup',
        'dropzone_custody',
        'lane_custody',
        'main_document',
        'archive_entry',
        'view_entry',
        'retention_snapshot'
    ));

ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_source_area_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_source_area_check
    CHECK (source_area IN (
        'projects',
        'notes',
        'documents',
        'dropzone',
        'lane',
        'external_watched_root',
        'main_documents',
        'main_archive',
        'unknown'
    ));

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
        'archive_file',
        'external_uri'
    ));

ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_source_area_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_source_area_check
    CHECK (source_area IN (
        'projects',
        'notes',
        'documents',
        'dropzone',
        'external_watched_root',
        'main_documents',
        'main_archive',
        'unknown'
    ));

ALTER TABLE storage.storage_entries
    DROP CONSTRAINT IF EXISTS storage_entries_storage_class_check;

ALTER TABLE storage.storage_entries
    ADD CONSTRAINT storage_entries_storage_class_check
    CHECK (storage_class IN (
        'object_blob',
        'private_backup',
        'dropzone_custody',
        'main_document',
        'archive_entry',
        'view_entry',
        'retention_snapshot'
    ));
