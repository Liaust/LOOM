-- +goose Up
-- Keep the schema-112 V1/V2 expressions intact. Only explicit V3 project
-- contracts may bind an owner-accepted active location rather than a slug path.

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v3_active(ref jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_typeof(ref)='object'
  AND octet_length(ref->>'relative_path') BETWEEN 10 AND 4096
  AND ref->>'relative_path' LIKE 'Projects/%'
  AND ref->>'relative_path' !~ '(^|/)[.]{1,2}(/|$)|//|/$'
  AND strpos(ref->>'relative_path', chr(92))=0
  AND storage.workspace_archive_v2_ref(ref,'box',ref->>'relative_path'),false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v3_location(l jsonb, project text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_typeof(l)='object'
  AND project ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
  AND l->>'project_id'=project
  AND l->>'location_id' ~ '^[a-z][A-Za-z0-9_]{2,159}$'
  AND CASE WHEN jsonb_typeof(l->'registration_revision')='number'
   AND l->>'registration_revision' ~ '^[0-9]+$'
   THEN (l->>'registration_revision')::numeric BETWEEN 1 AND 9223372036854775807 ELSE false END
  AND CASE WHEN jsonb_typeof(l->'repository_source_revision')='number'
   AND l->>'repository_source_revision' ~ '^[0-9]+$'
   THEN (l->>'repository_source_revision')::numeric BETWEEN 0 AND 9223372036854775807 ELSE false END
  AND storage.workspace_archive_v3_active(l->'path'),false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v3_plan(p jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE op text := p->>'operation_kind'; slug text := p->>'slug';
 archive_id text := p->>'archive_operation_id'; metadata jsonb := p#>'{placement,metadata}';
 metadata_path text := metadata#>>'{path,relative_path}'; payload jsonb; active jsonb;
 device text := p#>>'{source,identity,device_id}'; binding jsonb; chain jsonb;
 seen jsonb := '{}'::jsonb; ancestor jsonb; key text;
BEGIN
 IF p->>'schema_version' IS DISTINCT FROM 'storage.workspace_archive_plan.v3'
  OR p->>'evidence_kind' IS DISTINCT FROM 'physical_workspace_move'
  OR p->>'kind' IS DISTINCT FROM 'project'
  OR NOT COALESCE(slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$',false)
  OR NOT COALESCE(p->>'operation_id' ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$',false)
  OR NOT storage.workspace_archive_v3_location(p->'active_location',p->>'object_id')
  OR jsonb_typeof(p->'placement') IS DISTINCT FROM 'object'
  OR COALESCE(p#>>'{placement,relocation_id}','')<>'' THEN RETURN false; END IF;
 IF op='archive' THEN
  active := p#>'{source,path}'; payload := p#>'{destination,path}';
  IF metadata_path IS DISTINCT FROM '.loom/workspace-archives/'||(p->>'operation_id')
   OR COALESCE(archive_id,'')<>'' OR COALESCE(p->>'archive_manifest_digest','')<>''
   OR NOT storage.workspace_archive_v2_identity(metadata->'identity','absent',device)
   THEN RETURN false; END IF;
 ELSIF op='restore' THEN
  active := p#>'{destination,path}'; payload := p#>'{source,path}';
  IF NOT COALESCE(archive_id ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
    AND archive_id<>p->>'operation_id' AND p->>'archive_manifest_digest' ~ '^sha256:[0-9a-f]{64}$',false)
   OR p ? 'previous_cycle'
   OR metadata_path IS DISTINCT FROM '.loom/workspace-archives/'||archive_id
   OR NOT storage.workspace_archive_v2_identity(metadata->'identity','present',device)
   THEN RETURN false; END IF;
 ELSE RETURN false; END IF;
 IF active IS DISTINCT FROM p#>'{active_location,path}'
  OR NOT storage.workspace_archive_v2_ref(payload,'storage','Projects/'||slug)
  OR NOT storage.workspace_archive_v2_ref(metadata->'path','storage',metadata_path)
  OR left(payload->>'absolute_path',length(payload->>'absolute_path')-length(payload->>'relative_path'))
   IS DISTINCT FROM left(metadata#>>'{path,absolute_path}',length(metadata#>>'{path,absolute_path}')-length(metadata_path))
  OR NOT storage.workspace_archive_v2_identity(p#>'{source,identity}','present',device)
  OR NOT storage.workspace_archive_v2_identity(p#>'{destination,identity}','absent',device)
  OR p#>>'{inventory,schema_version}' IS DISTINCT FROM 'storage.workspace_archive_inventory.v1'
  OR p#>'{inventory,root_identity}' IS DISTINCT FROM p#>'{source,identity}'
  THEN RETURN false; END IF;
 FOR binding, chain IN SELECT p->'source',p->'source_ancestors'
  UNION ALL SELECT p->'destination',p->'destination_ancestors'
  UNION ALL SELECT metadata,p#>'{placement,metadata_ancestors}' LOOP
  IF NOT storage.workspace_archive_v2_identity(binding->'parent_identity','present',device)
   OR NOT storage.workspace_archive_v2_chain(chain,binding#>>'{path,relative_path}',binding->'parent_identity',device)
   THEN RETURN false; END IF;
  FOR ancestor IN SELECT value FROM jsonb_array_elements(chain)
   UNION ALL SELECT jsonb_build_object('relative_path',binding#>>'{path,relative_path}','identity',binding->'identity')
    WHERE binding#>>'{identity,presence}'='present' LOOP
   key := binding#>>'{path,root}'||':'||(ancestor->>'relative_path');
   IF seen ? key AND seen->key IS DISTINCT FROM ancestor->'identity' THEN RETURN false; END IF;
   seen := jsonb_set(seen,ARRAY[key],ancestor->'identity');
  END LOOP;
 END LOOP;
 RETURN true;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v3_manifest(m jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(m->>'schema_version'='storage.workspace_archive_manifest.v3'
  AND m->>'kind'='project' AND m->>'object_id' ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
  AND storage.workspace_archive_v3_active(m->'active_path')
  AND storage.workspace_archive_v2_ref(m->'archive_payload_path','storage','Projects/'||(m->>'slug'))
  AND (NOT (m ? 'restored_from_path') OR (m->>'lifecycle_state'='active'
   AND m->'restored_from_path'=m->'archive_payload_path'))
  AND (m->>'restore_operation_id' IS NULL OR m->>'restore_operation_id'<>m->>'archive_operation_id'),false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION projects.physical_archive_v3_plan(p jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE w jsonb := p->'workspace'; l jsonb := p->'active_location';
 c jsonb := p->'custody'; r jsonb := p#>'{registration,registration}';
 s jsonb := p#>'{repository,source}'; archive jsonb; source_revision numeric;
BEGIN
 IF NOT storage.workspace_archive_v3_plan(w)
  OR p->>'schema_version' IS DISTINCT FROM 'storage.project_physical_'||(w->>'operation_kind')||'_plan.v3'
  OR l IS DISTINCT FROM w->'active_location' THEN RETURN false; END IF;
 IF w->>'operation_kind'='restore' THEN
  archive := p->'archive_plan'; c := archive->'custody';
  IF p->>'activation_state' IS DISTINCT FROM 'inactive'
   OR archive#>>'{workspace,operation_kind}' IS DISTINCT FROM 'archive'
   OR NOT projects.physical_archive_v3_plan(archive)
   OR l IS DISTINCT FROM archive->'active_location'
   OR w->>'archive_operation_id' IS DISTINCT FROM archive#>>'{workspace,operation_id}'
   OR w#>'{destination,path}' IS DISTINCT FROM archive#>'{workspace,source,path}'
   OR w#>'{source,path}' IS DISTINCT FROM archive#>'{workspace,destination,path}'
   OR w->'inventory' IS DISTINCT FROM archive#>'{workspace,inventory}'
   THEN RETURN false; END IF;
 ELSIF w->>'operation_kind'<>'archive' THEN RETURN false; END IF;
 IF l->>'project_id' IS DISTINCT FROM c->>'project_id'
  OR l->>'project_id' IS DISTINCT FROM p#>>'{registration,project,project,project_id}'
  OR l->>'project_id' IS DISTINCT FROM p#>>'{repository,project,project_id}'
  OR l->>'project_id' IS DISTINCT FROM r->>'project_id'
  OR r->>'project_contract_registration_id' IS DISTINCT FROM c->>'registration_id'
  OR l#>>'{path,absolute_path}' IS DISTINCT FROM c->>'canonical_root'
  OR l#>>'{path,absolute_path}' IS DISTINCT FROM c->>'registered_root'
  OR l#>>'{path,absolute_path}' IS DISTINCT FROM r->>'project_root'
  OR l->'registration_revision' IS DISTINCT FROM r->'registration_revision'
  OR l->'registration_revision' IS DISTINCT FROM c->'registration_revision'
  THEN RETURN false; END IF;
 source_revision := (l->>'repository_source_revision')::numeric;
 IF source_revision IS DISTINCT FROM COALESCE((c->>'repository_source_revision')::numeric,0) THEN RETURN false; END IF;
 IF s IS NULL OR s='null'::jsonb THEN
  IF source_revision<>0 THEN RETURN false; END IF;
 ELSE
  IF jsonb_typeof(s) IS DISTINCT FROM 'object'
   OR source_revision IS DISTINCT FROM (s->>'source_revision')::numeric
   OR s->>'project_contract_registration_id' IS DISTINCT FROM c->>'registration_id'
   OR l#>>'{path,absolute_path}' IS DISTINCT FROM c->>'repository_project_root'
   THEN RETURN false; END IF;
 END IF;
 RETURN true;
END $$;
-- +goose StatementEnd

-- Select the exact validated schema-112 check, including the dependency columns
-- added by 110. Never guess PostgreSQL's anonymous constraint numbering.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.extend_workspace_active_check(target regclass, columns text[], marker text,
 legacy_gate text, v3_gate text, v3_expression text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE names text[]; expressions text[];
BEGIN
 SELECT array_agg(c.conname::text),array_agg(pg_get_expr(c.conbin,c.conrelid)) INTO names,expressions
 FROM pg_constraint c WHERE c.conrelid=target AND c.contype='c' AND c.convalidated
 AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) k
  JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k ORDER BY a.attname::text COLLATE "C")
  = ARRAY(SELECT col FROM unnest(columns) col ORDER BY col COLLATE "C")
 AND strpos(pg_get_expr(c.conbin,c.conrelid),marker)>0;
 IF cardinality(names) IS DISTINCT FROM 1 THEN
  RAISE EXCEPTION 'expected exactly one schema-112 check on % for % containing %, found %',target,columns,marker,cardinality(names);
 END IF;
 EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I, ADD CONSTRAINT %I CHECK ((((%s) AND (%s)) OR ((%s) AND (%s))) IS TRUE)',
  target,names[1],names[1],legacy_gate,expressions[1],v3_gate,v3_expression);
END $$;
-- +goose StatementEnd

SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_operations',ARRAY['schema_version','workspace_kind'],
 'storage.workspace_archive_operation.v2', $$schema_version IN ('storage.workspace_archive_operation.v1','storage.workspace_archive_operation.v2')$$,
 $$schema_version='storage.workspace_archive_operation.v3' AND workspace_kind='project'$$,'true');
SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_operations',ARRAY[
 'schema_version','plan_json','workspace_archive_operation_id','operation_kind','workspace_kind','object_id','slug','plan_digest',
 'source_root','source_relative_path','source_identity_json','source_parent_identity_json',
 'destination_root','destination_relative_path','destination_identity_json','destination_parent_identity_json',
 'inventory_digest','actor_id','reason','planned_at'],
 'workspace_archive_v2_plan', $$schema_version IN ('storage.workspace_archive_operation.v1','storage.workspace_archive_operation.v2')$$,
 $$schema_version='storage.workspace_archive_operation.v3' AND workspace_kind='project'$$,
 $$storage.workspace_archive_v3_plan(plan_json)
 AND plan_json->>'operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id
 AND plan_json->>'operation_kind' IS NOT DISTINCT FROM operation_kind
 AND plan_json->>'kind' IS NOT DISTINCT FROM workspace_kind
 AND plan_json->>'object_id' IS NOT DISTINCT FROM object_id AND plan_json->>'slug' IS NOT DISTINCT FROM slug
 AND plan_json->>'plan_digest' IS NOT DISTINCT FROM plan_digest
 AND plan_json#>>'{source,path,root}' IS NOT DISTINCT FROM source_root
 AND plan_json#>>'{source,path,relative_path}' IS NOT DISTINCT FROM source_relative_path
 AND plan_json#>'{source,identity}' IS NOT DISTINCT FROM source_identity_json
 AND plan_json#>'{source,parent_identity}' IS NOT DISTINCT FROM source_parent_identity_json
 AND plan_json#>>'{destination,path,root}' IS NOT DISTINCT FROM destination_root
 AND plan_json#>>'{destination,path,relative_path}' IS NOT DISTINCT FROM destination_relative_path
 AND plan_json#>'{destination,identity}' IS NOT DISTINCT FROM destination_identity_json
 AND plan_json#>'{destination,parent_identity}' IS NOT DISTINCT FROM destination_parent_identity_json
 AND plan_json#>>'{inventory,digest}' IS NOT DISTINCT FROM inventory_digest
 AND plan_json->>'actor_id' IS NOT DISTINCT FROM actor_id AND plan_json->>'reason' IS NOT DISTINCT FROM reason
 AND (plan_json->>'planned_at')::timestamptz IS NOT DISTINCT FROM planned_at
 AND octet_length(plan_json::text)<=268435456$$);
SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_operations',
 ARRAY['schema_version','source_relative_path','operation_kind','workspace_kind','slug'],'archive/projects/',
 $$schema_version IN ('storage.workspace_archive_operation.v1','storage.workspace_archive_operation.v2')$$,
 $$schema_version='storage.workspace_archive_operation.v3' AND workspace_kind='project'$$,
 $$source_relative_path=CASE WHEN operation_kind='archive' THEN plan_json#>>'{active_location,path,relative_path}' ELSE 'Projects/'||slug END$$);
SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_operations',
 ARRAY['schema_version','destination_relative_path','operation_kind','workspace_kind','slug'],'archive/projects/',
 $$schema_version IN ('storage.workspace_archive_operation.v1','storage.workspace_archive_operation.v2')$$,
 $$schema_version='storage.workspace_archive_operation.v3' AND workspace_kind='project'$$,
 $$destination_relative_path=CASE WHEN operation_kind='restore' THEN plan_json#>>'{active_location,path,relative_path}' ELSE 'Projects/'||slug END$$);

SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_manifests',ARRAY['schema_version','workspace_kind'],
 'storage.workspace_archive_manifest.v2', $$schema_version IN ('storage.workspace_archive_manifest.v1','storage.workspace_archive_manifest.v2')$$,
 $$schema_version='storage.workspace_archive_manifest.v3' AND workspace_kind='project'$$,'true');
SELECT pg_temp.extend_workspace_active_check('storage.workspace_archive_manifests',ARRAY[
 'schema_version','manifest_json','workspace_archive_operation_id','workspace_kind','object_id','slug','lifecycle_state',
 'plan_digest','restore_plan_digest','inventory_digest','archive_source_identity_json','authentication_key_id',
 'authentication_tag','archived_at','restore_operation_id','restored_at'],
 'storage.workspace_archive_manifest.v2', $$schema_version IN ('storage.workspace_archive_manifest.v1','storage.workspace_archive_manifest.v2')$$,
 $$schema_version='storage.workspace_archive_manifest.v3' AND workspace_kind='project'$$,
 $$storage.workspace_archive_v3_manifest(manifest_json)
 AND manifest_json->>'evidence_kind' IS NOT DISTINCT FROM 'physical_workspace_move'
 AND manifest_json->>'archive_operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id
 AND manifest_json->>'kind' IS NOT DISTINCT FROM workspace_kind
 AND manifest_json->>'object_id' IS NOT DISTINCT FROM object_id AND manifest_json->>'slug' IS NOT DISTINCT FROM slug
 AND manifest_json->>'lifecycle_state' IS NOT DISTINCT FROM lifecycle_state
 AND manifest_json->>'plan_digest' IS NOT DISTINCT FROM plan_digest
 AND manifest_json->>'restore_plan_digest' IS NOT DISTINCT FROM restore_plan_digest
 AND manifest_json->>'inventory_digest' IS NOT DISTINCT FROM inventory_digest
 AND manifest_json->'archive_source_identity' IS NOT DISTINCT FROM archive_source_identity_json
 AND manifest_json#>>'{authentication,algorithm}' IS NOT DISTINCT FROM 'hmac-sha256'
 AND manifest_json#>>'{authentication,key_id}' IS NOT DISTINCT FROM authentication_key_id
 AND manifest_json#>>'{authentication,tag}' IS NOT DISTINCT FROM authentication_tag
 AND (manifest_json->>'archived_at')::timestamptz IS NOT DISTINCT FROM archived_at
 AND manifest_json->>'restore_operation_id' IS NOT DISTINCT FROM restore_operation_id
 AND (manifest_json->>'restored_at')::timestamptz IS NOT DISTINCT FROM restored_at
 AND octet_length(manifest_json::text)<=1048576$$);

SELECT pg_temp.extend_workspace_active_check('storage.workspace_lifecycle_events',ARRAY[
 'schema_version','details','workspace_lifecycle_event_id','event_kind','workspace_archive_operation_id','workspace_kind',
 'object_id','slug','transition','from_state','to_state','actor_id','reason','occurred_at',
 'source_root','source_relative_path','destination_root','destination_relative_path'],
 'storage.workspace_lifecycle_event.v2', $$schema_version IN ('storage.workspace_lifecycle_event.v1','storage.workspace_lifecycle_event.v2')$$,
 $$schema_version='storage.workspace_lifecycle_event.v3' AND workspace_kind='project'$$,
 $$details->>'schema_version' IS NOT DISTINCT FROM schema_version
 AND details->>'event_id' IS NOT DISTINCT FROM workspace_lifecycle_event_id
 AND details->>'event_kind' IS NOT DISTINCT FROM event_kind
 AND details->>'operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id
 AND details->>'kind' IS NOT DISTINCT FROM workspace_kind AND details->>'object_id' IS NOT DISTINCT FROM object_id
 AND details->>'slug' IS NOT DISTINCT FROM slug AND details->>'transition' IS NOT DISTINCT FROM transition
 AND details->>'from_state' IS NOT DISTINCT FROM from_state AND details->>'to_state' IS NOT DISTINCT FROM to_state
 AND details->>'actor_id' IS NOT DISTINCT FROM actor_id AND details->>'reason' IS NOT DISTINCT FROM reason
 AND (details->>'occurred_at')::timestamptz IS NOT DISTINCT FROM occurred_at
 AND storage.workspace_archive_v2_ref(details->'source_path',source_root,source_relative_path)
 AND storage.workspace_archive_v2_ref(details->'destination_path',destination_root,destination_relative_path)$$);
SELECT pg_temp.extend_workspace_active_check('storage.workspace_lifecycle_events',
 ARRAY['schema_version','source_relative_path','transition','workspace_kind','slug'],'archive/projects/',
 $$schema_version IN ('storage.workspace_lifecycle_event.v1','storage.workspace_lifecycle_event.v2')$$,
 $$schema_version='storage.workspace_lifecycle_event.v3' AND workspace_kind='project'$$,
 $$CASE WHEN transition='active_to_archived' THEN storage.workspace_archive_v3_active(details->'source_path') ELSE source_relative_path='Projects/'||slug END$$);
SELECT pg_temp.extend_workspace_active_check('storage.workspace_lifecycle_events',
 ARRAY['schema_version','destination_relative_path','transition','workspace_kind','slug'],'archive/projects/',
 $$schema_version IN ('storage.workspace_lifecycle_event.v1','storage.workspace_lifecycle_event.v2')$$,
 $$schema_version='storage.workspace_lifecycle_event.v3' AND workspace_kind='project'$$,
 $$CASE WHEN transition='archived_to_active' THEN storage.workspace_archive_v3_active(details->'destination_path') ELSE destination_relative_path='Projects/'||slug END$$);

SELECT pg_temp.extend_workspace_active_check('projects.physical_archive_plan_evidence',
 ARRAY['payload','plan_digest','request_json','operation_id','operation_kind','project_id'],'storage.project_physical_',
 $$convert_from(payload,'UTF8')::jsonb->>'schema_version' IN ('storage.project_physical_'||operation_kind||'_plan.v1','storage.project_physical_'||operation_kind||'_plan.v2')$$,
 $$convert_from(payload,'UTF8')::jsonb->>'schema_version'='storage.project_physical_'||operation_kind||'_plan.v3'$$,
 $$projects.physical_archive_v3_plan(convert_from(payload,'UTF8')::jsonb)
 AND convert_from(payload,'UTF8')::jsonb->>'plan_digest' IS NOT DISTINCT FROM plan_digest
 AND convert_from(payload,'UTF8')::jsonb->'request' IS NOT DISTINCT FROM request_json
 AND convert_from(payload,'UTF8')::jsonb#>>'{workspace,operation_id}' IS NOT DISTINCT FROM operation_id
 AND convert_from(payload,'UTF8')::jsonb#>>'{workspace,operation_kind}' IS NOT DISTINCT FROM operation_kind
 AND convert_from(payload,'UTF8')::jsonb#>>'{workspace,object_id}' IS NOT DISTINCT FROM project_id$$);

DROP FUNCTION pg_temp.extend_workspace_active_check(regclass,text[],text,text,text,text);

-- Recompute only this generated version tag. V1/V2 outputs are identical; no
-- signed JSON, physical-plan bytes, timestamps or event records are rewritten.
ALTER TABLE storage.workspace_archive_manifests
 DROP CONSTRAINT workspace_archive_manifests_archive_version_fkey,
 DROP COLUMN archive_schema_version,
 ADD COLUMN archive_schema_version text GENERATED ALWAYS AS (CASE
  WHEN schema_version='storage.workspace_archive_manifest.v3' THEN 'storage.workspace_archive_operation.v3'
  WHEN schema_version='storage.workspace_archive_manifest.v1' OR manifest_json#>>'{archive_payload_path,relative_path}'='archive/projects/'||slug||'/project'
  THEN 'storage.workspace_archive_operation.v1' ELSE 'storage.workspace_archive_operation.v2' END) STORED,
 ADD CONSTRAINT workspace_archive_manifests_archive_version_fkey
  FOREIGN KEY(workspace_archive_operation_id,archive_schema_version)
  REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id,schema_version);
-- Existing replace()-generated restore/event version FKs already map V3 exactly.

-- +goose StatementBegin
CREATE FUNCTION storage.guard_workspace_archive_v3_operation()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE archived jsonb;
BEGIN
 IF TG_OP='UPDATE' AND (OLD.schema_version='storage.workspace_archive_operation.v3' OR NEW.schema_version='storage.workspace_archive_operation.v3') THEN
  IF OLD.schema_version IS DISTINCT FROM NEW.schema_version OR OLD.plan_json IS DISTINCT FROM NEW.plan_json
   OR OLD.plan_digest IS DISTINCT FROM NEW.plan_digest THEN
   RAISE EXCEPTION 'V3 operation plan is immutable' USING ERRCODE='23514';
  END IF;
 END IF;
 IF NEW.schema_version<>'storage.workspace_archive_operation.v3' OR NEW.operation_kind<>'restore' THEN RETURN NEW; END IF;
 SELECT plan_json INTO archived FROM storage.workspace_archive_operations
  WHERE workspace_archive_operation_id=NEW.plan_json->>'archive_operation_id'
   AND schema_version='storage.workspace_archive_operation.v3' AND operation_kind='archive'
   AND phase='archive_complete' AND terminal_status='complete';
 IF archived IS NULL OR archived->>'object_id' IS DISTINCT FROM NEW.object_id OR archived->>'slug' IS DISTINCT FROM NEW.slug
  OR NEW.plan_json->'active_location' IS DISTINCT FROM archived->'active_location'
  OR NEW.plan_json#>'{destination,path}' IS DISTINCT FROM archived#>'{source,path}'
  OR NEW.plan_json#>'{source,path}' IS DISTINCT FROM archived#>'{destination,path}'
  OR NEW.plan_json->'inventory' IS DISTINCT FROM archived->'inventory' THEN
  RAISE EXCEPTION 'V3 restore contradicts frozen archive location or inventory' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workspace_archive_v3_operation
 BEFORE INSERT OR UPDATE ON storage.workspace_archive_operations
 FOR EACH ROW EXECUTE FUNCTION storage.guard_workspace_archive_v3_operation();

-- +goose StatementBegin
CREATE FUNCTION storage.guard_workspace_archive_v3_manifest_paths()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE archived jsonb; restored jsonb;
BEGIN
 IF NEW.schema_version<>'storage.workspace_archive_manifest.v3' THEN RETURN NEW; END IF;
 SELECT plan_json INTO archived FROM storage.workspace_archive_operations
  WHERE workspace_archive_operation_id=NEW.workspace_archive_operation_id AND schema_version='storage.workspace_archive_operation.v3';
 IF archived IS NULL OR NEW.manifest_json->'active_path' IS DISTINCT FROM archived#>'{source,path}'
  OR NEW.manifest_json->'archive_payload_path' IS DISTINCT FROM archived#>'{destination,path}'
  OR NEW.manifest_json->'archive_source_identity' IS DISTINCT FROM archived#>'{source,identity}' THEN
  RAISE EXCEPTION 'V3 manifest contradicts frozen archive paths' USING ERRCODE='23514';
 END IF;
 IF NEW.lifecycle_state='active' THEN
  SELECT plan_json INTO restored FROM storage.workspace_archive_operations
   WHERE workspace_archive_operation_id=NEW.restore_operation_id AND schema_version='storage.workspace_archive_operation.v3';
  IF restored IS NULL OR restored->>'archive_operation_id' IS DISTINCT FROM NEW.workspace_archive_operation_id
   OR restored->'active_location' IS DISTINCT FROM archived->'active_location'
   OR NEW.manifest_json->'active_path' IS DISTINCT FROM restored#>'{destination,path}'
   OR COALESCE(NEW.manifest_json->'restored_from_path',NEW.manifest_json->'archive_payload_path') IS DISTINCT FROM restored#>'{source,path}' THEN
   RAISE EXCEPTION 'V3 manifest contradicts frozen restore paths' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workspace_archive_v3_manifest_paths
 BEFORE INSERT OR UPDATE ON storage.workspace_archive_manifests
 FOR EACH ROW EXECUTE FUNCTION storage.guard_workspace_archive_v3_manifest_paths();

-- +goose StatementBegin
CREATE FUNCTION storage.guard_workspace_archive_v3_event_paths()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE plan jsonb;
BEGIN
 IF NEW.schema_version<>'storage.workspace_lifecycle_event.v3' THEN RETURN NEW; END IF;
 SELECT plan_json INTO plan FROM storage.workspace_archive_operations
  WHERE workspace_archive_operation_id=NEW.workspace_archive_operation_id AND schema_version='storage.workspace_archive_operation.v3';
 IF plan IS NULL OR NEW.details->'source_path' IS DISTINCT FROM plan#>'{source,path}'
  OR NEW.details->'destination_path' IS DISTINCT FROM plan#>'{destination,path}' THEN
  RAISE EXCEPTION 'V3 event contradicts owning operation paths' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workspace_archive_v3_event_paths
 BEFORE INSERT OR UPDATE ON storage.workspace_lifecycle_events
 FOR EACH ROW EXECUTE FUNCTION storage.guard_workspace_archive_v3_event_paths();

-- These additions do not change scope admission or permit editing old events.
ALTER TABLE notes_workspace.source_scopes DROP CONSTRAINT source_scopes_kind_check;
ALTER TABLE notes_workspace.source_scopes ADD CONSTRAINT source_scopes_kind_check CHECK(kind IN ('notes','box','retained'));
ALTER TABLE storage.retained_relocation_events DROP CONSTRAINT retained_relocation_events_phase_check;
ALTER TABLE storage.retained_relocation_events ADD CONSTRAINT retained_relocation_events_phase_check
 CHECK(phase IN ('planned','prepared','complete','cancelled','location_successor'));

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'workspace active location migration is forward-only; retain V3 and location successor evidence';
END $$;
-- +goose StatementEnd
