-- +goose Up

CREATE TABLE IF NOT EXISTS knowledge.pipeline_runs (
    knowledge_pipeline_run_id text PRIMARY KEY CHECK (knowledge_pipeline_run_id LIKE 'knowledge_pipeline_run_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NOT NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE CASCADE,
    pipeline_definition_key text NOT NULL CHECK (pipeline_definition_key ~ '^[a-z][a-z0-9_:-]{0,127}$'),
    pipeline_definition_version text NOT NULL CHECK (pipeline_definition_version <> ''),
    generation bigint NOT NULL CHECK (generation > 0),
    status text NOT NULL CHECK (status IN (
        'queued', 'waiting_quiet_window', 'waiting_coordinator', 'waiting_heavy',
        'processing', 'complete', 'complete_with_warnings',
        'blocked_manual_action', 'failed', 'stale', 'cancelled'
    )),
    current_stage_key text NOT NULL DEFAULT '',
    current_execution_class text NOT NULL DEFAULT '' CHECK (current_execution_class IN ('', 'coordinator', 'heavy')),
    priority integer NOT NULL DEFAULT 100 CHECK (priority >= 0),
    source_revision text NOT NULL DEFAULT '',
    source_hash text NOT NULL DEFAULT '' CHECK (source_hash = '' OR source_hash ~ '^sha256:[0-9a-f]{64}$'),
    quiet_window_eligible_at timestamptz NULL,
    claimed_by_worker_run_id text NOT NULL DEFAULT '',
    claim_generation bigint NOT NULL DEFAULT 0 CHECK (claim_generation >= 0),
    claim_expires_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    warning_count integer NOT NULL DEFAULT 0 CHECK (warning_count >= 0),
    plan_snapshot jsonb NOT NULL CHECK (jsonb_typeof(plan_snapshot) = 'object'),
    resource_totals jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_totals) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    CHECK ((claimed_by_worker_run_id = '' AND claim_expires_at IS NULL) OR
           (claimed_by_worker_run_id <> '' AND claim_generation > 0 AND claim_expires_at IS NOT NULL))
);

CREATE UNIQUE INDEX IF NOT EXISTS pipeline_runs_active_revision_definition_idx
ON knowledge.pipeline_runs (knowledge_object_version_id, pipeline_definition_key, pipeline_definition_version)
WHERE status IN ('queued', 'waiting_quiet_window', 'waiting_coordinator', 'waiting_heavy', 'processing', 'blocked_manual_action');

CREATE INDEX IF NOT EXISTS pipeline_runs_ready_queue_idx
ON knowledge.pipeline_runs (current_execution_class, status, priority, quiet_window_eligible_at, created_at);

CREATE INDEX IF NOT EXISTS pipeline_runs_object_generation_idx
ON knowledge.pipeline_runs (knowledge_object_id, generation DESC);

CREATE INDEX IF NOT EXISTS pipeline_runs_current_stage_idx
ON knowledge.pipeline_runs (current_stage_key, status);

CREATE INDEX IF NOT EXISTS pipeline_runs_claim_idx
ON knowledge.pipeline_runs (claim_expires_at)
WHERE claimed_by_worker_run_id <> '';

CREATE TABLE IF NOT EXISTS knowledge.pipeline_stage_runs (
    knowledge_pipeline_stage_run_id text PRIMARY KEY CHECK (knowledge_pipeline_stage_run_id LIKE 'knowledge_pipeline_stage_run_%'),
    knowledge_pipeline_run_id text NOT NULL REFERENCES knowledge.pipeline_runs(knowledge_pipeline_run_id) ON DELETE CASCADE,
    stage_key text NOT NULL CHECK (stage_key IN (
        'metadata', 'native_text', 'pdf_page_analysis', 'pdf_ocr', 'image_description',
        'consolidate_text', 'chunk', 'lexical_index', 'embedding', 'finalize'
    )),
    stage_contract_version text NOT NULL CHECK (stage_contract_version <> ''),
    ordinal integer NOT NULL CHECK (ordinal > 0),
    dependency_snapshot jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(dependency_snapshot) = 'array'),
    execution_class text NOT NULL CHECK (execution_class IN ('coordinator', 'heavy')),
    status text NOT NULL CHECK (status IN (
        'planned', 'waiting_dependency', 'waiting_quiet_window', 'ready', 'processing',
        'complete', 'complete_with_warnings', 'skipped_not_applicable', 'skipped_by_policy',
        'failed_retryable', 'blocked_manual_action', 'stale', 'cancelled'
    )),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claimed_by_worker_run_id text NOT NULL DEFAULT '',
    claim_generation bigint NOT NULL DEFAULT 0 CHECK (claim_generation >= 0),
    next_attempt_at timestamptz NULL,
    input_hash text NOT NULL DEFAULT '' CHECK (input_hash = '' OR input_hash ~ '^sha256:[0-9a-f]{64}$'),
    output_artifact_count integer NOT NULL DEFAULT 0 CHECK (output_artifact_count >= 0),
    progress_completed integer NOT NULL DEFAULT 0 CHECK (progress_completed >= 0),
    progress_total integer NOT NULL DEFAULT 0 CHECK (progress_total >= 0),
    resource_request jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_request) = 'object'),
    resource_usage jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_usage) = 'object'),
    warning_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(warning_json) = 'array'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    CHECK ((status = 'processing') = (claimed_by_worker_run_id <> '' AND claim_generation > 0)),
    UNIQUE (knowledge_pipeline_run_id, stage_key)
);

CREATE INDEX IF NOT EXISTS pipeline_stage_runs_status_idx
ON knowledge.pipeline_stage_runs (status, next_attempt_at, ordinal);

CREATE INDEX IF NOT EXISTS pipeline_stage_runs_run_ordinal_idx
ON knowledge.pipeline_stage_runs (knowledge_pipeline_run_id, ordinal);

CREATE TABLE IF NOT EXISTS knowledge.pipeline_stage_units (
    knowledge_pipeline_stage_unit_id text PRIMARY KEY CHECK (knowledge_pipeline_stage_unit_id LIKE 'knowledge_pipeline_stage_unit_%'),
    knowledge_pipeline_stage_run_id text NOT NULL REFERENCES knowledge.pipeline_stage_runs(knowledge_pipeline_stage_run_id) ON DELETE CASCADE,
    unit_key text NOT NULL CHECK (unit_key <> ''),
    page_number integer NULL CHECK (page_number IS NULL OR page_number > 0),
    unit_input_hash text NOT NULL CHECK (unit_input_hash ~ '^sha256:[0-9a-f]{64}$'),
    status text NOT NULL CHECK (status IN (
        'planned', 'ready', 'processing', 'complete', 'complete_with_warnings',
        'skipped_not_applicable', 'failed_retryable', 'blocked_manual_action', 'stale', 'cancelled'
    )),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    claimed_by_worker_run_id text NOT NULL DEFAULT '',
    claim_generation bigint NOT NULL DEFAULT 0 CHECK (claim_generation >= 0),
    output_artifact_id text NULL,
    confidence double precision NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    resource_usage jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(resource_usage) = 'object'),
    warning_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(warning_json) = 'array'),
    error_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(error_json) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    CHECK ((status = 'processing') = (claimed_by_worker_run_id <> '' AND claim_generation > 0)),
    UNIQUE (knowledge_pipeline_stage_run_id, unit_key)
);

CREATE INDEX IF NOT EXISTS pipeline_stage_units_stage_status_idx
ON knowledge.pipeline_stage_units (knowledge_pipeline_stage_run_id, status, page_number);

CREATE TABLE IF NOT EXISTS knowledge.derived_artifacts (
    knowledge_derived_artifact_id text PRIMARY KEY CHECK (knowledge_derived_artifact_id LIKE 'knowledge_derived_artifact_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NOT NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE CASCADE,
    knowledge_pipeline_run_id text NOT NULL REFERENCES knowledge.pipeline_runs(knowledge_pipeline_run_id) ON DELETE CASCADE,
    knowledge_pipeline_stage_run_id text NOT NULL REFERENCES knowledge.pipeline_stage_runs(knowledge_pipeline_stage_run_id) ON DELETE CASCADE,
    generation bigint NOT NULL CHECK (generation > 0),
    artifact_kind text NOT NULL CHECK (artifact_kind IN (
        'metadata_text', 'embedded_text', 'structured_text', 'ocr_text',
        'vision_description', 'consolidated_text'
    )),
    source_locator text NOT NULL CHECK (source_locator <> ''),
    text_content text NULL,
    payload_ref text NOT NULL DEFAULT '',
    content_hash text NOT NULL CHECK (content_hash ~ '^sha256:[0-9a-f]{64}$'),
    input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
    generator_key text NOT NULL CHECK (generator_key <> ''),
    generator_version text NOT NULL CHECK (generator_version <> ''),
    engine_key text NOT NULL DEFAULT '',
    engine_version text NOT NULL DEFAULT '',
    prompt_version text NOT NULL DEFAULT '',
    language text NOT NULL DEFAULT '',
    confidence double precision NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
    state text NOT NULL CHECK (state IN ('active', 'historical', 'reusable', 'stale')),
    active boolean NOT NULL DEFAULT false,
    source_created_at timestamptz NULL,
    source_modified_at timestamptz NULL,
    recency_at timestamptz NOT NULL,
    recency_basis text NOT NULL CHECK (recency_basis IN (
        'source_filesystem_mtime', 'source_filesystem_birthtime',
        'frontmatter_updated_at', 'frontmatter_created_at',
        'embedded_modified_at', 'embedded_created_at',
        'source_object_metadata', 'observed_at_fallback'
    )),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz NULL,
    deactivated_at timestamptz NULL,
    CHECK ((text_content IS NOT NULL AND payload_ref = '') OR (text_content IS NULL AND payload_ref <> '')),
    CHECK (active = false OR state = 'active')
);

ALTER TABLE knowledge.pipeline_stage_units
    ADD CONSTRAINT pipeline_stage_units_output_artifact_fk
    FOREIGN KEY (output_artifact_id) REFERENCES knowledge.derived_artifacts(knowledge_derived_artifact_id) ON DELETE SET NULL;

CREATE UNIQUE INDEX IF NOT EXISTS derived_artifacts_active_identity_idx
ON knowledge.derived_artifacts (
    knowledge_object_version_id, artifact_kind, source_locator,
    generator_key, generator_version, input_hash
)
WHERE active = true;

CREATE INDEX IF NOT EXISTS derived_artifacts_object_generation_idx
ON knowledge.derived_artifacts (knowledge_object_id, generation DESC, state);

CREATE INDEX IF NOT EXISTS derived_artifacts_pipeline_stage_idx
ON knowledge.derived_artifacts (knowledge_pipeline_run_id, knowledge_pipeline_stage_run_id);

-- +goose Down

ALTER TABLE IF EXISTS knowledge.pipeline_stage_units
    DROP CONSTRAINT IF EXISTS pipeline_stage_units_output_artifact_fk;
DROP TABLE IF EXISTS knowledge.derived_artifacts;
DROP TABLE IF EXISTS knowledge.pipeline_stage_units;
DROP TABLE IF EXISTS knowledge.pipeline_stage_runs;
DROP TABLE IF EXISTS knowledge.pipeline_runs;
