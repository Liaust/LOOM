-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_script_exposures (
    project_script_exposure_id text PRIMARY KEY CHECK (project_script_exposure_id LIKE 'project_script_exposure_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    script_key text NOT NULL,
    script_folder text NOT NULL,
    script_manifest_path text NOT NULL,
    exposure_path text NOT NULL DEFAULT '',
    exposure_hash text NOT NULL DEFAULT '',
    exposure_enabled boolean NOT NULL DEFAULT false,
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    provider_address text NOT NULL DEFAULT '',
    script_id text NULL REFERENCES packages.scripts(script_id),
    script_version_id text NULL REFERENCES packages.script_versions(script_version_id),
    capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    capability_endpoint_version_id text NULL REFERENCES capabilities.endpoint_versions(capability_endpoint_version_id),
    runtime_binding_id text NULL REFERENCES capabilities.endpoint_runtime_bindings(runtime_binding_id),
    capability_address text NOT NULL DEFAULT '',
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
    UNIQUE (project_id, script_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS projects_script_exposures_capability_idx
ON projects.project_script_exposures (project_id, capability_address)
WHERE capability_address <> '';

CREATE INDEX IF NOT EXISTS projects_script_exposures_registration_idx
ON projects.project_script_exposures (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS projects_script_exposures_project_status_idx
ON projects.project_script_exposures (project_id, activation_status);

CREATE INDEX IF NOT EXISTS projects_script_exposures_script_idx
ON projects.project_script_exposures (script_id, script_version_id);

-- +goose Down
DROP TABLE IF EXISTS projects.project_script_exposures;
