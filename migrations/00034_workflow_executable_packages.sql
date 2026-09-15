-- +goose Up
CREATE SCHEMA IF NOT EXISTS packages;

CREATE TABLE IF NOT EXISTS packages.workflows (
    workflow_id text PRIMARY KEY CHECK (workflow_id LIKE 'workflow_%'),
    slug text UNIQUE NOT NULL CHECK (slug ~ '^[a-z][a-z0-9_-]{0,62}$'),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    owner_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    active_version_id text NULL,
    status text NOT NULL CHECK (
        status IN (
            'registered',
            'active',
            'deprecated',
            'disabled',
            'revoked'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS packages.workflow_versions (
    workflow_version_id text PRIMARY KEY CHECK (workflow_version_id LIKE 'workflow_version_%'),
    workflow_id text NOT NULL REFERENCES packages.workflows(workflow_id),
    version_label text NOT NULL,
    manifest_json jsonb NOT NULL,
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    package_root text NOT NULL,
    entrypoint_json jsonb NOT NULL,
    runtime_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    input_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    execution_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    artifact_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    usage_documents_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL CHECK (
        status IN (
            'active',
            'pending_review',
            'disabled',
            'revoked',
            'superseded'
        )
    ),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (workflow_id, version_label, content_hash)
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'packages_workflows_active_version_id_fkey'
    ) THEN
        ALTER TABLE packages.workflows
        ADD CONSTRAINT packages_workflows_active_version_id_fkey
        FOREIGN KEY (active_version_id)
        REFERENCES packages.workflow_versions(workflow_version_id);
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_job_type_check'
    ) THEN
        ALTER TABLE jobs.jobs
        DROP CONSTRAINT jobs_job_type_check;
    END IF;

    IF EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_job_type_check'
    ) THEN
        ALTER TABLE jobs.jobs
        DROP CONSTRAINT jobs_jobs_job_type_check;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_job_type_v034_check'
    ) THEN
        ALTER TABLE jobs.jobs
        ADD CONSTRAINT jobs_jobs_job_type_v034_check
        CHECK (job_type IN ('script_run', 'workflow_run'));
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE jobs.jobs
    ADD COLUMN IF NOT EXISTS workflow_id text NULL REFERENCES packages.workflows(workflow_id),
    ADD COLUMN IF NOT EXISTS workflow_version_id text NULL REFERENCES packages.workflow_versions(workflow_version_id);

CREATE TABLE IF NOT EXISTS projects.project_workflow_registrations (
    project_workflow_registration_id text PRIMARY KEY CHECK (project_workflow_registration_id LIKE 'project_workflow_registration_%'),
    project_contract_registration_id text NOT NULL REFERENCES projects.project_contract_registrations(project_contract_registration_id) ON DELETE CASCADE,
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    workflow_key text NOT NULL,
    workflow_folder text NOT NULL DEFAULT '',
    workflow_manifest_path text NOT NULL DEFAULT '',
    workflow_manifest_hash text NOT NULL DEFAULT '',
    implementation_kind text NOT NULL DEFAULT '',
    runtime_kind text NOT NULL DEFAULT '',
    workflow_id text NULL REFERENCES packages.workflows(workflow_id),
    workflow_version_id text NULL REFERENCES packages.workflow_versions(workflow_version_id),
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    provider_address text NOT NULL DEFAULT '',
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
    UNIQUE (project_id, workflow_key)
);

CREATE INDEX IF NOT EXISTS packages_workflows_owner_scope_idx ON packages.workflows (owner_scope_id);
CREATE INDEX IF NOT EXISTS packages_workflows_status_idx ON packages.workflows (status);
CREATE INDEX IF NOT EXISTS packages_workflow_versions_workflow_idx ON packages.workflow_versions (workflow_id, created_at DESC);
CREATE INDEX IF NOT EXISTS packages_workflow_versions_status_idx ON packages.workflow_versions (status);
CREATE INDEX IF NOT EXISTS packages_workflow_versions_content_hash_idx ON packages.workflow_versions (content_hash);
CREATE INDEX IF NOT EXISTS jobs_jobs_workflow_idx ON jobs.jobs (workflow_id, workflow_version_id);
CREATE INDEX IF NOT EXISTS project_workflow_registrations_registration_idx
ON projects.project_workflow_registrations (project_contract_registration_id);
CREATE INDEX IF NOT EXISTS project_workflow_registrations_project_status_idx
ON projects.project_workflow_registrations (project_id, activation_status);
CREATE INDEX IF NOT EXISTS project_workflow_registrations_workflow_idx
ON projects.project_workflow_registrations (workflow_id, workflow_version_id);
CREATE INDEX IF NOT EXISTS project_workflow_registrations_capability_idx
ON projects.project_workflow_registrations (project_id, capability_address)
WHERE capability_address <> '';

-- +goose Down
DROP TABLE IF EXISTS projects.project_workflow_registrations;

DROP INDEX IF EXISTS jobs.jobs_jobs_workflow_idx;
DROP INDEX IF EXISTS packages.packages_workflow_versions_content_hash_idx;
DROP INDEX IF EXISTS packages.packages_workflow_versions_status_idx;
DROP INDEX IF EXISTS packages.packages_workflow_versions_workflow_idx;
DROP INDEX IF EXISTS packages.packages_workflows_status_idx;
DROP INDEX IF EXISTS packages.packages_workflows_owner_scope_idx;

ALTER TABLE jobs.jobs
    DROP COLUMN IF EXISTS workflow_version_id,
    DROP COLUMN IF EXISTS workflow_id;

ALTER TABLE jobs.jobs DROP CONSTRAINT IF EXISTS jobs_jobs_job_type_v034_check;
ALTER TABLE jobs.jobs ADD CONSTRAINT jobs_job_type_check CHECK (job_type IN ('script_run'));

ALTER TABLE packages.workflows DROP CONSTRAINT IF EXISTS packages_workflows_active_version_id_fkey;
DROP TABLE IF EXISTS packages.workflow_versions;
DROP TABLE IF EXISTS packages.workflows;
