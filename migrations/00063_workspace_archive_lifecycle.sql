-- +goose Up
CREATE TABLE IF NOT EXISTS storage.workspace_archive_operations (
    workspace_archive_operation_id text PRIMARY KEY CHECK (
        workspace_archive_operation_id ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
    ),
    schema_version text NOT NULL CHECK (schema_version = 'storage.workspace_archive_operation.v1'),
    evidence_kind text NOT NULL CHECK (evidence_kind = 'physical_workspace_move'),
    operation_kind text NOT NULL CHECK (operation_kind IN ('archive', 'restore')),
    workspace_kind text NOT NULL CHECK (workspace_kind IN ('topic', 'project', 'library_item')),
    object_id text NOT NULL CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$'),
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    phase text NOT NULL CHECK (phase IN (
        'archive_planned',
        'archive_intent_committed',
        'archive_payload_moved',
        'archive_projections_committed',
        'archive_complete',
        'restore_planned',
        'restore_intent_committed',
        'restore_payload_moved',
        'restore_projections_committed',
        'restore_complete',
        'blocked',
        'manual_repair_required'
    )),
    last_safe_phase text NULL CHECK (last_safe_phase IS NULL OR last_safe_phase IN (
        'archive_planned',
        'archive_intent_committed',
        'archive_payload_moved',
        'archive_projections_committed',
        'restore_planned',
        'restore_intent_committed',
        'restore_payload_moved',
        'restore_projections_committed'
    )),
    terminal_status text NOT NULL CHECK (terminal_status IN (
        'pending', 'running', 'complete', 'blocked', 'manual_repair'
    )),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    plan_json jsonb NOT NULL CHECK (
        jsonb_typeof(plan_json) = 'object'
        AND plan_json->>'schema_version' IS NOT DISTINCT FROM 'storage.workspace_archive_plan.v1'
        AND plan_json->>'evidence_kind' IS NOT DISTINCT FROM 'physical_workspace_move'
        AND plan_json->>'operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id
        AND plan_json->>'operation_kind' IS NOT DISTINCT FROM operation_kind
        AND plan_json->>'kind' IS NOT DISTINCT FROM workspace_kind
        AND plan_json->>'object_id' IS NOT DISTINCT FROM object_id
        AND plan_json->>'slug' IS NOT DISTINCT FROM slug
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
        AND plan_json->>'actor_id' IS NOT DISTINCT FROM actor_id
        AND plan_json->>'reason' IS NOT DISTINCT FROM reason
        AND (plan_json->>'planned_at')::timestamptz IS NOT DISTINCT FROM planned_at
        AND octet_length(plan_json::text) <= 268435456
    ),
    source_root text NOT NULL CHECK (source_root IN ('box', 'storage')),
    source_relative_path text NOT NULL,
    source_identity_json jsonb NOT NULL CHECK (
        jsonb_typeof(source_identity_json) = 'object'
        AND source_identity_json->>'presence' IS NOT DISTINCT FROM 'present'
    ),
    source_parent_identity_json jsonb NOT NULL CHECK (
        jsonb_typeof(source_parent_identity_json) = 'object'
        AND source_parent_identity_json->>'presence' IS NOT DISTINCT FROM 'present'
    ),
    destination_root text NOT NULL CHECK (destination_root IN ('box', 'storage')),
    destination_relative_path text NOT NULL,
    destination_identity_json jsonb NOT NULL CHECK (
        jsonb_typeof(destination_identity_json) = 'object'
        AND destination_identity_json->>'presence' IS NOT DISTINCT FROM 'absent'
    ),
    destination_parent_identity_json jsonb NOT NULL CHECK (
        jsonb_typeof(destination_parent_identity_json) = 'object'
        AND destination_parent_identity_json->>'presence' IS NOT DISTINCT FROM 'present'
    ),
    inventory_digest text NOT NULL CHECK (inventory_digest ~ '^sha256:[0-9a-f]{64}$'),
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    reason text NOT NULL CHECK (reason = btrim(reason) AND octet_length(reason) BETWEEN 1 AND 1024),
    planned_at timestamptz NOT NULL,
    intent_committed_at timestamptz NULL,
    payload_moved_at timestamptz NULL,
    projections_committed_at timestamptz NULL,
    completed_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        plan_digest
    ),
    UNIQUE (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        source_root,
        source_relative_path,
        destination_root,
        destination_relative_path,
        actor_id,
        reason
    ),
    CHECK (
        (operation_kind = 'archive' AND source_root = 'box' AND destination_root = 'storage')
        OR
        (operation_kind = 'restore' AND source_root = 'storage' AND destination_root = 'box')
    ),
    CHECK (
        source_relative_path = CASE
            WHEN operation_kind = 'archive' THEN CASE workspace_kind
                WHEN 'topic' THEN 'Topics/' || slug
                WHEN 'project' THEN 'Projects/' || slug
                WHEN 'library_item' THEN 'Library/' || slug
            END
            ELSE CASE workspace_kind
                WHEN 'topic' THEN 'archive/topics/' || slug || '/content'
                WHEN 'project' THEN 'archive/projects/' || slug || '/project'
                WHEN 'library_item' THEN 'archive/library/' || slug || '/content'
            END
        END
    ),
    CHECK (
        destination_relative_path = CASE
            WHEN operation_kind = 'archive' THEN CASE workspace_kind
                WHEN 'topic' THEN 'archive/topics/' || slug || '/content'
                WHEN 'project' THEN 'archive/projects/' || slug || '/project'
                WHEN 'library_item' THEN 'archive/library/' || slug || '/content'
            END
            ELSE CASE workspace_kind
                WHEN 'topic' THEN 'Topics/' || slug
                WHEN 'project' THEN 'Projects/' || slug
                WHEN 'library_item' THEN 'Library/' || slug
            END
        END
    ),
    CHECK (
        (phase IN ('blocked', 'manual_repair_required') AND last_safe_phase IS NOT NULL)
        OR
        (phase NOT IN ('blocked', 'manual_repair_required') AND last_safe_phase IS NULL)
    ),
    CHECK (
        (operation_kind = 'archive' AND COALESCE(last_safe_phase, phase) LIKE 'archive_%')
        OR
        (operation_kind = 'restore' AND COALESCE(last_safe_phase, phase) LIKE 'restore_%')
    ),
    CHECK (
        (phase IN ('archive_planned', 'restore_planned') AND terminal_status = 'pending')
        OR
        (phase IN (
            'archive_intent_committed', 'archive_payload_moved', 'archive_projections_committed',
            'restore_intent_committed', 'restore_payload_moved', 'restore_projections_committed'
        ) AND terminal_status = 'running')
        OR
        (phase IN ('archive_complete', 'restore_complete') AND terminal_status = 'complete')
        OR
        (phase = 'blocked' AND terminal_status = 'blocked')
        OR
        (phase = 'manual_repair_required' AND terminal_status = 'manual_repair')
    ),
    CHECK (
        CASE COALESCE(last_safe_phase, phase)
            WHEN 'archive_planned' THEN
                intent_committed_at IS NULL AND payload_moved_at IS NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'restore_planned' THEN
                intent_committed_at IS NULL AND payload_moved_at IS NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'archive_intent_committed' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'restore_intent_committed' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'archive_payload_moved' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'restore_payload_moved' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NULL AND completed_at IS NULL
            WHEN 'archive_projections_committed' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NOT NULL AND completed_at IS NULL
            WHEN 'restore_projections_committed' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NOT NULL AND completed_at IS NULL
            WHEN 'archive_complete' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NOT NULL AND completed_at IS NOT NULL
            WHEN 'restore_complete' THEN
                intent_committed_at IS NOT NULL AND payload_moved_at IS NOT NULL
                AND projections_committed_at IS NOT NULL AND completed_at IS NOT NULL
            ELSE false
        END
    ),
    CHECK (
        (intent_committed_at IS NULL OR intent_committed_at >= planned_at)
        AND (payload_moved_at IS NULL OR payload_moved_at >= intent_committed_at)
        AND (projections_committed_at IS NULL OR projections_committed_at >= payload_moved_at)
        AND (completed_at IS NULL OR completed_at >= projections_committed_at)
        AND updated_at >= COALESCE(
            completed_at,
            projections_committed_at,
            payload_moved_at,
            intent_committed_at,
            planned_at
        )
    )
);

CREATE INDEX IF NOT EXISTS workspace_archive_operations_object_idx
ON storage.workspace_archive_operations (workspace_kind, object_id, created_at DESC);

CREATE INDEX IF NOT EXISTS workspace_archive_operations_phase_idx
ON storage.workspace_archive_operations (terminal_status, phase, updated_at DESC);

CREATE TABLE IF NOT EXISTS storage.workspace_archive_manifests (
    workspace_archive_operation_id text PRIMARY KEY
        REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id) ON DELETE RESTRICT,
    schema_version text NOT NULL CHECK (schema_version = 'storage.workspace_archive_manifest.v1'),
    evidence_kind text NOT NULL CHECK (evidence_kind = 'physical_workspace_move'),
    workspace_kind text NOT NULL CHECK (workspace_kind IN ('topic', 'project', 'library_item')),
    object_id text NOT NULL CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$'),
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    lifecycle_state text NOT NULL CHECK (lifecycle_state IN ('active', 'archived')),
    plan_digest text NOT NULL CHECK (plan_digest ~ '^sha256:[0-9a-f]{64}$'),
    restore_plan_digest text NULL CHECK (
        restore_plan_digest IS NULL OR restore_plan_digest ~ '^sha256:[0-9a-f]{64}$'
    ),
    inventory_digest text NOT NULL CHECK (inventory_digest ~ '^sha256:[0-9a-f]{64}$'),
    archive_source_identity_json jsonb NOT NULL CHECK (
        jsonb_typeof(archive_source_identity_json) = 'object'
        AND archive_source_identity_json->>'presence' IS NOT DISTINCT FROM 'present'
    ),
    manifest_json jsonb NOT NULL CHECK (
        jsonb_typeof(manifest_json) = 'object'
        AND manifest_json->>'schema_version' IS NOT DISTINCT FROM 'storage.workspace_archive_manifest.v1'
        AND manifest_json->>'evidence_kind' IS NOT DISTINCT FROM 'physical_workspace_move'
        AND manifest_json->>'archive_operation_id' IS NOT DISTINCT FROM workspace_archive_operation_id
        AND manifest_json->>'kind' IS NOT DISTINCT FROM workspace_kind
        AND manifest_json->>'object_id' IS NOT DISTINCT FROM object_id
        AND manifest_json->>'slug' IS NOT DISTINCT FROM slug
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
        AND CASE
            WHEN manifest_json->>'restored_at' IS NULL THEN NULL
            ELSE (manifest_json->>'restored_at')::timestamptz
        END IS NOT DISTINCT FROM restored_at
        AND octet_length(manifest_json::text) <= 1048576
    ),
    authentication_key_id text NOT NULL CHECK (authentication_key_id ~ '^[a-z][a-z0-9_.-]{2,127}$'),
    authentication_tag text NOT NULL CHECK (authentication_tag ~ '^hmac-sha256:[0-9a-f]{64}$'),
    archived_at timestamptz NOT NULL,
    archive_operation_kind text NOT NULL DEFAULT 'archive' CHECK (archive_operation_kind = 'archive'),
    restore_operation_id text NULL
        REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id) ON DELETE RESTRICT
        CHECK (
            restore_operation_id IS NULL
            OR restore_operation_id ~ '^workspace_archive_operation_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
        ),
    restore_operation_kind text NULL CHECK (
        restore_operation_kind IS NULL OR restore_operation_kind = 'restore'
    ),
    restored_at timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (
            lifecycle_state = 'archived'
            AND restore_operation_id IS NULL
            AND restore_operation_kind IS NULL
            AND restore_plan_digest IS NULL
            AND restored_at IS NULL
        )
        OR
        (
            lifecycle_state = 'active'
            AND restore_operation_id IS NOT NULL
            AND restore_operation_kind = 'restore'
            AND restore_plan_digest IS NOT NULL
            AND restored_at IS NOT NULL
        )
    ),
    CHECK (restored_at IS NULL OR restored_at >= archived_at),
    FOREIGN KEY (
        workspace_archive_operation_id,
        archive_operation_kind,
        workspace_kind,
        object_id,
        slug,
        plan_digest
    ) REFERENCES storage.workspace_archive_operations (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        plan_digest
    ) ON DELETE RESTRICT,
    FOREIGN KEY (
        restore_operation_id,
        restore_operation_kind,
        workspace_kind,
        object_id,
        slug,
        restore_plan_digest
    ) REFERENCES storage.workspace_archive_operations (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        plan_digest
    ) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS storage.workspace_archive_findings (
    workspace_archive_finding_id text PRIMARY KEY CHECK (
        workspace_archive_finding_id ~ '^workspace_archive_finding_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
    ),
    workspace_archive_operation_id text NOT NULL
        REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id) ON DELETE CASCADE,
    schema_version text NOT NULL CHECK (schema_version = 'storage.workspace_archive_finding.v1'),
    finding_code text NOT NULL CHECK (finding_code IN (
        'destination_collision',
        'symlink_escape',
        'cross_device',
        'source_drift',
        'destination_substitution',
        'replay_conflict',
        'historical_copy_evidence',
        'ambiguous_custody',
        'invalid_binding',
        'manual_repair_required'
    )),
    severity text NOT NULL CHECK (severity IN ('info', 'warning', 'error')),
    at_phase text NOT NULL CHECK (at_phase IN (
        'archive_planned',
        'archive_intent_committed',
        'archive_payload_moved',
        'archive_projections_committed',
        'archive_complete',
        'restore_planned',
        'restore_intent_committed',
        'restore_payload_moved',
        'restore_projections_committed',
        'restore_complete',
        'blocked',
        'manual_repair_required'
    )),
    summary text NOT NULL CHECK (summary = btrim(summary) AND octet_length(summary) BETWEEN 1 AND 512),
    repairable boolean NOT NULL,
    evidence_json jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (
        jsonb_typeof(evidence_json) = 'array'
        AND jsonb_array_length(evidence_json) <= 32
        AND octet_length(evidence_json::text) <= 32768
    ),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS workspace_archive_findings_operation_idx
ON storage.workspace_archive_findings (workspace_archive_operation_id, created_at);

CREATE TABLE IF NOT EXISTS storage.workspace_lifecycle_events (
    workspace_lifecycle_event_id text PRIMARY KEY CHECK (
        workspace_lifecycle_event_id ~ '^workspace_lifecycle_event_[0-7][0-9A-HJKMNP-TV-Z]{25}$'
    ),
    schema_version text NOT NULL CHECK (schema_version = 'storage.workspace_lifecycle_event.v1'),
    event_kind text NOT NULL CHECK (event_kind = 'workspace.lifecycle_changed'),
    workspace_archive_operation_id text NOT NULL UNIQUE
        REFERENCES storage.workspace_archive_operations(workspace_archive_operation_id) ON DELETE RESTRICT,
    operation_kind text NOT NULL CHECK (operation_kind IN ('archive', 'restore')),
    workspace_kind text NOT NULL CHECK (workspace_kind IN ('topic', 'project', 'library_item')),
    object_id text NOT NULL CHECK (object_id ~ '^[a-z][a-z0-9_]{2,127}$'),
    slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    transition text NOT NULL CHECK (transition IN ('active_to_archived', 'archived_to_active')),
    from_state text NOT NULL CHECK (from_state IN ('active', 'archived')),
    to_state text NOT NULL CHECK (to_state IN ('active', 'archived')),
    source_root text NOT NULL CHECK (source_root IN ('box', 'storage')),
    source_relative_path text NOT NULL,
    destination_root text NOT NULL CHECK (destination_root IN ('box', 'storage')),
    destination_relative_path text NOT NULL,
    actor_id text NOT NULL REFERENCES identity.actors(actor_id),
    reason text NOT NULL CHECK (reason = btrim(reason) AND octet_length(reason) BETWEEN 1 AND 1024),
    details jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (
        jsonb_typeof(details) = 'object'
        AND octet_length(details::text) <= 32768
    ),
    occurred_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (
        (operation_kind = 'archive' AND transition = 'active_to_archived' AND from_state = 'active' AND to_state = 'archived'
            AND source_root = 'box' AND destination_root = 'storage')
        OR
        (operation_kind = 'restore' AND transition = 'archived_to_active' AND from_state = 'archived' AND to_state = 'active'
            AND source_root = 'storage' AND destination_root = 'box')
    ),
    CHECK (
        source_relative_path = CASE
            WHEN transition = 'active_to_archived' THEN CASE workspace_kind
                WHEN 'topic' THEN 'Topics/' || slug
                WHEN 'project' THEN 'Projects/' || slug
                WHEN 'library_item' THEN 'Library/' || slug
            END
            ELSE CASE workspace_kind
                WHEN 'topic' THEN 'archive/topics/' || slug || '/content'
                WHEN 'project' THEN 'archive/projects/' || slug || '/project'
                WHEN 'library_item' THEN 'archive/library/' || slug || '/content'
            END
        END
    ),
    CHECK (
        destination_relative_path = CASE
            WHEN transition = 'active_to_archived' THEN CASE workspace_kind
                WHEN 'topic' THEN 'archive/topics/' || slug || '/content'
                WHEN 'project' THEN 'archive/projects/' || slug || '/project'
                WHEN 'library_item' THEN 'archive/library/' || slug || '/content'
            END
            ELSE CASE workspace_kind
                WHEN 'topic' THEN 'Topics/' || slug
                WHEN 'project' THEN 'Projects/' || slug
                WHEN 'library_item' THEN 'Library/' || slug
            END
        END
    ),
    FOREIGN KEY (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        source_root,
        source_relative_path,
        destination_root,
        destination_relative_path,
        actor_id,
        reason
    ) REFERENCES storage.workspace_archive_operations (
        workspace_archive_operation_id,
        operation_kind,
        workspace_kind,
        object_id,
        slug,
        source_root,
        source_relative_path,
        destination_root,
        destination_relative_path,
        actor_id,
        reason
    ) ON DELETE RESTRICT
);

CREATE INDEX IF NOT EXISTS workspace_lifecycle_events_object_idx
ON storage.workspace_lifecycle_events (workspace_kind, object_id, occurred_at DESC);

-- +goose Down
DROP TABLE IF EXISTS storage.workspace_lifecycle_events;
DROP TABLE IF EXISTS storage.workspace_archive_findings;
DROP TABLE IF EXISTS storage.workspace_archive_manifests;
DROP TABLE IF EXISTS storage.workspace_archive_operations;
