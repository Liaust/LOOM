-- +goose Up
-- V1 checks are retained verbatim behind their original version gate. Only
-- explicitly project-scoped V2 counterparts are admitted. No evidence is updated.

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v2_ref(ref jsonb, root_name text, relative_name text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_typeof(ref) = 'object'
  AND ref->>'root' = root_name AND ref->>'relative_path' = relative_name
  AND octet_length(ref->>'absolute_path') BETWEEN 1 AND 4096
  AND ref->>'absolute_path' LIKE '/%'
  AND ref->>'absolute_path' !~ '(^|/)[.]{1,2}(/|$)|//|/$'
  AND right(ref->>'absolute_path', length(relative_name)+1) = '/' || relative_name, false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v2_identity(i jsonb, presence text, device text)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT COALESCE(jsonb_typeof(i) = 'object' AND i->>'presence' = presence AND
  CASE WHEN presence = 'absent' THEN i = '{"presence":"absent"}'::jsonb
  ELSE i->>'object_kind' = 'directory' AND i->>'device_id' = device
   AND (i->>'device_id')::numeric > 0 AND (i->>'inode')::numeric > 0
   AND (i->>'mode')::numeric > 0 AND (i->>'modified_unix_ns')::numeric <> 0
   AND COALESCE((i->>'size_bytes')::numeric, 0) >= 0 END, false)
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v2_chain(chain jsonb, relative_name text, parent jsonb, device text)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE parts text[] := string_to_array(relative_name, '/');
 prefix text := '.'; i integer;
BEGIN
 IF jsonb_typeof(chain) IS DISTINCT FROM 'array' THEN RETURN false; END IF;
 IF jsonb_array_length(chain) <> cardinality(parts) THEN RETURN false; END IF;
 FOR i IN 0..cardinality(parts)-1 LOOP
  IF i > 0 THEN prefix := CASE WHEN prefix='.' THEN parts[i] ELSE prefix||'/'||parts[i] END; END IF;
  IF chain->i->>'relative_path' IS DISTINCT FROM prefix OR
   NOT storage.workspace_archive_v2_identity(chain->i->'identity', 'present', device) THEN RETURN false; END IF;
 END LOOP;
 RETURN chain->(cardinality(parts)-1)->'identity' IS NOT DISTINCT FROM parent;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION storage.workspace_archive_v2_plan(p jsonb)
RETURNS boolean LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE op text := p->>'operation_kind'; slug text := p->>'slug';
 archive_id text := p->>'archive_operation_id'; metadata jsonb := p#>'{placement,metadata}';
 metadata_path text := metadata#>>'{path,relative_path}'; payload jsonb; active jsonb;
 device text := p#>>'{source,identity,device_id}'; relocation text := p#>>'{placement,relocation_id}';
 binding jsonb; chain jsonb; seen jsonb := '{}'::jsonb; ancestor jsonb; key text;
BEGIN
 IF p->>'schema_version' IS DISTINCT FROM 'storage.workspace_archive_plan.v2'
  OR p->>'evidence_kind' IS DISTINCT FROM 'physical_workspace_move'
  OR p->>'kind' IS DISTINCT FROM 'project'
  OR NOT COALESCE(slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$', false)
  OR NOT COALESCE(p->>'operation_id' ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$', false)
  OR jsonb_typeof(p->'placement') IS DISTINCT FROM 'object' THEN RETURN false; END IF;
 IF op = 'archive' THEN
  payload := p#>'{destination,path}'; active := p#>'{source,path}';
  IF metadata_path IS DISTINCT FROM '.loom/workspace-archives/'||(p->>'operation_id')
   OR COALESCE(relocation, '') <> '' OR COALESCE(archive_id, '') <> ''
   OR COALESCE(p->>'archive_manifest_digest', '') <> ''
   OR NOT storage.workspace_archive_v2_identity(metadata->'identity', 'absent', device)
   OR NOT storage.workspace_archive_v2_ref(payload, 'storage', 'Projects/'||slug) THEN RETURN false; END IF;
 ELSIF op = 'restore' THEN
  payload := p#>'{source,path}'; active := p#>'{destination,path}';
  IF NOT COALESCE(archive_id ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
   AND archive_id <> p->>'operation_id'
   AND p->>'archive_manifest_digest' ~ '^sha256:[0-9a-f]{64}$', false)
   OR p ? 'previous_cycle'
   OR NOT COALESCE(metadata_path IN ('archive/projects/'||slug, '.loom/workspace-archives/'||archive_id), false)
   OR NOT storage.workspace_archive_v2_identity(metadata->'identity', 'present', device)
   OR NOT (storage.workspace_archive_v2_ref(payload, 'storage', 'Projects/'||slug)
    OR storage.workspace_archive_v2_ref(payload, 'storage', 'archive/projects/'||slug||'/project'))
   OR (COALESCE(relocation, '') <> '' AND relocation !~ '^retained_relocation_[0-7][0-9A-HJKMNP-TV-Z]{25}$')
   OR (payload->>'relative_path' = 'Projects/'||slug AND metadata_path = 'archive/projects/'||slug AND COALESCE(relocation, '') = '')
   THEN RETURN false; END IF;
 ELSE RETURN false; END IF;
 IF NOT storage.workspace_archive_v2_ref(active, 'box', 'Projects/'||slug)
  OR NOT storage.workspace_archive_v2_ref(metadata->'path', 'storage', metadata_path)
  OR left(payload->>'absolute_path', length(payload->>'absolute_path')-length(payload->>'relative_path'))
   IS DISTINCT FROM left(metadata#>>'{path,absolute_path}', length(metadata#>>'{path,absolute_path}')-length(metadata_path))
  OR NOT storage.workspace_archive_v2_identity(p#>'{source,identity}', 'present', device)
  OR NOT storage.workspace_archive_v2_identity(p#>'{destination,identity}', 'absent', device)
  THEN RETURN false; END IF;
 FOR binding, chain IN SELECT p->'source', p->'source_ancestors'
  UNION ALL SELECT p->'destination', p->'destination_ancestors'
  UNION ALL SELECT metadata, p#>'{placement,metadata_ancestors}' LOOP
  IF NOT storage.workspace_archive_v2_identity(binding->'parent_identity', 'present', device)
   OR NOT storage.workspace_archive_v2_chain(chain, binding#>>'{path,relative_path}', binding->'parent_identity', device) THEN RETURN false; END IF;
  FOR ancestor IN SELECT value FROM jsonb_array_elements(chain)
   UNION ALL SELECT jsonb_build_object('relative_path', binding#>>'{path,relative_path}', 'identity', binding->'identity')
    WHERE binding#>>'{identity,presence}' = 'present' LOOP
   key := binding#>>'{path,root}' || ':' || (ancestor->>'relative_path');
   IF seen ? key AND seen->key IS DISTINCT FROM ancestor->'identity' THEN RETURN false; END IF;
   seen := jsonb_set(seen, ARRAY[key], ancestor->'identity');
  END LOOP;
 END LOOP;
 RETURN true;
END $$;
-- +goose StatementEnd

-- Locate each legacy check by its exact column dependency set and a required
-- literal. Names of anonymous table checks are PostgreSQL-generated. Refuse an
-- unexpected/missing/ambiguous catalog rather than dropping a family of checks.
-- +goose StatementBegin
CREATE FUNCTION pg_temp.extend_workspace_archive_check(target regclass, columns text[], marker text,
 legacy_gate text, v2_gate text, extra text, old_schema text DEFAULT NULL, new_schema text DEFAULT NULL)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE names text[]; expressions text[]; old_expression text; counterpart text;
BEGIN
 SELECT array_agg(c.conname::text), array_agg(pg_get_expr(c.conbin, c.conrelid)) INTO names, expressions
 FROM pg_constraint c WHERE c.conrelid = target AND c.contype = 'c' AND c.convalidated
 AND ARRAY(SELECT a.attname::text FROM unnest(c.conkey) k
  JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k ORDER BY a.attname::text COLLATE "C")
  = ARRAY(SELECT col FROM unnest(columns) col ORDER BY col COLLATE "C")
 AND strpos(pg_get_expr(c.conbin, c.conrelid), marker) > 0;
 IF cardinality(names) IS DISTINCT FROM 1 THEN
  RAISE EXCEPTION 'expected exactly one legacy check on % for % containing %, found %', target, columns, marker, cardinality(names);
 END IF;
 old_expression := expressions[1];
 counterpart := extra;
 IF old_schema IS NOT NULL THEN
  IF length(old_expression)-length(replace(old_expression, old_schema, '')) <> length(old_schema) THEN
   RAISE EXCEPTION 'expected exactly one schema literal in %.%', target, names[1];
  END IF;
  counterpart := '('||replace(old_expression, old_schema, new_schema)||') AND ('||extra||')';
 END IF;
 EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I, ADD CONSTRAINT %I CHECK ((((%s) AND (%s)) OR ((%s) AND (%s))) IS TRUE)',
  target, names[1], names[1], legacy_gate, old_expression, v2_gate, counterpart);
END $$;
-- +goose StatementEnd

SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_operations', ARRAY['schema_version'],
 'storage.workspace_archive_operation.v1', 'true',
 $$schema_version='storage.workspace_archive_operation.v2' AND workspace_kind='project'$$, 'true');

SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_operations', ARRAY[
 'plan_json','workspace_archive_operation_id','operation_kind','workspace_kind','object_id','slug','plan_digest',
 'source_root','source_relative_path','source_identity_json','source_parent_identity_json',
 'destination_root','destination_relative_path','destination_identity_json','destination_parent_identity_json',
 'inventory_digest','actor_id','reason','planned_at'],
 'storage.workspace_archive_plan.v1', $$schema_version='storage.workspace_archive_operation.v1' AND NOT (plan_json ? 'placement')$$,
 $$schema_version='storage.workspace_archive_operation.v2' AND workspace_kind='project'$$,
 'storage.workspace_archive_v2_plan(plan_json)', 'storage.workspace_archive_plan.v1', 'storage.workspace_archive_plan.v2');

SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_operations',
 ARRAY['source_relative_path','operation_kind','workspace_kind','slug'], 'archive/projects/',
 $$schema_version='storage.workspace_archive_operation.v1'$$,
 $$schema_version='storage.workspace_archive_operation.v2' AND workspace_kind='project'$$,
 $$source_relative_path='Projects/'||slug OR (operation_kind='restore' AND source_relative_path='archive/projects/'||slug||'/project')$$);
SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_operations',
 ARRAY['destination_relative_path','operation_kind','workspace_kind','slug'], 'archive/projects/',
 $$schema_version='storage.workspace_archive_operation.v1'$$,
 $$schema_version='storage.workspace_archive_operation.v2' AND workspace_kind='project'$$,
 $$destination_relative_path='Projects/'||slug$$);

SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_manifests', ARRAY['schema_version'],
 'storage.workspace_archive_manifest.v1', 'true',
 $$schema_version='storage.workspace_archive_manifest.v2' AND workspace_kind='project'$$, 'true');
SELECT pg_temp.extend_workspace_archive_check('storage.workspace_archive_manifests', ARRAY[
 'manifest_json','workspace_archive_operation_id','workspace_kind','object_id','slug','lifecycle_state',
 'plan_digest','restore_plan_digest','inventory_digest','archive_source_identity_json','authentication_key_id',
 'authentication_tag','archived_at','restore_operation_id','restored_at'],
 'storage.workspace_archive_manifest.v1', $$schema_version='storage.workspace_archive_manifest.v1' AND NOT (manifest_json ? 'restored_from_path')$$,
 $$schema_version='storage.workspace_archive_manifest.v2' AND workspace_kind='project'$$,
 $$storage.workspace_archive_v2_ref(manifest_json->'active_path','box','Projects/'||slug)
 AND (storage.workspace_archive_v2_ref(manifest_json->'archive_payload_path','storage','Projects/'||slug)
  OR (lifecycle_state='active' AND storage.workspace_archive_v2_ref(manifest_json->'archive_payload_path','storage','archive/projects/'||slug||'/project')))
 AND (NOT (manifest_json ? 'restored_from_path') OR (lifecycle_state='active' AND
  (storage.workspace_archive_v2_ref(manifest_json->'restored_from_path','storage','Projects/'||slug)
   OR storage.workspace_archive_v2_ref(manifest_json->'restored_from_path','storage','archive/projects/'||slug||'/project'))
  AND left(manifest_json#>>'{restored_from_path,absolute_path}',length(manifest_json#>>'{restored_from_path,absolute_path}')-length(manifest_json#>>'{restored_from_path,relative_path}'))
   = left(manifest_json#>>'{archive_payload_path,absolute_path}',length(manifest_json#>>'{archive_payload_path,absolute_path}')-length(manifest_json#>>'{archive_payload_path,relative_path}'))))$$,
 'storage.workspace_archive_manifest.v1', 'storage.workspace_archive_manifest.v2');

SELECT pg_temp.extend_workspace_archive_check('storage.workspace_lifecycle_events', ARRAY['schema_version'],
 'storage.workspace_lifecycle_event.v1', $$COALESCE(details->>'schema_version','storage.workspace_lifecycle_event.v1')='storage.workspace_lifecycle_event.v1'$$,
 $$schema_version='storage.workspace_lifecycle_event.v2' AND workspace_kind='project'$$,
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
SELECT pg_temp.extend_workspace_archive_check('storage.workspace_lifecycle_events',
 ARRAY['source_relative_path','transition','workspace_kind','slug'], 'archive/projects/',
 $$schema_version='storage.workspace_lifecycle_event.v1'$$,
 $$schema_version='storage.workspace_lifecycle_event.v2' AND workspace_kind='project'$$,
 $$source_relative_path='Projects/'||slug OR (transition='archived_to_active' AND source_relative_path='archive/projects/'||slug||'/project')$$);
SELECT pg_temp.extend_workspace_archive_check('storage.workspace_lifecycle_events',
 ARRAY['destination_relative_path','transition','workspace_kind','slug'], 'archive/projects/',
 $$schema_version='storage.workspace_lifecycle_event.v1'$$,
 $$schema_version='storage.workspace_lifecycle_event.v2' AND workspace_kind='project'$$,
 $$destination_relative_path='Projects/'||slug$$);

SELECT pg_temp.extend_workspace_archive_check('projects.physical_archive_plan_evidence',
 ARRAY['payload','plan_digest','request_json','operation_id','operation_kind','project_id'],
 'storage.project_physical_',
 $$convert_from(payload,'UTF8')::jsonb->>'schema_version'='storage.project_physical_'||operation_kind||'_plan.v1'
 AND convert_from(payload,'UTF8')::jsonb#>>'{workspace,schema_version}'='storage.workspace_archive_plan.v1'
 AND NOT (convert_from(payload,'UTF8')::jsonb->'workspace' ? 'placement')$$,
 $$convert_from(payload,'UTF8')::jsonb->>'schema_version'='storage.project_physical_'||operation_kind||'_plan.v2'$$,
 $$storage.workspace_archive_v2_plan(convert_from(payload,'UTF8')::jsonb->'workspace')$$, '_plan.v1', '_plan.v2');

-- Schema-qualified FKs reject partial version upgrades without changing old
-- plan/manifests/events. A V2 restore may explicitly refer to a V1 archive.
ALTER TABLE storage.workspace_archive_operations ADD CONSTRAINT workspace_archive_operations_version_key
 UNIQUE (workspace_archive_operation_id, schema_version);
ALTER TABLE storage.workspace_lifecycle_events
 ADD COLUMN operation_schema_version text GENERATED ALWAYS AS
  (replace(schema_version,'workspace_lifecycle_event','workspace_archive_operation')) STORED,
 ADD CONSTRAINT workspace_lifecycle_events_operation_version_fkey
  FOREIGN KEY (workspace_archive_operation_id,operation_schema_version)
  REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id,schema_version);
ALTER TABLE storage.workspace_archive_manifests
 ADD COLUMN archive_schema_version text GENERATED ALWAYS AS (CASE
  WHEN schema_version='storage.workspace_archive_manifest.v1' OR manifest_json#>>'{archive_payload_path,relative_path}'='archive/projects/'||slug||'/project'
  THEN 'storage.workspace_archive_operation.v1' ELSE 'storage.workspace_archive_operation.v2' END) STORED,
 ADD COLUMN restore_schema_version text GENERATED ALWAYS AS (CASE WHEN restore_operation_id IS NOT NULL
  THEN replace(schema_version,'workspace_archive_manifest','workspace_archive_operation') END) STORED,
 ADD CONSTRAINT workspace_archive_manifests_archive_version_fkey
  FOREIGN KEY (workspace_archive_operation_id,archive_schema_version)
  REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id,schema_version),
 ADD CONSTRAINT workspace_archive_manifests_restore_version_fkey
  FOREIGN KEY (restore_operation_id,restore_schema_version)
  REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id,schema_version);

-- Only new V2 receipts qualify a current restore source independently of the
-- historical archive path. Validate both against the actual immutable plans.
-- +goose StatementBegin
CREATE FUNCTION storage.guard_workspace_archive_v2_manifest_paths()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE archive_plan jsonb; restore_plan jsonb;
BEGIN
 IF NEW.schema_version <> 'storage.workspace_archive_manifest.v2' THEN RETURN NEW; END IF;
 SELECT plan_json INTO archive_plan FROM storage.workspace_archive_operations
  WHERE workspace_archive_operation_id=NEW.workspace_archive_operation_id;
 IF archive_plan IS NULL
  OR NEW.manifest_json->'active_path' IS DISTINCT FROM archive_plan#>'{source,path}'
  OR NEW.manifest_json->'archive_payload_path' IS DISTINCT FROM archive_plan#>'{destination,path}'
  OR NEW.manifest_json->'archive_source_identity' IS DISTINCT FROM archive_plan#>'{source,identity}' THEN
  RAISE EXCEPTION 'V2 manifest contradicts historical archive plan' USING ERRCODE='23514';
 END IF;
 IF NEW.lifecycle_state='active' THEN
  SELECT plan_json INTO restore_plan FROM storage.workspace_archive_operations
   WHERE workspace_archive_operation_id=NEW.restore_operation_id;
  IF restore_plan IS NULL
   OR COALESCE(NEW.manifest_json->'restored_from_path', NEW.manifest_json->'archive_payload_path') IS DISTINCT FROM restore_plan#>'{source,path}'
   OR NEW.manifest_json->'active_path' IS DISTINCT FROM restore_plan#>'{destination,path}'
   OR restore_plan->>'archive_operation_id' IS DISTINCT FROM NEW.workspace_archive_operation_id THEN
   RAISE EXCEPTION 'V2 manifest contradicts actual restore plan' USING ERRCODE='23514';
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER workspace_archive_v2_manifest_paths
 BEFORE INSERT OR UPDATE ON storage.workspace_archive_manifests
 FOR EACH ROW EXECUTE FUNCTION storage.guard_workspace_archive_v2_manifest_paths();

DROP FUNCTION pg_temp.extend_workspace_archive_check(regclass,text[],text,text,text,text,text,text);

-- +goose Down
-- Deliberately irreversible once V2 evidence exists. Never delete or rewrite
-- authenticated history merely to fit the legacy schema.
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'workspace archive placement migration is forward-only; retain V2 evidence';
END $$;
-- +goose StatementEnd
