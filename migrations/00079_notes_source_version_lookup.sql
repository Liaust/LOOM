-- +goose Up
-- Current-source reads also fence retained identities outside admission pages.
-- Keep their exact-path lookup indexed rather than repeating broad version scans.
CREATE INDEX objects_object_versions_source_path_order_idx
ON objects.object_versions (source_path, created_at DESC, object_version_id DESC);

-- +goose Down
DROP INDEX objects.objects_object_versions_source_path_order_idx;
