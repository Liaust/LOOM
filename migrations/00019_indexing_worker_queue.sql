-- +goose Up

ALTER TABLE search.index_status
    ADD COLUMN IF NOT EXISTS attempt_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS claimed_by_worker_run_id text NULL,
    ADD COLUMN IF NOT EXISTS claim_expires_at timestamptz NULL,
    ADD COLUMN IF NOT EXISTS priority integer NOT NULL DEFAULT 100,
    ADD COLUMN IF NOT EXISTS manual_action_required boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS last_worker_run_id text NULL,
    ADD COLUMN IF NOT EXISTS last_attempt_at timestamptz NULL;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'search_index_status_attempt_count_nonnegative'
    ) THEN
        ALTER TABLE search.index_status
        ADD CONSTRAINT search_index_status_attempt_count_nonnegative
        CHECK (attempt_count >= 0);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'search_index_status_priority_nonnegative'
    ) THEN
        ALTER TABLE search.index_status
        ADD CONSTRAINT search_index_status_priority_nonnegative
        CHECK (priority >= 0);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS search_index_status_queue_ready_idx
ON search.index_status (
    index_type,
    status,
    manual_action_required,
    next_attempt_at,
    priority,
    updated_at
);

CREATE INDEX IF NOT EXISTS search_index_status_claimed_run_idx
ON search.index_status (claimed_by_worker_run_id)
WHERE claimed_by_worker_run_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS search_index_status_claim_expires_idx
ON search.index_status (claim_expires_at)
WHERE claim_expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS search_index_status_manual_action_idx
ON search.index_status (status, manual_action_required, updated_at);

-- +goose Down

DROP INDEX IF EXISTS search.search_index_status_manual_action_idx;
DROP INDEX IF EXISTS search.search_index_status_claim_expires_idx;
DROP INDEX IF EXISTS search.search_index_status_claimed_run_idx;
DROP INDEX IF EXISTS search.search_index_status_queue_ready_idx;

ALTER TABLE search.index_status
    DROP CONSTRAINT IF EXISTS search_index_status_priority_nonnegative,
    DROP CONSTRAINT IF EXISTS search_index_status_attempt_count_nonnegative,
    DROP COLUMN IF EXISTS last_attempt_at,
    DROP COLUMN IF EXISTS last_worker_run_id,
    DROP COLUMN IF EXISTS manual_action_required,
    DROP COLUMN IF EXISTS priority,
    DROP COLUMN IF EXISTS claim_expires_at,
    DROP COLUMN IF EXISTS claimed_by_worker_run_id,
    DROP COLUMN IF EXISTS next_attempt_at,
    DROP COLUMN IF EXISTS attempt_count;

