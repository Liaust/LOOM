-- +goose Up
CREATE TABLE IF NOT EXISTS watched_roots.backup_batches (
    watched_root_backup_batch_id text PRIMARY KEY CHECK (watched_root_backup_batch_id LIKE 'watched_root_backup_batch_%'),
    watched_root_id text NOT NULL REFERENCES watched_roots.roots(watched_root_id) ON DELETE CASCADE,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE CASCADE,
    root_key text NOT NULL,
    worker_key text NOT NULL DEFAULT '',
    idempotency_key text NULL,
    batch_kind text NOT NULL,
    backup_mode text NOT NULL,
    status text NOT NULL,
    item_count integer NOT NULL DEFAULT 0,
    accepted_count integer NOT NULL DEFAULT 0,
    duplicate_count integer NOT NULL DEFAULT 0,
    skipped_count integer NOT NULL DEFAULT 0,
    failed_count integer NOT NULL DEFAULT 0,
    artifact_count integer NOT NULL DEFAULT 0,
    deletion_marker_count integer NOT NULL DEFAULT 0,
    total_bytes bigint NOT NULL DEFAULT 0,
    received_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (node_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS watched_roots.backup_items (
    watched_root_backup_item_id text PRIMARY KEY CHECK (watched_root_backup_item_id LIKE 'watched_root_backup_item_%'),
    watched_root_backup_batch_id text NOT NULL REFERENCES watched_roots.backup_batches(watched_root_backup_batch_id) ON DELETE CASCADE,
    watched_root_id text NOT NULL REFERENCES watched_roots.roots(watched_root_id) ON DELETE CASCADE,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE CASCADE,
    root_key text NOT NULL,
    local_item_ref text NOT NULL DEFAULT '',
    item_kind text NOT NULL,
    status text NOT NULL,
    backup_mode text NOT NULL,
    relative_path text NOT NULL DEFAULT '',
    content_hash_uri text NOT NULL DEFAULT '',
    previous_hash_uri text NOT NULL DEFAULT '',
    size_bytes bigint NOT NULL DEFAULT 0,
    modified_at timestamptz NULL,
    deleted_at timestamptz NULL,
    artifact_kind text NOT NULL DEFAULT '',
    artifact_ref text NOT NULL DEFAULT '',
    private_backup_operation_id text NULL REFERENCES sync.private_backup_operations(private_backup_operation_id),
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, root_key, local_item_ref)
);

CREATE INDEX IF NOT EXISTS watched_roots_backup_batches_root_received_idx
    ON watched_roots.backup_batches (watched_root_id, received_at DESC);

CREATE INDEX IF NOT EXISTS watched_roots_backup_batches_node_status_idx
    ON watched_roots.backup_batches (node_id, status, received_at DESC);

CREATE INDEX IF NOT EXISTS watched_roots_backup_items_root_path_idx
    ON watched_roots.backup_items (watched_root_id, relative_path, created_at DESC);

CREATE INDEX IF NOT EXISTS watched_roots_backup_items_artifact_idx
    ON watched_roots.backup_items (artifact_kind, artifact_ref);

-- +goose Down
DROP TABLE IF EXISTS watched_roots.backup_items;
DROP TABLE IF EXISTS watched_roots.backup_batches;
