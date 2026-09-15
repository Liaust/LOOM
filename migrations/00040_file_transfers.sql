-- +goose Up
CREATE SCHEMA IF NOT EXISTS storage;

CREATE TABLE IF NOT EXISTS storage.file_transfers (
    file_transfer_id text PRIMARY KEY CHECK (file_transfer_id LIKE 'file_transfer_%'),
    idempotency_key text NOT NULL DEFAULT '',
    source_node_id text NULL CHECK (source_node_id IS NULL OR source_node_id LIKE 'node_%'),
    source_node_key text NOT NULL DEFAULT '',
    source_root_key text NOT NULL DEFAULT '',
    source_relative_path text NOT NULL DEFAULT '',
    destination_logical_path text NOT NULL,
    transfer_kind text NOT NULL CHECK (transfer_kind IN (
        'watched_root_backup',
        'dropzone_custody',
        'main_documents_import',
        'manual_upload'
    )),
    custody_mode text NOT NULL CHECK (custody_mode IN (
        'backup_copy',
        'custody_transfer',
        'main_owned'
    )),
    file_size_bytes bigint NOT NULL CHECK (file_size_bytes >= 0),
    mtime timestamptz NULL,
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_hex text NOT NULL DEFAULT '',
    chunk_size_bytes bigint NOT NULL CHECK (chunk_size_bytes > 0),
    chunk_count integer NOT NULL CHECK (chunk_count >= 0),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending',
        'uploading',
        'paused',
        'completing',
        'accepted',
        'failed',
        'aborted'
    )),
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    accepted_path text NOT NULL DEFAULT '',
    storage_entry_id text NULL CHECK (storage_entry_id IS NULL OR storage_entry_id LIKE 'storage_entry_%'),
    storage_physical_ref_id text NULL CHECK (storage_physical_ref_id IS NULL OR storage_physical_ref_id LIKE 'storage_physical_ref_%'),
    dropzone_transfer_id text NOT NULL DEFAULT '',
    watched_root_id text NULL CHECK (watched_root_id IS NULL OR watched_root_id LIKE 'watched_root_%'),
    watched_root_backup_batch_id text NULL CHECK (watched_root_backup_batch_id IS NULL OR watched_root_backup_batch_id LIKE 'watched_root_backup_batch_%'),
    watched_root_backup_item_id text NULL CHECK (watched_root_backup_item_id IS NULL OR watched_root_backup_item_id LIKE 'watched_root_backup_item_%'),
    private_backup_operation_id text NULL CHECK (
        private_backup_operation_id IS NULL
        OR private_backup_operation_id LIKE 'private_backup_%'
        OR private_backup_operation_id LIKE 'watched_root_backup_batch_%'
    ),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    aborted_at timestamptz NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS file_transfers_idempotency_key_idx
    ON storage.file_transfers (idempotency_key)
    WHERE idempotency_key <> '';
CREATE INDEX IF NOT EXISTS file_transfers_source_idx
    ON storage.file_transfers (source_node_key, source_root_key, source_relative_path);
CREATE INDEX IF NOT EXISTS file_transfers_status_idx
    ON storage.file_transfers (status, updated_at DESC);
CREATE INDEX IF NOT EXISTS file_transfers_destination_idx
    ON storage.file_transfers (destination_logical_path);
CREATE INDEX IF NOT EXISTS file_transfers_dropzone_idx
    ON storage.file_transfers (dropzone_transfer_id)
    WHERE dropzone_transfer_id <> '';
CREATE INDEX IF NOT EXISTS file_transfers_storage_entry_idx
    ON storage.file_transfers (storage_entry_id)
    WHERE storage_entry_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS storage.file_transfer_chunks (
    file_transfer_chunk_id text PRIMARY KEY CHECK (file_transfer_chunk_id LIKE 'file_transfer_chunk_%'),
    file_transfer_id text NOT NULL REFERENCES storage.file_transfers(file_transfer_id) ON DELETE CASCADE,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    offset_bytes bigint NOT NULL CHECK (offset_bytes >= 0),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    checksum_algorithm text NOT NULL DEFAULT '',
    checksum_hex text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'pending' CHECK (status IN (
        'pending',
        'uploading',
        'uploaded',
        'accepted',
        'failed',
        'skipped'
    )),
    received_bytes bigint NOT NULL DEFAULT 0 CHECK (received_bytes >= 0),
    staging_path text NOT NULL DEFAULT '',
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    received_at timestamptz NULL,
    accepted_at timestamptz NULL,
    UNIQUE (file_transfer_id, chunk_index)
);

CREATE INDEX IF NOT EXISTS file_transfer_chunks_transfer_status_idx
    ON storage.file_transfer_chunks (file_transfer_id, status, chunk_index);
CREATE INDEX IF NOT EXISTS file_transfer_chunks_transfer_idx
    ON storage.file_transfer_chunks (file_transfer_id, chunk_index);

-- +goose Down
DROP TABLE IF EXISTS storage.file_transfer_chunks;
DROP TABLE IF EXISTS storage.file_transfers;
