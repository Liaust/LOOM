-- +goose Up
CREATE SCHEMA IF NOT EXISTS knowledge;

CREATE TABLE IF NOT EXISTS knowledge.notes_source_roots (
    notes_source_root_id text PRIMARY KEY CHECK (notes_source_root_id LIKE 'notes_source_root_%'),
    root_kind text NOT NULL CHECK (root_kind IN ('box_notes', 'project_notes')),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    node_key text NOT NULL DEFAULT '',
    project_id text NULL REFERENCES projects.projects(project_id) ON DELETE SET NULL,
    box_watch_root_registration_id text NULL REFERENCES box.watch_root_registrations(box_watch_root_registration_id) ON DELETE SET NULL,
    project_watched_root_registration_id text NULL REFERENCES projects.project_watched_root_registrations(project_watched_root_registration_id) ON DELETE SET NULL,
    backend_root_key text NOT NULL,
    display_name text NOT NULL DEFAULT '',
    source_path text NOT NULL DEFAULT '',
    root_relative_path text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'stale', 'disabled', 'deleted', 'blocked')
    ),
    authorization_metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(authorization_metadata) = 'object'),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS notes_source_roots_identity_idx
ON knowledge.notes_source_roots (
    root_kind,
    node_key,
    COALESCE(project_id, ''),
    backend_root_key
);

CREATE INDEX IF NOT EXISTS notes_source_roots_node_kind_idx
ON knowledge.notes_source_roots (node_key, root_kind, status);

CREATE INDEX IF NOT EXISTS notes_source_roots_project_idx
ON knowledge.notes_source_roots (project_id)
WHERE project_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS knowledge.knowledge_objects (
    knowledge_object_id text PRIMARY KEY CHECK (knowledge_object_id LIKE 'knowledge_object_%'),
    notes_source_root_id text NOT NULL REFERENCES knowledge.notes_source_roots(notes_source_root_id) ON DELETE CASCADE,
    storage_entry_id text NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE SET NULL,
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    source_node_key text NOT NULL DEFAULT '',
    project_id text NULL REFERENCES projects.projects(project_id) ON DELETE SET NULL,
    source_path text NOT NULL DEFAULT '',
    relative_path text NOT NULL CHECK (relative_path <> ''),
    title text NOT NULL DEFAULT '',
    file_class text NOT NULL DEFAULT 'unknown' CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    )),
    mime_type text NOT NULL DEFAULT '',
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    source_hash text NOT NULL DEFAULT '' CHECK (source_hash = '' OR source_hash ~ '^sha256:[0-9a-f]{64}$'),
    source_revision text NOT NULL DEFAULT '',
    processing_state text NOT NULL DEFAULT 'metadata_only' CHECK (
        processing_state IN (
            'metadata_only',
            'text_extracted',
            'chunked',
            'indexed',
            'embedded',
            'failed',
            'stale',
            'deleted'
        )
    ),
    pipeline_key text NOT NULL DEFAULT '',
    pipeline_version text NOT NULL DEFAULT '',
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    last_processed_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS knowledge_objects_active_source_path_idx
ON knowledge.knowledge_objects (notes_source_root_id, relative_path)
WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS knowledge_objects_source_root_idx
ON knowledge.knowledge_objects (notes_source_root_id, processing_state);

CREATE INDEX IF NOT EXISTS knowledge_objects_storage_entry_idx
ON knowledge.knowledge_objects (storage_entry_id)
WHERE storage_entry_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS knowledge_objects_project_idx
ON knowledge.knowledge_objects (project_id)
WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS knowledge_objects_hash_idx
ON knowledge.knowledge_objects (source_hash)
WHERE source_hash <> '';

CREATE TABLE IF NOT EXISTS knowledge.knowledge_object_versions (
    knowledge_object_version_id text PRIMARY KEY CHECK (knowledge_object_version_id LIKE 'knowledge_object_version_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    version_number integer NOT NULL CHECK (version_number > 0),
    storage_entry_id text NULL REFERENCES storage.storage_entries(storage_entry_id) ON DELETE SET NULL,
    source_hash text NOT NULL DEFAULT '' CHECK (source_hash = '' OR source_hash ~ '^sha256:[0-9a-f]{64}$'),
    source_revision text NOT NULL DEFAULT '',
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    mime_type text NOT NULL DEFAULT '',
    file_class text NOT NULL DEFAULT 'unknown' CHECK (file_class IN (
        'markdown',
        'text',
        'pdf',
        'image',
        'video',
        'audio',
        'archive',
        'code',
        'directory',
        'package',
        'generated_metadata',
        'binary',
        'unknown'
    )),
    source_path text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    observed_at timestamptz NOT NULL DEFAULT now(),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (knowledge_object_id, version_number)
);

CREATE INDEX IF NOT EXISTS knowledge_object_versions_hash_idx
ON knowledge.knowledge_object_versions (source_hash)
WHERE source_hash <> '';

CREATE INDEX IF NOT EXISTS knowledge_object_versions_storage_entry_idx
ON knowledge.knowledge_object_versions (storage_entry_id)
WHERE storage_entry_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS knowledge.knowledge_chunks (
    knowledge_chunk_id text PRIMARY KEY CHECK (knowledge_chunk_id LIKE 'knowledge_chunk_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE CASCADE,
    chunk_index integer NOT NULL CHECK (chunk_index > 0),
    chunk_text text NOT NULL,
    chunk_hash text NOT NULL CHECK (chunk_hash ~ '^sha256:[0-9a-f]{64}$'),
    structural_path text NOT NULL DEFAULT '',
    start_offset integer NULL CHECK (start_offset IS NULL OR start_offset >= 0),
    end_offset integer NULL CHECK (
        end_offset IS NULL
        OR (end_offset >= 0 AND (start_offset IS NULL OR end_offset >= start_offset))
    ),
    token_count_estimate integer NULL CHECK (token_count_estimate IS NULL OR token_count_estimate >= 0),
    chunker_version text NOT NULL,
    status text NOT NULL DEFAULT 'created' CHECK (
        status IN ('created', 'indexed', 'stale', 'failed', 'disabled_by_policy')
    ),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    indexed_at timestamptz NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS knowledge_chunks_object_version_index_idx
ON knowledge.knowledge_chunks (knowledge_object_version_id, chunk_index)
WHERE knowledge_object_version_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS knowledge_chunks_object_idx
ON knowledge.knowledge_chunks (knowledge_object_id, status);

CREATE TABLE IF NOT EXISTS knowledge.pipeline_statuses (
    knowledge_pipeline_status_id text PRIMARY KEY CHECK (knowledge_pipeline_status_id LIKE 'knowledge_pipeline_status_%'),
    knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    knowledge_object_version_id text NULL REFERENCES knowledge.knowledge_object_versions(knowledge_object_version_id) ON DELETE CASCADE,
    pipeline_key text NOT NULL,
    pipeline_version text NOT NULL,
    stage text NOT NULL CHECK (
        stage IN ('metadata', 'text_extraction', 'chunking', 'bm25', 'projection')
    ),
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
    queued_at timestamptz NULL,
    started_at timestamptz NULL,
    completed_at timestamptz NULL,
    failed_at timestamptz NULL,
    last_error_code text NOT NULL DEFAULT '',
    last_error_message text NOT NULL DEFAULT '',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS pipeline_statuses_identity_idx
ON knowledge.pipeline_statuses (
    knowledge_object_id,
    COALESCE(knowledge_object_version_id, ''),
    pipeline_key,
    stage
);

CREATE INDEX IF NOT EXISTS pipeline_statuses_status_idx
ON knowledge.pipeline_statuses (status, stage);

CREATE TABLE IF NOT EXISTS knowledge.object_links (
    knowledge_object_link_id text PRIMARY KEY CHECK (knowledge_object_link_id LIKE 'knowledge_object_link_%'),
    source_knowledge_object_id text NOT NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE CASCADE,
    target_knowledge_object_id text NULL REFERENCES knowledge.knowledge_objects(knowledge_object_id) ON DELETE SET NULL,
    chunk_id text NULL REFERENCES knowledge.knowledge_chunks(knowledge_chunk_id) ON DELETE SET NULL,
    link_kind text NOT NULL CHECK (link_kind IN ('markdown', 'wikilink', 'url', 'file', 'unknown')),
    raw_target text NOT NULL CHECK (raw_target <> ''),
    normalized_target text NOT NULL DEFAULT '',
    link_text text NOT NULL DEFAULT '',
    status text NOT NULL DEFAULT 'unresolved' CHECK (status IN ('unresolved', 'resolved', 'broken', 'ignored')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS object_links_source_idx
ON knowledge.object_links (source_knowledge_object_id, link_kind, status);

CREATE INDEX IF NOT EXISTS object_links_target_idx
ON knowledge.object_links (target_knowledge_object_id)
WHERE target_knowledge_object_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS object_links_normalized_target_idx
ON knowledge.object_links (normalized_target)
WHERE normalized_target <> '';

-- +goose Down
DROP TABLE IF EXISTS knowledge.object_links;
DROP TABLE IF EXISTS knowledge.pipeline_statuses;
DROP TABLE IF EXISTS knowledge.knowledge_chunks;
DROP TABLE IF EXISTS knowledge.knowledge_object_versions;
DROP TABLE IF EXISTS knowledge.knowledge_objects;
DROP TABLE IF EXISTS knowledge.notes_source_roots;
DROP SCHEMA IF EXISTS knowledge;
