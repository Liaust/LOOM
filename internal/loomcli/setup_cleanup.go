package loomcli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/setup"
)

type setupCleanupFlags struct {
	planPath      string
	confirmDigest string
	dryRun        bool
	yes           bool
}

func newSetupCleanupCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Inspect or apply the reviewed retired Main intake cleanup inventory",
	}
	cmd.AddCommand(newSetupCleanupPlanCommand(opts))
	cmd.AddCommand(newSetupCleanupApplyCommand(opts))
	return cmd
}

func newSetupCleanupPlanCommand(opts *options) *cobra.Command {
	flags := setupCleanupFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Inspect the read-only retired intake inventory in a saved setup plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := setup.LoadPlanFile(flags.planPath)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "cleanup_plan", err)
			}
			if plan.PlanHash != setup.HashPlan(plan) {
				return renderSetupCommandError(cmd, opts, "cleanup_plan", fmt.Errorf("setup plan hash mismatch"))
			}
			if plan.RetiredIntakeCleanup == nil {
				return renderSetupCommandError(cmd, opts, "cleanup_plan", fmt.Errorf("setup plan has no retired intake cleanup inventory"))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan.RetiredIntakeCleanup)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.RetiredIntakeCleanup.PlanDigest)
				return nil
			}
			setup.RenderRetiredIntakeCleanupPlan(cmd.OutOrStdout(), *plan.RetiredIntakeCleanup)
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.planPath, "plan", "", "reviewed setup plan JSON or YAML path")
	_ = cmd.MarkFlagRequired("plan")
	return cmd
}

func newSetupCleanupApplyCommand(opts *options) *cobra.Command {
	flags := setupCleanupFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Remove only unchanged empty directories or exact known links from a reviewed plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := setup.LoadPlanFile(flags.planPath)
			if err != nil {
				return renderSetupCommandError(cmd, opts, "cleanup_apply", err)
			}
			result, err := setup.ApplyRetiredIntakeCleanup(setup.RetiredIntakeCleanupApplyInput{
				Plan:          plan,
				ConfirmDigest: strings.TrimSpace(flags.confirmDigest),
				DryRun:        flags.dryRun,
				Yes:           flags.yes,
			})
			if err != nil {
				return renderSetupCommandError(cmd, opts, "cleanup_apply", err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			setup.RenderRetiredIntakeCleanupResult(cmd.OutOrStdout(), result)
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.planPath, "plan", "", "reviewed setup plan JSON or YAML path")
	cmd.Flags().StringVar(&flags.confirmDigest, "confirm-digest", "", "exact retired intake cleanup digest from the reviewed plan")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "revalidate exact targets without removing them")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm exact non-recursive cleanup removals")
	_ = cmd.MarkFlagRequired("plan")
	_ = cmd.MarkFlagRequired("confirm-digest")
	return cmd
}
