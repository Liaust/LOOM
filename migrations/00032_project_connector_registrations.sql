-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_connector_registrations (
    project_connector_registration_id text PRIMARY KEY CHECK (project_connector_registration_id LIKE 'project_connector_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    connector_key text NOT NULL,
    connector_folder text NOT NULL,
    connector_manifest_path text NOT NULL,
    connector_hash text NOT NULL DEFAULT '',
    provider_key text NOT NULL,
    provider_address text NOT NULL DEFAULT '',
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    provider_status text NOT NULL DEFAULT '',
    runtime_kind text NOT NULL DEFAULT '',
    capability_count integer NOT NULL DEFAULT 0 CHECK (capability_count >= 0),
    active_capability_count integer NOT NULL DEFAULT 0 CHECK (active_capability_count >= 0),
    usage_document_count integer NOT NULL DEFAULT 0 CHECK (usage_document_count >= 0),
    activation_status text NOT NULL CHECK (
        activation_status IN (
            'registered',
            'active',
            'disabled',
            'blocked',
            'stale'
        )
    ),
    last_activated_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    last_activated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, connector_key),
    UNIQUE (project_id, provider_address)
);

CREATE INDEX IF NOT EXISTS project_connector_registrations_registration_idx
ON projects.project_connector_registrations (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS project_connector_registrations_project_status_idx
ON projects.project_connector_registrations (project_id, activation_status);

CREATE INDEX IF NOT EXISTS project_connector_registrations_provider_idx
ON projects.project_connector_registrations (provider_id);

CREATE INDEX IF NOT EXISTS project_connector_registrations_provider_key_idx
ON projects.project_connector_registrations (project_id, provider_key);

-- +goose Down
DROP TABLE IF EXISTS projects.project_connector_registrations;
