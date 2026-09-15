-- +goose Up
-- Keep legacy stable IDs byte-for-byte; additionally admit the canonical
-- project IDs already produced by registration. Other workspace kinds retain
-- their existing ID contract.
ALTER TABLE storage.workspace_archive_operations
    DROP CONSTRAINT workspace_archive_operations_object_id_check,
    ADD CONSTRAINT workspace_archive_operations_object_id_check CHECK (
        object_id ~ '^[a-z][a-z0-9_]{2,127}$'
        OR (workspace_kind = 'project' AND object_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$')
    );
ALTER TABLE storage.workspace_archive_manifests
    DROP CONSTRAINT workspace_archive_manifests_object_id_check,
    ADD CONSTRAINT workspace_archive_manifests_object_id_check CHECK (
        object_id ~ '^[a-z][a-z0-9_]{2,127}$'
        OR (workspace_kind = 'project' AND object_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$')
    );
ALTER TABLE storage.workspace_lifecycle_events
    DROP CONSTRAINT workspace_lifecycle_events_object_id_check,
    ADD CONSTRAINT workspace_lifecycle_events_object_id_check CHECK (
        object_id ~ '^[a-z][a-z0-9_]{2,127}$'
        OR (workspace_kind = 'project' AND object_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$')
    );

-- +goose Down
-- Deliberately fail transactionally if canonical project IDs have been stored.
-- A schema downgrade must not rewrite or delete their authenticated evidence.
ALTER TABLE storage.workspace_archive_operations
    DROP CONSTRAINT workspace_archive_operations_object_id_check,
    ADD CONSTRAINT workspace_archive_operations_object_id_check CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$');
ALTER TABLE storage.workspace_archive_manifests
    DROP CONSTRAINT workspace_archive_manifests_object_id_check,
    ADD CONSTRAINT workspace_archive_manifests_object_id_check CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$');
ALTER TABLE storage.workspace_lifecycle_events
    DROP CONSTRAINT workspace_lifecycle_events_object_id_check,
    ADD CONSTRAINT workspace_lifecycle_events_object_id_check CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$');
