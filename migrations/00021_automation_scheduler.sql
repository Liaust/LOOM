-- +goose Up
CREATE SCHEMA IF NOT EXISTS automation;

CREATE TABLE IF NOT EXISTS automation.automations (
    automation_id text PRIMARY KEY CHECK (automation_id LIKE 'automation_%'),
    automation_key text UNIQUE NOT NULL CHECK (automation_key ~ '^[a-z][a-z0-9_-]{0,80}$'),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'paused', 'disabled', 'archived')),
    source_kind text NOT NULL CHECK (source_kind IN ('schedule')),
    source_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(source_profile_json) = 'object'),
    target_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(target_profile_json) = 'object'),
    communication_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(communication_profile_json) = 'object'),
    idempotency_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(idempotency_profile_json) = 'object'),
    execution_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(execution_profile_json) = 'object'),
    timeout_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(timeout_profile_json) = 'object'),
    retry_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retry_profile_json) = 'object'),
    concurrency_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(concurrency_profile_json) = 'object'),
    misfire_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(misfire_profile_json) = 'object'),
    approval_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(approval_profile_json) = 'object'),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    run_as_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    project_id text NULL REFERENCES projects.projects(project_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_automations_status_idx ON automation.automations (status);
CREATE INDEX IF NOT EXISTS automation_automations_source_status_idx ON automation.automations (source_kind, status);
CREATE INDEX IF NOT EXISTS automation_automations_scope_idx ON automation.automations (scope_id);
CREATE INDEX IF NOT EXISTS automation_automations_project_idx ON automation.automations (project_id);
CREATE INDEX IF NOT EXISTS automation_automations_created_at_idx ON automation.automations (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.schedules (
    schedule_id text PRIMARY KEY CHECK (schedule_id LIKE 'schedule_%'),
    automation_id text NOT NULL REFERENCES automation.automations(automation_id),
    schedule_key text UNIQUE NOT NULL CHECK (schedule_key ~ '^[a-z][a-z0-9_-]{0,80}$'),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'paused', 'disabled', 'completed')),
    schedule_kind text NOT NULL CHECK (schedule_kind IN ('one_shot', 'interval')),
    schedule_expr text NOT NULL,
    timezone text NOT NULL DEFAULT 'UTC',
    start_at timestamptz NULL,
    next_fire_at timestamptz NULL,
    last_fire_at timestamptz NULL,
    last_schedule_fire_id text NULL,
    input_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_json) = 'object'),
    target_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(target_profile_json) = 'object'),
    misfire_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(misfire_profile_json) = 'object'),
    concurrency_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(concurrency_profile_json) = 'object'),
    approval_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(approval_profile_json) = 'object'),
    timeout_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(timeout_profile_json) = 'object'),
    retry_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retry_profile_json) = 'object'),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    run_as_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    project_id text NULL REFERENCES projects.projects(project_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_schedules_status_next_fire_idx ON automation.schedules (status, next_fire_at);
CREATE INDEX IF NOT EXISTS automation_schedules_automation_idx ON automation.schedules (automation_id);
CREATE INDEX IF NOT EXISTS automation_schedules_scope_idx ON automation.schedules (scope_id);
CREATE INDEX IF NOT EXISTS automation_schedules_project_idx ON automation.schedules (project_id);
CREATE INDEX IF NOT EXISTS automation_schedules_created_at_idx ON automation.schedules (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.schedule_fires (
    schedule_fire_id text PRIMARY KEY CHECK (schedule_fire_id LIKE 'schedule_fire_%'),
    schedule_id text NOT NULL REFERENCES automation.schedules(schedule_id),
    automation_id text NOT NULL REFERENCES automation.automations(automation_id),
    scheduled_for timestamptz NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'created',
            'missed',
            'skipped',
            'pending_invocation',
            'invocation_created',
            'completed',
            'failed',
            'requires_manual_action'
        )
    ),
    misfire_status text NOT NULL DEFAULT 'none',
    lateness_seconds integer NOT NULL DEFAULT 0,
    worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id),
    invocation_id text NULL,
    route_id text NULL,
    capability_call_id text NULL,
    job_id text NULL,
    failure_code text NULL,
    failure_message text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object'),
    UNIQUE (schedule_id, scheduled_for)
);

CREATE INDEX IF NOT EXISTS automation_schedule_fires_status_idx ON automation.schedule_fires (status);
CREATE INDEX IF NOT EXISTS automation_schedule_fires_schedule_idx ON automation.schedule_fires (schedule_id, created_at DESC);
CREATE INDEX IF NOT EXISTS automation_schedule_fires_invocation_idx ON automation.schedule_fires (invocation_id);
CREATE INDEX IF NOT EXISTS automation_schedule_fires_created_at_idx ON automation.schedule_fires (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.invocations (
    invocation_id text PRIMARY KEY CHECK (invocation_id LIKE 'invocation_%'),
    automation_id text NOT NULL REFERENCES automation.automations(automation_id),
    source_kind text NOT NULL CHECK (source_kind IN ('schedule')),
    source_ref text NOT NULL,
    source_occurrence_ref text NOT NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    project_id text NULL REFERENCES projects.projects(project_id),
    target_capability text NOT NULL,
    input_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_json) = 'object'),
    input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'pending',
            'leased',
            'calling',
            'waiting_approval',
            'succeeded',
            'failed',
            'timed_out',
            'cancelled',
            'requires_manual_action'
        )
    ),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    max_attempts integer NOT NULL DEFAULT 1 CHECK (max_attempts > 0),
    next_attempt_at timestamptz NULL,
    leased_by_worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id),
    leased_at timestamptz NULL,
    lease_expires_at timestamptz NULL,
    route_id text NULL,
    capability_call_id text NULL,
    job_id text NULL,
    policy_decision_id text NULL,
    approval_id text NULL,
    grant_id text NULL,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    result_refs_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_refs_json) = 'object'),
    failure_code text NULL,
    failure_message text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object'),
    UNIQUE (source_kind, source_occurrence_ref),
    UNIQUE (idempotency_key)
);

CREATE INDEX IF NOT EXISTS automation_invocations_claim_idx ON automation.invocations (status, next_attempt_at, created_at);
CREATE INDEX IF NOT EXISTS automation_invocations_automation_idx ON automation.invocations (automation_id);
CREATE INDEX IF NOT EXISTS automation_invocations_source_idx ON automation.invocations (source_kind, source_ref, source_occurrence_ref);
CREATE INDEX IF NOT EXISTS automation_invocations_capability_call_idx ON automation.invocations (capability_call_id);
CREATE INDEX IF NOT EXISTS automation_invocations_route_idx ON automation.invocations (route_id);
CREATE INDEX IF NOT EXISTS automation_invocations_job_idx ON automation.invocations (job_id);
CREATE INDEX IF NOT EXISTS automation_invocations_created_at_idx ON automation.invocations (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS automation.invocations;
DROP TABLE IF EXISTS automation.schedule_fires;
DROP TABLE IF EXISTS automation.schedules;
DROP TABLE IF EXISTS automation.automations;

DROP SCHEMA IF EXISTS automation;
