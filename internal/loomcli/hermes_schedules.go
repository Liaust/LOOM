package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/hermesschedules"
)

func newHermesSchedulesCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use: "hermes", Short: "Observe Hermes-owned schedules (read-only)", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			_, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.HermesSchedules(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "automation", "hermes", "Could not observe Hermes schedules.", err))
			}
			render := func(view *cobra.Command) error {
				if opts.jsonOutput {
					return json.NewEncoder(view.OutOrStdout()).Encode(envelope)
				}
				var b strings.Builder
				renderHermesScheduleInventory(&b, envelope.Data)
				_, err := fmt.Fprint(view.OutOrStdout(), b.String())
				return err
			}
			if envelope.Data.Availability != hermesschedules.Available {
				return renderOwnedCLIError(cmd, fmt.Errorf("Hermes schedule inventory is %s", envelope.Data.Availability), render)
			}
			return render(cmd)
		},
	}
}

func renderHermesScheduleInventory(b *strings.Builder, data hermesschedules.Observation) {
	fmt.Fprintf(b, "Hermes schedules: %s  owner=hermes  host=%s  profile=%s  revision=%s\n", data.Availability, hermesKnown(data.Source.Host), hermesKnown(data.Source.Profile), hermesKnown(data.Source.Revision))
	fmt.Fprintln(b, "Read-only native inventory; scheduler_liveness=unknown  timezone=unknown  current_completion=unknown.")
	fmt.Fprintf(b, "Attempted: %s  Observed: %s  Store updated: %s\n", hermesStoredTime(&data.AttemptedAt), hermesStoredTime(data.ObservedAt), hermesStoredTime(data.StoreUpdatedAt))
	if data.Availability != hermesschedules.Available {
		fmt.Fprintln(b, "Native inventory unavailable; no empty-store conclusion can be drawn.")
		return
	}
	for _, job := range data.Jobs {
		enabled := "unknown"
		if job.Enabled != nil {
			enabled = fmt.Sprint(*job.Enabled)
		}
		fmt.Fprintf(b, "%s  enabled=%s  stored_state=%s  kind=%s  %s\n", job.ID, enabled, hermesKnown(string(job.NativeState)), hermesKnown(string(job.Schedule.Kind)), hermesScheduleValue(job.Schedule))
		if job.Project != nil {
			fmt.Fprintf(b, "  project=%s resource=%s profile=%s retired=%t\n", job.Project.ProjectID, job.Project.Resource, job.Project.Profile, job.Project.Retired)
		}
		fmt.Fprintf(b, "  paused_at=%s  stored_next=%s  last_result=%s  result_at=%s\n", hermesStoredTime(job.PausedAt), hermesStoredTime(job.NextRunAt), hermesKnown(string(job.LastResult)), hermesStoredTime(job.LastRunAt))
		delivery := "unknown"
		if job.DeliveryErrorReported != nil && *job.DeliveryErrorReported {
			delivery = "error_reported"
		}
		fmt.Fprintf(b, "  delivery=%s\n", delivery)
	}
	if len(data.Jobs) == 0 {
		fmt.Fprintln(b, "No native jobs in the observed store.")
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

func hermesKnown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
