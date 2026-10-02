package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/hermesschedules"
	"loom.local/loom/internal/response"
)

type hermesScheduleClient interface {
	HermesSchedules(context.Context, string) (response.Envelope[hermesschedules.Observation], error)
}

func loadHermesSchedules(ctx context.Context, client Client, correlationID string) *hermesschedules.Observation {
	reader, ok := client.(hermesScheduleClient)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	attemptedAt := time.Now().UTC()
	unavailable := func() *hermesschedules.Observation {
		return &hermesschedules.Observation{Owner: "hermes", Availability: hermesschedules.Unavailable, AttemptedAt: attemptedAt}
	}
	if ctx.Err() != nil {
		return unavailable()
	}
	envelope, err := reader.HermesSchedules(ctx, correlationID)
	if err != nil || ctx.Err() != nil {
		return unavailable()
	}
	return &envelope.Data
}

// Native jobs are informational rows, not LOOM action/selection targets.
func renderHermesSchedules(builder *strings.Builder, observation *hermesschedules.Observation) {
	renderSummarySection(builder, "Hermes Schedules (read-only)")
	if observation == nil {
		renderEmpty(builder, "Native inventory not observed.")
		return
	}
	fmt.Fprintf(builder, "  owner=hermes  host=%s  profile=%s  availability=%s\n",
		firstNonEmpty(observation.Source.Host, "unknown"), firstNonEmpty(observation.Source.Profile, "unknown"), observation.Availability)
	fmt.Fprintf(builder, "  observed=%s  timezone=unknown  scheduler_liveness=unknown  current_completion=unknown\n", hermesStoredTime(observation.ObservedAt))
	fmt.Fprintf(builder, "  attempted=%s  store_updated=%s  revision=%s\n", hermesStoredTime(&observation.AttemptedAt), hermesStoredTime(observation.StoreUpdatedAt), firstNonEmpty(observation.Source.Revision, "unknown"))
	if observation.Availability != hermesschedules.Available {
		renderEmpty(builder, "Native inventory unavailable; this is not an empty schedule list.")
		return
	}
	if len(observation.Jobs) == 0 {
		renderEmpty(builder, "No jobs in the observed native store.")
	}
	for _, job := range observation.Jobs {
		enabled := "unknown"
		if job.Enabled != nil {
			enabled = fmt.Sprint(*job.Enabled)
		}
		fmt.Fprintf(builder, "  %s  enabled=%s  stored_state=%s  kind=%s  %s\n", job.ID, enabled, firstNonEmpty(string(job.NativeState), "unknown"), firstNonEmpty(string(job.Schedule.Kind), "unknown"), hermesScheduleValue(job.Schedule))
		fmt.Fprintf(builder, "    paused_at=%s  stored_next=%s  last_result=%s  result_at=%s\n", hermesStoredTime(job.PausedAt), hermesStoredTime(job.NextRunAt), firstNonEmpty(string(job.LastResult), "unknown"), hermesStoredTime(job.LastRunAt))
		delivery := "unknown"
		if job.DeliveryErrorReported != nil && *job.DeliveryErrorReported {
			delivery = "error_reported"
		}
		fmt.Fprintf(builder, "    delivery=%s\n", delivery)
	}
}

func hermesScheduleValue(schedule hermesschedules.Schedule) string {
	switch schedule.Kind {
	case hermesschedules.Cron:
		if schedule.Expression != nil {
			return fmt.Sprintf("expression=%q", *schedule.Expression)
		}
		return "expression=unknown"
	case hermesschedules.Interval:
		if schedule.Minutes != nil {
			return fmt.Sprintf("minutes=%d", *schedule.Minutes)
		}
		return "minutes=unknown"
	case hermesschedules.Once:
		return "run_at=" + hermesStoredTime(schedule.RunAt)
	default:
		return "schedule=unknown"
	}
}

func hermesStoredTime(value *time.Time) string {
	if value == nil || value.IsZero() {
		return "unknown"
	}
	return value.Format(time.RFC3339Nano)
}
