-- +goose Up
CREATE TABLE notes_workspace.desktop_imports (
 import_id text PRIMARY KEY,
 record jsonb NOT NULL,
 blob_id text REFERENCES files.blobs(blob_id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE knowledge.workspace_metadata_refresh (
 refresh_id text PRIMARY KEY,
 record jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE knowledge.workspace_metadata_refresh_items (
 refresh_id text NOT NULL REFERENCES knowledge.workspace_metadata_refresh(refresh_id),
 path text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('directory','file')),
 state text NOT NULL DEFAULT 'pending',
 cursor text NOT NULL DEFAULT '',
 detail text NOT NULL DEFAULT '',
 PRIMARY KEY(refresh_id,path)
);
CREATE INDEX workspace_metadata_refresh_pending ON knowledge.workspace_metadata_refresh_items(refresh_id,state,kind,path);
-- +goose Down
DROP TABLE knowledge.workspace_metadata_refresh_items;
DROP TABLE knowledge.workspace_metadata_refresh;
DROP TABLE notes_workspace.desktop_imports;
