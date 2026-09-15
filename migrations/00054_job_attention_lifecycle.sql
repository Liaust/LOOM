-- +goose Up
ALTER TABLE jobs.jobs
    ADD COLUMN IF NOT EXISTS failure_attention_status text NOT NULL DEFAULT 'active',
    ADD COLUMN IF NOT EXISTS failure_attention_updated_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS failure_attention_updated_by_actor_id text NULL,
    ADD COLUMN IF NOT EXISTS failure_attention_note text NOT NULL DEFAULT '';

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_failure_attention_status_check'
    ) THEN
        ALTER TABLE jobs.jobs
        ADD CONSTRAINT jobs_jobs_failure_attention_status_check
        CHECK (failure_attention_status IN ('active', 'acknowledged', 'archived'));
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_failure_attention_updated_by_actor_id_fkey'
    ) THEN
        ALTER TABLE jobs.jobs
        ADD CONSTRAINT jobs_jobs_failure_attention_updated_by_actor_id_fkey
        FOREIGN KEY (failure_attention_updated_by_actor_id)
        REFERENCES identity.actors(actor_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS jobs_jobs_failure_attention_idx
    ON jobs.jobs (failure_attention_status, status, updated_at DESC)
    WHERE status IN ('failed', 'timed_out')
       OR manual_action_required = true;

-- +goose Down
DROP INDEX IF EXISTS jobs.jobs_jobs_failure_attention_idx;

ALTER TABLE jobs.jobs DROP CONSTRAINT IF EXISTS jobs_jobs_failure_attention_updated_by_actor_id_fkey;
ALTER TABLE jobs.jobs DROP CONSTRAINT IF EXISTS jobs_jobs_failure_attention_status_check;

ALTER TABLE jobs.jobs
    DROP COLUMN IF EXISTS failure_attention_note,
    DROP COLUMN IF EXISTS failure_attention_updated_by_actor_id,
    DROP COLUMN IF EXISTS failure_attention_updated_at,
    DROP COLUMN IF EXISTS failure_attention_status;
