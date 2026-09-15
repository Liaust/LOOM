-- +goose Up
CREATE SCHEMA IF NOT EXISTS policy;
CREATE SCHEMA IF NOT EXISTS security;

CREATE TABLE IF NOT EXISTS policy.decisions (
    policy_decision_id text PRIMARY KEY CHECK (policy_decision_id LIKE 'policy_decision_%'),
    decision_key text UNIQUE NULL,
    actor_id text NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NULL REFERENCES nodes.nodes(node_id),
    target_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    resource_kind text NOT NULL,
    resource_id text NULL,
    operation text NOT NULL,
    capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    risk_level text NULL CHECK (
        risk_level IS NULL OR risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    execution_authorization_level integer NULL CHECK (
        execution_authorization_level IS NULL OR execution_authorization_level BETWEEN 1 AND 5
    ),
    actor_authorization_level integer NULL CHECK (
        actor_authorization_level IS NULL OR actor_authorization_level BETWEEN 1 AND 5
    ),
    decision text NOT NULL CHECK (
        decision IN (
            'allow',
            'deny',
            'approval_required'
        )
    ),
    reason_code text NOT NULL,
    safe_explanation text NOT NULL,
    approval_id text NULL,
    grant_id text NULL,
    context_hash text NOT NULL CHECK (context_hash ~ '^sha256:[0-9a-f]{64}$'),
    context_summary jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(context_summary) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS policy_decisions_created_at_desc_idx ON policy.decisions (created_at DESC);
CREATE INDEX IF NOT EXISTS policy_decisions_actor_idx ON policy.decisions (actor_id);
CREATE INDEX IF NOT EXISTS policy_decisions_origin_node_idx ON policy.decisions (origin_node_id);
CREATE INDEX IF NOT EXISTS policy_decisions_target_node_idx ON policy.decisions (target_node_id);
CREATE INDEX IF NOT EXISTS policy_decisions_operation_idx ON policy.decisions (operation);
CREATE INDEX IF NOT EXISTS policy_decisions_decision_idx ON policy.decisions (decision);
CREATE INDEX IF NOT EXISTS policy_decisions_capability_endpoint_idx ON policy.decisions (capability_endpoint_id);
CREATE INDEX IF NOT EXISTS policy_decisions_approval_idx ON policy.decisions (approval_id);
CREATE INDEX IF NOT EXISTS policy_decisions_grant_idx ON policy.decisions (grant_id);

CREATE TABLE IF NOT EXISTS policy.approvals (
    approval_id text PRIMARY KEY CHECK (approval_id LIKE 'approval_%'),
    approval_key text UNIQUE NOT NULL,
    requested_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    approving_actor_id text NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NULL REFERENCES nodes.nodes(node_id),
    target_node_id text NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    resource_kind text NOT NULL,
    resource_id text NULL,
    operation text NOT NULL,
    capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    risk_level text NOT NULL CHECK (
        risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    execution_authorization_level integer NOT NULL CHECK (execution_authorization_level BETWEEN 1 AND 5),
    request_reason text NOT NULL,
    action_summary text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'pending',
            'approved',
            'denied',
            'expired',
            'cancelled',
            'superseded'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    decided_at timestamptz NULL,
    decision_reason text NULL,
    resulting_grant_id text NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS policy_approvals_status_idx ON policy.approvals (status);
CREATE INDEX IF NOT EXISTS policy_approvals_requested_by_actor_idx ON policy.approvals (requested_by_actor_id);
CREATE INDEX IF NOT EXISTS policy_approvals_approving_actor_idx ON policy.approvals (approving_actor_id);
CREATE INDEX IF NOT EXISTS policy_approvals_created_at_desc_idx ON policy.approvals (created_at DESC);
CREATE INDEX IF NOT EXISTS policy_approvals_expires_at_idx ON policy.approvals (expires_at);
CREATE INDEX IF NOT EXISTS policy_approvals_capability_endpoint_idx ON policy.approvals (capability_endpoint_id);

CREATE TABLE IF NOT EXISTS policy.grants (
    grant_id text PRIMARY KEY CHECK (grant_id LIKE 'grant_%'),
    grant_key text UNIQUE NOT NULL,
    grant_type text NOT NULL CHECK (
        grant_type IN (
            'one_shot',
            'elevation',
            'job',
            'workflow',
            'project',
            'route',
            'transfer',
            'credential_use',
            'break_glass'
        )
    ),
    granted_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    granted_to_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    status text NOT NULL CHECK (
        status IN (
            'active',
            'consumed',
            'expired',
            'revoked',
            'suspended'
        )
    ),
    bypass_confirmation boolean NOT NULL DEFAULT false,
    max_risk_level text NULL CHECK (
        max_risk_level IS NULL OR max_risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    max_authorization_level integer NULL CHECK (
        max_authorization_level IS NULL OR max_authorization_level BETWEEN 1 AND 5
    ),
    scope_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(scope_constraints) = 'object'),
    node_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(node_constraints) = 'object'),
    capability_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(capability_constraints) = 'object'),
    credential_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(credential_constraints) = 'object'),
    object_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(object_constraints) = 'object'),
    egress_constraints jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(egress_constraints) = 'object'),
    max_uses integer NULL CHECK (max_uses IS NULL OR max_uses > 0),
    uses_count integer NOT NULL DEFAULT 0 CHECK (uses_count >= 0),
    audit_level text NOT NULL DEFAULT 'audit' CHECK (
        audit_level IN (
            'local_debug',
            'node_activity',
            'audit',
            'summary',
            'critical'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL,
    revoked_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    revoked_reason text NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS policy_grants_status_idx ON policy.grants (status);
CREATE INDEX IF NOT EXISTS policy_grants_granted_to_actor_idx ON policy.grants (granted_to_actor_id);
CREATE INDEX IF NOT EXISTS policy_grants_granted_by_actor_idx ON policy.grants (granted_by_actor_id);
CREATE INDEX IF NOT EXISTS policy_grants_approval_idx ON policy.grants (approval_id);
CREATE INDEX IF NOT EXISTS policy_grants_expires_at_idx ON policy.grants (expires_at);
CREATE INDEX IF NOT EXISTS policy_grants_created_at_desc_idx ON policy.grants (created_at DESC);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'policy_decisions_approval_id_fkey'
    ) THEN
        ALTER TABLE policy.decisions
        ADD CONSTRAINT policy_decisions_approval_id_fkey
        FOREIGN KEY (approval_id)
        REFERENCES policy.approvals(approval_id)
        ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'policy_decisions_grant_id_fkey'
    ) THEN
        ALTER TABLE policy.decisions
        ADD CONSTRAINT policy_decisions_grant_id_fkey
        FOREIGN KEY (grant_id)
        REFERENCES policy.grants(grant_id)
        ON DELETE SET NULL;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'policy_approvals_resulting_grant_id_fkey'
    ) THEN
        ALTER TABLE policy.approvals
        ADD CONSTRAINT policy_approvals_resulting_grant_id_fkey
        FOREIGN KEY (resulting_grant_id)
        REFERENCES policy.grants(grant_id)
        ON DELETE SET NULL;
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE IF EXISTS policy.decisions DROP CONSTRAINT IF EXISTS policy_decisions_approval_id_fkey;
ALTER TABLE IF EXISTS policy.decisions DROP CONSTRAINT IF EXISTS policy_decisions_grant_id_fkey;
ALTER TABLE IF EXISTS policy.approvals DROP CONSTRAINT IF EXISTS policy_approvals_resulting_grant_id_fkey;

DROP TABLE IF EXISTS policy.grants;
DROP TABLE IF EXISTS policy.approvals;
DROP TABLE IF EXISTS policy.decisions;

DROP SCHEMA IF EXISTS security;
DROP SCHEMA IF EXISTS policy;
