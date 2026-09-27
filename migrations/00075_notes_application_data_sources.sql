-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION projects.declaration_application_data_matches(expected jsonb, metadata jsonb, effective jsonb, effective_hash text, safe_key text, project text, node text) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE adapter jsonb; binding jsonb; reference jsonb; compiler_bytes bytea; compiler_text text; effective_text text; expected_key text; subpath text; transformed jsonb;
BEGIN
 adapter:=metadata->'declaration_adapter'; binding:=adapter->'application_data';
 reference:=expected#>'{metadata,knowledge_source,application_data}';
 expected_key:='declaration_app_'||lower(project)||'_'||(expected->>'backend_root_key');
 subpath:=coalesce(nullif(reference->>'subpath',''),'.');
 IF jsonb_typeof(reference) IS DISTINCT FROM 'object' OR jsonb_typeof(binding) IS DISTINCT FROM 'object'
 OR metadata-'declaration_adapter' IS DISTINCT FROM expected->'metadata'
 OR expected->>'safe_root_key' IS DISTINCT FROM 'project' OR safe_key IS DISTINCT FROM expected_key
 OR binding->>'application' IS DISTINCT FROM reference->>'application' OR binding->>'data' IS DISTINCT FROM reference->>'data'
 OR coalesce(binding->>'subpath','') IS DISTINCT FROM coalesce(reference->>'subpath','')
 OR coalesce(binding->>'path','') !~ '^/[^/]'
 OR coalesce(binding->>'path','') ~ '(^|/)(\.|\.\.)(/|$)' OR right(binding->>'path',1)='/'
 OR (binding->>'inode')::numeric <= 0 OR (binding->>'pool_inode')::numeric <= 0
 OR coalesce(binding->>'binding_ref','')='' OR coalesce(binding->>'pool_identity','')=''
 OR coalesce(binding->>'installation_revision','')='' OR coalesce(binding->>'location_revision','')=''
 OR coalesce(binding->>'policy_revision','')=''
 OR adapter IS DISTINCT FROM jsonb_build_object('schema_version','project.watch.binding.v1','project_id',project,'node_id',node,
  'group_hash',adapter->>'group_hash','safe_root_key',expected_key,'compiler_config_json',adapter->>'compiler_config_json',
  'compiler_config_hash',expected->>'config_hash','effective_config_hash',effective_hash,'application_data',binding)
 OR coalesce(adapter->>'group_hash','') !~ '^sha256:[0-9a-f]{64}$' THEN RETURN false; END IF;
 compiler_bytes:=decode(adapter->>'compiler_config_json','base64'); compiler_text:=convert_from(compiler_bytes,'UTF8');
 IF 'sha256:'||encode(sha256(compiler_bytes),'hex') IS DISTINCT FROM expected->>'config_hash'
 OR compiler_text::jsonb IS DISTINCT FROM expected->'config_json' THEN RETURN false; END IF;
 transformed:=jsonb_set(jsonb_set(jsonb_set(expected->'config_json','{safe_root_key}',to_jsonb(expected_key)),
  '{root_relative_path}',to_jsonb(subpath)),'{ignore_policy,policy_root_relative_path}','"."');
 effective_text:=replace(compiler_text,'"safe_root_key":"project"','"safe_root_key":'||to_json(expected_key)::text);
 effective_text:=replace(effective_text,'"root_relative_path":'||to_json(expected->>'root_relative_path')::text,'"root_relative_path":'||to_json(subpath)::text);
 effective_text:=replace(effective_text,'"policy_root_relative_path":'||to_json(expected#>>'{config_json,ignore_policy,policy_root_relative_path}')::text,'"policy_root_relative_path":"."');
 IF effective IS DISTINCT FROM transformed OR effective_text::jsonb IS DISTINCT FROM effective
 OR effective_hash IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to(effective_text,'UTF8')),'hex') THEN RETURN false; END IF;
 RETURN EXISTS(SELECT 1 FROM projects.declaration_watch_deliveries d
  JOIN projects.declaration_watch_intents i ON i.operation_id=d.operation_id AND i.group_hash=d.group_hash AND i.final_intent AND i.stage='applied'
  JOIN communication.messages m ON m.communication_message_id=d.message_id AND m.node_id=d.node_id AND m.kind='project.watch.reconcile.v1'
  CROSS JOIN LATERAL jsonb_array_elements(m.payload_json#>'{group,roots}') root
  WHERE d.project_id=project AND d.node_id=node AND d.group_hash=adapter->>'group_hash'
  AND root->>'backend_root_key'=expected->>'backend_root_key' AND root->'metadata'=expected->'metadata'
  AND root->>'compiler_config_json'=adapter->>'compiler_config_json' AND root->>'compiler_config_hash'=expected->>'config_hash'
  AND root->'config_json'=effective AND root->>'config_hash'=effective_hash AND root->'application_data'=binding);
EXCEPTION WHEN others THEN RETURN false;
END; $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM projects.project_watched_root_registrations WHERE metadata#>'{declaration_adapter,application_data}' IS NOT NULL) THEN
  RAISE EXCEPTION 'application-data source evidence must be preserved';
 END IF;
END; $$;
-- +goose StatementEnd
DROP FUNCTION projects.declaration_application_data_matches(jsonb,jsonb,jsonb,text,text,text,text);
