package loomcli

import (
	"context"
	"encoding/json"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
)

func newScheduleTimingCommand(opts *options) *cobra.Command {
	var input automation.ScheduleTimingInput
	var window, key string
	var yes bool
	cmd := &cobra.Command{
		Use: "timing <schedule-ref>", Short: "Change a paused cron schedule's timing without replacing its identity or history",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			corr := correlation.Normalize(opts.correlationID)
			if !yes || !cmd.Flags().Changed("cron") || !cmd.Flags().Changed("timezone") || !cmd.Flags().Changed("misfire") {
				return renderError(cmd, opts, corr, loomerrors.New("schedule.timing_invalid", "automation", args[0], "Pass --cron, --timezone, --misfire and --yes; pause the schedule first."))
			}
			if _, err := automation.ParseScheduleExpressionInTimezone(automation.ScheduleKindCron, input.Cron, input.Timezone, time.Now().UTC()); err != nil {
				return renderError(cmd, opts, corr, loomerrors.Wrap("schedule.timing_invalid", "automation", args[0], "Calendar timing is invalid.", err))
			}
			if window != "" {
				seconds, err := durationSeconds(window)
				if err != nil {
					return renderError(cmd, opts, corr, loomerrors.Wrap("schedule.timing_invalid", "automation", args[0], "Lateness window is invalid.", err))
				}
				input.LatenessWindowSeconds = seconds
			}
			if _, err := automation.NormalizeMisfireProfile(automationJSON(input)); err != nil {
				return renderError(cmd, opts, corr, loomerrors.Wrap("schedule.timing_invalid", "automation", args[0], "Misfire policy is invalid.", err))
			}
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, corr, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, key, "schedule.timing."+args[0])
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			envelope, err := client.UpdateScheduleTiming(ctx, corr, args[0], input)
			if err != nil {
				return renderError(cmd, opts, corr, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update schedule timing.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScheduleDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Cron, "cron", "", "five-field cron expression")
	cmd.Flags().StringVar(&input.Timezone, "timezone", "", "explicit IANA timezone")
	cmd.Flags().StringVar(&input.MisfirePolicy, "misfire", "", "explicit misfire policy")
	cmd.Flags().StringVar(&window, "lateness-window", "", "bounded lateness duration, for example 120s")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "stable retry key")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm the timing change")
	return cmd
}

func automationJSON(input automation.ScheduleTimingInput) json.RawMessage {
	value, _ := json.Marshal(automation.MisfireProfile{Policy: input.MisfirePolicy, LatenessWindowSeconds: input.LatenessWindowSeconds})
	return value
}
