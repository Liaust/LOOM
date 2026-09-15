-- +goose Up
CREATE TABLE IF NOT EXISTS projects.repositories (
    repository_id text PRIMARY KEY CHECK (repository_id ~ '^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    owning_project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    lifecycle_status text NOT NULL CHECK (lifecycle_status IN ('active', 'archived')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (repository_id, owning_project_id)
);

CREATE INDEX IF NOT EXISTS projects_repositories_owner_lifecycle_idx
ON projects.repositories (owning_project_id, lifecycle_status, repository_id);

CREATE TABLE IF NOT EXISTS projects.project_repository_memberships (
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    repository_id text NOT NULL,
    repository_owner_project_id text NOT NULL,
    member_key text NOT NULL CHECK (member_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    member_path text NOT NULL CHECK (
        member_path <> ''
        AND member_path !~ '^/'
        AND member_path !~ '(^|/)\.\.(/|$)'
    ),
    role text NOT NULL CHECK (role IN ('primary', 'component', 'reference')),
    state_root text NOT NULL DEFAULT '',
    lifecycle_status text NOT NULL CHECK (lifecycle_status IN ('active', 'archived')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, repository_id),
    FOREIGN KEY (repository_id, repository_owner_project_id)
        REFERENCES projects.repositories(repository_id, owning_project_id)
        ON DELETE CASCADE,
    CHECK (
        (role IN ('primary', 'component') AND project_id = repository_owner_project_id)
        OR (role = 'reference' AND project_id <> repository_owner_project_id)
    ),
    UNIQUE (project_id, member_key),
    UNIQUE (project_id, member_path)
);

CREATE UNIQUE INDEX IF NOT EXISTS projects_repository_memberships_owning_idx
ON projects.project_repository_memberships (repository_id)
WHERE role IN ('primary', 'component');

CREATE INDEX IF NOT EXISTS projects_repository_memberships_project_lifecycle_idx
ON projects.project_repository_memberships (project_id, lifecycle_status, member_key);

ALTER TABLE projects.project_contract_registrations
ADD CONSTRAINT project_contract_registrations_registration_project_key
UNIQUE (project_contract_registration_id, project_id);

CREATE TABLE IF NOT EXISTS projects.project_repository_sources (
    project_id text PRIMARY KEY REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    project_contract_registration_id text UNIQUE NOT NULL,
    project_contract_schema_version text NOT NULL CHECK (
        project_contract_schema_version IN ('project.contract.v0.3', 'project.contract.v0.4')
    ),
    repos_contract_schema_version text NOT NULL CHECK (
        repos_contract_schema_version IN ('repos.contract.v0.3', 'repos.contract.v0.4')
    ),
    project_root text NOT NULL,
    project_contract_path text NOT NULL,
    repos_contract_path text NOT NULL,
    project_contract_digest text NOT NULL CHECK (project_contract_digest ~ '^sha256:[0-9a-f]{64}$'),
    repos_contract_digest text NOT NULL CHECK (repos_contract_digest ~ '^sha256:[0-9a-f]{64}$'),
    semantic_digest text NOT NULL CHECK (semantic_digest ~ '^sha256:[0-9a-f]{64}$'),
    location_digest text NOT NULL CHECK (location_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot_json) = 'object'),
    source_revision bigint NOT NULL CHECK (source_revision > 0),
    registered_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    registered_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT project_repository_sources_registration_project_fkey
        FOREIGN KEY (project_contract_registration_id, project_id)
        REFERENCES projects.project_contract_registrations(project_contract_registration_id, project_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS projects_repository_sources_registration_idx
ON projects.project_repository_sources (project_contract_registration_id);

CREATE TABLE IF NOT EXISTS projects.project_repository_source_history (
    project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE CASCADE,
    project_contract_schema_version text NOT NULL CHECK (
        project_contract_schema_version IN ('project.contract.v0.3', 'project.contract.v0.4')
    ),
    repos_contract_schema_version text NOT NULL CHECK (
        repos_contract_schema_version IN ('repos.contract.v0.3', 'repos.contract.v0.4')
    ),
    project_root text NOT NULL,
    project_contract_path text NOT NULL,
    repos_contract_path text NOT NULL,
    project_contract_digest text NOT NULL CHECK (project_contract_digest ~ '^sha256:[0-9a-f]{64}$'),
    repos_contract_digest text NOT NULL CHECK (repos_contract_digest ~ '^sha256:[0-9a-f]{64}$'),
    semantic_digest text NOT NULL CHECK (semantic_digest ~ '^sha256:[0-9a-f]{64}$'),
    location_digest text NOT NULL CHECK (location_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(source_snapshot_json) = 'object'),
    source_revision bigint NOT NULL CHECK (source_revision > 0),
    source_change_kind text NOT NULL CHECK (
        source_change_kind IN (
            'first_registration',
            'semantic_change',
            'source_relocation',
            'semantic_change_and_relocation'
        )
    ),
    change_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(change_summary_json) = 'object'),
    accepted_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    accepted_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, source_revision)
);

CREATE INDEX IF NOT EXISTS projects_repository_source_history_project_idx
ON projects.project_repository_source_history (project_id, source_revision DESC);

CREATE TABLE IF NOT EXISTS projects.project_repository_observations (
    project_id text NOT NULL,
    repository_id text NOT NULL,
    source_binding_digest text NOT NULL CHECK (source_binding_digest ~ '^sha256:[0-9a-f]{64}$'),
    observation_posture text NOT NULL CHECK (
        observation_posture IN ('not_observed', 'observed', 'remote_unavailable')
    ),
    reason_code text NOT NULL DEFAULT '' CHECK (length(reason_code) <= 128),
    observed_at timestamptz NULL,
    observation_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(observation_json) = 'object'),
    observation_revision bigint NOT NULL DEFAULT 1 CHECK (observation_revision > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_id, repository_id),
    FOREIGN KEY (project_id, repository_id)
        REFERENCES projects.project_repository_memberships(project_id, repository_id)
        ON DELETE CASCADE,
    CHECK (
        (observation_posture = 'not_observed' AND observed_at IS NULL)
        OR (observation_posture IN ('observed', 'remote_unavailable') AND observed_at IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS projects_repository_observations_posture_idx
ON projects.project_repository_observations (project_id, observation_posture, repository_id);

-- +goose Down
DROP TABLE IF EXISTS projects.project_repository_observations;
DROP TABLE IF EXISTS projects.project_repository_source_history;
DROP TABLE IF EXISTS projects.project_repository_sources;
DROP TABLE IF EXISTS projects.project_repository_memberships;
DROP TABLE IF EXISTS projects.repositories;
ALTER TABLE projects.project_contract_registrations
DROP CONSTRAINT IF EXISTS project_contract_registrations_registration_project_key;
