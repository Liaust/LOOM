-- +goose Up
CREATE SCHEMA IF NOT EXISTS identity;
CREATE SCHEMA IF NOT EXISTS nodes;
CREATE SCHEMA IF NOT EXISTS scopes;
CREATE SCHEMA IF NOT EXISTS events;

CREATE TABLE IF NOT EXISTS identity.actors (
    actor_id text PRIMARY KEY CHECK (actor_id LIKE 'actor_%'),
    actor_key text UNIQUE NOT NULL,
    display_name text NOT NULL,
    actor_kind text NOT NULL CHECK (
        actor_kind IN (
            'human',
            'agent',
            'service',
            'scheduler',
            'workflow_run',
            'external_integration'
        )
    ),
    home_node_id text NULL,
    default_scope_id text NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'revoked')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz NULL,
    revoked_reason text NULL
);

CREATE TABLE IF NOT EXISTS nodes.nodes (
    node_id text PRIMARY KEY CHECK (node_id LIKE 'node_%'),
    node_key text UNIQUE NOT NULL,
    display_name text NOT NULL,
    node_kind text NOT NULL,
    node_role text NOT NULL,
    runtime_class text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'retired', 'quarantined')),
    owner_actor_id text NULL REFERENCES identity.actors(actor_id),
    home_scope_id text NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz NULL
);

CREATE TABLE IF NOT EXISTS identity.actor_node_authorizations (
    authorization_id text PRIMARY KEY,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    authorization_level smallint NOT NULL CHECK (authorization_level BETWEEN 1 AND 5),
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'expired', 'revoked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (actor_id, node_id)
);

CREATE TABLE IF NOT EXISTS scopes.scope_types (
    scope_type text PRIMARY KEY,
    description text NOT NULL,
    default_visibility text NOT NULL,
    allows_workspace_view boolean NOT NULL,
    allows_mounts boolean NOT NULL,
    allows_object_links boolean NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

INSERT INTO scopes.scope_types (
    scope_type,
    description,
    default_visibility,
    allows_workspace_view,
    allows_mounts,
    allows_object_links,
    metadata
)
VALUES
    ('system', 'System-wide LOOM scope.', 'internal', false, false, false, '{}'::jsonb),
    ('project', 'Persistent human-led workspace scope.', 'internal', true, true, true, '{}'::jsonb),
    ('node', 'Scope owned by a LOOM node.', 'internal', false, false, true, '{}'::jsonb),
    ('actor', 'Scope owned by an actor.', 'private', false, false, true, '{}'::jsonb),
    ('job', 'Execution/job context scope.', 'internal', false, false, true, '{}'::jsonb),
    ('module', 'Scope for a LOOM-owned module installation.', 'internal', true, true, true, '{}'::jsonb),
    ('inbox', 'Inbox/intake scope.', 'internal', true, false, true, '{}'::jsonb),
    ('archive', 'Archived context scope.', 'internal', false, false, true, '{}'::jsonb),
    ('collection', 'General collection scope.', 'internal', true, false, true, '{}'::jsonb),
    ('package', 'Package scope for scripts, workflows, and skills.', 'internal', false, false, true, '{}'::jsonb),
    ('external_service', 'Context scope for wrapped external services.', 'internal', false, true, true, '{}'::jsonb)
ON CONFLICT (scope_type) DO NOTHING;

CREATE TABLE IF NOT EXISTS scopes.scopes (
    scope_id text PRIMARY KEY CHECK (scope_id LIKE 'scope_%'),
    scope_type text NOT NULL REFERENCES scopes.scope_types(scope_type),
    scope_key text UNIQUE NOT NULL,
    slug text NOT NULL,
    display_name text NOT NULL,
    owner_actor_id text NULL REFERENCES identity.actors(actor_id),
    home_node_id text NULL REFERENCES nodes.nodes(node_id),
    parent_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    status text NOT NULL CHECK (status IN ('active', 'paused', 'archived', 'disabled', 'deleted_later')),
    created_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (scope_type, slug)
);

CREATE TABLE IF NOT EXISTS events.events (
    event_id text PRIMARY KEY CHECK (event_id LIKE 'event_%'),
    event_type text NOT NULL CHECK (position('.' in event_type) > 0),
    event_level text NOT NULL CHECK (
        event_level IN (
            'local_debug',
            'node_activity',
            'audit',
            'summary',
            'critical'
        )
    ),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    origin_node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    scope_id text NULL REFERENCES scopes.scopes(scope_id),
    target_kind text NULL,
    target_id text NULL,
    correlation_id text NULL,
    route_id text NULL,
    job_id text NULL,
    status text NULL,
    result text NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    visibility_class text NOT NULL CHECK (
        visibility_class IN (
            'internal',
            'private',
            'audit',
            'security',
            'public_later'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS events_events_created_at_desc_idx ON events.events (created_at DESC);
CREATE INDEX IF NOT EXISTS events_events_event_type_idx ON events.events (event_type);
CREATE INDEX IF NOT EXISTS events_events_actor_id_idx ON events.events (actor_id);
CREATE INDEX IF NOT EXISTS events_events_origin_node_id_idx ON events.events (origin_node_id);
CREATE INDEX IF NOT EXISTS events_events_scope_id_idx ON events.events (scope_id);
CREATE INDEX IF NOT EXISTS events_events_correlation_id_idx ON events.events (correlation_id);
CREATE INDEX IF NOT EXISTS events_events_target_idx ON events.events (target_kind, target_id);

-- +goose Down
DROP TABLE IF EXISTS events.events;
DROP TABLE IF EXISTS scopes.scopes;
DROP TABLE IF EXISTS scopes.scope_types;
DROP TABLE IF EXISTS identity.actor_node_authorizations;
DROP TABLE IF EXISTS nodes.nodes;
DROP TABLE IF EXISTS identity.actors;

DROP SCHEMA IF EXISTS events;
DROP SCHEMA IF EXISTS scopes;
DROP SCHEMA IF EXISTS nodes;
DROP SCHEMA IF EXISTS identity;
