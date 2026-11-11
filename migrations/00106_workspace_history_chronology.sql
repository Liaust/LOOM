-- +goose Up
-- Recovery-journal placement may update old receipts. Chronology must use the
-- immutable receipt creation time, without rewriting any evidence rows.
DROP INDEX notes_workspace.notes_workspace_operations_file_timeline;
DROP INDEX notes_workspace.notes_workspace_operations_timeline;
CREATE INDEX notes_workspace_operations_timeline ON notes_workspace.operations(created_at DESC,operation_id DESC);
CREATE INDEX notes_workspace_operations_file_timeline ON notes_workspace.operations(file_id,created_at DESC,operation_id DESC);
CREATE INDEX notes_workspace_tree_timeline ON notes_workspace.tree_moves(created_at DESC,move_id DESC);

-- +goose Down
DROP INDEX notes_workspace.notes_workspace_tree_timeline;
DROP INDEX notes_workspace.notes_workspace_operations_file_timeline;
DROP INDEX notes_workspace.notes_workspace_operations_timeline;
CREATE INDEX notes_workspace_operations_timeline ON notes_workspace.operations(updated_at DESC,operation_id DESC);
CREATE INDEX notes_workspace_operations_file_timeline ON notes_workspace.operations(file_id,updated_at DESC,operation_id DESC);
