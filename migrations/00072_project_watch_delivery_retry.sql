-- +goose Up
-- Keep desired-state identity immutable; only explicit retry may replace a
-- failed delivery with an identical, fresh transport message.
-- +goose StatementBegin
CREATE FUNCTION projects.guard_declaration_watch_retry() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(NEW)-'message_id') IS DISTINCT FROM (to_jsonb(OLD)-'message_id')
 OR NEW.message_id = OLD.message_id
 OR NOT EXISTS (
  SELECT 1 FROM communication.messages old_message
  JOIN communication.messages next_message ON next_message.communication_message_id=NEW.message_id
  WHERE old_message.communication_message_id=OLD.message_id
   AND old_message.status='failed_retryable' AND next_message.status='available'
   AND next_message.kind=old_message.kind AND next_message.direction=old_message.direction
   AND next_message.node_id=old_message.node_id AND next_message.payload_json=old_message.payload_json
 ) OR NOT EXISTS (
  SELECT 1 FROM projects.declaration_watch_intents i
  WHERE i.operation_id=OLD.operation_id AND i.group_hash=OLD.group_hash
   AND i.final_intent AND i.stage='pending'
 ) THEN
  RAISE EXCEPTION 'watch retry must preserve exact pending desired state';
 END IF;
 RETURN NEW;
END; $$;
-- +goose StatementEnd
DROP TRIGGER declaration_watch_delivery_guard ON projects.declaration_watch_deliveries;
CREATE TRIGGER declaration_watch_delivery_guard BEFORE INSERT OR DELETE ON projects.declaration_watch_deliveries
 FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_watch_delivery();
CREATE TRIGGER declaration_watch_retry_guard BEFORE UPDATE ON projects.declaration_watch_deliveries
 FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_watch_retry();

-- +goose Down
DROP TRIGGER declaration_watch_retry_guard ON projects.declaration_watch_deliveries;
DROP TRIGGER declaration_watch_delivery_guard ON projects.declaration_watch_deliveries;
CREATE TRIGGER declaration_watch_delivery_guard BEFORE INSERT OR UPDATE OR DELETE ON projects.declaration_watch_deliveries
 FOR EACH ROW EXECUTE FUNCTION projects.guard_declaration_watch_delivery();
DROP FUNCTION projects.guard_declaration_watch_retry();
