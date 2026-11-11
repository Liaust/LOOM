-- Directory metadata shares the existing event-driven Notes delivery inventory.
-- +goose Up
ALTER TABLE notes_workspace.source_inventory DROP CONSTRAINT source_inventory_entry_kind_check;
ALTER TABLE notes_workspace.source_inventory ADD CONSTRAINT source_inventory_entry_kind_check
 CHECK (entry_kind IN ('text','asset','directory'));
-- A one-time bounded catch-up must include existing empty directories. Each
-- collection resumes through its ordinary page cursor; no file bytes are reset.
UPDATE notes_workspace.source_inventory_generations
 SET record=jsonb_set(record,'{next_repair}','"1970-01-01T00:00:00Z"'::jsonb);
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Directory inventory is retained; use compatible code rollback';
END $$;
-- +goose StatementEnd
