-- +goose Up

CREATE SCHEMA IF NOT EXISTS modules;

CREATE TABLE IF NOT EXISTS modules.packages (
    module_package_id text PRIMARY KEY CHECK (module_package_id LIKE 'module_package_%'),
    package_kind text NOT NULL CHECK (
        package_kind IN ('native_module', 'connector', 'core_package', 'script_pack', 'workflow_pack', 'skill_pack')
    ),
    source_uri text NOT NULL CHECK (length(source_uri) > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    package_size_bytes bigint NOT NULL DEFAULT 0 CHECK (package_size_bytes >= 0),
    manifest_path text NOT NULL DEFAULT 'module.json',
    discovered_at timestamptz NOT NULL DEFAULT now(),
    registered_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    status text NOT NULL CHECK (status IN ('discovered', 'registered', 'invalid', 'deprecated')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (package_kind, source_uri, content_hash)
);

CREATE INDEX IF NOT EXISTS modules_packages_kind_status_idx ON modules.packages (package_kind, status);
CREATE INDEX IF NOT EXISTS modules_packages_source_idx ON modules.packages (source_uri);
CREATE INDEX IF NOT EXISTS modules_packages_discovered_idx ON modules.packages (discovered_at DESC);

CREATE TABLE IF NOT EXISTS modules.module_versions (
    module_version_id text PRIMARY KEY CHECK (module_version_id LIKE 'module_version_%'),
    module_id text NOT NULL CHECK (module_id ~ '^loom\.[a-z0-9-]+(\.[a-z0-9-]+)*$'),
    module_name text NOT NULL CHECK (length(module_name) > 0),
    version text NOT NULL CHECK (length(version) > 0),
    module_package_id text NOT NULL REFERENCES modules.packages(module_package_id),
    manifest_hash text NOT NULL CHECK (manifest_hash ~ '^sha256:[0-9a-f]{64}$'),
    manifest_json jsonb NOT NULL CHECK (jsonb_typeof(manifest_json) = 'object'),
    module_kind text NOT NULL CHECK (module_kind IN ('native', 'internal', 'experimental', 'deprecated')),
    compatible_core_min text NOT NULL DEFAULT '',
    compatible_core_max text NOT NULL DEFAULT '',
    source_ref text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    registered_at timestamptz NOT NULL DEFAULT now(),
    registered_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    status text NOT NULL CHECK (status IN ('valid', 'invalid', 'deprecated', 'blocked')),
    validation_errors jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(validation_errors) = 'array'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_id, version)
);

CREATE INDEX IF NOT EXISTS modules_versions_module_idx ON modules.module_versions (module_id, registered_at DESC);
CREATE INDEX IF NOT EXISTS modules_versions_package_idx ON modules.module_versions (module_package_id);
CREATE INDEX IF NOT EXISTS modules_versions_status_idx ON modules.module_versions (status);

CREATE TABLE IF NOT EXISTS modules.runtime_requirements (
    module_requirement_id text PRIMARY KEY CHECK (module_requirement_id LIKE 'module_requirement_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    requirement_kind text NOT NULL CHECK (
        requirement_kind IN ('runtime_feature', 'node_profile', 'provider', 'connector', 'credential', 'hardware', 'module')
    ),
    requirement_key text NOT NULL CHECK (length(requirement_key) > 0),
    required_version text NOT NULL DEFAULT '',
    required_status text NOT NULL DEFAULT '',
    optional boolean NOT NULL DEFAULT false,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, requirement_kind, requirement_key)
);

CREATE INDEX IF NOT EXISTS modules_requirements_version_idx ON modules.runtime_requirements (module_version_id);
CREATE INDEX IF NOT EXISTS modules_requirements_kind_idx ON modules.runtime_requirements (requirement_kind, requirement_key);

CREATE TABLE IF NOT EXISTS modules.object_type_declarations (
    module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    object_type text NOT NULL CHECK (length(object_type) > 0),
    schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(schema_json) = 'object'),
    default_classification text NOT NULL DEFAULT 'internal',
    default_indexing_policy text NOT NULL DEFAULT 'none',
    default_backup_policy text NOT NULL DEFAULT 'module_owned',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, object_type)
);

CREATE TABLE IF NOT EXISTS modules.provider_declarations (
    module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    provider_key text NOT NULL CHECK (provider_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    display_name text NOT NULL CHECK (length(display_name) > 0),
    description text NOT NULL DEFAULT '',
    provider_type text NOT NULL DEFAULT 'native_module_provider' CHECK (provider_type = 'native_module_provider'),
    runtime_requirements jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(runtime_requirements) = 'object'),
    health_check_spec jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(health_check_spec) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, provider_key)
);

CREATE INDEX IF NOT EXISTS modules_provider_declarations_version_idx ON modules.provider_declarations (module_version_id);

CREATE TABLE IF NOT EXISTS modules.capability_declarations (
    module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    provider_key text NOT NULL CHECK (provider_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    endpoint_name text NOT NULL CHECK (endpoint_name ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$'),
    capability_class_namespace text NOT NULL CHECK (length(capability_class_namespace) > 0),
    capability_class_name text NOT NULL CHECK (length(capability_class_name) > 0),
    display_name text NOT NULL CHECK (length(display_name) > 0),
    description text NOT NULL DEFAULT '',
    form text NOT NULL CHECK (form IN ('query', 'command', 'job', 'session', 'stream', 'subscription', 'lease')),
    input_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(input_schema_json) = 'object'),
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema_json) = 'object'),
    execution_authorization_level integer NOT NULL CHECK (execution_authorization_level BETWEEN 1 AND 5),
    risk_level text NOT NULL CHECK (risk_level IN ('low', 'medium', 'high', 'critical')),
    side_effects_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(side_effects_json) = 'object'),
    credential_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(credential_requirements_json) = 'object'),
    policy_requirements_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(policy_requirements_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, provider_key, endpoint_name)
);

CREATE INDEX IF NOT EXISTS modules_capability_declarations_version_idx ON modules.capability_declarations (module_version_id);
CREATE INDEX IF NOT EXISTS modules_capability_declarations_provider_idx ON modules.capability_declarations (module_version_id, provider_key);

CREATE TABLE IF NOT EXISTS modules.usage_document_declarations (
    module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    target_kind text NOT NULL CHECK (target_kind IN ('provider', 'capability')),
    target_ref text NOT NULL CHECK (length(target_ref) > 0),
    title text NOT NULL CHECK (length(title) > 0),
    path text NOT NULL CHECK (length(path) > 0),
    body_format text NOT NULL DEFAULT 'markdown' CHECK (body_format IN ('markdown', 'plain')),
    section_map_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(section_map_json) = 'object'),
    visibility_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(visibility_policy_json) = 'object'),
    review_status text NOT NULL DEFAULT 'pending_review' CHECK (review_status IN ('pending_review', 'approved', 'rejected')),
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, target_kind, target_ref, path)
);

CREATE INDEX IF NOT EXISTS modules_usage_document_declarations_version_idx ON modules.usage_document_declarations (module_version_id);

CREATE TABLE IF NOT EXISTS modules.backup_hook_declarations (
    module_declaration_id text PRIMARY KEY CHECK (module_declaration_id LIKE 'module_declaration_%'),
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id) ON DELETE CASCADE,
    hook_key text NOT NULL CHECK (hook_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
    hook_kind text NOT NULL CHECK (hook_kind IN ('manifest', 'database_export', 'file_export', 'object_manifest', 'restore_metadata')),
    trigger_mode text NOT NULL DEFAULT 'manual' CHECK (trigger_mode IN ('manual', 'scheduled_later', 'pre_update', 'pre_remove')),
    output_schema_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(output_schema_json) = 'object'),
    retention_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(retention_policy_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_version_id, hook_key)
);

CREATE INDEX IF NOT EXISTS modules_backup_hook_declarations_version_idx ON modules.backup_hook_declarations (module_version_id);

-- +goose Down

DROP TABLE IF EXISTS modules.backup_hook_declarations;
DROP TABLE IF EXISTS modules.usage_document_declarations;
DROP TABLE IF EXISTS modules.capability_declarations;
DROP TABLE IF EXISTS modules.provider_declarations;
DROP TABLE IF EXISTS modules.object_type_declarations;
DROP TABLE IF EXISTS modules.runtime_requirements;
DROP TABLE IF EXISTS modules.module_versions;
DROP TABLE IF EXISTS modules.packages;

DROP SCHEMA IF EXISTS modules;
