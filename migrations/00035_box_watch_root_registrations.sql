-- +goose Up
CREATE SCHEMA IF NOT EXISTS box;

CREATE TABLE IF NOT EXISTS box.watch_root_registrations (
    box_watch_root_registration_id text PRIMARY KEY CHECK (box_watch_root_registration_id LIKE 'box_watch_root_registration_%'),
    box_id text NOT NULL CHECK (box_id LIKE 'box_%'),
    box_root_path text NOT NULL,
    box_contract_path text NOT NULL,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    owner_node_key text NOT NULL DEFAULT '',
    area_key text NOT NULL CHECK (area_key IN ('notes', 'launchpad')),
    local_root_key text NOT NULL,
    backend_root_key text NOT NULL,
    worker_key text NOT NULL DEFAULT '',
    source_kinds_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(source_kinds_json) = 'array'),
    safe_root_key text NOT NULL DEFAULT '',
    root_relative_path text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    sync_mode text NOT NULL DEFAULT '',
    backup_mode text NOT NULL DEFAULT '',
    index_mode text NOT NULL DEFAULT '',
    delete_mode text NOT NULL DEFAULT '',
    config_hash text NOT NULL DEFAULT '',
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config_json) = 'object'),
    command_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(command_json) = 'array'),
    watched_root_id text NULL REFERENCES watched_roots.roots(watched_root_id),
    activation_status text NOT NULL CHECK (
        activation_status IN (
            'registered',
            'pending_agent_apply',
            'applied',
            'reported',
            'blocked',
            'disabled',
            'stale'
        )
    ),
    last_applied_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    last_applied_at timestamptz NULL,
    last_reported_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, box_id, backend_root_key),
    UNIQUE (node_id, box_root_path, area_key)
);

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_box_status_idx
ON box.watch_root_registrations (box_id, activation_status);

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_node_root_idx
ON box.watch_root_registrations (node_id, backend_root_key);

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_root_idx
ON box.watch_root_registrations (watched_root_id);

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_config_hash_idx
ON box.watch_root_registrations (config_hash);

-- +goose Down
DROP TABLE IF EXISTS box.watch_root_registrations;
DROP SCHEMA IF EXISTS box;
