-- Notes metadata only. Canonical sources, bytes and native journals retain ownership.
-- +goose Up
CREATE TABLE notes_workspace.source_inventory_generations (
 collection_id text NOT NULL REFERENCES knowledge.notes_source_roots(notes_source_root_id),
 generation text NOT NULL,
 record jsonb NOT NULL CHECK (jsonb_typeof(record)='object'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(collection_id,generation)
);
CREATE TABLE notes_workspace.source_inventory (
 collection_id text NOT NULL,
 generation text NOT NULL,
 relative_path text COLLATE "C" NOT NULL,
 entry_kind text NOT NULL CHECK(entry_kind IN ('text','asset')),
 present boolean NOT NULL,
 fingerprint text NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes>=0),
 sequence bigint NOT NULL CHECK(sequence>0),
 delivered_sequence bigint NOT NULL DEFAULT 0,
 seen_scan bigint NOT NULL DEFAULT 0,
 priority boolean NOT NULL DEFAULT false,
 waiting_since timestamptz NOT NULL,
 retry_at timestamptz NOT NULL,
 attempts integer NOT NULL DEFAULT 0,
 reason text NOT NULL DEFAULT '',
 PRIMARY KEY(collection_id,generation,relative_path),
 FOREIGN KEY(collection_id,generation) REFERENCES notes_workspace.source_inventory_generations(collection_id,generation)
);
CREATE INDEX notes_source_inventory_delivery ON notes_workspace.source_inventory
 (collection_id,generation,entry_kind,priority DESC,waiting_since,relative_path)
 WHERE sequence>delivered_sequence AND present;
CREATE INDEX notes_source_inventory_retry ON notes_workspace.source_inventory
 (collection_id,generation,retry_at) WHERE sequence>delivered_sequence AND present;
CREATE INDEX notes_source_inventory_scan ON notes_workspace.source_inventory
 (collection_id,generation,relative_path,seen_scan) WHERE present;
CREATE INDEX notes_source_inventory_oldest ON notes_workspace.source_inventory
 (collection_id,generation,entry_kind,waiting_since,relative_path) WHERE sequence>delivered_sequence AND present;
CREATE INDEX notes_source_inventory_lane_retry ON notes_workspace.source_inventory
 (collection_id,generation,entry_kind,retry_at) WHERE sequence>delivered_sequence AND present;
-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
 RAISE EXCEPTION 'Notes inventory downgrade retains resumable metadata; use compatible code rollback';
END $$;
-- +goose StatementEnd
