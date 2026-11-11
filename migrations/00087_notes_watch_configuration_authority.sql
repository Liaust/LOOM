-- +goose Up
-- Preserve original-generation and archive-stop proofs; successors are additive.
ALTER FUNCTION projects.declaration_watch_binding_matches(jsonb,jsonb,jsonb,text,text,text,text,boolean)
 RENAME TO declaration_watch_binding_original_matches;
ALTER FUNCTION projects.declaration_application_data_matches(jsonb,jsonb,jsonb,text,text,text,text)
 RENAME TO declaration_application_data_original_matches;

-- +goose StatementBegin
CREATE FUNCTION projects.declaration_watch_metadata_matches(before_metadata jsonb, after_metadata jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_typeof(before_metadata)='object' AND jsonb_typeof(after_metadata)='object'
 AND (before_metadata #- '{knowledge_source,config_hash}') IS NOT DISTINCT FROM
     (after_metadata #- '{knowledge_source,config_hash}');
$$;

CREATE FUNCTION projects.declaration_watch_policy_matches(before_config jsonb, after_config jsonb)
RETURNS boolean LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_typeof(before_config)='object' AND jsonb_typeof(after_config)='object'
 AND ((before_config-'watch') #- '{scan,full_rescan_interval}') =
     ((after_config-'watch') #- '{scan,full_rescan_interval}');
$$;

-- One proof for the complete current generation, not an adapter flag or a
-- historic good delivery. All receipt/journal tables remain their existing owners.
CREATE FUNCTION projects.declaration_watch_configuration_matches(expected jsonb, metadata jsonb, effective jsonb,
 effective_hash text, safe_key text, project text, node text, archive_stop boolean DEFAULT false)
RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE accepted projects.project_contract_registrations%ROWTYPE;
 delivery record; payload jsonb; desired jsonb; original jsonb; member jsonb;
 compiler bytea; portable jsonb; selected jsonb; binding jsonb; old_binding jsonb;
BEGIN
 SELECT * INTO accepted FROM projects.project_contract_registrations r
 WHERE r.project_id=project AND r.contract_schema_version='project.contract.v0.5'
 AND r.registration_status IN ('registered','archived');
 IF NOT FOUND OR accepted.validation_report_json->>'ok' IS DISTINCT FROM 'true'
 OR accepted.validation_report_json->'watched_roots' IS DISTINCT FROM accepted.registration_plan_json->'watched_roots'
 OR NOT accepted.registration_plan_json->'watched_roots' @> jsonb_build_array(expected) THEN RETURN false; END IF;
 SELECT d.*,m.payload_json,m.result_json,m.status AS message_status,m.node_id AS message_node,
 m.kind,m.direction,o.resolution,o.state AS operation_state,o.actor_id,o.origin_node_id,o.target_node_id
 INTO delivery FROM projects.declaration_watch_deliveries d
 JOIN communication.messages m ON m.communication_message_id=d.message_id
 JOIN projects.declaration_operations o USING(operation_id)
 WHERE d.project_id=project AND d.node_id=node ORDER BY d.desired_revision DESC LIMIT 1;
 IF NOT FOUND THEN RETURN false; END IF;
 payload:=delivery.payload_json; desired:=payload->'group';
 IF delivery.operation_state='superseded' OR delivery.target_node_id IS DISTINCT FROM node
 OR delivery.resolution#>'{plan,basis,effects}' IS DISTINCT FROM '["reconcile_watches"]'::jsonb
 OR delivery.message_status IS DISTINCT FROM 'acked' OR delivery.message_node IS DISTINCT FROM node
 OR delivery.kind IS DISTINCT FROM 'project.watch.reconcile.v1' OR delivery.direction IS DISTINCT FROM 'main_to_node'
 OR payload->>'schema_version' IS DISTINCT FROM 'project.watch.control.v1'
 OR payload->>'operation_id' IS DISTINCT FROM delivery.operation_id OR payload->>'group_hash' IS DISTINCT FROM delivery.group_hash
 OR desired->>'project_id' IS DISTINCT FROM project OR desired->>'node_id' IS DISTINCT FROM node
 OR desired->>'project_root' IS DISTINCT FROM accepted.project_root
 OR desired->>'node_key' IS DISTINCT FROM accepted.contract_json#>>'{project,owner_node}'
 OR desired ? 'predecessor' OR jsonb_typeof(desired->'roots') IS DISTINCT FROM 'array'
 OR jsonb_array_length(desired->'roots')<>jsonb_array_length(accepted.registration_plan_json->'watched_roots')
 OR (SELECT count(DISTINCT r->>'backend_root_key') FROM jsonb_array_elements(desired->'roots') r)<>jsonb_array_length(desired->'roots')
 OR NOT EXISTS (SELECT 1 FROM communication.message_acks a WHERE a.communication_message_id=delivery.message_id
 AND a.node_id=node AND a.ack_status='completed' AND a.result_json=delivery.result_json)
 OR delivery.result_json->>'schema_version' IS DISTINCT FROM 'project.watch.control.v1'
 OR delivery.result_json->>'operation_id' IS DISTINCT FROM delivery.operation_id
 OR delivery.result_json->>'project_id' IS DISTINCT FROM project OR delivery.result_json->>'node_id' IS DISTINCT FROM node
 OR delivery.result_json->>'group_hash' IS DISTINCT FROM delivery.group_hash
 OR delivery.result_json#>>'{evidence,schema_version}' IS DISTINCT FROM 'loom.control.evidence.v1'
 OR delivery.result_json#>'{evidence,desired_revision}' IS DISTINCT FROM to_jsonb(delivery.desired_revision)
 OR delivery.result_json#>'{evidence,applied_revision}' IS DISTINCT FROM to_jsonb(delivery.desired_revision)
 OR delivery.result_json#>>'{evidence,config_hash}' IS DISTINCT FROM delivery.group_hash
 OR delivery.result_json#>>'{evidence,outcome}' IS DISTINCT FROM 'completed'
 OR coalesce(delivery.result_json->>'error_code','')<>'' THEN RETURN false; END IF;

 -- Source snapshots must be the same complete accepted set, including schema
 -- and revision, and still carry their original content hashes.
 IF jsonb_typeof(desired->'sources') IS DISTINCT FROM 'array'
 OR jsonb_array_length(desired->'sources')<>jsonb_array_length(accepted.registration_plan_json#>'{declaration,sources}')
 OR (SELECT count(DISTINCT s->>'ref') FROM jsonb_array_elements(desired->'sources') s)<>jsonb_array_length(desired->'sources')
 OR EXISTS (SELECT 1 FROM jsonb_array_elements(accepted.registration_plan_json#>'{declaration,sources}') s
 WHERE NOT EXISTS (SELECT 1 FROM jsonb_array_elements(desired->'sources') g
 WHERE g=s-'raw' AND s->>'hash'='sha256:'||encode(sha256(decode(s->>'raw','base64')),'hex'))) THEN RETURN false; END IF;
 IF NOT EXISTS (SELECT 1 FROM projects.declaration_watch_intents i
 JOIN projects.declaration_action_receipts a USING(operation_id,action_id)
 WHERE i.operation_id=delivery.operation_id AND i.project_id=project AND i.node_id=node
 AND i.group_hash=delivery.group_hash AND i.final_intent AND i.stage='applied' AND NOT i.retire
 AND i.actor_id=delivery.actor_id AND i.origin_node_id=delivery.origin_node_id
 AND a.owner=i.owner AND a.token=i.token AND a.input_hash=i.input_hash AND a.state='succeeded'
 AND delivery.resolution#>ARRAY['payloads',i.action_id,'group']=desired)
 OR EXISTS (SELECT 1 FROM jsonb_array_elements(desired->'contributors') c WHERE c->'retire' IS DISTINCT FROM 'false'::jsonb)
 THEN RETURN false; END IF;

 FOR member IN SELECT * FROM jsonb_array_elements(desired->'roots') LOOP
  SELECT e INTO original FROM jsonb_array_elements(accepted.registration_plan_json->'watched_roots') e
  WHERE e->>'backend_root_key'=member->>'backend_root_key';
  compiler:=decode(member->>'compiler_config_json','base64'); portable:=convert_from(compiler,'UTF8')::jsonb;
  IF original IS NULL OR member->'enabled' IS DISTINCT FROM 'true'::jsonb
  OR member->>'local_root_key' IS DISTINCT FROM original->>'key'
  OR member->>'worker_key' IS DISTINCT FROM original->>'worker_key'
  OR member->'source_kinds' IS DISTINCT FROM original->'source_kinds'
  OR member->>'compiler_config_hash' IS DISTINCT FROM 'sha256:'||encode(sha256(compiler),'hex')
  OR (member->'metadata' ? 'knowledge_source' AND member#>>'{metadata,knowledge_source,config_hash}' IS DISTINCT FROM member->>'compiler_config_hash')
  OR NOT coalesce(projects.declaration_watch_policy_matches(original->'config_json',portable),false)
  OR NOT coalesce(projects.declaration_watch_metadata_matches(original->'metadata',member->'metadata'),false)
  THEN RETURN false; END IF;
  -- Include protection roots in the member proof even though only knowledge
  -- roots reach Notes. They need not carry knowledge_source metadata.
  IF NOT EXISTS (SELECT 1 FROM projects.project_watched_root_registrations r
   WHERE r.project_id=project AND r.node_id=node AND r.project_contract_registration_id=accepted.project_contract_registration_id
   AND r.backend_root_key=member->>'backend_root_key' AND r.local_root_key=member->>'local_root_key'
   AND r.owner_node_key=desired->>'node_key' AND r.worker_key=member->>'worker_key'
   AND r.source_kinds_json=member->'source_kinds' AND r.root_relative_path=original->>'root_relative_path'
   AND r.safe_root_key=member#>>'{config_json,safe_root_key}' AND r.display_name=original->>'display_name'
   AND r.sync_mode=original->>'sync_mode' AND r.index_mode=original->>'index_mode'
   AND r.backup_mode=original->>'backup_mode' AND r.delete_mode=original->>'delete_mode'
   AND r.activation_status IN ('applied','reported') AND r.config_json=member->'config_json' AND r.config_hash=member->>'config_hash'
   AND r.metadata-'declaration_adapter'=member->'metadata' AND r.metadata#>>'{declaration_adapter,group_hash}'=delivery.group_hash)
  OR EXISTS (SELECT 1 FROM projects.project_watched_root_registrations extra WHERE extra.project_id=project AND extra.node_id=node
   AND extra.activation_status NOT IN ('stale','disabled') AND NOT EXISTS
   (SELECT 1 FROM jsonb_array_elements(desired->'roots') r WHERE r->>'backend_root_key'=extra.backend_root_key))
  THEN
   IF NOT archive_stop THEN RETURN false; END IF;
  END IF;
  binding:=member->'application_data';
  IF binding IS NOT NULL THEN
   -- Original immutable delivery fixes allocation identity. Reinstallation may
   -- refresh only installation_revision, never path/device/inode/ownership.
   SELECT r->'application_data' INTO old_binding FROM projects.declaration_watch_deliveries d
   JOIN projects.declaration_watch_intents i ON i.operation_id=d.operation_id AND i.group_hash=d.group_hash AND i.final_intent AND i.stage='applied'
   JOIN communication.messages m ON m.communication_message_id=d.message_id
   CROSS JOIN LATERAL jsonb_array_elements(m.payload_json#>'{group,roots}') r
   WHERE d.project_id=project AND d.node_id=node AND r->>'backend_root_key'=member->>'backend_root_key'
   AND r->'metadata'=original->'metadata' AND r->>'compiler_config_hash'=original->>'config_hash'
   AND convert_from(decode(r->>'compiler_config_json','base64'),'UTF8')::jsonb=original->'config_json'
   ORDER BY d.desired_revision LIMIT 1;
   IF old_binding IS NULL OR coalesce(binding->>'installation_revision','')=''
   OR (binding-'installation_revision') IS DISTINCT FROM (old_binding-'installation_revision') THEN RETURN false; END IF;
  ELSIF original#>'{metadata,knowledge_source}' ? 'application_data' THEN RETURN false;
  END IF;
  original:=jsonb_set(jsonb_set(jsonb_set(original,'{config_json}',portable),'{config_hash}',member->'compiler_config_hash'),'{metadata}',member->'metadata');
  IF NOT coalesce(CASE WHEN binding IS NULL THEN projects.declaration_watch_binding_original_matches(original,
   member->'metadata'||jsonb_build_object('declaration_adapter',jsonb_build_object('schema_version','project.watch.binding.v1',
   'project_id',project,'node_id',node,'group_hash',delivery.group_hash,'safe_root_key',member#>>'{config_json,safe_root_key}',
   'compiler_config_json',member->>'compiler_config_json','compiler_config_hash',member->>'compiler_config_hash','effective_config_hash',member->>'config_hash')),
   member->'config_json',member->>'config_hash',member#>>'{config_json,safe_root_key}',project,node)
   ELSE projects.declaration_application_data_original_matches(original,
   member->'metadata'||jsonb_build_object('declaration_adapter',jsonb_build_object('schema_version','project.watch.binding.v1',
   'project_id',project,'node_id',node,'group_hash',delivery.group_hash,'safe_root_key',member#>>'{config_json,safe_root_key}',
   'compiler_config_json',member->>'compiler_config_json','compiler_config_hash',member->>'compiler_config_hash','effective_config_hash',member->>'config_hash',
   'application_data',binding)),member->'config_json',member->>'config_hash',member#>>'{config_json,safe_root_key}',project,node) END,false)
   THEN RETURN false; END IF;
  IF member->>'backend_root_key'=expected->>'backend_root_key' THEN selected:=member; END IF;
 END LOOP;
 IF selected IS NULL OR selected->'config_json' IS DISTINCT FROM effective OR selected->>'config_hash' IS DISTINCT FROM effective_hash
 OR selected#>>'{config_json,safe_root_key}' IS DISTINCT FROM safe_key THEN RETURN false; END IF;
 IF archive_stop THEN RETURN true; END IF;
 binding:=selected->'application_data';
 RETURN metadata IS NOT DISTINCT FROM selected->'metadata'||jsonb_build_object('declaration_adapter',
  jsonb_build_object('schema_version','project.watch.binding.v1','project_id',project,'node_id',node,'group_hash',delivery.group_hash,
  'safe_root_key',safe_key,'compiler_config_json',selected->>'compiler_config_json','compiler_config_hash',selected->>'compiler_config_hash',
  'effective_config_hash',effective_hash)||CASE WHEN binding IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('application_data',binding) END);
EXCEPTION WHEN others THEN RETURN false;
END; $$;

CREATE FUNCTION projects.declaration_watch_binding_matches(expected jsonb, metadata jsonb, effective jsonb,
 effective_hash text, safe_key text, project text, node text, archive_stop boolean DEFAULT false)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT projects.declaration_watch_binding_original_matches(expected,metadata,effective,effective_hash,safe_key,project,node,archive_stop)
 OR projects.declaration_watch_configuration_matches(expected,metadata,effective,effective_hash,safe_key,project,node,archive_stop);
$$;
CREATE FUNCTION projects.declaration_application_data_matches(expected jsonb, metadata jsonb, effective jsonb,
 effective_hash text, safe_key text, project text, node text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT projects.declaration_application_data_original_matches(expected,metadata,effective,effective_hash,safe_key,project,node)
 OR projects.declaration_watch_configuration_matches(expected,metadata,effective,effective_hash,safe_key,project,node);
$$;

-- Compute the retained source declarations once for set-based root reads.
-- The surrounding authority fence separately proves the current generation.
CREATE FUNCTION projects.declaration_watch_read_sources(project text, node text, root_key text)
RETURNS jsonb LANGUAGE plpgsql STABLE AS $$
DECLARE sources jsonb;
BEGIN
 WITH expected AS MATERIALIZED (
 SELECT e FROM projects.project_contract_registrations a
 CROSS JOIN LATERAL jsonb_array_elements(a.registration_plan_json->'watched_roots') e
 WHERE a.project_id=project AND e->>'backend_root_key'=root_key), readable AS (
 SELECT e#>'{metadata,knowledge_source}' AS source FROM expected
 UNION
 SELECT r#>'{metadata,knowledge_source}' FROM expected
 CROSS JOIN projects.declaration_watch_deliveries d
 JOIN projects.declaration_watch_intents i ON i.operation_id=d.operation_id AND i.group_hash=d.group_hash AND i.final_intent AND i.stage='applied'
 JOIN projects.declaration_operations o ON o.operation_id=d.operation_id
 JOIN communication.messages m ON m.communication_message_id=d.message_id
 CROSS JOIN LATERAL jsonb_array_elements(m.payload_json#>'{group,roots}') r
 WHERE d.project_id=project AND d.node_id=node AND o.state<>'superseded'
 AND o.resolution#>'{plan,basis,effects}'='["reconcile_watches"]'::jsonb
 AND r->>'backend_root_key'=root_key
 AND r#>>'{metadata,knowledge_source,config_hash}'=r->>'compiler_config_hash'
 AND projects.declaration_watch_metadata_matches(e->'metadata',r->'metadata')
 AND projects.declaration_watch_policy_matches(e->'config_json',convert_from(decode(r->>'compiler_config_json','base64'),'UTF8')::jsonb)
 AND r->>'compiler_config_hash'='sha256:'||encode(sha256(decode(r->>'compiler_config_json','base64')),'hex'))
 SELECT coalesce(jsonb_agg(source),'[]'::jsonb) INTO sources FROM readable WHERE source IS NOT NULL;
 RETURN sources;
EXCEPTION WHEN others THEN RETURN '[]'::jsonb;
END;
$$;

CREATE FUNCTION projects.declaration_watch_source_matches(published jsonb, current_source jsonb, project text, node text, root_key text)
RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(published=current_source,false) OR coalesce((
 jsonb_typeof(published)='object' AND jsonb_typeof(current_source)='object'
 AND published-'config_hash'=current_source-'config_hash'
 AND projects.declaration_watch_read_sources(project,node,root_key) @> jsonb_build_array(published)),false);
$$;
-- +goose StatementEnd

-- +goose Down
-- Refuse a downgrade that would hide an adopted configuration successor.
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM projects.declaration_operations WHERE resolution#>'{plan,basis,effects}'='["reconcile_watches"]'::jsonb) THEN
  RAISE EXCEPTION 'watch configuration successor evidence must be preserved';
 END IF;
END; $$;
-- +goose StatementEnd
DROP FUNCTION projects.declaration_watch_source_matches(jsonb,jsonb,text,text,text);
DROP FUNCTION projects.declaration_watch_read_sources(text,text,text);
DROP FUNCTION projects.declaration_application_data_matches(jsonb,jsonb,jsonb,text,text,text,text);
DROP FUNCTION projects.declaration_watch_binding_matches(jsonb,jsonb,jsonb,text,text,text,text,boolean);
DROP FUNCTION projects.declaration_watch_configuration_matches(jsonb,jsonb,jsonb,text,text,text,text,boolean);
DROP FUNCTION projects.declaration_watch_policy_matches(jsonb,jsonb);
DROP FUNCTION projects.declaration_watch_metadata_matches(jsonb,jsonb);
ALTER FUNCTION projects.declaration_watch_binding_original_matches(jsonb,jsonb,jsonb,text,text,text,text,boolean) RENAME TO declaration_watch_binding_matches;
ALTER FUNCTION projects.declaration_application_data_original_matches(jsonb,jsonb,jsonb,text,text,text,text) RENAME TO declaration_application_data_matches;
