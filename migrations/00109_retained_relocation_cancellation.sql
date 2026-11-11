-- +goose Up
ALTER TABLE storage.retained_relocations DROP CONSTRAINT retained_relocations_state_check;
ALTER TABLE storage.retained_relocations ADD CONSTRAINT retained_relocations_state_check CHECK(state IN ('planned','prepared','complete','cancelled'));
ALTER TABLE storage.retained_relocation_events DROP CONSTRAINT retained_relocation_events_phase_check;
ALTER TABLE storage.retained_relocation_events ADD CONSTRAINT retained_relocation_events_phase_check CHECK(phase IN ('planned','prepared','complete','cancelled'));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION storage.guard_retained_relocation_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='retained_relocation_events' OR TG_OP='DELETE' THEN
  RAISE EXCEPTION 'retained relocation history is append-only';
 END IF;
 IF (OLD.relocation_id,OLD.custody_id,OLD.original_prefix,OLD.source_prefix,OLD.destination_prefix,OLD.plan_digest,OLD.plan_json,OLD.created_at)
  IS DISTINCT FROM
  (NEW.relocation_id,NEW.custody_id,NEW.original_prefix,NEW.source_prefix,NEW.destination_prefix,NEW.plan_digest,NEW.plan_json,NEW.created_at)
  OR NOT (OLD.state='planned' AND NEW.state='prepared' OR OLD.state='prepared' AND NEW.state='complete'
    OR OLD.state IN ('planned','prepared') AND NEW.state='cancelled') THEN
  RAISE EXCEPTION 'invalid retained relocation transition';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Cancellation evidence cannot be removed to satisfy an older schema.
DO $$ BEGIN RAISE EXCEPTION 'retained cancellation history requires forward-compatible recovery'; END $$;
