-- +goose Up
ALTER TABLE automation.automations
    ADD COLUMN IF NOT EXISTS integration_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(integration_profile_json) = 'object'),
    ADD COLUMN IF NOT EXISTS mapping_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(mapping_profile_json) = 'object'),
    ADD COLUMN IF NOT EXISTS storage_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(storage_profile_json) = 'object');

ALTER TABLE automation.automations
    DROP CONSTRAINT IF EXISTS automations_source_kind_check;

ALTER TABLE automation.automations
    ADD CONSTRAINT automations_source_kind_check CHECK (source_kind IN ('schedule', 'direct_event'));

ALTER TABLE automation.invocations
    DROP CONSTRAINT IF EXISTS invocations_source_kind_check;

ALTER TABLE automation.invocations
    ADD CONSTRAINT invocations_source_kind_check CHECK (source_kind IN ('schedule', 'direct_event'));

CREATE TABLE IF NOT EXISTS automation.integrations (
    integration_id text PRIMARY KEY CHECK (integration_id LIKE 'integration_%'),
    integration_key text UNIQUE NOT NULL CHECK (integration_key ~ '^[a-z][a-z0-9_-]{0,80}$'),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'revoked')),
    actor_id text NOT NULL UNIQUE REFERENCES identity.actors(actor_id),
    main_auth_level smallint NULL CHECK (main_auth_level BETWEEN 1 AND 5),
    allowed_scopes_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_scopes_json) = 'array'),
    allowed_projects_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_projects_json) = 'array'),
    allowed_endpoint_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_endpoint_refs_json) = 'array'),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz NULL,
    revoked_reason text NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_integrations_status_idx ON automation.integrations (status);
CREATE INDEX IF NOT EXISTS automation_integrations_actor_idx ON automation.integrations (actor_id);
CREATE INDEX IF NOT EXISTS automation_integrations_created_at_idx ON automation.integrations (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.integration_auth_profiles (
    auth_profile_id text PRIMARY KEY CHECK (auth_profile_id LIKE 'integration_auth_profile_%'),
    integration_id text NOT NULL REFERENCES automation.integrations(integration_id),
    display_name text NOT NULL,
    auth_kind text NOT NULL CHECK (auth_kind IN ('bearer_header', 'query_token', 'private_network')),
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'revoked')),
    token_hash text NULL CHECK (token_hash IS NULL OR token_hash ~ '^sha256:[0-9a-f]{64}$'),
    token_last_four text NOT NULL DEFAULT '',
    allowed_endpoint_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(allowed_endpoint_refs_json) = 'array'),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz NULL,
    revoked_reason text NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_integration_auth_profiles_integration_idx ON automation.integration_auth_profiles (integration_id, status);
CREATE INDEX IF NOT EXISTS automation_integration_auth_profiles_created_at_idx ON automation.integration_auth_profiles (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.direct_event_endpoints (
    endpoint_id text PRIMARY KEY CHECK (endpoint_id LIKE 'direct_event_endpoint_%'),
    endpoint_slug text UNIQUE NOT NULL CHECK (endpoint_slug ~ '^[a-z][a-z0-9_-]{0,80}$'),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('active', 'paused', 'disabled')),
    integration_id text NOT NULL REFERENCES automation.integrations(integration_id),
    automation_id text NOT NULL UNIQUE REFERENCES automation.automations(automation_id),
    event_type text NOT NULL,
    endpoint_path text NOT NULL UNIQUE,
    response_mode text NOT NULL CHECK (response_mode IN ('accepted', 'sync_wait')),
    mapping_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(mapping_profile_json) = 'object'),
    auth_profile_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(auth_profile_refs_json) = 'array'),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_direct_event_endpoints_status_idx ON automation.direct_event_endpoints (status);
CREATE INDEX IF NOT EXISTS automation_direct_event_endpoints_integration_idx ON automation.direct_event_endpoints (integration_id, status);
CREATE INDEX IF NOT EXISTS automation_direct_event_endpoints_automation_idx ON automation.direct_event_endpoints (automation_id);
CREATE INDEX IF NOT EXISTS automation_direct_event_endpoints_created_at_idx ON automation.direct_event_endpoints (created_at DESC);

CREATE TABLE IF NOT EXISTS automation.direct_events (
    direct_event_id text PRIMARY KEY CHECK (direct_event_id LIKE 'direct_event_%'),
    endpoint_id text NOT NULL REFERENCES automation.direct_event_endpoints(endpoint_id),
    integration_id text NOT NULL REFERENCES automation.integrations(integration_id),
    automation_id text NOT NULL REFERENCES automation.automations(automation_id),
    status text NOT NULL CHECK (
        status IN (
            'received',
            'authenticated',
            'rejected',
            'duplicate',
            'mapped',
            'mapping_failed',
            'invocation_created',
            'completed',
            'failed',
            'timed_out'
        )
    ),
    external_event_id text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    request_headers_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(request_headers_json) = 'object'),
    query_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(query_json) = 'object'),
    raw_body_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(raw_body_json) = 'object'),
    mapped_input_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(mapped_input_json) = 'object'),
    response_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(response_json) = 'object'),
    invocation_id text NULL,
    failure_code text NULL,
    failure_message text NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata_json) = 'object')
);

CREATE INDEX IF NOT EXISTS automation_direct_events_endpoint_received_idx ON automation.direct_events (endpoint_id, received_at DESC);
CREATE INDEX IF NOT EXISTS automation_direct_events_status_idx ON automation.direct_events (status, received_at DESC);
CREATE INDEX IF NOT EXISTS automation_direct_events_integration_idx ON automation.direct_events (integration_id, received_at DESC);
CREATE INDEX IF NOT EXISTS automation_direct_events_invocation_idx ON automation.direct_events (invocation_id);
CREATE UNIQUE INDEX IF NOT EXISTS automation_direct_events_endpoint_idempotency_idx
    ON automation.direct_events (endpoint_id, idempotency_key)
    WHERE idempotency_key <> '';

-- +goose Down
DROP TABLE IF EXISTS automation.direct_events;
DROP TABLE IF EXISTS automation.direct_event_endpoints;
DROP TABLE IF EXISTS automation.integration_auth_profiles;
DROP TABLE IF EXISTS automation.integrations;

DELETE FROM automation.invocations WHERE source_kind = 'direct_event';
DELETE FROM automation.automations WHERE source_kind = 'direct_event';

ALTER TABLE automation.invocations
    DROP CONSTRAINT IF EXISTS invocations_source_kind_check;

ALTER TABLE automation.invocations
    ADD CONSTRAINT invocations_source_kind_check CHECK (source_kind IN ('schedule'));

ALTER TABLE automation.automations
    DROP CONSTRAINT IF EXISTS automations_source_kind_check;

ALTER TABLE automation.automations
    ADD CONSTRAINT automations_source_kind_check CHECK (source_kind IN ('schedule'));

ALTER TABLE automation.automations
    DROP COLUMN IF EXISTS storage_profile_json,
    DROP COLUMN IF EXISTS mapping_profile_json,
    DROP COLUMN IF EXISTS integration_profile_json;
