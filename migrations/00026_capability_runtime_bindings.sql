-- +goose Up
CREATE TABLE IF NOT EXISTS capabilities.endpoint_runtime_bindings (
    runtime_binding_id text PRIMARY KEY CHECK (runtime_binding_id LIKE 'runtime_binding_%'),
    capability_endpoint_version_id text NOT NULL REFERENCES capabilities.endpoint_versions(capability_endpoint_version_id) ON DELETE CASCADE,
    runtime_kind text NOT NULL CHECK (
        runtime_kind IN (
            'script',
            'command',
            'http',
            'node_agent',
            'native',
            'module',
            'workflow',
            'external_process'
        )
    ),
    runtime_config_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(runtime_config_json) = 'object'),
    input_mapping_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_mapping_json) = 'object'),
    output_mapping_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_mapping_json) = 'object'),
    status text NOT NULL CHECK (
        status IN (
            'registered',
            'active',
            'disabled',
            'deprecated',
            'revoked'
        )
    ),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    approved_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    approved_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    disabled_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (capability_endpoint_version_id)
);

CREATE INDEX IF NOT EXISTS capabilities_endpoint_runtime_bindings_version_idx
ON capabilities.endpoint_runtime_bindings (capability_endpoint_version_id);

CREATE INDEX IF NOT EXISTS capabilities_endpoint_runtime_bindings_kind_status_idx
ON capabilities.endpoint_runtime_bindings (runtime_kind, status);

CREATE INDEX IF NOT EXISTS capabilities_endpoint_runtime_bindings_created_at_desc_idx
ON capabilities.endpoint_runtime_bindings (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS capabilities.endpoint_runtime_bindings;
