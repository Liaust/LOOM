-- +goose Up
-- Keep hot custody predicates independent of the size of the retained inventory.
-- Generated values remain exactly bound to the authoritative plan, including NULL.
ALTER TABLE storage.workspace_archive_operations
    ADD COLUMN source_absolute_path text GENERATED ALWAYS AS
        (plan_json #>> '{source,path,absolute_path}') STORED,
    ADD COLUMN destination_absolute_path text GENERATED ALWAYS AS
        (plan_json #>> '{destination,path,absolute_path}') STORED;

-- +goose Down
ALTER TABLE storage.workspace_archive_operations
    DROP COLUMN destination_absolute_path,
    DROP COLUMN source_absolute_path;
