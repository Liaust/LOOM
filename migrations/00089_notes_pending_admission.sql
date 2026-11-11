-- +goose Up
-- Delivery identity is durable work, not enrollment or read authority.
CREATE TABLE knowledge.pending_source_admissions (
    object_version_id text PRIMARY KEY REFERENCES objects.object_versions(object_version_id) ON DELETE CASCADE,
    received_at timestamptz NOT NULL DEFAULT now(),
    next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pending_source_admissions_ready_idx
    ON knowledge.pending_source_admissions(next_attempt_at, received_at, object_version_id);

-- +goose Down
DROP TABLE knowledge.pending_source_admissions;
