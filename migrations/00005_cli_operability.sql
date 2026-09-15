-- +goose Up
CREATE SCHEMA IF NOT EXISTS interface;
CREATE SCHEMA IF NOT EXISTS admin;

CREATE TABLE IF NOT EXISTS interface.idempotency_keys (
    idempotency_id text PRIMARY KEY CHECK (idempotency_id LIKE 'idempotency_%'),
    scope_kind text NOT NULL DEFAULT 'actor_node',
    scope_ref text NOT NULL DEFAULT '',
    actor_id text NULL REFERENCES identity.actors(actor_id),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    key text NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    operation text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'in_progress',
            'completed',
            'failed'
        )
    ),
    result_kind text NULL,
    result_ref text NULL,
    response_snapshot jsonb NULL,
    error_code text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (scope_kind, scope_ref, key)
);

CREATE INDEX IF NOT EXISTS interface_idempotency_keys_actor_node_idx ON interface.idempotency_keys (actor_id, node_id);
CREATE INDEX IF NOT EXISTS interface_idempotency_keys_status_idx ON interface.idempotency_keys (status);
CREATE INDEX IF NOT EXISTS interface_idempotency_keys_expires_at_idx ON interface.idempotency_keys (expires_at);
CREATE INDEX IF NOT EXISTS interface_idempotency_keys_operation_idx ON interface.idempotency_keys (operation);

CREATE TABLE IF NOT EXISTS admin.operations (
    admin_operation_id text PRIMARY KEY CHECK (admin_operation_id LIKE 'admin_operation_%'),
    operation_type text NOT NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    target_kind text NOT NULL,
    target_ref text NOT NULL DEFAULT '',
    authorization_level integer NOT NULL CHECK (authorization_level BETWEEN 1 AND 5),
    policy_decision_id text NULL,
    approval_request_id text NULL,
    status text NOT NULL CHECK (
        status IN (
            'requested',
            'approved',
            'rejected',
            'running',
            'completed',
            'failed',
            'cancelled'
        )
    ),
    dry_run boolean NOT NULL DEFAULT false,
    requested_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    error_code text NULL,
    error_message text NULL,
    correlation_id text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS admin_operations_actor_idx ON admin.operations (actor_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS admin_operations_node_idx ON admin.operations (node_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS admin_operations_status_idx ON admin.operations (status);
CREATE INDEX IF NOT EXISTS admin_operations_operation_type_idx ON admin.operations (operation_type);
CREATE INDEX IF NOT EXISTS admin_operations_correlation_idx ON admin.operations (correlation_id);

-- +goose Down
DROP SCHEMA IF EXISTS admin CASCADE;
DROP SCHEMA IF EXISTS interface CASCADE;
