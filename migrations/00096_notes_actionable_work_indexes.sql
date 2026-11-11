-- +goose Up
-- Match the actual delivery predicate, including absent directory tombstones.
-- Excluded rows and delivered history are custody, not runnable queue members.
CREATE INDEX notes_inventory_actionable_delivery
 ON notes_workspace.source_inventory(collection_id,generation,entry_kind,priority DESC,waiting_since,relative_path)
 WHERE sequence>delivered_sequence AND (present OR entry_kind='directory') AND reason<>'source_excluded';
CREATE INDEX notes_inventory_actionable_oldest
 ON notes_workspace.source_inventory(collection_id,generation,waiting_since,relative_path)
 WHERE sequence>delivered_sequence AND (present OR entry_kind='directory') AND reason<>'source_excluded';
CREATE INDEX notes_inventory_actionable_retry
 ON notes_workspace.source_inventory(collection_id,generation,retry_at)
 WHERE sequence>delivered_sequence AND (present OR entry_kind='directory') AND reason<>'source_excluded';
CREATE INDEX notes_sync_unfinished_moves
 ON notes_workspace.sync_records(replica_id,record_id COLLATE "C")
 WHERE kind='export' AND COALESCE(record->>'Published','false')<>'true' AND COALESCE(record->>'Terminal','false')<>'true';
CREATE INDEX notes_sync_pending_observations
 ON notes_workspace.sync_records(replica_id,record_id COLLATE "C")
 WHERE kind='observation' AND record->>'Reason'='control_pending';

-- +goose Down
DROP INDEX notes_workspace.notes_sync_pending_observations;
DROP INDEX notes_workspace.notes_sync_unfinished_moves;
DROP INDEX notes_workspace.notes_inventory_actionable_retry;
DROP INDEX notes_workspace.notes_inventory_actionable_oldest;
DROP INDEX notes_workspace.notes_inventory_actionable_delivery;
