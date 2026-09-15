-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_direct_event_registrations (
    project_direct_event_registration_id text PRIMARY KEY CHECK (project_direct_event_registration_id LIKE 'project_direct_event_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    event_key text NOT NULL,
    integration_key text NOT NULL,
    backend_integration_key text NOT NULL,
    endpoint_slug text NOT NULL,
    backend_endpoint_slug text NOT NULL,
    endpoint_path text NOT NULL DEFAULT '',
    event_folder text NOT NULL,
    event_manifest_path text NOT NULL,
    event_hash text NOT NULL DEFAULT '',
    payload_example_path text NOT NULL DEFAULT '',
    payload_example_hash text NOT NULL DEFAULT '',
    expected_input_path text NOT NULL DEFAULT '',
    expected_input_hash text NOT NULL DEFAULT '',
    event_type text NOT NULL DEFAULT '',
    target_capability text NOT NULL DEFAULT '',
    response_mode text NOT NULL DEFAULT '',
    integration_id text NULL REFERENCES automation.integrations(integration_id),
    auth_profile_id text NULL REFERENCES automation.integration_auth_profiles(auth_profile_id),
    endpoint_id text NULL REFERENCES automation.direct_event_endpoints(endpoint_id),
    automation_id text NULL REFERENCES automation.automations(automation_id),
    activation_status text NOT NULL CHECK (
        activation_status IN (
            'registered',
            'paused',
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
    UNIQUE (project_id, event_key),
    UNIQUE (project_id, backend_endpoint_slug)
);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_registration_idx
ON projects.project_direct_event_registrations (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_project_status_idx
ON projects.project_direct_event_registrations (project_id, activation_status);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_integration_idx
ON projects.project_direct_event_registrations (integration_id);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_auth_profile_idx
ON projects.project_direct_event_registrations (auth_profile_id);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_endpoint_idx
ON projects.project_direct_event_registrations (endpoint_id);

CREATE INDEX IF NOT EXISTS projects_direct_event_registrations_backend_endpoint_idx
ON projects.project_direct_event_registrations (backend_endpoint_slug);

-- +goose Down
DROP TABLE IF EXISTS projects.project_direct_event_registrations;
