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
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

func newNotesEmbeddingsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "embeddings",
		Short: "Manage notes semantic embeddings",
	}
	cmd.AddCommand(newNotesEmbeddingsStatusCommand(opts))
	cmd.AddCommand(newNotesEmbeddingsToggleCommand(opts, true))
	cmd.AddCommand(newNotesEmbeddingsToggleCommand(opts, false))
	cmd.AddCommand(newNotesEmbeddingsRunCommand(opts))
	return cmd
}

func newNotesEmbeddingsStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show notes embedding status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetKnowledgeNotesEmbeddingStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.embeddings_status_failed", "knowledge", "notes_embeddings", "Could not load notes embedding status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			if opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "enabled=%t queued=%d ready=%d processing=%d active=%d failed=%d\n",
					envelope.Data.Settings.Enabled,
					envelope.Data.Queue.Queued,
					envelope.Data.Queue.Ready,
					envelope.Data.Queue.Processing,
					envelope.Data.ActiveVectors,
					envelope.Data.Queue.Failed+envelope.Data.Objects.Failed,
				)
				return nil
			}
			renderNotesEmbeddingStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newNotesEmbeddingsToggleCommand(opts *options, enabled bool) *cobra.Command {
	var yes bool
	var idempotencyKey string
	use := "disable"
	short := "Disable notes embeddings"
	if enabled {
		use = "enable"
		short = "Enable notes embeddings"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return fmt.Errorf("pass --yes to %s notes embeddings", use)
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "notes.embeddings."+use)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var envelopeData knowledge.SetEmbeddingsEnabledResult
			if enabled {
				envelope, err := client.EnableKnowledgeNotesEmbeddings(ctx, commandCtx.CorrelationID)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.embeddings_enable_failed", "knowledge", "notes_embeddings", "Could not enable notes embeddings.", err))
				}
				envelopeData = envelope.Data
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
				}
				renderNotesEmbeddingsToggle(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			envelope, err := client.DisableKnowledgeNotesEmbeddings(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.embeddings_disable_failed", "knowledge", "notes_embeddings", "Could not disable notes embeddings.", err))
			}
			envelopeData = envelope.Data
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesEmbeddingsToggle(cmd, envelopeData)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm changing global notes embedding state")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for this request")
	return cmd
}

func newNotesEmbeddingsRunCommand(opts *options) *cobra.Command {
	var once bool
	var reason string
	var idempotencyKey string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the notes embedder once",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("notes.embeddings_run_mode_required", "knowledge", "main.knowledge_embedder", "Pass --once to run the notes embedder once."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "notes.embeddings.run")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			if strings.TrimSpace(reason) == "" {
				reason = "manual notes embedding run"
			}
			envelope, err := client.RunKnowledgeNotesEmbedder(ctx, commandCtx.CorrelationID, workers.RunOnceInput{Reason: reason})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run notes embedder.", err))
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
	cmd.Flags().BoolVar(&once, "once", false, "run the notes embedder once")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded on the worker run")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for this run request")
	return cmd
}

func renderNotesEmbeddingStatus(cmd *cobra.Command, status knowledge.EmbeddingStatus) {
	state := "OFF"
	if status.Settings.Enabled {
		state = "ON"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Notes embeddings: %s\n", state)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s model=%s endpoint=%s dimensions=%d\n",
		dashIfEmpty(status.Settings.RuntimeKey),
		dashIfEmpty(status.Settings.ModelKey),
		dashIfEmpty(status.Settings.OllamaURL),
		status.Settings.Dimensions,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Queue: queued=%d ready=%d processing=%d failed=%d stale=%d\n",
		status.Queue.Queued,
		status.Queue.Ready,
		status.Queue.Processing,
		status.Queue.Failed,
		status.Queue.Stale,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Objects: queued=%d processing=%d complete=%d failed=%d stale=%d\n",
		status.Objects.Queued,
		status.Objects.Processing,
		status.Objects.Complete,
		status.Objects.Failed,
		status.Objects.Stale,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Vectors: active=%d historical=%d reusable=%d\n", status.ActiveVectors, status.Historical, status.ReusableVectors)
	if !status.GeneratedAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Generated: %s\n", status.GeneratedAt.UTC().Format(time.RFC3339))
	}
}

func renderNotesEmbeddingsToggle(cmd *cobra.Command, result knowledge.SetEmbeddingsEnabledResult) {
	state := "disabled"
	if result.Settings.Enabled {
		state = "enabled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Notes embeddings %s\n", state)
	fmt.Fprintf(cmd.OutOrStdout(), "Queued: %d\n", result.Queued)
	renderNotesEmbeddingStatus(cmd, result.Status)
}
