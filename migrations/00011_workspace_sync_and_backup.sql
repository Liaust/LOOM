-- +goose Up

CREATE SCHEMA IF NOT EXISTS sync;

CREATE TABLE IF NOT EXISTS sync.local_events (
    local_event_id text PRIMARY KEY CHECK (local_event_id LIKE 'local_event_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    local_sequence bigint NOT NULL CHECK (local_sequence > 0),
    stream_name text NOT NULL,
    event_type text NOT NULL,
    event_level text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    sync_status text NOT NULL DEFAULT 'pending' CHECK (
        sync_status IN ('pending', 'queued', 'synced', 'failed', 'conflicted')
    ),
    global_event_id text NULL REFERENCES events.events(event_id),
    synced_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    UNIQUE (origin_node_id, stream_name, local_sequence)
);

CREATE TABLE IF NOT EXISTS sync.local_outbox (
    local_outbox_id text PRIMARY KEY CHECK (local_outbox_id LIKE 'local_outbox_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    item_kind text NOT NULL CHECK (
        item_kind IN ('event', 'object_metadata', 'object_blob', 'private_backup', 'deletion_request')
    ),
    local_ref text NOT NULL,
    stream_name text NOT NULL,
    local_sequence bigint NOT NULL CHECK (local_sequence > 0),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    status text NOT NULL DEFAULT 'pending' CHECK (
        status IN ('pending', 'in_flight', 'accepted', 'failed', 'conflicted')
    ),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (origin_node_id, local_ref)
);

CREATE TABLE IF NOT EXISTS sync.local_cursors (
    local_cursor_id text PRIMARY KEY CHECK (local_cursor_id LIKE 'local_cursor_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    stream_name text NOT NULL,
    last_queued_sequence bigint NOT NULL DEFAULT 0 CHECK (last_queued_sequence >= 0),
    last_pushed_sequence bigint NOT NULL DEFAULT 0 CHECK (last_pushed_sequence >= 0),
    last_accepted_sequence bigint NOT NULL DEFAULT 0 CHECK (last_accepted_sequence >= 0),
    last_success_at timestamptz NULL,
    last_error_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (origin_node_id, stream_name)
);

CREATE TABLE IF NOT EXISTS sync.local_conflicts (
    local_conflict_id text PRIMARY KEY CHECK (local_conflict_id LIKE 'local_conflict_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    local_ref text NOT NULL,
    conflict_type text NOT NULL,
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved', 'ignored')),
    summary text NOT NULL DEFAULT '',
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    main_response_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(main_response_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS sync.batches (
    sync_batch_id text PRIMARY KEY CHECK (sync_batch_id LIKE 'sync_batch_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    idempotency_key text NULL,
    batch_kind text NOT NULL CHECK (batch_kind IN ('events', 'object_metadata', 'object_blobs', 'mixed')),
    status text NOT NULL DEFAULT 'received' CHECK (
        status IN ('received', 'accepted', 'partial', 'conflicted', 'failed')
    ),
    item_count integer NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    accepted_count integer NOT NULL DEFAULT 0 CHECK (accepted_count >= 0),
    conflict_count integer NOT NULL DEFAULT 0 CHECK (conflict_count >= 0),
    failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
    cursor_before_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(cursor_before_json) = 'object'),
    cursor_after_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(cursor_after_json) = 'object'),
    received_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (origin_node_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS sync.batch_items (
    sync_batch_item_id text PRIMARY KEY CHECK (sync_batch_item_id LIKE 'sync_batch_item_%'),
    sync_batch_id text NOT NULL REFERENCES sync.batches(sync_batch_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    local_ref text NOT NULL,
    item_kind text NOT NULL CHECK (
        item_kind IN ('event', 'object_metadata', 'object_blob', 'private_backup', 'deletion_request')
    ),
    status text NOT NULL CHECK (status IN ('accepted', 'duplicate', 'conflicted', 'failed')),
    global_ref text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (origin_node_id, local_ref, sync_batch_id)
);

CREATE TABLE IF NOT EXISTS sync.ingested_events (
    ingested_event_id text PRIMARY KEY CHECK (ingested_event_id LIKE 'ingested_event_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    local_event_id text NOT NULL,
    local_sequence bigint NOT NULL CHECK (local_sequence > 0),
    stream_name text NOT NULL,
    event_type text NOT NULL,
    event_level text NOT NULL,
    local_created_at timestamptz NOT NULL,
    global_event_id text NOT NULL REFERENCES events.events(event_id),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    ingested_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (origin_node_id, local_event_id),
    UNIQUE (origin_node_id, stream_name, local_sequence)
);

CREATE TABLE IF NOT EXISTS sync.cursors (
    sync_cursor_id text PRIMARY KEY CHECK (sync_cursor_id LIKE 'sync_cursor_%'),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    stream_name text NOT NULL,
    last_accepted_sequence bigint NOT NULL DEFAULT 0 CHECK (last_accepted_sequence >= 0),
    last_accepted_local_event_id text NOT NULL DEFAULT '',
    last_batch_id text NULL REFERENCES sync.batches(sync_batch_id),
    last_success_at timestamptz NULL,
    last_error_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, stream_name)
);

CREATE TABLE IF NOT EXISTS sync.conflicts (
    sync_conflict_id text PRIMARY KEY CHECK (sync_conflict_id LIKE 'sync_conflict_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    sync_batch_id text NULL REFERENCES sync.batches(sync_batch_id),
    local_ref text NOT NULL,
    conflict_type text NOT NULL CHECK (
        conflict_type IN (
            'duplicate_payload_mismatch',
            'sequence_gap',
            'object_hash_mismatch',
            'metadata_version_mismatch',
            'unsupported_item_kind'
        )
    ),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'acknowledged', 'resolved', 'ignored')),
    summary text NOT NULL DEFAULT '',
    local_payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(local_payload_json) = 'object'),
    main_payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(main_payload_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz NULL
);

CREATE TABLE IF NOT EXISTS sync.replicas (
    replica_id text PRIMARY KEY CHECK (replica_id LIKE 'replica_%'),
    replicated_kind text NOT NULL,
    replicated_id text NOT NULL,
    source_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    replica_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    replica_mode text NOT NULL CHECK (
        replica_mode IN ('main_backup', 'main_snapshot', 'metadata_snapshot', 'raw_private_backup')
    ),
    freshness_state text NOT NULL DEFAULT 'fresh' CHECK (
        freshness_state IN ('fresh', 'stale', 'unknown', 'live_required')
    ),
    source_cursor_ref text NOT NULL DEFAULT '',
    storage_ref text NOT NULL DEFAULT '',
    last_verified_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (replicated_kind, replicated_id, source_node_id, replica_node_id, replica_mode)
);

CREATE TABLE IF NOT EXISTS sync.private_backup_operations (
    private_backup_operation_id text PRIMARY KEY CHECK (private_backup_operation_id LIKE 'private_backup_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    idempotency_key text NULL,
    status text NOT NULL DEFAULT 'received' CHECK (
        status IN ('received', 'stored', 'failed', 'deleted_later')
    ),
    coarse_size_bytes bigint NOT NULL DEFAULT 0 CHECK (coarse_size_bytes >= 0),
    storage_ref text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (origin_node_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS sync.deletion_requests (
    deletion_request_id text PRIMARY KEY CHECK (deletion_request_id LIKE 'deletion_request_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    target_kind text NOT NULL,
    target_ref text NOT NULL,
    requested_action text NOT NULL DEFAULT 'tombstone' CHECK (
        requested_action IN ('tombstone', 'unlink', 'purge_requested')
    ),
    status text NOT NULL DEFAULT 'pending_review' CHECK (
        status IN ('pending_review', 'recorded', 'approved', 'denied', 'completed')
    ),
    reason text NOT NULL DEFAULT '',
    requested_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz NULL,
    reviewed_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS sync_local_events_origin_stream_idx
    ON sync.local_events (origin_node_id, stream_name, local_sequence);

CREATE INDEX IF NOT EXISTS sync_local_outbox_origin_status_idx
    ON sync.local_outbox (origin_node_id, status, created_at);

CREATE INDEX IF NOT EXISTS sync_batches_origin_received_idx
    ON sync.batches (origin_node_id, received_at DESC);

CREATE INDEX IF NOT EXISTS sync_batch_items_batch_idx
    ON sync.batch_items (sync_batch_id, created_at);

CREATE INDEX IF NOT EXISTS sync_ingested_events_origin_stream_idx
    ON sync.ingested_events (origin_node_id, stream_name, local_sequence);

CREATE INDEX IF NOT EXISTS sync_conflicts_origin_status_idx
    ON sync.conflicts (origin_node_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS sync_replicas_source_idx
    ON sync.replicas (source_node_id, replica_mode, freshness_state);

CREATE INDEX IF NOT EXISTS sync_private_backups_origin_idx
    ON sync.private_backup_operations (origin_node_id, started_at DESC);

CREATE INDEX IF NOT EXISTS sync_deletion_requests_origin_idx
    ON sync.deletion_requests (origin_node_id, requested_at DESC);

-- +goose Down

DROP TABLE IF EXISTS sync.deletion_requests;
DROP TABLE IF EXISTS sync.private_backup_operations;
DROP TABLE IF EXISTS sync.replicas;
DROP TABLE IF EXISTS sync.conflicts;
DROP TABLE IF EXISTS sync.cursors;
DROP TABLE IF EXISTS sync.ingested_events;
DROP TABLE IF EXISTS sync.batch_items;
DROP TABLE IF EXISTS sync.batches;
DROP TABLE IF EXISTS sync.local_conflicts;
DROP TABLE IF EXISTS sync.local_cursors;
DROP TABLE IF EXISTS sync.local_outbox;
DROP TABLE IF EXISTS sync.local_events;
DROP SCHEMA IF EXISTS sync;
