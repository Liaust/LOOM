package loomcli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/storagecleanup"
)

func newStorageInventoryCommand(opts *options) *cobra.Command {
	var root string
	var production bool
	var productionOverride bool
	var limit int
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Inventory high-confidence dev/smoke artifacts under the canonical Box",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedRoot, err := resolveStorageCleanupRoot(opts, root)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.inventory_root_invalid", "storage", "inventory", "Could not resolve storage inventory root.", err))
			}
			result, err := storagecleanup.Inventory(storagecleanup.InventoryInput{Root: resolvedRoot, Production: production, AllowBroadProductionScan: productionOverride})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.inventory_failed", "storage", "inventory", "Could not inventory storage root.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if opts.plainOutput {
				for _, item := range result.Items {
					fmt.Fprintln(cmd.OutOrStdout(), item.RelativePath)
				}
				return nil
			}
			renderStorageCleanupInventory(cmd, "LOOM storage inventory", result.Root, result.Summary, result.Items, limit)
			if len(result.Warnings) > 0 {
				renderStorageCleanupWarnings(cmd, result.Warnings)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "canonical storage root; defaults to the configured Box")
	cmd.Flags().BoolVar(&production, "production", false, "mark this inventory as targeting production storage")
	cmd.Flags().BoolVar(&productionOverride, "production-override", false, "allow production inventory outside .loom-acceptance paths")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum candidates to render in human output")
	return cmd
}

func newStorageCleanupCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Plan and apply safe cleanup for high-confidence storage dev artifacts",
	}
	cmd.AddCommand(newStorageCleanupPlanCommand(opts))
	cmd.AddCommand(newStorageCleanupApplyCommand(opts))
	return cmd
}

func newStorageCleanupPlanCommand(opts *options) *cobra.Command {
	var root string
	var out string
	var production bool
	var productionOverride bool
	var limit int
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Create a storage cleanup plan without deleting anything",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedRoot, err := resolveStorageCleanupRoot(opts, root)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.cleanup_root_invalid", "storage", "cleanup", "Could not resolve storage cleanup root.", err))
			}
			plan, err := storagecleanup.Plan(storagecleanup.PlanInput{Root: resolvedRoot, Production: production, AllowBroadProductionScan: productionOverride})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.cleanup_plan_failed", "storage", "cleanup", "Could not create storage cleanup plan.", err))
			}
			if strings.TrimSpace(out) != "" {
				if err := storagecleanup.WritePlan(out, plan); err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.cleanup_plan_write_failed", "storage", "cleanup", "Could not write storage cleanup plan.", err))
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				if out != "" {
					fmt.Fprintln(cmd.OutOrStdout(), out)
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), plan.Root)
				return nil
			}
			renderStorageCleanupInventory(cmd, "LOOM storage cleanup plan", plan.Root, plan.Summary, plan.Items, limit)
			if out != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Plan file: %s\n", out)
			}
			if len(plan.Warnings) > 0 {
				renderStorageCleanupWarnings(cmd, plan.Warnings)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "canonical storage root; defaults to the configured Box")
	cmd.Flags().StringVar(&out, "out", "", "write the cleanup plan to this JSON file")
	cmd.Flags().BoolVar(&production, "production", false, "mark this plan as targeting production storage")
	cmd.Flags().BoolVar(&productionOverride, "production-override", false, "allow production cleanup planning outside .loom-acceptance paths")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum candidates to render in human output")
	return cmd
}

func newStorageCleanupApplyCommand(opts *options) *cobra.Command {
	var planPath string
	var dryRun bool
	var yes bool
	var deleteNow bool
	var acceptMetadataLoss bool
	var limit int
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply safe auto-apply items from a storage cleanup plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := storagecleanup.Apply(storagecleanup.ApplyInput{PlanPath: planPath, DryRun: dryRun, Yes: yes, DeleteNow: deleteNow, AcceptMetadataLoss: acceptMetadataLoss})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.cleanup_apply_failed", "storage", "cleanup", "Could not apply storage cleanup plan.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if opts.plainOutput {
				if result.Refused {
					fmt.Fprintln(cmd.OutOrStdout(), "refused")
					return nil
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%d\n", result.Summary.Removed+result.Summary.WouldRemove+result.Summary.Quarantined+result.Summary.WouldQuarantine)
				return nil
			}
			renderStorageCleanupApply(cmd, result, limit)
			if result.Refused {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("storage.cleanup_refused", "storage", "cleanup", result.Refusal))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "cleanup plan JSON file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be quarantined or deleted without mutating anything")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm safe cleanup mutations")
	cmd.Flags().BoolVar(&deleteNow, "delete-now", false, "delete eligible paths immediately instead of moving them to quarantine")
	cmd.Flags().BoolVar(&acceptMetadataLoss, "accept-metadata-loss", false, "allow cleanup of candidates carrying reviewed fidelity metadata-loss findings")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum changes to render in human output")
	_ = cmd.MarkFlagRequired("plan")
	return cmd
}

func resolveStorageCleanupRoot(opts *options, explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit, nil
	}
	cfg, err := config.Load(config.Overrides{
		ConfigFile: opts.configFile,
		SocketPath: opts.socketPath,
	})
	if err != nil {
		return "", err
	}
	return cfg.BoxPath, nil
}

func renderStorageCleanupInventory(cmd *cobra.Command, title string, root string, summary storagecleanup.Summary, items []storagecleanup.Candidate, limit int) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", title)
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", root)
	fmt.Fprintf(cmd.OutOrStdout(), "Scanned: %d candidates=%d auto_apply=%d manual_review=%d candidate_bytes=%d\n",
		summary.Scanned, summary.Candidates, summary.AutoApply, summary.ManualReview, summary.CandidateBytes)
	if len(items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No high-confidence cleanup candidates found.")
		return
	}
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "AUTO\tACTION\tSCOPE\tCATALOG\tKIND\tSIZE\tFIDELITY\tPATH")
	for _, item := range items[:limit] {
		fmt.Fprintf(writer, "%t\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			item.AutoApply,
			item.Action,
			item.CleanupScope,
			item.CatalogDisposition,
			item.Kind,
			item.SizeBytes,
			strings.Join(item.FidelityFindings, ","),
			item.RelativePath)
	}
	_ = writer.Flush()
	if len(items) > limit {
		fmt.Fprintf(cmd.OutOrStdout(), "... %d more candidates\n", len(items)-limit)
	}
}

func renderStorageCleanupApply(cmd *cobra.Command, result storagecleanup.ApplyResult, limit int) {
	title := "LOOM storage cleanup applied"
	if result.DryRun {
		title = "LOOM storage cleanup dry-run"
	}
	if result.Refused {
		title = "LOOM storage cleanup refused"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", title)
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", result.Root)
	if result.Refused {
		fmt.Fprintf(cmd.OutOrStdout(), "Refusal: %s\n", result.Refusal)
		return
	}
	if result.QuarantineDir != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Quarantine: %s\n", result.QuarantineDir)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Planned: %d eligible=%d quarantined=%d removed=%d would_quarantine=%d would_remove=%d skipped=%d blocked=%d manual_review=%d\n",
		result.Summary.Planned,
		result.Summary.Eligible,
		result.Summary.Quarantined,
		result.Summary.Removed,
		result.Summary.WouldQuarantine,
		result.Summary.WouldRemove,
		result.Summary.Skipped,
		result.Summary.Blocked,
		result.Summary.ManualReview)
	if len(result.Changes) == 0 {
		return
	}
	if limit <= 0 || limit > len(result.Changes) {
		limit = len(result.Changes)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tACTION\tPATH\tQUARANTINE\tMESSAGE")
	for _, change := range result.Changes[:limit] {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", change.Status, change.Action, change.RelativePath, change.QuarantinePath, change.Message)
	}
	_ = writer.Flush()
	if len(result.Changes) > limit {
		fmt.Fprintf(cmd.OutOrStdout(), "... %d more changes\n", len(result.Changes)-limit)
	}
}

func renderStorageCleanupWarnings(cmd *cobra.Command, warnings []string) {
	fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
	for _, warning := range warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
	}
}
