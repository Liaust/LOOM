-- +goose Up

CREATE SCHEMA IF NOT EXISTS maintenance;

CREATE TABLE IF NOT EXISTS maintenance.operations (
    maintenance_operation_id text PRIMARY KEY CHECK (maintenance_operation_id LIKE 'maintenance_operation_%'),
    operation_key text NOT NULL UNIQUE,
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    operation_kind text NOT NULL,
    status text NOT NULL CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'cancelled', 'requires_manual_action', 'skipped')),
    subject_kind text NOT NULL DEFAULT '',
    subject_id text NOT NULL DEFAULT '',
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz NULL,
    config_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(config_json) = 'object'),
    result_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(result_json) = 'object'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS maintenance_operations_kind_status_idx ON maintenance.operations (operation_kind, status);
CREATE INDEX IF NOT EXISTS maintenance_operations_worker_started_idx ON maintenance.operations (worker_instance_id, started_at DESC);
CREATE INDEX IF NOT EXISTS maintenance_operations_worker_run_idx ON maintenance.operations (worker_run_id);
CREATE INDEX IF NOT EXISTS maintenance_operations_subject_idx ON maintenance.operations (subject_kind, subject_id);

CREATE TABLE IF NOT EXISTS maintenance.findings (
    maintenance_finding_id text PRIMARY KEY CHECK (maintenance_finding_id LIKE 'maintenance_finding_%'),
    finding_key text NOT NULL UNIQUE,
    worker_instance_id text NOT NULL REFERENCES workers.worker_instances(worker_instance_id) ON DELETE CASCADE,
    worker_run_id text NULL REFERENCES workers.worker_runs(worker_run_id) ON DELETE SET NULL,
    finding_kind text NOT NULL,
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    status text NOT NULL CHECK (status IN ('open', 'acknowledged', 'resolved', 'ignored')),
    subject_kind text NOT NULL DEFAULT '',
    subject_id text NOT NULL DEFAULT '',
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz NULL,
    summary text NOT NULL,
    details_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(details_json) = 'object'),
    resolution_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resolution_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS maintenance_findings_status_severity_idx ON maintenance.findings (status, severity);
CREATE INDEX IF NOT EXISTS maintenance_findings_kind_status_idx ON maintenance.findings (finding_kind, status);
CREATE INDEX IF NOT EXISTS maintenance_findings_worker_status_idx ON maintenance.findings (worker_instance_id, status);
CREATE INDEX IF NOT EXISTS maintenance_findings_subject_status_idx ON maintenance.findings (subject_kind, subject_id, status);
CREATE INDEX IF NOT EXISTS maintenance_findings_last_seen_idx ON maintenance.findings (last_seen_at DESC);

CREATE TABLE IF NOT EXISTS maintenance.artifacts (
    maintenance_artifact_id text PRIMARY KEY CHECK (maintenance_artifact_id LIKE 'maintenance_artifact_%'),
    maintenance_operation_id text NOT NULL REFERENCES maintenance.operations(maintenance_operation_id) ON DELETE CASCADE,
    artifact_kind text NOT NULL,
    uri text NOT NULL,
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    sha256 text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS maintenance_artifacts_operation_idx ON maintenance.artifacts (maintenance_operation_id);
CREATE INDEX IF NOT EXISTS maintenance_artifacts_kind_created_idx ON maintenance.artifacts (artifact_kind, created_at DESC);

-- +goose Down

DROP TABLE IF EXISTS maintenance.artifacts;
DROP TABLE IF EXISTS maintenance.findings;
DROP TABLE IF EXISTS maintenance.operations;
DROP SCHEMA IF EXISTS maintenance;
