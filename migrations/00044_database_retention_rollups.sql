-- +goose Up

CREATE TABLE IF NOT EXISTS maintenance.database_rollups (
    database_rollup_id text PRIMARY KEY CHECK (database_rollup_id LIKE 'database_rollup_%'),
    rollup_day date NOT NULL,
    rollup_kind text NOT NULL,
    rollup_key text NOT NULL,
    success_count bigint NOT NULL DEFAULT 0 CHECK (success_count >= 0),
    failure_count bigint NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    total_count bigint NOT NULL DEFAULT 0 CHECK (total_count >= 0),
    total_duration_ms bigint NOT NULL DEFAULT 0 CHECK (total_duration_ms >= 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (rollup_day, rollup_kind, rollup_key)
);

CREATE INDEX IF NOT EXISTS maintenance_database_rollups_kind_day_idx
    ON maintenance.database_rollups (rollup_kind, rollup_day DESC);

-- +goose Down

DROP TABLE IF EXISTS maintenance.database_rollups;
