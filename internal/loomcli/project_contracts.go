package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/localclient"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projectexport"
	"loom.local/loom/internal/projectregistration"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/storagearchive"
)

func addProjectContractCommands(cmd *cobra.Command, opts *options) {
	scaffoldOpts := projectcontracts.ScaffoldOptions{}
	var scaffoldFacets string
	var scaffoldBackend bool
	var scaffoldRegister bool
	scaffoldCmd := &cobra.Command{
		Use:   "scaffold <name>",
		Short: "Scaffold a local LOOM project folder without registering it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			input := scaffoldOpts
			input.Name = args[0]
			if cmd.Name() == "create" {
				input.Mode = projectcontracts.ScaffoldModeDeclaration
				input.Preset = ""
			}
			if scaffoldRegister && !scaffoldBackend {
				return fmt.Errorf("--register requires --backend")
			}
			if strings.TrimSpace(scaffoldFacets) != "" {
				input.Facets = splitScaffoldFacets(scaffoldFacets)
			}
			if scaffoldBackend {
				if !cmd.Flags().Changed("directory") {
					input.Directory = ""
					input.DirectorySource = ""
				} else {
					input.DirectorySource = projectcontracts.ScaffoldDirectoryExplicit
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
				}
				envelope, err := commandCtx.Client.ScaffoldProject(ctx, commandCtx.CorrelationID, input)
				if err != nil {
					if envelope.Data.SourceState == "source_created" {
						if opts.jsonOutput {
							_ = json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
						} else {
							projectcontracts.RenderScaffold(cmd.OutOrStdout(), envelope.Data, false)
						}
					}
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.scaffold_failed", "projects", input.Name, "Could not scaffold project through backend.", err))
				}
				envelope.Data.ExecutionLocation = "configured_backend"
				var registration *projects.RegisterProjectContractResult
				if scaffoldRegister && !input.DryRun && input.Mode != projectcontracts.ScaffoldModeDeclaration {
					registerEnvelope, err := commandCtx.Client.RegisterProjectContractFromBackend(ctx, commandCtx.CorrelationID, projects.RegisterProjectContractFromBackendInput{
						ProjectRef:  envelope.Data.Slug,
						ProjectRoot: envelope.Data.ProjectRoot,
					})
					if err != nil {
						// Creation already completed. Preserve that result before reporting the
						// separate registration failure; a retry must not imply no files exist.
						if opts.jsonOutput {
							_ = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
								Scaffold          projectcontracts.ScaffoldResult `json:"scaffold"`
								RegistrationState string                          `json:"registration_state"`
							}{envelope.Data, "failed"})
						} else {
							projectcontracts.RenderScaffold(cmd.OutOrStdout(), envelope.Data, false)
						}
						return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.register_failed", "projects", firstNonEmptyCLI(envelope.Data.Slug, input.Name), "Project was scaffolded, but backend registration failed.", err))
					}
					registration = &registerEnvelope.Data
				}
				if opts.jsonOutput {
					if registration != nil {
						if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
							Scaffold     projectcontracts.ScaffoldResult        `json:"scaffold"`
							Registration projects.RegisterProjectContractResult `json:"registration"`
						}{
							Scaffold:     envelope.Data,
							Registration: *registration,
						}); encodeErr != nil {
							return encodeErr
						}
						return nil
					}
					if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data); encodeErr != nil {
						return encodeErr
					}
				} else {
					projectcontracts.RenderScaffold(cmd.OutOrStdout(), envelope.Data, input.DryRun)
					if registration != nil {
						fmt.Fprintln(cmd.OutOrStdout())
						renderProjectRegistrationResult(cmd, *registration)
					} else if scaffoldRegister && input.DryRun {
						fmt.Fprintln(cmd.OutOrStdout(), "\nRegistration: skipped (dry run)")
					}
					fmt.Fprintf(cmd.OutOrStdout(), "\nBackend: %s\n", firstNonEmptyCLI(commandCtx.Config.MainURL, commandCtx.Config.SocketPath))
					renderResponseMeta(cmd, opts, envelope.Meta)
				}
				return nil
			}
			if cmd.Flags().Changed("directory") {
				input.DirectorySource = projectcontracts.ScaffoldDirectoryExplicit
			} else {
				var err error
				input, err = applyBoxDefaultScaffoldDirectory(opts, input)
				if err != nil {
					if !opts.jsonOutput {
						fmt.Fprintln(cmd.ErrOrStderr(), err.Error())
					}
					return err
				}
			}
			result, err := projectcontracts.ScaffoldProject(input)
			result.ExecutionLocation = "caller_local"
			if err != nil {
				if opts.jsonOutput && result.ProjectRoot != "" {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				return err
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if result.ProjectRoot != "" {
				projectcontracts.RenderScaffold(cmd.OutOrStdout(), result, input.DryRun)
			}
			return err
		},
	}
	scaffoldCmd.Flags().StringVar(&scaffoldOpts.Slug, "slug", "", "project slug")
	scaffoldCmd.Flags().StringVar(&scaffoldOpts.OwnerNode, "owner-node", "", "owner node key")
	scaffoldCmd.Flags().StringVar(&scaffoldOpts.Preset, "preset", projectcontracts.PresetMinimal, "scaffold preset")
	scaffoldCmd.Flags().StringVar(&scaffoldFacets, "facets", "", "comma-separated project facets")
	scaffoldCmd.Flags().StringVar(&scaffoldOpts.Directory, "directory", "", "parent directory for the project folder; defaults to LOOM Box Projects")
	scaffoldCmd.Flags().BoolVar(&scaffoldOpts.Force, "force", false, "overwrite generated scaffold files")
	scaffoldCmd.Flags().BoolVar(&scaffoldOpts.DryRun, "dry-run", false, "print planned files without writing")
	scaffoldCmd.Flags().BoolVar(&scaffoldBackend, "backend", false, "ask the configured LOOM backend to scaffold on its filesystem")
	scaffoldCmd.Flags().BoolVar(&scaffoldRegister, "register", false, "after backend scaffold succeeds, register the generated contract from the backend filesystem")

	scaffoldCleanupOpts := projectcontracts.ScaffoldCleanupOptions{}
	scaffoldCleanupCmd := &cobra.Command{
		Use:   "cleanup <project-root-or-slug>",
		Short: "Remove untouched generated scaffold examples from a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root, err := resolveProjectFacetRoot(ctx, opts, correlationID, args[0])
			if err != nil {
				return err
			}
			input := scaffoldCleanupOpts
			input.ProjectRoot = root
			result, err := projectcontracts.CleanupScaffoldExamples(input)
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else {
				projectcontracts.RenderScaffoldCleanup(cmd.OutOrStdout(), result)
			}
			return err
		},
	}
	scaffoldCleanupCmd.Flags().BoolVar(&scaffoldCleanupOpts.DryRun, "dry-run", false, "print cleanup plan without deleting generated example packages")
	scaffoldCmd.AddCommand(scaffoldCleanupCmd)
	cmd.AddCommand(scaffoldCmd)
	// Preserve the old explicit registry-only operation under a distinct name.
	// Normal create now owns the ordinary-files constructor.
	for _, child := range cmd.Commands() {
		if child.Name() == "create" {
			child.Use = "create-scope <name>"
		}
	}
	createCmd := &cobra.Command{Use: "create <name>", Short: "Create a minimal project with ordinary notes and repos folders", Args: cobra.ExactArgs(1), RunE: scaffoldCmd.RunE}
	createCmd.Flags().StringVar(&scaffoldOpts.Slug, "slug", "", "project slug")
	createCmd.Flags().StringVar(&scaffoldOpts.OwnerNode, "owner-node", "", "declared owner node key; does not select the execution filesystem")
	createCmd.Flags().StringVar(&scaffoldOpts.Directory, "directory", "", "parent directory; defaults to LOOM Box Projects")
	createCmd.Flags().BoolVar(&scaffoldOpts.DryRun, "dry-run", false, "show planned files without writing")
	createCmd.Flags().BoolVar(&scaffoldBackend, "backend", false, "create on the configured backend filesystem")
	createCmd.Flags().BoolVar(&scaffoldRegister, "register", false, "compatibility flag; backend creation already registers the project identity")
	cmd.AddCommand(createCmd)

	facetCmd := &cobra.Command{
		Use:   "facet",
		Short: "Manage local project facets",
	}
	facetAddOpts := projectcontracts.AddProjectFacetsOptions{}
	var facetAddBackend bool
	var facetAddNoRegister bool
	facetAddCmd := &cobra.Command{
		Use:   "add <project-root-or-slug> <facet> [facet...]",
		Short: "Add missing facet contracts and folders to an existing project",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if facetAddBackend {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				input := facetAddOpts
				if filepath.IsAbs(strings.TrimSpace(args[0])) {
					input.ProjectRoot = args[0]
				} else {
					input.ProjectRef = args[0]
				}
				input.Facets = args[1:]
				envelope, err := commandCtx.Client.AddProjectFacets(ctx, commandCtx.CorrelationID, input)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.facet_add_failed", "projects", args[0], "Could not add project facets through backend.", err))
				}
				var registration *projects.RegisterProjectContractResult
				if !input.DryRun && !facetAddNoRegister {
					registerEnvelope, err := commandCtx.Client.RegisterProjectContractFromBackend(ctx, commandCtx.CorrelationID, projects.RegisterProjectContractFromBackendInput{
						ProjectRef:  firstNonEmptyCLI(envelope.Data.Slug, input.ProjectRef),
						ProjectRoot: envelope.Data.ProjectRoot,
					})
					if err != nil {
						return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.register_failed", "projects", firstNonEmptyCLI(envelope.Data.Slug, input.ProjectRef, args[0]), "Project facets were added, but backend registration failed.", err))
					}
					registration = &registerEnvelope.Data
				}
				if opts.jsonOutput {
					if registration != nil {
						return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
							Facets       projectcontracts.AddProjectFacetsResult `json:"facets"`
							Registration projects.RegisterProjectContractResult  `json:"registration"`
						}{
							Facets:       envelope.Data,
							Registration: *registration,
						})
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
				}
				projectcontracts.RenderAddProjectFacets(cmd.OutOrStdout(), envelope.Data)
				if registration != nil {
					fmt.Fprintln(cmd.OutOrStdout())
					renderProjectRegistrationResult(cmd, *registration)
				} else if !input.DryRun && facetAddNoRegister {
					fmt.Fprintln(cmd.OutOrStdout(), "\nRegistration: skipped (--no-register)")
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nBackend: %s\n", firstNonEmptyCLI(commandCtx.Config.MainURL, commandCtx.Config.SocketPath))
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			root, err := resolveProjectFacetRoot(ctx, opts, correlationID, args[0])
			if err != nil {
				return err
			}
			input := facetAddOpts
			input.ProjectRoot = root
			input.Facets = args[1:]
			result, err := projectcontracts.AddProjectFacets(input)
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else {
				projectcontracts.RenderAddProjectFacets(cmd.OutOrStdout(), result)
			}
			return err
		},
	}
	facetAddCmd.Flags().BoolVar(&facetAddOpts.DryRun, "dry-run", false, "print planned facet changes without writing")
	facetAddCmd.Flags().BoolVar(&facetAddOpts.Force, "force", false, "overwrite generated facet files that already exist")
	facetAddCmd.Flags().BoolVar(&facetAddBackend, "backend", false, "ask the configured LOOM backend to add facets on its filesystem")
	facetAddCmd.Flags().BoolVar(&facetAddNoRegister, "no-register", false, "after backend apply, skip registering the updated contract")
	facetCmd.AddCommand(facetAddCmd)
	cmd.AddCommand(facetCmd)

	layoutMigrationOpts := projectcontracts.LayoutMigrationOptions{}
	var layoutMigrationDryRun bool
	var layoutMigrationBackend bool
	layoutMigrationCmd := &cobra.Command{
		Use:   "migrate-layout <project-root-or-ref>",
		Short: "Plan or apply migration to the canonical project-local .loom layout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if layoutMigrationDryRun && layoutMigrationOpts.Apply {
				return fmt.Errorf("--dry-run and --apply cannot be used together")
			}
			if layoutMigrationBackend {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				input := layoutMigrationOpts
				if filepath.IsAbs(strings.TrimSpace(args[0])) {
					input.ProjectRoot = args[0]
				} else {
					input.ProjectRef = args[0]
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.MigrateProjectLayout(ctx, commandCtx.CorrelationID, input)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.layout_migration_failed", "projects", args[0], "Could not migrate project layout through backend.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
				}
				projectcontracts.RenderLayoutMigration(cmd.OutOrStdout(), envelope.Data)
				fmt.Fprintf(cmd.OutOrStdout(), "\nBackend: %s\n", firstNonEmptyCLI(commandCtx.Config.MainURL, commandCtx.Config.SocketPath))
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			root, err := resolveProjectFacetRoot(ctx, opts, correlationID, args[0])
			if err != nil {
				return err
			}
			input := layoutMigrationOpts
			input.ProjectRoot = root
			result, migrationErr := projectcontracts.MigrateProjectLayout(input)
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			} else if result.ProjectRoot != "" {
				projectcontracts.RenderLayoutMigration(cmd.OutOrStdout(), result)
			}
			return migrationErr
		},
	}
	layoutMigrationCmd.Flags().BoolVar(&layoutMigrationDryRun, "dry-run", false, "print the migration plan without writing (the default)")
	layoutMigrationCmd.Flags().BoolVar(&layoutMigrationOpts.Apply, "apply", false, "apply the complete collision-free migration plan")
	layoutMigrationCmd.Flags().BoolVar(&layoutMigrationOpts.Yes, "yes", false, "confirm an --apply migration")
	layoutMigrationCmd.Flags().BoolVar(&layoutMigrationBackend, "backend", false, "run migration on the configured LOOM backend")
	cmd.AddCommand(layoutMigrationCmd)

	var exportMode string
	var exportOutput string
	var exportBackend bool
	var exportOverwrite bool
	exportCmd := &cobra.Command{
		Use:   "export <project-ref-or-path>",
		Short: "Export a deterministic human, portable, or archival project tar",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			mode, err := projectexport.ParseMode(exportMode)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.export_mode_invalid", "projects", exportMode, "Project export mode is invalid.", err))
			}
			if strings.TrimSpace(exportOutput) == "" {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.export_output_required", "projects", args[0], "Project export requires --out <archive.tar>.", nil))
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			var result projectexport.Summary
			if exportBackend {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
				}
				result, err = commandCtx.Client.ExportProject(ctx, commandCtx.CorrelationID, projectexport.Request{ProjectRef: args[0], Mode: mode}, exportOutput, exportOverwrite)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("project.export_failed", "projects", args[0], "Could not download backend project export.", err))
				}
			} else {
				root, err := resolveProjectFacetRoot(ctx, opts, correlationID, args[0])
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.export_target_invalid", "projects", args[0], "Could not resolve local project export target.", err))
				}
				result, err = projectexport.ExportToFile(ctx, root, exportOutput, exportOverwrite, projectexport.PlanOptions{Mode: mode})
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.export_failed", "projects", args[0], "Could not create project export.", err))
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderProjectExport(cmd, result)
			return nil
		},
	}
	exportCmd.Flags().StringVar(&exportMode, "mode", string(projectexport.ModePortable), "export mode: human, portable, or archival")
	exportCmd.Flags().StringVar(&exportOutput, "out", "", "caller-local output tar path (required)")
	exportCmd.Flags().BoolVar(&exportBackend, "backend", false, "create the archive on the backend and download its bytes here")
	exportCmd.Flags().BoolVar(&exportOverwrite, "overwrite", false, "replace the reviewed existing output path")
	cmd.AddCommand(exportCmd)

	validateOpts := struct {
		strict  bool
		backend bool
	}{}
	validateCmd := &cobra.Command{
		Use:   "validate <project-folder-or-ref>",
		Short: "Validate a LOOM project contract without registering it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			analysis, metaCorrelationID, err := analyzeProjectForCLI(cmd, opts, args[0], validateOpts.backend)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(analysis.Report); err != nil {
					return err
				}
			} else {
				projectcontracts.RenderValidation(cmd.OutOrStdout(), analysis.Report)
				renderProjectAnalysisLayout(cmd, analysis, args[0], validateOpts.backend)
				if metaCorrelationID != "" {
					renderResponseMeta(cmd, opts, response.Meta{CorrelationID: metaCorrelationID})
				}
			}
			if analysis.Report.Summary.Errors > 0 {
				return fmt.Errorf("project contract validation failed with %d error(s)", analysis.Report.Summary.Errors)
			}
			if validateOpts.strict && analysis.Report.Summary.Warnings > 0 {
				return fmt.Errorf("project contract validation failed strict mode with %d warning(s)", analysis.Report.Summary.Warnings)
			}
			return nil
		},
	}
	validateCmd.Flags().BoolVar(&validateOpts.strict, "strict", false, "fail when validation produces warnings")
	validateCmd.Flags().BoolVar(&validateOpts.backend, "backend", false, "ask the configured LOOM backend to validate a project on its filesystem")
	cmd.AddCommand(validateCmd)

	planOpts := struct {
		includeDiagnostics bool
		backend            bool
	}{}
	planCmd := &cobra.Command{
		Use:   "plan <project-folder-or-ref>",
		Short: "Print the read-only registration plan for a LOOM project contract",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			analysis, metaCorrelationID, err := analyzeProjectForCLI(cmd, opts, args[0], planOpts.backend)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(analysis.Plan); err != nil {
					return err
				}
			} else {
				projectcontracts.RenderPlan(cmd.OutOrStdout(), analysis.Plan, planOpts.includeDiagnostics || analysis.Plan.Summary.Errors > 0)
				renderProjectAnalysisLayout(cmd, analysis, args[0], planOpts.backend)
				if metaCorrelationID != "" {
					renderResponseMeta(cmd, opts, response.Meta{CorrelationID: metaCorrelationID})
				}
			}
			if analysis.Plan.Summary.Errors > 0 {
				return fmt.Errorf("project contract plan is blocked by %d validation error(s)", analysis.Plan.Summary.Errors)
			}
			return nil
		},
	}
	planCmd.Flags().BoolVar(&planOpts.includeDiagnostics, "include-diagnostics", false, "include diagnostics in human-readable plan output")
	planCmd.Flags().BoolVar(&planOpts.backend, "backend", false, "ask the configured LOOM backend to plan a project on its filesystem")
	cmd.AddCommand(planCmd)

	diffOpts := struct {
		project          string
		includeUnchanged bool
		strict           bool
		backend          bool
	}{}
	diffCmd := &cobra.Command{
		Use:   "diff <project-folder-or-ref>",
		Short: "Compare a LOOM project contract against the registered backend snapshot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if diffOpts.backend {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cfg, client, err := commandClient(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
				}
				envelope, err := client.AnalyzeProjectContractBackend(ctx, correlationID, backendAnalysisInputForRef(args[0]))
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", firstNonEmptyCLI(cfg.MainURL, cfg.SocketPath), "Could not analyze backend project contract.", err))
				}
				result := envelope.Data
				report := result.Diff
				if opts.jsonOutput {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
						return err
					}
				} else {
					projectdoctor.RenderDiff(cmd.OutOrStdout(), report, diffOpts.includeUnchanged)
					renderResponseMeta(cmd, opts, envelope.Meta)
				}
				if result.Analysis.Report.Summary.Errors > 0 {
					return fmt.Errorf("project contract validation failed with %d error(s)", result.Analysis.Report.Summary.Errors)
				}
				if diffOpts.strict && projectdoctor.DiffHasChanges(report) {
					return fmt.Errorf("project diff detected backend/registered drift")
				}
				return nil
			}
			analysis := projectcontracts.Analyze(args[0])
			projectRef := strings.TrimSpace(diffOpts.project)
			if projectRef == "" {
				projectRef = analysis.Plan.Project.Slug
			}
			var detail *projects.ProjectRegistrationDetail
			if projectRef != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				cfg, client, err := commandClient(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
				}
				envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, projectRef)
				if err != nil {
					if !isLocalClientNotFound(err) {
						return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project registration status.", err))
					}
				} else {
					detail = &envelope.Data
				}
			}
			report := projectdoctor.BuildDiff(analysis, detail)
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				projectdoctor.RenderDiff(cmd.OutOrStdout(), report, diffOpts.includeUnchanged)
			}
			if analysis.Report.Summary.Errors > 0 {
				return fmt.Errorf("project contract validation failed with %d error(s)", analysis.Report.Summary.Errors)
			}
			if diffOpts.strict && projectdoctor.DiffHasChanges(report) {
				return fmt.Errorf("project diff detected local/registered drift")
			}
			return nil
		},
	}
	diffCmd.Flags().StringVar(&diffOpts.project, "project", "", "registered project ref override")
	diffCmd.Flags().BoolVar(&diffOpts.includeUnchanged, "include-unchanged", false, "include unchanged rows in human-readable output")
	diffCmd.Flags().BoolVar(&diffOpts.strict, "strict", false, "fail when local and registered state differ")
	diffCmd.Flags().BoolVar(&diffOpts.backend, "backend", false, "ask the configured LOOM backend to compare the project on its filesystem")
	cmd.AddCommand(diffCmd)

	watchPlanOpts := projectwatch.BuildPlanInput{}
	watchPlanCmd := &cobra.Command{
		Use:   "watch-plan <project-ref-or-path>",
		Short: "Compile project notes/repo policy into node-agent watched-root desired state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ref := args[0]
			if watchPlanOpts.ProjectRoot == "" && localProjectPathExists(ref) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				plan, err := projectwatch.NewService(projectwatch.Deps{}).BuildPlan(ctx, ref, watchPlanOpts)
				if opts.jsonOutput {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
				}
				if err != nil {
					return err
				}
				if !opts.jsonOutput {
					renderProjectWatchPlan(cmd, plan)
				}
				return nil
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.BuildProjectWatchPlan(ctx, correlationID, ref, watchPlanOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not build project watch plan.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectWatchPlan(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	watchPlanCmd.Flags().StringVar(&watchPlanOpts.ProjectRoot, "project-root", "", "local project root override")
	watchPlanCmd.Flags().BoolVar(&watchPlanOpts.UseRegisteredSnapshot, "use-registered-snapshot", false, "use the registered contract snapshot if local files cannot be loaded")
	cmd.AddCommand(watchPlanCmd)

	workflowsCmd := &cobra.Command{
		Use:   "workflows",
		Short: "Inspect project workflow intent",
	}
	workflowsListCmd := &cobra.Command{
		Use:   "list <project-ref-or-path>",
		Short: "List workflow contracts from a local project folder or registered project plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			if localProjectPathExists(ref) {
				analysis := projectcontracts.Analyze(ref)
				result := projectWorkflowListResult{
					Project:   analysis.Plan.Project.Slug,
					Source:    "local_analysis",
					Workflows: analysis.Plan.Workflows,
				}
				if opts.jsonOutput {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
						return err
					}
				} else {
					renderProjectWorkflowList(cmd, result)
				}
				if analysis.Report.Summary.Errors > 0 {
					return fmt.Errorf("project workflow list is blocked by %d validation error(s)", analysis.Report.Summary.Errors)
				}
				return nil
			}

			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, ref)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project registration status.", err))
			}
			result, err := projectWorkflowListFromRegisteredDetail(envelope.Data)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.workflows_unavailable", "projects", ref, "Could not list project workflows.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderProjectWorkflowList(cmd, result)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	workflowsCmd.AddCommand(workflowsListCmd)
	workflowsInspectCmd := &cobra.Command{
		Use:   "inspect <project-ref-or-path> <workflow-key>",
		Short: "Inspect a workflow contract and its registered runtime surface",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := args[0]
			key := args[1]
			if localProjectPathExists(ref) {
				analysis := projectcontracts.Analyze(ref)
				workflow, ok := findProjectWorkflow(analysis.Plan.Workflows, key)
				if !ok {
					return fmt.Errorf("workflow not found: %s", key)
				}
				result := projectWorkflowInspectResult{WorkflowFacetItem: workflow}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				renderProjectWorkflowInspect(cmd, result, "local_analysis")
				if analysis.Report.Summary.Errors > 0 {
					return fmt.Errorf("project workflow inspect is blocked by %d validation error(s)", analysis.Report.Summary.Errors)
				}
				return nil
			}

			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, ref)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project registration status.", err))
			}
			result, err := projectWorkflowListFromRegisteredDetail(envelope.Data)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.workflows_unavailable", "projects", ref, "Could not inspect project workflow.", err))
			}
			workflow, ok := findProjectWorkflow(result.Workflows, key)
			if !ok {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.workflow_not_found", "projects", ref, "Project workflow was not found.", fmt.Errorf("workflow not found: %s", key)))
			}
			inspectResult := projectWorkflowInspectResult{WorkflowFacetItem: workflow}
			if workflow.RegisteredWorkflowID != "" {
				if jobsEnvelope, err := client.ListJobs(ctx, correlationID, jobs.ListFilter{Limit: 5, JobType: jobs.TypeWorkflowRun, WorkflowRef: workflow.RegisteredWorkflowID, ProjectRef: ref}); err == nil {
					inspectResult.RecentJobs = jobsEnvelope.Data
				}
			}
			if capability := projectWorkflowCapabilityRef(workflow); capability != "" {
				if callsEnvelope, err := client.ListCapabilityCalls(ctx, correlationID, routing.CapabilityCallFilter{Limit: 5, CapabilityEndpointRef: capability}); err == nil {
					inspectResult.RecentCapabilityCalls = callsEnvelope.Data
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(inspectResult)
			}
			renderProjectWorkflowInspect(cmd, inspectResult, result.Source)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	workflowsCmd.AddCommand(workflowsInspectCmd)
	cmd.AddCommand(workflowsCmd)

	registerOpts := struct {
		strict         bool
		idempotencyKey string
		backend        bool
	}{}
	registerCmd := &cobra.Command{
		Use:   "register <project-folder-or-ref>",
		Short: "Register a validated LOOM project folder with the backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if registerOpts.backend {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cfg, client, err := commandClient(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
				}
				client, _ = withEffectIdempotency(client, registerOpts.idempotencyKey, "project.contract.register.backend")
				input := backendRegisterInputForRef(args[0])
				input.Strict = registerOpts.strict
				envelope, err := client.RegisterProjectContractFromBackend(ctx, correlationID, input)
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", firstNonEmptyCLI(cfg.MainURL, cfg.SocketPath), "Could not register backend project contract.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				renderProjectRegistrationResult(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			analysis := projectcontracts.Analyze(args[0])
			if analysis.Report.Summary.Errors > 0 {
				if opts.jsonOutput {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(analysis.Report)
				} else {
					projectcontracts.RenderValidation(cmd.OutOrStdout(), analysis.Report)
				}
				return fmt.Errorf("project contract validation failed with %d error(s)", analysis.Report.Summary.Errors)
			}
			if registerOpts.strict && analysis.Report.Summary.Warnings > 0 {
				if opts.jsonOutput {
					_ = json.NewEncoder(cmd.OutOrStdout()).Encode(analysis.Report)
				} else {
					projectcontracts.RenderValidation(cmd.OutOrStdout(), analysis.Report)
				}
				return fmt.Errorf("project contract validation failed strict mode with %d warning(s)", analysis.Report.Summary.Warnings)
			}
			input, err := buildRegisterProjectContractInput(analysis)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.contract_invalid", "projects", args[0], "Project contract could not be prepared for registration.", err))
			}

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, registerOpts.idempotencyKey, "project.contract.register")
			envelope, err := client.RegisterProjectContract(ctx, correlationID, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not register project contract.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectRegistrationResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	registerCmd.Flags().BoolVar(&registerOpts.strict, "strict", false, "fail when validation produces warnings")
	registerCmd.Flags().StringVar(&registerOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	registerCmd.Flags().BoolVar(&registerOpts.backend, "backend", false, "ask the configured LOOM backend to analyze and register a project on its filesystem")
	cmd.AddCommand(registerCmd)

	statusCmd := &cobra.Command{
		Use:   "status <project-ref>",
		Short: "Show project registration status and facet inventory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project registration status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectRegistrationStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(statusCmd)
	addProjectDeclarationCommands(cmd, opts, planCmd, statusCmd)

	doctorOpts := struct {
		projectRoot           string
		useRegisteredSnapshot bool
		strict                bool
		backend               bool
	}{}
	doctorCmd := &cobra.Command{
		Use:   "doctor <project-ref-or-path>",
		Short: "Diagnose project registration, activation, drift, and v0.3 expansion health",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ref := strings.TrimSpace(args[0])
			var analysis *projectcontracts.Analysis
			if doctorOpts.projectRoot != "" {
				local := projectcontracts.Analyze(doctorOpts.projectRoot)
				analysis = &local
			} else if localProjectPathExists(ref) {
				local := projectcontracts.Analyze(ref)
				analysis = &local
				ref = local.Plan.Project.Slug
			}
			if ref == "" && analysis != nil {
				ref = analysis.Plan.Project.Slug
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			var backendResult *projectdoctor.BackendAnalysisResult
			if doctorOpts.backend && analysis == nil {
				envelope, err := client.AnalyzeProjectContractBackend(ctx, correlationID, backendAnalysisInputForRef(ref))
				if err != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", firstNonEmptyCLI(cfg.MainURL, cfg.SocketPath), "Could not analyze backend project contract.", err))
				}
				result := envelope.Data
				backendResult = &result
				analysis = &result.Analysis
				ref = firstNonEmptyCLI(result.ProjectRef, ref)
			}
			var detail *projects.ProjectRegistrationDetail
			if backendResult != nil {
				detail = backendResult.Detail
			}
			if ref != "" {
				envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, ref)
				if err != nil {
					if backendResult == nil && (analysis == nil || !isLocalClientNotFound(err)) {
						return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project registration status.", err))
					}
				} else {
					detail = &envelope.Data
				}
			}
			var syncStatus *projectwatch.ProjectSyncStatus
			var backupStatus *projectwatch.ProjectBackupStatus
			if detail != nil && detail.Registration != nil {
				if envelope, err := client.GetProjectSyncStatus(ctx, correlationID, ref); err == nil {
					status := envelope.Data
					syncStatus = &status
				}
				if envelope, err := client.GetProjectBackupStatus(ctx, correlationID, ref); err == nil {
					status := envelope.Data
					backupStatus = &status
				}
			}
			if analysis == nil && doctorOpts.useRegisteredSnapshot && detail != nil && detail.Registration != nil {
				// Registered snapshots are already included through the detail checks.
			}
			report := projectdoctor.Report{}
			if backendResult != nil {
				report = backendResult.Report
			} else {
				report = projectdoctor.BuildReport(projectdoctor.ReportInput{
					ProjectRef: ref,
					Source:     "loom.project.doctor",
					Local:      analysis,
					Detail:     detail,
					Sync:       syncStatus,
					Backup:     backupStatus,
					CreatedAt:  time.Now().UTC(),
				})
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
					return err
				}
			} else {
				projectdoctor.RenderReport(cmd.OutOrStdout(), report)
				if analysis != nil {
					renderProjectAnalysisLayout(cmd, *analysis, args[0], doctorOpts.backend)
				}
			}
			if projectdoctor.HasFailures(report) {
				return fmt.Errorf("project doctor found %d error(s) and %d blocked check(s)", report.Summary.Errors, report.Summary.Blocked)
			}
			if doctorOpts.strict && projectdoctor.HasWarnings(report) {
				return fmt.Errorf("project doctor strict mode found %d warning/unknown check(s)", report.Summary.Warnings+report.Summary.Unknown)
			}
			return nil
		},
	}
	doctorCmd.Flags().StringVar(&doctorOpts.projectRoot, "project-root", "", "local project root to compare against a registered project ref")
	doctorCmd.Flags().BoolVar(&doctorOpts.useRegisteredSnapshot, "use-registered-snapshot", false, "allow doctor to run from the registered snapshot without local files")
	doctorCmd.Flags().BoolVar(&doctorOpts.strict, "strict", false, "fail when doctor reports warnings or unknown checks")
	doctorCmd.Flags().BoolVar(&doctorOpts.backend, "backend", false, "ask the configured LOOM backend to diagnose the project on its filesystem")
	cmd.AddCommand(doctorCmd)

	activateOpts := struct {
		facet          string
		projectRoot    string
		idempotencyKey string
	}{}
	activateCmd := &cobra.Command{
		Use:   "activate <project-ref>",
		Short: "Activate a registered project or project facet",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			projectRoot, err := normalizeProjectRootOverride(activateOpts.projectRoot)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("project.root_invalid", "projects", args[0], "Project root override is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, activateOpts.idempotencyKey, "project.activate")
			envelope, err := client.ActivateProject(ctx, correlationID, args[0], projects.ActivateProjectInput{Facet: activateOpts.facet, ProjectRoot: projectRoot})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not activate project.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectActivation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	activateCmd.Flags().StringVar(&activateOpts.facet, "facet", "", "facet to activate (supports scripts, workflows, schedules, direct-events, connectors, modules)")
	activateCmd.Flags().StringVar(&activateOpts.projectRoot, "project-root", "", "local project root override for local-only facet activation")
	activateCmd.Flags().StringVar(&activateOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(activateCmd)

	deactivateOpts := struct {
		input          projects.DeactivateProjectInput
		idempotencyKey string
	}{}
	deactivateCmd := &cobra.Command{
		Use:   "deactivate <project-ref>",
		Short: "Deactivate a project-owned facet without deleting history",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, deactivateOpts.idempotencyKey, "project.deactivate")
			envelope, err := client.DeactivateProject(ctx, correlationID, args[0], deactivateOpts.input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not deactivate project.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectDeactivation(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	deactivateCmd.Flags().StringVar(&deactivateOpts.input.Facet, "facet", "", "facet to deactivate (scripts, schedules, direct-events, connectors, modules, watched-roots, workflows)")
	deactivateCmd.Flags().StringVar(&deactivateOpts.input.ProjectRoot, "project-root", "", "local project root override")
	deactivateCmd.Flags().StringVar(&deactivateOpts.input.Reason, "reason", "", "human-readable reason for deactivation")
	deactivateCmd.Flags().BoolVar(&deactivateOpts.input.DryRun, "dry-run", false, "show what would be disabled without changing backend state")
	deactivateCmd.Flags().StringVar(&deactivateOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	_ = deactivateCmd.MarkFlagRequired("facet")
	cmd.AddCommand(deactivateCmd)

	archiveOpts := struct {
		input          storagearchive.ProjectArchiveInput
		idempotencyKey string
	}{}
	archiveCmd := &cobra.Command{
		Use:   "archive <project-ref>",
		Short: "Archive project storage and disable project-owned runtime surfaces",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, archiveOpts.idempotencyKey, "project.archive")
			envelope, err := client.ArchiveProject(ctx, correlationID, args[0], archiveOpts.input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not archive project.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectArchive(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	archiveCmd.Flags().StringVar(&archiveOpts.input.SourceRef, "source", "", "explicit storage-catalog source to archive; defaults to a confined snapshot of the registered project root")
	archiveCmd.Flags().StringVar(&archiveOpts.input.TargetPath, "to", "", "main archive target path; defaults to main/Archive/Projects/<project>")
	archiveCmd.Flags().StringVar(&archiveOpts.input.Reason, "reason", "", "human-readable reason for archiving")
	archiveCmd.Flags().BoolVar(&archiveOpts.input.SkipStorageArchive, "skip-storage-archive", false, "write runtime archive manifest and deactivate runtime without copying storage")
	archiveCmd.Flags().BoolVar(&archiveOpts.input.DryRun, "dry-run", false, "show what would be archived and disabled without changing backend state")
	archiveCmd.Flags().StringVar(&archiveOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")

	archiveCmd.AddCommand(&cobra.Command{
		Use:   "inspect <project-ref>",
		Short: "Inspect the runtime archive manifest and archive state for a project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.InspectProjectArchive(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project archive.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectArchiveInspect(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	restoreOpts := storagearchive.ProjectArchiveRestoreInput{DryRun: true}
	restoreCmd := &cobra.Command{
		Use:   "restore <project-ref>",
		Short: "Plan a dry-run restore from a project archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			restoreOpts.DryRun = true
			envelope, err := client.PlanProjectArchiveRestore(ctx, correlationID, args[0], restoreOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not plan project archive restore.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectArchiveRestorePlan(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&restoreOpts.ToNode, "to-node", "", "node that would receive restored project files")
	restoreCmd.Flags().BoolVar(&restoreOpts.DryRun, "dry-run", true, "restore planning is dry-run only in this slice")
	addProjectPhysicalArchiveCommands(archiveCmd, opts, false)
	addProjectPhysicalArchiveCommands(restoreCmd, opts, true)
	archiveCmd.AddCommand(restoreCmd)

	migrateOpts := storagearchive.ProjectRuntimeMigrationInput{DryRun: true}
	migrateCmd := &cobra.Command{
		Use:   "migrate-runtime <project-ref>",
		Short: "Plan a dry-run project runtime migration without rewriting old capability URLs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			migrateOpts.DryRun = true
			envelope, err := client.PlanProjectRuntimeMigration(ctx, correlationID, args[0], migrateOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not plan project runtime migration.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectRuntimeMigrationPlan(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	migrateCmd.Flags().StringVar(&migrateOpts.ToNode, "to-node", "", "node that would own successor runtime URLs")
	migrateCmd.Flags().BoolVar(&migrateOpts.DryRun, "dry-run", true, "runtime migration planning is dry-run only in this slice")
	archiveCmd.AddCommand(migrateCmd)
	cmd.AddCommand(archiveCmd)

	applyWatchOpts := struct {
		input          projects.ApplyProjectWatchPolicyInput
		emitShell      bool
		idempotencyKey string
	}{}
	applyWatchCmd := &cobra.Command{
		Use:   "apply-watch-policy <project-ref>",
		Short: "Record project watched-root desired state on main and print node-agent apply commands",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, applyWatchOpts.idempotencyKey, "project.watch_policy.apply")
			envelope, err := client.ApplyProjectWatchPolicy(ctx, correlationID, args[0], applyWatchOpts.input)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not apply project watch policy.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if applyWatchOpts.emitShell {
				renderProjectWatchShell(cmd, envelope.Data.Commands)
			} else {
				renderProjectWatchApply(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	applyWatchCmd.Flags().StringVar(&applyWatchOpts.input.ProjectRoot, "project-root", "", "local project root override")
	applyWatchCmd.Flags().BoolVar(&applyWatchOpts.input.DryRun, "dry-run", false, "compute desired state without writing project watched-root registration rows")
	applyWatchCmd.Flags().BoolVar(&applyWatchOpts.input.UseRegisteredSnapshot, "use-registered-snapshot", false, "use the registered contract snapshot if local files cannot be loaded")
	applyWatchCmd.Flags().BoolVar(&applyWatchOpts.emitShell, "emit-shell", false, "print shell commands only")
	applyWatchCmd.Flags().StringVar(&applyWatchOpts.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	cmd.AddCommand(applyWatchCmd)

	syncStatusCmd := &cobra.Command{
		Use:   "sync-status <project-ref>",
		Short: "Show project watched-root sync status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetProjectSyncStatus(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project sync status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectSyncStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(syncStatusCmd)

	backupStatusCmd := &cobra.Command{
		Use:   "backup-status <project-ref>",
		Short: "Show project watched-root backup status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetProjectBackupStatus(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect project backup status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderProjectBackupStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(backupStatusCmd)
}

func splitScaffoldFacets(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func normalizeProjectRootOverride(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", nil
	}
	if filepath.IsAbs(root) {
		return filepath.Clean(root), nil
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func applyBoxDefaultScaffoldDirectory(opts *options, input projectcontracts.ScaffoldOptions) (projectcontracts.ScaffoldOptions, error) {
	resolved, err := resolveBoxForCLI(opts, boxCommandFlags{})
	if err != nil {
		return input, err
	}
	status := box.Inspect(resolved)
	if status.State != "ok" {
		initCommand := fmt.Sprintf("loom box init --path %s --profile %s", shellQuoteIfNeeded(status.RootPath), status.Profile)
		switch status.State {
		case "missing":
			return input, fmt.Errorf("LOOM Box is not initialized at %s; run `%s` or pass --directory", status.RootPath, initCommand)
		default:
			return input, fmt.Errorf("LOOM Box is %s at %s; run `loom box status --path %s`, fix the Box, or pass --directory", status.State, status.RootPath, shellQuoteIfNeeded(status.RootPath))
		}
	}
	input.Directory = status.DefaultProjectPath
	input.DirectorySource = projectcontracts.ScaffoldDirectoryBox
	input.BoxDefaultUsed = true
	input.BoxRoot = status.RootPath
	input.BoxProfile = status.Profile
	input.BoxContractPath = status.ContractPath
	return input, nil
}

func resolveProjectFacetRoot(ctx context.Context, opts *options, correlationID, ref string) (string, error) {
	normalized, err := normalizeProjectRootOverride(ref)
	if err != nil {
		return "", err
	}
	if normalized != "" {
		if info, statErr := os.Stat(normalized); statErr == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("project root is not a directory: %s", normalized)
			}
			return normalized, nil
		}
	}
	cfg, client, err := commandClient(opts)
	if err != nil {
		return "", fmt.Errorf("project %q is not a readable local directory and backend lookup is unavailable: %w", ref, err)
	}
	envelope, err := client.GetProjectRegistrationStatus(ctx, correlationID, ref)
	if err != nil {
		return "", fmt.Errorf("project %q is not a readable local directory and could not be resolved through %s: %w", ref, cfg.SocketPath, err)
	}
	if envelope.Data.Registration == nil || strings.TrimSpace(envelope.Data.Registration.ProjectRoot) == "" {
		return "", fmt.Errorf("project %q has no registered project root", ref)
	}
	return normalizeProjectRootOverride(envelope.Data.Registration.ProjectRoot)
}

func analyzeProjectForCLI(cmd *cobra.Command, opts *options, ref string, backend bool) (projectcontracts.Analysis, string, error) {
	if !backend {
		return projectcontracts.Analyze(ref), "", nil
	}
	correlationID := correlation.Normalize(opts.correlationID)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cfg, client, err := commandClient(opts)
	if err != nil {
		return projectcontracts.Analysis{}, "", renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
	}
	envelope, err := client.AnalyzeProjectContractBackend(ctx, correlationID, backendAnalysisInputForRef(ref))
	if err != nil {
		return projectcontracts.Analysis{}, "", renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", firstNonEmptyCLI(cfg.MainURL, cfg.SocketPath), "Could not analyze backend project contract.", err))
	}
	return envelope.Data.Analysis, envelope.Meta.CorrelationID, nil
}

func backendAnalysisInputForRef(ref string) projectcontracts.BackendAnalysisInput {
	ref = strings.TrimSpace(ref)
	if filepath.IsAbs(ref) {
		return projectcontracts.BackendAnalysisInput{ProjectRoot: filepath.Clean(ref)}
	}
	return projectcontracts.BackendAnalysisInput{ProjectRef: ref}
}

func backendRegisterInputForRef(ref string) projects.RegisterProjectContractFromBackendInput {
	analysisInput := backendAnalysisInputForRef(ref)
	return projects.RegisterProjectContractFromBackendInput{
		ProjectRef:  analysisInput.ProjectRef,
		ProjectRoot: analysisInput.ProjectRoot,
	}
}

func buildRegisterProjectContractInput(analysis projectcontracts.Analysis) (projects.RegisterProjectContractInput, error) {
	return projectregistration.BuildInput(analysis, "loom.project.register")
}

type projectWorkflowListResult struct {
	Project   string                               `json:"project"`
	Source    string                               `json:"source"`
	Workflows []projectcontracts.WorkflowFacetItem `json:"workflows"`
}

type projectWorkflowInspectResult struct {
	projectcontracts.WorkflowFacetItem
	RecentJobs            []jobs.Job               `json:"recent_jobs,omitempty"`
	RecentCapabilityCalls []routing.CapabilityCall `json:"recent_capability_calls,omitempty"`
}

func projectWorkflowListFromRegisteredDetail(detail projects.ProjectRegistrationDetail) (projectWorkflowListResult, error) {
	if detail.Registration == nil {
		return projectWorkflowListResult{}, fmt.Errorf("project has no registered project contract")
	}
	var plan projectcontracts.ProjectPlan
	if len(detail.Registration.RegistrationPlan) > 0 {
		if err := json.Unmarshal(detail.Registration.RegistrationPlan, &plan); err != nil {
			return projectWorkflowListResult{}, fmt.Errorf("decode registered project plan: %w", err)
		}
	}
	project := plan.Project.Slug
	if project == "" {
		project = detail.Project.Project.Slug
	}
	workflows := append([]projectcontracts.WorkflowFacetItem{}, plan.Workflows...)
	registrations := map[string]projects.ProjectWorkflowRegistration{}
	for _, registration := range detail.WorkflowRegistrations {
		registrations[registration.WorkflowKey] = registration
	}
	for idx := range workflows {
		key := firstNonEmpty(workflows[idx].Key, workflows[idx].WorkflowID)
		if registration, ok := registrations[key]; ok {
			mergeWorkflowRegistration(&workflows[idx], registration)
			delete(registrations, key)
		}
	}
	for _, registration := range registrations {
		workflow := projectcontracts.WorkflowFacetItem{
			Key:                registration.WorkflowKey,
			WorkflowID:         registration.WorkflowKey,
			Folder:             registration.WorkflowFolder,
			ManifestPath:       registration.WorkflowManifestPath,
			ManifestHash:       registration.WorkflowManifestHash,
			ImplementationKind: registration.ImplementationKind,
			CapabilityAddress:  registration.CapabilityAddress,
		}
		mergeWorkflowRegistration(&workflow, registration)
		workflows = append(workflows, workflow)
	}
	sort.SliceStable(workflows, func(i, j int) bool {
		return firstNonEmpty(workflows[i].Key, workflows[i].WorkflowID) < firstNonEmpty(workflows[j].Key, workflows[j].WorkflowID)
	})
	return projectWorkflowListResult{
		Project:   project,
		Source:    "registered_plan",
		Workflows: workflows,
	}, nil
}

func mergeWorkflowRegistration(workflow *projectcontracts.WorkflowFacetItem, registration projects.ProjectWorkflowRegistration) {
	if workflow == nil {
		return
	}
	workflow.RegistrationStatus = registration.ActivationStatus
	workflow.RegisteredRuntimeKind = registration.RuntimeKind
	workflow.RegisteredWorkflowID = ptrOrEmpty(registration.WorkflowID)
	workflow.RegisteredVersionID = ptrOrEmpty(registration.WorkflowVersionID)
	workflow.RuntimeBindingID = ptrOrEmpty(registration.RuntimeBindingID)
	workflow.CapabilityEndpointID = ptrOrEmpty(registration.CapabilityEndpointID)
	workflow.EndpointVersionID = ptrOrEmpty(registration.CapabilityEndpointVersionID)
	if strings.TrimSpace(registration.CapabilityAddress) != "" {
		workflow.CapabilityAddress = registration.CapabilityAddress
	}
	if strings.TrimSpace(registration.ProviderAddress) != "" {
		workflow.ProviderAddress = registration.ProviderAddress
	}
	if strings.TrimSpace(registration.RuntimeKind) != "" {
		workflow.WorkflowCapabilityStatus = "registered"
	}
	if strings.TrimSpace(registration.ActivationStatus) != "" {
		workflow.ActivationStatus = registration.ActivationStatus
	}
}

func projectWorkflowsFromPlanJSON(planJSON json.RawMessage) []projectcontracts.WorkflowFacetItem {
	if len(planJSON) == 0 {
		return nil
	}
	var plan projectcontracts.ProjectPlan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil
	}
	return plan.Workflows
}

func renderProjectRegistrationResult(cmd *cobra.Command, result projects.RegisterProjectContractResult) {
	action := "unchanged"
	if result.Created {
		action = "created"
	} else if result.Updated {
		action = "updated"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project registration: %s\n", action)
	renderProjectRegistrationCore(cmd, result.Detail)
	if len(result.ChangedKeys) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", strings.Join(result.ChangedKeys, ", "))
	}
	if len(result.EventIDs) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Events: %s\n", strings.Join(result.EventIDs, ", "))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Next:")
	fmt.Fprintf(cmd.OutOrStdout(), "  loom project status %s\n", result.Detail.Project.Project.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "  loom project activate %s\n", result.Detail.Project.Project.Slug)
}

func renderProjectRegistrationStatus(cmd *cobra.Command, detail projects.ProjectRegistrationDetail) {
	renderProjectRegistrationCore(cmd, detail)
	if detail.Registration == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "Registration: none")
		return
	}
	if len(detail.Facets) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Facets:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "KEY\tENABLED\tPRESENT\tSTATUS")
		for _, facet := range detail.Facets {
			fmt.Fprintf(writer, "%s\t%t\t%t\t%s\n", facet.FacetKey, facet.Enabled, facet.Present, facet.FacetStatus)
		}
		_ = writer.Flush()
	}
	renderProjectScriptExposures(cmd, detail.ScriptExposures)
	renderProjectScheduleRegistrations(cmd, detail.ScheduleRegistrations)
	renderProjectDirectEventRegistrations(cmd, detail.DirectEventRegistrations)
	renderProjectConnectorRegistrations(cmd, detail.ConnectorRegistrations)
	renderProjectModuleRegistrations(cmd, detail.ModuleRegistrations)
	renderProjectWorkflowRegistrations(cmd, detail.WorkflowRegistrations)
	renderProjectWatchedRootRegistrations(cmd, detail.WatchedRootRegistrations)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	if len(detail.ScriptExposures) == 0 && len(detail.ScheduleRegistrations) == 0 && len(detail.DirectEventRegistrations) == 0 && len(detail.ConnectorRegistrations) == 0 && len(detail.ModuleRegistrations) == 0 && len(detail.WorkflowRegistrations) == 0 && len(detail.WatchedRootRegistrations) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No project facets have been activated by this registration.")
	}
}

func renderProjectActivation(cmd *cobra.Command, detail projects.ProjectRegistrationDetail) {
	activation := "none"
	if detail.Registration != nil {
		activation = detail.Registration.ActivationStatus
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project activation: %s\n", activation)
	renderProjectRegistrationCore(cmd, detail)
	if facets := projectRegistrationActivatedFacetKeys(detail.Facets); len(facets) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Activated facets: %s\n", strings.Join(facets, ", "))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Activated facets: none")
	}
	if projectRegistrationFacetActivated(detail.Facets, "scripts") {
		activeCapabilities := activeProjectScriptCapabilityAddresses(detail.ScriptExposures)
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: scripts activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Scripts: %d registered\n", len(detail.ScriptExposures))
		fmt.Fprintf(cmd.OutOrStdout(), "Exposed capabilities: %d\n", len(activeCapabilities))
		if len(activeCapabilities) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Capabilities:")
			for _, capability := range activeCapabilities {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s active script\n", capability)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			for _, capability := range activeCapabilities {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom capability inspect %s\n", capability)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom capability call %s --input '{\"message\":\"hello\"}' --wait\n", capability)
			}
		}
	}
	if projectRegistrationFacetActivated(detail.Facets, "workflows") {
		workflows := []projectcontracts.WorkflowFacetItem{}
		if detail.Registration != nil {
			workflows = projectWorkflowsFromPlanJSON(detail.Registration.RegistrationPlan)
		}
		discovered, executable, scriptBacked, blocked := projectWorkflowCounts(workflows)
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: workflows activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Project workflows: %d discovered, %d executable, %d script-backed, %d blocked\n", discovered, executable, scriptBacked, blocked)
		if scriptBacked > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Script-backed workflow shims:")
			for _, workflow := range workflows {
				if strings.TrimSpace(workflow.ImplementationKind) != projectcontracts.WorkflowImplementationScript {
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "  %s -> %s\n", firstNonEmpty(workflow.WorkflowID, workflow.Key), dashIfEmpty(projectWorkflowCapabilityRef(workflow)))
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			fmt.Fprintf(cmd.OutOrStdout(), "  loom project workflows list %s\n", detail.Project.Project.Slug)
		}
	}
	if projectRegistrationFacetActivated(detail.Facets, "schedules") {
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: schedules activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Project schedules: %d registered\n", len(detail.ScheduleRegistrations))
		if len(detail.ScheduleRegistrations) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			for _, registration := range detail.ScheduleRegistrations {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom schedule inspect %s\n", registration.BackendScheduleKey)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom schedule fire %s --reason 'manual project schedule check'\n", registration.BackendScheduleKey)
			}
		}
	}
	if projectRegistrationFacetActivated(detail.Facets, "direct_events") {
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: direct_events activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Project direct events: %d registered\n", len(detail.DirectEventRegistrations))
		if len(detail.DirectEventRegistrations) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			for _, registration := range detail.DirectEventRegistrations {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom direct-event endpoint inspect %s\n", registration.BackendEndpointSlug)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom direct-event endpoint resume %s --reason 'enable project direct event'\n", registration.BackendEndpointSlug)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom direct-event ingest %s --body-json '{\"id\":\"manual-test\"}'\n", registration.BackendEndpointSlug)
			}
		}
	}
	if projectRegistrationFacetActivated(detail.Facets, "connectors") {
		activeCapabilities := activeProjectConnectorCapabilityAddresses(detail.ConnectorRegistrations)
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: connectors activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Connectors: %d registered\n", len(detail.ConnectorRegistrations))
		if providers := projectConnectorProviderAddresses(detail.ConnectorRegistrations); len(providers) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Providers:")
			for _, provider := range providers {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", provider)
			}
		}
		if len(activeCapabilities) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Capabilities:")
			for _, capability := range activeCapabilities {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", capability)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			for _, provider := range projectConnectorProviderAddresses(detail.ConnectorRegistrations) {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom provider inspect %s\n", provider)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom capabilities list --provider %s\n", provider)
			}
			for _, capability := range activeCapabilities {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom capability call %s --input '{\"message\":\"hello\"}' --wait\n", capability)
			}
		}
	}
	if projectRegistrationFacetActivated(detail.Facets, "modules") {
		fmt.Fprintln(cmd.OutOrStdout(), "Facet: modules activated")
		fmt.Fprintf(cmd.OutOrStdout(), "Modules: %d registered\n", len(detail.ModuleRegistrations))
		if modules := projectModuleIDs(detail.ModuleRegistrations); len(modules) > 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Module packages:")
			for _, moduleID := range modules {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", moduleID)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Next:")
			fmt.Fprintf(cmd.OutOrStdout(), "  loom modules list --project %s\n", detail.Project.Project.Slug)
			for _, moduleID := range modules {
				fmt.Fprintf(cmd.OutOrStdout(), "  loom module inspect %s\n", moduleID)
				fmt.Fprintf(cmd.OutOrStdout(), "  loom module install %s\n", moduleID)
			}
		}
	}
	renderProjectScriptExposures(cmd, detail.ScriptExposures)
	renderProjectScheduleRegistrations(cmd, detail.ScheduleRegistrations)
	renderProjectDirectEventRegistrations(cmd, detail.DirectEventRegistrations)
	renderProjectConnectorRegistrations(cmd, detail.ConnectorRegistrations)
	renderProjectModuleRegistrations(cmd, detail.ModuleRegistrations)
	renderProjectWatchedRootRegistrations(cmd, detail.WatchedRootRegistrations)
}

func renderProjectDeactivation(cmd *cobra.Command, result projects.ProjectDeactivationResult) {
	action := "unchanged"
	if result.DryRun {
		action = "dry-run"
	} else if result.Changed {
		action = "deactivated"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project deactivation: %s\n", action)
	if result.Facet != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Facet: %s\n", result.Facet)
	}
	renderProjectRegistrationCore(cmd, result.Detail)
	if len(result.Actions) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Actions:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATUS\tKIND\tKEY\tREF\tSUMMARY")
		for _, action := range result.Actions {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				action.Status,
				action.Kind,
				action.Key,
				dashIfEmpty(action.Ref),
				action.Summary,
			)
		}
		_ = writer.Flush()
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Next:")
	fmt.Fprintf(cmd.OutOrStdout(), "  loom project doctor %s --use-registered-snapshot\n", result.Detail.Project.Project.Slug)
}

func renderProjectArchive(cmd *cobra.Command, result storagearchive.ProjectArchiveResult) {
	action := "archived"
	if result.DryRun {
		action = "dry-run"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project archive: %s\n", action)
	renderProjectRegistrationCore(cmd, result.Project)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime archive: %s\n", result.RuntimeManifest.ProjectRuntimeArchiveID)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime manifest: %s\n", dashIfEmpty(result.RuntimeManifestPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Source kind: %s\n", dashIfEmpty(result.RuntimeManifest.SourceKind))
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", result.RuntimeManifest.SourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.RuntimeManifest.TargetPath)
	if result.FilesystemSnapshotPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Filesystem snapshot: %s\n", result.FilesystemSnapshotPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime rule: %s\n", result.RuntimeManifest.RuntimeOwnershipRule)
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime migration: %s\n", result.RuntimeManifest.SuccessorPolicy.Status)
	if !result.StorageArchiveSkipped {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage archive: %s\n", dashIfEmpty(result.StorageArchive.ArchiveManifest.ArchiveManifestID))
		fmt.Fprintf(cmd.OutOrStdout(), "Storage manifest: %s\n", dashIfEmpty(result.StorageArchive.ManifestPath))
		fmt.Fprintf(cmd.OutOrStdout(), "Archived entries: %d\n", len(result.StorageArchive.Entries))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Safe to delete local source: %t\n", result.SafeToDelete)
	if len(result.Deactivations) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Runtime deactivations:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "FACET\tCHANGED\tACTIONS")
		for _, deactivation := range result.Deactivations {
			fmt.Fprintf(writer, "%s\t%t\t%d\n", deactivation.Facet, deactivation.Changed, len(deactivation.Actions))
		}
		_ = writer.Flush()
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Next:")
	fmt.Fprintf(cmd.OutOrStdout(), "  loom project archive inspect %s\n", result.Project.Project.Project.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "  loom project archive restore %s --dry-run\n", result.Project.Project.Project.Slug)
}

func renderProjectArchiveInspect(cmd *cobra.Command, result storagearchive.ProjectArchiveInspectResult) {
	fmt.Fprintln(cmd.OutOrStdout(), "Project archive inspect")
	if physical := result.Physical; physical != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", physical.ProjectID)
		fmt.Fprintf(cmd.OutOrStdout(), "Physical operation: %s\n", dashIfEmpty(physical.OperationID))
		fmt.Fprintf(cmd.OutOrStdout(), "Phase: %s\nEvidence: %s\n", dashIfEmpty(physical.Phase), physical.EvidenceStatus)
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s\nMutation blocked: %t\n", dashIfEmpty(string(physical.ActivationState)), physical.MutationBlocked)
		fmt.Fprintf(cmd.OutOrStdout(), "Next: %s\n", physical.NextAction)
		if physical.Workspace != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Custody: %s\n", physical.Workspace.Custody)
		}
		return
	}
	renderProjectRegistrationCore(cmd, result.Project)
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime manifest: %s\n", dashIfEmpty(result.RuntimeManifestPath))
	if result.RuntimeManifest != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime archive: %s\n", result.RuntimeManifest.ProjectRuntimeArchiveID)
		fmt.Fprintf(cmd.OutOrStdout(), "Storage archive: %s\n", dashIfEmpty(result.RuntimeManifest.StorageArchiveManifestID))
		fmt.Fprintf(cmd.OutOrStdout(), "Source kind: %s\n", dashIfEmpty(result.RuntimeManifest.SourceKind))
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", result.RuntimeManifest.SourceRef)
		fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.RuntimeManifest.TargetPath)
		if result.RuntimeManifest.FilesystemSnapshotPath != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Filesystem snapshot: %s\n", result.RuntimeManifest.FilesystemSnapshotPath)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Scripts: %d\n", len(result.RuntimeManifest.Scripts))
		fmt.Fprintf(cmd.OutOrStdout(), "Workflows: %d\n", len(result.RuntimeManifest.Workflows))
		fmt.Fprintf(cmd.OutOrStdout(), "Connectors: %d\n", len(result.RuntimeManifest.Connectors))
		fmt.Fprintf(cmd.OutOrStdout(), "Schedules: %d\n", len(result.RuntimeManifest.Schedules))
		fmt.Fprintf(cmd.OutOrStdout(), "Direct events: %d\n", len(result.RuntimeManifest.DirectEvents))
		fmt.Fprintf(cmd.OutOrStdout(), "Watched roots: %d\n", len(result.RuntimeManifest.WatchedRoots))
		fmt.Fprintf(cmd.OutOrStdout(), "Modules: %d\n", len(result.RuntimeManifest.Modules))
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime rule: %s\n", result.RuntimeManifest.RuntimeOwnershipRule)
		fmt.Fprintf(cmd.OutOrStdout(), "Runtime migration: %s\n", result.RuntimeManifest.SuccessorPolicy.Status)
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
}

func renderProjectArchiveRestorePlan(cmd *cobra.Command, result storagearchive.ProjectArchiveRestorePlan) {
	fmt.Fprintln(cmd.OutOrStdout(), "Project archive restore plan")
	renderProjectRegistrationCore(cmd, result.Project)
	renderProjectArchivePlanSteps(cmd, result.Steps)
	renderProjectArchiveWarnings(cmd, result.Warnings)
}

func renderProjectRuntimeMigrationPlan(cmd *cobra.Command, result storagearchive.ProjectRuntimeMigrationPlan) {
	fmt.Fprintln(cmd.OutOrStdout(), "Project runtime migration plan")
	renderProjectRegistrationCore(cmd, result.Project)
	renderProjectArchivePlanSteps(cmd, result.Steps)
	renderProjectArchiveWarnings(cmd, result.Warnings)
}

func renderProjectArchivePlanSteps(cmd *cobra.Command, steps []storagearchive.ProjectArchivePlanStep) {
	if len(steps) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Steps:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tKIND\tKEY\tSUMMARY")
	for _, step := range steps {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", step.Status, step.Kind, step.Key, step.Summary)
	}
	_ = writer.Flush()
}

func renderProjectArchiveWarnings(cmd *cobra.Command, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
	for _, warning := range warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
	}
}

func renderProjectExport(cmd *cobra.Command, result projectexport.Summary) {
	fmt.Fprintf(cmd.OutOrStdout(), "Project export: %s\n", result.OutputPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", result.ProjectSlug)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", result.Mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Policy: %s (%s)\n", result.PolicyVersion, result.PolicyFingerprint)
	fmt.Fprintf(cmd.OutOrStdout(), "Included: %d entries, %d bytes\n", result.Included.Count, result.Included.Bytes)
	fmt.Fprintf(cmd.OutOrStdout(), "Ignored: %d entries, %d bytes\n", result.Ignored.Count, result.Ignored.Bytes)
	fmt.Fprintf(cmd.OutOrStdout(), "Archive: %d bytes, %s\n", result.ArchiveBytes, result.ArchiveChecksum)
}

func renderProjectRegistrationCore(cmd *cobra.Command, detail projects.ProjectRegistrationDetail) {
	project := detail.Project.Project
	fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", project.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "ID: %s\n", project.ProjectID)
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", project.Name)
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", project.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Scope: %s (%s)\n", project.ProjectScopeKey, project.ProjectScopeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Home node: %s\n", ptrOrDash(project.HomeNodeID))
	if detail.Registration == nil {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Registration: %s\n", detail.Registration.RegistrationStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Activation: %s\n", detail.Registration.ActivationStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", detail.Registration.ProjectRoot)
	fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", detail.Registration.ContractPath)
	if layout := projectLayoutFromResolvedPath(detail.Registration.ProjectRoot, detail.Registration.ContractPath); layout != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Layout: %s\n", layout)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Contract hash: %s\n", detail.Registration.ContractHash)
	fmt.Fprintf(cmd.OutOrStdout(), "Revision: %d\n", detail.Registration.RegistrationRevision)
	fmt.Fprintf(cmd.OutOrStdout(), "Registered: %s\n", detail.Registration.LastRegisteredAt.UTC().Format(time.RFC3339))
	if errors, warnings, ok := projectRegistrationValidationSummary(detail.Registration.ValidationReport); ok {
		fmt.Fprintf(cmd.OutOrStdout(), "Validation: %d errors, %d warnings\n", errors, warnings)
	}
	if providers := projectRegistrationProviderAddresses(detail.Registration.DerivedProviders); len(providers) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Derived providers: %s\n", strings.Join(providers, ", "))
	}
	if len(detail.Facets) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Facets: %s\n", strings.Join(projectRegistrationFacetKeys(detail.Facets), ", "))
	}
	if pending := projectRegistrationPendingActionCount(detail.Registration.RegistrationPlan); pending > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending actions: %d\n", pending)
	}
	if pending := projectRegistrationPendingFacetKeys(detail.Facets); len(pending) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending facets: %s\n", strings.Join(pending, ", "))
	}
	if active := activeProjectScriptCapabilityAddresses(detail.ScriptExposures); len(active) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Script capabilities: %s\n", strings.Join(active, ", "))
	}
	if schedules := projectScheduleBackendKeys(detail.ScheduleRegistrations); len(schedules) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Project schedules: %s\n", strings.Join(schedules, ", "))
	}
	if endpoints := projectDirectEventBackendSlugs(detail.DirectEventRegistrations); len(endpoints) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Project direct events: %s\n", strings.Join(endpoints, ", "))
	}
	if providers := projectConnectorProviderAddresses(detail.ConnectorRegistrations); len(providers) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Project connectors: %s\n", strings.Join(providers, ", "))
	}
	if capabilities := activeProjectConnectorCapabilityAddresses(detail.ConnectorRegistrations); len(capabilities) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Connector capabilities: %s\n", strings.Join(capabilities, ", "))
	}
	if modules := projectModuleIDs(detail.ModuleRegistrations); len(modules) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Project modules: %s\n", strings.Join(modules, ", "))
	}
	if detail.Registration != nil {
		if workflows := projectWorkflowsFromPlanJSON(detail.Registration.RegistrationPlan); len(workflows) > 0 {
			discovered, executable, scriptBacked, blocked := projectWorkflowCounts(workflows)
			fmt.Fprintf(cmd.OutOrStdout(), "Project workflows: %d discovered, %d executable, %d script-backed, %d blocked\n", discovered, executable, scriptBacked, blocked)
		}
	}
	if roots := projectWatchedRootBackendKeys(detail.WatchedRootRegistrations); len(roots) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Project watched roots: %s\n", strings.Join(roots, ", "))
	}
}

func renderProjectAnalysisLayout(cmd *cobra.Command, analysis projectcontracts.Analysis, target string, backend bool) {
	if analysis.Loaded == nil {
		return
	}
	loaded := analysis.Loaded
	fmt.Fprintf(cmd.OutOrStdout(), "Layout: %s\n", loaded.Layout)
	fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", loaded.ContractPath)
	if loaded.Layout != projectcontracts.ProjectLayoutLegacy && loaded.Layout != projectcontracts.ProjectLayoutCanonicalWithLegacy {
		return
	}
	commandTarget := strings.TrimSpace(target)
	if commandTarget == "" {
		commandTarget = loaded.RootPath
	}
	backendFlag := ""
	if backend {
		backendFlag = " --backend"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Compatibility: migrate with `loom project migrate-layout %s%s --dry-run`.\n", commandTarget, backendFlag)
}

func projectLayoutFromResolvedPath(root, contractPath string) projectcontracts.ProjectLayout {
	root = strings.TrimSpace(root)
	contractPath = strings.TrimSpace(contractPath)
	if root == "" || contractPath == "" {
		return ""
	}
	relative, err := filepath.Rel(root, contractPath)
	if err != nil {
		return ""
	}
	switch filepath.ToSlash(relative) {
	case projectcontracts.CanonicalRootContractPath:
		return projectcontracts.ProjectLayoutCanonical
	case projectcontracts.LegacyRootContractPath:
		return projectcontracts.ProjectLayoutLegacy
	default:
		return ""
	}
}

func isLocalClientNotFound(err error) bool {
	var requestErr *localclient.RequestError
	return errors.As(err, &requestErr) && requestErr.StatusCode == http.StatusNotFound
}

func renderProjectWorkflowList(cmd *cobra.Command, result projectWorkflowListResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Project workflows: %s\n", dashIfEmpty(result.Project))
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", dashIfEmpty(result.Source))
	if len(result.Workflows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No workflow contracts discovered.")
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "WORKFLOW\tSTATUS\tIMPLEMENTATION\tRUNTIME\tSTEPS\tCAPABILITY\tPACKAGE")
	for _, workflow := range result.Workflows {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			firstNonEmpty(workflow.WorkflowID, workflow.Key),
			firstNonEmpty(workflow.RegistrationStatus, workflow.ContractStatus),
			workflow.ImplementationKind,
			dashIfEmpty(firstNonEmpty(workflow.RegisteredRuntimeKind, workflow.WorkflowCapabilityStatus)),
			workflow.StepCount,
			dashIfEmpty(projectWorkflowCapabilityRef(workflow)),
			dashIfEmpty(firstNonEmpty(workflow.RegisteredVersionID, workflow.PackageHash, workflow.ManifestHash)),
		)
	}
	_ = writer.Flush()
}

func renderProjectWorkflowInspect(cmd *cobra.Command, result projectWorkflowInspectResult, source string) {
	workflow := result.WorkflowFacetItem
	fmt.Fprintf(cmd.OutOrStdout(), "Workflow: %s\n", firstNonEmpty(workflow.WorkflowID, workflow.Key))
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", dashIfEmpty(source))
	fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\n", dashIfEmpty(workflow.Name))
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", dashIfEmpty(firstNonEmpty(workflow.RegistrationStatus, workflow.ContractStatus)))
	fmt.Fprintf(cmd.OutOrStdout(), "Implementation: %s\n", dashIfEmpty(workflow.ImplementationKind))
	fmt.Fprintf(cmd.OutOrStdout(), "Contract: %s\n", dashIfEmpty(workflow.ManifestPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest hash: %s\n", dashIfEmpty(workflow.ManifestHash))
	fmt.Fprintf(cmd.OutOrStdout(), "Package root: %s\n", dashIfEmpty(workflow.PackageRoot))
	fmt.Fprintf(cmd.OutOrStdout(), "Package hash: %s\n", dashIfEmpty(workflow.PackageHash))
	fmt.Fprintf(cmd.OutOrStdout(), "Capability: %s\n", dashIfEmpty(projectWorkflowCapabilityRef(workflow)))
	fmt.Fprintf(cmd.OutOrStdout(), "Provider: %s\n", dashIfEmpty(workflow.ProviderAddress))
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", dashIfEmpty(workflow.Endpoint))
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime: %s\n", dashIfEmpty(workflow.RegisteredRuntimeKind))
	fmt.Fprintf(cmd.OutOrStdout(), "Runtime binding: %s\n", dashIfEmpty(workflow.RuntimeBindingID))
	fmt.Fprintf(cmd.OutOrStdout(), "Workflow package: %s\n", dashIfEmpty(workflow.RegisteredWorkflowID))
	fmt.Fprintf(cmd.OutOrStdout(), "Workflow version: %s\n", dashIfEmpty(workflow.RegisteredVersionID))
	fmt.Fprintf(cmd.OutOrStdout(), "Entrypoint: %s\n", dashIfEmpty(strings.Join(workflow.Entrypoint.Command, " ")))
	fmt.Fprintf(cmd.OutOrStdout(), "Timeout: %d seconds\n", workflow.Execution.TimeoutSeconds)
	fmt.Fprintf(cmd.OutOrStdout(), "Input schema: %s\n", compactMapJSON(workflow.Capability.InputSchema, 180))
	fmt.Fprintf(cmd.OutOrStdout(), "Output schema: %s\n", compactMapJSON(workflow.Capability.OutputSchema, 180))
	if len(workflow.UsageDocuments) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Usage docs:")
		for _, doc := range workflow.UsageDocuments {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", doc.Path)
		}
	}
	renderProjectWorkflowRecentJobs(cmd, result.RecentJobs)
	renderProjectWorkflowRecentCalls(cmd, result.RecentCapabilityCalls)
	if capability := projectWorkflowCapabilityRef(workflow); capability != "" {
		fmt.Fprintln(cmd.OutOrStdout(), "Next:")
		fmt.Fprintf(cmd.OutOrStdout(), "  loom capability inspect %s\n", capability)
		fmt.Fprintf(cmd.OutOrStdout(), "  loom capability call %s --input '{}' --wait\n", capability)
	}
}

func renderProjectWorkflowRecentJobs(cmd *cobra.Command, jobList []jobs.Job) {
	if len(jobList) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Recent workflow jobs:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "JOB\tSTATUS\tTYPE\tCREATED")
	for _, job := range jobList {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			job.JobID,
			job.Status,
			job.JobType,
			job.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func renderProjectWorkflowRecentCalls(cmd *cobra.Command, calls []routing.CapabilityCall) {
	if len(calls) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Recent capability calls:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CALL\tSTATUS\tJOB\tCREATED")
	for _, call := range calls {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			call.CapabilityCallID,
			call.Status,
			ptrOrDash(call.JobID),
			call.CreatedAt.UTC().Format(time.RFC3339),
		)
	}
	_ = writer.Flush()
}

func compactMapJSON(value map[string]any, limit int) string {
	if len(value) == 0 {
		return "-"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "-"
	}
	return compactText(string(raw), limit)
}

func findProjectWorkflow(workflows []projectcontracts.WorkflowFacetItem, key string) (projectcontracts.WorkflowFacetItem, bool) {
	key = strings.TrimSpace(key)
	for _, workflow := range workflows {
		if key == workflow.Key || key == workflow.WorkflowID || key == workflow.CapabilityAddress || key == workflow.ScriptCapabilityAddress {
			return workflow, true
		}
	}
	return projectcontracts.WorkflowFacetItem{}, false
}

func renderProjectScriptExposures(cmd *cobra.Command, exposures []projects.ProjectScriptExposure) {
	if len(exposures) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Script capabilities:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CAPABILITY\tSCRIPT\tSTATUS\tRUNTIME")
	for _, exposure := range exposures {
		capability := exposure.CapabilityAddress
		if capability == "" {
			capability = "scripts/" + exposure.ScriptKey
		}
		runtime := "-"
		if exposure.RuntimeBindingID != nil && strings.TrimSpace(*exposure.RuntimeBindingID) != "" {
			runtime = "script"
		} else if !exposure.ExposureEnabled {
			runtime = "exposure"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", capability, exposure.ScriptKey, exposure.ActivationStatus, runtime)
	}
	_ = writer.Flush()
}

func renderProjectScheduleRegistrations(cmd *cobra.Command, registrations []projects.ProjectScheduleRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project schedules:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SCHEDULE\tSOURCE\tSTATUS\tTARGET")
	for _, registration := range registrations {
		schedule := registration.BackendScheduleKey
		if schedule == "" {
			schedule = "schedules/" + registration.ScheduleKey
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", schedule, registration.ScheduleKey, registration.ActivationStatus, registration.TargetCapability)
	}
	_ = writer.Flush()
}

func renderProjectDirectEventRegistrations(cmd *cobra.Command, registrations []projects.ProjectDirectEventRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project direct events:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ENDPOINT\tSOURCE\tSTATUS\tTARGET")
	for _, registration := range registrations {
		endpoint := registration.BackendEndpointSlug
		if endpoint == "" {
			endpoint = "direct_events/" + registration.EventKey
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", endpoint, registration.EventKey, registration.ActivationStatus, registration.TargetCapability)
	}
	_ = writer.Flush()
}

func renderProjectConnectorRegistrations(cmd *cobra.Command, registrations []projects.ProjectConnectorRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project connectors:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "PROVIDER\tCONNECTOR\tSTATUS\tRUNTIME\tCAPS\tACTIVE")
	for _, registration := range registrations {
		provider := registration.ProviderAddress
		if provider == "" {
			provider = registration.ProviderKey
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%d\n",
			provider,
			registration.ConnectorKey,
			registration.ActivationStatus,
			registration.RuntimeKind,
			registration.CapabilityCount,
			registration.ActiveCapabilityCount,
		)
	}
	_ = writer.Flush()
}

func renderProjectModuleRegistrations(cmd *cobra.Command, registrations []projects.ProjectModuleRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project modules:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "MODULE\tSOURCE\tVERSION\tSTATUS\tPROVIDERS\tCAPS")
	for _, registration := range registrations {
		moduleID := registration.ModuleID
		if moduleID == "" {
			moduleID = registration.ModuleKey
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\t%d\n",
			moduleID,
			registration.ModuleKey,
			registration.ModuleVersion,
			registration.ActivationStatus,
			registration.ProviderCount,
			registration.CapabilityCount,
		)
	}
	_ = writer.Flush()
}

func renderProjectWorkflowRegistrations(cmd *cobra.Command, registrations []projects.ProjectWorkflowRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project workflows:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "WORKFLOW\tCAPABILITY\tSTATUS\tRUNTIME\tPACKAGE")
	for _, registration := range registrations {
		packageRef := firstNonEmpty(ptrOrEmpty(registration.WorkflowVersionID), ptrOrEmpty(registration.WorkflowID), "-")
		if packageRef == "-" && registration.ImplementationKind == projectcontracts.WorkflowImplementationScript {
			packageRef = "script_shim"
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			registration.WorkflowKey,
			dashIfEmpty(registration.CapabilityAddress),
			registration.ActivationStatus,
			dashIfEmpty(registration.RuntimeKind),
			packageRef,
		)
	}
	_ = writer.Flush()
}

func renderProjectWatchedRootRegistrations(cmd *cobra.Command, registrations []projects.ProjectWatchedRootRegistration) {
	if len(registrations) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Project watched roots:")
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ROOT\tNODE\tSTATUS\tSYNC\tINDEX\tBACKUP\tREPORTED\tDRIFT")
	for _, registration := range registrations {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			registration.BackendRootKey,
			registration.OwnerNodeKey,
			registration.ActivationStatus,
			registration.SyncMode,
			registration.IndexMode,
			registration.BackupMode,
			timePtrOrDash(registration.LastReportedAt),
			boolMarker(projectWatchedRootConfigDrift(registration.Metadata)),
		)
	}
	_ = writer.Flush()
}

func renderProjectWatchPlan(cmd *cobra.Command, plan projectwatch.ProjectWatchPlan) {
	project := plan.Report.Project.Slug
	if project == "" && plan.Detail != nil {
		project = plan.Detail.Project.Project.Slug
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project watch plan: %s\n", dashIfEmpty(project))
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", dashIfEmpty(plan.ProjectRoot))
	if plan.ContractHash != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Contract hash: %s\n", plan.ContractHash)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Watched roots: %d\n", len(plan.WatchedRoots))
	fmt.Fprintf(cmd.OutOrStdout(), "Owner nodes: %s\n", dashIfEmpty(strings.Join(projectWatchPlanOwnerNodes(plan.WatchedRoots), ", ")))
	if plan.Report.Summary.Errors > 0 || plan.Report.Summary.Warnings > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Validation: %d errors, %d warnings\n", plan.Report.Summary.Errors, plan.Report.Summary.Warnings)
	}
	if len(plan.WatchedRoots) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Desired watched roots:")
		for _, owner := range projectWatchPlanOwnerNodes(plan.WatchedRoots) {
			fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", owner)
			writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(writer, "ROOT\tSOURCES\tPATH\tSYNC\tINDEX\tBACKUP\tWORKER")
			for _, root := range plan.WatchedRoots {
				if root.OwnerNode != owner {
					continue
				}
				fmt.Fprintf(writer, "%s\t%s\t%s:%s\t%s\t%s\t%s\t%s\n",
					root.BackendRootKey,
					strings.Join(root.SourceKinds, ","),
					root.SafeRootKey,
					root.RootRelativePath,
					root.SyncMode,
					root.IndexMode,
					root.BackupMode,
					root.WorkerKey,
				)
			}
			_ = writer.Flush()
		}
	}
	if len(plan.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Node-agent apply:")
		for _, command := range plan.Commands {
			if command.Description != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  # %s\n", command.Description)
			}
			if shell := projectWatchedRootCommandShell(command); shell != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", shell)
			}
		}
	}
}

func renderProjectWatchApply(cmd *cobra.Command, result projects.ApplyProjectWatchPolicyResult) {
	action := "recorded"
	if result.DryRun {
		action = "dry-run"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Project watch policy: %s\n", action)
	renderProjectRegistrationCore(cmd, result.Detail)
	registrations := result.WatchedRoots
	if len(registrations) == 0 {
		registrations = result.Detail.WatchedRootRegistrations
	}
	renderProjectWatchedRootRegistrations(cmd, registrations)
	if len(result.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Next on owner node-agent:")
		fmt.Fprintln(cmd.OutOrStdout(), "  loom project watch-plan <project-ref> --json > /tmp/loom-project-watch-plan.json")
		fmt.Fprintln(cmd.OutOrStdout(), "  loom-node-agent watched-roots apply-plan /tmp/loom-project-watch-plan.json --project-root <local-project-root>")
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Generated command recipes:")
		renderProjectWatchShell(cmd, result.Commands)
	}
}

func renderProjectWatchShell(cmd *cobra.Command, commands []projects.ProjectWatchedRootCommand) {
	for _, command := range commands {
		if shell := projectWatchedRootCommandShell(command); shell != "" {
			fmt.Fprintln(cmd.OutOrStdout(), shell)
		}
	}
}

func renderProjectSyncStatus(cmd *cobra.Command, status projectwatch.ProjectSyncStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Project sync status: %s\n", status.ProjectRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Sync roots: %d\n", status.SyncRoots)
	fmt.Fprintf(cmd.OutOrStdout(), "Pending agent apply: %d\n", status.PendingAgentApply)
	fmt.Fprintf(cmd.OutOrStdout(), "Reported: %d\n", status.Reported)
	fmt.Fprintf(cmd.OutOrStdout(), "Stale: %d\n", status.Stale)
	fmt.Fprintf(cmd.OutOrStdout(), "Blocked: %d\n", status.Blocked)
	roots := projectSyncWatchedRoots(status.WatchedRoots)
	if len(roots) > 0 {
		renderProjectWatchedRootRegistrations(cmd, roots)
	}
	if len(status.Statuses) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Latest reports:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tNODE\tSTATUS\tCONFIG\tFINDINGS")
		for _, rootStatus := range status.Statuses {
			node := rootStatus.Root.NodeID
			if rootStatus.Root.Node != nil && rootStatus.Root.Node.NodeKey != "" {
				node = rootStatus.Root.Node.NodeKey
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\n",
				rootStatus.Root.RootKey,
				node,
				rootStatus.Root.Status,
				rootStatus.Root.ConfigHash,
				len(rootStatus.LatestFindings),
			)
		}
		_ = writer.Flush()
	}
	if len(status.NodeStatuses) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Owner node sync:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "NODE\tCURSORS\tBATCHES\tCONFLICTS\tREPLICAS")
		for _, nodeStatus := range status.NodeStatuses {
			node := nodeStatus.Node.NodeKey
			if node == "" {
				node = nodeStatus.Node.NodeID
			}
			fmt.Fprintf(writer, "%s\t%d\t%d\t%d\t%d\n",
				node,
				nodeStatus.Summary.CursorCount,
				nodeStatus.Summary.RecentBatchCount,
				nodeStatus.Summary.OpenConflictCount,
				nodeStatus.Summary.ReplicaCount,
			)
		}
		_ = writer.Flush()
	}
	if len(status.Replicas) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Recent replicas:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "REPLICA\tNODE\tMODE\tSTATE\tTARGET")
		for _, replica := range status.Replicas {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				replica.ReplicaID,
				replica.SourceNodeID,
				replica.ReplicaMode,
				replica.FreshnessState,
				replica.ReplicatedID,
			)
		}
		_ = writer.Flush()
	}
	if status.PendingAgentApply > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Next:")
		fmt.Fprintf(cmd.OutOrStdout(), "  loom project watch-plan %s --json > /tmp/loom-%s-watch-plan.json\n", status.ProjectRef, status.ProjectRef)
		fmt.Fprintf(cmd.OutOrStdout(), "  loom-node-agent watched-roots apply-plan /tmp/loom-%s-watch-plan.json --project-root <local-project-root>\n", status.ProjectRef)
	}
}

func renderProjectBackupStatus(cmd *cobra.Command, status projectwatch.ProjectBackupStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Project backup status: %s\n", status.ProjectRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup roots: %d\n", status.BackupRoots)
	fmt.Fprintf(cmd.OutOrStdout(), "Pending agent apply: %d\n", status.PendingAgentApply)
	fmt.Fprintf(cmd.OutOrStdout(), "Reported: %d\n", status.Reported)
	fmt.Fprintf(cmd.OutOrStdout(), "Stale: %d\n", status.Stale)
	fmt.Fprintf(cmd.OutOrStdout(), "Blocked: %d\n", status.Blocked)
	roots := projectBackupWatchedRoots(status.WatchedRoots)
	if len(roots) > 0 {
		renderProjectWatchedRootRegistrations(cmd, roots)
	}
	if len(status.Backups) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Latest backup reports:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tNODE\tSTATUS\tBATCHES\tITEMS\tACCEPTED\tFAILED")
		for _, backup := range status.Backups {
			node := backup.Root.NodeID
			if backup.Root.Node != nil && backup.Root.Node.NodeKey != "" {
				node = backup.Root.Node.NodeKey
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%d\n",
				backup.Root.RootKey,
				node,
				backup.Status,
				backup.BatchCount,
				backup.ItemCount,
				backup.AcceptedCount,
				backup.FailedCount,
			)
		}
		_ = writer.Flush()
	}
	if len(status.BackupBatches) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Recent backup batches:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tSTATUS\tITEMS\tACCEPTED\tFAILED\tBYTES\tRECEIVED")
		for _, batch := range status.BackupBatches {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%d\t%d\t%s\n",
				batch.RootKey,
				batch.Status,
				batch.ItemCount,
				batch.AcceptedCount,
				batch.FailedCount,
				batch.TotalBytes,
				batch.ReceivedAt.UTC().Format(time.RFC3339),
			)
		}
		_ = writer.Flush()
	}
	if len(status.BackupItems) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Recent backup items:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tSTATUS\tMODE\tPATH\tHASH")
		for _, item := range status.BackupItems {
			path := item.RelativePath
			if path == "" {
				path = item.LocalItemRef
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				item.RootKey,
				item.Status,
				item.BackupMode,
				dashIfEmpty(path),
				dashIfEmpty(item.ContentHashURI),
			)
		}
		_ = writer.Flush()
	}
	if status.PendingAgentApply > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Next:")
		fmt.Fprintf(cmd.OutOrStdout(), "  loom project watch-plan %s --json > /tmp/loom-%s-watch-plan.json\n", status.ProjectRef, status.ProjectRef)
		fmt.Fprintf(cmd.OutOrStdout(), "  loom-node-agent watched-roots apply-plan /tmp/loom-%s-watch-plan.json --project-root <local-project-root>\n", status.ProjectRef)
	}
}

func localProjectPathExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func projectWatchPlanOwnerNodes(roots []projectcontracts.ProjectWatchedRootItem) []string {
	seen := map[string]struct{}{}
	for _, root := range roots {
		node := strings.TrimSpace(root.OwnerNode)
		if node != "" {
			seen[node] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for node := range seen {
		out = append(out, node)
	}
	sort.Strings(out)
	return out
}

func projectWatchedRootCommandShell(command projects.ProjectWatchedRootCommand) string {
	if strings.TrimSpace(command.Shell) != "" {
		return strings.TrimSpace(command.Shell)
	}
	if len(command.Command) == 0 {
		return ""
	}
	return strings.Join(command.Command, " ")
}

func projectWatchedRootBackendKeys(registrations []projects.ProjectWatchedRootRegistration) []string {
	keys := make([]string, 0, len(registrations))
	for _, registration := range registrations {
		if strings.TrimSpace(registration.BackendRootKey) != "" {
			keys = append(keys, registration.BackendRootKey)
		}
	}
	sort.Strings(keys)
	return keys
}

func projectSyncWatchedRoots(registrations []projects.ProjectWatchedRootRegistration) []projects.ProjectWatchedRootRegistration {
	out := make([]projects.ProjectWatchedRootRegistration, 0, len(registrations))
	for _, registration := range registrations {
		if registration.SyncMode != "" && registration.SyncMode != agentwatchedroots.SyncModeNone {
			out = append(out, registration)
		}
	}
	return out
}

func projectBackupWatchedRoots(registrations []projects.ProjectWatchedRootRegistration) []projects.ProjectWatchedRootRegistration {
	out := make([]projects.ProjectWatchedRootRegistration, 0, len(registrations))
	for _, registration := range registrations {
		if registration.BackupMode != "" && registration.BackupMode != agentwatchedroots.BackupModeNone {
			out = append(out, registration)
		}
	}
	return out
}

func projectWatchedRootConfigDrift(raw json.RawMessage) bool {
	var metadata map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &metadata) != nil {
		return false
	}
	value, ok := metadata["config_drift"]
	if !ok {
		return false
	}
	boolValue, ok := value.(bool)
	return ok && boolValue
}

func boolMarker(value bool) string {
	if value {
		return "yes"
	}
	return "-"
}

func projectRegistrationFacetKeys(facets []projects.ProjectContractFacet) []string {
	keys := make([]string, 0, len(facets))
	for _, facet := range facets {
		if facet.Enabled {
			keys = append(keys, facet.FacetKey)
		}
	}
	return keys
}

func projectRegistrationPendingFacetKeys(facets []projects.ProjectContractFacet) []string {
	keys := make([]string, 0, len(facets))
	for _, facet := range facets {
		if facet.Enabled && facet.FacetStatus != projects.ProjectFacetStatusActivated {
			keys = append(keys, facet.FacetKey)
		}
	}
	return keys
}

func projectRegistrationActivatedFacetKeys(facets []projects.ProjectContractFacet) []string {
	keys := make([]string, 0, len(facets))
	for _, facet := range facets {
		if facet.FacetStatus == projects.ProjectFacetStatusActivated {
			keys = append(keys, facet.FacetKey)
		}
	}
	sort.Strings(keys)
	return keys
}

func projectRegistrationFacetActivated(facets []projects.ProjectContractFacet, key string) bool {
	for _, facet := range facets {
		if facet.FacetKey == key && facet.FacetStatus == projects.ProjectFacetStatusActivated {
			return true
		}
	}
	return false
}

func activeProjectScriptCapabilityAddresses(exposures []projects.ProjectScriptExposure) []string {
	addresses := []string{}
	for _, exposure := range exposures {
		if exposure.ActivationStatus == projects.ProjectScriptExposureStatusActive && strings.TrimSpace(exposure.CapabilityAddress) != "" {
			addresses = append(addresses, exposure.CapabilityAddress)
		}
	}
	sort.Strings(addresses)
	return addresses
}

func projectScheduleBackendKeys(registrations []projects.ProjectScheduleRegistration) []string {
	keys := []string{}
	for _, registration := range registrations {
		if strings.TrimSpace(registration.BackendScheduleKey) != "" {
			keys = append(keys, registration.BackendScheduleKey)
		}
	}
	sort.Strings(keys)
	return keys
}

func projectDirectEventBackendSlugs(registrations []projects.ProjectDirectEventRegistration) []string {
	keys := []string{}
	for _, registration := range registrations {
		if strings.TrimSpace(registration.BackendEndpointSlug) != "" {
			keys = append(keys, registration.BackendEndpointSlug)
		}
	}
	sort.Strings(keys)
	return keys
}

func projectConnectorProviderAddresses(registrations []projects.ProjectConnectorRegistration) []string {
	addresses := []string{}
	for _, registration := range registrations {
		if strings.TrimSpace(registration.ProviderAddress) != "" {
			addresses = append(addresses, registration.ProviderAddress)
		}
	}
	sort.Strings(addresses)
	return addresses
}

func activeProjectConnectorCapabilityAddresses(registrations []projects.ProjectConnectorRegistration) []string {
	type endpointView struct {
		CapabilityAddress string `json:"capability_address"`
	}
	type metadataView struct {
		Endpoints []endpointView `json:"endpoints"`
	}
	addresses := []string{}
	for _, registration := range registrations {
		if registration.ActivationStatus != projects.ProjectConnectorRegistrationStatusActive {
			continue
		}
		var metadata metadataView
		if len(registration.Metadata) == 0 || json.Unmarshal(registration.Metadata, &metadata) != nil {
			continue
		}
		for _, endpoint := range metadata.Endpoints {
			if strings.TrimSpace(endpoint.CapabilityAddress) != "" {
				addresses = append(addresses, endpoint.CapabilityAddress)
			}
		}
	}
	sort.Strings(addresses)
	return addresses
}

func projectModuleIDs(registrations []projects.ProjectModuleRegistration) []string {
	ids := []string{}
	for _, registration := range registrations {
		if registration.ActivationStatus != projects.ProjectModuleRegistrationStatusRegistered {
			continue
		}
		moduleID := strings.TrimSpace(registration.ModuleID)
		if moduleID != "" {
			ids = append(ids, moduleID)
		}
	}
	sort.Strings(ids)
	return ids
}

func projectWorkflowCounts(workflows []projectcontracts.WorkflowFacetItem) (discovered, executable, scriptBacked, blocked int) {
	for _, workflow := range workflows {
		if strings.TrimSpace(workflow.ManifestPath) == "" {
			continue
		}
		discovered++
		if workflow.Executable || workflow.ImplementationKind == projectcontracts.WorkflowImplementationWorkflow {
			executable++
		}
		if workflow.ImplementationKind == projectcontracts.WorkflowImplementationScript {
			scriptBacked++
		}
		if workflow.ActivationStatus == projectcontracts.WorkflowActivationStatusBlocked {
			blocked++
		}
	}
	return discovered, executable, scriptBacked, blocked
}

func projectWorkflowCapabilityRef(workflow projectcontracts.WorkflowFacetItem) string {
	return firstNonEmpty(workflow.CapabilityAddress, workflow.ScriptCapabilityAddress)
}

func projectRegistrationProviderAddresses(raw json.RawMessage) []string {
	type providerView struct {
		CompactAddress string `json:"compact_address"`
		ProviderKey    string `json:"provider_key"`
	}
	var providers []providerView
	if len(raw) == 0 || json.Unmarshal(raw, &providers) != nil {
		return nil
	}
	addresses := make([]string, 0, len(providers))
	for _, provider := range providers {
		address := strings.TrimSpace(provider.CompactAddress)
		if address == "" {
			address = strings.TrimSpace(provider.ProviderKey)
		}
		if address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses
}

func projectRegistrationPendingActionCount(raw json.RawMessage) int {
	type actionView struct {
		Status string `json:"status"`
	}
	var plan struct {
		Actions []actionView `json:"actions"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &plan) != nil {
		return 0
	}
	count := 0
	for _, action := range plan.Actions {
		status := strings.ToLower(strings.TrimSpace(action.Status))
		if strings.Contains(status, "pending") {
			count++
		}
	}
	return count
}

func projectRegistrationValidationSummary(raw json.RawMessage) (int, int, bool) {
	var report struct {
		Summary struct {
			Errors   int `json:"errors"`
			Warnings int `json:"warnings"`
		} `json:"summary"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &report) != nil {
		return 0, 0, false
	}
	return report.Summary.Errors, report.Summary.Warnings, true
}
