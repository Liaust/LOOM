-- +goose Up
CREATE SCHEMA IF NOT EXISTS watched_roots;

CREATE TABLE IF NOT EXISTS watched_roots.roots (
    watched_root_id text PRIMARY KEY CHECK (watched_root_id LIKE 'watched_root_%'),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE CASCADE,
    root_key text NOT NULL,
    worker_key text NOT NULL DEFAULT '',
    display_name text NOT NULL DEFAULT '',
    safe_root_key text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'unknown',
    config_hash text NOT NULL DEFAULT '',
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    summary_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    last_reported_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (node_id, root_key)
);

CREATE TABLE IF NOT EXISTS watched_roots.findings (
    watched_root_finding_id text PRIMARY KEY CHECK (watched_root_finding_id LIKE 'watched_root_finding_%'),
    watched_root_id text NOT NULL REFERENCES watched_roots.roots(watched_root_id) ON DELETE CASCADE,
    node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE CASCADE,
    root_key text NOT NULL,
    finding_key text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    status text NOT NULL CHECK (status IN ('open', 'resolved', 'ignored')),
    finding_kind text NOT NULL,
    relative_path text NOT NULL DEFAULT '',
    summary text NOT NULL DEFAULT '',
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (watched_root_id, finding_key)
);

CREATE INDEX IF NOT EXISTS watched_roots_roots_node_status_idx
    ON watched_roots.roots (node_id, status, updated_at DESC);

CREATE INDEX IF NOT EXISTS watched_roots_findings_node_status_idx
    ON watched_roots.findings (node_id, status, severity, updated_at DESC);

CREATE INDEX IF NOT EXISTS watched_roots_findings_root_status_idx
    ON watched_roots.findings (watched_root_id, status, severity, updated_at DESC);

-- +goose Down
DROP TABLE IF EXISTS watched_roots.findings;
DROP TABLE IF EXISTS watched_roots.roots;
DROP SCHEMA IF EXISTS watched_roots;
