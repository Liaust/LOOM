-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_watched_root_registrations (
    project_watched_root_registration_id text PRIMARY KEY CHECK (project_watched_root_registration_id LIKE 'project_watched_root_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    owner_node_key text NOT NULL DEFAULT '',
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
    UNIQUE (project_id, backend_root_key),
    UNIQUE (project_contract_registration_id, local_root_key)
);

CREATE INDEX IF NOT EXISTS projects_watched_root_registrations_registration_idx
ON projects.project_watched_root_registrations (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS projects_watched_root_registrations_project_status_idx
ON projects.project_watched_root_registrations (project_id, activation_status);

CREATE INDEX IF NOT EXISTS projects_watched_root_registrations_node_root_idx
ON projects.project_watched_root_registrations (node_id, backend_root_key);

CREATE INDEX IF NOT EXISTS projects_watched_root_registrations_root_idx
ON projects.project_watched_root_registrations (watched_root_id);

CREATE INDEX IF NOT EXISTS projects_watched_root_registrations_config_hash_idx
ON projects.project_watched_root_registrations (config_hash);

-- +goose Down
DROP TABLE IF EXISTS projects.project_watched_root_registrations;
