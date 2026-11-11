-- +goose Up
-- Bound source discovery by its physical cursor, then resolve a path's current
-- representation without rescanning every historical entry in that source.
CREATE INDEX storage_entries_notes_admission_page_idx
ON storage.storage_entries (source_area, watched_root_key, storage_entry_id);

-- Hash only for bounded index keys; admission still checks the full path.
CREATE INDEX storage_entries_notes_admission_identity_idx
ON storage.storage_entries (
    source_area, watched_root_key, origin_node_key,
    md5(COALESCE(NULLIF(original_source_path, ''), logical_path)),
    (COALESCE(origin_node_id, '')), (COALESCE(project_id, ''))
);

-- +goose Down
DROP INDEX storage.storage_entries_notes_admission_identity_idx;
DROP INDEX storage.storage_entries_notes_admission_page_idx;
