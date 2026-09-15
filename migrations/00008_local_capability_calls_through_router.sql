-- +goose Up
CREATE SCHEMA IF NOT EXISTS routing;

CREATE TABLE IF NOT EXISTS routing.routes (
    route_id text PRIMARY KEY CHECK (route_id LIKE 'route_%'),
    route_key text UNIQUE NOT NULL,
    correlation_id text NOT NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    origin_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    runtime_node_id text NULL REFERENCES nodes.nodes(node_id),
    target_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    provider_id text NOT NULL REFERENCES capabilities.providers(provider_id),
    capability_endpoint_id text NOT NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    capability_class_id text NULL REFERENCES capabilities.capability_classes(capability_class_id),
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    grant_id text NULL REFERENCES policy.grants(grant_id),
    job_id text NULL REFERENCES jobs.jobs(job_id),
    route_kind text NOT NULL CHECK (
        route_kind IN (
            'local',
            'remote'
        )
    ),
    execution_mode text NOT NULL CHECK (
        execution_mode IN (
            'immediate',
            'job',
            'session',
            'stream'
        )
    ),
    selected_path_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(selected_path_json) = 'object'),
    request_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(request_summary_json) = 'object'),
    result_target_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_target_json) = 'object'),
    status text NOT NULL CHECK (
        status IN (
            'planned',
            'authorized',
            'waiting_for_approval',
            'queued',
            'dispatched',
            'executing',
            'completed',
            'failed',
            'cancelled',
            'expired'
        )
    ),
    failure_code text NULL,
    failure_message text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    authorized_at timestamptz NULL,
    dispatched_at timestamptz NULL,
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS routing_routes_correlation_idx ON routing.routes (correlation_id);
CREATE INDEX IF NOT EXISTS routing_routes_actor_idx ON routing.routes (actor_id);
CREATE INDEX IF NOT EXISTS routing_routes_origin_node_idx ON routing.routes (origin_node_id);
CREATE INDEX IF NOT EXISTS routing_routes_origin_scope_idx ON routing.routes (origin_scope_id);
CREATE INDEX IF NOT EXISTS routing_routes_runtime_node_idx ON routing.routes (runtime_node_id);
CREATE INDEX IF NOT EXISTS routing_routes_target_node_idx ON routing.routes (target_node_id);
CREATE INDEX IF NOT EXISTS routing_routes_provider_idx ON routing.routes (provider_id);
CREATE INDEX IF NOT EXISTS routing_routes_capability_endpoint_idx ON routing.routes (capability_endpoint_id);
CREATE INDEX IF NOT EXISTS routing_routes_policy_decision_idx ON routing.routes (policy_decision_id);
CREATE INDEX IF NOT EXISTS routing_routes_approval_idx ON routing.routes (approval_id);
CREATE INDEX IF NOT EXISTS routing_routes_grant_idx ON routing.routes (grant_id);
CREATE INDEX IF NOT EXISTS routing_routes_job_idx ON routing.routes (job_id);
CREATE INDEX IF NOT EXISTS routing_routes_status_idx ON routing.routes (status);
CREATE INDEX IF NOT EXISTS routing_routes_created_at_desc_idx ON routing.routes (created_at DESC);
CREATE INDEX IF NOT EXISTS routing_routes_updated_at_desc_idx ON routing.routes (updated_at DESC);

CREATE TABLE IF NOT EXISTS routing.capability_calls (
    capability_call_id text PRIMARY KEY CHECK (capability_call_id LIKE 'capability_call_%'),
    capability_call_key text UNIQUE NOT NULL,
    route_id text NOT NULL REFERENCES routing.routes(route_id),
    correlation_id text NOT NULL,
    idempotency_key text NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    target_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    provider_id text NOT NULL REFERENCES capabilities.providers(provider_id),
    capability_endpoint_id text NOT NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    operation text NOT NULL,
    execution_mode text NOT NULL CHECK (
        execution_mode IN (
            'immediate',
            'job',
            'session',
            'stream'
        )
    ),
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    grant_id text NULL REFERENCES policy.grants(grant_id),
    job_id text NULL REFERENCES jobs.jobs(job_id),
    status text NOT NULL CHECK (
        status IN (
            'planned',
            'approval_required',
            'authorized',
            'dispatched',
            'executing',
            'completed',
            'failed',
            'cancelled'
        )
    ),
    input_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_json) = 'object'),
    input_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_summary_json) = 'object'),
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    result_refs_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_refs_json) = 'object'),
    error_code text NULL,
    error_message text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS routing_capability_calls_route_idx ON routing.capability_calls (route_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_correlation_idx ON routing.capability_calls (correlation_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_idempotency_key_idx ON routing.capability_calls (idempotency_key);
CREATE INDEX IF NOT EXISTS routing_capability_calls_actor_idx ON routing.capability_calls (actor_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_origin_node_idx ON routing.capability_calls (origin_node_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_scope_idx ON routing.capability_calls (scope_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_target_node_idx ON routing.capability_calls (target_node_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_provider_idx ON routing.capability_calls (provider_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_endpoint_idx ON routing.capability_calls (capability_endpoint_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_policy_decision_idx ON routing.capability_calls (policy_decision_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_approval_idx ON routing.capability_calls (approval_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_grant_idx ON routing.capability_calls (grant_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_job_idx ON routing.capability_calls (job_id);
CREATE INDEX IF NOT EXISTS routing_capability_calls_status_idx ON routing.capability_calls (status);
CREATE INDEX IF NOT EXISTS routing_capability_calls_created_at_desc_idx ON routing.capability_calls (created_at DESC);
CREATE INDEX IF NOT EXISTS routing_capability_calls_updated_at_desc_idx ON routing.capability_calls (updated_at DESC);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'events_events_route_id_fkey'
    ) THEN
        ALTER TABLE events.events
        ADD CONSTRAINT events_events_route_id_fkey
        FOREIGN KEY (route_id)
        REFERENCES routing.routes(route_id)
        ON DELETE SET NULL;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE IF EXISTS events.events DROP CONSTRAINT IF EXISTS events_events_route_id_fkey;

DROP TABLE IF EXISTS routing.capability_calls;
DROP TABLE IF EXISTS routing.routes;

DROP SCHEMA IF EXISTS routing;
