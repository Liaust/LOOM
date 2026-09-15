-- +goose Up
CREATE SCHEMA IF NOT EXISTS storage;

CREATE TABLE IF NOT EXISTS storage.storage_entries (
    storage_entry_id text PRIMARY KEY CHECK (storage_entry_id LIKE 'storage_entry_%'),
    storage_class text NOT NULL CHECK (storage_class IN (
        'object_blob',
        'private_backup',
        'dropzone_custody',
        'main_document',
        'archive_entry',
        'view_entry',
        'retention_snapshot'
    )),
    source_area text NOT NULL DEFAULT 'unknown' CHECK (source_area IN (
        'projects',
        'notes',
        'documents',
        'dropzone',
        'external_watched_root',
        'main_documents',
        'main_archive',
        'unknown'
    )),
    origin_node_id text NULL CHECK (origin_node_id IS NULL OR origin_node_id LIKE 'node_%'),
    origin_node_key text NOT NULL DEFAULT '',
    project_id text NULL CHECK (project_id IS NULL OR project_id LIKE 'project_%'),
    watched_root_id text NULL CHECK (watched_root_id IS NULL OR watched_root_id LIKE 'watched_root_%'),
    watched_root_key text NOT NULL DEFAULT '',
    dropzone_transfer_id text NOT NULL DEFAULT '',
    private_backup_operation_id text NULL CHECK (
        private_backup_operation_id IS NULL
        OR private_backup_operation_id LIKE 'private_backup_%'
        OR private_backup_operation_id LIKE 'local_backup_batch_%'
        OR private_backup_operation_id LIKE 'watched_root_backup_batch_%'
    ),
    private_backup_item_id text NULL CHECK (
        private_backup_item_id IS NULL
        OR private_backup_item_id LIKE 'local_backup_item_%'
        OR private_backup_item_id LIKE 'watched_root_backup_item_%'
    ),
    object_id text NULL CHECK (object_id IS NULL OR object_id LIKE 'object_%'),
    object_version_id text NULL CHECK (object_version_id IS NULL OR object_version_id LIKE 'version_%'),
    archive_manifest_id text NULL CHECK (archive_manifest_id IS NULL OR archive_manifest_id LIKE 'storage_archive_manifest_%'),
    logical_path text NOT NULL,
    original_source_path text NOT NULL DEFAULT '',
    current_view_path text NOT NULL DEFAULT '',
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_hex text NOT NULL DEFAULT '',
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    mime_type text NOT NULL DEFAULT '',
    file_class text NOT NULL DEFAULT 'unknown' CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'binary',
        'unknown'
    )),
    processing_state text NOT NULL DEFAULT 'metadata_only' CHECK (processing_state IN (
        'unprocessed',
        'backup_only',
        'object_registered',
        'text_indexed',
        'metadata_only',
        'excluded',
        'failed'
    )),
    availability_state text NOT NULL DEFAULT 'available' CHECK (availability_state IN (
        'discovered',
        'pending',
        'available',
        'deleted',
        'tombstoned',
        'archived',
        'superseded',
        'failed'
    )),
    retention_state text NOT NULL DEFAULT 'none' CHECK (retention_state IN (
        'none',
        'retained',
        'snapshot',
        'expired',
        'pending'
    )),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz NULL
);

CREATE INDEX IF NOT EXISTS storage_entries_logical_path_idx
    ON storage.storage_entries (logical_path);
CREATE INDEX IF NOT EXISTS storage_entries_view_path_idx
    ON storage.storage_entries (current_view_path)
    WHERE current_view_path <> '';
CREATE INDEX IF NOT EXISTS storage_entries_origin_node_idx
    ON storage.storage_entries (origin_node_key, source_area);
CREATE INDEX IF NOT EXISTS storage_entries_class_state_idx
    ON storage.storage_entries (storage_class, availability_state, processing_state);
CREATE INDEX IF NOT EXISTS storage_entries_project_idx
    ON storage.storage_entries (project_id)
    WHERE project_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS storage_entries_object_idx
    ON storage.storage_entries (object_id, object_version_id)
    WHERE object_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS storage.storage_entry_versions (
    storage_entry_version_id text PRIMARY KEY CHECK (storage_entry_version_id LIKE 'storage_entry_version_%'),
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    version_number integer NOT NULL CHECK (version_number > 0),
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_hex text NOT NULL DEFAULT '',
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    physical_ref_id text NULL CHECK (physical_ref_id IS NULL OR physical_ref_id LIKE 'storage_physical_ref_%'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (storage_entry_id, version_number)
);

CREATE TABLE IF NOT EXISTS storage.storage_physical_refs (
    storage_physical_ref_id text PRIMARY KEY CHECK (storage_physical_ref_id LIKE 'storage_physical_ref_%'),
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    storage_entry_version_id text NULL CHECK (storage_entry_version_id IS NULL OR storage_entry_version_id LIKE 'storage_entry_version_%'),
    ref_kind text NOT NULL CHECK (ref_kind IN ('local_path', 'object_blob', 'backup_artifact', 'dropzone_file', 'archive_file', 'external_uri')),
    uri text NOT NULL,
    node_id text NULL CHECK (node_id IS NULL OR node_id LIKE 'node_%'),
    node_key text NOT NULL DEFAULT '',
    content_address text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'available' CHECK (status IN ('available', 'missing', 'verifying', 'failed', 'superseded')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS storage_physical_refs_entry_idx
    ON storage.storage_physical_refs (storage_entry_id, status);
CREATE INDEX IF NOT EXISTS storage_physical_refs_uri_idx
    ON storage.storage_physical_refs (ref_kind, uri);

CREATE TABLE IF NOT EXISTS storage.storage_events (
    storage_event_id bigserial PRIMARY KEY,
    storage_entry_id text NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE SET NULL,
    event_kind text NOT NULL,
    source_component text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS storage_events_entry_created_idx
    ON storage.storage_events (storage_entry_id, created_at DESC);

CREATE TABLE IF NOT EXISTS storage.storage_view_builds (
    storage_view_build_id text PRIMARY KEY CHECK (storage_view_build_id LIKE 'storage_view_build_%'),
    view_key text NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'complete', 'failed')),
    root_path text NOT NULL DEFAULT '',
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    started_at timestamptz NULL,
    finished_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS storage.storage_view_entries (
    storage_view_entry_id text PRIMARY KEY CHECK (storage_view_entry_id LIKE 'storage_view_entry_%'),
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    view_key text NOT NULL,
    view_path text NOT NULL,
    entry_kind text NOT NULL DEFAULT 'file' CHECK (entry_kind IN ('file', 'directory', 'status_file', 'placeholder')),
    permissions text NOT NULL DEFAULT 'read_only' CHECK (permissions IN ('read_only', 'writable', 'controlled')),
    generated boolean NOT NULL DEFAULT true,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (view_key, view_path)
);

CREATE INDEX IF NOT EXISTS storage_view_entries_entry_idx
    ON storage.storage_view_entries (storage_entry_id);

CREATE TABLE IF NOT EXISTS storage.archive_manifests (
    archive_manifest_id text PRIMARY KEY CHECK (archive_manifest_id LIKE 'storage_archive_manifest_%'),
    archive_key text NOT NULL,
    archive_kind text NOT NULL CHECK (archive_kind IN ('project_archive', 'document_archive', 'notes_archive', 'manual_archive')),
    owner_node_id text NULL CHECK (owner_node_id IS NULL OR owner_node_id LIKE 'node_%'),
    owner_node_key text NOT NULL DEFAULT '',
    source_ref text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'building', 'complete', 'failed', 'superseded')),
    manifest_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(manifest_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    finalized_at timestamptz NULL,
    UNIQUE (archive_key)
);

CREATE TABLE IF NOT EXISTS storage.archive_items (
    archive_manifest_id text NOT NULL REFERENCES storage.archive_manifests(archive_manifest_id) ON DELETE CASCADE,
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    archive_path text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (archive_manifest_id, storage_entry_id)
);

CREATE TABLE IF NOT EXISTS storage.retention_entries (
    storage_retention_entry_id text PRIMARY KEY CHECK (storage_retention_entry_id LIKE 'storage_retention_entry_%'),
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    policy_key text NOT NULL,
    retention_state text NOT NULL DEFAULT 'retained' CHECK (retention_state IN ('retained', 'snapshot', 'expired', 'pending')),
    retained_until timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS storage.tombstones (
    storage_tombstone_id text PRIMARY KEY CHECK (storage_tombstone_id LIKE 'storage_tombstone_%'),
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    tombstone_kind text NOT NULL CHECK (tombstone_kind IN ('source_deleted', 'archived_elsewhere', 'manual_delete', 'retention_expired')),
    reason text NOT NULL DEFAULT '',
    created_by text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS storage.project_runtime_archives (
    project_runtime_archive_id text PRIMARY KEY CHECK (project_runtime_archive_id LIKE 'project_runtime_archive_%'),
    project_id text NOT NULL CHECK (project_id LIKE 'project_%'),
    source_node_id text NULL CHECK (source_node_id IS NULL OR source_node_id LIKE 'node_%'),
    source_node_key text NOT NULL DEFAULT '',
    archive_manifest_id text NULL CHECK (archive_manifest_id IS NULL OR archive_manifest_id LIKE 'storage_archive_manifest_%'),
    runtime_status text NOT NULL DEFAULT 'disabled' CHECK (runtime_status IN ('disabled', 'main_owned_pending', 'main_owned_active', 'failed')),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id)
);

CREATE TABLE IF NOT EXISTS storage.project_runtime_archive_items (
    project_runtime_archive_id text NOT NULL REFERENCES storage.project_runtime_archives(project_runtime_archive_id) ON DELETE CASCADE,
    storage_entry_id text NOT NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    item_role text NOT NULL CHECK (item_role IN ('contract', 'script', 'workflow', 'connector', 'module', 'repo', 'notes', 'other')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_runtime_archive_id, storage_entry_id)
);

CREATE TABLE IF NOT EXISTS storage.export_roots (
    export_root_id text PRIMARY KEY CHECK (export_root_id LIKE 'storage_export_root_%'),
    view_key text NOT NULL,
    root_path text NOT NULL,
    status text NOT NULL DEFAULT 'configured' CHECK (status IN ('configured', 'building', 'ready', 'failed', 'disabled')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (view_key, root_path)
);

CREATE TABLE IF NOT EXISTS storage.export_builds (
    export_build_id text PRIMARY KEY CHECK (export_build_id LIKE 'storage_export_build_%'),
    export_root_id text NOT NULL REFERENCES storage.export_roots(export_root_id) ON DELETE CASCADE,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'complete', 'failed')),
    summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(summary) = 'object'),
    started_at timestamptz NULL,
    finished_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS storage.export_entry_status (
    export_root_id text NOT NULL REFERENCES storage.export_roots(export_root_id) ON DELETE CASCADE,
    storage_view_entry_id text NOT NULL CHECK (storage_view_entry_id LIKE 'storage_view_entry_%'),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'materialized', 'missing_source', 'failed', 'pruned')),
    exported_path text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (export_root_id, storage_view_entry_id)
);

-- +goose Down
DROP TABLE IF EXISTS storage.export_entry_status;
DROP TABLE IF EXISTS storage.export_builds;
DROP TABLE IF EXISTS storage.export_roots;
DROP TABLE IF EXISTS storage.project_runtime_archive_items;
DROP TABLE IF EXISTS storage.project_runtime_archives;
DROP TABLE IF EXISTS storage.tombstones;
DROP TABLE IF EXISTS storage.retention_entries;
DROP TABLE IF EXISTS storage.archive_items;
DROP TABLE IF EXISTS storage.archive_manifests;
DROP TABLE IF EXISTS storage.storage_view_entries;
DROP TABLE IF EXISTS storage.storage_view_builds;
DROP TABLE IF EXISTS storage.storage_events;
DROP TABLE IF EXISTS storage.storage_physical_refs;
DROP TABLE IF EXISTS storage.storage_entry_versions;
DROP TABLE IF EXISTS storage.storage_entries;
DROP SCHEMA IF EXISTS storage;
