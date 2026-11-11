package automation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/workers"
)

func (s Service) UpdateScheduleTiming(ctx context.Context, req requestctx.Context, ref string, input ScheduleTimingInput) (ScheduleDetail, error) {
	if s.DB == nil {
		return ScheduleDetail{}, fmt.Errorf("database is required")
	}
	plan, misfire, err := normalizeScheduleTiming(input, time.Now().UTC())
	if err != nil {
		return ScheduleDetail{}, err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ScheduleDetail{}, err
	}
	defer tx.Rollback()
	schedule, err := readSchedule(ctx, tx, strings.TrimSpace(ref), true)
	if err != nil {
		return ScheduleDetail{}, err
	}
	if schedule.Status != ScheduleStatusPaused || schedule.ScheduleKind != ScheduleKindCron {
		return ScheduleDetail{}, fmt.Errorf("pause the existing cron schedule before changing its timing")
	}
	if schedule.ScheduleExpr == strings.TrimSpace(input.Cron) && schedule.Timezone == strings.TrimSpace(input.Timezone) {
		plan.StartAt, plan.NextFireAt = schedule.StartAt, schedule.NextFireAt
	}
	// All identity, target, input, history, retry and ownership fields stay untouched.
	if _, err := tx.ExecContext(ctx, `UPDATE automation.schedules
		SET schedule_expr = $2, timezone = $3, start_at = $4, next_fire_at = $5,
		    misfire_profile_json = $6::jsonb, updated_at = now()
		WHERE schedule_id = $1`, schedule.ScheduleID, strings.TrimSpace(input.Cron), strings.TrimSpace(input.Timezone), nullableTime(plan.StartAt), nullableTime(plan.NextFireAt), mustJSON(misfire)); err != nil {
		return ScheduleDetail{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE automation.automations
		SET misfire_profile_json = $2::jsonb, updated_at = now()
		WHERE automation_id = $1`, schedule.AutomationID, mustJSON(misfire)); err != nil {
		return ScheduleDetail{}, err
	}
	if err := appendLifecycleEventTx(ctx, tx, req, events.TypeScheduleUpdated, "schedule", schedule.ScheduleID, schedule.Status, map[string]any{
		"schedule_key": schedule.ScheduleKey, "schedule_expr": strings.TrimSpace(input.Cron),
		"timezone": strings.TrimSpace(input.Timezone), "misfire_profile": misfire,
	}); err != nil {
		return ScheduleDetail{}, err
	}
	if err := workers.NotifyQueueTx(ctx, tx, workers.KindAutomationScheduler); err != nil {
		return ScheduleDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return ScheduleDetail{}, err
	}
	return s.GetSchedule(ctx, schedule.ScheduleID)
}

func normalizeScheduleTiming(input ScheduleTimingInput, now time.Time) (SchedulePlan, MisfireProfile, error) {
	if strings.TrimSpace(input.Timezone) == "" || strings.TrimSpace(input.MisfirePolicy) == "" {
		return SchedulePlan{}, MisfireProfile{}, fmt.Errorf("explicit timezone and misfire policy are required")
	}
	plan, err := ParseScheduleExpressionInTimezone(ScheduleKindCron, input.Cron, input.Timezone, now)
	if err != nil {
		return SchedulePlan{}, MisfireProfile{}, err
	}
	misfire, err := NormalizeMisfireProfile(mustJSON(MisfireProfile{Policy: input.MisfirePolicy, LatenessWindowSeconds: input.LatenessWindowSeconds}))
	return plan, misfire, err
}
