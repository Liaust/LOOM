-- Filesystem mutation custody is independent of optional Notes enrollment.
-- +goose Up
CREATE TABLE notes_workspace.source_scopes (
 scope_id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind IN ('notes','box')),
 -- Historical unresolved Notes scopes retain their unknown path; this is not
 -- write authority. New Box scopes require an actual pinned absolute root.
 source_path text NOT NULL,
 node_key text NOT NULL DEFAULT '',
 root_identity text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (kind='notes' OR source_path<>'')
);
INSERT INTO notes_workspace.source_scopes(scope_id,kind,source_path,node_key,created_at)
 SELECT notes_source_root_id,'notes',source_path,COALESCE(node_key,''),created_at
 FROM knowledge.notes_source_roots;

ALTER TABLE notes_workspace.files DROP CONSTRAINT files_collection_id_fkey;
ALTER TABLE notes_workspace.files ADD CONSTRAINT files_collection_id_fkey
 FOREIGN KEY(collection_id) REFERENCES notes_workspace.source_scopes(scope_id);

CREATE TABLE notes_workspace.tree_moves (
 move_id text PRIMARY KEY,
 scope_id text NOT NULL REFERENCES notes_workspace.source_scopes(scope_id),
 state text NOT NULL CHECK (state IN ('preview','prepared','accepted','conflict','held')),
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workspace_prepared_tree_moves ON notes_workspace.tree_moves(state) WHERE state IN ('prepared','held');

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Workspace mutation scope downgrade requires explicit custody recovery; history retained';
END $$;
-- +goose StatementEnd
