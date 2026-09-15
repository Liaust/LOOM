-- +goose Up
ALTER TABLE automation.schedules
    DROP CONSTRAINT schedules_schedule_kind_check;
ALTER TABLE automation.schedules
    ADD CONSTRAINT schedules_schedule_kind_check
    CHECK (schedule_kind IN ('one_shot', 'interval', 'cron'));

-- +goose Down
-- Refuse rollback with any retained cron row, even disabled. History is data;
-- never delete or reinterpret it merely to fit an older application.
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM automation.schedules WHERE schedule_kind = 'cron') THEN
        RAISE EXCEPTION 'calendar schedule rollback refused: retained cron schedules require migration 00069';
    END IF;
END;
$$;
-- +goose StatementEnd
ALTER TABLE automation.schedules
    DROP CONSTRAINT schedules_schedule_kind_check;
ALTER TABLE automation.schedules
    ADD CONSTRAINT schedules_schedule_kind_check
    CHECK (schedule_kind IN ('one_shot', 'interval'));
