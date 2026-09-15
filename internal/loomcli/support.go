package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/loomcli/portal"
	"loom.local/loom/internal/supportbundle"
)

type supportBundleCreateFlags struct {
	output               string
	profile              string
	includeLogs          bool
	includeLive          bool
	includeProjects      bool
	projects             []string
	includeAbsolutePaths bool
	maxItems             int
	maxBytes             int64
	timeout              time.Duration
	dryRun               bool
}

func newSupportCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "support",
		Short: "Create bounded LOOM support artifacts",
	}
	cmd.AddCommand(newSupportBundleCommand(opts))
	cmd.AddCommand(newSupportAcceptanceCommand(opts))
	return cmd
}

func newSupportAcceptanceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "acceptance",
		Short: "Manage bounded acceptance-test artifacts",
	}
	cmd.AddCommand(newSupportAcceptanceCleanupCommand(opts))
	return cmd
}

func newSupportAcceptanceCleanupCommand(opts *options) *cobra.Command {
	var root string
	var archiveRoot string
	var yes bool
	var deleteNow bool
	cmd := &cobra.Command{
		Use:   "cleanup --root <path>",
		Short: "Archive or delete children of .loom-acceptance folders",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			result, err := runAcceptanceCleanup(acceptanceCleanupInput{
				Root:        root,
				ArchiveRoot: archiveRoot,
				Yes:         yes,
				DeleteNow:   deleteNow,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("support.acceptance_cleanup_failed", "support", root, "Could not clean acceptance fixtures.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderAcceptanceCleanup(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "root to scan; only children under .loom-acceptance folders are eligible")
	cmd.Flags().StringVar(&archiveRoot, "archive-root", "", "archive root; defaults to a sibling or child .loom-acceptance-archive directory")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply cleanup; without this flag the command is a dry-run")
	cmd.Flags().BoolVar(&deleteNow, "delete-now", false, "delete eligible acceptance fixtures instead of archiving them")
	_ = cmd.MarkFlagRequired("root")
	return cmd
}

func newSupportBundleCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Create redacted support bundles",
	}
	cmd.AddCommand(newSupportBundleCreateCommand(opts))
	return cmd
}

func newSupportBundleCreateCommand(opts *options) *cobra.Command {
	flags := &supportBundleCreateFlags{}
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a redacted LOOM support bundle",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			now := time.Now().UTC()
			outputPath := strings.TrimSpace(flags.output)
			if outputPath == "" {
				outputPath = defaultSupportBundleOutputPath(now)
			}
			if _, err := supportbundle.NormalizeProfile(flags.profile); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("support_bundle.invalid_profile", "support", "profile", err.Error()))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			result, err := supportbundle.Create(cmd.Context(), supportbundle.Options{
				OutputPath:              outputPath,
				Profile:                 supportbundle.Profile(flags.profile),
				IncludeLogs:             flags.includeLogs,
				IncludeLive:             flags.includeLive,
				IncludeProjects:         flags.includeProjects,
				Projects:                flags.projects,
				IncludeAbsolutePaths:    flags.includeAbsolutePaths,
				MaxItems:                flags.maxItems,
				MaxBytes:                flags.maxBytes,
				Timeout:                 flags.timeout,
				DryRun:                  flags.dryRun,
				Now:                     now,
				Runtime:                 runtimeInfoFromCommandContext(commandCtx),
				SafeConfigFields:        commandCtx.Config.SafeFields(),
				Command:                 supportCommandArgs(cmd),
				CorrelationID:           commandCtx.CorrelationID,
				CoreClient:              commandCtx.Client,
				DoctorDataProvider:      doctorDataProvider(commandCtx),
				CloudStatusProvider:     cloudStatusProvider(),
				LiveDiagnosticsProvider: liveDiagnosticsProvider(commandCtx),
				RedactionProfile:        supportbundle.RedactionProfileDefault,
				PathAliases:             supportPathAliases(commandCtx),
				UserFileContentsAllowed: false,
			}, nil)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("support_bundle.create_failed", "support", outputPath, "Could not create support bundle.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if result.DryRun {
				fmt.Fprint(cmd.OutOrStdout(), supportbundle.RenderPlanText(result.Plan, result.OutputPath))
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), supportbundle.RenderResultText(result))
			return nil
		},
	}
	cmd.Flags().StringVar(&flags.output, "output", "", "support bundle archive path")
	cmd.Flags().StringVar(&flags.profile, "profile", string(supportbundle.ProfileDefault), "support bundle profile: minimal, default, or full")
	cmd.Flags().BoolVar(&flags.includeLogs, "include-logs", false, "include bounded log excerpts")
	cmd.Flags().BoolVar(&flags.includeLive, "include-live", false, "allow live diagnostic probes")
	cmd.Flags().BoolVar(&flags.includeProjects, "include-projects", false, "include project-scoped summaries")
	cmd.Flags().StringArrayVar(&flags.projects, "project", nil, "project slug or ref to include; repeatable")
	cmd.Flags().BoolVar(&flags.includeAbsolutePaths, "include-absolute-paths", false, "preserve absolute paths instead of root aliases")
	cmd.Flags().IntVar(&flags.maxItems, "max-items", 0, "maximum items per collector; 0 uses the default")
	cmd.Flags().Int64Var(&flags.maxBytes, "max-bytes", 0, "maximum bytes per collected section; 0 uses the default")
	cmd.Flags().DurationVar(&flags.timeout, "timeout", 0, "per-collector timeout; 0 uses the default")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show the collection plan without writing an archive")
	return cmd
}

func defaultSupportBundleOutputPath(now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return filepath.Join(os.TempDir(), "loom-support-"+now.UTC().Format("20060102T150405Z")+".tar.gz")
}

func renderAcceptanceCleanup(cmd *cobra.Command, result acceptanceCleanupResult) {
	title := "LOOM acceptance cleanup"
	if result.DryRun {
		title += " dry-run"
	}
	fmt.Fprintln(cmd.OutOrStdout(), title+": "+result.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", result.Root)
	if result.ArchiveRoot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Archive root: %s\n", result.ArchiveRoot)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Candidates: %d archived=%d deleted=%d would_archive=%d would_delete=%d skipped=%d\n",
		result.Summary.Candidates,
		result.Summary.Archived,
		result.Summary.Deleted,
		result.Summary.WouldArchive,
		result.Summary.WouldDelete,
		result.Summary.Skipped,
	)
	for _, item := range result.Items {
		detail := item.ArchivePath
		if detail == "" {
			detail = item.Error
		}
		if detail == "" {
			detail = item.Action
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s -> %s\n", item.Status, item.RelativePath, detail)
	}
}

func supportCommandArgs(cmd *cobra.Command) []string {
	if cmd == nil {
		return nil
	}
	return strings.Fields(cmd.CommandPath())
}

func runtimeInfoFromCommandContext(ctx commandContext) supportbundle.RuntimeInfo {
	return supportbundle.RuntimeInfo{
		Environment:  ctx.Config.Env,
		NodeID:       ctx.Config.NodeID,
		NodeKind:     ctx.Config.NodeKind,
		NodeRole:     ctx.Config.NodeRole,
		RuntimeClass: ctx.Config.RuntimeClass,
	}
}

func supportPathAliases(ctx commandContext) []supportbundle.PathAlias {
	aliases := supportbundle.DefaultPathAliases()
	aliases = append(aliases,
		supportbundle.PathAlias{Label: "$LOOM_SERVICE_ROOT", Root: ctx.Filesystem.ServiceRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_BOX", Root: ctx.Filesystem.BoxRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_AGENTS_ROOT", Root: ctx.Filesystem.AgentsRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_STORAGE_ROOT", Root: ctx.Filesystem.StorageRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_IMPORTS_ROOT", Root: ctx.Filesystem.ImportsRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_USER_BACKUPS_ROOT", Root: ctx.Filesystem.UserBackupsRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_ARCHIVE_ROOT", Root: ctx.Filesystem.ArchiveRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_DATA", Root: ctx.Filesystem.DataRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_BOX_STATE_ROOT", Root: ctx.Filesystem.BoxStateRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_GENERATED_ROOT", Root: ctx.Filesystem.GeneratedRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_NOTES_PROJECTION_ROOT", Root: ctx.Filesystem.NotesProjectionRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_STORAGE_RETENTION_ROOT", Root: ctx.Filesystem.StorageRetentionRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_OPERATIONAL_BACKUPS_ROOT", Root: ctx.Filesystem.OperationalBackupsRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_DEPRECATED_STORAGE_EXPORT_ROOT", Root: ctx.Filesystem.DeprecatedStorageExportRoot.String()},
		supportbundle.PathAlias{Label: "$LOOM_DEPRECATED_MAIN_DOCUMENTS_ROOT", Root: ctx.Filesystem.DeprecatedMainDocumentsRoot.String()},
	)
	return aliases
}

func doctorDataProvider(ctx commandContext) supportbundle.DoctorDataProvider {
	return func(runCtx context.Context, _ supportbundle.Options) (portal.DoctorData, error) {
		snapshot, err := portal.CollectSnapshot(runCtx, ctx.Client, ctx.CorrelationID)
		if err != nil {
			return portal.DoctorData{}, err
		}
		return portal.BuildDoctorData(snapshot), nil
	}
}

func cloudStatusProvider() supportbundle.CloudStatusProvider {
	return func(runCtx context.Context, _ supportbundle.Options) (cloudstorage.StatusReport, error) {
		return cloudstorage.Status(runCtx, cloudstorage.StatusInput{
			ConfigPath: cloudstorage.DefaultConfigPath,
			Mode:       cloudstorage.StatusModeCached,
		})
	}
}

func liveDiagnosticsProvider(ctx commandContext) supportbundle.LiveDiagnosticsProvider {
	return func(runCtx context.Context, _ supportbundle.Options) (supportbundle.LiveDiagnosticsSummary, error) {
		cloud, err := ctx.Client.CloudStatusLive(runCtx, ctx.CorrelationID, cloudstorage.CloudStatusLiveInput{
			ConfigPath: cloudstorage.DefaultConfigPath,
		})
		if err != nil {
			return supportbundle.LiveDiagnosticsSummary{}, err
		}
		snapshot, err := ctx.Client.CloudSnapshotStatusLive(runCtx, ctx.CorrelationID, cloudstorage.CloudSnapshotStatusLiveInput{
			ConfigPath: cloudstorage.DefaultConfigPath,
			NodeID:     ctx.Config.NodeID,
		})
		if err != nil {
			return supportbundle.LiveDiagnosticsSummary{}, err
		}
		return supportbundle.LiveDiagnosticsSummary{
			Status:        "collected",
			Cloud:         &cloud.Data,
			CloudSnapshot: &snapshot.Data,
		}, nil
	}
}
