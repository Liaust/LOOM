-- LOOM-owned deterministic project/repository projection snapshots. This is
-- intentionally not a port of upstream provenance migrations 007-009.
CREATE TABLE provenance.project_projection_snapshots (
    id UUID PRIMARY KEY,
    project_id TEXT NOT NULL CHECK (project_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    projection_revision BIGINT NOT NULL CHECK (projection_revision > 0),
    source_revision BIGINT NOT NULL CHECK (source_revision >= 0),
    source_identity_digest TEXT NOT NULL CHECK (source_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_digest TEXT NOT NULL CHECK (source_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_schema_version TEXT NOT NULL CHECK (btrim(source_schema_version) <> ''),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    observed_at TIMESTAMPTZ NOT NULL,
    searchable_summary TEXT NOT NULL CHECK (length(searchable_summary) <= 4000),
    projection_json JSONB NOT NULL CHECK (jsonb_typeof(projection_json) = 'object'),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (project_id, projection_revision),
    UNIQUE (project_id, id)
);

CREATE TABLE provenance.repository_projection_snapshots (
    id UUID PRIMARY KEY,
    project_snapshot_id UUID NOT NULL,
    project_id TEXT NOT NULL CHECK (project_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    repository_id TEXT NOT NULL CHECK (repository_id ~ '^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    source_revision BIGINT NOT NULL CHECK (source_revision > 0),
    source_identity_digest TEXT NOT NULL CHECK (source_identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    source_version TEXT NULL CHECK (source_version IS NULL OR btrim(source_version) <> ''),
    source_digest TEXT NULL CHECK (source_digest IS NULL OR source_digest ~ '^sha256:[0-9a-f]{64}$'),
    snapshot_digest TEXT NOT NULL CHECK (snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
    observed_commit TEXT NULL CHECK (observed_commit IS NULL OR observed_commit ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    tracking_status TEXT NOT NULL CHECK (tracking_status IN (
        'valid', 'not_enabled', 'stale', 'malformed', 'mismatched_owner',
        'not_observed', 'remote_unavailable'
    )),
    observed_at TIMESTAMPTZ NULL,
    searchable_summary TEXT NOT NULL CHECK (length(searchable_summary) <= 4000),
    card_json JSONB NOT NULL CHECK (jsonb_typeof(card_json) = 'object'),
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (repository_id, source_revision),
    FOREIGN KEY (project_id, project_snapshot_id)
        REFERENCES provenance.project_projection_snapshots(project_id, id)
);

CREATE INDEX project_projection_latest_idx
    ON provenance.project_projection_snapshots(project_id, projection_revision DESC, id);
CREATE INDEX repository_projection_latest_idx
    ON provenance.repository_projection_snapshots(repository_id, source_revision DESC, id);
CREATE INDEX repository_projection_project_idx
    ON provenance.repository_projection_snapshots(project_id, repository_id, source_revision DESC);
CREATE INDEX repository_projection_filter_idx
    ON provenance.repository_projection_snapshots(tracking_status, repository_id, source_revision DESC);
CREATE INDEX repository_projection_topics_idx
    ON provenance.repository_projection_snapshots USING GIN ((card_json->'topics'));

CREATE TRIGGER reject_ledger_mutation
BEFORE UPDATE OR DELETE ON provenance.project_projection_snapshots
FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation();

CREATE TRIGGER reject_ledger_mutation
BEFORE UPDATE OR DELETE ON provenance.repository_projection_snapshots
FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation();
