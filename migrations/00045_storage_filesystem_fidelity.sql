-- +goose Up

CREATE TABLE IF NOT EXISTS storage.storage_entry_filesystem_observations (
    storage_filesystem_observation_id text PRIMARY KEY CHECK (storage_filesystem_observation_id LIKE 'storage_filesystem_observation_%'),
    storage_entry_id text NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE SET NULL,
    source_area text NOT NULL DEFAULT 'unknown' CHECK (source_area IN (
        'projects',
        'notes',
        'documents',
        'dropzone',
        'lane',
        'external_watched_root',
        'main_documents',
        'main_archive',
        'unknown'
    )),
    source_node_id text NULL CHECK (source_node_id IS NULL OR source_node_id LIKE 'node_%'),
    source_node_key text NOT NULL DEFAULT '',
    source_ref text NOT NULL DEFAULT '',
    logical_path text NOT NULL,
    object_kind text NOT NULL DEFAULT 'unknown' CHECK (object_kind IN (
        'regular_file',
        'directory',
        'symlink',
        'hard_link',
        'special',
        'package',
        'unknown'
    )),
    source_mode integer NULL CHECK (source_mode IS NULL OR source_mode >= 0),
    executable boolean NOT NULL DEFAULT false,
    uid integer NULL,
    gid integer NULL,
    user_name text NOT NULL DEFAULT '',
    group_name text NOT NULL DEFAULT '',
    symlink_target text NOT NULL DEFAULT '',
    device_id bigint NULL CHECK (device_id IS NULL OR device_id >= 0),
    inode bigint NULL CHECK (inode IS NULL OR inode >= 0),
    link_count bigint NULL CHECK (link_count IS NULL OR link_count >= 0),
    is_hard_link boolean NOT NULL DEFAULT false,
    is_sparse boolean NOT NULL DEFAULT false,
    logical_size_bytes bigint NOT NULL DEFAULT 0 CHECK (logical_size_bytes >= 0),
    allocated_bytes bigint NULL CHECK (allocated_bytes IS NULL OR allocated_bytes >= 0),
    has_xattrs boolean NOT NULL DEFAULT false,
    xattr_names_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(xattr_names_json) = 'array'),
    has_acl boolean NOT NULL DEFAULT false,
    has_resource_fork boolean NOT NULL DEFAULT false,
    has_finder_tags boolean NOT NULL DEFAULT false,
    has_quarantine boolean NOT NULL DEFAULT false,
    is_package boolean NOT NULL DEFAULT false,
    package_kind text NOT NULL DEFAULT '',
    unicode_form text NOT NULL DEFAULT '',
    casefold_key text NOT NULL DEFAULT '',
    hidden boolean NOT NULL DEFAULT false,
    generated_metadata boolean NOT NULL DEFAULT false,
    permission_denied boolean NOT NULL DEFAULT false,
    risks_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(risks_json) = 'array'),
    observed_at timestamptz NOT NULL DEFAULT now(),
    raw_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(raw_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_area, source_node_key, source_ref, logical_path)
);

CREATE INDEX IF NOT EXISTS storage_filesystem_observations_entry_idx
    ON storage.storage_entry_filesystem_observations (storage_entry_id)
    WHERE storage_entry_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS storage_filesystem_observations_source_idx
    ON storage.storage_entry_filesystem_observations (source_area, source_node_key, source_ref, logical_path);

CREATE INDEX IF NOT EXISTS storage_filesystem_observations_kind_idx
    ON storage.storage_entry_filesystem_observations (object_kind, observed_at DESC);

CREATE INDEX IF NOT EXISTS storage_filesystem_observations_risk_idx
    ON storage.storage_entry_filesystem_observations (permission_denied, generated_metadata, is_package, is_sparse, is_hard_link);

CREATE TABLE IF NOT EXISTS storage.storage_fidelity_findings (
    storage_fidelity_finding_id text PRIMARY KEY CHECK (storage_fidelity_finding_id LIKE 'storage_fidelity_finding_%'),
    storage_filesystem_observation_id text NULL REFERENCES storage.storage_entry_filesystem_observations(storage_filesystem_observation_id) ON DELETE SET NULL,
    storage_entry_id text NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE SET NULL,
    node_id text NULL CHECK (node_id IS NULL OR node_id LIKE 'node_%'),
    node_key text NOT NULL DEFAULT '',
    source_area text NOT NULL DEFAULT 'unknown' CHECK (source_area IN (
        'projects',
        'notes',
        'documents',
        'dropzone',
        'lane',
        'external_watched_root',
        'main_documents',
        'main_archive',
        'unknown'
    )),
    source_ref text NOT NULL DEFAULT '',
    logical_path text NOT NULL DEFAULT '',
    severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    finding_kind text NOT NULL,
    summary text NOT NULL DEFAULT '',
    detail_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(detail_json) = 'object'),
    status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'suppressed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz NULL
);

CREATE INDEX IF NOT EXISTS storage_fidelity_findings_source_idx
    ON storage.storage_fidelity_findings (source_area, node_key, source_ref, logical_path);

CREATE INDEX IF NOT EXISTS storage_fidelity_findings_status_idx
    ON storage.storage_fidelity_findings (status, severity, created_at DESC);

CREATE INDEX IF NOT EXISTS storage_fidelity_findings_entry_idx
    ON storage.storage_fidelity_findings (storage_entry_id)
    WHERE storage_entry_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS storage_fidelity_findings_observation_idx
    ON storage.storage_fidelity_findings (storage_filesystem_observation_id)
    WHERE storage_filesystem_observation_id IS NOT NULL;

-- +goose Down

DROP TABLE IF EXISTS storage.storage_fidelity_findings;
DROP TABLE IF EXISTS storage.storage_entry_filesystem_observations;
