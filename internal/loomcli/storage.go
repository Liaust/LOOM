package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storageexport"
	"loom.local/loom/internal/storagefidelity"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
)

func newStorageCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Browse LOOM main storage catalog and logical view",
	}

	treeFilter := storagecatalog.ListFilter{Limit: 1000}
	treeCmd := &cobra.Command{
		Use:   "tree",
		Short: "List catalog entries using stable human storage paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListStorageEntries(ctx, commandCtx.CorrelationID, treeFilter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read storage catalog paths.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			viewEntries := make([]storageview.ViewEntry, 0, len(envelope.Data))
			for _, entry := range envelope.Data {
				viewEntry, err := storageview.CatalogViewEntry(entry)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.catalog_path_invalid", "storage", entry.StorageEntryID, "Could not represent storage catalog path.", err))
				}
				viewEntries = append(viewEntries, viewEntry)
			}
			if opts.plainOutput {
				for _, entry := range viewEntries {
					fmt.Fprintln(cmd.OutOrStdout(), entry.ViewPath)
				}
				return nil
			}
			renderStorageCatalogPaths(cmd, viewEntries)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addStorageFilterFlags(treeCmd, &treeFilter)
	cmd.AddCommand(treeCmd)

	listFilter := storagecatalog.ListFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List storage catalog entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListStorageEntries(ctx, commandCtx.CorrelationID, listFilter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list storage entries.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, entry := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), entry.StorageEntryID)
				}
				return nil
			}
			renderStorageEntryList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addStorageFilterFlags(listCmd, &listFilter)
	cmd.AddCommand(listCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <view-path-or-storage-entry-id>",
		Short: "Inspect one storage entry by view path or storage entry ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ref := strings.TrimSpace(args[0])
			if strings.HasPrefix(ref, ids.StorageEntryPrefix+"_") {
				envelope, err := commandCtx.Client.InspectStorageEntry(ctx, commandCtx.CorrelationID, ref)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect storage entry.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Entry.StorageEntryID)
					return nil
				}
				renderStorageEntryDetail(cmd, envelope.Data, nil)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}

			envelope, err := commandCtx.Client.ResolveStoragePath(ctx, commandCtx.CorrelationID, ref)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not resolve storage path.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.ViewEntry.StorageEntryID)
				return nil
			}
			if envelope.Data.EntryDetail != nil {
				renderStorageEntryDetail(cmd, *envelope.Data.EntryDetail, &envelope.Data.ViewEntry)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(inspectCmd)
	cmd.AddCommand(newStorageInspectPathCommand(opts))
	cmd.AddCommand(newStorageFidelityCommand(opts))
	cmd.AddCommand(newStorageVerifyPathCommand(opts))
	cmd.AddCommand(newStorageStatusCommand(opts))
	cmd.AddCommand(newStorageVerifyCommand(opts))
	cmd.AddCommand(newStorageFailuresCommand(opts))
	cmd.AddCommand(newStorageTransfersCommand(opts))
	cmd.AddCommand(newStorageFetchCommand(opts))
	cmd.AddCommand(newStorageRestoreCommand(opts))
	cmd.AddCommand(newStorageArchiveCommand(opts))
	cmd.AddCommand(newStorageWorkspaceArchiveCommand(opts))
	cmd.AddCommand(newStorageSafeToDeleteCommand(opts))
	cmd.AddCommand(newStorageSafeDeleteCommand(opts))
	cmd.AddCommand(newStorageRetentionCommand(opts))
	cmd.AddCommand(newStorageMainDocumentsCommand(opts))
	cmd.AddCommand(newStorageInventoryCommand(opts))
	cmd.AddCommand(newStorageCleanupCommand(opts))
	cmd.AddCommand(newStorageExportCommand(opts))
	cmd.AddCommand(newStorageDoctorCommand(opts))
	cmd.AddCommand(newStorageRepairCommand(opts))
	cmd.AddCommand(newStorageMountStatusCommand(opts))
	cmd.AddCommand(newStorageMountPolicyCommand(opts))
	cmd.AddCommand(newStorageCloudOffloadCommand(opts))
	cmd.AddCommand(newStorageCloudFetchCommand(opts))
	cmd.AddCommand(newStorageCloudStatusCommand(opts))
	cmd.AddCommand(newStorageBenchmarkCommand(opts))
	cmd.AddCommand(newStorageFilesystemCommand(opts))

	return cmd
}

func newStorageInspectPathCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect-path <view-path-or-main-documents-path>",
		Short: "Inspect storage protection and fidelity state by path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ref := strings.TrimSpace(args[0])
			if isMainDocumentsPathRef(ref) {
				envelope, err := commandCtx.Client.InspectStoragePath(ctx, commandCtx.CorrelationID, ref)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect storage path.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.AvailabilityState)
					return nil
				}
				renderMainDocumentProtectionStatus(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			envelope, err := commandCtx.Client.ResolveStoragePath(ctx, commandCtx.CorrelationID, ref)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not resolve storage path.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.ViewEntry.AvailabilityState)
				return nil
			}
			if envelope.Data.EntryDetail != nil {
				renderStorageEntryDetail(cmd, *envelope.Data.EntryDetail, &envelope.Data.ViewEntry)
			} else {
				renderStorageFidelityEvaluation(cmd, storagefidelity.EvaluateViewEntry(envelope.Data.ViewEntry, time.Now().UTC()))
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newStorageFidelityCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fidelity",
		Short: "Inspect filesystem fidelity observations and safe-view differences",
	}
	cmd.AddCommand(newStorageFidelityReportCommand(opts))
	cmd.AddCommand(newStorageFidelityBackfillCommand(opts))
	return cmd
}

func newStorageFidelityReportCommand(opts *options) *cobra.Command {
	var filter storagecatalog.ListFilter
	var prefix string
	cmd := &cobra.Command{
		Use:   "report [path-or-prefix]",
		Short: "Report paths where LOOM knows safe views differ from source filesystem metadata",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if filter.Limit <= 0 {
				filter.Limit = 5000
			}
			envelope, err := commandCtx.Client.ListStorageEntries(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read storage catalog for fidelity report.", err))
			}
			effectivePrefix := strings.TrimSpace(prefix)
			if effectivePrefix == "" && len(args) > 0 {
				effectivePrefix = strings.TrimSpace(args[0])
			}
			report := storagefidelity.ReportForCatalogEntries(effectivePrefix, filter.OriginNodeKey, envelope.Data, time.Now().UTC())
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				for _, item := range report.Items {
					fmt.Fprintln(cmd.OutOrStdout(), item.ViewPath)
				}
				return nil
			}
			renderStorageFidelityReport(cmd, report)
			return nil
		},
	}
	addStorageFilterFlags(cmd, &filter)
	cmd.Flags().StringVar(&prefix, "prefix", "", "filter by storage view path prefix")
	return cmd
}

func newStorageFidelityBackfillCommand(opts *options) *cobra.Command {
	var input storagefidelity.BackfillInput
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Backfill filesystem observations and fidelity findings for existing catalog entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.BackfillStorageFidelity(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not backfill storage fidelity metadata.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.FindingsRecorded)
				return nil
			}
			renderStorageFidelityBackfillResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Source, "source", storagefidelity.BackfillSourceAll, "source to backfill: all, watched-roots, main-documents, dropzone, or lane")
	cmd.Flags().BoolVar(&input.DryRun, "dry-run", false, "plan fidelity backfill without writing observations or findings; this is the default")
	cmd.Flags().BoolVar(&input.Apply, "apply", false, "record filesystem observations and findings")
	cmd.Flags().BoolVar(&input.Yes, "yes", false, "confirm --apply")
	cmd.Flags().IntVar(&input.Limit, "limit", 0, "maximum entries to scan; 0 scans all matching entries")
	cmd.Flags().BoolVar(&input.IncludeDeleted, "include-deleted", false, "include deleted catalog rows")
	return cmd
}

func newStorageVerifyPathCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "verify-path <main-documents-view-path>",
		Short: "Verify retained and cloud protection state by path",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.InspectStoragePath(ctx, commandCtx.CorrelationID, strings.TrimSpace(args[0]))
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not verify storage path.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.SafeToDeleteSource)
				return nil
			}
			renderMainDocumentProtectionStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newStorageArchiveCommand(opts *options) *cobra.Command {
	var input storagearchive.ArchiveInput
	cmd := &cobra.Command{
		Use:   "archive <view-path-or-storage-entry-id>",
		Short: "Archive a storage file or subtree into main/Archive",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			input.SourceRef = strings.TrimSpace(args[0])
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.ArchiveStorage(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not archive storage subtree.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.ArchiveManifest.ArchiveManifestID)
				return nil
			}
			renderStorageArchiveResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.TargetPath, "to", "", "archive target path under main/Archive")
	cmd.Flags().StringVar(&input.ArchiveKind, "kind", "", "archive kind: project_archive, document_archive, notes_archive, or manual_archive")
	cmd.Flags().StringVar(&input.ArchiveKey, "archive-key", "", "operator-friendly archive key")
	cmd.Flags().StringVar(&input.OwnerNodeKey, "owner-node", "", "node key that owns the archive, defaults to main")
	cmd.Flags().BoolVar(&input.MarkSourceArchived, "mark-source-archived", false, "mark archived source catalog entries as archived")
	cmd.Flags().BoolVar(&input.MarkSourceSuperseded, "mark-source-superseded", false, "mark archived source catalog entries as superseded")
	cmd.Flags().BoolVar(&input.DryRun, "dry-run", false, "plan the archive without writing files or catalog rows")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newStorageFetchCommand(opts *options) *cobra.Command {
	var input storageretention.FetchInput
	cmd := &cobra.Command{
		Use:   "fetch <view-path-or-storage-entry-id>",
		Short: "Fetch a retained storage file to an explicit local destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			input.Ref = strings.TrimSpace(args[0])
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.FetchStorage(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not fetch storage file.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.DestinationPath)
				return nil
			}
			renderStorageFetchResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.DestinationPath, "to", "", "destination path for the fetched file")
	cmd.Flags().BoolVar(&input.Overwrite, "overwrite", false, "overwrite the destination path if it already exists")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newStorageRestoreCommand(opts *options) *cobra.Command {
	var input storageretention.RestoreInput
	cmd := &cobra.Command{
		Use:   "restore <view-path-or-storage-entry-id>",
		Short: "Restore a retained or tombstoned storage file to an explicit local destination",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			input.Ref = strings.TrimSpace(args[0])
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.RestoreStorage(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not restore storage file.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.FetchResult.DestinationPath)
				return nil
			}
			renderStorageRestoreResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.DestinationPath, "to", "", "destination path for the restored file")
	cmd.Flags().BoolVar(&input.Overwrite, "overwrite", false, "overwrite the destination path if it already exists")
	cmd.Flags().StringVar(&input.Reason, "reason", "", "operator reason for the restore")
	cmd.Flags().StringVar(&input.CreatedBy, "created-by", "", "actor or operator creating the restore")
	_ = cmd.MarkFlagRequired("to")
	cmd.AddCommand(newStorageRestorePlanCommand(opts))
	cmd.AddCommand(newStorageRestoreApplyCommand(opts))
	return cmd
}

func newStorageRestorePlanCommand(opts *options) *cobra.Command {
	var mode string
	var targetPath string
	var out string
	cmd := &cobra.Command{
		Use:   "plan <view-path-or-storage-entry-id>",
		Short: "Plan a safe, faithful, or raw storage restore",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ref := strings.TrimSpace(args[0])
			detail, viewEntry, err := resolveStorageEntryDetailForRef(ctx, commandCtx, ref)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect storage entry for restore planning.", err))
			}
			plan, err := storageexport.PlanRestore(storageexport.RestorePlanInput{
				Ref:         ref,
				Mode:        mode,
				TargetPath:  targetPath,
				ViewEntry:   viewEntry,
				EntryDetail: detail,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.restore_plan_failed", "storage", ref, "Could not create storage restore plan.", err))
			}
			if strings.TrimSpace(out) == "" {
				out = storageexport.DefaultRestorePlanPath(plan.PlanID)
			}
			if err := storageexport.WriteRestorePlan(out, plan); err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.restore_plan_write_failed", "storage", ref, "Could not write storage restore plan.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanID)
				return nil
			}
			renderStorageRestorePlan(cmd, plan, out)
			return nil
		},
	}
	cmd.Flags().StringVar(&mode, "mode", storageexport.RestoreModeSafe, "restore mode: safe, faithful, or raw")
	cmd.Flags().StringVar(&targetPath, "target", "", "explicit local target path")
	cmd.Flags().StringVar(&out, "out", "", "write restore plan JSON to this path; defaults to ~/.loom/storage-restore-plans/<plan-id>.json")
	_ = cmd.MarkFlagRequired("target")
	return cmd
}

func newStorageRestoreApplyCommand(opts *options) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "apply <plan-id-or-plan-file>",
		Short: "Apply an explicit storage restore plan",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			planPath := storageexport.ResolveRestorePlanPath(args[0])
			plan, err := storageexport.ReadRestorePlan(planPath)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.restore_plan_read_failed", "storage", args[0], "Could not read storage restore plan.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := storageexport.ApplyRestorePlan(ctx, plan, yes)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.restore_apply_failed", "storage", plan.PlanID, "Could not apply storage restore plan.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if opts.plainOutput {
				if result.Refused {
					fmt.Fprintln(cmd.OutOrStdout(), "refused")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), result.TargetPath)
				}
				return nil
			}
			renderStorageRestoreApplyResult(cmd, result)
			if result.Refused {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("storage.restore_apply_refused", "storage", plan.PlanID, result.Refusal))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm restore plan application")
	return cmd
}

func newStorageSafeToDeleteCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "safe-to-delete <view-path-or-storage-entry-id-or-source-path>",
		Short: "Check whether a source file is safe to delete after main retention",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageSafeToDeleteCheck(cmd, opts, args[0])
		},
	}
	return cmd
}

func newStorageSafeDeleteCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "safe-delete",
		Short: "Check explicit safe-delete state before removing source-side data",
	}
	checkCmd := &cobra.Command{
		Use:   "check <source-path-or-view-path>",
		Short: "Check whether a source or view path is safe to delete",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageSafeToDeleteCheck(cmd, opts, args[0])
		},
	}
	cmd.AddCommand(checkCmd)
	return cmd
}

func runStorageSafeToDeleteCheck(cmd *cobra.Command, opts *options, ref string) error {
	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	envelope, err := commandCtx.Client.CheckStorageSafeToDelete(ctx, commandCtx.CorrelationID, storageretention.SafeToDeleteInput{Ref: strings.TrimSpace(ref)})
	if err != nil {
		return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not check safe-to-delete state.", err))
	}
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Decision)
		return nil
	}
	renderStorageSafeToDeleteResult(cmd, envelope.Data)
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func newStorageRetentionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Inspect storage retention and tombstone state",
	}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show storage retention status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetStorageRetentionStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read storage retention status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\n", envelope.Data.Entries)
				return nil
			}
			renderStorageRetentionStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(statusCmd)
	return cmd
}

func newStorageMainDocumentsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "main-documents",
		Short: "Inspect writable main/Documents import status",
	}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show main Documents import scanner status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetMainDocumentsStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read main Documents import status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.BackingRoot)
				return nil
			}
			renderMainDocumentsStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(statusCmd)

	var reconcileInput mainstorage.ReconcileInput
	reconcileCmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile main/Documents catalog rows with the backing filesystem",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			reconcileInput.DryRun = reconcileInput.DryRun || !reconcileInput.Yes
			reconcileInput.CompareLegacy = true
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.ReconcileMainDocuments(ctx, commandCtx.CorrelationID, reconcileInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not reconcile main Documents catalog state.", err))
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(envelope); err != nil {
					return err
				}
				return mainDocumentsReconcileExitError(envelope.Data)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.MissingCataloged)
				return mainDocumentsReconcileExitError(envelope.Data)
			}
			renderMainDocumentsReconcileResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return mainDocumentsReconcileExitError(envelope.Data)
		},
	}
	reconcileCmd.Flags().BoolVar(&reconcileInput.DryRun, "dry-run", false, "plan reconciliation without tombstoning catalog rows; this is the default unless --yes is passed")
	reconcileCmd.Flags().BoolVar(&reconcileInput.Yes, "yes", false, "tombstone missing main Documents catalog rows")
	reconcileCmd.Flags().StringVar(&reconcileInput.Reason, "reason", "", "operator reason stored on tombstones")
	reconcileCmd.Flags().StringVar(&reconcileInput.CreatedBy, "created-by", "", "actor recorded on tombstones")
	cmd.AddCommand(reconcileCmd)

	retentionCmd := &cobra.Command{
		Use:   "retention",
		Short: "Manage retained byte payloads for main Documents",
	}
	var backfillInput mainstorage.RetentionBackfillInput
	backfillCmd := &cobra.Command{
		Use:   "backfill",
		Short: "Create missing retained payload copies for cataloged main Documents",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			backfillInput.DryRun = backfillInput.DryRun || !backfillInput.Yes
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.BackfillMainDocumentsRetention(ctx, commandCtx.CorrelationID, backfillInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not backfill main Documents retention payloads.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Created)
				return nil
			}
			renderMainDocumentsRetentionBackfillResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	backfillCmd.Flags().BoolVar(&backfillInput.DryRun, "dry-run", false, "plan retention payload creation without writing bytes; this is the default unless --yes is passed")
	backfillCmd.Flags().BoolVar(&backfillInput.Yes, "yes", false, "create missing retained payloads and catalog retention refs")
	backfillCmd.Flags().IntVar(&backfillInput.Limit, "limit", 0, "maximum active main Documents rows to scan; 0 scans all")
	retentionCmd.AddCommand(backfillCmd)
	cmd.AddCommand(retentionCmd)

	protectionCmd := &cobra.Command{
		Use:   "protection <relative-path>",
		Short: "Show retained-byte and cloud protection state for a main Documents file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetMainDocumentProtection(ctx, commandCtx.CorrelationID, storagecatalog.MainDocumentProtectionInput{RelativePath: strings.TrimSpace(args[0])})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not inspect main Documents protection state.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.AvailabilityState)
				return nil
			}
			renderMainDocumentProtectionStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(protectionCmd)

	safeDeleteCmd := &cobra.Command{
		Use:   "safe-delete <relative-path>",
		Short: "Check if a main Documents source file is safe to delete",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.CheckMainDocumentSafeDelete(ctx, commandCtx.CorrelationID, storagecatalog.MainDocumentProtectionInput{RelativePath: strings.TrimSpace(args[0])})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not check main Documents safe-delete state.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.SafeToDeleteSource)
				return nil
			}
			renderMainDocumentProtectionStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.AddCommand(safeDeleteCmd)

	return cmd
}

func newStorageExportCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Deprecated read-only compatibility diagnostic",
	}
	diagnostic := func(cmd *cobra.Command, _ []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		envelope, err := commandCtx.Client.GetStorageExportStatus(ctx, commandCtx.CorrelationID)
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read storage export compatibility status.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), "retired")
			return nil
		}
		renderStorageExportDiagnostic(cmd, envelope.Data)
		renderResponseMeta(cmd, opts, envelope.Meta)
		return nil
	}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show the retired export compatibility status",
		RunE:  diagnostic,
	}
	cmd.AddCommand(statusCmd)
	for _, name := range []string{"refresh", "rebuild"} {
		subcommand := &cobra.Command{
			Use:        name,
			Short:      "Deprecated; reports retirement without changing files",
			Deprecated: "the generated filesystem export is retired",
			Args:       cobra.MaximumNArgs(1),
			RunE:       diagnostic,
		}
		if name == "rebuild" {
			subcommand.Flags().Bool("dry-run", false, "deprecated compatibility flag; no files are changed")
			subcommand.Flags().Bool("include-all", false, "deprecated compatibility flag; no files are changed")
			subcommand.Flags().Int("max-results", 0, "deprecated compatibility flag; no files are changed")
		}
		cmd.AddCommand(subcommand)
	}
	return cmd
}

func renderStorageExportDiagnostic(cmd *cobra.Command, status storagedoctor.ExportCompatibilityInfo) {
	fmt.Fprintln(cmd.OutOrStdout(), "LOOM generated storage export is retired")
	fmt.Fprintf(cmd.OutOrStdout(), "Active: %t\n", status.Active)
	if status.LegacyPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Legacy path (migration evidence only): %s\n", status.LegacyPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s\n", status.Message)
	for _, replacement := range status.ReplacementCommands {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", replacement)
	}
}

func addStorageFilterFlags(cmd *cobra.Command, filter *storagecatalog.ListFilter) {
	cmd.Flags().IntVar(&filter.Limit, "limit", filter.Limit, "maximum number of entries to return")
	cmd.Flags().StringVar(&filter.StorageClass, "storage-class", "", "filter by storage class")
	cmd.Flags().StringVar(&filter.SourceArea, "source-area", "", "filter by source area")
	cmd.Flags().StringVar(&filter.OriginNodeKey, "node", "", "filter by origin node key")
	cmd.Flags().StringVar(&filter.FileClass, "file-class", "", "filter by file class")
	cmd.Flags().StringVar(&filter.ProcessingState, "processing-state", "", "filter by processing state")
	cmd.Flags().StringVar(&filter.AvailabilityState, "availability-state", "", "filter by availability state")
	cmd.Flags().BoolVar(&filter.IncludeDeleted, "include-deleted", false, "include deleted catalog entries")
}

func renderStorageCatalogPaths(cmd *cobra.Command, entries []storageview.ViewEntry) {
	fmt.Fprintln(cmd.OutOrStdout(), "LOOM storage catalog paths")
	for _, entry := range entries {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", entry.ViewPath, entry.AvailabilityState, entry.StorageEntryID)
	}
}

func renderStorageEntryList(cmd *cobra.Command, entries []storagecatalog.Entry) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "ENTRY\tCLASS\tNODE\tSOURCE\tFILE\tSTATE\tPATH")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\n",
			entry.StorageEntryID,
			entry.StorageClass,
			dash(entry.OriginNodeKey),
			entry.SourceArea,
			entry.FileClass,
			entry.AvailabilityState,
			entry.ProcessingState,
			entry.LogicalPath,
		)
	}
	_ = writer.Flush()
}

func renderStorageEntryDetail(cmd *cobra.Command, detail storagecatalog.EntryDetail, viewEntry *storageview.ViewEntry) {
	entry := detail.Entry
	if viewEntry != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "View path: %s\n", viewEntry.ViewPath)
		fmt.Fprintf(cmd.OutOrStdout(), "Permissions: %s\n", viewEntry.Permissions)
		if viewEntry.ReadOnlyReason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", viewEntry.ReadOnlyReason)
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", entry.StorageEntryID)
	fmt.Fprintf(cmd.OutOrStdout(), "Class: %s\n", entry.StorageClass)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s node=%s\n", entry.SourceArea, dash(entry.OriginNodeKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Logical path: %s\n", entry.LogicalPath)
	if entry.CurrentViewPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Current view path: %s\n", entry.CurrentViewPath)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "File: class=%s mime=%s size=%s\n", entry.FileClass, dash(entry.MimeType), sizePtrOrDash(entry.SizeBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "State: availability=%s processing=%s retention=%s\n", entry.AvailabilityState, entry.ProcessingState, entry.RetentionState)
	if entry.ChecksumHex != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Checksum: %s:%s\n", entry.ChecksumAlgorithm, entry.ChecksumHex)
	}
	if len(detail.PhysicalRefs) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Physical refs:")
		for _, ref := range detail.PhysicalRefs {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s %s\n", ref.StoragePhysicalRefID, ref.RefKind, ref.Status, ref.URI)
		}
	}
	renderStorageFidelityEvaluation(cmd, storagefidelity.EvaluateEntry(entry, detail.PhysicalRefs, time.Now().UTC()))
}

func renderMainDocumentsStatus(cmd *cobra.Command, status mainstorage.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "Main Documents import\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Canonical Box root: %s\n", status.BackingRoot)
	fmt.Fprintf(cmd.OutOrStdout(), "Exists: %t\n", status.Exists)
	fmt.Fprintf(cmd.OutOrStdout(), "Stable window: %ds\n", status.StableWindowSeconds)
	fmt.Fprintf(cmd.OutOrStdout(), "Last scan: %s\n", status.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Files: discovered=%d accepted=%d delayed=%d skipped=%d failed=%d missing_cataloged=%d tombstoned=%d bytes_hashed=%d\n",
		status.FilesDiscovered, status.FilesAccepted, status.FilesDelayed, status.FilesSkipped, status.FilesFailed, status.FilesMissingCataloged, status.FilesTombstoned, status.BytesHashed)
	if mainDocumentsMetricsAny(status.Metrics) {
		fmt.Fprintf(cmd.OutOrStdout(), "Timing: total=%s discover=%s active_catalog=%s hash=%s retention_copy=%s register=%s reconcile=%s\n",
			formatDurationMS(status.Metrics.TotalDurationMS),
			formatDurationMS(status.Metrics.DiscoverDurationMS),
			formatDurationMS(status.Metrics.ActiveCatalogFetchDurationMS),
			formatDurationMS(status.Metrics.HashDurationMS),
			formatDurationMS(status.Metrics.RetentionCopyDurationMS),
			formatDurationMS(status.Metrics.RegisterDurationMS),
			formatDurationMS(status.Metrics.ReconcileDurationMS),
		)
		fmt.Fprintf(cmd.OutOrStdout(), "Operations: hash=%d retention_copy=%d register=%d\n",
			status.Metrics.HashOperations,
			status.Metrics.RetentionCopyOperations,
			status.Metrics.RegisterOperations,
		)
	}
	if status.FilesMissingCataloged > 0 && status.FilesTombstoned == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Missing cataloged files are deferred; verify retained bytes and cloud backup before manual tombstone.")
	}
	if status.LatestAcceptedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Latest accepted: %s\n", status.LatestAcceptedAt.Format(time.RFC3339))
	}
	if len(status.Imports) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Recent imports:")
		limit := len(status.Imports)
		if limit > 20 {
			limit = 20
		}
		for _, item := range status.Imports[:limit] {
			suffix := ""
			if item.StorageEntryID != "" {
				suffix = " " + item.StorageEntryID
			}
			if item.Error != "" {
				suffix += " error=" + item.Error
			}
			if item.DelayReason != "" {
				suffix += " delay=" + item.DelayReason
			}
			if item.IgnoredReason != "" {
				suffix += " ignored=" + item.IgnoredReason
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s%s\n", item.State, item.RelativePath, suffix)
		}
		if len(status.Imports) > limit {
			fmt.Fprintf(cmd.OutOrStdout(), "  ... %d more imports\n", len(status.Imports)-limit)
		}
	}
}

func renderMainDocumentsReconcileResult(cmd *cobra.Command, result mainstorage.ReconcileResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Main Documents reconciliation: %s\n", renderDryRunState(result.DryRun))
	fmt.Fprintf(cmd.OutOrStdout(), "Canonical Box Documents: %s\n", result.BackingRoot)
	if result.Migration.Status != "" && result.Migration.Status != mainstorage.DocumentsMigrationNotRequested {
		fmt.Fprintf(cmd.OutOrStdout(), "Legacy Documents migration: %s\n", result.Migration.Status)
		fmt.Fprintf(cmd.OutOrStdout(), "Legacy root: %s exists=%t\n", result.Migration.LegacyRoot, result.Migration.LegacyExists)
		fmt.Fprintf(cmd.OutOrStdout(), "Canonical root exists: %t\n", result.Migration.CanonicalExists)
		fmt.Fprintf(cmd.OutOrStdout(), "Migration paths: legacy_only=%d canonical_only=%d equivalent=%d conflicts=%d\n",
			result.Migration.LegacyOnly,
			result.Migration.CanonicalOnly,
			result.Migration.Equivalent,
			result.Migration.Conflicts,
		)
		if result.Migration.Status == mainstorage.DocumentsMigrationReady {
			fmt.Fprintln(cmd.OutOrStdout(), "No files were moved; apply the reviewed filesystem migration during the production cutover.")
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Present files: %d\n", result.FilesPresent)
	fmt.Fprintf(cmd.OutOrStdout(), "Active cataloged: %d\n", result.ActiveCataloged)
	fmt.Fprintf(cmd.OutOrStdout(), "Missing cataloged: %d\n", result.MissingCataloged)
	fmt.Fprintf(cmd.OutOrStdout(), "Tombstoned: %d retained=%d failed=%d skipped=%d\n", result.Tombstoned, result.Retained, result.Failed, result.Skipped)
	if result.DryRun && result.MissingCataloged > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No tombstones were written; pass --yes only after verifying retained bytes and cloud backup.")
	}
	if result.CatalogMutationBlocked {
		fmt.Fprintf(cmd.OutOrStdout(), "Catalog mutation blocked: %s\n", result.CatalogMutationBlockReason)
	}
	if len(result.Migration.Items) > 0 {
		limit := len(result.Migration.Items)
		if limit > 20 {
			limit = 20
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "MIGRATION_STATE\tPATH\tDETAIL")
		for _, item := range result.Migration.Items[:limit] {
			fmt.Fprintf(writer, "%s\t%s\t%s\n", item.State, item.RelativePath, item.Reason)
		}
		_ = writer.Flush()
		if result.Migration.ItemsTruncated || len(result.Migration.Items) > limit {
			fmt.Fprintln(cmd.OutOrStdout(), "  ... migration item detail is bounded; use the Slice 3 filesystem manifest for complete path actions")
		}
	}
	if len(result.Items) > 0 {
		limit := len(result.Items)
		if limit > 20 {
			limit = 20
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATE\tPATH\tENTRY\tDETAIL")
		for _, item := range result.Items[:limit] {
			detail := firstNonEmptyString(item.Error, item.DelayReason, item.IgnoredReason, item.ChecksumURI)
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.State, item.RelativePath, item.StorageEntryID, detail)
		}
		_ = writer.Flush()
		if len(result.Items) > limit {
			fmt.Fprintf(cmd.OutOrStdout(), "  ... %d more item(s)\n", len(result.Items)-limit)
		}
	}
}

func mainDocumentsReconcileExitError(result mainstorage.ReconcileResult) error {
	if result.Migration.Status == mainstorage.DocumentsMigrationBlocked {
		return fmt.Errorf("main Documents migration is blocked by %d conflict(s)", result.Migration.Conflicts)
	}
	if result.CatalogMutationBlocked && !result.DryRun {
		return fmt.Errorf("main Documents catalog mutation is blocked: %s", result.CatalogMutationBlockReason)
	}
	return nil
}

func renderMainDocumentsRetentionBackfillResult(cmd *cobra.Command, result mainstorage.RetentionBackfillResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Main Documents retention backfill: %s\n", renderDryRunState(result.DryRun))
	fmt.Fprintf(cmd.OutOrStdout(), "Backing root: %s\n", result.BackingRoot)
	fmt.Fprintf(cmd.OutOrStdout(), "Retention root: %s\n", result.RetentionRoot)
	fmt.Fprintf(cmd.OutOrStdout(), "Scanned: %d already_retained=%d would_create=%d created=%d missing_source=%d failed=%d\n",
		result.Scanned, result.AlreadyRetained, result.WouldCreate, result.Created, result.MissingSource, result.Failed)
	if len(result.Items) > 0 {
		limit := len(result.Items)
		if limit > 20 {
			limit = 20
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATE\tPATH\tENTRY\tRETENTION\tDETAIL")
		for _, item := range result.Items[:limit] {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", item.State, item.RelativePath, item.StorageEntryID, item.RetentionPath, item.Error)
		}
		_ = writer.Flush()
		if len(result.Items) > limit {
			fmt.Fprintf(cmd.OutOrStdout(), "  ... %d more item(s)\n", len(result.Items)-limit)
		}
	}
}

func renderMainDocumentProtectionStatus(cmd *cobra.Command, status storagecatalog.MainDocumentProtectionStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Main Documents protection\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", firstNonEmptyString(status.ViewPath, status.RelativePath))
	fmt.Fprintf(cmd.OutOrStdout(), "Catalog: accepted=%t availability=%s retention=%s\n", status.AcceptedByCatalog, status.AvailabilityState, dash(status.RetentionState))
	if status.StorageEntryID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", status.StorageEntryID)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Current source: present=%t\n", status.CurrentSourcePresent)
	fmt.Fprintf(cmd.OutOrStdout(), "Retained payload: present=%t verified=%t\n", status.RetainedPayloadPresent, status.RetainedPayloadVerified)
	fmt.Fprintf(cmd.OutOrStdout(), "Cloud backup: confirmed=%t", status.CloudBackupConfirmed)
	if status.CloudBackupRef != "" {
		fmt.Fprintf(cmd.OutOrStdout(), " ref=%s", status.CloudBackupRef)
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintf(cmd.OutOrStdout(), "Safe to delete source: %t\n", status.SafeToDeleteSource)
	if status.UnsafeReason != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", status.UnsafeReason)
	}
}

func renderDryRunState(dryRun bool) string {
	if dryRun {
		return "dry-run"
	}
	return "applied"
}

func renderStorageArchiveResult(cmd *cobra.Command, result storagearchive.ArchiveResult) {
	title := "Storage archived"
	if result.DryRun {
		title = "Storage archive planned"
	}
	fmt.Fprintln(cmd.OutOrStdout(), title)
	fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s\n", result.ArchiveManifest.ArchiveManifestID)
	fmt.Fprintf(cmd.OutOrStdout(), "Archive key: %s\n", result.ArchiveManifest.ArchiveKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Kind: %s\n", result.ArchiveManifest.ArchiveKind)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", result.Manifest.SourceRef)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.Manifest.TargetPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Entries: %d\n", len(result.Entries))
	if result.ManifestPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Manifest file: %s\n", result.ManifestPath)
	}
	if len(result.Entries) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Archived entries:")
		limit := len(result.Entries)
		if limit > 20 {
			limit = 20
		}
		for _, entry := range result.Entries[:limit] {
			dedupe := ""
			if entry.Deduped {
				dedupe = " deduped"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s -> %s%s\n", entry.SourceViewPath, entry.ArchiveViewPath, dedupe)
		}
		if len(result.Entries) > limit {
			fmt.Fprintf(cmd.OutOrStdout(), "  ... %d more entries\n", len(result.Entries)-limit)
		}
	}
	if len(result.SafeToDelete) > 0 {
		safe := 0
		for _, check := range result.SafeToDelete {
			if check.Safe {
				safe++
			}
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Safe-to-delete: %d/%d safe\n", safe, len(result.SafeToDelete))
	}
}

func renderStorageFetchResult(cmd *cobra.Command, result storageretention.FetchResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage file fetched\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Ref: %s\n", result.Ref)
	fmt.Fprintf(cmd.OutOrStdout(), "Destination: %s\n", result.DestinationPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Bytes: %d\n", result.BytesWritten)
	fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", result.Entry.StorageEntryID)
	if result.SourceRef != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s %s\n", result.SourceRef.RefKind, result.SourceRef.URI)
	}
}

func renderStorageRestoreResult(cmd *cobra.Command, result storageretention.RestoreResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage file restored\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Destination: %s\n", result.FetchResult.DestinationPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Bytes: %d\n", result.FetchResult.BytesWritten)
	fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", result.FetchResult.Entry.StorageEntryID)
}

func renderStorageRestorePlan(cmd *cobra.Command, plan storageexport.RestorePlan, path string) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage restore plan\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\n", plan.PlanID)
	fmt.Fprintf(cmd.OutOrStdout(), "Mode: %s\n", plan.Mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Ref: %s\n", plan.Ref)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", plan.TargetPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Plan file: %s\n", path)
	fmt.Fprintf(cmd.OutOrStdout(), "Bytes: %d can_apply=%t confirmation=%t\n", plan.BytesToRestore, plan.CanApply, plan.RequiresConfirmation)
	if len(plan.Risks) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Risks:")
		for _, risk := range plan.Risks {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", risk)
		}
	}
	if len(plan.Steps) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "KIND\tSTATUS\tPATH\tSUMMARY")
		for _, step := range plan.Steps {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", step.Kind, step.Status, step.Path, step.Summary)
		}
		_ = writer.Flush()
	}
}

func renderStorageRestoreApplyResult(cmd *cobra.Command, result storageexport.RestoreApplyResult) {
	if result.Refused {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage restore refused\n")
		fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\n", result.PlanID)
		fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", result.Refusal)
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Storage restore applied\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s\n", result.PlanID)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.TargetPath)
	fmt.Fprintf(cmd.OutOrStdout(), "Applied: %s\n", result.AppliedAt.Format(time.RFC3339))
}

func renderStorageFidelityReport(cmd *cobra.Command, report storagefidelity.Report) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage fidelity report\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Prefix: %s\n", dash(report.Prefix))
	if report.Node != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", report.Node)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Items: %d errors=%d warnings=%d partially_safe=%d not_safe=%d\n",
		len(report.Items),
		report.Summary.Errors,
		report.Summary.Warnings,
		report.Summary.PartiallySafe,
		report.Summary.NotSafe,
	)
	if len(report.Items) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No fidelity-specific metadata found for this prefix.")
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "SEVERITY\tDECISION\tPATH\tCLASS\tSTATE\tSUMMARY")
	for _, item := range report.Items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Severity,
			item.Decision,
			item.ViewPath,
			item.FileClass,
			item.ProcessingState,
			fidelitySummaryText(item),
		)
	}
	_ = writer.Flush()
}

func renderStorageFidelityBackfillResult(cmd *cobra.Command, result storagefidelity.BackfillResult) {
	mode := "dry-run"
	if result.Applied {
		mode = "applied"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Storage fidelity backfill: %s\n", mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", result.Source)
	fmt.Fprintf(cmd.OutOrStdout(), "Scanned: %d observed=%d findings_planned=%d findings_recorded=%d already_present=%d\n",
		result.Scanned,
		result.Observed,
		result.FindingsPlanned,
		result.FindingsRecorded,
		result.FindingsAlreadyPresent,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Payload rewrites: %d\n", result.PayloadRewrites)
	fmt.Fprintln(cmd.OutOrStdout(), "Generated export mutation: retired (no refresh requested)")
	if len(result.Notes) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Notes:")
		for _, note := range result.Notes {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", note)
		}
	}
	if len(result.Items) == 0 {
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATE\tAREA\tNODE\tPATH\tKIND\tFINDINGS")
	for _, item := range result.Items {
		state := "planned"
		switch {
		case item.Error != "":
			state = "error"
		case item.Applied:
			state = "applied"
		case item.Skipped:
			state = "skipped"
		case item.Observed:
			state = "observed"
		}
		kinds := make([]string, 0, len(item.Findings))
		for _, finding := range item.Findings {
			kinds = append(kinds, finding.Kind)
		}
		if len(kinds) == 0 && len(item.ExistingFindingKinds) > 0 {
			kinds = append(kinds, item.ExistingFindingKinds...)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			state,
			item.SourceArea,
			dash(item.NodeKey),
			firstNonEmpty(item.ViewPath, item.LogicalPath),
			dash(item.ObjectKind),
			dash(strings.Join(kinds, ", ")),
		)
	}
	_ = writer.Flush()
}

func renderStorageFidelityEvaluation(cmd *cobra.Command, eval storagefidelity.Evaluation) {
	if len(eval.Findings) == 0 {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Fidelity: %s decision=%s payload_retained=%t\n", eval.Severity, eval.Decision, eval.PayloadRetained)
	for _, finding := range eval.Findings {
		blocking := ""
		if finding.Blocking {
			blocking = " blocking"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s%s: %s\n", finding.Severity, finding.Kind, blocking, finding.Summary)
	}
}

func fidelitySummaryText(eval storagefidelity.Evaluation) string {
	if len(eval.Findings) == 0 {
		return "no fidelity findings"
	}
	parts := make([]string, 0, len(eval.Findings))
	for _, finding := range eval.Findings {
		prefix := finding.Kind
		if finding.Blocking {
			prefix += "!"
		}
		parts = append(parts, prefix)
	}
	return strings.Join(parts, ", ")
}

func isMainDocumentsPathRef(value string) bool {
	value = strings.Trim(strings.ReplaceAll(strings.TrimSpace(value), "\\", "/"), "/")
	return strings.EqualFold(value, "main/Documents") ||
		strings.HasPrefix(strings.ToLower(value), "main/documents/")
}

func resolveStorageEntryDetailForRef(ctx context.Context, commandCtx commandContext, ref string) (storagecatalog.EntryDetail, *storageview.ViewEntry, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, ids.StorageEntryPrefix+"_") {
		envelope, err := commandCtx.Client.InspectStorageEntry(ctx, commandCtx.CorrelationID, ref)
		if err != nil {
			return storagecatalog.EntryDetail{}, nil, err
		}
		return envelope.Data, nil, nil
	}
	envelope, err := commandCtx.Client.ResolveStoragePath(ctx, commandCtx.CorrelationID, ref)
	if err != nil {
		return storagecatalog.EntryDetail{}, nil, err
	}
	if envelope.Data.EntryDetail == nil {
		return storagecatalog.EntryDetail{}, &envelope.Data.ViewEntry, fmt.Errorf("storage path resolved without entry detail")
	}
	viewEntry := envelope.Data.ViewEntry
	return *envelope.Data.EntryDetail, &viewEntry, nil
}

func renderStorageSafeToDeleteResult(cmd *cobra.Command, result storageretention.SafeToDeleteResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Safe to delete: %s\n", result.Decision)
	if result.Entry != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Storage entry: %s\n", result.Entry.StorageEntryID)
		fmt.Fprintf(cmd.OutOrStdout(), "State: availability=%s processing=%s retention=%s\n", result.Entry.AvailabilityState, result.Entry.ProcessingState, result.Entry.RetentionState)
	}
	if len(result.Reasons) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Reasons:")
		for _, reason := range result.Reasons {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", reason)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
	if len(result.Blockers) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Blockers:")
		for _, blocker := range result.Blockers {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", blocker)
		}
	}
}

func renderStorageRetentionStatus(cmd *cobra.Command, status storagecatalog.RetentionStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Storage retention\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Entries: %d\n", status.Entries)
	fmt.Fprintf(cmd.OutOrStdout(), "Retained: %d snapshots=%d pending=%d expired=%d\n", status.Retained, status.Snapshots, status.Pending, status.Expired)
	fmt.Fprintf(cmd.OutOrStdout(), "Tombstoned: %d failed=%d\n", status.Tombstoned, status.Failed)
	fmt.Fprintf(cmd.OutOrStdout(), "Safe candidates: %d unsafe candidates=%d\n", status.SafeCandidates, status.UnsafeCandidates)
}

func mainDocumentsMetricsAny(metrics mainstorage.Metrics) bool {
	return metrics.TotalDurationMS > 0 ||
		metrics.DiscoverDurationMS > 0 ||
		metrics.ActiveCatalogFetchDurationMS > 0 ||
		metrics.HashDurationMS > 0 ||
		metrics.RetentionCopyDurationMS > 0 ||
		metrics.RegisterDurationMS > 0 ||
		metrics.ReconcileDurationMS > 0 ||
		metrics.HashOperations > 0 ||
		metrics.RetentionCopyOperations > 0 ||
		metrics.RegisterOperations > 0
}

func formatDurationMS(value int64) string {
	if value <= 0 {
		return "0ms"
	}
	return fmt.Sprintf("%dms", value)
}

func dash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func sizePtrOrDash(value *int64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *value)
}
