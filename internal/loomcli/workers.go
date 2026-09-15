package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/workers"
)

func newWorkersCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workers",
		Short: "List LOOM background workers",
	}

	filter := workers.WorkerFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List registered workers",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListWorkers(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list workers.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, worker := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), worker.WorkerKey)
				}
				return nil
			}
			renderWorkerList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of workers to return")
	listCmd.Flags().StringVar(&filter.Kind, "kind", "", "filter by worker kind")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by lifecycle status")
	listCmd.Flags().StringVar(&filter.Health, "health", "", "filter by health status")
	listCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by host or owner node")
	cmd.AddCommand(listCmd)
	return cmd
}

func newWorkerCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Inspect and run a LOOM background worker",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <worker-ref>",
		Short: "Inspect a worker by ID, key, or unique kind",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.InspectWorker(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect worker.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Instance.WorkerKey)
				return nil
			}
			renderWorkerDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	runFilter := workers.RunFilter{}
	runsCmd := &cobra.Command{
		Use:   "runs <worker-ref>",
		Short: "List worker run history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListWorkerRuns(ctx, commandCtx.CorrelationID, args[0], runFilter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list worker runs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, run := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), run.WorkerRunID)
				}
				return nil
			}
			renderWorkerRuns(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	runsCmd.Flags().IntVar(&runFilter.Limit, "limit", 50, "maximum number of runs to return")
	runsCmd.Flags().StringVar(&runFilter.Status, "status", "", "filter by run status")
	runsCmd.Flags().StringVar(&runFilter.Trigger, "trigger", "", "filter by trigger kind")
	cmd.AddCommand(runsCmd)
	cmd.AddCommand(newWorkerPolicyCommand(opts))

	var once bool
	var reason string
	var idempotencyKey string
	runCmd := &cobra.Command{
		Use:   "run <worker-ref>",
		Short: "Run a worker once",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.run_mode_required", "workers", args[0], "Pass --once to run a worker once."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "worker.run_once")
			ctx, cancel := workerRunCommandContext(cmd, args[0])
			defer cancel()

			envelope, err := client.RunWorkerOnce(ctx, commandCtx.CorrelationID, args[0], workers.RunOnceInput{
				Reason: reason,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run worker once.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Run.WorkerRunID)
				return nil
			}
			renderWorkerRunOnce(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	runCmd.Flags().BoolVar(&once, "once", false, "run this worker once")
	runCmd.Flags().StringVar(&reason, "reason", "", "reason recorded on the worker run")
	runCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for the run request")
	cmd.AddCommand(runCmd)

	var repairYes bool
	repairCmd := &cobra.Command{
		Use:   "repair-stale",
		Short: "Repair stale worker runs whose deadline or lease expired",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !repairYes {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.repair_confirmation_required", "workers", "repair-stale", "Pass --yes to mark stale worker runs timed_out and release their worker instances."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.RepairStaleWorkerRuns(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not repair stale worker runs.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.RepairedInstances)
				return nil
			}
			renderStaleWorkerRepair(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	repairCmd.Flags().BoolVar(&repairYes, "yes", false, "confirm marking stale worker runs timed_out")
	cmd.AddCommand(repairCmd)

	return cmd
}

func workerRunCommandContext(cmd *cobra.Command, workerRef string) (context.Context, context.CancelFunc) {
	parent := context.Background()
	if cmd != nil && cmd.Context() != nil {
		parent = cmd.Context()
	}
	if strings.TrimSpace(workerRef) == "main.cloud_snapshot_upload" {
		return context.WithTimeout(parent, 3*time.Hour)
	}
	return parent, func() {}
}

func newWorkerPolicyCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "policy",
		Short: "Inspect or set one worker tick policy",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <worker-ref>",
		Short: "Inspect normalized tick policy and its stable fingerprint",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.InspectWorkerPolicy(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect worker policy.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.PolicyFingerprint)
				return nil
			}
			renderWorkerPolicyState(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	var mode, every, localTime, timezone, expectedFingerprint, reason, idempotencyKey string
	var runOnStartup, dryRun, yes bool
	setCmd := &cobra.Command{
		Use:   "set <worker-ref>",
		Short: "Dry-run or apply a fingerprint-guarded tick-policy change",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun == yes {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_mode_required", "workers", args[0], "Choose exactly one of --dry-run or --yes."))
			}
			if strings.TrimSpace(mode) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_value_required", "workers", args[0], "Pass --mode manual, --mode interval, or --mode daily_local."))
			}
			normalizedMode := strings.TrimSpace(mode)
			switch normalizedMode {
			case workers.TickModeManual:
				if cmd.Flags().Changed("every") || cmd.Flags().Changed("run-on-startup") || cmd.Flags().Changed("local-time") || cmd.Flags().Changed("timezone") {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_flags_conflict", "workers", args[0], "Manual mode cannot be combined with --every, --run-on-startup, --local-time, or --timezone."))
				}
			case workers.TickModeInterval:
				if cmd.Flags().Changed("local-time") || cmd.Flags().Changed("timezone") {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_flags_conflict", "workers", args[0], "Interval mode cannot be combined with --local-time or --timezone."))
				}
			case workers.TickModeDailyLocal:
				if cmd.Flags().Changed("every") || cmd.Flags().Changed("run-on-startup") {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_flags_conflict", "workers", args[0], "Daily-local mode cannot be combined with --every or --run-on-startup."))
				}
				if !cmd.Flags().Changed("local-time") || !cmd.Flags().Changed("timezone") {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_daily_fields_required", "workers", args[0], "Daily-local mode requires --local-time HH:MM and --timezone IANA_NAME."))
				}
			}
			if strings.TrimSpace(expectedFingerprint) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_fingerprint_required", "workers", args[0], "Pass --expected-policy-fingerprint from policy inspect."))
			}
			if yes && strings.TrimSpace(reason) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_reason_required", "workers", args[0], "Pass --reason when applying worker policy."))
			}
			if yes && strings.TrimSpace(idempotencyKey) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_idempotency_required", "workers", args[0], "Pass --idempotency-key when applying worker policy."))
			}
			intervalSeconds := 0
			if strings.TrimSpace(every) != "" {
				duration, err := time.ParseDuration(strings.TrimSpace(every))
				if err != nil || duration <= 0 || duration%time.Second != 0 {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_interval_invalid", "workers", args[0], "Pass --every as a positive whole-second duration such as 5m or 1h."))
				}
				seconds := int64(duration / time.Second)
				intervalSeconds = int(seconds)
				if int64(intervalSeconds) != seconds {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("worker.policy_interval_invalid", "workers", args[0], "The --every duration is too large for this system."))
				}
			}
			input := workers.SetWorkerPolicyInput{
				Policy: workers.TickPolicy{
					SchemaVersion:   workers.TickPolicySchemaVersion,
					Mode:            normalizedMode,
					IntervalSeconds: intervalSeconds,
					RunOnStartup:    runOnStartup,
					LocalTime:       localTime,
					Timezone:        timezone,
				},
				ExpectedPolicyFingerprint: expectedFingerprint,
				Reason:                    reason,
				DryRun:                    dryRun,
				Confirm:                   yes,
				IdempotencyKey:            idempotencyKey,
			}
			if err := workers.ValidateSetWorkerPolicyInput(input); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("worker.policy_invalid", "workers", args[0], "Worker policy request is invalid.", err))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client := commandCtx.Client
			if yes {
				client = client.WithIdempotencyKey(idempotencyKey)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := client.SetWorkerPolicy(ctx, commandCtx.CorrelationID, args[0], input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not set worker policy.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.New.PolicyFingerprint)
				return nil
			}
			renderWorkerPolicyTransition(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	setCmd.Flags().StringVar(&mode, "mode", "", "tick mode: manual, interval, or daily_local")
	setCmd.Flags().StringVar(&every, "every", "", "interval duration, for example 5m or 1h (minimum 5s)")
	setCmd.Flags().BoolVar(&runOnStartup, "run-on-startup", false, "make an interval worker due when no next-run time exists")
	setCmd.Flags().StringVar(&localTime, "local-time", "", "daily local wall-clock time in strict HH:MM form")
	setCmd.Flags().StringVar(&timezone, "timezone", "", "daily IANA timezone, for example Europe/Amsterdam")
	setCmd.Flags().StringVar(&expectedFingerprint, "expected-policy-fingerprint", "", "stable fingerprint returned by policy inspect")
	setCmd.Flags().StringVar(&reason, "reason", "", "operator reason recorded with an applied policy change")
	setCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit retry key required for apply")
	setCmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate and render the proposed change without mutation")
	setCmd.Flags().BoolVar(&yes, "yes", false, "confirm applying the worker policy")
	cmd.AddCommand(setCmd)
	return cmd
}

func renderWorkerPolicyState(cmd *cobra.Command, state workers.WorkerPolicyState) {
	fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s\n", state.WorkerKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy: %s\n", renderTickPolicy(state.Policy))
	fmt.Fprintf(cmd.OutOrStdout(), "Fingerprint: %s\n", state.PolicyFingerprint)
	fmt.Fprintf(cmd.OutOrStdout(), "Next run after: %s\n", timePtrOrDash(state.NextRunAfter))
	fmt.Fprintf(cmd.OutOrStdout(), "Locality: %s\n", state.Locality)
	fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %t\n", state.Enabled)
	fmt.Fprintf(cmd.OutOrStdout(), "Current run: %s\n", ptrOrDash(state.CurrentRunID))
}

func renderWorkerPolicyTransition(cmd *cobra.Command, result workers.SetWorkerPolicyResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Worker policy: %s\n", result.WorkerKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", map[bool]string{true: "dry-run", false: "apply"}[result.DryRun])
	fmt.Fprintf(cmd.OutOrStdout(), "Changed: %t\n", result.Changed)
	fmt.Fprintf(cmd.OutOrStdout(), "Old policy: %s\n", renderTickPolicy(result.Old.Policy))
	fmt.Fprintf(cmd.OutOrStdout(), "Old fingerprint: %s\n", result.Old.PolicyFingerprint)
	fmt.Fprintf(cmd.OutOrStdout(), "Previous next run: %s\n", timePtrOrDash(result.NextRunEvidence.Previous))
	fmt.Fprintf(cmd.OutOrStdout(), "New policy: %s\n", renderTickPolicy(result.New.Policy))
	fmt.Fprintf(cmd.OutOrStdout(), "New fingerprint: %s\n", result.New.PolicyFingerprint)
	fmt.Fprintf(cmd.OutOrStdout(), "Proposed next run: %s\n", timePtrOrDash(result.NextRunEvidence.Proposed))
	fmt.Fprintf(cmd.OutOrStdout(), "Next-run basis: %s\n", result.NextRunEvidence.DerivationBasis)
}

func renderTickPolicy(policy workers.TickPolicy) string {
	if policy.Mode == workers.TickModeInterval {
		return fmt.Sprintf("interval every %ds (run_on_startup=%t)", policy.IntervalSeconds, policy.RunOnStartup)
	}
	if policy.Mode == workers.TickModeDailyLocal {
		return fmt.Sprintf("daily_local at %s %s", policy.LocalTime, policy.Timezone)
	}
	return workers.TickModeManual
}

func renderWorkerList(cmd *cobra.Command, workerList []workers.WorkerListItem) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "WORKER\tKIND\tLIFECYCLE\tHEALTH\tLAST SUCCESS\tCURRENT")
	for _, worker := range workerList {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			worker.WorkerKey,
			worker.WorkerKind,
			worker.LifecycleStatus,
			worker.HealthStatus,
			timePtrOrDash(worker.LastSuccessAt),
			ptrOrDash(worker.CurrentRunID),
		)
	}
	_ = writer.Flush()
}

func renderWorkerDetail(cmd *cobra.Command, detail workers.WorkerDetail) {
	instance := detail.Instance
	fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s\n", instance.WorkerKey)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", instance.WorkerInstanceID)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", instance.WorkerKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Lifecycle: %s\n", instance.LifecycleStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Enabled: %t\n", instance.Enabled)
	fmt.Fprintf(cmd.OutOrStdout(), "Paused: %t\n", instance.Paused)
	if detail.Health != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", detail.Health.HealthStatus)
		fmt.Fprintf(cmd.OutOrStdout(), "Severity: %s\n", detail.Health.Severity)
		fmt.Fprintf(cmd.OutOrStdout(), "Summary: %s\n", detail.Health.Summary)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Current run: %s\n", ptrOrDash(instance.CurrentRunID))
	fmt.Fprintf(cmd.OutOrStdout(), "Last run: %s\n", ptrOrDash(instance.LastRunID))
	fmt.Fprintf(cmd.OutOrStdout(), "Last success: %s\n", timePtrOrDash(instance.LastSuccessAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Last failure: %s\n", timePtrOrDash(instance.LastFailureAt))
	if detail.LatestCheckpoint != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Checkpoint: %s updated %s\n", detail.LatestCheckpoint.CheckpointKey, detail.LatestCheckpoint.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if len(detail.RecentRuns) > 0 {
		renderWorkerRuns(cmd, detail.RecentRuns)
	}
}

func renderWorkerRuns(cmd *cobra.Command, runs []workers.WorkerRun) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "RUN\tSTATUS\tTRIGGER\tSTARTED\tDURATION\tRESULT")
	for _, run := range runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			run.WorkerRunID,
			run.RunStatus,
			run.TriggerKind,
			run.StartedAt.UTC().Format(time.RFC3339),
			workerRunDuration(run),
			workerCountersText(run.CountersJSON),
		)
	}
	_ = writer.Flush()
}

func renderWorkerRunOnce(cmd *cobra.Command, result workers.RunOnceResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s\n", result.Worker.Instance.WorkerKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", result.Run.WorkerRunID)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.Run.RunStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Started: %s\n", result.Run.StartedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Finished: %s\n", timePtrOrDash(result.Run.FinishedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Health: %s\n", result.Health.HealthStatus)
	if counters := workerCountersText(result.Run.CountersJSON); counters != "-" {
		fmt.Fprintf(cmd.OutOrStdout(), "Counters: %s\n", counters)
	}
	if len(result.Checkpoints) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "CHECKPOINT\tUPDATED")
		for _, checkpoint := range result.Checkpoints {
			fmt.Fprintf(writer, "%s\t%s\n", checkpoint.CheckpointKey, checkpoint.UpdatedAt.UTC().Format(time.RFC3339))
		}
		_ = writer.Flush()
	}
}

func renderStaleWorkerRepair(cmd *cobra.Command, result workers.StaleRunRepairResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Stale worker repair: %s\n", result.CheckedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Timed out runs: %d\n", result.TimedOutRuns)
	fmt.Fprintf(cmd.OutOrStdout(), "Repaired instances: %d\n", result.RepairedInstances)
	fmt.Fprintf(cmd.OutOrStdout(), "Expired leases: %d\n", result.ExpiredLeases)
	for _, runID := range result.RunIDs {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", runID)
	}
}

func workerRunDuration(run workers.WorkerRun) string {
	if run.FinishedAt == nil {
		return "-"
	}
	return run.FinishedAt.Sub(run.StartedAt).Round(time.Millisecond).String()
}

func workerCountersText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "-"
	}
	var counters map[string]int64
	if err := json.Unmarshal(raw, &counters); err != nil || len(counters) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counters[key]))
	}
	return strings.Join(parts, " ")
}
