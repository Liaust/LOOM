-- +goose Up
-- This is derived retrieval state, not an access grant. Final admission still
-- reads current source, declaration, publication and custody in one snapshot.
-- Keep the initial snapshot and trigger installation atomic with source writes.
LOCK TABLE search.search_documents, knowledge.knowledge_objects,
 knowledge.knowledge_chunks, knowledge.notes_current_custody,
 knowledge.pipeline_runs, knowledge.notes_source_roots IN SHARE ROW EXCLUSIVE MODE;
CREATE VIEW knowledge.notes_bm25_source AS
SELECT sd.search_document_id AS id, ko.knowledge_object_id AS object_id,
       COALESCE(kc.knowledge_chunk_id,'') AS chunk_id,
       COALESCE(kc.knowledge_object_version_id,'') AS version_id,
       COALESCE(kov.source_hash,ko.source_hash) AS source_hash,
       COALESCE(kov.source_revision,ko.source_revision) AS source_revision,
       COALESCE(NULLIF(ko.title,''),sd.title,'') AS title,
       COALESCE(kc.structural_path,sd.summary,'') AS heading,
       ko.relative_path AS path, sd.body,
       COALESCE(sd.metadata->'tags','[]'::jsonb) AS tags,
       COALESCE(ko.project_id,'') AS project_id,
       ko.notes_source_root_id AS root_id, ko.source_node_key AS node_key,
       root.root_kind, ko.file_class,
       COALESCE(sd.recency_at,ko.recency_at) AS recency_at,
       COALESCE(event.to_state,'active') AS lifecycle,
       (COALESCE(kov.source_hash,ko.source_hash)=ko.source_hash AND
        COALESCE(kov.source_revision,ko.source_revision)=ko.source_revision) AS is_current
FROM search.search_documents sd
JOIN knowledge.knowledge_objects ko ON ko.knowledge_object_id=sd.notes_object_id
JOIN knowledge.notes_source_roots root USING(notes_source_root_id)
LEFT JOIN knowledge.knowledge_chunks kc ON sd.source_kind='knowledge_chunk'
 AND kc.knowledge_chunk_id=sd.source_id AND kc.knowledge_object_id=ko.knowledge_object_id
LEFT JOIN knowledge.knowledge_object_versions kov
 ON kov.knowledge_object_version_id=kc.knowledge_object_version_id
 AND kov.knowledge_object_id=ko.knowledge_object_id
LEFT JOIN knowledge.notes_current_custody custody ON custody.knowledge_object_id=ko.knowledge_object_id
LEFT JOIN storage.workspace_lifecycle_events event USING(workspace_lifecycle_event_id)
WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata')
 AND sd.index_version='knowledge_bm25_v1' AND ko.deleted_at IS NULL
 AND (sd.source_kind='knowledge_object_metadata' OR
      (kc.status IN ('created','indexed') AND
       (kc.knowledge_object_version_id IS NULL OR kc.knowledge_object_version_id=ko.lexical_version_id OR
        (ko.lexical_version_id IS NULL AND kov.source_hash=ko.source_hash AND kov.source_revision=ko.source_revision
         AND NOT EXISTS (SELECT 1 FROM knowledge.pipeline_runs pr WHERE pr.knowledge_object_id=ko.knowledge_object_id)))));

CREATE TABLE knowledge.notes_bm25_documents AS SELECT * FROM knowledge.notes_bm25_source;
ALTER TABLE knowledge.notes_bm25_documents ADD PRIMARY KEY(id);
ALTER TABLE knowledge.notes_bm25_documents ADD COLUMN id_sort text COLLATE "C" GENERATED ALWAYS AS (id) STORED;
CREATE INDEX notes_bm25_object_idx ON knowledge.notes_bm25_documents(object_id);
CREATE INDEX notes_bm25_root_idx ON knowledge.notes_bm25_documents(root_id);

-- Publication writers already share transactions. Triggers cover every writer,
-- including archive replay and navigation updates, without a second queue.
-- +goose StatementBegin
CREATE FUNCTION knowledge.refresh_notes_bm25(object_key text, document_key text DEFAULT NULL) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM knowledge.notes_bm25_documents d
    WHERE d.object_id=object_key AND (document_key IS NULL OR d.id=document_key)
      AND NOT EXISTS (SELECT 1 FROM knowledge.notes_bm25_source s WHERE s.id=d.id);
    INSERT INTO knowledge.notes_bm25_documents
        (id,object_id,chunk_id,version_id,source_hash,source_revision,title,heading,path,body,tags,
         project_id,root_id,node_key,root_kind,file_class,recency_at,lifecycle,is_current)
    SELECT * FROM knowledge.notes_bm25_source s
    WHERE s.object_id=object_key AND (document_key IS NULL OR s.id=document_key)
    ON CONFLICT(id) DO UPDATE SET
        object_id=EXCLUDED.object_id,chunk_id=EXCLUDED.chunk_id,version_id=EXCLUDED.version_id,
        source_hash=EXCLUDED.source_hash,source_revision=EXCLUDED.source_revision,
        title=EXCLUDED.title,heading=EXCLUDED.heading,path=EXCLUDED.path,body=EXCLUDED.body,tags=EXCLUDED.tags,
        project_id=EXCLUDED.project_id,root_id=EXCLUDED.root_id,node_key=EXCLUDED.node_key,
        root_kind=EXCLUDED.root_kind,file_class=EXCLUDED.file_class,recency_at=EXCLUDED.recency_at,
        lifecycle=EXCLUDED.lifecycle,is_current=EXCLUDED.is_current
    WHERE (notes_bm25_documents.object_id,notes_bm25_documents.chunk_id,notes_bm25_documents.version_id,
           notes_bm25_documents.source_hash,notes_bm25_documents.source_revision,
           notes_bm25_documents.title,notes_bm25_documents.heading,notes_bm25_documents.path,notes_bm25_documents.body,
           notes_bm25_documents.tags,notes_bm25_documents.project_id,notes_bm25_documents.root_id,
           notes_bm25_documents.node_key,notes_bm25_documents.root_kind,notes_bm25_documents.file_class,
           notes_bm25_documents.recency_at,notes_bm25_documents.lifecycle,notes_bm25_documents.is_current)
      IS DISTINCT FROM
          (EXCLUDED.object_id,EXCLUDED.chunk_id,EXCLUDED.version_id,EXCLUDED.source_hash,EXCLUDED.source_revision,
           EXCLUDED.title,EXCLUDED.heading,EXCLUDED.path,EXCLUDED.body,EXCLUDED.tags,EXCLUDED.project_id,
           EXCLUDED.root_id,EXCLUDED.node_key,EXCLUDED.root_kind,EXCLUDED.file_class,EXCLUDED.recency_at,
           EXCLUDED.lifecycle,EXCLUDED.is_current);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION knowledge.sync_notes_bm25_document() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        DELETE FROM knowledge.notes_bm25_documents WHERE id=OLD.search_document_id;
    ELSE
        DELETE FROM knowledge.notes_bm25_documents WHERE id=NEW.search_document_id
          AND (object_id IS DISTINCT FROM NEW.notes_object_id OR NEW.index_version<>'knowledge_bm25_v1');
        IF NEW.notes_object_id IS NOT NULL THEN
            PERFORM knowledge.refresh_notes_bm25(NEW.notes_object_id,NEW.search_document_id);
        ELSE
            DELETE FROM knowledge.notes_bm25_documents WHERE id=NEW.search_document_id;
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_bm25_document_change AFTER INSERT OR UPDATE OR DELETE ON search.search_documents
FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_document();

-- +goose StatementBegin
CREATE FUNCTION knowledge.sync_notes_bm25_object() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='knowledge_chunks' THEN
        PERFORM knowledge.refresh_notes_bm25(COALESCE(NEW.knowledge_object_id,OLD.knowledge_object_id),sd.search_document_id)
        FROM search.search_documents sd WHERE sd.source_kind='knowledge_chunk'
          AND sd.source_id=COALESCE(NEW.knowledge_chunk_id,OLD.knowledge_chunk_id);
    ELSIF TG_OP='DELETE' THEN
        PERFORM knowledge.refresh_notes_bm25(OLD.knowledge_object_id);
    ELSE
        PERFORM knowledge.refresh_notes_bm25(NEW.knowledge_object_id);
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_bm25_object_change AFTER UPDATE OF notes_source_root_id,project_id,source_node_key,
 relative_path,title,file_class,source_revision,source_hash,lexical_version_id,deleted_at,recency_at
 ON knowledge.knowledge_objects FOR EACH ROW
 WHEN ((OLD.notes_source_root_id,OLD.project_id,OLD.source_node_key,OLD.relative_path,OLD.title,OLD.file_class,
        OLD.source_revision,OLD.source_hash,OLD.lexical_version_id,OLD.deleted_at,OLD.recency_at)
 IS DISTINCT FROM
       (NEW.notes_source_root_id,NEW.project_id,NEW.source_node_key,NEW.relative_path,NEW.title,NEW.file_class,
        NEW.source_revision,NEW.source_hash,NEW.lexical_version_id,NEW.deleted_at,NEW.recency_at))
 EXECUTE FUNCTION knowledge.sync_notes_bm25_object();
CREATE TRIGGER notes_bm25_object_delete AFTER DELETE ON knowledge.knowledge_objects
FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_object();
CREATE TRIGGER notes_bm25_custody_change AFTER INSERT OR UPDATE OR DELETE ON knowledge.notes_current_custody
FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_object();
CREATE TRIGGER notes_bm25_chunk_change AFTER UPDATE OF status,chunk_text,structural_path,chunk_hash,knowledge_object_version_id
 ON knowledge.knowledge_chunks FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_object();
CREATE TRIGGER notes_bm25_chunk_delete AFTER DELETE ON knowledge.knowledge_chunks
FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_object();
CREATE TRIGGER notes_bm25_run_change AFTER INSERT OR DELETE ON knowledge.pipeline_runs
FOR EACH ROW EXECUTE FUNCTION knowledge.sync_notes_bm25_object();

-- +goose StatementBegin
CREATE FUNCTION knowledge.sync_notes_bm25_root() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    UPDATE knowledge.notes_bm25_documents SET root_kind=NEW.root_kind WHERE root_id=NEW.notes_source_root_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_bm25_root_change AFTER UPDATE OF root_kind ON knowledge.notes_source_roots
FOR EACH ROW WHEN (OLD.root_kind IS DISTINCT FROM NEW.root_kind) EXECUTE FUNCTION knowledge.sync_notes_bm25_root();
ANALYZE knowledge.notes_bm25_documents;

-- +goose Down
DROP TRIGGER notes_bm25_root_change ON knowledge.notes_source_roots;
DROP TRIGGER notes_bm25_run_change ON knowledge.pipeline_runs;
DROP TRIGGER notes_bm25_chunk_change ON knowledge.knowledge_chunks;
DROP TRIGGER notes_bm25_chunk_delete ON knowledge.knowledge_chunks;
DROP TRIGGER notes_bm25_custody_change ON knowledge.notes_current_custody;
DROP TRIGGER notes_bm25_object_change ON knowledge.knowledge_objects;
DROP TRIGGER notes_bm25_object_delete ON knowledge.knowledge_objects;
DROP TRIGGER notes_bm25_document_change ON search.search_documents;
DROP FUNCTION knowledge.sync_notes_bm25_root();
DROP FUNCTION knowledge.sync_notes_bm25_object();
DROP FUNCTION knowledge.sync_notes_bm25_document();
DROP FUNCTION knowledge.refresh_notes_bm25(text,text);
DROP TABLE knowledge.notes_bm25_documents;
DROP VIEW knowledge.notes_bm25_source;
