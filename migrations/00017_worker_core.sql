-- +goose Up

CREATE SCHEMA IF NOT EXISTS workers;

CREATE TABLE IF NOT EXISTS workers.worker_kinds (
    worker_kind text PRIMARY KEY,
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    runtime_owner text NOT NULL CHECK (runtime_owner IN ('loomd', 'loom-node-agent', 'trusted_module')),
    runtime_package text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'deprecated', 'disabled')),
    supported_localities text[] NOT NULL DEFAULT '{}',
    may_create_jobs boolean NOT NULL DEFAULT false,
    may_call_capabilities boolean NOT NULL DEFAULT false,
    may_touch_filesystem boolean NOT NULL DEFAULT false,
    may_store_raw_payloads boolean NOT NULL DEFAULT false,
    default_tick_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_tick_policy_json) = 'object'),
    default_concurrency_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_concurrency_policy_json) = 'object'),
    default_retry_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_retry_policy_json) = 'object'),
    default_timeout_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_timeout_policy_json) = 'object'),
    default_resource_limits_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_resource_limits_json) = 'object'),
    config_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config_schema_json) = 'object'),
    checkpoint_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(checkpoint_schema_json) = 'object'),
    result_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_schema_json) = 'object'),
    registered_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (worker_kind ~ '^[a-z][a-z0-9_:-]{0,127}$')
);

CREATE INDEX IF NOT EXISTS workers_worker_kinds_status_idx ON workers.worker_kinds (status);
CREATE INDEX IF NOT EXISTS workers_worker_kinds_runtime_owner_idx ON workers.worker_kinds (runtime_owner);

CREATE TABLE IF NOT EXISTS workers.worker_instances (
    worker_instance_id text PRIMARY KEY CHECK (worker_instance_id LIKE 'worker_instance_%'),
    worker_key text NOT NULL UNIQUE,
    worker_kind text NOT NULL REFERENCES workers.worker_kinds(worker_kind),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    owner_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    host_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    project_id text NULL REFERENCES projects.projects(project_id),
    locality text NOT NULL CHECK (locality IN ('main_owned', 'node_agent_owned', 'external_reported')),
    lifecycle_status text NOT NULL DEFAULT 'registered' CHECK (lifecycle_status IN ('registered', 'active', 'degraded', 'failed', 'disabled', 'retired')),
    enabled boolean NOT NULL DEFAULT false,
    paused boolean NOT NULL DEFAULT false,
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config_json) = 'object'),
    tick_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(tick_policy_json) = 'object'),
    concurrency_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(concurrency_policy_json) = 'object'),
    retry_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retry_policy_json) = 'object'),
    timeout_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(timeout_policy_json) = 'object'),
    resource_limits_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_limits_json) = 'object'),
    visibility_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(visibility_json) = 'object'),
    current_run_id text NULL,
    last_run_id text NULL,
    last_success_at timestamptz NULL,
    last_failure_at timestamptz NULL,
    last_heartbeat_at timestamptz NULL,
    next_run_after timestamptz NULL,
    backoff_until timestamptz NULL,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (worker_key ~ '^[a-z0-9][a-z0-9_.:-]{0,191}$')
);

CREATE INDEX IF NOT EXISTS workers_worker_instances_kind_idx ON workers.worker_instances (worker_kind);
CREATE INDEX IF NOT EXISTS workers_worker_instances_host_node_idx ON workers.worker_instances (host_node_id);
CREATE INDEX IF NOT EXISTS workers_worker_instances_owner_node_idx ON workers.worker_instances (owner_node_id);
CREATE INDEX IF NOT EXISTS workers_worker_instances_locality_idx ON workers.worker_instances (locality);
CREATE INDEX IF NOT EXISTS workers_worker_instances_lifecycle_idx ON workers.worker_instances (lifecycle_status);
CREATE INDEX IF NOT EXISTS workers_worker_instances_enabled_paused_idx ON workers.worker_instances (enabled, paused);
CREATE INDEX IF NOT EXISTS workers_worker_instances_next_run_idx ON workers.worker_instances (next_run_after);

CREATE TABLE IF NOT EXISTS workers.worker_runs (
    worker_run_id text PRIMARY KEY CHECK (worker_run_id LIKE 'worker_run_%'),
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    worker_kind text NOT NULL REFERENCES workers.worker_kinds(worker_kind),
    run_status text NOT NULL CHECK (run_status IN ('starting', 'running', 'succeeded', 'failed', 'cancelled', 'timed_out')),
    trigger_kind text NOT NULL CHECK (trigger_kind IN ('manual', 'supervisor_tick', 'schedule', 'direct_event', 'retry', 'startup')),
    trigger_ref text NOT NULL DEFAULT '',
    lease_id text NULL,
    lease_generation bigint NULL,
    correlation_id text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz NULL,
    deadline_at timestamptz NULL,
    result_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_summary_json) = 'object'),
    counters_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(counters_json) = 'object'),
    resource_usage_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_usage_json) = 'object'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    retryable boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (run_status NOT IN ('succeeded', 'failed', 'cancelled', 'timed_out') OR finished_at IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS workers_worker_runs_instance_started_idx ON workers.worker_runs (worker_instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_runs_kind_started_idx ON workers.worker_runs (worker_kind, started_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_runs_status_started_idx ON workers.worker_runs (run_status, started_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_runs_correlation_idx ON workers.worker_runs (correlation_id) WHERE correlation_id <> '';

ALTER TABLE workers.worker_instances
    ADD CONSTRAINT workers_worker_instances_current_run_fk
    FOREIGN KEY (current_run_id) REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL;

ALTER TABLE workers.worker_instances
    ADD CONSTRAINT workers_worker_instances_last_run_fk
    FOREIGN KEY (last_run_id) REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS workers.worker_leases (
    worker_lease_id text PRIMARY KEY CHECK (worker_lease_id LIKE 'worker_lease_%'),
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    lease_key text NOT NULL,
    lease_status text NOT NULL CHECK (lease_status IN ('active', 'released', 'expired', 'stolen')),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    holder_id text NOT NULL,
    holder_kind text NOT NULL CHECK (holder_kind IN ('loomd', 'loom-node-agent', 'cli')),
    run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    renewed_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    released_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (expires_at > acquired_at)
);

CREATE INDEX IF NOT EXISTS workers_worker_leases_instance_status_idx ON workers.worker_leases (worker_instance_id, lease_status);
CREATE INDEX IF NOT EXISTS workers_worker_leases_key_status_idx ON workers.worker_leases (lease_key, lease_status);
CREATE INDEX IF NOT EXISTS workers_worker_leases_active_expires_idx ON workers.worker_leases (expires_at) WHERE lease_status = 'active';
CREATE UNIQUE INDEX IF NOT EXISTS workers_worker_leases_active_key_unique_idx ON workers.worker_leases (lease_key) WHERE lease_status = 'active';

CREATE TABLE IF NOT EXISTS workers.worker_checkpoints (
    worker_checkpoint_id text PRIMARY KEY CHECK (worker_checkpoint_id LIKE 'worker_checkpoint_%'),
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    checkpoint_key text NOT NULL,
    checkpoint_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(checkpoint_json) = 'object'),
    schema_version text NOT NULL DEFAULT '',
    updated_by_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (worker_instance_id, checkpoint_key),
    CHECK (checkpoint_key ~ '^[a-z0-9][a-z0-9_.:-]{0,127}$')
);

CREATE INDEX IF NOT EXISTS workers_worker_checkpoints_instance_updated_idx ON workers.worker_checkpoints (worker_instance_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_checkpoints_run_idx ON workers.worker_checkpoints (updated_by_run_id);

CREATE TABLE IF NOT EXISTS workers.worker_controls (
    worker_control_id text PRIMARY KEY CHECK (worker_control_id LIKE 'worker_control_%'),
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    control_kind text NOT NULL CHECK (control_kind IN ('run_once', 'pause', 'resume', 'disable', 'force_release_lease')),
    control_status text NOT NULL CHECK (control_status IN ('requested', 'applied', 'rejected', 'expired', 'failed')),
    requested_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    requested_at timestamptz NOT NULL DEFAULT now(),
    applied_by_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    applied_at timestamptz NULL,
    expires_at timestamptz NULL,
    input_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_json) = 'object'),
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS workers_worker_controls_instance_requested_idx ON workers.worker_controls (worker_instance_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_controls_status_requested_idx ON workers.worker_controls (control_status, requested_at DESC);

CREATE TABLE IF NOT EXISTS workers.worker_heartbeats (
    worker_heartbeat_id text PRIMARY KEY CHECK (worker_heartbeat_id LIKE 'worker_heartbeat_%'),
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    host_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    heartbeat_status text NOT NULL CHECK (heartbeat_status IN ('idle', 'running', 'paused', 'disabled', 'unhealthy')),
    current_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    observed_at timestamptz NOT NULL DEFAULT now(),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS workers_worker_heartbeats_instance_observed_idx ON workers.worker_heartbeats (worker_instance_id, observed_at DESC);
CREATE INDEX IF NOT EXISTS workers_worker_heartbeats_host_observed_idx ON workers.worker_heartbeats (host_node_id, observed_at DESC);

CREATE TABLE IF NOT EXISTS workers.worker_health (
    worker_health_id text PRIMARY KEY CHECK (worker_health_id LIKE 'worker_health_%'),
    worker_instance_id text NOT NULL UNIQUE REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    health_status text NOT NULL CHECK (health_status IN ('unknown', 'healthy', 'running', 'lagging', 'degraded', 'failed', 'paused', 'disabled')),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    summary text NOT NULL DEFAULT '',
    attention_required boolean NOT NULL DEFAULT false,
    last_success_at timestamptz NULL,
    last_failure_at timestamptz NULL,
    current_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    queue_depth integer NOT NULL DEFAULT 0 CHECK (queue_depth >= 0),
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures >= 0),
    computed_at timestamptz NOT NULL DEFAULT now(),
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS workers_worker_health_status_idx ON workers.worker_health (health_status);
CREATE INDEX IF NOT EXISTS workers_worker_health_attention_idx ON workers.worker_health (attention_required, severity);
CREATE INDEX IF NOT EXISTS workers_worker_health_computed_idx ON workers.worker_health (computed_at DESC);

-- +goose Down

ALTER TABLE IF EXISTS workers.worker_instances
    DROP CONSTRAINT IF EXISTS workers_worker_instances_current_run_fk;

ALTER TABLE IF EXISTS workers.worker_instances
    DROP CONSTRAINT IF EXISTS workers_worker_instances_last_run_fk;

DROP TABLE IF EXISTS workers.worker_health;
DROP TABLE IF EXISTS workers.worker_heartbeats;
DROP TABLE IF EXISTS workers.worker_controls;
DROP TABLE IF EXISTS workers.worker_checkpoints;
DROP TABLE IF EXISTS workers.worker_leases;
DROP TABLE IF EXISTS workers.worker_runs;
DROP TABLE IF EXISTS workers.worker_instances;
DROP TABLE IF EXISTS workers.worker_kinds;

DROP SCHEMA IF EXISTS workers;
