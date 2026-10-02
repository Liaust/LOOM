-- Durable canonical Notes source custody and native sync evidence.
-- +goose Up
CREATE SCHEMA notes_workspace;
CREATE TABLE notes_workspace.files (
 file_id text PRIMARY KEY,
 collection_id text NOT NULL REFERENCES knowledge.notes_source_roots(notes_source_root_id),
 path_key text NOT NULL,
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 deleted boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX notes_workspace_active_path ON notes_workspace.files(collection_id,path_key) WHERE NOT deleted;
CREATE TABLE notes_workspace.bases (
 base_id text PRIMARY KEY,
 file_id text NOT NULL REFERENCES notes_workspace.files(file_id),
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE notes_workspace.operations (
 operation_id text PRIMARY KEY,
 file_id text NOT NULL REFERENCES notes_workspace.files(file_id),
 state text NOT NULL CHECK (state IN ('pending','accepted','conflict','held')),
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notes_workspace_pending ON notes_workspace.operations(state,created_at)
 WHERE state IN ('pending','held');
-- Cursor order is stable across retries/updated_at changes.
CREATE INDEX notes_workspace_pending_cursor ON notes_workspace.operations(operation_id COLLATE "C")
 WHERE state IN ('pending','held');
CREATE INDEX notes_workspace_retained_cursor ON notes_workspace.operations(operation_id COLLATE "C")
 WHERE COALESCE(record->>'journal','') <> '';
CREATE TABLE notes_workspace.recoveries (
 recovery_id text PRIMARY KEY,
 operation_id text NOT NULL REFERENCES notes_workspace.operations(operation_id),
 state text NOT NULL CHECK (state IN ('conflict','held')),
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notes_workspace_recovery_cursor ON notes_workspace.recoveries(operation_id,recovery_id COLLATE "C");
CREATE TABLE notes_workspace.path_history (
 file_id text NOT NULL REFERENCES notes_workspace.files(file_id),
 path_version bigint NOT NULL,
 operation_id text NOT NULL UNIQUE REFERENCES notes_workspace.operations(operation_id),
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(file_id,path_version)
);
CREATE INDEX notes_workspace_path_reservations ON notes_workspace.operations(file_id)
 WHERE state IN ('pending','held') AND COALESCE(record->>'path_stage','')<>'';
-- Deliberately no automatic retention/pruning: offline bases and conflicts are custody.
CREATE TABLE notes_workspace.sync_replicas (
 replica_id text PRIMARY KEY,
 native_epoch text NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE notes_workspace.sync_records (
 replica_id text NOT NULL REFERENCES notes_workspace.sync_replicas(replica_id),
 kind text NOT NULL CHECK(kind IN ('cursor','observation','operation','binding','file','source_file','export')),
 record_id text NOT NULL,
 record jsonb NOT NULL CHECK(jsonb_typeof(record) IN ('object','string')),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(replica_id,kind,record_id)
);
CREATE INDEX notes_workspace_sync_discovery ON notes_workspace.sync_records(replica_id,kind,record_id COLLATE "C");
-- Epoch changes require explicit reconciliation; no automatic reset/rebinding.
-- No automatic pruning of protocol receipts, source mappings or unresolved evidence.
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Notes workspace downgrade requires explicit custody recovery; records retained';
END $$;
-- +goose StatementEnd
