-- +goose Up
-- Read indexes only. Original operations, paths and retained bytes are unchanged.
CREATE INDEX notes_workspace_operations_timeline ON notes_workspace.operations(updated_at DESC,operation_id DESC);
CREATE INDEX notes_workspace_operations_file_timeline ON notes_workspace.operations(file_id,updated_at DESC,operation_id DESC);

-- +goose Down
DROP INDEX notes_workspace.notes_workspace_operations_file_timeline;
DROP INDEX notes_workspace.notes_workspace_operations_timeline;
