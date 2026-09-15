-- +goose Up
INSERT INTO scopes.scope_types (
    scope_type,
    description,
    default_visibility,
    allows_workspace_view,
    allows_mounts,
    allows_object_links,
    metadata
)
VALUES (
    'box_area',
    'Scope for a LOOM Box area such as Notes or Launchpad.',
    'internal',
    true,
    false,
    true,
    '{"slice":"v0.5.3-06","source":"box_watch_policy"}'::jsonb
)
ON CONFLICT (scope_type) DO UPDATE
SET description = EXCLUDED.description,
    default_visibility = EXCLUDED.default_visibility,
    allows_workspace_view = EXCLUDED.allows_workspace_view,
    allows_mounts = EXCLUDED.allows_mounts,
    allows_object_links = EXCLUDED.allows_object_links,
    metadata = scopes.scope_types.metadata || EXCLUDED.metadata;

-- +goose Down
DELETE FROM scopes.scope_types
WHERE scope_type = 'box_area'
  AND NOT EXISTS (
      SELECT 1
      FROM scopes.scopes
      WHERE scopes.scopes.scope_type = 'box_area'
  );
