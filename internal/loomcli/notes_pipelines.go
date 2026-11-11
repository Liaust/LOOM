package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/workers"
)

func newNotesPipelinesCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "pipelines", Short: "Inspect and repair unified Notes file pipelines"}
	root.AddCommand(newNotesPipelineStatusCommand(opts), newNotesPipelineListCommand(opts, false), newNotesPipelineInspectCommand(opts), newNotesPipelineListCommand(opts, true), newNotesPipelineRetryCommand(opts), newNotesPipelinePolicyCommand(opts), newNotesPipelineBackfillCommand(opts))
	return root
}

func newNotesPipelineBackfillCommand(opts *options) *cobra.Command {
	var apply, yes bool
	var priority int
	cmd := &cobra.Command{Use: "backfill", Short: "Plan or apply the idempotent unified pipeline backfill", RunE: func(cmd *cobra.Command, args []string) error {
		if apply && !yes {
			return fmt.Errorf("pass --yes with --apply to backfill Notes pipelines")
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		if apply {
			client, _ := withEffectIdempotency(commandCtx.Client, "", "notes.pipelines.backfill")
			envelope, err := client.ApplyKnowledgeNotesPipelineBackfill(ctx, commandCtx.CorrelationID, knowledge.PipelineBackfillInput{Confirm: true, Priority: priority})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created pipelines: %d\nExisting pipelines: %d\nBlocked objects: %d\nLegacy rows marked historical: %d\n", envelope.Data.CreatedPipelines, envelope.Data.ExistingPipelines, envelope.Data.BlockedObjects, envelope.Data.LegacyRowsHistorical)
			return nil
		}
		envelope, err := commandCtx.Client.PlanKnowledgeNotesPipelineBackfill(ctx, commandCtx.CorrelationID)
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
		}
		renderPipelineBackfillPlan(cmd, envelope.Data)
		return nil
	}}
	cmd.Flags().BoolVar(&apply, "apply", false, "apply the backfill; default is a non-mutating dry-run")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm backfill apply")
	cmd.Flags().IntVar(&priority, "priority", 100, "priority for newly created pipelines")
	return cmd
}

func renderPipelineBackfillPlan(cmd *cobra.Command, plan knowledge.PipelineBackfillPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "Dry run: %t\nObjects: %d\nCreate: %d\nExisting: %d\nReprocess source: %d\nReuse: chunks=%d lexical=%d vectors=%d\nLegacy embedding: total=%d pending=%d active_claims=%d\n", plan.DryRun, plan.TotalObjects, plan.ObjectsToCreate, plan.ExistingPipelines, plan.RequiresSourceReprocessing, plan.ReusableChunks, plan.ReusableLexicalDocuments, plan.ReusableVectors, plan.LegacyEmbedding.TotalRows, plan.LegacyEmbedding.PendingRows, plan.LegacyEmbedding.ActiveClaims)
	for _, key := range sortedStringIntKeys(plan.ByFileClass) {
		fmt.Fprintf(cmd.OutOrStdout(), "file_class.%s: %d\n", key, plan.ByFileClass[key])
	}
	for _, key := range sortedStringIntKeys(plan.StagePlan) {
		fmt.Fprintf(cmd.OutOrStdout(), "stage.%s: %d\n", key, plan.StagePlan[key])
	}
}

func sortedStringIntKeys(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func newNotesPipelineStatusCommand(opts *options) *cobra.Command {
	var inspectIntegrity, summary, details bool
	cmd := &cobra.Command{Use: "status", Short: "Show bounded Notes progress; optionally inspect full integrity", RunE: func(cmd *cobra.Command, args []string) error {
		if summary && details {
			return fmt.Errorf("choose --summary or --details, not both")
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		timeout := 10 * time.Second
		if inspectIntegrity {
			timeout = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		read := commandCtx.Client.GetKnowledgeNotesPipelineStatus
		if inspectIntegrity {
			read = commandCtx.Client.InspectKnowledgeNotesPipelineIntegrity
		}
		envelope, err := read(ctx, commandCtx.CorrelationID)
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			if summary {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(summarizePipelineStatus(envelope.Data))
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
		}
		if details || inspectIntegrity {
			renderPipelineProgress(cmd, envelope.Data)
		} else {
			renderPipelineStatusSummary(cmd, summarizePipelineStatus(envelope.Data))
		}
		return nil
	}}
	cmd.Flags().BoolVar(&inspectIntegrity, "inspect-integrity", false, "explicitly scan historical activation integrity and publication/vector counts")
	cmd.Flags().BoolVar(&summary, "summary", false, "return compact status JSON without raw plans, policies or worker result payloads")
	cmd.Flags().BoolVar(&details, "details", false, "show full observed stages, policy and publication details")
	return cmd
}

func renderPipelineProgress(cmd *cobra.Command, status knowledge.PipelineOverallStatus) {
	policyKnown := true
	if status.Progress != nil {
		for _, failure := range status.Progress.PartialErrors {
			if failure == "policy_unavailable" {
				policyKnown = false
			}
		}
	}
	if policyKnown {
		fmt.Fprintf(cmd.OutOrStdout(), "Policy: ocr=%t vision=%t embeddings=%t\n", status.Policy.Policy.PDFOCREnabled, status.Policy.Policy.ImageDescriptionsEnabled, status.Policy.Policy.EmbeddingsEnabled)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Policy: unknown")
	}
	if status.Progress == nil {
		for _, key := range sortedStringIntKeys(status.Counts) {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d\n", key, status.Counts[key])
		}
		return
	}
	p := status.Progress
	fmt.Fprintf(cmd.OutOrStdout(), "Selected queue observations: %d (limit=%d window_full=%t known=%t); totals: unknown\n", len(status.Current), p.Limit, p.WindowFull, p.CurrentKnown)
	for _, run := range p.Runs {
		fmt.Fprintf(cmd.OutOrStdout(), "%s generation=%d selected=%s age=%.0fs reason=%s\n", run.ObjectID, run.Generation, run.SelectedVersionID, run.RunAgeSeconds, run.Reason)
		if run.Publication != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  Publication: catalog_current=%t body=%t lexical=%t semantic=%t\n", run.Publication.CatalogCurrent, run.Publication.BodyCurrent, run.Publication.LexicalCurrent, run.Publication.SemanticCurrent)
		}
		if run.NextEligibleAt != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "  Next eligible: %s\n", run.NextEligibleAt.UTC().Format(time.RFC3339))
		}
		for _, stage := range run.Stages {
			total := "unknown"
			if stage.TotalUnits != nil {
				total = fmt.Sprint(*stage.TotalUnits)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s units=%d/%s reused=unknown %s\n", stage.StageKey, stage.Status, stage.CompletedUnits, total, stage.SkipReason)
			if stage.Admission != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "    Pressure: %s observed=%s\n", stage.Admission.Reason, stage.Admission.SampledAt.UTC().Format(time.RFC3339))
			}
		}
	}
	var native notesworkspacesync.WorkspaceProgress
	if len(status.NativePublication) > 0 && json.Unmarshal(status.NativePublication, &native) == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Native publication selection: %s current=unknown checkpoint=%s run=%s\n", native.SelectionScope, native.CheckpointID, native.CheckpointRunID)
		for _, inventory := range native.Inventory {
			fmt.Fprintf(cmd.OutOrStdout(), "  source=%s native=%s generation=%s complete=%t counters_known=%t totals_known=%t observed=%s\n", inventory.Collection, inventory.NativeCollection, inventory.Generation, inventory.Complete, inventory.CountersKnown, inventory.TotalsKnown, inventory.ObservedAt.UTC().Format(time.RFC3339))
			if inventory.CountersKnown {
				fmt.Fprintf(cmd.OutOrStdout(), "    Native pending: text=%d assets=%d bytes=%d excluded=%d\n", inventory.Counts.TextPending, inventory.Counts.AssetPending, inventory.Counts.PendingBytes, inventory.Counts.Excluded)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "    Native published observations: text=%s assets=%s\n", pipelineCountText(inventory.TextPublished), pipelineCountText(inventory.AssetPublished))
			if inventory.CountersKnown {
				fmt.Fprintf(cmd.OutOrStdout(), "    Waiting: text=%s assets=%s\n", pipelineWaitText(inventory.TextOldestAgeSeconds, inventory.TextReason), pipelineWaitText(inventory.AssetOldestAgeSeconds, inventory.AssetReason))
			}
			if inventory.TextRetryAt != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "    Text retry: %s\n", inventory.TextRetryAt.UTC().Format(time.RFC3339))
			}
			if inventory.AssetRetryAt != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "    Asset retry: %s\n", inventory.AssetRetryAt.UTC().Format(time.RFC3339))
			}
		}
		for _, failure := range native.PartialErrors {
			fmt.Fprintf(cmd.OutOrStdout(), "Partial: %s\n", failure)
		}
	}
	for _, worker := range p.Workers {
		renderPipelineWorkerReceipt(cmd, summarizePipelineWorker(worker, p.ObservedAt))
		fmt.Fprintln(cmd.OutOrStdout(), "  Worker counters: last_finished_turn")
		for _, turn := range worker.Turns {
			admission := turn.Observation.Admission
			if admission == nil {
				continue
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  Pressure: %s observed=%s\n", admission.Reason, admission.SampledAt.UTC().Format(time.RFC3339))
		}
	}
	keys := make([]string, 0, len(p.Unknown))
	for key := range p.Unknown {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "Unknown %s: %s\n", key, p.Unknown[key])
	}
	for _, failure := range p.PartialErrors {
		fmt.Fprintf(cmd.OutOrStdout(), "Partial: %s\n", failure)
	}
	if status.Operations.IntegrityCheckedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Integrity observed=%s activation_mismatches=%d lexical_documents=%d active_vectors=%d\n", status.Operations.IntegrityCheckedAt.UTC().Format(time.RFC3339), status.Operations.ActivationMismatches, status.Operations.LexicalDocuments, status.Operations.ActiveVectors)
	}
}

func newNotesPipelineListCommand(opts *options, failures bool) *cobra.Command {
	input := knowledge.PipelineListInput{Limit: 100}
	use := "list"
	short := "List Notes pipelines"
	if failures {
		use = "failures"
		short = "List blocked or failed Notes pipelines"
	}
	cmd := &cobra.Command{Use: use, Short: short, RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var runs []knowledge.PipelineRun
		if failures {
			envelope, err := commandCtx.Client.ListKnowledgeNotesPipelineFailures(ctx, commandCtx.CorrelationID)
			if err != nil {
				return err
			}
			runs = envelope.Data
		} else {
			envelope, err := commandCtx.Client.ListKnowledgeNotesPipelines(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return err
			}
			runs = envelope.Data
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(runs)
		}
		renderPipelineRuns(cmd, runs)
		return nil
	}}
	if !failures {
		cmd.Flags().StringVar(&input.Status, "status", "", "filter by status")
		cmd.Flags().StringVar(&input.ObjectID, "object-id", "", "filter by object id")
		cmd.Flags().IntVar(&input.Limit, "limit", 100, "maximum pipelines")
	}
	return cmd
}

func newNotesPipelineInspectCommand(opts *options) *cobra.Command {
	var summary bool
	cmd := &cobra.Command{Use: "inspect <pipeline-or-object-ref>", Short: "Inspect a pipeline without returning artifact bodies", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		envelope, err := commandCtx.Client.GetKnowledgeNotesPipeline(ctx, commandCtx.CorrelationID, args[0])
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			if summary {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(summarizePipelineInspect(envelope.Data))
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Pipeline: %s\nObject: %s\nSaved status: %s\nSaved stage: %s (%s)\nGeneration: %d\n", envelope.Data.Run.KnowledgePipelineRunID, envelope.Data.Run.KnowledgeObjectID, envelope.Data.Run.Status, envelope.Data.Run.CurrentStageKey, envelope.Data.Run.CurrentExecutionClass, envelope.Data.Run.Generation)
		renderPipelineRunSummary(cmd, summarizePipelineInspect(envelope.Data))
		var plan knowledge.CompiledPipelinePlan
		if json.Unmarshal(envelope.Data.Run.PlanSnapshot, &plan) == nil && plan.SourcePolicy != nil {
			requested, _ := json.Marshal(plan.SourcePolicy)
			fmt.Fprintf(cmd.OutOrStdout(), "Saved source policy: %s\n", requested)
			if p := plan.EffectivePolicy; p != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Saved effective processing: pdf_ocr=%t image_ocr=%t embeddings=%t image_descriptions=%t\n", p.PDFOCREnabled, p.ImageOCREnabled, p.EmbeddingsEnabled, p.ImageDescriptionsEnabled)
			}
		}
		if deadline := envelope.Data.Run.QuietWindowEligibleAt; deadline != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Saved quiet-window deadline: %s (not current source admission)\n", deadline.UTC().Format(time.RFC3339))
		}
		refresh := envelope.Data.Refresh
		fmt.Fprintf(cmd.OutOrStdout(), "Refresh: selected=%s latest=%s pending=%t\nPublished: lexical=%s semantic=%s\n", dashIfEmpty(refresh.SelectedSourceRevision), dashIfEmpty(refresh.LatestSourceRevision), refresh.Pending, dashIfEmpty(refresh.LexicalVersionID), dashIfEmpty(refresh.SemanticVersionID))
		for _, stage := range envelope.Data.Stages {
			fmt.Fprintf(cmd.OutOrStdout(), "  %02d %-22s %s attempts=%d progress=%d/%d\n", stage.Ordinal, stage.StageKey, stage.Status, stage.AttemptCount, stage.ProgressCompleted, stage.ProgressTotal)
			for _, planned := range plan.Stages {
				if planned.StageKey == stage.StageKey && planned.SkipReason != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "     %s\n", planned.SkipReason)
				}
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Artifacts: %d (bodies omitted)\n", len(envelope.Data.Artifacts))
		return nil
	}}
	cmd.Flags().BoolVar(&summary, "summary", false, "return compact inspection JSON with current source eligibility and typed follow-up")
	return cmd
}

func newNotesPipelineRetryCommand(opts *options) *cobra.Command {
	var stage string
	var yes bool
	var priority int
	cmd := &cobra.Command{Use: "retry <pipeline-ref>", Short: "Retry a pipeline as a new generation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return fmt.Errorf("pass --yes to retry a Notes pipeline")
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		client, _ := withEffectIdempotency(commandCtx.Client, "", "notes.pipelines.retry")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		envelope, err := client.RetryKnowledgeNotesPipeline(ctx, commandCtx.CorrelationID, args[0], knowledge.PipelineRetryInput{StageKey: stage, Priority: priority})
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Queued pipeline %s generation %d\n", envelope.Data.KnowledgePipelineRunID, envelope.Data.Generation)
		return nil
	}}
	cmd.Flags().StringVar(&stage, "stage", "", "validate and retry from a named stage")
	cmd.Flags().IntVar(&priority, "priority", 100, "pipeline priority")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm pipeline retry")
	return cmd
}

func newNotesPipelinePolicyCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "policy", Short: "Read or update unified Notes pipeline policy"}
	root.AddCommand(&cobra.Command{Use: "status", RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		envelope, err := commandCtx.Client.GetKnowledgeNotesPipelinePolicy(ctx, commandCtx.CorrelationID)
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "PDF OCR: %t\nImage OCR: %t\nImage descriptions: %t\nEmbeddings: %t\n", envelope.Data.Policy.PDFOCREnabled, envelope.Data.Policy.ImageOCREnabled, envelope.Data.Policy.ImageDescriptionsEnabled, envelope.Data.Policy.EmbeddingsEnabled)
		return nil
	}})
	var ocr, imageOCR, vision, embeddings, yes bool
	set := &cobra.Command{Use: "set", RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return fmt.Errorf("pass --yes to update Notes pipeline policy")
		}
		input := knowledge.PipelinePolicyUpdate{}
		if cmd.Flags().Changed("image-ocr") {
			input.ImageOCREnabled = &imageOCR
		}
		if cmd.Flags().Changed("pdf-ocr") {
			input.PDFOCREnabled = &ocr
		}
		if cmd.Flags().Changed("image-descriptions") {
			input.ImageDescriptionsEnabled = &vision
		}
		if cmd.Flags().Changed("embeddings") {
			input.EmbeddingsEnabled = &embeddings
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		client, _ := withEffectIdempotency(commandCtx.Client, "", "notes.pipelines.policy")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		envelope, err := client.UpdateKnowledgeNotesPipelinePolicy(ctx, commandCtx.CorrelationID, input)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
	}}
	set.Flags().BoolVar(&ocr, "pdf-ocr", true, "enable or disable PDF OCR")
	set.Flags().BoolVar(&imageOCR, "image-ocr", false, "enable or disable standalone image OCR")
	set.Flags().BoolVar(&vision, "image-descriptions", true, "enable or disable standalone image descriptions")
	set.Flags().BoolVar(&embeddings, "embeddings", false, "enable or disable embeddings")
	set.Flags().BoolVar(&yes, "yes", false, "confirm policy change")
	root.AddCommand(set)
	return root
}

func renderPipelineRuns(cmd *cobra.Command, runs []knowledge.PipelineRun) {
	if len(runs) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No Notes pipelines found.")
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tOBJECT\tSTATUS\tSTAGE\tCLASS\tGEN")
	for _, run := range runs {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%d\n", run.KnowledgePipelineRunID, run.KnowledgeObjectID, run.Status, run.CurrentStageKey, run.CurrentExecutionClass, run.Generation)
	}
	_ = writer.Flush()
}

type notesWorkerRunner func(context.Context, localclient.Client, string, workers.RunOnceInput) (workers.RunOnceResult, error)

func newNotesWorkerOnceCommand(opts *options, use, short, scope string, run notesWorkerRunner) *cobra.Command {
	var once bool
	var reason, idempotencyKey string
	cmd := &cobra.Command{Use: use, Short: short, RunE: func(cmd *cobra.Command, args []string) error {
		if !once {
			return fmt.Errorf("pass --once to run %s", use)
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, scope)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		result, err := run(ctx, client, commandCtx.CorrelationID, workers.RunOnceInput{Reason: reason})
		if err != nil {
			return err
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		renderWorkerRunOnce(cmd, result)
		return nil
	}}
	cmd.Flags().BoolVar(&once, "once", false, "run once")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded on worker run")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key")
	return cmd
}

func pipelineCountText(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprint(*value)
}
func pipelineWaitText(age float64, reason string) string {
	if reason == "waiting_outside_bounded_window" {
		return "unknown (bounded lane window)"
	}
	return fmt.Sprintf("%.0fs %s", age, reason)
}
