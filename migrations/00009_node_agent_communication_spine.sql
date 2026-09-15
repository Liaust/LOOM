-- +goose Up
CREATE SCHEMA IF NOT EXISTS communication;

ALTER TABLE nodes.nodes
    ADD COLUMN IF NOT EXISTS presence_state text NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS last_heartbeat_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS last_seen_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS runtime_version text NULL,
    ADD COLUMN IF NOT EXISTS enrollment_status text NOT NULL DEFAULT 'approved',
    ADD COLUMN IF NOT EXISTS credential_status text NOT NULL DEFAULT 'none';

ALTER TABLE nodes.nodes DROP CONSTRAINT IF EXISTS nodes_nodes_presence_state_check;
ALTER TABLE nodes.nodes ADD CONSTRAINT nodes_nodes_presence_state_check CHECK (
    presence_state IN (
        'unknown',
        'online',
        'recently_seen',
        'offline',
        'degraded',
        'quarantined',
        'revoked'
    )
);

ALTER TABLE nodes.nodes DROP CONSTRAINT IF EXISTS nodes_nodes_enrollment_status_check;
ALTER TABLE nodes.nodes ADD CONSTRAINT nodes_nodes_enrollment_status_check CHECK (
    enrollment_status IN (
        'none',
        'pending',
        'approved',
        'denied',
        'revoked'
    )
);

ALTER TABLE nodes.nodes DROP CONSTRAINT IF EXISTS nodes_nodes_credential_status_check;
ALTER TABLE nodes.nodes ADD CONSTRAINT nodes_nodes_credential_status_check CHECK (
    credential_status IN (
        'none',
        'bootstrap_token',
        'active',
        'revoked',
        'expired'
    )
);

CREATE INDEX IF NOT EXISTS nodes_nodes_presence_state_idx ON nodes.nodes (presence_state);
CREATE INDEX IF NOT EXISTS nodes_nodes_last_heartbeat_at_desc_idx ON nodes.nodes (last_heartbeat_at DESC);
CREATE INDEX IF NOT EXISTS nodes_nodes_enrollment_status_idx ON nodes.nodes (enrollment_status);
CREATE INDEX IF NOT EXISTS nodes_nodes_credential_status_idx ON nodes.nodes (credential_status);

CREATE TABLE IF NOT EXISTS nodes.authority_profiles (
    authority_profile_id text PRIMARY KEY,
    profile_key text UNIQUE NOT NULL,
    display_name text NOT NULL,
    node_kind text NOT NULL,
    can_query_main boolean NOT NULL DEFAULT false,
    can_publish_events boolean NOT NULL DEFAULT false,
    can_expose_providers boolean NOT NULL DEFAULT false,
    can_receive_routes boolean NOT NULL DEFAULT false,
    can_request_main_routed_capabilities boolean NOT NULL DEFAULT false,
    can_sync_object_metadata boolean NOT NULL DEFAULT false,
    can_hold_local_credentials boolean NOT NULL DEFAULT false,
    can_act_offline boolean NOT NULL DEFAULT false,
    risk_limits_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(risk_limits_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nodes.runtime_profiles (
    runtime_profile_id text PRIMARY KEY,
    profile_key text UNIQUE NOT NULL,
    display_name text NOT NULL,
    node_kind text NOT NULL,
    local_database text NOT NULL DEFAULT 'false',
    local_event_log text NOT NULL DEFAULT 'false',
    local_provider_runtime text NOT NULL DEFAULT 'false',
    local_job_runner text NOT NULL DEFAULT 'false',
    script_runtime text NOT NULL DEFAULT 'false',
    workflow_runtime text NOT NULL DEFAULT 'false',
    skill_package_cache text NOT NULL DEFAULT 'false',
    local_policy_cache text NOT NULL DEFAULT 'false',
    sync_agent text NOT NULL DEFAULT 'false',
    inbox_outbox text NOT NULL DEFAULT 'false',
    offline_mode text NOT NULL DEFAULT 'false',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS nodes.node_profile_assignments (
    node_id text PRIMARY KEY REFERENCES nodes.nodes(node_id) ON DELETE CASCADE,
    authority_profile_id text NULL REFERENCES nodes.authority_profiles(authority_profile_id),
    runtime_profile_id text NULL REFERENCES nodes.runtime_profiles(runtime_profile_id),
    assigned_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    assigned_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE IF NOT EXISTS nodes.heartbeats (
    node_heartbeat_id text PRIMARY KEY CHECK (node_heartbeat_id LIKE 'node_heartbeat_%'),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    runtime_version text NULL,
    reported_status text NOT NULL CHECK (
        reported_status IN (
            'ok',
            'degraded',
            'error',
            'offline'
        )
    ),
    presence_state text NOT NULL CHECK (
        presence_state IN (
            'unknown',
            'online',
            'recently_seen',
            'offline',
            'degraded',
            'quarantined',
            'revoked'
        )
    ),
    inbox_backlog integer NOT NULL DEFAULT 0 CHECK (inbox_backlog >= 0),
    outbox_backlog integer NOT NULL DEFAULT 0 CHECK (outbox_backlog >= 0),
    storage_status_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(storage_status_json) = 'object'),
    error_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_summary_json) = 'object'),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    reported_at timestamptz NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS nodes_heartbeats_node_received_idx ON nodes.heartbeats (node_id, received_at DESC);
CREATE INDEX IF NOT EXISTS nodes_heartbeats_presence_idx ON nodes.heartbeats (presence_state);

CREATE TABLE IF NOT EXISTS nodes.node_status_history (
    node_status_history_id text PRIMARY KEY,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    previous_presence_state text NULL,
    next_presence_state text NOT NULL,
    reason_code text NOT NULL,
    heartbeat_id text NULL REFERENCES nodes.heartbeats(node_heartbeat_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS nodes_status_history_node_created_idx ON nodes.node_status_history (node_id, created_at DESC);

CREATE TABLE IF NOT EXISTS security.node_enrollment_tokens (
    node_enrollment_token_id text PRIMARY KEY CHECK (node_enrollment_token_id LIKE 'node_enrollment_token_%'),
    token_hash text UNIQUE NOT NULL,
    token_hint text NOT NULL,
    requested_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    status text NOT NULL CHECK (
        status IN (
            'active',
            'used',
            'expired',
            'revoked'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS security_node_enrollment_tokens_status_idx ON security.node_enrollment_tokens (status);
CREATE INDEX IF NOT EXISTS security_node_enrollment_tokens_expires_at_idx ON security.node_enrollment_tokens (expires_at);

CREATE TABLE IF NOT EXISTS security.node_enrollment_requests (
    node_enrollment_request_id text PRIMARY KEY CHECK (node_enrollment_request_id LIKE 'node_enrollment_request_%'),
    node_enrollment_token_id text NULL REFERENCES security.node_enrollment_tokens(node_enrollment_token_id),
    requested_node_key text NOT NULL,
    requested_display_name text NOT NULL,
    requested_node_kind text NOT NULL,
    requested_node_role text NOT NULL,
    requested_runtime_class text NOT NULL,
    requested_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(requested_profile_json) = 'object'),
    requested_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    approved_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    status text NOT NULL CHECK (
        status IN (
            'pending',
            'approved',
            'denied',
            'expired',
            'cancelled'
        )
    ),
    activated_node_id text NULL REFERENCES nodes.nodes(node_id),
    node_credential_id text NULL,
    denial_reason text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    approved_at timestamptz NULL,
    denied_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS security_node_enrollment_requests_status_idx ON security.node_enrollment_requests (status);
CREATE INDEX IF NOT EXISTS security_node_enrollment_requests_node_key_idx ON security.node_enrollment_requests (requested_node_key);
CREATE INDEX IF NOT EXISTS security_node_enrollment_requests_activated_node_idx ON security.node_enrollment_requests (activated_node_id);

CREATE TABLE IF NOT EXISTS security.node_auth_credentials (
    node_credential_id text PRIMARY KEY CHECK (node_credential_id LIKE 'node_credential_%'),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    credential_kind text NOT NULL CHECK (
        credential_kind IN (
            'node_token_bootstrap',
            'node_keypair',
            'node_certificate',
            'mtls_certificate'
        )
    ),
    credential_hash text UNIQUE NOT NULL,
    credential_hint text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'active',
            'revoked',
            'expired',
            'rotated'
        )
    ),
    issued_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NULL,
    last_used_at timestamptz NULL,
    revoked_at timestamptz NULL,
    revoked_reason text NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS security_node_auth_credentials_node_idx ON security.node_auth_credentials (node_id);
CREATE INDEX IF NOT EXISTS security_node_auth_credentials_status_idx ON security.node_auth_credentials (status);
CREATE INDEX IF NOT EXISTS security_node_auth_credentials_expires_at_idx ON security.node_auth_credentials (expires_at);

CREATE TABLE IF NOT EXISTS communication.messages (
    communication_message_id text PRIMARY KEY CHECK (communication_message_id LIKE 'communication_message_%'),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    direction text NOT NULL CHECK (
        direction IN (
            'main_to_node',
            'node_to_main'
        )
    ),
    kind text NOT NULL,
    status text NOT NULL CHECK (
        status IN (
            'pending',
            'available',
            'claimed',
            'delivered',
            'acked',
            'failed_retryable',
            'failed_permanent',
            'dead_letter',
            'cancelled',
            'expired'
        )
    ),
    idempotency_key text NULL,
    correlation_id text NULL,
    route_id text NULL REFERENCES routing.routes(route_id),
    capability_call_id text NULL REFERENCES routing.capability_calls(capability_call_id),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    payload_hash text NULL CHECK (payload_hash IS NULL OR payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    available_at timestamptz NOT NULL DEFAULT now(),
    claimed_at timestamptz NULL,
    delivered_at timestamptz NULL,
    acked_at timestamptz NULL,
    expires_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS communication_messages_node_direction_idem_idx
    ON communication.messages (node_id, direction, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS communication_messages_node_status_idx ON communication.messages (node_id, status, available_at);
CREATE INDEX IF NOT EXISTS communication_messages_kind_idx ON communication.messages (kind);
CREATE INDEX IF NOT EXISTS communication_messages_correlation_idx ON communication.messages (correlation_id);
CREATE INDEX IF NOT EXISTS communication_messages_route_idx ON communication.messages (route_id);
CREATE INDEX IF NOT EXISTS communication_messages_capability_call_idx ON communication.messages (capability_call_id);
CREATE INDEX IF NOT EXISTS communication_messages_created_at_desc_idx ON communication.messages (created_at DESC);

CREATE TABLE IF NOT EXISTS communication.message_acks (
    communication_ack_id text PRIMARY KEY CHECK (communication_ack_id LIKE 'communication_ack_%'),
    communication_message_id text NOT NULL REFERENCES communication.messages(communication_message_id),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    ack_status text NOT NULL CHECK (
        ack_status IN (
            'accepted',
            'completed',
            'failed_retryable',
            'failed_permanent',
            'rejected'
        )
    ),
    idempotency_key text NULL,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    processed_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE UNIQUE INDEX IF NOT EXISTS communication_message_acks_message_idx ON communication.message_acks (communication_message_id);
CREATE UNIQUE INDEX IF NOT EXISTS communication_message_acks_idem_idx
    ON communication.message_acks (node_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS communication_message_acks_node_idx ON communication.message_acks (node_id, created_at DESC);

INSERT INTO nodes.authority_profiles (
    authority_profile_id, profile_key, display_name, node_kind,
    can_query_main, can_publish_events, can_expose_providers, can_receive_routes,
    can_request_main_routed_capabilities, can_sync_object_metadata,
    can_hold_local_credentials, can_act_offline, risk_limits_json, metadata
)
VALUES
    ('node_authority_profile_main_default', 'main_node_default', 'Main Node Default', 'main',
     true, true, true, true, true, true, true, true, '{"max_risk":"critical"}'::jsonb, '{"seed":"slice_10"}'::jsonb),
    ('node_authority_profile_owned_workspace_default', 'owned_workspace_default', 'Owned Workspace Default', 'workspace',
     true, true, false, false, true, true, true, true, '{"max_risk":"high"}'::jsonb, '{"seed":"slice_10"}'::jsonb),
    ('node_authority_profile_guest_restricted_default', 'guest_restricted_default', 'Guest Restricted Default', 'guest',
     true, false, false, false, false, false, false, false, '{"max_risk":"low"}'::jsonb, '{"seed":"slice_10"}'::jsonb)
ON CONFLICT (profile_key) DO NOTHING;

INSERT INTO nodes.runtime_profiles (
    runtime_profile_id, profile_key, display_name, node_kind,
    local_database, local_event_log, local_provider_runtime, local_job_runner,
    script_runtime, workflow_runtime, skill_package_cache, local_policy_cache,
    sync_agent, inbox_outbox, offline_mode, metadata
)
VALUES
    ('node_runtime_profile_main_default', 'main_node_default', 'Main Node Default', 'main',
     'true', 'true', 'true', 'true', 'true', 'limited', 'true', 'true', 'true', 'true', 'true', '{"seed":"slice_10"}'::jsonb),
    ('node_runtime_profile_owned_workspace_default', 'owned_workspace_default', 'Owned Workspace Default', 'workspace',
     'optional', 'true', 'true', 'optional', 'optional', 'optional', 'true', 'true', 'true', 'true', 'true', '{"seed":"slice_10"}'::jsonb),
    ('node_runtime_profile_guest_restricted_default', 'guest_restricted_default', 'Guest Restricted Default', 'guest',
     'false', 'minimal', 'false', 'false', 'false', 'false', 'false', 'false', 'minimal', 'minimal', 'false', '{"seed":"slice_10"}'::jsonb)
ON CONFLICT (profile_key) DO NOTHING;

INSERT INTO nodes.node_profile_assignments (
    node_id, authority_profile_id, runtime_profile_id, metadata
)
SELECT n.node_id,
       'node_authority_profile_main_default',
       'node_runtime_profile_main_default',
       '{"seed":"slice_10"}'::jsonb
FROM nodes.nodes n
WHERE n.node_key = 'main'
ON CONFLICT (node_id) DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS communication.message_acks;
DROP TABLE IF EXISTS communication.messages;
DROP SCHEMA IF EXISTS communication;

DROP TABLE IF EXISTS security.node_auth_credentials;
DROP TABLE IF EXISTS security.node_enrollment_requests;
DROP TABLE IF EXISTS security.node_enrollment_tokens;

DROP TABLE IF EXISTS nodes.node_status_history;
DROP TABLE IF EXISTS nodes.heartbeats;
DROP TABLE IF EXISTS nodes.node_profile_assignments;
DROP TABLE IF EXISTS nodes.runtime_profiles;
DROP TABLE IF EXISTS nodes.authority_profiles;

DROP INDEX IF EXISTS nodes_nodes_credential_status_idx;
DROP INDEX IF EXISTS nodes_nodes_enrollment_status_idx;
DROP INDEX IF EXISTS nodes_nodes_last_heartbeat_at_desc_idx;
DROP INDEX IF EXISTS nodes_nodes_presence_state_idx;

ALTER TABLE nodes.nodes
    DROP COLUMN IF EXISTS credential_status,
    DROP COLUMN IF EXISTS enrollment_status,
    DROP COLUMN IF EXISTS runtime_version,
    DROP COLUMN IF EXISTS last_seen_at,
    DROP COLUMN IF EXISTS last_heartbeat_at,
    DROP COLUMN IF EXISTS presence_state;
