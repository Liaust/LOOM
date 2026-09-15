-- +goose Up
CREATE TABLE projects.physical_archive_plan_evidence (
    operation_id text PRIMARY KEY CHECK (operation_id ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$'),
    project_id text NOT NULL REFERENCES projects.projects(project_id),
    operation_kind text NOT NULL CHECK (operation_kind IN ('archive', 'restore')),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    request_json jsonb NOT NULL CHECK (jsonb_typeof(request_json) = 'object'),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    payload_sha256 text NOT NULL CHECK (payload_sha256 ~ '^sha256:[0-9a-f]{64}$'),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 2 AND 134217728),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK (payload_sha256 = 'sha256:' || encode(sha256(payload), 'hex')),
    CHECK (request_json->>'actor_id' IS NOT DISTINCT FROM actor_id
        AND request_json->>'origin_node_id' IS NOT DISTINCT FROM origin_node_id
        AND request_json->>'scope_id' IS NOT DISTINCT FROM scope_id),
    CHECK (convert_from(payload, 'UTF8')::jsonb->>'plan_digest' IS NOT DISTINCT FROM plan_digest
        AND convert_from(payload, 'UTF8')::jsonb->'request' IS NOT DISTINCT FROM request_json
        AND convert_from(payload, 'UTF8')::jsonb#>>'{workspace,operation_id}' IS NOT DISTINCT FROM operation_id
        AND convert_from(payload, 'UTF8')::jsonb#>>'{workspace,operation_kind}' IS NOT DISTINCT FROM operation_kind
        AND convert_from(payload, 'UTF8')::jsonb#>>'{workspace,object_id}' IS NOT DISTINCT FROM project_id
        AND convert_from(payload, 'UTF8')::jsonb->>'schema_version' IS NOT DISTINCT FROM
            'storage.project_physical_' || operation_kind || '_plan.v1')
);

-- Private recovery evidence is append-only, not public project metadata.
REVOKE ALL ON projects.physical_archive_plan_evidence FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION projects.reject_physical_archive_plan_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'project archive plan evidence is immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER physical_archive_plan_evidence_immutable
BEFORE UPDATE OR DELETE ON projects.physical_archive_plan_evidence
FOR EACH ROW EXECUTE FUNCTION projects.reject_physical_archive_plan_change();

-- +goose Down
DROP TABLE projects.physical_archive_plan_evidence;
DROP FUNCTION projects.reject_physical_archive_plan_change();
