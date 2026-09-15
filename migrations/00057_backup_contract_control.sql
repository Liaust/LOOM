-- +goose Up
CREATE SCHEMA IF NOT EXISTS backup;

CREATE TABLE IF NOT EXISTS backup.protected_folder_preflights (
    protected_folder_preflight_id text PRIMARY KEY CHECK (protected_folder_preflight_id LIKE 'backup_preflight_%'),
    target_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    requested_path text NOT NULL,
    canonical_path text NOT NULL DEFAULT '',
    status text NOT NULL CHECK (status IN ('pending', 'completed', 'failed', 'expired')),
    request_schema_version text NOT NULL,
    response_schema_version text NOT NULL DEFAULT '',
    communication_message_id text NULL REFERENCES communication.messages(communication_message_id) ON DELETE SET NULL,
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    error_code text NOT NULL DEFAULT '',
    error_message text NOT NULL DEFAULT '' CHECK (length(error_message) <= 2048),
    expires_at timestamptz NOT NULL,
    requested_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    correlation_id text NOT NULL DEFAULT '',
    idempotency_key text NOT NULL DEFAULT '',
    retry_of_preflight_id text NULL REFERENCES backup.protected_folder_preflights(protected_folder_preflight_id),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    completed_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS protected_folder_preflights_node_status_idx
ON backup.protected_folder_preflights (target_node_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS protected_folder_preflights_message_idx
ON backup.protected_folder_preflights (communication_message_id)
WHERE communication_message_id IS NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS protected_folder_preflights_idempotency_idx
ON backup.protected_folder_preflights (target_node_id, idempotency_key)
WHERE idempotency_key <> '';

CREATE INDEX IF NOT EXISTS protected_folder_preflights_retry_idx
ON backup.protected_folder_preflights (retry_of_preflight_id)
WHERE retry_of_preflight_id IS NOT NULL;

ALTER TABLE box.watch_root_registrations
    ADD COLUMN IF NOT EXISTS source_kind text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_contract_key text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_contract_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS source_contract_deleted_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS desired_revision bigint NOT NULL DEFAULT 0 CHECK (desired_revision >= 0),
    ADD COLUMN IF NOT EXISTS applied_revision bigint NOT NULL DEFAULT 0 CHECK (applied_revision >= 0),
    ADD COLUMN IF NOT EXISTS desired_config_hash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS applied_config_hash text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS reconciliation_message_id text NULL REFERENCES communication.messages(communication_message_id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS last_node_ack_status text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_node_acknowledged_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS last_apply_error_code text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_apply_error_message text NOT NULL DEFAULT '' CHECK (length(last_apply_error_message) <= 2048);

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_contract_idx
ON box.watch_root_registrations (source_contract_key)
WHERE source_contract_key <> '';

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_reconciliation_message_idx
ON box.watch_root_registrations (reconciliation_message_id)
WHERE reconciliation_message_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS box_watch_root_registrations_stale_desired_idx
ON box.watch_root_registrations (node_id, desired_revision, applied_revision)
WHERE desired_revision > applied_revision;

-- +goose Down
DROP INDEX IF EXISTS box.box_watch_root_registrations_stale_desired_idx;
DROP INDEX IF EXISTS box.box_watch_root_registrations_reconciliation_message_idx;
DROP INDEX IF EXISTS box.box_watch_root_registrations_contract_idx;

ALTER TABLE box.watch_root_registrations
    DROP COLUMN IF EXISTS last_apply_error_message,
    DROP COLUMN IF EXISTS last_apply_error_code,
    DROP COLUMN IF EXISTS last_node_acknowledged_at,
    DROP COLUMN IF EXISTS last_node_ack_status,
    DROP COLUMN IF EXISTS reconciliation_message_id,
    DROP COLUMN IF EXISTS applied_config_hash,
    DROP COLUMN IF EXISTS desired_config_hash,
    DROP COLUMN IF EXISTS applied_revision,
    DROP COLUMN IF EXISTS desired_revision,
    DROP COLUMN IF EXISTS source_contract_path,
    DROP COLUMN IF EXISTS source_contract_deleted_at,
    DROP COLUMN IF EXISTS source_contract_key,
    DROP COLUMN IF EXISTS source_kind;

DROP TABLE IF EXISTS backup.protected_folder_preflights;
DROP SCHEMA IF EXISTS backup;
