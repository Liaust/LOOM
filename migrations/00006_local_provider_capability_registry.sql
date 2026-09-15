-- +goose Up
CREATE SCHEMA IF NOT EXISTS capabilities;

CREATE TABLE IF NOT EXISTS capabilities.providers (
    provider_id text PRIMARY KEY CHECK (provider_id LIKE 'prov_%'),
    provider_key text NOT NULL CHECK (provider_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    compact_address text NOT NULL CHECK (compact_address ~ '^[a-z][a-z0-9_/-]{0,255}@[a-z][a-z0-9_-]{0,62}$'),
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    provider_type text NOT NULL CHECK (
        provider_type IN (
            'system',
            'object_store',
            'script_runner',
            'workflow_runner',
            'connector',
            'module',
            'hardware',
            'agent',
            'service'
        )
    ),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    version text NOT NULL DEFAULT '0.1.0',
    status text NOT NULL CHECK (
        status IN (
            'registered',
            'active',
            'disabled',
            'deprecated',
            'revoked'
        )
    ),
    runtime_profile_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(runtime_profile_json) = 'object'),
    documentation_refs_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(documentation_refs_json) = 'array'),
    package_ref text NULL,
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    last_advertised_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (node_id, provider_key)
);

CREATE UNIQUE INDEX IF NOT EXISTS capabilities_providers_active_address_idx
ON capabilities.providers (compact_address)
WHERE status IN ('registered', 'active', 'disabled');

CREATE INDEX IF NOT EXISTS capabilities_providers_node_idx ON capabilities.providers (node_id);
CREATE INDEX IF NOT EXISTS capabilities_providers_scope_idx ON capabilities.providers (scope_id);
CREATE INDEX IF NOT EXISTS capabilities_providers_type_idx ON capabilities.providers (provider_type);
CREATE INDEX IF NOT EXISTS capabilities_providers_status_idx ON capabilities.providers (status);

CREATE TABLE IF NOT EXISTS capabilities.provider_health (
    provider_id text PRIMARY KEY REFERENCES capabilities.providers(provider_id) ON DELETE CASCADE,
    health_status text NOT NULL CHECK (
        health_status IN (
            'unknown',
            'ok',
            'degraded',
            'unhealthy',
            'offline'
        )
    ),
    availability_status text NOT NULL CHECK (
        availability_status IN (
            'unknown',
            'available',
            'limited',
            'unavailable'
        )
    ),
    last_checked_at timestamptz NULL,
    last_ok_at timestamptz NULL,
    message text NOT NULL DEFAULT '',
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details_json) = 'object'),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS capabilities_provider_health_health_idx ON capabilities.provider_health (health_status);
CREATE INDEX IF NOT EXISTS capabilities_provider_health_availability_idx ON capabilities.provider_health (availability_status);

CREATE TABLE IF NOT EXISTS capabilities.capability_classes (
    capability_class_id text PRIMARY KEY CHECK (capability_class_id LIKE 'cls_%'),
    namespace text NOT NULL CHECK (namespace ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    name text NOT NULL CHECK (name ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    version text NOT NULL DEFAULT '0.1.0',
    display_name text NOT NULL,
    description text NOT NULL DEFAULT '',
    form text NOT NULL CHECK (
        form IN (
            'query',
            'command',
            'job',
            'session',
            'stream',
            'subscription',
            'lease'
        )
    ),
    input_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_schema_json) = 'object'),
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema_json) = 'object'),
    default_risk_level text NOT NULL CHECK (
        default_risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    default_policy_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(default_policy_requirements_json) = 'object'),
    status text NOT NULL CHECK (
        status IN (
            'active',
            'deprecated',
            'disabled',
            'revoked'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deprecated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (namespace, name, version)
);

CREATE INDEX IF NOT EXISTS capabilities_classes_namespace_idx ON capabilities.capability_classes (namespace);
CREATE INDEX IF NOT EXISTS capabilities_classes_name_idx ON capabilities.capability_classes (name);
CREATE INDEX IF NOT EXISTS capabilities_classes_status_idx ON capabilities.capability_classes (status);

CREATE TABLE IF NOT EXISTS capabilities.capability_endpoints (
    capability_endpoint_id text PRIMARY KEY CHECK (capability_endpoint_id LIKE 'endp_%'),
    provider_id text NOT NULL REFERENCES capabilities.providers(provider_id),
    capability_class_id text NOT NULL REFERENCES capabilities.capability_classes(capability_class_id),
    active_endpoint_version_id text NULL,
    endpoint_name text NOT NULL CHECK (endpoint_name ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    compact_address text NOT NULL CHECK (compact_address ~ '^[a-z][a-z0-9_/-]{0,255}@[a-z][a-z0-9_-]{0,62}\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    form text NOT NULL CHECK (
        form IN (
            'query',
            'command',
            'job',
            'session',
            'stream',
            'subscription',
            'lease'
        )
    ),
    input_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_schema_json) = 'object'),
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema_json) = 'object'),
    risk_level text NOT NULL CHECK (
        risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    execution_authorization_level integer NOT NULL CHECK (execution_authorization_level BETWEEN 1 AND 5),
    side_effects_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(side_effects_json) = 'object'),
    policy_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy_requirements_json) = 'object'),
    credential_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(credential_requirements_json) = 'object'),
    approval_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(approval_requirements_json) = 'object'),
    job_behavior_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(job_behavior_json) = 'object'),
    session_behavior_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(session_behavior_json) = 'object'),
    stream_behavior_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(stream_behavior_json) = 'object'),
    lease_behavior_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(lease_behavior_json) = 'object'),
    status text NOT NULL CHECK (
        status IN (
            'registered',
            'active',
            'disabled',
            'deprecated',
            'revoked'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deprecated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (provider_id, endpoint_name)
);

CREATE UNIQUE INDEX IF NOT EXISTS capabilities_endpoints_active_address_idx
ON capabilities.capability_endpoints (compact_address)
WHERE status IN ('registered', 'active', 'disabled');

CREATE INDEX IF NOT EXISTS capabilities_endpoints_provider_idx ON capabilities.capability_endpoints (provider_id);
CREATE INDEX IF NOT EXISTS capabilities_endpoints_class_idx ON capabilities.capability_endpoints (capability_class_id);
CREATE INDEX IF NOT EXISTS capabilities_endpoints_status_idx ON capabilities.capability_endpoints (status);
CREATE INDEX IF NOT EXISTS capabilities_endpoints_risk_idx ON capabilities.capability_endpoints (risk_level);
CREATE INDEX IF NOT EXISTS capabilities_endpoints_auth_level_idx ON capabilities.capability_endpoints (execution_authorization_level);

CREATE TABLE IF NOT EXISTS capabilities.endpoint_versions (
    capability_endpoint_version_id text PRIMARY KEY CHECK (capability_endpoint_version_id LIKE 'endpv_%'),
    capability_endpoint_id text NOT NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id) ON DELETE CASCADE,
    version_label text NOT NULL,
    implementation_hash text NULL CHECK (implementation_hash IS NULL OR implementation_hash ~ '^sha256:[0-9a-f]{64}$'),
    manifest_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(manifest_json) = 'object'),
    input_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_schema_json) = 'object'),
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema_json) = 'object'),
    risk_level text NOT NULL CHECK (
        risk_level IN (
            'low',
            'medium',
            'high',
            'critical'
        )
    ),
    execution_authorization_level integer NOT NULL CHECK (execution_authorization_level BETWEEN 1 AND 5),
    policy_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy_requirements_json) = 'object'),
    credential_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(credential_requirements_json) = 'object'),
    approval_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(approval_requirements_json) = 'object'),
    status text NOT NULL CHECK (
        status IN (
            'pending_review',
            'active',
            'disabled',
            'deprecated',
            'revoked',
            'superseded'
        )
    ),
    approved_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    approved_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    deprecated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (capability_endpoint_id, version_label)
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'capabilities_endpoints_active_endpoint_version_id_fkey'
    ) THEN
        ALTER TABLE capabilities.capability_endpoints
        ADD CONSTRAINT capabilities_endpoints_active_endpoint_version_id_fkey
        FOREIGN KEY (active_endpoint_version_id)
        REFERENCES capabilities.endpoint_versions(capability_endpoint_version_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS capabilities_endpoint_versions_endpoint_idx ON capabilities.endpoint_versions (capability_endpoint_id);
CREATE INDEX IF NOT EXISTS capabilities_endpoint_versions_status_idx ON capabilities.endpoint_versions (status);
CREATE INDEX IF NOT EXISTS capabilities_endpoint_versions_hash_idx ON capabilities.endpoint_versions (implementation_hash);

CREATE TABLE IF NOT EXISTS capabilities.usage_documents (
    capability_usage_document_id text PRIMARY KEY CHECK (capability_usage_document_id LIKE 'udoc_%'),
    target_kind text NOT NULL CHECK (
        target_kind IN (
            'capability_class',
            'capability_endpoint',
            'provider',
            'capability_pack',
            'provider_pack',
            'script',
            'workflow',
            'module_capability',
            'connector_capability'
        )
    ),
    target_id text NOT NULL,
    target_address text NULL,
    title text NOT NULL,
    version_label text NOT NULL DEFAULT '0.1.0',
    body_format text NOT NULL CHECK (body_format IN ('markdown')),
    body text NOT NULL,
    section_map_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(section_map_json) = 'object'),
    visibility_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(visibility_policy_json) = 'object'),
    review_status text NOT NULL CHECK (
        review_status IN (
            'draft',
            'pending_review',
            'approved',
            'stale',
            'rejected',
            'deprecated'
        )
    ),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    source_kind text NOT NULL DEFAULT 'manual' CHECK (
        source_kind IN (
            'bootstrap',
            'manifest',
            'manual',
            'generated'
        )
    ),
    source_ref text NULL,
    created_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    approved_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    approved_at timestamptz NULL,
    indexed_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deprecated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (target_kind, target_id, version_label, content_hash)
);

CREATE INDEX IF NOT EXISTS capabilities_usage_documents_target_idx ON capabilities.usage_documents (target_kind, target_id);
CREATE INDEX IF NOT EXISTS capabilities_usage_documents_review_idx ON capabilities.usage_documents (review_status);
CREATE INDEX IF NOT EXISTS capabilities_usage_documents_hash_idx ON capabilities.usage_documents (content_hash);
CREATE INDEX IF NOT EXISTS capabilities_usage_documents_text_idx ON capabilities.usage_documents
USING gin (to_tsvector('simple', title || ' ' || body));

-- +goose Down
DROP SCHEMA IF EXISTS capabilities CASCADE;
