-- +goose Up
CREATE SCHEMA IF NOT EXISTS projects;
CREATE SCHEMA IF NOT EXISTS objects;
CREATE SCHEMA IF NOT EXISTS files;

CREATE TABLE IF NOT EXISTS projects.projects (
    project_id text PRIMARY KEY CHECK (project_id LIKE 'project_%'),
    project_scope_id text UNIQUE NOT NULL REFERENCES scopes.scopes(scope_id),
    slug text UNIQUE NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    owner_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    home_node_id text NULL REFERENCES nodes.nodes(node_id),
    default_workspace_view_id text NULL,
    default_policy_ref text NULL,
    status text NOT NULL CHECK (
        status IN (
            'active',
            'paused',
            'completed',
            'archived',
            'abandoned',
            'blocked'
        )
    ),
    priority text NOT NULL DEFAULT 'normal',
    project_type text NOT NULL DEFAULT 'general',
    indexing_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    backup_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    sharing_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    freshness_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    archive_state jsonb NOT NULL DEFAULT '{}'::jsonb,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION projects.ensure_project_scope_type()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM scopes.scopes
        WHERE scope_id = NEW.project_scope_id
          AND scope_type = 'project'
    ) THEN
        RAISE EXCEPTION 'project_scope_id % must reference a project scope', NEW.project_scope_id;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS projects_projects_project_scope_type_trigger ON projects.projects;
CREATE TRIGGER projects_projects_project_scope_type_trigger
BEFORE INSERT OR UPDATE OF project_scope_id ON projects.projects
FOR EACH ROW
EXECUTE FUNCTION projects.ensure_project_scope_type();

CREATE TABLE IF NOT EXISTS projects.project_memberships (
    project_membership_id text PRIMARY KEY CHECK (project_membership_id LIKE 'project_membership_%'),
    project_id text NOT NULL REFERENCES projects.projects(project_id),
    scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    role text NOT NULL CHECK (
        role IN (
            'owner',
            'maintainer',
            'contributor',
            'viewer',
            'agent',
            'service',
            'auditor'
        )
    ),
    status text NOT NULL CHECK (status IN ('active', 'disabled', 'expired', 'revoked')),
    authorization_hint smallint NULL CHECK (authorization_hint BETWEEN 1 AND 5),
    allowed_action_categories jsonb NOT NULL DEFAULT '{}'::jsonb,
    assigned_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    assigned_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (project_id, actor_id)
);

CREATE TABLE IF NOT EXISTS projects.project_policy_profiles (
    project_policy_profile_id text PRIMARY KEY CHECK (project_policy_profile_id LIKE 'project_policy_%'),
    project_id text UNIQUE NOT NULL REFERENCES projects.projects(project_id),
    visibility text NOT NULL DEFAULT 'private' CHECK (
        visibility IN (
            'private',
            'internal',
            'shared',
            'public_later'
        )
    ),
    allowed_nodes jsonb NOT NULL DEFAULT '{}'::jsonb,
    allowed_actors jsonb NOT NULL DEFAULT '{}'::jsonb,
    allowed_modules jsonb NOT NULL DEFAULT '{}'::jsonb,
    allowed_connectors jsonb NOT NULL DEFAULT '{}'::jsonb,
    capability_visibility jsonb NOT NULL DEFAULT '{}'::jsonb,
    script_workflow_execution jsonb NOT NULL DEFAULT '{}'::jsonb,
    data_classification_defaults jsonb NOT NULL DEFAULT '{}'::jsonb,
    backup_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    indexing_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    external_sharing_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    credential_policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    approval_requirements jsonb NOT NULL DEFAULT '{}'::jsonb,
    export_restrictions jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS projects.workspace_views (
    workspace_view_id text PRIMARY KEY CHECK (workspace_view_id LIKE 'workspace_view_%'),
    project_id text NOT NULL REFERENCES projects.projects(project_id),
    scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    view_kind text NOT NULL DEFAULT 'metadata_only' CHECK (
        view_kind IN (
            'metadata_only',
            'real_folder',
            'generated_folder',
            'synced_mirror',
            'virtual',
            'ui_surface_later'
        )
    ),
    node_id text NULL REFERENCES nodes.nodes(node_id),
    root_path text NULL,
    virtual_path text NULL,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'archived')),
    materialized boolean NOT NULL DEFAULT false,
    created_by_actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    last_checked_at timestamptz NULL,
    last_materialized_at timestamptz NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'projects_projects_default_workspace_view_id_fkey'
    ) THEN
        ALTER TABLE projects.projects
        ADD CONSTRAINT projects_projects_default_workspace_view_id_fkey
        FOREIGN KEY (default_workspace_view_id)
        REFERENCES projects.workspace_views(workspace_view_id);
    END IF;
END;
$$;
-- +goose StatementEnd

CREATE TABLE IF NOT EXISTS files.blobs (
    blob_id text PRIMARY KEY CHECK (blob_id LIKE 'blob_%'),
    hash_algorithm text NOT NULL CHECK (hash_algorithm = 'sha256'),
    hash_hex text NOT NULL CHECK (hash_hex ~ '^[0-9a-f]{64}$'),
    hash_uri text UNIQUE NOT NULL CHECK (hash_uri ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    storage_path text UNIQUE NOT NULL,
    mime_type text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    verified_at timestamptz NULL,
    status text NOT NULL CHECK (status IN ('pending', 'verified', 'missing', 'corrupt')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (hash_algorithm, hash_hex)
);

CREATE TABLE IF NOT EXISTS objects.objects (
    object_id text PRIMARY KEY CHECK (object_id LIKE 'object_%'),
    object_type text NOT NULL CHECK (
        object_type IN (
            'file',
            'artifact',
            'project',
            'note',
            'report',
            'decision',
            'dataset',
            'script',
            'workflow',
            'skill',
            'repo',
            'module',
            'package',
            'external_resource',
            'hardware_device',
            'structured_record'
        )
    ),
    slug text NULL,
    name text NOT NULL,
    owner_actor_id text NULL REFERENCES identity.actors(actor_id),
    home_scope_id text NULL REFERENCES scopes.scopes(scope_id),
    state_class text NOT NULL CHECK (
        state_class IN (
            'canonical',
            'replica',
            'derived',
            'ephemeral',
            'external'
        )
    ),
    status text NOT NULL CHECK (status IN ('active', 'archived', 'failed', 'deleted_later')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS objects.object_versions (
    object_version_id text PRIMARY KEY CHECK (object_version_id LIKE 'version_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    version_number integer NOT NULL CHECK (version_number > 0),
    blob_id text NULL REFERENCES files.blobs(blob_id),
    content_hash text NULL CHECK (content_hash IS NULL OR content_hash ~ '^sha256:[0-9a-f]{64}$'),
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    source_path text NULL,
    created_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_by_job_id text NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    synced_at timestamptz NULL,
    size_bytes bigint NULL CHECK (size_bytes IS NULL OR size_bytes >= 0),
    mime_type text NULL,
    status text NOT NULL CHECK (status IN ('active', 'superseded', 'failed', 'missing')),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (object_id, version_number)
);

CREATE TABLE IF NOT EXISTS objects.object_scope_links (
    object_scope_link_id text PRIMARY KEY CHECK (object_scope_link_id LIKE 'object_scope_link_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    scope_id text NOT NULL REFERENCES scopes.scopes(scope_id),
    relationship_type text NOT NULL CHECK (
        relationship_type IN (
            'primary',
            'relevant',
            'visible_in',
            'owned_by',
            'source_for',
            'artifact_for',
            'temporary',
            'historical'
        )
    ),
    is_primary boolean NOT NULL DEFAULT false,
    relevance_status text NOT NULL DEFAULT 'active' CHECK (relevance_status IN ('active', 'inactive', 'archived')),
    valid_from timestamptz NOT NULL DEFAULT now(),
    valid_until timestamptz NULL,
    created_by_actor_id text NULL REFERENCES identity.actors(actor_id),
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    UNIQUE (object_id, scope_id, relationship_type)
);

CREATE TABLE IF NOT EXISTS files.file_metadata (
    object_id text PRIMARY KEY REFERENCES objects.objects(object_id),
    logical_name text NOT NULL,
    extension text NULL,
    mime_type text NULL,
    source_node_id text NULL REFERENCES nodes.nodes(node_id),
    source_path text NULL,
    source_mtime timestamptz NULL,
    latest_version_id text NULL REFERENCES objects.object_versions(object_version_id),
    text_extractable boolean NOT NULL DEFAULT false,
    raw_backup_policy text NOT NULL DEFAULT 'normal' CHECK (
        raw_backup_policy IN (
            'normal',
            'priority',
            'rate_limited',
            'delayed',
            'private_raw_backup',
            'excluded_temp'
        )
    ),
    index_policy text NOT NULL DEFAULT 'metadata_only' CHECK (
        index_policy IN (
            'none',
            'metadata_only',
            'text_later',
            'semantic_later',
            'private_no_index'
        )
    ),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS files.object_locations (
    object_location_id text PRIMARY KEY CHECK (object_location_id LIKE 'object_location_%'),
    object_id text NOT NULL REFERENCES objects.objects(object_id),
    version_id text NULL REFERENCES objects.object_versions(object_version_id),
    node_id text NOT NULL REFERENCES nodes.nodes(node_id),
    location_type text NOT NULL CHECK (
        location_type IN (
            'object_store',
            'local_filesystem',
            'external_uri'
        )
    ),
    path_or_uri text NOT NULL,
    is_canonical_location boolean NOT NULL DEFAULT false,
    freshness_state text NOT NULL DEFAULT 'fresh' CHECK (
        freshness_state IN (
            'fresh',
            'stale',
            'unknown',
            'live_required'
        )
    ),
    last_verified_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS projects_projects_slug_idx ON projects.projects (slug);
CREATE INDEX IF NOT EXISTS projects_projects_scope_idx ON projects.projects (project_scope_id);
CREATE INDEX IF NOT EXISTS projects_project_memberships_actor_idx ON projects.project_memberships (actor_id);
CREATE INDEX IF NOT EXISTS projects_workspace_views_project_idx ON projects.workspace_views (project_id);

CREATE INDEX IF NOT EXISTS objects_objects_home_scope_idx ON objects.objects (home_scope_id);
CREATE INDEX IF NOT EXISTS objects_objects_type_status_idx ON objects.objects (object_type, status);
CREATE INDEX IF NOT EXISTS objects_object_versions_object_idx ON objects.object_versions (object_id, version_number DESC);
CREATE INDEX IF NOT EXISTS objects_object_versions_blob_idx ON objects.object_versions (blob_id);
CREATE INDEX IF NOT EXISTS objects_object_scope_links_scope_idx ON objects.object_scope_links (scope_id);
CREATE INDEX IF NOT EXISTS objects_object_scope_links_object_idx ON objects.object_scope_links (object_id);
CREATE INDEX IF NOT EXISTS objects_object_scope_links_primary_idx ON objects.object_scope_links (object_id, is_primary);

CREATE INDEX IF NOT EXISTS files_blobs_hash_uri_idx ON files.blobs (hash_uri);
CREATE INDEX IF NOT EXISTS files_file_metadata_latest_version_idx ON files.file_metadata (latest_version_id);
CREATE INDEX IF NOT EXISTS files_object_locations_object_idx ON files.object_locations (object_id);
CREATE INDEX IF NOT EXISTS files_object_locations_version_idx ON files.object_locations (version_id);
CREATE INDEX IF NOT EXISTS files_object_locations_node_idx ON files.object_locations (node_id);

-- +goose Down
ALTER TABLE projects.projects DROP CONSTRAINT IF EXISTS projects_projects_default_workspace_view_id_fkey;

DROP TABLE IF EXISTS files.object_locations;
DROP TABLE IF EXISTS files.file_metadata;
DROP TABLE IF EXISTS objects.object_scope_links;
DROP TABLE IF EXISTS objects.object_versions;
DROP TABLE IF EXISTS objects.objects;
DROP TABLE IF EXISTS files.blobs;
DROP TABLE IF EXISTS projects.workspace_views;
DROP TABLE IF EXISTS projects.project_policy_profiles;
DROP TABLE IF EXISTS projects.project_memberships;
DROP TRIGGER IF EXISTS projects_projects_project_scope_type_trigger ON projects.projects;
DROP FUNCTION IF EXISTS projects.ensure_project_scope_type();
DROP TABLE IF EXISTS projects.projects;

DROP SCHEMA IF EXISTS files;
DROP SCHEMA IF EXISTS objects;
DROP SCHEMA IF EXISTS projects;
