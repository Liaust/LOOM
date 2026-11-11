-- +goose Up
ALTER TABLE knowledge.notes_source_roots ADD COLUMN workspace_authority jsonb;
ALTER TABLE watched_roots.roots ADD COLUMN observation_generations jsonb NOT NULL DEFAULT '{}';
UPDATE watched_roots.roots SET observation_generations=jsonb_build_object(config_hash,true) WHERE config_hash<>'';

-- +goose StatementBegin
CREATE FUNCTION watched_roots.content_config(config jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT config-'display_name' #- '{watch,debounce_window}'
 #- '{scan,full_rescan_interval}' #- '{scan,max_runtime}'
 #- '{scan,max_files_per_run}' #- '{scan,max_dirs_per_run}'
 #- '{scan,max_hash_bytes_per_run}' #- '{scan,stability_window}';
$$;
-- +goose StatementEnd

-- Retain the exact old generation inputs. This is not an access grant; current
-- owner acknowledgement, custody and path checks still precede every use.
WITH owners AS (
 SELECT r.notes_source_root_id,w.config_hash,w.config_json,w.metadata
 FROM knowledge.notes_source_roots r JOIN box.watch_root_registrations w
 ON w.box_watch_root_registration_id=r.box_watch_root_registration_id
 UNION ALL
 SELECT r.notes_source_root_id,w.config_hash,w.config_json,w.metadata
 FROM knowledge.notes_source_roots r JOIN projects.project_watched_root_registrations w
 ON w.project_watched_root_registration_id=r.project_watched_root_registration_id
)
UPDATE knowledge.notes_source_roots r SET workspace_authority=jsonb_build_object(
 'root',jsonb_build_object('id',r.notes_source_root_id,'kind',r.root_kind,
 'node',r.node_key,'node_id',r.node_id,'project',r.project_id,'path',r.source_path,
 'relative',r.root_relative_path,'backend',r.backend_root_key),
 'config_hash',w.config_hash,'config',w.config_json,'metadata',r.metadata,
 'authorization',r.authorization_metadata)
FROM owners w WHERE r.notes_source_root_id=w.notes_source_root_id
 AND r.metadata->'registration_metadata'=w.metadata;

-- Exact payload-free change hints use the existing listener. No-op writes and
-- bookkeeping cursors/observations must not invalidate held dependency graphs.
-- +goose StatementBegin
CREATE FUNCTION notes_workspace.notify_dependency_change() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE payload jsonb;
BEGIN
 IF TG_OP='UPDATE' AND OLD.record IS NOT DISTINCT FROM NEW.record THEN
  RETURN NEW;
 END IF;
 IF TG_TABLE_NAME='files' THEN
  IF TG_OP='DELETE' THEN
   payload=jsonb_build_object('kind','source','id',OLD.file_id);
  ELSE
   payload=jsonb_build_object('kind','source','id',NEW.file_id);
  END IF;
 ELSE
  IF TG_OP='DELETE' THEN
   IF OLD.kind NOT IN ('file','source_file','binding','export') THEN RETURN OLD; END IF;
   payload=jsonb_build_object('replica',OLD.replica_id,'kind',OLD.kind,'id',OLD.record_id);
  ELSE
   IF NEW.kind NOT IN ('file','source_file','binding','export') THEN RETURN NEW; END IF;
   payload=jsonb_build_object('replica',NEW.replica_id,'kind',NEW.kind,'id',NEW.record_id);
  END IF;
 END IF;
 PERFORM pg_notify('loom_notes_dependency',payload::text);
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER notes_source_dependency_changed AFTER INSERT OR UPDATE OR DELETE
 ON notes_workspace.files FOR EACH ROW EXECUTE FUNCTION notes_workspace.notify_dependency_change();
CREATE TRIGGER notes_sync_dependency_changed AFTER INSERT OR UPDATE OR DELETE
 ON notes_workspace.sync_records FOR EACH ROW EXECUTE FUNCTION notes_workspace.notify_dependency_change();

-- +goose Down
DROP TRIGGER notes_sync_dependency_changed ON notes_workspace.sync_records;
DROP TRIGGER notes_source_dependency_changed ON notes_workspace.files;
DROP FUNCTION notes_workspace.notify_dependency_change();
-- Keep the additive config function/observation receipts for compatible older
-- code and retained outbox items, just as generation anchors are retained.
-- Retain generation anchors: older code ignores them; deleting them would make
-- a later upgrade invalidate otherwise unchanged pending user bindings.
