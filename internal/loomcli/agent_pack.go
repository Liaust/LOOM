package loomcli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/agentpack"
)

func newAgentPackCommand(opts *options) *cobra.Command {
	packDir := ""
	cmd := &cobra.Command{
		Use:   "pack",
		Short: "Inspect and validate the external AI LOOM pack",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&packDir, "pack-dir", "", "AI LOOM pack root override")
	cmd.AddCommand(newAgentPackStatusCommand(opts, &packDir))
	cmd.AddCommand(newAgentPackValidateCommand(opts, &packDir))
	cmd.AddCommand(newAgentPackScaffoldWorkspaceCommand(opts, &packDir))
	cmd.AddCommand(newAgentPackExportWorkspaceCommand(opts, &packDir))
	return cmd
}

func newAgentPackExportWorkspaceCommand(opts *options, packDir *string) *cobra.Command {
	options := agentpack.WorkspaceExportOptions{}
	dryRun, yes := false, false
	cmd := &cobra.Command{
		Use:   "export-workspace",
		Short: "Plan or export MINA portable source from an exact local Git HEAD",
		Long:  "Export an explicit positive allowlist of MINA source instructions and the supplied skin. Requires --template mina --source-root <loom-checkout> --path <absent-export-dir>. Planning is read-only by default; --dry-run never writes, including with --yes. This reconstructs portable instructions only, not a live profile, memory or authentication. Existing destinations are always refused; no sync, overwrite or remote operation occurs.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			defer func() {
				if runErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Workspace export failed: %v\n", runErr)
				}
			}()
			if *packDir != "" {
				return fmt.Errorf("export uses only --source-root; --pack-dir is not supported")
			}
			plan, err := agentpack.PlanWorkspaceExport(options)
			if err != nil {
				return err
			}
			if dryRun || !yes {
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "MINA source export plan: %s\nSource revision: %s\nPayloads: %d; plus export-manifest.json\n", plan.Manifest.ExportID, plan.Manifest.SourceRevision, len(plan.Manifest.Files))
				return nil
			}
			result, applyErr := agentpack.ApplyWorkspaceExport(plan, true)
			// Preserve truthful partial output even when publication fails.
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "MINA source export applied: %t\nExport: %s\n", result.Applied, result.Manifest.ExportID)
				for _, created := range result.Created {
					fmt.Fprintf(cmd.OutOrStdout(), "- created %s %s (complete=%t)\n", created.Kind, created.Path, created.Complete)
				}
			}
			return applyErr
		},
	}
	cmd.Flags().StringVar(&options.Template, "template", "", "explicit template selection (mina only)")
	cmd.Flags().StringVar(&options.SourceRoot, "source-root", "", "explicit trusted local LOOM Git checkout")
	cmd.Flags().StringVar(&options.Path, "path", "", "absent export destination under an existing real parent")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "plan without writing, even if --yes is present")
	cmd.Flags().BoolVar(&yes, "yes", false, "create the reviewed portable export")
	return cmd
}

func newAgentPackStatusCommand(opts *options, packDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local AI LOOM pack status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pack, report, err := loadAndValidateAgentPack(*packDir)
			if err != nil {
				return err
			}
			statuses := map[string]int{}
			for _, skill := range pack.Catalogue.Skills {
				statuses[skill.Status]++
			}
			status := struct {
				Root          agentpack.ResolvedRoot     `json:"root"`
				Name          string                     `json:"name"`
				Version       string                     `json:"version"`
				SkillCount    int                        `json:"skill_count"`
				SkillStatuses map[string]int             `json:"skill_statuses"`
				Harnesses     []string                   `json:"harnesses"`
				SkillSets     []string                   `json:"recommended_skill_sets"`
				Validation    agentpack.ValidationReport `json:"validation"`
			}{Root: pack.Root, Name: pack.Manifest.Name, Version: pack.Manifest.Version, SkillCount: len(pack.Manifest.Skills), SkillStatuses: statuses, Validation: report}
			for name := range pack.Manifest.Harnesses {
				status.Harnesses = append(status.Harnesses, name)
			}
			for name := range pack.Manifest.RecommendedSkillSets {
				status.SkillSets = append(status.SkillSets, name)
			}
			sort.Strings(status.Harnesses)
			sort.Strings(status.SkillSets)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "AI LOOM pack: %s %s\n", status.Name, status.Version)
			fmt.Fprintf(cmd.OutOrStdout(), "Root: %s (%s)\n", status.Root.Path, status.Root.Source)
			fmt.Fprintf(cmd.OutOrStdout(), "Skills: %d\n", status.SkillCount)
			fmt.Fprintf(cmd.OutOrStdout(), "Harnesses: %s\n", strings.Join(status.Harnesses, ", "))
			fmt.Fprintf(cmd.OutOrStdout(), "Recommended skill sets: %s\n", strings.Join(status.SkillSets, ", "))
			fmt.Fprintf(cmd.OutOrStdout(), "Validation: errors=%d warnings=%d\n", report.Errors, report.Warnings)
			return nil
		},
	}
}

func newAgentPackValidateCommand(opts *options, packDir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate pack, catalogue, templates, and skill metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, report, err := loadAndValidateAgentPack(*packDir)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Pack validation: errors=%d warnings=%d\n", report.Errors, report.Warnings)
				for _, issue := range report.Issues {
					fmt.Fprintf(cmd.OutOrStdout(), "- %s %s %s: %s\n", issue.Severity, issue.Code, issue.Path, issue.Message)
				}
			}
			if !report.Valid() {
				return fmt.Errorf("AI LOOM pack validation failed with %d error(s)", report.Errors)
			}
			return nil
		},
	}
}

func newAgentPackScaffoldWorkspaceCommand(opts *options, packDir *string) *cobra.Command {
	workspaceOptions := agentpack.WorkspaceOptions{Template: "morathustra"}
	dryRun := false
	yes := false
	cmd := &cobra.Command{
		Use:   "scaffold-workspace",
		Short: "Plan or create an isolated external-agent workspace",
		Long:  "Scaffold portable instructions without overwriting existing files. Morathustra and explicit --template mina each use one .hermes/SOUL.md and a read-only skills/installed placeholder. This command does not initialize Hermes or migrate legacy/live profiles; those require a separate operator workflow.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (runErr error) {
			// The root suppresses Cobra errors. Refusals here must still explain
			// the non-overwriting scaffold boundary to the CLI operator.
			defer func() {
				if runErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Workspace scaffolding failed: %v\n", runErr)
				}
			}()
			if dryRun == yes {
				return fmt.Errorf("choose exactly one of --dry-run or --yes")
			}
			pack, report, err := loadAndValidateAgentPack(*packDir)
			if err != nil {
				return err
			}
			if !report.Valid() {
				return fmt.Errorf("AI LOOM pack validation failed with %d error(s)", report.Errors)
			}
			plan, err := agentpack.PlanWorkspace(pack, workspaceOptions)
			if err != nil {
				return err
			}
			if dryRun {
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				}
				renderWorkspacePlan(cmd, plan)
				return nil
			}
			result, err := agentpack.ApplyWorkspace(plan, yes)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Scaffolded %s workspace at %s\n", result.Template, result.Path)
			renderWorkspaceActions(cmd, result.Actions)
			return nil
		},
	}
	cmd.Flags().StringVar(&workspaceOptions.Template, "template", "morathustra", "workspace template name")
	cmd.Flags().StringVar(&workspaceOptions.Path, "path", "", "explicit workspace destination")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the workspace plan without writing")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply the reviewed workspace plan")
	return cmd
}

func loadAndValidateAgentPack(explicit string) (*agentpack.Pack, agentpack.ValidationReport, error) {
	root, err := agentpack.ResolveRoot(agentpack.ResolveOptions{ExplicitPath: explicit})
	if err != nil {
		return nil, agentpack.ValidationReport{}, err
	}
	pack, err := agentpack.Load(root)
	if err != nil {
		return nil, agentpack.ValidationReport{}, err
	}
	return pack, agentpack.Validate(pack), nil
}

func renderWorkspacePlan(cmd *cobra.Command, plan agentpack.WorkspacePlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "AI LOOM workspace plan: %s %s\n", plan.Template, plan.PackVersion)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", plan.Path)
	renderWorkspaceActions(cmd, plan.Actions)
	if plan.Conflicts > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Conflicts: %d (apply is blocked)\n", plan.Conflicts)
	}
}

func renderWorkspaceActions(cmd *cobra.Command, actions []agentpack.WorkspaceAction) {
	for _, action := range actions {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s %s\n", action.Kind, action.RelativePath)
	}
}
