-- +goose Up
ALTER TABLE box.watch_root_registrations
DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check;

ALTER TABLE box.watch_root_registrations
ADD CONSTRAINT watch_root_registrations_area_key_check
CHECK (
    area_key IN ('notes', 'documents', 'launchpad')
    OR area_key ~ '^backup_[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$'
);

-- +goose Down
DELETE FROM box.watch_root_registrations
WHERE area_key LIKE 'backup\_%' ESCAPE '\';

ALTER TABLE box.watch_root_registrations
DROP CONSTRAINT IF EXISTS watch_root_registrations_area_key_check;

ALTER TABLE box.watch_root_registrations
ADD CONSTRAINT watch_root_registrations_area_key_check
CHECK (area_key IN ('notes', 'documents', 'launchpad'));
