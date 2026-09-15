-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_module_registrations (
    project_module_registration_id text PRIMARY KEY CHECK (project_module_registration_id LIKE 'project_module_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    module_key text NOT NULL,
    module_folder text NOT NULL,
    module_manifest_path text NOT NULL,
    module_project_contract_path text NOT NULL DEFAULT '',
    module_manifest_hash text NOT NULL DEFAULT '',
    module_project_contract_hash text NOT NULL DEFAULT '',
    module_package_hash text NOT NULL DEFAULT '',
    module_id text NOT NULL DEFAULT '',
    module_name text NOT NULL DEFAULT '',
    module_version text NOT NULL DEFAULT '',
    module_kind text NOT NULL DEFAULT '',
    module_package_id text NULL REFERENCES modules.packages(module_package_id),
    module_version_id text NULL REFERENCES modules.module_versions(module_version_id),
    requirement_count integer NOT NULL DEFAULT 0 CHECK (requirement_count >= 0),
    object_type_count integer NOT NULL DEFAULT 0 CHECK (object_type_count >= 0),
    provider_count integer NOT NULL DEFAULT 0 CHECK (provider_count >= 0),
    capability_count integer NOT NULL DEFAULT 0 CHECK (capability_count >= 0),
    usage_document_count integer NOT NULL DEFAULT 0 CHECK (usage_document_count >= 0),
    backup_hook_count integer NOT NULL DEFAULT 0 CHECK (backup_hook_count >= 0),
    registration_enabled boolean NOT NULL DEFAULT true,
    install_plan_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(install_plan_json) = 'object'),
    exposure_plan_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(exposure_plan_json) = 'object'),
    activation_status text NOT NULL CHECK (
        activation_status IN (
            'registered',
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
    UNIQUE (project_id, module_key)
);

CREATE INDEX IF NOT EXISTS project_module_registrations_registration_idx
ON projects.project_module_registrations (project_contract_registration_id);

CREATE INDEX IF NOT EXISTS project_module_registrations_project_status_idx
ON projects.project_module_registrations (project_id, activation_status);

CREATE INDEX IF NOT EXISTS project_module_registrations_module_id_idx
ON projects.project_module_registrations (module_id);

CREATE INDEX IF NOT EXISTS project_module_registrations_module_version_idx
ON projects.project_module_registrations (module_version_id);

-- +goose Down
DROP TABLE IF EXISTS projects.project_module_registrations;
