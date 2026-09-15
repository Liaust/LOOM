-- +goose Up
ALTER TABLE box.watch_root_registrations
DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check;

ALTER TABLE box.watch_root_registrations
ADD CONSTRAINT watch_root_registrations_area_key_check
CHECK (area_key IN ('notes', 'documents', 'launchpad'));

UPDATE scopes.scope_types
SET description = 'Scope for a LOOM Box synced area such as Notes.'
WHERE scope_type = 'box_area';

-- +goose Down
DELETE FROM box.watch_root_registrations
WHERE area_key = 'documents';

ALTER TABLE box.watch_root_registrations
DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check;

ALTER TABLE box.watch_root_registrations
ADD CONSTRAINT watch_root_registrations_area_key_check
CHECK (area_key IN ('notes', 'launchpad'));

UPDATE scopes.scope_types
SET description = 'Scope for a LOOM Box area such as Notes or Launchpad.'
WHERE scope_type = 'box_area';
