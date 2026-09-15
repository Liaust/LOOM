-- +goose Up
CREATE SCHEMA IF NOT EXISTS packages;
CREATE SCHEMA IF NOT EXISTS jobs;

CREATE TABLE IF NOT EXISTS packages.scripts (
    script_id text PRIMARY KEY CHECK (script_id LIKE 'script_%'),
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

CREATE TABLE IF NOT EXISTS packages.script_versions (
    script_version_id text PRIMARY KEY CHECK (script_version_id LIKE 'script_version_%'),
    script_id text NOT NULL REFERENCES packages.scripts(script_id),
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
    UNIQUE (script_id, version_label, content_hash)
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'packages_scripts_active_version_id_fkey'
    ) THEN
        ALTER TABLE packages.scripts
        ADD CONSTRAINT packages_scripts_active_version_id_fkey
        FOREIGN KEY (active_version_id)
        REFERENCES packages.script_versions(script_version_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS jobs.runners (
    runner_id text PRIMARY KEY CHECK (runner_id LIKE 'runner_%'),
    runner_key text UNIQUE NOT NULL,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    runner_type text NOT NULL,
    supported_job_types jsonb NOT NULL DEFAULT '[]'::jsonb,
    supported_runtimes jsonb NOT NULL DEFAULT '[]'::jsonb,
    status text NOT NULL CHECK (
        status IN (
            'starting',
            'idle',
            'running',
            'draining',
            'offline',
            'failed'
        )
    ),
    last_heartbeat_at timestamptz NULL,
    current_job_id text NULL,
    version text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS jobs.jobs (
    job_id text PRIMARY KEY CHECK (job_id LIKE 'job_%'),
    job_type text NOT NULL CHECK (job_type IN ('script_run')),
    status text NOT NULL CHECK (
        status IN (
            'created',
            'queued',
            'running',
            'completed',
            'failed',
            'cancelled',
            'timed_out'
        )
    ),
    origin_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    execution_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    target_kind text NULL,
    target_id text NULL,
    script_id text NULL REFERENCES packages.scripts(script_id),
    script_version_id text NULL REFERENCES packages.script_versions(script_version_id),
    source_object_id text NULL REFERENCES objects.objects(object_id),
    source_object_version_id text NULL REFERENCES objects.object_versions(object_version_id),
    input_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    output_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    progress_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    artifact_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb,
    workdir_path text NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL DEFAULT 1 CHECK (max_attempts > 0),
    timeout_seconds integer NULL CHECK (timeout_seconds IS NULL OR timeout_seconds > 0),
    lease_owner text NULL REFERENCES jobs.runners(runner_id),
    lease_expires_at timestamptz NULL,
    exit_code integer NULL,
    failure_code text NULL,
    failure_message text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    queued_at timestamptz NULL,
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    cancelled_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_runners_current_job_id_fkey'
    ) THEN
        ALTER TABLE jobs.runners
        ADD CONSTRAINT jobs_runners_current_job_id_fkey
        FOREIGN KEY (current_job_id)
        REFERENCES jobs.jobs(job_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'objects_object_versions_created_by_job_id_fkey'
    ) THEN
        ALTER TABLE objects.object_versions
        ADD CONSTRAINT objects_object_versions_created_by_job_id_fkey
        FOREIGN KEY (created_by_job_id)
        REFERENCES jobs.jobs(job_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'events_events_job_id_fkey'
    ) THEN
        ALTER TABLE events.events
        ADD CONSTRAINT events_events_job_id_fkey
        FOREIGN KEY (job_id)
        REFERENCES jobs.jobs(job_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS jobs.job_attempts (
    job_attempt_id text PRIMARY KEY CHECK (job_attempt_id LIKE 'job_attempt_%'),
    job_id text NOT NULL REFERENCES jobs.jobs(job_id),
    attempt_number integer NOT NULL CHECK (attempt_number > 0),
    runner_id text NULL REFERENCES jobs.runners(runner_id),
    status text NOT NULL CHECK (
        status IN (
            'created',
            'running',
            'completed',
            'failed',
            'timed_out',
            'cancelled'
        )
    ),
    workdir_path text NOT NULL,
    input_dir text NOT NULL,
    output_dir text NOT NULL,
    artifact_dir text NOT NULL,
    temp_dir text NOT NULL,
    stdout_log_path text NULL,
    stderr_log_path text NULL,
    runner_log_path text NULL,
    result_path text NULL,
    exit_code integer NULL,
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (job_id, attempt_number)
);

CREATE TABLE IF NOT EXISTS jobs.artifacts (
    artifact_id text PRIMARY KEY CHECK (artifact_id LIKE 'artifact_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    object_version_id text NULL REFERENCES objects.object_versions(object_version_id),
    blob_id text NULL REFERENCES files.blobs(blob_id),
    job_id text NOT NULL REFERENCES jobs.jobs(job_id),
    job_attempt_id text NULL REFERENCES jobs.job_attempts(job_attempt_id),
    script_id text NULL REFERENCES packages.scripts(script_id),
    script_version_id text NULL REFERENCES packages.script_versions(script_version_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    source_object_id text NULL REFERENCES objects.objects(object_id),
    artifact_type text NOT NULL,
    title text NOT NULL DEFAULT '',
    hash_uri text NULL CHECK (hash_uri IS NULL OR hash_uri ~ '^sha256:[0-9a-f]{64}$'),
    mime_type text NULL,
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    status text NOT NULL CHECK (
        status IN (
            'created',
            'indexed',
            'failed',
            'archived'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS jobs.job_logs (
    job_log_id text PRIMARY KEY CHECK (job_log_id LIKE 'job_log_%'),
    job_id text NOT NULL REFERENCES jobs.jobs(job_id),
    job_attempt_id text NULL REFERENCES jobs.job_attempts(job_attempt_id),
    stream text NOT NULL CHECK (stream IN ('stdout', 'stderr', 'runner')),
    storage_kind text NOT NULL DEFAULT 'file' CHECK (storage_kind IN ('file')),
    path text NOT NULL,
    byte_count bigint NOT NULL DEFAULT 0 CHECK (byte_count >= 0),
    tail_text text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS jobs.job_outputs (
    job_output_id text PRIMARY KEY CHECK (job_output_id LIKE 'job_output_%'),
    job_id text NOT NULL REFERENCES jobs.jobs(job_id),
    job_attempt_id text NULL REFERENCES jobs.job_attempts(job_attempt_id),
    output_key text NOT NULL,
    output_type text NOT NULL,
    value_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    artifact_id text NULL REFERENCES jobs.artifacts(artifact_id),
    status text NOT NULL CHECK (status IN ('created', 'failed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS packages_scripts_owner_scope_idx ON packages.scripts (owner_scope_id);
CREATE INDEX IF NOT EXISTS packages_scripts_status_idx ON packages.scripts (status);
CREATE INDEX IF NOT EXISTS packages_script_versions_script_idx ON packages.script_versions (script_id, created_at DESC);
CREATE INDEX IF NOT EXISTS packages_script_versions_status_idx ON packages.script_versions (status);
CREATE INDEX IF NOT EXISTS packages_script_versions_content_hash_idx ON packages.script_versions (content_hash);

CREATE INDEX IF NOT EXISTS jobs_runners_node_idx ON jobs.runners (node_id);
CREATE INDEX IF NOT EXISTS jobs_runners_status_idx ON jobs.runners (status);
CREATE INDEX IF NOT EXISTS jobs_jobs_status_idx ON jobs.jobs (status);
CREATE INDEX IF NOT EXISTS jobs_jobs_type_status_idx ON jobs.jobs (job_type, status);
CREATE INDEX IF NOT EXISTS jobs_jobs_scope_idx ON jobs.jobs (scope_id);
CREATE INDEX IF NOT EXISTS jobs_jobs_script_idx ON jobs.jobs (script_id, script_version_id);
CREATE INDEX IF NOT EXISTS jobs_jobs_source_object_idx ON jobs.jobs (source_object_id);
CREATE INDEX IF NOT EXISTS jobs_jobs_execution_node_idx ON jobs.jobs (execution_node_id);
CREATE INDEX IF NOT EXISTS jobs_jobs_lease_idx ON jobs.jobs (status, execution_node_id, lease_expires_at);
CREATE INDEX IF NOT EXISTS jobs_jobs_created_at_idx ON jobs.jobs (created_at DESC);

CREATE INDEX IF NOT EXISTS jobs_job_attempts_job_idx ON jobs.job_attempts (job_id, attempt_number);
CREATE INDEX IF NOT EXISTS jobs_job_logs_job_idx ON jobs.job_logs (job_id, stream);
CREATE INDEX IF NOT EXISTS jobs_job_outputs_job_idx ON jobs.job_outputs (job_id);
CREATE INDEX IF NOT EXISTS jobs_artifacts_job_idx ON jobs.artifacts (job_id);
CREATE INDEX IF NOT EXISTS jobs_artifacts_object_idx ON jobs.artifacts (object_id);
CREATE INDEX IF NOT EXISTS jobs_artifacts_scope_idx ON jobs.artifacts (scope_id);
CREATE INDEX IF NOT EXISTS jobs_artifacts_source_object_idx ON jobs.artifacts (source_object_id);

-- +goose Down
ALTER TABLE IF EXISTS events.events DROP CONSTRAINT IF EXISTS events_events_job_id_fkey;
ALTER TABLE IF EXISTS objects.object_versions DROP CONSTRAINT IF EXISTS objects_object_versions_created_by_job_id_fkey;
DROP SCHEMA IF EXISTS jobs CASCADE;
DROP SCHEMA IF EXISTS packages CASCADE;
