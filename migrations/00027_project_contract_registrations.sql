-- +goose Up
CREATE TABLE IF NOT EXISTS projects.project_contract_registrations (
    project_contract_registration_id text PRIMARY KEY CHECK (project_contract_registration_id LIKE 'project_contract_registration_%'),
    project_id text UNIQUE NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    project_root text NOT NULL,
    contract_path text NOT NULL,
    contract_hash text NOT NULL CHECK (contract_hash ~ '^sha256:[0-9a-f]{64}$'),
    contract_schema_version text NOT NULL,
    contract_json jsonb NOT NULL CHECK (jsonb_typeof(contract_json) = 'object'),
    validation_report_json jsonb NOT NULL CHECK (jsonb_typeof(validation_report_json) = 'object'),
    registration_plan_json jsonb NOT NULL CHECK (jsonb_typeof(registration_plan_json) = 'object'),
    derived_providers_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(derived_providers_json) = 'array'),
    policy_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(policy_refs_json) = 'array'),
    registration_status text NOT NULL CHECK (registration_status IN ('registered', 'blocked', 'stale', 'archived')),
    activation_status text NOT NULL CHECK (activation_status IN ('inactive', 'base_active', 'facet_activation_pending', 'blocked')),
    registration_revision integer NOT NULL DEFAULT 1 CHECK (registration_revision > 0),
    last_registered_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    last_registered_at timestamptz NOT NULL DEFAULT now(),
    base_activated_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    base_activated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS projects.project_contract_facets (
    project_contract_facet_id text PRIMARY KEY CHECK (project_contract_facet_id LIKE 'project_contract_facet_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    facet_key text NOT NULL,
    folder text NOT NULL DEFAULT '',
    enabled boolean NOT NULL,
    present boolean NOT NULL,
    placeholder boolean NOT NULL DEFAULT false,
    facet_status text NOT NULL CHECK (
        facet_status IN (
            'declared',
            'disabled',
            'missing',
            'placeholder',
            'pending_later_slice',
            'unsupported',
            'activated'
        )
    ),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_contract_registration_id, facet_key)
);

CREATE INDEX IF NOT EXISTS project_contract_registrations_project_idx
    ON projects.project_contract_registrations (project_id);

CREATE INDEX IF NOT EXISTS project_contract_registrations_hash_idx
    ON projects.project_contract_registrations (contract_hash);

CREATE INDEX IF NOT EXISTS project_contract_facets_project_idx
    ON projects.project_contract_facets (project_id, facet_key);

-- +goose Down
DROP TABLE IF EXISTS projects.project_contract_facets;
DROP TABLE IF EXISTS projects.project_contract_registrations;
