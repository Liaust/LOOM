package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workers"
)

func newIndexesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "indexes",
		Short: "Inspect LOOM index queues and failures",
	}

	statusOpts := search.StatusFilter{}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "List index status records",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListIndexStatus(ctx, correlationID, statusOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list index status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addIndexStatusFlags(statusCmd, &statusOpts)
	cmd.AddCommand(statusCmd)

	queueCmd := &cobra.Command{
		Use:   "queue",
		Short: "Inspect queued index work",
	}
	queueOpts := search.IndexQueueFilter{}
	queueListCmd := &cobra.Command{
		Use:   "list",
		Short: "List queued index work",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListIndexQueue(ctx, correlationID, queueOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list index queue.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	queueListCmd.Flags().IntVar(&queueOpts.Limit, "limit", 50, "maximum number of queue records to return")
	queueListCmd.Flags().StringVar(&queueOpts.ObjectRef, "object", "", "filter by object ID, slug, or name")
	queueListCmd.Flags().StringVar(&queueOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	queueListCmd.Flags().StringVar(&queueOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	queueListCmd.Flags().StringVar(&queueOpts.Status, "status", "", "filter by queue status")
	queueListCmd.Flags().BoolVar(&queueOpts.IncludeActive, "include-active", false, "include actively claimed or processing items")
	queueCmd.AddCommand(queueListCmd)
	queueCmd.AddCommand(&cobra.Command{
		Use:   "show <index-status-id>",
		Short: "Show one index queue item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.GetIndexQueueItem(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect index queue item.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, []search.IndexStatus{envelope.Data})
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(queueCmd)

	failureOpts := search.StatusFilter{}
	failuresCmd := &cobra.Command{
		Use:   "failures",
		Short: "List failed index work",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ListIndexFailures(ctx, correlationID, failureOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list index failures.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addIndexStatusFlags(failuresCmd, &failureOpts)
	cmd.AddCommand(failuresCmd)

	explainCmd := &cobra.Command{
		Use:   "explain",
		Short: "Explain index state for an object",
	}
	explainCmd.AddCommand(&cobra.Command{
		Use:   "object <object-ref>",
		Short: "Explain an object's index state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.ExplainIndexObject(ctx, correlationID, search.IndexExplainInput{ObjectRef: args[0]})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not explain object index state.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexExplain(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(explainCmd)

	retryCmd := &cobra.Command{
		Use:   "retry <index-status-id>",
		Short: "Retry one failed or blocked index item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.RetryIndexWork(ctx, correlationID, search.IndexStatusRefInput{IndexStatusID: args[0]})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not retry index work.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexStatusList(cmd, []search.IndexStatus{envelope.Data})
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(retryCmd)

	retryFailedOpts := search.IndexRetryFailedInput{}
	retryFailedCmd := &cobra.Command{
		Use:   "retry-failed",
		Short: "Retry failed index items",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}

			envelope, err := client.RetryFailedIndexWork(ctx, correlationID, retryFailedOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not retry failed index work.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Retried: %d\n", envelope.Data.Retried)
			fmt.Fprintf(cmd.OutOrStdout(), "Skipped: %d\n", envelope.Data.Skipped)
			if len(envelope.Data.Items) > 0 {
				renderIndexStatusList(cmd, envelope.Data.Items)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	retryFailedCmd.Flags().IntVar(&retryFailedOpts.Limit, "limit", 50, "maximum number of failed records to retry")
	retryFailedCmd.Flags().StringVar(&retryFailedOpts.ObjectRef, "object", "", "filter by object ID, slug, or name")
	retryFailedCmd.Flags().StringVar(&retryFailedOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	retryFailedCmd.Flags().StringVar(&retryFailedOpts.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	retryFailedCmd.Flags().StringVar(&retryFailedOpts.IndexType, "index-type", "", "filter by index type")
	cmd.AddCommand(retryFailedCmd)

	rebuildCmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Queue index rebuild work",
	}
	rebuildOpts := struct {
		search.RebuildInput
		idempotencyKey string
	}{}
	rebuildObjectCmd := &cobra.Command{
		Use:   "object <object-ref>",
		Short: "Queue a rebuild for an object's latest version",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, rebuildOpts.idempotencyKey, "indexes.rebuild.object")
			input := rebuildOpts.RebuildInput
			input.ObjectRef = args[0]
			input.Force = true

			envelope, err := client.RebuildIndexObject(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not queue index rebuild.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderIndexResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	rebuildObjectCmd.Flags().StringVar(&rebuildOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	rebuildCmd.AddCommand(rebuildObjectCmd)
	cmd.AddCommand(rebuildCmd)

	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run index workers",
	}
	var runOnce bool
	var runReason string
	var runIdempotencyKey string
	runTextCmd := &cobra.Command{
		Use:   "text",
		Short: "Run the text indexer once",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !runOnce {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("indexes.run_mode_required", "search", "main.indexer_text", "Pass --once to run the text indexer once."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, runIdempotencyKey, "indexes.run.text")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			reason := runReason
			if reason == "" {
				reason = "manual text indexer run"
			}
			envelope, err := client.RunWorkerOnce(ctx, commandCtx.CorrelationID, "main.indexer_text", workers.RunOnceInput{
				Reason: reason,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run text indexer.", err))
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
	runTextCmd.Flags().BoolVar(&runOnce, "once", false, "run the text indexer once")
	runTextCmd.Flags().StringVar(&runReason, "reason", "", "reason recorded on the worker run")
	runTextCmd.Flags().StringVar(&runIdempotencyKey, "idempotency-key", "", "explicit idempotency key for the run request")
	runCmd.AddCommand(runTextCmd)
	cmd.AddCommand(runCmd)

	return cmd
}

func addIndexStatusFlags(cmd *cobra.Command, filter *search.StatusFilter) {
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of status records to return")
	cmd.Flags().StringVar(&filter.ObjectRef, "object", "", "filter by object ID, slug, or name")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.ScopeRef, "scope", "", "filter by scope ID, key, or slug")
	cmd.Flags().StringVar(&filter.IndexType, "index-type", "", "filter by index type")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by status")
	cmd.Flags().BoolVar(&filter.FailedOnly, "failed", false, "show only failed status records")
}

func renderIndexExplain(cmd *cobra.Command, result search.IndexExplainResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Object: %s\n", result.ObjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Version: %s\n", result.ObjectVersionID)
	fmt.Fprintf(cmd.OutOrStdout(), "Queue status: %s\n", dashIfEmpty(result.QueueStatus))
	if len(result.Statuses) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No index status records found.")
		return
	}
	renderIndexStatusList(cmd, result.Statuses)
}
