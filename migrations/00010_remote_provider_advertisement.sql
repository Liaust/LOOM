-- +goose Up

CREATE TABLE IF NOT EXISTS capabilities.provider_advertisements (
    provider_advertisement_id text PRIMARY KEY CHECK (provider_advertisement_id LIKE 'provider_advertisement_%'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    communication_message_id text NULL REFERENCES communication.messages(communication_message_id),
    advertisement_hash text NOT NULL CHECK (advertisement_hash ~ '^sha256:[0-9a-f]{64}$'),
    idempotency_key text NULL,
    status text NOT NULL DEFAULT 'pending_review' CHECK (status IN (
        'received',
        'validated',
        'pending_review',
        'approved',
        'rejected',
        'stale'
    )),
    raw_payload_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(raw_payload_json) = 'object'),
    validation_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(validation_summary_json) = 'object'),
    upsert_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(upsert_summary_json) = 'object'),
    rejection_reason text NOT NULL DEFAULT '',
    received_at timestamptz NOT NULL DEFAULT now(),
    validated_at timestamptz NULL,
    reviewed_at timestamptz NULL,
    reviewed_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    UNIQUE (origin_node_id, advertisement_hash)
);

CREATE INDEX IF NOT EXISTS provider_advertisements_origin_status_idx
    ON capabilities.provider_advertisements (origin_node_id, status, received_at DESC);

CREATE INDEX IF NOT EXISTS provider_advertisements_provider_status_idx
    ON capabilities.provider_advertisements (provider_id, status, received_at DESC);

CREATE INDEX IF NOT EXISTS provider_advertisements_received_idx
    ON capabilities.provider_advertisements (received_at DESC);

-- +goose Down

DROP TABLE IF EXISTS capabilities.provider_advertisements;
