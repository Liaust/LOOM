-- +goose Up

CREATE TABLE IF NOT EXISTS modules.installations (
    module_installation_id text PRIMARY KEY CHECK (module_installation_id LIKE 'module_installation_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id),
    target_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    install_scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    installed_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    status text NOT NULL CHECK (
        status IN ('installing', 'installed', 'enabled', 'disabled', 'failed', 'removed')
    ),
    namespace_root text NOT NULL CHECK (namespace_root ~ '^loom\.[a-z0-9-]+(\.[a-z0-9-]+)*$'),
    filesystem_path text NOT NULL DEFAULT '',
    object_store_prefix text NOT NULL DEFAULT '',
    database_name text NOT NULL DEFAULT '',
    compatibility_report jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(compatibility_report) = 'object'),
    failure_reason text NOT NULL DEFAULT '',
    installed_at timestamptz NULL,
    enabled_at timestamptz NULL,
    disabled_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, target_node_id, install_scope_id)
);

CREATE INDEX IF NOT EXISTS modules_installations_version_idx ON modules.installations (module_version_id);
CREATE INDEX IF NOT EXISTS modules_installations_node_idx ON modules.installations (target_node_id);
CREATE INDEX IF NOT EXISTS modules_installations_scope_idx ON modules.installations (install_scope_id);
CREATE INDEX IF NOT EXISTS modules_installations_status_idx ON modules.installations (status);

CREATE TABLE IF NOT EXISTS modules.namespaces (
    module_namespace_id text PRIMARY KEY CHECK (module_namespace_id LIKE 'module_namespace_%'),
    module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id) ON DELETE CASCADE,
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id),
    namespace_kind text NOT NULL CHECK (
        namespace_kind IN (
            'module',
            'provider',
            'capability',
            'event_prefix',
            'object_type',
            'filesystem',
            'object_store',
            'database'
        )
    ),
    namespace_key text NOT NULL CHECK (length(namespace_key) > 0),
    namespace_value text NOT NULL CHECK (length(namespace_value) > 0),
    status text NOT NULL CHECK (status IN ('reserved', 'active', 'disabled', 'released_later', 'failed')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (namespace_kind, namespace_value)
);

CREATE INDEX IF NOT EXISTS modules_namespaces_installation_idx ON modules.namespaces (module_installation_id);
CREATE INDEX IF NOT EXISTS modules_namespaces_kind_status_idx ON modules.namespaces (namespace_kind, status);

CREATE TABLE IF NOT EXISTS modules.installation_providers (
    module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id) ON DELETE CASCADE,
    module_declaration_id text NOT NULL REFERENCES modules.provider_declarations(module_declaration_id),
    provider_id text NOT NULL REFERENCES capabilities.providers(provider_id),
    exposure_status text NOT NULL CHECK (
        exposure_status IN ('declared', 'installed_disabled', 'exposed', 'disabled', 'revoked')
    ),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (module_installation_id, module_declaration_id),
    UNIQUE (provider_id)
);

CREATE INDEX IF NOT EXISTS modules_installation_providers_provider_idx ON modules.installation_providers (provider_id);
CREATE INDEX IF NOT EXISTS modules_installation_providers_status_idx ON modules.installation_providers (exposure_status);

CREATE TABLE IF NOT EXISTS modules.installation_capabilities (
    module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id) ON DELETE CASCADE,
    module_declaration_id text NOT NULL REFERENCES modules.capability_declarations(module_declaration_id),
    capability_class_id text NOT NULL REFERENCES capabilities.capability_classes(capability_class_id),
    capability_endpoint_id text NOT NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    capability_endpoint_version_id text NOT NULL REFERENCES capabilities.endpoint_versions(capability_endpoint_version_id),
    exposure_status text NOT NULL CHECK (
        exposure_status IN ('declared', 'installed_disabled', 'exposed', 'disabled', 'revoked')
    ),
    usage_document_ids jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(usage_document_ids) = 'array'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (module_installation_id, module_declaration_id),
    UNIQUE (capability_endpoint_id)
);

CREATE INDEX IF NOT EXISTS modules_installation_capabilities_endpoint_idx ON modules.installation_capabilities (capability_endpoint_id);
CREATE INDEX IF NOT EXISTS modules_installation_capabilities_version_idx ON modules.installation_capabilities (capability_endpoint_version_id);
CREATE INDEX IF NOT EXISTS modules_installation_capabilities_status_idx ON modules.installation_capabilities (exposure_status);

CREATE TABLE IF NOT EXISTS modules.health_snapshots (
    module_health_id text PRIMARY KEY CHECK (module_health_id LIKE 'module_health_%'),
    module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id) ON DELETE CASCADE,
    health_status text NOT NULL CHECK (health_status IN ('ok', 'degraded', 'blocked', 'unknown')),
    installation_status text NOT NULL,
    target_node_status text NOT NULL DEFAULT '',
    provider_count integer NOT NULL DEFAULT 0 CHECK (provider_count >= 0),
    capability_count integer NOT NULL DEFAULT 0 CHECK (capability_count >= 0),
    namespace_count integer NOT NULL DEFAULT 0 CHECK (namespace_count >= 0),
    missing_requirements jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(missing_requirements) = 'array'),
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(warnings) = 'array'),
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details_json) = 'object'),
    checked_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS modules_health_installation_checked_idx ON modules.health_snapshots (module_installation_id, checked_at DESC);
CREATE INDEX IF NOT EXISTS modules_health_status_idx ON modules.health_snapshots (health_status);

-- +goose Down

DROP TABLE IF EXISTS modules.health_snapshots;
DROP TABLE IF EXISTS modules.installation_capabilities;
DROP TABLE IF EXISTS modules.installation_providers;
DROP TABLE IF EXISTS modules.namespaces;
DROP TABLE IF EXISTS modules.installations;
