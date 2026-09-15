-- +goose Up
-- Durable operation journal, NOT jobs.jobs and NOT a second project registry.
-- project_id may not yet exist: register_project intent must precede its owner
-- commit. Identity is exact and all already-existing actor/node references use
-- FKs. Retain operation/action dedup evidence beyond HTTP idempotency expiry.
CREATE TABLE projects.declaration_operations (
    operation_id text PRIMARY KEY CHECK (operation_id ~ '^job_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id) ON DELETE RESTRICT
        CHECK (actor_id ~ '^actor_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT
        CHECK (origin_node_id ~ '^node_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    project_id text NOT NULL CHECK (project_id ~ '^project_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    target_node_id text NOT NULL REFERENCES nodes.nodes(node_id) ON DELETE RESTRICT
        CHECK (target_node_id ~ '^node_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    project_root text NOT NULL CHECK (project_root LIKE '/%' AND length(project_root)>1),
    location_revision text NOT NULL CHECK (length(location_revision)>0),
    operation_kind text NOT NULL CHECK (operation_kind = 'declaration_apply'),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 256),
    request_hash text NOT NULL CHECK (request_hash ~ '^sha256:[0-9a-f]{64}$'),
    plan_id text NOT NULL CHECK (plan_id ~ '^sha256:[0-9a-f]{64}$'),
    request jsonb NOT NULL CHECK (jsonb_typeof(request)='object'),
    resolution jsonb NOT NULL CHECK (jsonb_typeof(resolution)='object'),
    state text NOT NULL CHECK (state IN ('queued','running','partial','succeeded','failed','superseded')),
    errors jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(errors)='array'),
    superseded_by text REFERENCES projects.declaration_operations(operation_id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (actor_id, origin_node_id, project_id, operation_kind, idempotency_key),
    CHECK (superseded_by IS DISTINCT FROM operation_id),
    CHECK ((state='superseded') = (superseded_by IS NOT NULL)),
    CHECK ((resolution #>> '{plan,schema_version}') IS NOT DISTINCT FROM 'project.declaration_plan.v0.5'),
    CHECK ((resolution #>> '{plan,plan_id}') IS NOT DISTINCT FROM plan_id),
    CHECK ((resolution #>> '{plan,basis,target,project_id}') IS NOT DISTINCT FROM project_id),
    CHECK ((resolution #>> '{plan,basis,target,owner_node_id}') IS NOT DISTINCT FROM target_node_id),
    CHECK ((resolution #>> '{plan,basis,target,project_root}') IS NOT DISTINCT FROM project_root),
    CHECK ((resolution #>> '{plan,basis,target,location_revision}') IS NOT DISTINCT FROM location_revision),
    CHECK (jsonb_typeof(resolution #> '{plan,basis,sources}') IS NOT DISTINCT FROM 'array'),
    CHECK (jsonb_typeof(resolution #> '{plan,basis,revisions}') IS NOT DISTINCT FROM 'object'),
    CHECK (jsonb_typeof(resolution #> '{plan,basis,bindings}') IS NOT DISTINCT FROM 'object'),
    CHECK (jsonb_typeof(resolution #> '{plan,basis,effects}') IS NOT DISTINCT FROM 'array'),
    CHECK (jsonb_typeof(resolution #> '{plan,basis,actions}') IS NOT DISTINCT FROM 'array'),
    CHECK (jsonb_typeof(resolution->'payloads') IS NOT DISTINCT FROM 'object'),
    CHECK ((request->>'schema_version') IS NOT DISTINCT FROM 'project.declaration_request.v0.5'),
    CHECK ((request->>'plan_id') IS NOT DISTINCT FROM plan_id),
    CHECK ((request->>'idempotency_key') IS NOT DISTINCT FROM idempotency_key),
    CHECK ((request->'effects') IS NOT DISTINCT FROM (resolution #> '{plan,basis,effects}'))
);
CREATE INDEX declaration_operations_project_idx ON projects.declaration_operations(project_id, created_at DESC, operation_id);
CREATE TABLE projects.declaration_action_receipts (
    operation_id text NOT NULL REFERENCES projects.declaration_operations(operation_id) ON DELETE RESTRICT,
    action_id text NOT NULL CHECK (action_id ~ '^[a-z_]+:[a-z][a-z0-9_-]*(:[a-z]+)?$'),
    owner text NOT NULL CHECK (owner IN ('projects','knowledge','backupcontracts','serviceregistry','notesprojection','provenance')),
    input_hash text NOT NULL CHECK (input_hash ~ '^sha256:[0-9a-f]{64}$'),
    token text NOT NULL CHECK (token ~ '^sha256:[0-9a-f]{64}$'),
    state text NOT NULL CHECK (state IN ('queued','running','partial','succeeded','failed')),
    receipt jsonb CHECK (jsonb_typeof(receipt)='object'),
    error jsonb CHECK (jsonb_typeof(error)='object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (operation_id, action_id),
    UNIQUE (operation_id, token),
    CHECK ((state='succeeded') = (receipt IS NOT NULL)),
    CHECK (receipt IS NULL OR (
        (receipt->>'token') IS NOT DISTINCT FROM token AND
        (receipt->>'action_id') IS NOT DISTINCT FROM action_id AND
        (receipt->>'owner') IS NOT DISTINCT FROM owner AND
        (receipt->>'input_hash') IS NOT DISTINCT FROM input_hash AND
        (length(receipt->>'effect_ref')>0) IS TRUE AND
        jsonb_typeof(receipt->'revisions') IS NOT DISTINCT FROM 'object' AND
        jsonb_typeof(receipt->'bindings') IS NOT DISTINCT FROM 'object'
    ))
);
-- Keep immutable identity and committed receipts even if an operator accidentally
-- attempts to rewrite operation inputs or remove replay evidence.
-- +goose StatementBegin
CREATE FUNCTION projects.guard_declaration_journal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='DELETE' THEN RAISE EXCEPTION 'declaration journal evidence must be retained'; END IF;
    IF TG_TABLE_NAME='declaration_operations' THEN
        IF (to_jsonb(OLD)-ARRAY['state','errors','superseded_by','updated_at']) IS DISTINCT FROM
           (to_jsonb(NEW)-ARRAY['state','errors','superseded_by','updated_at']) THEN
            RAISE EXCEPTION 'declaration operation identity is immutable';
        END IF;
        IF OLD.state IN ('succeeded','superseded') AND NEW IS DISTINCT FROM OLD THEN
            RAISE EXCEPTION 'terminal declaration operation is immutable';
        END IF;
        IF NEW.superseded_by IS NOT NULL AND NOT EXISTS (
            SELECT 1 FROM projects.declaration_operations o
            WHERE o.operation_id=NEW.superseded_by AND o.project_id=NEW.project_id
        ) THEN RAISE EXCEPTION 'superseding declaration project mismatch'; END IF;
    ELSE
        IF (to_jsonb(OLD)-ARRAY['state','receipt','error','updated_at']) IS DISTINCT FROM
           (to_jsonb(NEW)-ARRAY['state','receipt','error','updated_at']) THEN
            RAISE EXCEPTION 'declaration action identity is immutable';
        END IF;
        IF OLD.state='succeeded' AND NEW IS DISTINCT FROM OLD THEN
            RAISE EXCEPTION 'committed declaration receipt is immutable';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER declaration_operation_guard BEFORE UPDATE OR DELETE ON projects.declaration_operations
FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_journal();
CREATE TRIGGER declaration_receipt_guard BEFORE UPDATE OR DELETE ON projects.declaration_action_receipts
FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_journal();
REVOKE ALL ON projects.declaration_operations, projects.declaration_action_receipts FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM projects.declaration_operations) THEN
        RAISE EXCEPTION 'declaration operation dedup evidence must be preserved; downgrade refused';
    END IF;
END; $$;
-- +goose StatementEnd
DROP TABLE projects.declaration_action_receipts;
DROP TABLE projects.declaration_operations;
DROP FUNCTION projects.guard_declaration_journal();
