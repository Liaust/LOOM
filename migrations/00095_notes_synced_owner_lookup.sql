-- +goose Up
-- These are candidate lookups only. The owner query still checks full paths,
-- latest-version acceptance, source membership, node identity and project state.
CREATE INDEX notes_owner_version_path_idx
ON objects.object_versions (md5(btrim(source_path)), object_id);
CREATE INDEX notes_owner_file_path_idx
ON files.file_metadata (md5(btrim(source_path)), object_id);
CREATE INDEX notes_owner_metadata_path_idx
ON objects.objects (md5(btrim(metadata->>'source_path')), object_id);
CREATE INDEX notes_owner_legacy_root_idx
ON objects.objects ((metadata->'metadata'->>'watched_root'), object_id);

-- +goose Down
DROP INDEX objects.notes_owner_legacy_root_idx;
DROP INDEX objects.notes_owner_metadata_path_idx;
DROP INDEX files.notes_owner_file_path_idx;
DROP INDEX objects.notes_owner_version_path_idx;
