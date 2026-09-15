package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
)

func newSchedulesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedules",
		Short: "Manage LOOM schedules",
	}

	createOpts := struct {
		automation.CreateScheduleInput
		inputText      string
		inputFile      string
		oneShot        string
		cron           string
		every          string
		timeout        string
		latenessWindow string
		idempotencyKey string
	}{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a main-owned schedule",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := createOpts.CreateScheduleInput
			selected := 0
			for _, flag := range []string{"one-shot", "every", "cron"} {
				if cmd.Flags().Changed(flag) {
					selected++
				}
			}
			if selected != 1 {
				return renderError(cmd, opts, correlationID, loomerrors.New("schedule.create_invalid", "automation", "schedule_kind", "Choose exactly one of --one-shot, --every or --cron."))
			}
			switch {
			case cmd.Flags().Changed("one-shot"):
				input.ScheduleKind, input.ScheduleExpr = automation.ScheduleKindOneShot, createOpts.oneShot
			case cmd.Flags().Changed("every"):
				input.ScheduleKind, input.ScheduleExpr = automation.ScheduleKindInterval, createOpts.every
			case cmd.Flags().Changed("cron"):
				if !cmd.Flags().Changed("timezone") {
					return renderError(cmd, opts, correlationID, loomerrors.New("schedule.create_invalid", "automation", "timezone", "Pass an explicit --timezone with --cron."))
				}
				input.ScheduleKind, input.ScheduleExpr = automation.ScheduleKindCron, createOpts.cron
				if _, err := automation.ParseScheduleExpressionInTimezone(input.ScheduleKind, input.ScheduleExpr, input.Timezone, time.Now().UTC()); err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("schedule.create_invalid", "automation", "cron", "Calendar schedule is invalid.", err))
				}
			}
			rawInput, err := readCapabilityInput(createOpts.inputText, createOpts.inputFile)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("schedule.input_invalid", "automation", "input", "Schedule input is invalid.", err))
			}
			input.InputJSON = rawInput
			if createOpts.timeout != "" {
				seconds, err := durationSeconds(createOpts.timeout)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("schedule.timeout_invalid", "automation", "timeout", "Schedule timeout is invalid.", err))
				}
				input.TimeoutSeconds = seconds
			}
			if createOpts.latenessWindow != "" {
				seconds, err := durationSeconds(createOpts.latenessWindow)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("schedule.lateness_invalid", "automation", "lateness_window", "Schedule lateness window is invalid.", err))
				}
				input.LatenessWindowSecs = seconds
			}
			if !input.DryRun {
				client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "schedules.create."+input.ScheduleKey)
			}
			envelope, err := client.CreateSchedule(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create schedule.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput && !envelope.Data.DryRun {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Schedule.ScheduleID)
				return nil
			}
			renderScheduleDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.ScheduleKey, "key", "", "schedule key")
	createCmd.Flags().StringVar(&createOpts.DisplayName, "name", "", "schedule display name")
	createCmd.Flags().StringVar(&createOpts.Description, "description", "", "schedule description")
	createCmd.Flags().StringVar(&createOpts.TargetCapability, "target", "", "target capability address")
	createCmd.Flags().StringVar(&createOpts.inputText, "input-json", "", "capability input JSON object")
	createCmd.Flags().StringVar(&createOpts.inputFile, "input-file", "", "path to capability input JSON object")
	createCmd.Flags().StringVar(&createOpts.oneShot, "one-shot", "", "one-shot fire time, RFC3339 or now")
	createCmd.Flags().StringVar(&createOpts.every, "every", "", "interval duration, for example 15m or 1h")
	createCmd.Flags().StringVar(&createOpts.cron, "cron", "", "five-field cron expression (requires explicit --timezone; defaults disabled)")
	createCmd.Flags().BoolVar(&createOpts.DryRun, "dry-run", false, "validate and preview without creating or activating a schedule")
	createCmd.Flags().StringVar(&createOpts.Status, "status", "", "initial status: active, paused or disabled (cron defaults disabled)")
	createCmd.Flags().StringVar(&createOpts.Timezone, "timezone", "UTC", "schedule timezone")
	createCmd.Flags().StringVar(&createOpts.ScopeRef, "scope", "", "scope ID, key, or slug")
	createCmd.Flags().StringVar(&createOpts.ProjectRef, "project", "", "project ID or slug")
	createCmd.Flags().StringVar(&createOpts.RunAsActorRef, "run-as", "", "actor ID or key to run as")
	createCmd.Flags().StringVar(&createOpts.MisfirePolicy, "misfire", automation.MisfireMarkMissed, "misfire policy")
	createCmd.Flags().StringVar(&createOpts.latenessWindow, "lateness-window", "", "lateness window duration")
	createCmd.Flags().StringVar(&createOpts.ConcurrencyPolicy, "concurrency", automation.ConcurrencyAllowParallel, "concurrency policy")
	createCmd.Flags().StringVar(&createOpts.timeout, "timeout", "", "capability dispatch timeout duration")
	createCmd.Flags().IntVar(&createOpts.MaxAttempts, "max-attempts", 1, "maximum dispatch attempts")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(createCmd)

	listFilter := automation.ScheduleFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List schedules",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListSchedules(ctx, correlationID, listFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list schedules.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScheduleList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listFilter.Limit, "limit", 50, "maximum schedules to return")
	listCmd.Flags().StringVar(&listFilter.Status, "status", "", "filter by status")
	listCmd.Flags().StringVar(&listFilter.AutomationRef, "automation", "", "filter by automation")
	listCmd.Flags().StringVar(&listFilter.ProjectRef, "project", "", "filter by project")
	cmd.AddCommand(listCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Summarize schedule automation state",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ScheduleStatus(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not summarize schedules.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScheduleStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(statusCmd)

	return cmd
}

func newScheduleCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Inspect or update one LOOM schedule",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <schedule-ref>",
		Short: "Inspect a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetSchedule(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect schedule.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScheduleDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	addScheduleStatusCommand(cmd, opts, "pause")
	addScheduleStatusCommand(cmd, opts, "resume")
	addScheduleStatusCommand(cmd, opts, "disable")

	fireOpts := struct {
		reason         string
		idempotencyKey string
		enqueueOnly    bool
	}{}
	fireCmd := &cobra.Command{
		Use:   "fire <schedule-ref>",
		Short: "Create and dispatch a manual schedule fire",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, fireOpts.idempotencyKey, "schedule.fire."+args[0])
			envelope, err := client.FireScheduleNow(ctx, correlationID, args[0], automation.FireScheduleInput{Reason: fireOpts.reason, DispatchNow: !fireOpts.enqueueOnly})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not fire schedule.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Fire.ScheduleFireID)
				return nil
			}
			renderScheduleFireResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	fireCmd.Flags().StringVar(&fireOpts.reason, "reason", "", "reason for manual fire")
	fireCmd.Flags().StringVar(&fireOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	fireCmd.Flags().BoolVar(&fireOpts.enqueueOnly, "enqueue-only", false, "create the fire and pending invocation without running the dispatcher immediately")
	cmd.AddCommand(fireCmd)

	fireFilter := automation.ScheduleFireFilter{}
	firesCmd := &cobra.Command{
		Use:   "fires <schedule-ref>",
		Short: "List fires for one schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			filter := fireFilter
			filter.ScheduleRef = args[0]
			envelope, err := client.ListScheduleFires(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list schedule fires.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderScheduleFireList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	firesCmd.Flags().IntVar(&fireFilter.Limit, "limit", 50, "maximum fires to return")
	firesCmd.Flags().StringVar(&fireFilter.Status, "status", "", "filter by fire status")
	cmd.AddCommand(firesCmd)
	return cmd
}

func addScheduleStatusCommand(parent *cobra.Command, opts *options, action string) {
	var reason string
	cmd := &cobra.Command{
		Use:   action + " <schedule-ref>",
		Short: action + " a schedule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := automation.UpdateScheduleStatusInput{Reason: reason}
			switch action {
			case "pause":
				envelope, err := client.PauseSchedule(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update schedule.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderScheduleDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			case "resume":
				envelope, err := client.ResumeSchedule(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update schedule.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderScheduleDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			default:
				envelope, err := client.DisableSchedule(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update schedule.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderScheduleDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the status change")
	parent.AddCommand(cmd)
}

func newAutomationsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "automations", Short: "List LOOM automations"}
	filter := automation.AutomationFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List automations",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListAutomations(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list automations.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAutomationList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum automations to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	listCmd.Flags().StringVar(&filter.SourceKind, "source-kind", "", "filter by source kind")
	cmd.AddCommand(listCmd)
	return cmd
}

func newAutomationCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "automation", Short: "Inspect one LOOM automation"}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <automation-ref>",
		Short: "Inspect an automation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetAutomation(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect automation.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderAutomationDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func newIntegrationsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "integrations", Short: "Manage direct-event integrations"}

	createOpts := struct {
		automation.CreateIntegrationInput
		allowedScopes       []string
		allowedProjects     []string
		allowedEndpointRefs []string
		idempotencyKey      string
	}{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create an external integration identity",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := createOpts.CreateIntegrationInput
			input.AllowedScopes = jsonStringArray(createOpts.allowedScopes)
			input.AllowedProjects = jsonStringArray(createOpts.allowedProjects)
			input.AllowedEndpointRefs = jsonStringArray(createOpts.allowedEndpointRefs)
			client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "integrations.create."+input.IntegrationKey)
			envelope, err := client.CreateIntegration(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create integration.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Integration.IntegrationID)
				return nil
			}
			renderIntegrationDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.IntegrationKey, "key", "", "integration key")
	createCmd.Flags().StringVar(&createOpts.DisplayName, "name", "", "integration display name")
	createCmd.Flags().StringVar(&createOpts.Description, "description", "", "integration description")
	createCmd.Flags().IntVar(&createOpts.MainAuthLevel, "main-auth-level", 3, "authorization level for main node")
	createCmd.Flags().StringArrayVar(&createOpts.allowedScopes, "allowed-scope", nil, "allowed scope ref")
	createCmd.Flags().StringArrayVar(&createOpts.allowedProjects, "allowed-project", nil, "allowed project ref")
	createCmd.Flags().StringArrayVar(&createOpts.allowedEndpointRefs, "allowed-endpoint", nil, "allowed endpoint ref")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(createCmd)

	listFilter := automation.IntegrationFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List integrations",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListIntegrations(ctx, correlationID, listFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list integrations.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIntegrationList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listFilter.Limit, "limit", 50, "maximum integrations to return")
	listCmd.Flags().StringVar(&listFilter.Status, "status", "", "filter by status")
	cmd.AddCommand(listCmd)
	return cmd
}

func newIntegrationCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "integration", Short: "Inspect or update one integration"}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <integration-ref>",
		Short: "Inspect an integration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetIntegration(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect integration.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIntegrationDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	addIntegrationStatusCommand(cmd, opts, "disable")
	addIntegrationStatusCommand(cmd, opts, "revoke")
	cmd.AddCommand(newIntegrationAuthCommand(opts))
	return cmd
}

func addIntegrationStatusCommand(parent *cobra.Command, opts *options, action string) {
	var reason string
	cmd := &cobra.Command{
		Use:   action + " <integration-ref>",
		Short: action + " an integration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := automation.UpdateIntegrationStatusInput{Reason: reason}
			var envelope any
			if action == "revoke" {
				result, err := client.RevokeIntegration(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update integration.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				renderIntegrationDetail(cmd, result.Data)
				renderResponseMeta(cmd, opts, result.Meta)
				envelope = result
			} else {
				result, err := client.DisableIntegration(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update integration.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				renderIntegrationDetail(cmd, result.Data)
				renderResponseMeta(cmd, opts, result.Meta)
				envelope = result
			}
			_ = envelope
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the status change")
	parent.AddCommand(cmd)
}

func newIntegrationAuthCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "auth", Short: "Manage integration auth profiles"}
	createOpts := struct {
		automation.CreateIntegrationAuthProfileInput
		allowedEndpointRefs []string
		idempotencyKey      string
	}{}
	createCmd := &cobra.Command{
		Use:   "create <integration-ref>",
		Short: "Create an integration auth profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := createOpts.CreateIntegrationAuthProfileInput
			input.AllowedEndpointRefs = jsonStringArray(createOpts.allowedEndpointRefs)
			client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "integration.auth.create."+args[0])
			envelope, err := client.CreateIntegrationAuthProfile(ctx, correlationID, args[0], input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create integration auth profile.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Token)
				return nil
			}
			renderIntegrationAuthProfileCreateResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.DisplayName, "name", "default", "auth profile display name")
	createCmd.Flags().StringVar(&createOpts.AuthKind, "kind", automation.IntegrationAuthBearerHeader, "auth kind")
	createCmd.Flags().StringArrayVar(&createOpts.allowedEndpointRefs, "allowed-endpoint", nil, "allowed endpoint ref")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(createCmd)

	listFilter := automation.IntegrationAuthProfileFilter{}
	listCmd := &cobra.Command{
		Use:   "list <integration-ref>",
		Short: "List integration auth profiles",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			filter := listFilter
			filter.IntegrationRef = args[0]
			envelope, err := client.ListIntegrationAuthProfiles(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list integration auth profiles.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIntegrationAuthProfileList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listFilter.Limit, "limit", 50, "maximum auth profiles to return")
	listCmd.Flags().StringVar(&listFilter.Status, "status", "", "filter by status")
	cmd.AddCommand(listCmd)

	var revokeReason string
	revokeCmd := &cobra.Command{
		Use:   "revoke <auth-profile-ref>",
		Short: "Revoke an integration auth profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.RevokeIntegrationAuthProfile(ctx, correlationID, args[0], automation.UpdateIntegrationAuthProfileStatusInput{Reason: revokeReason})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not revoke integration auth profile.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIntegrationAuthProfile(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	revokeCmd.Flags().StringVar(&revokeReason, "reason", "", "reason for revocation")
	cmd.AddCommand(revokeCmd)
	return cmd
}

func newDirectEventCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "direct-event", Short: "Manage direct-event automation endpoints"}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <direct-event-ref>",
		Short: "Inspect a direct event",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetDirectEvent(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect direct event.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "raw <direct-event-ref>",
		Short: "Show stored direct-event raw payload",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetDirectEventRawPayload(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect direct event raw payload.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventRawPayload(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(newDirectEventEndpointsCommand(opts))
	cmd.AddCommand(newDirectEventEndpointCommand(opts))
	cmd.AddCommand(newDirectEventIngestCommand(opts))
	return cmd
}

func newDirectEventsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "direct-events", Short: "List direct-event occurrences"}
	filter := automation.DirectEventFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List direct events",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListDirectEvents(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list direct events.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addDirectEventFilterFlags(listCmd, &filter)
	cmd.AddCommand(listCmd)

	failuresFilter := automation.DirectEventFilter{}
	failuresCmd := &cobra.Command{
		Use:   "failures",
		Short: "List failed direct events",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListDirectEventFailures(ctx, correlationID, failuresFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list failed direct events.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addDirectEventFilterFlags(failuresCmd, &failuresFilter)
	cmd.AddCommand(failuresCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Summarize direct-event automation state",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.DirectEventStatus(ctx, correlationID)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not summarize direct events.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(statusCmd)
	return cmd
}

func addDirectEventFilterFlags(cmd *cobra.Command, filter *automation.DirectEventFilter) {
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum direct events to return")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&filter.EndpointRef, "endpoint", "", "filter by endpoint")
	cmd.Flags().StringVar(&filter.IntegrationRef, "integration", "", "filter by integration")
	cmd.Flags().StringVar(&filter.AutomationRef, "automation", "", "filter by automation")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project")
}

func newDirectEventEndpointsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "endpoints", Short: "Manage direct-event endpoints"}
	createOpts := struct {
		automation.CreateDirectEventEndpointInput
		mapFlags            []string
		requiredFields      []string
		authProfileRefs     []string
		timeout             string
		syncWaitTimeout     string
		idempotencyStrategy string
		idempotencyPath     string
		idempotencyHeader   string
		idempotencyKey      string
	}{}
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a direct-event endpoint",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := createOpts.CreateDirectEventEndpointInput
			mappingProfile, err := mappingProfileFromFlags(createOpts.mapFlags, createOpts.requiredFields)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.mapping_invalid", "automation", "mapping", "Direct event mapping is invalid.", err))
			}
			input.MappingProfile = mappingProfile
			input.AuthProfileRefs = jsonStringArray(createOpts.authProfileRefs)
			input.IdempotencyProfile = mustJSONMessage(automation.IdempotencyProfile{
				Strategy: createOpts.idempotencyStrategy,
				Path:     createOpts.idempotencyPath,
				Header:   createOpts.idempotencyHeader,
			})
			if createOpts.syncWaitTimeout != "" {
				seconds, err := durationSeconds(createOpts.syncWaitTimeout)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.sync_wait_timeout_invalid", "automation", "sync_wait_timeout", "Direct event sync-wait timeout is invalid.", err))
				}
				input.CommunicationProfile = mustJSONMessage(automation.CommunicationProfile{
					ResponseMode:           input.ResponseMode,
					SyncWaitTimeoutSeconds: seconds,
				})
			}
			if createOpts.timeout != "" {
				seconds, err := durationSeconds(createOpts.timeout)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.timeout_invalid", "automation", "timeout", "Direct event timeout is invalid.", err))
				}
				input.TimeoutSeconds = seconds
			}
			client, _ = withEffectIdempotency(client, createOpts.idempotencyKey, "direct_event.endpoints.create."+input.EndpointSlug)
			envelope, err := client.CreateDirectEventEndpoint(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not create direct event endpoint.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Endpoint.EndpointID)
				return nil
			}
			renderDirectEventEndpointDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	createCmd.Flags().StringVar(&createOpts.EndpointSlug, "slug", "", "endpoint slug")
	createCmd.Flags().StringVar(&createOpts.DisplayName, "name", "", "endpoint display name")
	createCmd.Flags().StringVar(&createOpts.Description, "description", "", "endpoint description")
	createCmd.Flags().StringVar(&createOpts.IntegrationRef, "integration", "", "integration ID or key")
	createCmd.Flags().StringVar(&createOpts.EventType, "event-type", "", "event type")
	createCmd.Flags().StringVar(&createOpts.TargetCapability, "target", "", "target capability address")
	createCmd.Flags().StringVar(&createOpts.ResponseMode, "response-mode", automation.DirectEventResponseAccepted, "response mode")
	createCmd.Flags().StringArrayVar(&createOpts.mapFlags, "map", nil, "mapping in destination=$.source.path form")
	createCmd.Flags().StringArrayVar(&createOpts.requiredFields, "required", nil, "required mapped input field")
	createCmd.Flags().StringArrayVar(&createOpts.authProfileRefs, "auth-profile", nil, "required auth profile ID")
	createCmd.Flags().StringVar(&createOpts.ScopeRef, "scope", "", "scope ID, key, or slug")
	createCmd.Flags().StringVar(&createOpts.ProjectRef, "project", "", "project ID or slug")
	createCmd.Flags().StringVar(&createOpts.timeout, "timeout", "", "capability dispatch timeout duration")
	createCmd.Flags().StringVar(&createOpts.syncWaitTimeout, "sync-wait-timeout", "", "HTTP sync-wait response timeout duration")
	createCmd.Flags().IntVar(&createOpts.MaxAttempts, "max-attempts", 1, "maximum dispatch attempts")
	createCmd.Flags().StringVar(&createOpts.idempotencyStrategy, "idempotency-strategy", automation.DirectEventIdempotencyNone, "event idempotency strategy")
	createCmd.Flags().StringVar(&createOpts.idempotencyPath, "idempotency-path", "", "payload path for payload_path idempotency")
	createCmd.Flags().StringVar(&createOpts.idempotencyHeader, "idempotency-header", "", "header name for header idempotency")
	createCmd.Flags().StringVar(&createOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(createCmd)

	listFilter := automation.DirectEventEndpointFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List direct-event endpoints",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListDirectEventEndpoints(ctx, correlationID, listFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list direct event endpoints.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventEndpointList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&listFilter.Limit, "limit", 50, "maximum endpoints to return")
	listCmd.Flags().StringVar(&listFilter.Status, "status", "", "filter by status")
	listCmd.Flags().StringVar(&listFilter.IntegrationRef, "integration", "", "filter by integration")
	listCmd.Flags().StringVar(&listFilter.AutomationRef, "automation", "", "filter by automation")
	listCmd.Flags().StringVar(&listFilter.ProjectRef, "project", "", "filter by project")
	cmd.AddCommand(listCmd)
	return cmd
}

func newDirectEventEndpointCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "endpoint", Short: "Inspect or update one direct-event endpoint"}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <endpoint-ref>",
		Short: "Inspect a direct-event endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetDirectEventEndpoint(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect direct event endpoint.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDirectEventEndpointDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	addDirectEventEndpointStatusCommand(cmd, opts, "pause")
	addDirectEventEndpointStatusCommand(cmd, opts, "resume")
	addDirectEventEndpointStatusCommand(cmd, opts, "disable")

	previewOpts := struct {
		bodyJSON    string
		headersJSON string
		queryJSON   string
	}{}
	previewCmd := &cobra.Command{
		Use:   "preview <endpoint-ref>",
		Short: "Preview endpoint mapping against sample event data",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			body, err := jsonObjectText(previewOpts.bodyJSON, "body-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.preview_invalid", "automation", "body-json", "Preview body JSON is invalid.", err))
			}
			headers, err := jsonObjectText(previewOpts.headersJSON, "headers-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.preview_invalid", "automation", "headers-json", "Preview headers JSON is invalid.", err))
			}
			query, err := jsonObjectText(previewOpts.queryJSON, "query-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.preview_invalid", "automation", "query-json", "Preview query JSON is invalid.", err))
			}
			envelope, err := client.PreviewDirectEventEndpointMapping(ctx, correlationID, args[0], automation.MappingPreviewInput{
				BodyJSON:    body,
				HeadersJSON: headers,
				QueryJSON:   query,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not preview direct event endpoint.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderMappingPreview(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	previewCmd.Flags().StringVar(&previewOpts.bodyJSON, "body-json", "{}", "sample body JSON object")
	previewCmd.Flags().StringVar(&previewOpts.headersJSON, "headers-json", "{}", "sample headers JSON object")
	previewCmd.Flags().StringVar(&previewOpts.queryJSON, "query-json", "{}", "sample query JSON object")
	cmd.AddCommand(previewCmd)
	return cmd
}

func newDirectEventIngestCommand(opts *options) *cobra.Command {
	ingestOpts := struct {
		bodyJSON    string
		bearerToken string
		queryToken  string
	}{}
	cmd := &cobra.Command{
		Use:   "ingest <endpoint-slug>",
		Short: "Submit a direct event through the local daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			body, err := jsonObjectText(ingestOpts.bodyJSON, "body-json")
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("direct_event.ingest_invalid", "automation", "body-json", "Direct event body JSON is invalid.", err))
			}
			envelope, err := client.IngestDirectEvent(ctx, correlationID, args[0], automation.DirectEventLocalIngestInput{
				BodyJSON:    body,
				BearerToken: ingestOpts.bearerToken,
				QueryToken:  ingestOpts.queryToken,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not ingest direct event.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.DirectEvent.DirectEventID)
				return nil
			}
			renderDirectEventIngestResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&ingestOpts.bodyJSON, "body-json", "{}", "direct event JSON body")
	cmd.Flags().StringVar(&ingestOpts.bearerToken, "bearer-token", "", "bearer token for endpoint auth")
	cmd.Flags().StringVar(&ingestOpts.queryToken, "query-token", "", "query token for endpoint auth")
	return cmd
}

func addDirectEventEndpointStatusCommand(parent *cobra.Command, opts *options, action string) {
	var reason string
	cmd := &cobra.Command{
		Use:   action + " <endpoint-ref>",
		Short: action + " a direct-event endpoint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			input := automation.UpdateDirectEventEndpointStatusInput{Reason: reason}
			switch action {
			case "pause":
				envelope, err := client.PauseDirectEventEndpoint(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update direct event endpoint.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderDirectEventEndpointDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			case "resume":
				envelope, err := client.ResumeDirectEventEndpoint(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update direct event endpoint.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderDirectEventEndpointDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			default:
				envelope, err := client.DisableDirectEventEndpoint(ctx, correlationID, args[0], input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update direct event endpoint.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderDirectEventEndpointDetail(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "reason for the status change")
	parent.AddCommand(cmd)
}

func newInvocationsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "invocations", Short: "List automation invocations"}
	filter := automation.InvocationFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List automation invocations",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListInvocations(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list invocations.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderInvocationList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum invocations to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	listCmd.Flags().StringVar(&filter.AutomationRef, "automation", "", "filter by automation")
	listCmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project")
	listCmd.Flags().StringVar(&filter.SourceKind, "source-kind", "", "filter by source kind")
	cmd.AddCommand(listCmd)

	failuresFilter := automation.InvocationFilter{}
	failuresCmd := &cobra.Command{
		Use:   "failures",
		Short: "List failed or attention-required automation invocations",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListInvocationFailures(ctx, correlationID, failuresFilter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list invocation failures.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderInvocationList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	failuresCmd.Flags().IntVar(&failuresFilter.Limit, "limit", 50, "maximum invocations to return")
	failuresCmd.Flags().StringVar(&failuresFilter.AutomationRef, "automation", "", "filter by automation")
	failuresCmd.Flags().StringVar(&failuresFilter.ProjectRef, "project", "", "filter by project")
	failuresCmd.Flags().StringVar(&failuresFilter.SourceKind, "source-kind", "", "filter by source kind")
	cmd.AddCommand(failuresCmd)
	return cmd
}

func newInvocationCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "invocation", Short: "Inspect one automation invocation"}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <invocation-ref>",
		Short: "Inspect an invocation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetInvocation(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect invocation.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderInvocation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	return cmd
}

func jsonStringArray(values []string) json.RawMessage {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	raw, err := json.Marshal(cleaned)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}

func mustJSONMessage(value any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(raw)
}

func jsonObjectText(value, name string) (json.RawMessage, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return json.RawMessage(`{}`), nil
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(value), &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", name)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func mappingProfileFromFlags(mapFlags, requiredFields []string) (json.RawMessage, error) {
	mappings := map[string]string{}
	for _, item := range mapFlags {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("mapping %q must be destination=expression", item)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return nil, fmt.Errorf("mapping %q must include destination and expression", item)
		}
		mappings[key] = value
	}
	required := make([]string, 0, len(requiredFields))
	for _, field := range requiredFields {
		field = strings.TrimSpace(field)
		if field != "" {
			required = append(required, field)
		}
	}
	return mustJSONMessage(automation.MappingProfile{
		FieldMappings: mappings,
		Required:      required,
	}), nil
}

func durationSeconds(value string) (int, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return int(duration.Seconds()), nil
}

func renderScheduleDetail(cmd *cobra.Command, detail automation.ScheduleDetail) {
	schedule := detail.Schedule
	if detail.DryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "Preview only: no schedule created or activated.")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Schedule: %s (%s)\n", schedule.ScheduleKey, schedule.ScheduleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", schedule.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s %s\nTimezone: %s\n", schedule.ScheduleKind, schedule.ScheduleExpr, schedule.Timezone)
	fmt.Fprintf(cmd.OutOrStdout(), "Automation: %s\n", detail.Automation.AutomationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Next fire: %s\n", timePtrText(schedule.NextFireAt))
}

func renderScheduleList(cmd *cobra.Command, schedules []automation.Schedule) {
	if len(schedules) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No schedules.")
		return
	}
	for _, schedule := range schedules {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n", schedule.ScheduleKey, schedule.Status, schedule.ScheduleKind, schedule.ScheduleExpr, timePtrText(schedule.NextFireAt))
	}
}

func renderScheduleStatus(cmd *cobra.Command, status automation.ScheduleStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Schedules: active=%d paused=%d disabled=%d completed=%d due=%d\n",
		status.ActiveScheduleCount,
		status.PausedScheduleCount,
		status.DisabledScheduleCount,
		status.CompletedScheduleCount,
		status.DueScheduleCount,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Fires: missed=%d\n", status.MissedFireCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Invocations: pending=%d failed=%d\n", status.PendingInvocationCount, status.FailedInvocationCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Next fire: %s\n", timePtrText(status.NextFireAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Oldest pending invocation: %s\n", timePtrText(status.OldestPendingInvocationAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Workers: %s %s\n", status.SchedulerWorkerKey, status.DispatcherWorkerKey)
}

func renderScheduleFireList(cmd *cobra.Command, fires []automation.ScheduleFire) {
	if len(fires) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No schedule fires.")
		return
	}
	for _, fire := range fires {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", fire.ScheduleFireID, fire.Status, fire.ScheduledFor.Format(time.RFC3339), ptrOrDash(fire.InvocationID))
	}
}

func renderScheduleFireResult(cmd *cobra.Command, result automation.FireScheduleResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Schedule: %s (%s)\n", result.Schedule.ScheduleKey, result.Schedule.ScheduleID)
	fmt.Fprintf(cmd.OutOrStdout(), "Fire: %s %s\n", result.Fire.ScheduleFireID, result.Fire.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Scheduled for: %s\n", result.Fire.ScheduledFor.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Invocation: %s %s\n", result.Invocation.InvocationID, result.Invocation.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.Invocation.TargetCapability)
	if result.Dispatch != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Dispatch: claimed=%d succeeded=%d failed=%d manual_action=%d\n", result.Dispatch.Claimed, result.Dispatch.Succeeded, result.Dispatch.Failed, result.Dispatch.ManualAction)
	}
	if result.DispatchError != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Dispatch error: %s\n", result.DispatchError)
	}
}

func renderAutomationList(cmd *cobra.Command, automations []automation.Automation) {
	if len(automations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No automations.")
		return
	}
	for _, item := range automations {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", item.AutomationKey, item.Status, item.SourceKind, item.AutomationID)
	}
}

func renderAutomationDetail(cmd *cobra.Command, detail automation.AutomationDetail) {
	item := detail.Automation
	fmt.Fprintf(cmd.OutOrStdout(), "Automation: %s (%s)\n", item.AutomationKey, item.AutomationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", item.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", item.SourceKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Schedules: %d\n", len(detail.Schedules))
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event endpoints: %d\n", len(detail.DirectEventEndpoints))
}

func renderIntegrationList(cmd *cobra.Command, integrations []automation.Integration) {
	if len(integrations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No integrations.")
		return
	}
	for _, integration := range integrations {
		level := "-"
		if integration.MainAuthLevel != nil {
			level = fmt.Sprintf("%d", *integration.MainAuthLevel)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tlevel=%s\t%s\n", integration.IntegrationKey, integration.Status, level, integration.IntegrationID)
	}
}

func renderIntegrationDetail(cmd *cobra.Command, detail automation.IntegrationDetail) {
	integration := detail.Integration
	fmt.Fprintf(cmd.OutOrStdout(), "Integration: %s (%s)\n", integration.IntegrationKey, integration.IntegrationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", integration.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Actor: %s\n", integration.ActorID)
	if integration.MainAuthLevel != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Main auth level: %d\n", *integration.MainAuthLevel)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Auth profiles: %d\n", len(detail.AuthProfiles))
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event endpoints: %d\n", len(detail.Endpoints))
}

func renderIntegrationAuthProfileCreateResult(cmd *cobra.Command, result automation.IntegrationAuthProfileCreateResult) {
	renderIntegrationAuthProfile(cmd, result.Profile)
	if result.Token != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Token: %s\n", result.Token)
	}
}

func renderIntegrationAuthProfileList(cmd *cobra.Command, profiles []automation.IntegrationAuthProfile) {
	if len(profiles) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No integration auth profiles.")
		return
	}
	for _, profile := range profiles {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", profile.AuthProfileID, profile.Status, profile.AuthKind, dashIfEmpty(profile.TokenLastFour))
	}
}

func renderIntegrationAuthProfile(cmd *cobra.Command, profile automation.IntegrationAuthProfile) {
	fmt.Fprintf(cmd.OutOrStdout(), "Auth profile: %s\n", profile.AuthProfileID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", profile.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", profile.AuthKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Token ending: %s\n", dashIfEmpty(profile.TokenLastFour))
}

func renderDirectEventEndpointList(cmd *cobra.Command, endpoints []automation.DirectEventEndpoint) {
	if len(endpoints) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No direct event endpoints.")
		return
	}
	for _, endpoint := range endpoints {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", endpoint.EndpointSlug, endpoint.Status, endpoint.EventType, endpoint.EndpointID)
	}
}

func renderDirectEventEndpointDetail(cmd *cobra.Command, detail automation.DirectEventEndpointDetail) {
	endpoint := detail.Endpoint
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event endpoint: %s (%s)\n", endpoint.EndpointSlug, endpoint.EndpointID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", endpoint.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Event type: %s\n", endpoint.EventType)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", endpoint.EndpointPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Response: %s\n", endpoint.ResponseMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Integration: %s (%s)\n", detail.Integration.IntegrationKey, endpoint.IntegrationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Automation: %s\n", endpoint.AutomationID)
}

func renderDirectEventList(cmd *cobra.Command, events []automation.DirectEvent) {
	if len(events) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No direct events.")
		return
	}
	for _, event := range events {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", event.DirectEventID, event.Status, event.PayloadHash, ptrOrDash(event.InvocationID))
	}
}

func renderDirectEventDetail(cmd *cobra.Command, detail automation.DirectEventDetail) {
	event := detail.DirectEvent
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event: %s\n", event.DirectEventID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", event.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s (%s)\n", detail.Endpoint.EndpointSlug, event.EndpointID)
	fmt.Fprintf(cmd.OutOrStdout(), "Integration: %s (%s)\n", detail.Integration.IntegrationKey, event.IntegrationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Automation: %s\n", event.AutomationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Invocation: %s\n", ptrOrDash(event.InvocationID))
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", ptrOrDash(event.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", ptrOrDash(event.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", ptrOrDash(event.JobID))
	fmt.Fprintf(cmd.OutOrStdout(), "Payload hash: %s\n", event.PayloadHash)
	if event.FailureCode != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Failure: %s %s\n", ptrOrDash(event.FailureCode), ptrOrDash(event.FailureMessage))
	}
}

func renderDirectEventRawPayload(cmd *cobra.Command, payload automation.DirectEventRawPayload) {
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event: %s\n", payload.DirectEventID)
	fmt.Fprintf(cmd.OutOrStdout(), "Payload hash: %s\n", payload.PayloadHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Received: %s\n", payload.ReceivedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Headers: %s\n", compactText(string(payload.HeadersJSON), 240))
	fmt.Fprintf(cmd.OutOrStdout(), "Query: %s\n", compactText(string(payload.QueryJSON), 240))
	fmt.Fprintf(cmd.OutOrStdout(), "Body: %s\n", compactText(string(payload.BodyJSON), 240))
}

func renderDirectEventIngestResult(cmd *cobra.Command, result automation.DirectEventIngestResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Direct event: %s\n", result.DirectEvent.DirectEventID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", result.Endpoint.EndpointSlug)
	fmt.Fprintf(cmd.OutOrStdout(), "Duplicate: %t\n", result.Duplicate)
	fmt.Fprintf(cmd.OutOrStdout(), "Response mode: %s\n", result.ResponseMode)
	fmt.Fprintf(cmd.OutOrStdout(), "Invocation: %s\n", ptrOrDash(result.DirectEvent.InvocationID))
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", ptrOrDash(result.DirectEvent.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", ptrOrDash(result.DirectEvent.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", ptrOrDash(result.DirectEvent.JobID))
}

func renderDirectEventStatus(cmd *cobra.Command, status automation.DirectEventStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoints: active=%d paused=%d disabled=%d\n",
		status.ActiveEndpointCount,
		status.PausedEndpointCount,
		status.DisabledEndpointCount,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Integrations: active=%d\n", status.ActiveIntegrationCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Events: accepted=%d mapping_pending=%d invocation_created=%d failed=%d\n",
		status.AcceptedCount,
		status.MappingPendingCount,
		status.InvocationCreatedCount,
		status.FailedCount,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Oldest accepted: %s\n", timePtrText(status.OldestAcceptedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Oldest mapping pending: %s\n", timePtrText(status.OldestMappingPendingAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Workers: %s %s\n", status.DirectEventIngestWorkerKey, status.DispatcherWorkerKey)
}

func renderMappingPreview(cmd *cobra.Command, result automation.MappingPreviewResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", result.Endpoint.EndpointSlug)
	fmt.Fprintf(cmd.OutOrStdout(), "Fields: %d\n", result.FieldCount)
	if len(result.MissingFields) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Missing: %s\n", strings.Join(result.MissingFields, ", "))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Input: %s\n", compactText(string(result.InputJSON), 240))
}

func renderInvocationList(cmd *cobra.Command, invocations []automation.Invocation) {
	if len(invocations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No invocations.")
		return
	}
	for _, invocation := range invocations {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\n", invocation.InvocationID, invocation.Status, invocation.TargetCapability, ptrOrDash(invocation.CapabilityCallID))
	}
}

func renderInvocation(cmd *cobra.Command, invocation automation.Invocation) {
	fmt.Fprintf(cmd.OutOrStdout(), "Invocation: %s\n", invocation.InvocationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", invocation.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s %s\n", invocation.SourceKind, invocation.SourceOccurrenceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", invocation.TargetCapability)
	fmt.Fprintf(cmd.OutOrStdout(), "Route: %s\n", ptrOrDash(invocation.RouteID))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability call: %s\n", ptrOrDash(invocation.CapabilityCallID))
	fmt.Fprintf(cmd.OutOrStdout(), "Job: %s\n", ptrOrDash(invocation.JobID))
}

func timePtrText(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
