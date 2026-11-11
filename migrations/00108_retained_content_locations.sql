-- +goose Up
CREATE TABLE storage.retained_relocations (
 relocation_id text PRIMARY KEY,
 custody_id text NOT NULL,
 original_prefix text NOT NULL,
 source_prefix text NOT NULL,
 destination_prefix text NOT NULL,
 plan_digest text NOT NULL CHECK(plan_digest ~ '^sha256:[a-f0-9]{64}$'),
 plan_json jsonb NOT NULL,
 state text NOT NULL CHECK(state IN ('planned','prepared','complete')),
 created_at timestamptz NOT NULL DEFAULT now(),
 completed_at timestamptz,
 CHECK(source_prefix<>destination_prefix),
 CHECK((state='complete')=(completed_at IS NOT NULL))
);
CREATE INDEX retained_relocations_incomplete ON storage.retained_relocations(custody_id) WHERE state='prepared';
CREATE TABLE storage.retained_relocation_events (
 event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 relocation_id text NOT NULL REFERENCES storage.retained_relocations(relocation_id),
 phase text NOT NULL CHECK(phase IN ('planned','prepared','complete')),
 evidence jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(relocation_id,phase)
);
CREATE TABLE storage.custody_locations (
 custody_id text NOT NULL,
 original_prefix text NOT NULL,
 current_prefix text NOT NULL,
 relocation_id text NOT NULL REFERENCES storage.retained_relocations(relocation_id),
 PRIMARY KEY(custody_id,original_prefix),
 UNIQUE(current_prefix)
);

-- +goose StatementBegin
CREATE FUNCTION storage.guard_retained_relocation_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='retained_relocation_events' OR TG_OP='DELETE' THEN
  RAISE EXCEPTION 'retained relocation history is append-only';
 END IF;
 IF (OLD.relocation_id,OLD.custody_id,OLD.original_prefix,OLD.source_prefix,OLD.destination_prefix,OLD.plan_digest,OLD.plan_json,OLD.created_at)
  IS DISTINCT FROM
  (NEW.relocation_id,NEW.custody_id,NEW.original_prefix,NEW.source_prefix,NEW.destination_prefix,NEW.plan_digest,NEW.plan_json,NEW.created_at)
  OR NOT (OLD.state='planned' AND NEW.state='prepared' OR OLD.state='prepared' AND NEW.state='complete') THEN
  RAISE EXCEPTION 'invalid retained relocation transition';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER retained_relocation_immutable BEFORE UPDATE OR DELETE ON storage.retained_relocations FOR EACH ROW EXECUTE FUNCTION storage.guard_retained_relocation_history();
CREATE TRIGGER retained_relocation_events_immutable BEFORE UPDATE OR DELETE ON storage.retained_relocation_events FOR EACH ROW EXECUTE FUNCTION storage.guard_retained_relocation_history();

-- Historical paths resolve within one custody identity. Reusing an old pathname
-- for a new archive cycle must not redirect that cycle to an older payload.
-- +goose StatementBegin
CREATE FUNCTION storage.resolve_custody_path(owner_id text, historical_path text) RETURNS text STABLE LANGUAGE plpgsql AS $$
DECLARE result text;
BEGIN
 IF EXISTS(SELECT 1 FROM storage.retained_relocations r WHERE r.custody_id=owner_id AND r.state='prepared' AND
  (historical_path=r.original_prefix OR left(historical_path,length(r.original_prefix)+1)=r.original_prefix||'/')) THEN
  RAISE EXCEPTION 'retained custody relocation needs recovery';
 END IF;
 SELECT l.current_prefix||substr(historical_path,length(l.original_prefix)+1) INTO result
 FROM storage.custody_locations l WHERE l.custody_id=owner_id AND
 (historical_path=l.original_prefix OR left(historical_path,length(l.original_prefix)+1)=l.original_prefix||'/')
 ORDER BY length(l.original_prefix) DESC LIMIT 1;
 RETURN COALESCE(result,historical_path);
END $$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION storage.resolve_custody_path(text,text);
DROP TABLE storage.custody_locations;
DROP TABLE storage.retained_relocation_events;
DROP TABLE storage.retained_relocations;
DROP FUNCTION storage.guard_retained_relocation_history();
