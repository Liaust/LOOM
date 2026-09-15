-- +goose Up
ALTER TABLE box.watch_root_registrations
    DROP CONSTRAINT watch_root_registrations_area_key_check;
ALTER TABLE box.watch_root_registrations
    ADD CONSTRAINT watch_root_registrations_area_key_check
    CHECK (area_key IN ('notes', 'launchpad', 'documents', 'topics', 'library')
        OR area_key ~ '^backup_[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$');

ALTER TABLE knowledge.notes_source_roots
    DROP CONSTRAINT notes_source_roots_root_kind_check;
ALTER TABLE knowledge.notes_source_roots
    ADD CONSTRAINT notes_source_roots_root_kind_check
    CHECK (root_kind IN ('box_notes', 'project_notes', 'box_topics', 'box_library', 'project_material'));
ALTER TABLE knowledge.notes_source_roots
    ADD CONSTRAINT notes_source_roots_expanded_owner_check CHECK (
        (root_kind NOT IN ('box_topics', 'box_library') OR
         (project_id IS NULL AND project_watched_root_registration_id IS NULL)) AND
        (root_kind <> 'project_material' OR
         (project_id IS NOT NULL AND box_watch_root_registration_id IS NULL))
    );

-- +goose Down
-- Refuse downgrade while expanded sources remain; never delete their history.
ALTER TABLE knowledge.notes_source_roots
    DROP CONSTRAINT notes_source_roots_expanded_owner_check;
ALTER TABLE knowledge.notes_source_roots
    DROP CONSTRAINT notes_source_roots_root_kind_check;
ALTER TABLE knowledge.notes_source_roots
    ADD CONSTRAINT notes_source_roots_root_kind_check CHECK (root_kind IN ('box_notes', 'project_notes'));
ALTER TABLE box.watch_root_registrations
    DROP CONSTRAINT watch_root_registrations_area_key_check;
ALTER TABLE box.watch_root_registrations
    ADD CONSTRAINT watch_root_registrations_area_key_check
    CHECK (area_key IN ('notes', 'launchpad', 'documents')
        OR area_key ~ '^backup_[a-z0-9][a-z0-9_-]{1,78}[a-z0-9]$');
