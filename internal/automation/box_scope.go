package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"loom.local/loom/internal/boxregistry"
	"loom.local/loom/internal/jobs"
)

func scheduleBoxPredicate(ctx context.Context, args *[]any) string {
	return jobs.BoxPredicate(ctx, jobs.BoxOwnerSQL("schedules.project_id", "", "schedules.metadata_json"), args)
}

func automationBoxPredicate(ctx context.Context, args *[]any) string {
	return jobs.BoxPredicate(ctx, jobs.BoxOwnerSQL("automations.project_id", "", "automations.metadata_json"), args)
}

func invocationBoxPredicate(ctx context.Context, args *[]any) string {
	return invocationBoxPredicateFor(ctx, "invocations", args)
}

func invocationBoxPredicateFor(ctx context.Context, alias string, args *[]any) string {
	return jobs.BoxPredicate(ctx, jobs.BoxOwnerSQL(alias+".project_id", "", alias+".metadata_json"), args)
}

func fireBoxPredicate(ctx context.Context, args *[]any) string {
	selection, bound := boxregistry.FromContext(ctx)
	if !bound || selection.AllBoxes {
		return "true"
	}
	return `EXISTS (SELECT 1 FROM automation.schedules WHERE schedules.schedule_id = schedule_fires.schedule_id AND ` + scheduleBoxPredicate(ctx, args) + `)`
}

func readSchedule(ctx context.Context, db queryer, ref string, lock bool) (Schedule, error) {
	args := []any{ref}
	predicate := scheduleBoxPredicate(ctx, &args)
	query := scheduleSelectSQL() + ` WHERE (schedule_id = $1 OR schedule_key = $1) AND ` + predicate
	if lock {
		query += ` FOR UPDATE`
	}
	return scanSchedule(db.QueryRowContext(ctx, query, args...))
}

func scheduleOwner(ctx context.Context, db queryer, schedule Schedule) (string, error) {
	var owner string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(`+jobs.BoxOwnerSQL("schedules.project_id", "", "schedules.metadata_json")+`,'') FROM automation.schedules WHERE schedule_id=$1`, schedule.ScheduleID).Scan(&owner)
	return owner, err
}

func scheduleInvocationOwner(ctx context.Context, db queryer, schedule Schedule, invocation Invocation) (string, error) {
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(invocation.MetadataJSON, &metadata); err != nil {
		return "", fmt.Errorf("invalid invocation ownership: %w", err)
	}
	if value, saved := metadata["home_box_id"]; saved {
		var owner string
		if err := json.Unmarshal(value, &owner); err != nil || owner == "" {
			return "", fmt.Errorf("invalid invocation home_box_id")
		}
		return owner, nil
	}
	return scheduleOwner(ctx, db, schedule)
}

// Status metadata is additive but cannot reassign the schedule, including a
// legacy row whose current project membership is derived rather than saved.
func validateScheduleMetadata(ctx context.Context, db queryer, schedule Schedule, raw json.RawMessage) error {
	var metadata map[string]json.RawMessage
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
	}
	if value, present := metadata["home_box_id"]; present {
		var supplied string
		if err := json.Unmarshal(value, &supplied); err != nil || supplied == "" {
			return fmt.Errorf("metadata home_box_id must be a nonempty string")
		}
		owner, err := scheduleOwner(ctx, db, schedule)
		if err != nil {
			return err
		}
		if supplied != owner {
			return fmt.Errorf("metadata cannot change schedule Box ownership")
		}
	}
	return nil
}

func guardScheduleEnsure(ctx context.Context, tx *sql.Tx, existing Schedule, normalized *normalizedCreateSchedule) error {
	args := []any{existing.ScheduleID}
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(`+scheduleBoxPredicate(ctx, &args)+`,false) FROM automation.schedules WHERE schedule_id=$1`, args...).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("schedule key belongs to work outside selected Box")
	}
	if stringValue(existing.ProjectID) != normalized.ProjectID {
		return fmt.Errorf("schedule key already has different project ownership")
	}
	if err := validateScheduleMetadata(ctx, tx, existing, normalized.Metadata); err != nil {
		return err
	}
	var old, incoming map[string]json.RawMessage
	if err := json.Unmarshal(existing.MetadataJSON, &old); err != nil {
		return err
	}
	if old == nil {
		old = map[string]json.RawMessage{}
	}
	if err := json.Unmarshal(normalized.Metadata, &incoming); err != nil {
		return err
	}
	for key, value := range incoming {
		old[key] = value
	}
	var err error
	normalized.Metadata, err = json.Marshal(old)
	return err
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
