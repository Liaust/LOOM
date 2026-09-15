-- +goose Up

CREATE SCHEMA IF NOT EXISTS agents;

CREATE TABLE IF NOT EXISTS agents.agent_access_sessions (
    agent_access_session_id text PRIMARY KEY CHECK (agent_access_session_id LIKE 'agent_access_session_%'),
    access_session_key text UNIQUE NOT NULL CHECK (length(access_session_key) > 0),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_kind text NOT NULL DEFAULT 'local_cli' CHECK (
        origin_kind IN ('local_cli', 'remote_api', 'external_client', 'service', 'manual')
    ),
    origin_node_id text NULL REFERENCES nodes.nodes(node_id),
    origin_client_id text NOT NULL DEFAULT '',
    runtime_node_id text NULL REFERENCES nodes.nodes(node_id),
    home_node_id text NULL REFERENCES nodes.nodes(node_id),
    current_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    active_project_id text NULL REFERENCES projects.projects(project_id),
    status text NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'completed', 'cancelled', 'failed', 'expired', 'revoked')
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NULL,
    closed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS agents_access_sessions_actor_status_idx ON agents.agent_access_sessions (actor_id, status);
CREATE INDEX IF NOT EXISTS agents_access_sessions_runtime_node_idx ON agents.agent_access_sessions (runtime_node_id);
CREATE INDEX IF NOT EXISTS agents_access_sessions_scope_idx ON agents.agent_access_sessions (current_scope_id);
CREATE INDEX IF NOT EXISTS agents_access_sessions_created_at_desc_idx ON agents.agent_access_sessions (created_at DESC);

CREATE TABLE IF NOT EXISTS agents.agent_work_contexts (
    agent_work_context_id text PRIMARY KEY CHECK (agent_work_context_id LIKE 'agent_work_context_%'),
    work_context_key text UNIQUE NOT NULL CHECK (length(work_context_key) > 0),
    agent_access_session_id text NOT NULL REFERENCES agents.agent_access_sessions(agent_access_session_id) ON DELETE CASCADE,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    objective text NOT NULL CHECK (length(objective) > 0),
    current_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    active_project_id text NULL REFERENCES projects.projects(project_id),
    runtime_node_id text NULL REFERENCES nodes.nodes(node_id),
    home_node_id text NULL REFERENCES nodes.nodes(node_id),
    active_tool_view_id text NULL,
    status text NOT NULL DEFAULT 'active' CHECK (
        status IN ('active', 'completed', 'cancelled', 'failed', 'expired', 'revoked')
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS agents_work_contexts_session_status_idx ON agents.agent_work_contexts (agent_access_session_id, status);
CREATE INDEX IF NOT EXISTS agents_work_contexts_actor_status_idx ON agents.agent_work_contexts (actor_id, status);
CREATE INDEX IF NOT EXISTS agents_work_contexts_scope_idx ON agents.agent_work_contexts (current_scope_id);
CREATE INDEX IF NOT EXISTS agents_work_contexts_created_at_desc_idx ON agents.agent_work_contexts (created_at DESC);

CREATE TABLE IF NOT EXISTS agents.tool_views (
    tool_view_id text PRIMARY KEY CHECK (tool_view_id LIKE 'tool_view_%'),
    agent_access_session_id text NULL REFERENCES agents.agent_access_sessions(agent_access_session_id) ON DELETE CASCADE,
    agent_work_context_id text NULL REFERENCES agents.agent_work_contexts(agent_work_context_id) ON DELETE CASCADE,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    runtime_node_id text NULL REFERENCES nodes.nodes(node_id),
    home_node_id text NULL REFERENCES nodes.nodes(node_id),
    current_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    active_project_id text NULL REFERENCES projects.projects(project_id),
    version integer NOT NULL DEFAULT 1 CHECK (version > 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'invalidated', 'expired')),
    max_entries integer NOT NULL DEFAULT 40 CHECK (max_entries > 0 AND max_entries <= 200),
    schema_budget integer NOT NULL DEFAULT 12 CHECK (schema_budget >= 0 AND schema_budget <= 100),
    direct_capability_schema_budget integer NOT NULL DEFAULT 4 CHECK (direct_capability_schema_budget >= 0 AND direct_capability_schema_budget <= 100),
    source_hash text NOT NULL CHECK (length(source_hash) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    invalidated_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object'),
    CHECK (agent_access_session_id IS NOT NULL OR agent_work_context_id IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS agents_tool_views_session_status_idx ON agents.tool_views (agent_access_session_id, status);
CREATE INDEX IF NOT EXISTS agents_tool_views_work_context_status_idx ON agents.tool_views (agent_work_context_id, status);
CREATE INDEX IF NOT EXISTS agents_tool_views_actor_status_idx ON agents.tool_views (actor_id, status);
CREATE INDEX IF NOT EXISTS agents_tool_views_created_at_desc_idx ON agents.tool_views (created_at DESC);

ALTER TABLE agents.agent_work_contexts
    ADD CONSTRAINT agents_work_contexts_active_tool_view_fkey
    FOREIGN KEY (active_tool_view_id) REFERENCES agents.tool_views(tool_view_id);

CREATE TABLE IF NOT EXISTS agents.tool_view_entries (
    tool_view_entry_id text PRIMARY KEY CHECK (tool_view_entry_id LIKE 'tool_view_entry_%'),
    tool_view_id text NOT NULL REFERENCES agents.tool_views(tool_view_id) ON DELETE CASCADE,
    entry_kind text NOT NULL CHECK (entry_kind IN ('operating_tool', 'capability')),
    tool_name text NOT NULL CHECK (length(tool_name) > 0),
    visibility_state text NOT NULL CHECK (visibility_state IN ('visible', 'requestable', 'redacted')),
    source_layer text NOT NULL CHECK (source_layer IN ('operating', 'home', 'current_scope', 'mounted', 'task_mount', 'grant', 'search_candidate')),
    reason_code text NOT NULL CHECK (length(reason_code) > 0),
    capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    capability_address text NOT NULL DEFAULT '',
    provider_id text NULL REFERENCES capabilities.providers(provider_id),
    provider_address text NOT NULL DEFAULT '',
    target_node_id text NULL REFERENCES nodes.nodes(node_id),
    display_name text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    risk_level text NULL CHECK (risk_level IS NULL OR risk_level IN ('low', 'medium', 'high', 'critical')),
    execution_authorization_level integer NULL CHECK (
        execution_authorization_level IS NULL OR execution_authorization_level BETWEEN 1 AND 5
    ),
    actor_authorization_level integer NULL CHECK (
        actor_authorization_level IS NULL OR actor_authorization_level BETWEEN 1 AND 5
    ),
    approval_hint text NOT NULL DEFAULT '',
    availability_hint text NOT NULL DEFAULT '',
    matched_use_summary text NOT NULL DEFAULT '',
    compact_metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(compact_metadata) = 'object'),
    schema_summary_json jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(schema_summary_json) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tool_view_id, tool_name),
    CHECK (
        (entry_kind = 'operating_tool' AND capability_endpoint_id IS NULL)
        OR
        (entry_kind = 'capability' AND capability_endpoint_id IS NOT NULL AND capability_address <> '')
    )
);

CREATE INDEX IF NOT EXISTS agents_tool_view_entries_view_kind_idx ON agents.tool_view_entries (tool_view_id, entry_kind);
CREATE INDEX IF NOT EXISTS agents_tool_view_entries_visibility_idx ON agents.tool_view_entries (visibility_state);
CREATE INDEX IF NOT EXISTS agents_tool_view_entries_capability_idx ON agents.tool_view_entries (capability_endpoint_id);

CREATE TABLE IF NOT EXISTS agents.tool_calls (
    agent_tool_call_id text PRIMARY KEY CHECK (agent_tool_call_id LIKE 'agent_tool_call_%'),
    agent_access_session_id text NULL REFERENCES agents.agent_access_sessions(agent_access_session_id) ON DELETE SET NULL,
    agent_work_context_id text NULL REFERENCES agents.agent_work_contexts(agent_work_context_id) ON DELETE SET NULL,
    tool_view_id text NULL REFERENCES agents.tool_views(tool_view_id) ON DELETE SET NULL,
    tool_view_entry_id text NULL REFERENCES agents.tool_view_entries(tool_view_entry_id) ON DELETE SET NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    tool_name text NOT NULL CHECK (length(tool_name) > 0),
    capability_endpoint_id text NULL REFERENCES capabilities.capability_endpoints(capability_endpoint_id),
    capability_address text NOT NULL DEFAULT '',
    route_id text NULL REFERENCES routing.routes(route_id),
    capability_call_id text NULL REFERENCES routing.capability_calls(capability_call_id),
    policy_decision_id text NULL REFERENCES policy.decisions(policy_decision_id),
    approval_id text NULL REFERENCES policy.approvals(approval_id),
    grant_id text NULL REFERENCES policy.grants(grant_id),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) > 0),
    status text NOT NULL CHECK (
        status IN ('planned', 'approval_required', 'dispatched', 'completed', 'failed', 'denied')
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS agents_tool_calls_work_context_status_idx ON agents.tool_calls (agent_work_context_id, status);
CREATE INDEX IF NOT EXISTS agents_tool_calls_actor_created_idx ON agents.tool_calls (actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS agents_tool_calls_capability_call_idx ON agents.tool_calls (capability_call_id);
CREATE UNIQUE INDEX IF NOT EXISTS agents_tool_calls_idempotency_idx
    ON agents.tool_calls (COALESCE(agent_work_context_id, ''), tool_name, idempotency_key);

CREATE TABLE IF NOT EXISTS agents.worklog_entries (
    worklog_entry_id text PRIMARY KEY CHECK (worklog_entry_id LIKE 'worklog_entry_%'),
    agent_access_session_id text NULL REFERENCES agents.agent_access_sessions(agent_access_session_id) ON DELETE SET NULL,
    agent_work_context_id text NOT NULL REFERENCES agents.agent_work_contexts(agent_work_context_id) ON DELETE CASCADE,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    entry_kind text NOT NULL CHECK (entry_kind IN ('note', 'observation', 'decision', 'result', 'artifact_ref')),
    summary text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    object_id text NULL REFERENCES objects.objects(object_id),
    visibility_class text NOT NULL DEFAULT 'internal' CHECK (
        visibility_class IN ('internal', 'private', 'audit', 'security', 'public_later')
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE INDEX IF NOT EXISTS agents_worklog_entries_work_context_created_idx ON agents.worklog_entries (agent_work_context_id, created_at DESC);
CREATE INDEX IF NOT EXISTS agents_worklog_entries_actor_created_idx ON agents.worklog_entries (actor_id, created_at DESC);

-- +goose Down

DROP TABLE IF EXISTS agents.worklog_entries;
DROP INDEX IF EXISTS agents_tool_calls_idempotency_idx;
DROP TABLE IF EXISTS agents.tool_calls;
DROP TABLE IF EXISTS agents.tool_view_entries;
ALTER TABLE IF EXISTS agents.agent_work_contexts DROP CONSTRAINT IF EXISTS agents_work_contexts_active_tool_view_fkey;
DROP TABLE IF EXISTS agents.tool_views;
DROP TABLE IF EXISTS agents.agent_work_contexts;
DROP TABLE IF EXISTS agents.agent_access_sessions;
DROP SCHEMA IF EXISTS agents;
