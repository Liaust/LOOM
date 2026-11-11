-- +goose Up
CREATE TABLE notes_workspace.reference_payloads (
 payload_hash text PRIMARY KEY CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
 size_bytes bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 67108864),
 content bytea NOT NULL CHECK (octet_length(content) = size_bytes),
 created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE notes_workspace.bases ADD COLUMN reference_payload_hash text
 REFERENCES notes_workspace.reference_payloads(payload_hash) ON DELETE RESTRICT;
ALTER TABLE notes_workspace.bases ADD CONSTRAINT notes_base_payload_hash
 CHECK (reference_payload_hash IS NULL OR
   (record->>'hash' IS NOT NULL AND reference_payload_hash = record->>'hash'
    AND COALESCE(record->'content','null'::jsonb) = 'null'::jsonb));
CREATE TABLE notes_workspace.reference_dedup_runs (
 plan_hash text PRIMARY KEY,
 plan jsonb NOT NULL CHECK (jsonb_typeof(plan) = 'object'),
 completed_at timestamptz NOT NULL DEFAULT now()
);
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Shared historical Notes payloads require explicit custody recovery; downgrade refused';
END $$;
-- +goose StatementEnd
