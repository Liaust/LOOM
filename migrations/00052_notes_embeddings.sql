-- +goose Up

CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE knowledge.pipeline_statuses
DROP CONSTRAINT IF EXISTS pipeline_statuses_stage_check;

ALTER TABLE knowledge.pipeline_statuses
ADD CONSTRAINT pipeline_statuses_stage_check
CHECK (
    stage IN ('metadata', 'text_extraction', 'chunking', 'bm25', 'projection', 'embedding')
);

CREATE TABLE IF NOT EXISTS knowledge.embedding_runtime_models (
    embedding_runtime_model_id text PRIMARY KEY CHECK (embedding_runtime_model_id LIKE 'embedding_runtime_model_%'),
    runtime_key text NOT NULL CHECK (runtime_key IN ('ollama')),
    model_key text NOT NULL CHECK (model_key <> ''),
    dimensions integer NOT NULL CHECK (dimensions > 0),
    distance_metric text NOT NULL DEFAULT 'cosine' CHECK (distance_metric IN ('cosine')),
    max_input_tokens integer NULL CHECK (max_input_tokens IS NULL OR max_input_tokens > 0),
    default_quiet_window_seconds integer NOT NULL DEFAULT 600 CHECK (default_quiet_window_seconds > 0),
    default_concurrency integer NOT NULL DEFAULT 1 CHECK (default_concurrency > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (runtime_key, model_key, dimensions)
);

INSERT INTO knowledge.embedding_runtime_models (
    embedding_runtime_model_id,
    runtime_key,
    model_key,
    dimensions,
    distance_metric,
    default_quiet_window_seconds,
    default_concurrency,
    status
)
VALUES (
    'embedding_runtime_model_ollama_mxbai_embed_large_1024',
    'ollama',
    'mxbai-embed-large',
    1024,
    'cosine',
    600,
    1,
    'active'
)
ON CONFLICT (runtime_key, model_key, dimensions) DO UPDATE
SET
    distance_metric = EXCLUDED.distance_metric,
    default_quiet_window_seconds = EXCLUDED.default_quiet_window_seconds,
    default_concurrency = EXCLUDED.default_concurrency,
    updated_at = now();

CREATE TABLE IF NOT EXISTS knowledge.embedding_settings (
    embedding_settings_id text PRIMARY KEY CHECK (embedding_settings_id = 'notes_embeddings'),
    enabled boolean NOT NULL DEFAULT false,
    runtime_key text NOT NULL DEFAULT 'ollama' CHECK (runtime_key IN ('ollama')),
    model_key text NOT NULL DEFAULT 'mxbai-embed-large' CHECK (model_key <> ''),
    dimensions integer NOT NULL DEFAULT 1024 CHECK (dimensions > 0),
    distance_metric text NOT NULL DEFAULT 'cosine' CHECK (distance_metric IN ('cosine')),
    ollama_url text NOT NULL DEFAULT 'http://127.0.0.1:11434' CHECK (ollama_url <> ''),
    quiet_window_seconds integer NOT NULL DEFAULT 600 CHECK (quiet_window_seconds > 0),
    global_concurrency integer NOT NULL DEFAULT 1 CHECK (global_concurrency > 0),
    history_per_lineage integer NOT NULL DEFAULT 5 CHECK (history_per_lineage > 0),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO knowledge.embedding_settings (
    embedding_settings_id,
    enabled,
    runtime_key,
    model_key,
    dimensions,
    distance_metric,
    ollama_url,
    quiet_window_seconds,
    global_concurrency,
    history_per_lineage
)
VALUES (
    'notes_embeddings',
    false,
    'ollama',
    'mxbai-embed-large',
    1024,
    'cosine',
    'http://127.0.0.1:11434',
    600,
    1,
    5
)
ON CONFLICT (embedding_settings_id) DO NOTHING;

CREATE TABLE IF NOT EXISTS knowledge.embedding_object_states (
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE SET NULL,
    runtime_key text NOT NULL DEFAULT 'ollama' CHECK (runtime_key IN ('ollama')),
    model_key text NOT NULL DEFAULT 'mxbai-embed-large' CHECK (model_key <> ''),
    dimensions integer NOT NULL DEFAULT 1024 CHECK (dimensions > 0),
    status text NOT NULL DEFAULT 'not_started' CHECK (
        status IN (
            'not_started',
            'queued',
            'processing',
            'complete',
            'stale',
            'failed',
            'disabled_by_policy',
            'skipped_unsupported'
        )
    ),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    last_content_change_at timestamptz NULL,
    eligible_at timestamptz NULL,
    last_queued_at timestamptz NULL,
    last_started_at timestamptz NULL,
    last_completed_at timestamptz NULL,
    last_failed_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (knowledge_object_id, runtime_key, model_key, dimensions)
);

CREATE INDEX IF NOT EXISTS embedding_object_states_status_idx
ON knowledge.embedding_object_states (status, eligible_at);

CREATE INDEX IF NOT EXISTS embedding_object_states_version_idx
ON knowledge.embedding_object_states (knowledge_object_version_id)
WHERE knowledge_object_version_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS knowledge.chunk_embeddings (
    knowledge_chunk_embedding_id text PRIMARY KEY CHECK (knowledge_chunk_embedding_id LIKE 'knowledge_chunk_embedding_%'),
    knowledge_chunk_id text NULL REFERENCES knowledge.knowledge_chunks(knowledge_chunk_id) ON DELETE SET NULL,
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE SET NULL,
    embedding_runtime_model_id text NOT NULL REFERENCES knowledge.embedding_runtime_models(embedding_runtime_model_id),
    runtime_key text NOT NULL DEFAULT 'ollama' CHECK (runtime_key IN ('ollama')),
    model_key text NOT NULL DEFAULT 'mxbai-embed-large' CHECK (model_key <> ''),
    dimensions integer NOT NULL DEFAULT 1024 CHECK (dimensions = 1024),
    distance_metric text NOT NULL DEFAULT 'cosine' CHECK (distance_metric IN ('cosine')),
    chunk_hash text NOT NULL CHECK (chunk_hash ~ '^sha256:[0-9a-f]{64}$'),
    chunker_version text NOT NULL CHECK (chunker_version <> ''),
    input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
    token_count_estimate integer NULL CHECK (token_count_estimate IS NULL OR token_count_estimate >= 0),
    source_generation bigint NOT NULL DEFAULT 0 CHECK (source_generation >= 0),
    embedding vector(1024) NOT NULL,
    status text NOT NULL DEFAULT 'reusable' CHECK (
        status IN ('active', 'historical', 'reusable', 'stale', 'failed')
    ),
    active boolean NOT NULL DEFAULT false,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    activated_at timestamptz NULL,
    deactivated_at timestamptz NULL
);

CREATE INDEX IF NOT EXISTS chunk_embeddings_reusable_input_idx
ON knowledge.chunk_embeddings (input_hash, runtime_key, model_key, dimensions);

CREATE UNIQUE INDEX IF NOT EXISTS chunk_embeddings_active_chunk_model_idx
ON knowledge.chunk_embeddings (knowledge_chunk_id, runtime_key, model_key, dimensions)
WHERE active = true AND knowledge_chunk_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS chunk_embeddings_object_idx
ON knowledge.chunk_embeddings (knowledge_object_id, status, active);

CREATE INDEX IF NOT EXISTS chunk_embeddings_version_idx
ON knowledge.chunk_embeddings (knowledge_object_version_id)
WHERE knowledge_object_version_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS chunk_embeddings_embedding_hnsw_idx
ON knowledge.chunk_embeddings USING hnsw (embedding vector_cosine_ops)
WHERE active = true;

CREATE TABLE IF NOT EXISTS knowledge.embedding_work_items (
    knowledge_embedding_work_item_id text PRIMARY KEY CHECK (knowledge_embedding_work_item_id LIKE 'knowledge_embedding_work_item_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE CASCADE,
    knowledge_chunk_id text NULL REFERENCES knowledge.knowledge_chunks(knowledge_chunk_id) ON DELETE CASCADE,
    runtime_key text NOT NULL DEFAULT 'ollama' CHECK (runtime_key IN ('ollama')),
    model_key text NOT NULL DEFAULT 'mxbai-embed-large' CHECK (model_key <> ''),
    dimensions integer NOT NULL DEFAULT 1024 CHECK (dimensions = 1024),
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0),
    chunk_hash text NOT NULL DEFAULT '' CHECK (chunk_hash = '' OR chunk_hash ~ '^sha256:[0-9a-f]{64}$'),
    chunker_version text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'queued' CHECK (
        status IN (
            'queued',
            'processing',
            'complete',
            'stale',
            'failed',
            'disabled_by_policy',
            'skipped_unsupported'
        )
    ),
    eligible_at timestamptz NOT NULL,
    queued_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    priority integer NOT NULL DEFAULT 100 CHECK (priority >= 0),
    claimed_by_worker_run_id text NOT NULL DEFAULT '',
    claim_expires_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS embedding_work_items_ready_idx
ON knowledge.embedding_work_items (status, eligible_at, priority, queued_at);

CREATE INDEX IF NOT EXISTS embedding_work_items_object_generation_idx
ON knowledge.embedding_work_items (knowledge_object_id, runtime_key, model_key, dimensions, generation);

CREATE INDEX IF NOT EXISTS embedding_work_items_claim_idx
ON knowledge.embedding_work_items (claimed_by_worker_run_id, claim_expires_at)
WHERE claimed_by_worker_run_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS embedding_work_items_active_chunk_generation_idx
ON knowledge.embedding_work_items (
    knowledge_chunk_id,
    runtime_key,
    model_key,
    dimensions,
    generation
)
WHERE knowledge_chunk_id IS NOT NULL AND status IN ('queued', 'processing');

-- +goose Down

DROP INDEX IF EXISTS knowledge.embedding_work_items_active_chunk_generation_idx;
DROP INDEX IF EXISTS knowledge.embedding_work_items_claim_idx;
DROP INDEX IF EXISTS knowledge.embedding_work_items_object_generation_idx;
DROP INDEX IF EXISTS knowledge.embedding_work_items_ready_idx;
DROP TABLE IF EXISTS knowledge.embedding_work_items;

DROP INDEX IF EXISTS knowledge.chunk_embeddings_embedding_hnsw_idx;
DROP INDEX IF EXISTS knowledge.chunk_embeddings_version_idx;
DROP INDEX IF EXISTS knowledge.chunk_embeddings_object_idx;
DROP INDEX IF EXISTS knowledge.chunk_embeddings_active_chunk_model_idx;
DROP INDEX IF EXISTS knowledge.chunk_embeddings_reusable_input_idx;
DROP TABLE IF EXISTS knowledge.chunk_embeddings;

DROP INDEX IF EXISTS knowledge.embedding_object_states_version_idx;
DROP INDEX IF EXISTS knowledge.embedding_object_states_status_idx;
DROP TABLE IF EXISTS knowledge.embedding_object_states;

DROP TABLE IF EXISTS knowledge.embedding_settings;
DROP TABLE IF EXISTS knowledge.embedding_runtime_models;

ALTER TABLE knowledge.pipeline_statuses
DROP CONSTRAINT IF EXISTS pipeline_statuses_stage_check;

ALTER TABLE knowledge.pipeline_statuses
ADD CONSTRAINT pipeline_statuses_stage_check
CHECK (
    stage IN ('metadata', 'text_extraction', 'chunking', 'bm25', 'projection')
);
