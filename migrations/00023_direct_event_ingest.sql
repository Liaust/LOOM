-- +goose Up
ALTER TABLE automation.direct_events
    ADD COLUMN IF NOT EXISTS request_method text NOT NULL DEFAULT 'POST',
    ADD COLUMN IF NOT EXISTS request_path text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS payload_hash text NOT NULL DEFAULT 'sha256:0000000000000000000000000000000000000000000000000000000000000000' CHECK (payload_hash ~ '^sha256:[0-9a-f]{64}$'),
    ADD COLUMN IF NOT EXISTS attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    ADD COLUMN IF NOT EXISTS ingest_worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id),
    ADD COLUMN IF NOT EXISTS route_id text NULL,
    ADD COLUMN IF NOT EXISTS capability_call_id text NULL,
    ADD COLUMN IF NOT EXISTS job_id text NULL,
    ADD COLUMN IF NOT EXISTS result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    ADD COLUMN IF NOT EXISTS result_refs_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_refs_json) = 'object');

ALTER TABLE automation.direct_events
    DROP CONSTRAINT IF EXISTS direct_events_status_check;

ALTER TABLE automation.direct_events
    ADD CONSTRAINT direct_events_status_check CHECK (
        status IN (
            'accepted',
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
    );

CREATE INDEX IF NOT EXISTS automation_direct_events_claim_idx
    ON automation.direct_events (status, received_at ASC)
    WHERE status = 'accepted';

CREATE INDEX IF NOT EXISTS automation_direct_events_payload_hash_idx
    ON automation.direct_events (endpoint_id, payload_hash);

CREATE INDEX IF NOT EXISTS automation_direct_events_route_idx ON automation.direct_events (route_id);
CREATE INDEX IF NOT EXISTS automation_direct_events_capability_call_idx ON automation.direct_events (capability_call_id);
CREATE INDEX IF NOT EXISTS automation_direct_events_job_idx ON automation.direct_events (job_id);

-- +goose Down
DROP INDEX IF EXISTS automation_direct_events_job_idx;
DROP INDEX IF EXISTS automation_direct_events_capability_call_idx;
DROP INDEX IF EXISTS automation_direct_events_route_idx;
DROP INDEX IF EXISTS automation_direct_events_payload_hash_idx;
DROP INDEX IF EXISTS automation_direct_events_claim_idx;

DELETE FROM automation.direct_events WHERE status = 'accepted';

ALTER TABLE automation.direct_events
    DROP CONSTRAINT IF EXISTS direct_events_status_check;

ALTER TABLE automation.direct_events
    ADD CONSTRAINT direct_events_status_check CHECK (
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
    );

ALTER TABLE automation.direct_events
    DROP COLUMN IF EXISTS result_refs_json,
    DROP COLUMN IF EXISTS result_json,
    DROP COLUMN IF EXISTS job_id,
    DROP COLUMN IF EXISTS capability_call_id,
    DROP COLUMN IF EXISTS route_id,
    DROP COLUMN IF EXISTS ingest_worker_run_id,
    DROP COLUMN IF EXISTS attempt_count,
    DROP COLUMN IF EXISTS payload_hash,
    DROP COLUMN IF EXISTS request_path,
    DROP COLUMN IF EXISTS request_method;
