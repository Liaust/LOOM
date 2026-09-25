-- +goose Up
ALTER TABLE knowledge.pipeline_runs ADD COLUMN source_snapshot jsonb;
ALTER TABLE knowledge.pipeline_runs ADD CONSTRAINT pipeline_source_snapshot_object
    CHECK (source_snapshot IS NULL OR COALESCE((jsonb_typeof(source_snapshot) = 'object'
      AND source_snapshot->'object'->>'knowledge_object_id' = knowledge_object_id
      AND COALESCE(source_snapshot->'object'->>'source_hash','') = source_hash
      AND COALESCE(source_snapshot->'object'->>'source_revision','') = source_revision),false));
CREATE UNIQUE INDEX pipeline_selected_object_idx ON knowledge.pipeline_runs (knowledge_object_id)
    WHERE source_snapshot IS NOT NULL AND status IN
      ('queued','waiting_quiet_window','waiting_coordinator','waiting_heavy','processing');

ALTER TABLE knowledge.knowledge_object_versions ADD CONSTRAINT knowledge_version_object_unique
    UNIQUE (knowledge_object_id, knowledge_object_version_id);
ALTER TABLE knowledge.knowledge_objects
    ADD COLUMN lexical_version_id text,
    ADD COLUMN semantic_version_id text,
    ADD COLUMN lexical_published_at timestamptz,
    ADD COLUMN semantic_published_at timestamptz,
    ADD COLUMN semantic_runtime_key text,
    ADD COLUMN semantic_model_key text,
    ADD COLUMN semantic_dimensions integer,
    ADD FOREIGN KEY (knowledge_object_id, lexical_version_id)
      REFERENCES knowledge.knowledge_object_versions (knowledge_object_id, knowledge_object_version_id),
    ADD FOREIGN KEY (knowledge_object_id, semantic_version_id)
      REFERENCES knowledge.knowledge_object_versions (knowledge_object_id, knowledge_object_version_id);

-- Only existing, current, successfully published output is adopted. Partial
-- embeddings are never promoted merely because some active vectors exist.
UPDATE knowledge.knowledge_objects o SET lexical_version_id=v.knowledge_object_version_id,
    lexical_published_at=v.published_at
FROM (SELECT DISTINCT ON (v.knowledge_object_id) v.knowledge_object_id,v.knowledge_object_version_id,
      max(d.indexed_at) AS published_at
    FROM knowledge.knowledge_object_versions v
    JOIN knowledge.knowledge_objects current ON current.knowledge_object_id=v.knowledge_object_id
      AND current.source_hash=v.source_hash AND current.source_revision=v.source_revision
    JOIN search.search_documents d ON d.source_version_id=v.knowledge_object_version_id
      AND d.source_kind='knowledge_chunk' AND d.index_version='knowledge_bm25_v1'
    GROUP BY v.knowledge_object_id,v.knowledge_object_version_id,v.version_number
    ORDER BY v.knowledge_object_id,v.version_number DESC) v
WHERE v.knowledge_object_id=o.knowledge_object_id;
UPDATE knowledge.knowledge_objects o SET semantic_version_id=o.lexical_version_id,
    semantic_published_at=o.lexical_published_at,
    semantic_runtime_key=settings.runtime_key, semantic_model_key=settings.model_key,
    semantic_dimensions=settings.dimensions
FROM knowledge.embedding_settings settings
WHERE o.lexical_version_id IS NOT NULL AND EXISTS (
    SELECT 1 FROM knowledge.pipeline_runs r JOIN knowledge.pipeline_stage_runs s USING (knowledge_pipeline_run_id)
    WHERE r.knowledge_object_version_id=o.lexical_version_id AND s.stage_key='embedding'
      AND s.status='complete') AND NOT EXISTS (
    SELECT 1 FROM knowledge.knowledge_chunks c WHERE c.knowledge_object_version_id=o.lexical_version_id
      AND NOT EXISTS (SELECT 1 FROM knowledge.chunk_embeddings e
        WHERE e.knowledge_chunk_id=c.knowledge_chunk_id AND e.active AND e.status='active'
          AND e.runtime_key=settings.runtime_key AND e.model_key=settings.model_key AND e.dimensions=settings.dimensions));

-- +goose Down
ALTER TABLE knowledge.knowledge_objects DROP COLUMN semantic_version_id, DROP COLUMN lexical_version_id,
    DROP COLUMN semantic_published_at, DROP COLUMN lexical_published_at,
    DROP COLUMN semantic_runtime_key, DROP COLUMN semantic_model_key, DROP COLUMN semantic_dimensions;
ALTER TABLE knowledge.knowledge_object_versions DROP CONSTRAINT knowledge_version_object_unique;
DROP INDEX knowledge.pipeline_selected_object_idx;
ALTER TABLE knowledge.pipeline_runs DROP COLUMN source_snapshot;
