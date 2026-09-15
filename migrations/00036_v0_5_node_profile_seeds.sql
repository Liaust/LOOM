-- +goose Up
INSERT INTO nodes.authority_profiles (
    authority_profile_id, profile_key, display_name, node_kind,
    can_query_main, can_publish_events, can_expose_providers, can_receive_routes,
    can_request_main_routed_capabilities, can_sync_object_metadata,
    can_hold_local_credentials, can_act_offline, risk_limits_json, metadata
)
VALUES
    (
        'node_authority_profile_primary_workspace_default',
        'primary_workspace_default',
        'Primary Workspace Default',
        'workspace',
        true, true, true, true, true, true, true, true,
        '{"max_risk":"high"}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_authority_profile_secondary_workspace_default',
        'secondary_workspace_default',
        'Secondary Workspace Default',
        'workspace',
        true, true, false, false, true, true, true, true,
        '{"max_risk":"medium"}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_authority_profile_hardware_capability_default',
        'hardware_capability_default',
        'Hardware Capability Default',
        'hardware',
        true, true, true, true, false, false, true, false,
        '{"max_risk":"medium"}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_authority_profile_compute_runner_default',
        'compute_runner_default',
        'Compute Runner Default',
        'hardware',
        true, true, true, true, false, false, true, false,
        '{"max_risk":"high","requires_route_policy":true}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_authority_profile_storage_edge_default',
        'storage_edge_default',
        'Storage Edge Default',
        'hardware',
        true, true, false, false, false, true, true, true,
        '{"max_risk":"medium","storage_only":true}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_authority_profile_automation_edge_default',
        'automation_edge_default',
        'Automation Edge Default',
        'integration',
        true, true, true, true, false, false, true, false,
        '{"max_risk":"medium","external_event_source":true}'::jsonb,
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    )
ON CONFLICT (profile_key) DO NOTHING;

INSERT INTO nodes.runtime_profiles (
    runtime_profile_id, profile_key, display_name, node_kind,
    local_database, local_event_log, local_provider_runtime, local_job_runner,
    script_runtime, workflow_runtime, skill_package_cache, local_policy_cache,
    sync_agent, inbox_outbox, offline_mode, metadata
)
VALUES
    (
        'node_runtime_profile_main_full',
        'main_full',
        'Main Full',
        'main',
        'true', 'true', 'true', 'true',
        'true', 'true', 'true', 'true',
        'true', 'true', 'true',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_workspace_full',
        'workspace_full',
        'Workspace Full',
        'workspace',
        'optional', 'true', 'true', 'optional',
        'optional', 'optional', 'true', 'true',
        'true', 'true', 'true',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_workspace_light',
        'workspace_light',
        'Workspace Light',
        'workspace',
        'false', 'minimal', 'limited', 'false',
        'false', 'false', 'false', 'true',
        'true', 'true', 'limited',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_hardware_agent',
        'hardware_agent',
        'Hardware Agent',
        'hardware',
        'false', 'minimal', 'true', 'false',
        'limited', 'false', 'false', 'true',
        'false', 'true', 'limited',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_compute_runner',
        'compute_runner',
        'Compute Runner',
        'hardware',
        'optional', 'true', 'true', 'true',
        'true', 'optional', 'true', 'true',
        'false', 'true', 'limited',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_storage_edge',
        'storage_edge',
        'Storage Edge',
        'hardware',
        'optional', 'true', 'limited', 'false',
        'false', 'false', 'false', 'true',
        'true', 'true', 'true',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    ),
    (
        'node_runtime_profile_guest_restricted',
        'guest_restricted',
        'Guest Restricted',
        'guest',
        'false', 'minimal', 'false', 'false',
        'false', 'false', 'false', 'false',
        'minimal', 'minimal', 'false',
        '{"seed":"v0.5","profile_family":"installer"}'::jsonb
    )
ON CONFLICT (profile_key) DO NOTHING;

-- +goose Down
DELETE FROM nodes.node_profile_assignments
WHERE authority_profile_id IN (
    'node_authority_profile_primary_workspace_default',
    'node_authority_profile_secondary_workspace_default',
    'node_authority_profile_hardware_capability_default',
    'node_authority_profile_compute_runner_default',
    'node_authority_profile_storage_edge_default',
    'node_authority_profile_automation_edge_default'
) OR runtime_profile_id IN (
    'node_runtime_profile_main_full',
    'node_runtime_profile_workspace_full',
    'node_runtime_profile_workspace_light',
    'node_runtime_profile_hardware_agent',
    'node_runtime_profile_compute_runner',
    'node_runtime_profile_storage_edge',
    'node_runtime_profile_guest_restricted'
);

DELETE FROM nodes.authority_profiles
WHERE profile_key IN (
    'primary_workspace_default',
    'secondary_workspace_default',
    'hardware_capability_default',
    'compute_runner_default',
    'storage_edge_default',
    'automation_edge_default'
);

DELETE FROM nodes.runtime_profiles
WHERE profile_key IN (
    'main_full',
    'workspace_full',
    'workspace_light',
    'hardware_agent',
    'compute_runner',
    'storage_edge',
    'guest_restricted'
);

