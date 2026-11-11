-- +goose Up
CREATE TABLE knowledge.pending_catalog_admissions (
    storage_entry_id text PRIMARY KEY REFERENCES storage.storage_entries(storage_entry_id) ON DELETE CASCADE,
    revision bigint NOT NULL DEFAULT nextval('knowledge.source_admission_revision'),
    received_at timestamptz NOT NULL DEFAULT now(),
    next_attempt_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX pending_catalog_admissions_ready_idx
    ON knowledge.pending_catalog_admissions(next_attempt_at, received_at, storage_entry_id);

-- +goose StatementBegin
CREATE FUNCTION knowledge.queue_catalog_admission() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.source_area NOT IN ('notes','projects','external_watched_root') THEN
        RETURN NEW;
    END IF;
    -- Observation timestamps and retention bookkeeping do not change admission.
    IF TG_OP = 'UPDATE' AND
       (to_jsonb(NEW) - ARRAY['updated_at','last_seen_at','retention_state','retention_metadata']) =
       (to_jsonb(OLD) - ARRAY['updated_at','last_seen_at','retention_state','retention_metadata']) THEN
        RETURN NEW;
    END IF;
    INSERT INTO knowledge.pending_catalog_admissions(storage_entry_id) VALUES(NEW.storage_entry_id)
    ON CONFLICT(storage_entry_id) DO UPDATE SET
        revision=nextval('knowledge.source_admission_revision'),
        received_at=clock_timestamp(), next_attempt_at=clock_timestamp();
    PERFORM pg_notify('loom_worker_wake','knowledge_indexer');
    RETURN NEW;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_catalog_admission_changed
AFTER INSERT OR UPDATE ON storage.storage_entries
FOR EACH ROW EXECUTE FUNCTION knowledge.queue_catalog_admission();

-- +goose Down
DROP TRIGGER notes_catalog_admission_changed ON storage.storage_entries;
DROP FUNCTION knowledge.queue_catalog_admission();
DROP TABLE knowledge.pending_catalog_admissions;
