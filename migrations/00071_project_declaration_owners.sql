-- +goose Up
-- Preserve all legacy combinations; v0.5 has exactly one real source.
-- +goose StatementBegin
DO $$
DECLARE tab text; c record;
BEGIN
 FOREACH tab IN ARRAY ARRAY['project_repository_sources','project_repository_source_history'] LOOP
  FOR c IN SELECT conname FROM pg_constraint WHERE conrelid=('projects.'||tab)::regclass AND contype='c'
   AND (pg_get_constraintdef(oid) LIKE '%project_contract_schema_version%' OR pg_get_constraintdef(oid) LIKE '%repos_contract_schema_version%') LOOP
   EXECUTE format('ALTER TABLE projects.%I DROP CONSTRAINT %I',tab,c.conname);
  END LOOP;
  EXECUTE format($q$ALTER TABLE projects.%I ADD CONSTRAINT declaration_source_versions CHECK (
   (project_contract_schema_version IN ('project.contract.v0.3','project.contract.v0.4') AND repos_contract_schema_version IN ('repos.contract.v0.3','repos.contract.v0.4')) OR
   (project_contract_schema_version='project.contract.v0.5' AND repos_contract_schema_version='project.contract.v0.5'
    AND project_contract_path=project_root||'/.loom/project.yaml' AND repos_contract_path=project_contract_path AND repos_contract_digest=project_contract_digest))$q$,tab);
 END LOOP;
END; $$;
-- +goose StatementEnd

CREATE TABLE projects.declaration_owner_receipts (
 project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE RESTRICT,
 owner text NOT NULL CHECK (owner IN ('projects','knowledge','backupcontracts')),
 token text NOT NULL CHECK (token ~ '^sha256:[0-9a-f]{64}$'),
 input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
 operation_id text NOT NULL REFERENCES projects.declaration_operations(operation_id) ON DELETE RESTRICT,
 action_id text NOT NULL,
 actor_id text NOT NULL REFERENCES identity.actors(actor_id) ON DELETE RESTRICT,
 origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
 target_node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
 result jsonb NOT NULL CHECK (jsonb_typeof(result)='object' AND octet_length(result::text)<=262144),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (project_id,owner,token),
 FOREIGN KEY (operation_id,action_id) REFERENCES projects.declaration_action_receipts(operation_id,action_id) ON DELETE RESTRICT
);
-- +goose StatementBegin
CREATE FUNCTION projects.guard_declaration_owner_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 RAISE EXCEPTION 'declaration owner dedup evidence is immutable';
END; $$;
-- +goose StatementEnd
CREATE TRIGGER declaration_owner_receipt_guard BEFORE UPDATE OR DELETE ON projects.declaration_owner_receipts
FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_owner_receipt();
-- +goose StatementBegin
CREATE FUNCTION projects.validate_declaration_owner_receipt() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE stage jsonb; pair record;
BEGIN
 IF NOT EXISTS (SELECT 1 FROM projects.declaration_operations o JOIN projects.declaration_action_receipts a USING(operation_id)
  WHERE o.operation_id=NEW.operation_id AND a.action_id=NEW.action_id AND a.owner=NEW.owner AND a.token=NEW.token AND a.input_hash=NEW.input_hash
   AND o.actor_id=NEW.actor_id AND o.origin_node_id=NEW.origin_node_id AND o.project_id=NEW.project_id AND o.target_node_id=NEW.target_node_id) THEN
  RAISE EXCEPTION 'declaration owner receipt identity mismatch';
 END IF;
 IF NEW.owner='projects' THEN
  IF NEW.result - ARRAY['effect_ref','before','after'] <> '{}'::jsonb OR NOT EXISTS (
   SELECT 1 FROM projects.project_contract_registrations WHERE project_id=NEW.project_id AND project_contract_registration_id=NEW.result->>'effect_ref') THEN
   RAISE EXCEPTION 'invalid declaration registration reference';
  END IF;
  FOREACH stage IN ARRAY ARRAY[NEW.result->'before',NEW.result->'after'] LOOP
   IF jsonb_typeof(stage) IS DISTINCT FROM 'object' OR stage - ARRAY['registry_revision','lifecycle_revision','repositories'] <> '{}'::jsonb
    OR coalesce(stage->>'registry_revision','') !~ '^sha256:[0-9a-f]{64}$' OR coalesce(stage->>'lifecycle_revision','') !~ '^sha256:[0-9a-f]{64}$'
    OR jsonb_typeof(stage->'repositories') IS DISTINCT FROM 'object' THEN RAISE EXCEPTION 'invalid declaration owner state'; END IF;
   IF (SELECT count(*) FROM jsonb_object_keys(stage->'repositories'))>500 THEN RAISE EXCEPTION 'declaration owner member bound exceeded'; END IF;
   FOR pair IN SELECT * FROM jsonb_each(stage->'repositories') LOOP
    IF pair.key !~ '^[a-z][a-z0-9_-]{0,62}$' OR jsonb_typeof(pair.value)<>'string' OR (pair.value#>>'{}') !~ '^repo_[0-7][0-9A-HJKMNP-TV-Z]{25}$' THEN
     RAISE EXCEPTION 'invalid declaration owner repository identity';
    END IF;
   END LOOP;
  END LOOP;
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER declaration_owner_receipt_validate BEFORE INSERT ON projects.declaration_owner_receipts
FOR EACH ROW EXECUTE FUNCTION projects.validate_declaration_owner_receipt();
REVOKE ALL ON projects.declaration_owner_receipts FROM PUBLIC;


CREATE TABLE projects.declaration_watch_intents (
 operation_id text NOT NULL REFERENCES projects.declaration_operations(operation_id) ON DELETE RESTRICT,
 action_id text NOT NULL,
 project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE RESTRICT,
 node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
 actor_id text NOT NULL REFERENCES identity.actors(actor_id) ON DELETE RESTRICT,
 origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
 owner text NOT NULL CHECK (owner IN ('knowledge','backupcontracts')),
 resource_key text NOT NULL CHECK (resource_key ~ '^[a-z][a-z0-9_-]{0,62}$'),
 token text NOT NULL CHECK (token ~ '^sha256:[0-9a-f]{64}$'),
 input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
 group_hash text NOT NULL CHECK (group_hash ~ '^sha256:[0-9a-f]{64}$'),
 authority_revision text NOT NULL CHECK (authority_revision ~ '^sha256:[0-9a-f]{64}$'),
 before_revision text NOT NULL CHECK (before_revision='absent' OR before_revision ~ '^sha256:[0-9a-f]{64}$'),
 after_revision text NOT NULL CHECK (after_revision ~ '^sha256:[0-9a-f]{64}$'),
 retire boolean NOT NULL,
 final_intent boolean NOT NULL,
 stage text NOT NULL CHECK (stage IN ('registered','pending','applied')),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (operation_id,action_id),
 UNIQUE (project_id,owner,token),
 FOREIGN KEY (operation_id,action_id) REFERENCES projects.declaration_action_receipts(operation_id,action_id) ON DELETE RESTRICT,
 CHECK ((stage='registered') = (NOT final_intent))
);
CREATE INDEX declaration_watch_intent_resource_idx ON projects.declaration_watch_intents(project_id,node_id,owner,resource_key,created_at DESC);
CREATE TABLE projects.declaration_watch_deliveries (
 project_id text NOT NULL REFERENCES projects.projects(project_id) ON DELETE RESTRICT,
 node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT,
 desired_revision bigint NOT NULL CHECK (desired_revision>0),
 operation_id text NOT NULL REFERENCES projects.declaration_operations(operation_id) ON DELETE RESTRICT,
 group_hash text NOT NULL CHECK (group_hash ~ '^sha256:[0-9a-f]{64}$'),
 message_id text NOT NULL UNIQUE REFERENCES communication.messages(communication_message_id) ON DELETE RESTRICT,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(project_id,node_id,desired_revision),
 UNIQUE(operation_id,project_id,node_id,group_hash)
);
REVOKE ALL ON projects.declaration_watch_intents,projects.declaration_watch_deliveries FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION projects.guard_declaration_watch_intent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE payload jsonb; expected_final text;
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'watch intent evidence is immutable'; END IF;
 IF TG_OP='UPDATE' THEN
  IF to_jsonb(NEW)-'stage' <> to_jsonb(OLD)-'stage' OR OLD.stage<>'pending' OR NEW.stage<>'applied' OR NOT OLD.final_intent THEN
   RAISE EXCEPTION 'watch intent identity and completed evidence are immutable';
  END IF;
  IF NOT EXISTS(SELECT 1 FROM projects.declaration_watch_deliveries d JOIN communication.messages m ON m.communication_message_id=d.message_id
   WHERE d.operation_id=NEW.operation_id AND d.project_id=NEW.project_id AND d.node_id=NEW.node_id AND d.group_hash=NEW.group_hash
   AND m.kind='project.watch.reconcile.v1' AND m.node_id=NEW.node_id AND m.status='acked'
   AND EXISTS(SELECT 1 FROM communication.message_acks a WHERE a.communication_message_id=m.communication_message_id AND a.node_id=m.node_id AND a.ack_status='completed' AND a.result_json=m.result_json)
   AND m.result_json->>'schema_version'='project.watch.control.v1' AND m.result_json->>'operation_id'=NEW.operation_id
   AND m.result_json->>'project_id'=NEW.project_id AND m.result_json->>'node_id'=NEW.node_id AND m.result_json->>'group_hash'=NEW.group_hash
   AND m.result_json#>>'{evidence,schema_version}'='loom.control.evidence.v1'
   AND m.result_json#>'{evidence,desired_revision}'=to_jsonb(d.desired_revision)
   AND m.result_json#>'{evidence,applied_revision}'=to_jsonb(d.desired_revision)
   AND m.result_json#>>'{evidence,config_hash}'=NEW.group_hash AND m.result_json#>>'{evidence,outcome}'='completed'
   AND coalesce(m.result_json->>'error_code','')='') THEN RAISE EXCEPTION 'exact node acknowledgement required'; END IF;
  RETURN NEW;
 END IF;
 SELECT o.resolution->'payloads'->NEW.action_id INTO payload
 FROM projects.declaration_operations o JOIN projects.declaration_action_receipts a USING(operation_id)
 WHERE o.operation_id=NEW.operation_id AND a.action_id=NEW.action_id AND a.owner=NEW.owner AND a.token=NEW.token AND a.input_hash=NEW.input_hash
 AND o.actor_id=NEW.actor_id AND o.origin_node_id=NEW.origin_node_id AND o.project_id=NEW.project_id AND o.target_node_id=NEW.node_id AND o.state<>'superseded';
 IF payload IS NULL OR payload->>'owner' IS DISTINCT FROM NEW.owner OR payload->>'resource' IS DISTINCT FROM NEW.resource_key
 OR payload->'retire' IS DISTINCT FROM to_jsonb(NEW.retire) OR payload->>'group_hash' IS DISTINCT FROM NEW.group_hash
 OR payload#>>'{group,project_id}' IS DISTINCT FROM NEW.project_id OR payload#>>'{group,node_id}' IS DISTINCT FROM NEW.node_id THEN
  RAISE EXCEPTION 'watch intent journal identity mismatch';
 END IF;
 SELECT max(c->>'action_id') INTO expected_final FROM jsonb_array_elements(payload#>'{group,contributors}') c;
 IF NEW.final_intent IS DISTINCT FROM (NEW.action_id=expected_final) OR NEW.stage IS DISTINCT FROM (CASE WHEN NEW.final_intent THEN 'pending' ELSE 'registered' END) THEN
  RAISE EXCEPTION 'invalid initial watch intent stage';
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER declaration_watch_intent_guard BEFORE INSERT OR UPDATE OR DELETE ON projects.declaration_watch_intents FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_watch_intent();
-- +goose StatementBegin
CREATE FUNCTION projects.guard_declaration_watch_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE payload jsonb; contributor jsonb; matched boolean;
BEGIN
 IF TG_OP<>'INSERT' THEN RAISE EXCEPTION 'watch delivery identity is immutable'; END IF;
 SELECT m.payload_json INTO payload FROM communication.messages m JOIN projects.declaration_operations o ON o.operation_id=NEW.operation_id
 WHERE m.communication_message_id=NEW.message_id AND m.kind='project.watch.reconcile.v1' AND m.direction='main_to_node'
 AND m.node_id=NEW.node_id AND o.project_id=NEW.project_id AND o.target_node_id=NEW.node_id;
 IF payload IS NULL OR payload->>'schema_version' IS DISTINCT FROM 'project.watch.control.v1'
 OR payload->>'operation_id' IS DISTINCT FROM NEW.operation_id OR payload->>'group_hash' IS DISTINCT FROM NEW.group_hash
 OR payload#>>'{group,project_id}' IS DISTINCT FROM NEW.project_id OR payload#>>'{group,node_id}' IS DISTINCT FROM NEW.node_id
 OR payload#>'{evidence,desired_revision}' IS DISTINCT FROM to_jsonb(NEW.desired_revision)
 OR payload#>>'{evidence,config_hash}' IS DISTINCT FROM NEW.group_hash
 OR jsonb_typeof(payload->'tokens') IS DISTINCT FROM 'array' OR jsonb_array_length(payload->'tokens')=0
 OR jsonb_array_length(payload->'tokens')<>(SELECT count(DISTINCT token->>'action_id') FROM jsonb_array_elements(payload->'tokens') token)
 OR jsonb_array_length(payload->'tokens')<>(SELECT count(*) FROM projects.declaration_watch_intents i WHERE i.operation_id=NEW.operation_id AND i.group_hash=NEW.group_hash) THEN
  RAISE EXCEPTION 'watch delivery identity or exact intent set mismatch';
 END IF;
 FOR contributor IN SELECT * FROM jsonb_array_elements(payload->'tokens') LOOP
  SELECT EXISTS(SELECT 1 FROM projects.declaration_watch_intents i JOIN projects.declaration_operations o USING(operation_id)
   WHERE i.operation_id=NEW.operation_id AND i.project_id=NEW.project_id AND i.node_id=NEW.node_id AND i.group_hash=NEW.group_hash
   AND i.action_id=contributor->>'action_id' AND i.owner=contributor->>'owner' AND i.resource_key=contributor->>'resource'
   AND i.token=contributor->>'token' AND i.input_hash=contributor->>'input_hash'
   AND o.resolution#>ARRAY['payloads',i.action_id,'group']=payload->'group') INTO matched;
  IF NOT matched THEN RAISE EXCEPTION 'watch delivery contributor mismatch'; END IF;
 END LOOP;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
CREATE TRIGGER declaration_watch_delivery_guard BEFORE INSERT OR UPDATE OR DELETE ON projects.declaration_watch_deliveries FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_watch_delivery();

-- Exact portable-to-local binding used by knowledge admission and read fences.
-- Decode errors are failed membership, not a relaxed legacy fallback.
-- +goose StatementBegin
CREATE FUNCTION projects.declaration_watch_binding_matches(expected jsonb, metadata jsonb, effective jsonb, effective_hash text, safe_key text, project text, node text, archive_stop boolean DEFAULT false) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE adapter jsonb; compiler_bytes bytea; compiler_text text; effective_text text; expected_key text;
BEGIN
 -- Physical archive deliberately replaces writer metadata. Only the caller's
 -- existing authenticated custody-stop predicate permits recovering the exact
 -- original metadata from immutable delivery evidence; live reads never do so.
 IF archive_stop THEN
  SELECT root->'metadata'||jsonb_build_object('declaration_adapter',jsonb_build_object(
   'schema_version','project.watch.binding.v1','project_id',d.project_id,'node_id',d.node_id,'group_hash',d.group_hash,
   'safe_root_key',safe_key,'compiler_config_json',root->>'compiler_config_json','compiler_config_hash',root->>'compiler_config_hash','effective_config_hash',root->>'config_hash')) INTO metadata
  FROM projects.declaration_watch_deliveries d JOIN projects.declaration_watch_intents i ON i.operation_id=d.operation_id AND i.group_hash=d.group_hash AND i.final_intent AND i.stage='applied'
  JOIN communication.messages m ON m.communication_message_id=d.message_id
  CROSS JOIN LATERAL jsonb_array_elements(m.payload_json#>'{group,roots}') root
  WHERE d.project_id=project AND d.node_id=node AND root->>'backend_root_key'=expected->>'backend_root_key'
   AND root->'metadata'=expected->'metadata' AND root->'config_json'=effective AND root->>'config_hash'=effective_hash
  ORDER BY d.desired_revision DESC LIMIT 1;
 END IF;
 adapter:=metadata->'declaration_adapter';
 expected_key:='declaration_'||lower(project);
 IF expected->>'safe_root_key' IS DISTINCT FROM 'project' OR safe_key IS DISTINCT FROM expected_key
 OR jsonb_typeof(adapter) IS DISTINCT FROM 'object' OR metadata-'declaration_adapter' IS DISTINCT FROM expected->'metadata'
 OR adapter IS DISTINCT FROM jsonb_build_object('schema_version','project.watch.binding.v1','project_id',project,'node_id',node,
  'group_hash',adapter->>'group_hash','safe_root_key',expected_key,'compiler_config_json',adapter->>'compiler_config_json',
  'compiler_config_hash',expected->>'config_hash','effective_config_hash',effective_hash)
 OR coalesce(adapter->>'group_hash','') !~ '^sha256:[0-9a-f]{64}$' THEN RETURN false; END IF;
 compiler_bytes:=decode(adapter->>'compiler_config_json','base64');
 compiler_text:=convert_from(compiler_bytes,'UTF8');
 IF 'sha256:'||encode(sha256(compiler_bytes),'hex') IS DISTINCT FROM expected->>'config_hash'
 OR compiler_text::jsonb IS DISTINCT FROM expected->'config_json' THEN RETURN false; END IF;
 effective_text:=replace(compiler_text,'"safe_root_key":"project"','"safe_root_key":"'||expected_key||'"');
 IF effective IS DISTINCT FROM jsonb_set(expected->'config_json','{safe_root_key}',to_jsonb(expected_key))
 OR effective_text::jsonb IS DISTINCT FROM effective
 OR effective_hash IS DISTINCT FROM 'sha256:'||encode(sha256(convert_to(effective_text,'UTF8')),'hex') THEN RETURN false; END IF;
 RETURN EXISTS(SELECT 1 FROM projects.declaration_watch_deliveries d
  JOIN projects.declaration_watch_intents i ON i.operation_id=d.operation_id AND i.group_hash=d.group_hash AND i.final_intent AND i.stage='applied'
  JOIN communication.messages m ON m.communication_message_id=d.message_id AND m.node_id=d.node_id AND m.kind='project.watch.reconcile.v1'
  CROSS JOIN LATERAL jsonb_array_elements(m.payload_json#>'{group,roots}') root
  WHERE d.project_id=project AND d.node_id=node AND d.group_hash=adapter->>'group_hash'
  AND root->>'backend_root_key'=expected->>'backend_root_key' AND root->'metadata'=expected->'metadata'
  AND root->>'compiler_config_json'=adapter->>'compiler_config_json' AND root->>'compiler_config_hash'=expected->>'config_hash'
  AND root->'config_json'=effective AND root->>'config_hash'=effective_hash);
EXCEPTION WHEN others THEN RETURN false;
END; $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM projects.declaration_watch_intents) OR EXISTS (SELECT 1 FROM projects.declaration_watch_deliveries) OR EXISTS (SELECT 1 FROM projects.declaration_owner_receipts) OR EXISTS (SELECT 1 FROM projects.project_repository_source_history WHERE project_contract_schema_version='project.contract.v0.5') THEN
  RAISE EXCEPTION 'declaration owner dedup evidence and source history must be preserved';
 END IF;
END; $$;
-- +goose StatementEnd
DROP FUNCTION projects.declaration_watch_binding_matches(jsonb,jsonb,jsonb,text,text,text,text,boolean);
DROP TABLE projects.declaration_watch_deliveries;
DROP TABLE projects.declaration_watch_intents;
DROP FUNCTION projects.guard_declaration_watch_intent();
DROP FUNCTION projects.guard_declaration_watch_delivery();
DROP TABLE projects.declaration_owner_receipts;
DROP FUNCTION projects.guard_declaration_owner_receipt();
DROP FUNCTION projects.validate_declaration_owner_receipt();
-- +goose StatementBegin
DO $$ DECLARE tab text; BEGIN
 FOREACH tab IN ARRAY ARRAY['project_repository_sources','project_repository_source_history'] LOOP
  EXECUTE format('ALTER TABLE projects.%I DROP CONSTRAINT declaration_source_versions',tab);
  EXECUTE format($q$ALTER TABLE projects.%I ADD CHECK (project_contract_schema_version IN ('project.contract.v0.3','project.contract.v0.4')), ADD CHECK (repos_contract_schema_version IN ('repos.contract.v0.3','repos.contract.v0.4'))$q$,tab);
 END LOOP;
END; $$;
-- +goose StatementEnd
