-- +goose Up
ALTER TABLE jobs.jobs
    ADD COLUMN IF NOT EXISTS priority integer NOT NULL DEFAULT 100 CHECK (priority >= 0),
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS manual_action_required boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS last_worker_run_id text NULL,
    ADD COLUMN IF NOT EXISTS last_heartbeat_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS cancel_requested_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS cancel_requested_by_actor_id text NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_last_worker_run_id_fkey'
    ) THEN
        ALTER TABLE jobs.jobs
        ADD CONSTRAINT jobs_jobs_last_worker_run_id_fkey
        FOREIGN KEY (last_worker_run_id)
        REFERENCES workers.worker_runs(worker_run_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'jobs_jobs_cancel_requested_by_actor_id_fkey'
    ) THEN
        ALTER TABLE jobs.jobs
        ADD CONSTRAINT jobs_jobs_cancel_requested_by_actor_id_fkey
        FOREIGN KEY (cancel_requested_by_actor_id)
        REFERENCES identity.actors(actor_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS jobs_jobs_queue_ready_idx
    ON jobs.jobs (
        execution_node_id,
        priority,
        COALESCE(next_attempt_at, queued_at, created_at),
        queued_at,
        created_at
    )
    WHERE status = 'queued'
      AND manual_action_required = false;

CREATE INDEX IF NOT EXISTS jobs_jobs_running_lease_idx
    ON jobs.jobs (execution_node_id, lease_expires_at)
    WHERE status = 'running';

CREATE INDEX IF NOT EXISTS jobs_jobs_manual_action_idx
    ON jobs.jobs (updated_at DESC)
    WHERE manual_action_required = true;

CREATE INDEX IF NOT EXISTS jobs_jobs_last_worker_run_idx
    ON jobs.jobs (last_worker_run_id)
    WHERE last_worker_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS jobs_jobs_next_attempt_idx
    ON jobs.jobs (next_attempt_at)
    WHERE next_attempt_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS jobs.jobs_jobs_next_attempt_idx;
DROP INDEX IF EXISTS jobs.jobs_jobs_last_worker_run_idx;
DROP INDEX IF EXISTS jobs.jobs_jobs_manual_action_idx;
DROP INDEX IF EXISTS jobs.jobs_jobs_running_lease_idx;
DROP INDEX IF EXISTS jobs.jobs_jobs_queue_ready_idx;

ALTER TABLE jobs.jobs DROP CONSTRAINT IF EXISTS jobs_jobs_cancel_requested_by_actor_id_fkey;
ALTER TABLE jobs.jobs DROP CONSTRAINT IF EXISTS jobs_jobs_last_worker_run_id_fkey;

ALTER TABLE jobs.jobs
    DROP COLUMN IF EXISTS cancel_requested_by_actor_id,
    DROP COLUMN IF EXISTS cancel_requested_at,
    DROP COLUMN IF EXISTS last_heartbeat_at,
    DROP COLUMN IF EXISTS last_worker_run_id,
    DROP COLUMN IF EXISTS manual_action_required,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS priority;
