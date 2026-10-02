-- +goose Up
-- Reuse publication-owned documents; do not create a second corpus or ACL cache.
ALTER TABLE search.search_documents
    ADD COLUMN notes_object_id text GENERATED ALWAYS AS
        (CASE WHEN source_kind IN ('knowledge_chunk','knowledge_object_metadata')
         THEN metadata->>'knowledge_object_id' END) STORED,
    ADD COLUMN notes_fields text GENERATED ALWAYS AS
        (lower(COALESCE(title,'') || ' ' || COALESCE(summary,'') || ' ' ||
               COALESCE(metadata->>'relative_path','') || ' ' ||
               COALESCE(metadata->>'source_path',''))) STORED;

-- Repair old navigation snapshots once; subsequent changes use the trigger.
UPDATE search.search_documents sd SET
    title=COALESCE(NULLIF(ko.title,''),ko.relative_path),
    metadata=sd.metadata || jsonb_build_object('relative_path',ko.relative_path,'source_path',ko.source_path),
    tsv=setweight(to_tsvector('simple',COALESCE(NULLIF(ko.title,''),ko.relative_path)), 'A') ||
        setweight(to_tsvector('simple',COALESCE(sd.summary,'')), 'B') ||
        setweight(to_tsvector('simple',sd.body), 'C')
FROM knowledge.knowledge_objects ko
WHERE sd.notes_object_id=ko.knowledge_object_id
  AND sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata')
  AND sd.index_version='knowledge_bm25_v1'
  AND (sd.title IS DISTINCT FROM COALESCE(NULLIF(ko.title,''),ko.relative_path)
       OR sd.metadata->>'relative_path' IS DISTINCT FROM ko.relative_path
       OR sd.metadata->>'source_path' IS DISTINCT FROM ko.source_path);

CREATE INDEX search_notes_object_idx ON search.search_documents (notes_object_id)
WHERE source_kind IN ('knowledge_chunk','knowledge_object_metadata')
  AND index_version='knowledge_bm25_v1';
CREATE INDEX search_notes_fields_idx ON search.search_documents USING GIN (notes_fields gin_trgm_ops)
WHERE source_kind IN ('knowledge_chunk','knowledge_object_metadata')
  AND index_version='knowledge_bm25_v1';

-- Keep indexed navigation fields in the same transaction as object navigation.
-- +goose StatementBegin
CREATE FUNCTION knowledge.update_notes_search_navigation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE search.search_documents SET
        title=COALESCE(NULLIF(NEW.title,''),NEW.relative_path),
        metadata=metadata || jsonb_build_object('relative_path',NEW.relative_path,'source_path',NEW.source_path),
        tsv=setweight(to_tsvector('simple',COALESCE(NULLIF(NEW.title,''),NEW.relative_path)), 'A') ||
            setweight(to_tsvector('simple',COALESCE(summary,'')), 'B') ||
            setweight(to_tsvector('simple',body), 'C')
    WHERE notes_object_id=NEW.knowledge_object_id
      AND source_kind IN ('knowledge_chunk','knowledge_object_metadata')
      AND index_version='knowledge_bm25_v1';
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_search_navigation_update
AFTER UPDATE OF title,relative_path,source_path ON knowledge.knowledge_objects
FOR EACH ROW WHEN (OLD.title IS DISTINCT FROM NEW.title OR OLD.relative_path IS DISTINCT FROM NEW.relative_path
                  OR OLD.source_path IS DISTINCT FROM NEW.source_path)
EXECUTE FUNCTION knowledge.update_notes_search_navigation();

ANALYZE search.search_documents;

-- +goose Down
DROP TRIGGER notes_search_navigation_update ON knowledge.knowledge_objects;
DROP FUNCTION knowledge.update_notes_search_navigation();
DROP INDEX search.search_notes_fields_idx;
DROP INDEX search.search_notes_object_idx;
ALTER TABLE search.search_documents DROP COLUMN notes_fields, DROP COLUMN notes_object_id;
