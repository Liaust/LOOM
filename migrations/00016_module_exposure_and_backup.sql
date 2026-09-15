-- +goose Up

CREATE TABLE IF NOT EXISTS modules.backup_exports (
    module_backup_export_id text PRIMARY KEY CHECK (module_backup_export_id LIKE 'module_backup_export_%'),
    module_installation_id text NOT NULL REFERENCES modules.installations(module_installation_id) ON DELETE CASCADE,
    module_version_id text NOT NULL REFERENCES modules.module_versions(module_version_id),
    backup_hook_declaration_id text NULL REFERENCES modules.backup_hook_declarations(module_declaration_id),
    export_kind text NOT NULL CHECK (export_kind IN ('manifest')),
    export_status text NOT NULL CHECK (export_status IN ('completed', 'failed')),
    payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload_json) = 'object'),
    payload_hash text NOT NULL CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    storage_uri text NOT NULL DEFAULT '',
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (module_installation_id, export_kind, payload_hash)
);

CREATE INDEX IF NOT EXISTS modules_backup_exports_installation_created_idx ON modules.backup_exports (module_installation_id, created_at DESC);
CREATE INDEX IF NOT EXISTS modules_backup_exports_version_kind_idx ON modules.backup_exports (module_version_id, export_kind);
CREATE INDEX IF NOT EXISTS modules_backup_exports_status_idx ON modules.backup_exports (export_status);

-- +goose Down

DROP TABLE IF EXISTS modules.backup_exports;
