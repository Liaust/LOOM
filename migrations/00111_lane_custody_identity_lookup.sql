-- +goose Up
CREATE INDEX storage_lane_member_identity_lookup ON storage.storage_entries
 (origin_node_key,(metadata->>'batch_id'),(metadata->>'relative_lane_path'),storage_entry_id)
 WHERE storage_class='lane_custody';

-- +goose Down
DROP INDEX storage.storage_lane_member_identity_lookup;
