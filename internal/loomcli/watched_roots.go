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
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func newWatchedRootsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watched-roots",
		Short: "Inspect watched-root status reported by node agents",
	}
	cmd.AddCommand(newWatchedRootsStatusCommand(opts))
	cmd.AddCommand(newWatchedRootsFindingsCommand(opts))
	cmd.AddCommand(newWatchedRootsFailuresCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupsCommand(opts))
	return cmd
}

func newWatchedRootsStatusCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.StatusFilter{Limit: 50}
	var includeFidelity bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "List watched-root status reports",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListWatchedRootStatus(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list watched-root status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, status := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), status.Root.RootKey)
				}
				return nil
			}
			for _, status := range envelope.Data {
				node := status.Root.NodeID
				if status.Root.Node != nil && status.Root.Node.NodeKey != "" {
					node = status.Root.Node.NodeKey
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\tfindings=%d\n",
					node,
					status.Root.RootKey,
					status.Root.WorkerKey,
					status.Root.Status,
					len(status.LatestFindings),
				)
				if includeFidelity {
					renderWatchedRootFidelitySummary(cmd, status.LatestFindings)
				}
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	cmd.Flags().StringVar(&filter.RootKey, "root", "", "filter by watched-root key")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by watched-root status")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum reports to return")
	cmd.Flags().BoolVar(&includeFidelity, "include-fidelity", false, "show filesystem fidelity finding counts on each watched root")
	return cmd
}

func renderWatchedRootFidelitySummary(cmd *cobra.Command, findings []mainwatchedroots.Finding) {
	counts := map[string]int{}
	for _, finding := range findings {
		kind := watchedRootFidelityKind(finding.Kind, finding.Summary)
		if kind == "" {
			continue
		}
		counts[kind]++
	}
	if len(counts) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  fidelity: ok")
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  fidelity: permission_denied=%d skipped_too_large=%d symlink_skipped=%d package_boundary=%d path_collision=%d\n",
		counts["permission_denied"],
		counts["skipped_too_large"],
		counts["symlink_skipped"],
		counts["package_boundary"],
		counts["path_collision"],
	)
}

func watchedRootFidelityKind(kind, summary string) string {
	value := strings.ToLower(kind + " " + summary)
	switch {
	case strings.Contains(value, "permission"):
		return "permission_denied"
	case strings.Contains(value, "too_large") || strings.Contains(value, "too large") || strings.Contains(value, "size"):
		return "skipped_too_large"
	case strings.Contains(value, "symlink"):
		return "symlink_skipped"
	case strings.Contains(value, "package"):
		return "package_boundary"
	case strings.Contains(value, "collision"):
		return "path_collision"
	default:
		return ""
	}
}

func newWatchedRootsFindingsCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.FindingFilter{Limit: 50}
	cmd := &cobra.Command{
		Use:   "findings",
		Short: "List watched-root findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListWatchedRootFindings(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list watched-root findings.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, finding := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), finding.FindingKey)
				}
				return nil
			}
			for _, finding := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\n",
					finding.NodeID,
					finding.RootKey,
					finding.Status,
					finding.Severity,
					finding.Kind,
					finding.Summary,
				)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	cmd.Flags().StringVar(&filter.RootKey, "root", "", "filter by watched-root key")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by finding status")
	cmd.Flags().StringVar(&filter.Severity, "severity", "", "filter by finding severity")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum findings to return")
	return cmd
}

func newWatchedRootsFailuresCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.FindingFilter{Status: mainwatchedroots.FindingStatusOpen, Limit: 50}
	cmd := &cobra.Command{
		Use:   "failures",
		Short: "List open watched-root failures requiring attention",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListWatchedRootFindings(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list watched-root failures.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, finding := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), finding.FindingKey)
				}
				return nil
			}
			for _, finding := range envelope.Data {
				path := dashIfEmpty(finding.RelativePath)
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\tpath=%s\t%s\n",
					finding.NodeID,
					finding.RootKey,
					finding.Status,
					finding.Severity,
					finding.Kind,
					path,
					finding.Summary,
				)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	cmd.Flags().StringVar(&filter.RootKey, "root", "", "filter by watched-root key")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.Status, "status", filter.Status, "filter by finding status")
	cmd.Flags().StringVar(&filter.Severity, "severity", "", "filter by finding severity")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum failures to return")
	return cmd
}

func newWatchedRootsBackupsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "Inspect watched-root backup status recorded on main",
	}
	cmd.AddCommand(newWatchedRootsBackupStatusCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupBatchesCommand(opts))
	cmd.AddCommand(newWatchedRootsBackupItemsCommand(opts))
	return cmd
}

func newWatchedRootsBackupStatusCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.BackupFilter{Limit: 50}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show watched-root backup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetWatchedRootBackupStatus(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not get watched-root backup status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Root.RootKey)
				return nil
			}
			latestBatch := ""
			if envelope.Data.LatestBatch != nil {
				latestBatch = envelope.Data.LatestBatch.WatchedRootBackupBatchID
			}
			fmt.Fprintf(cmd.OutOrStdout(), "node=%s root=%s status=%s latest_batch=%s batches=%d items=%d accepted=%d skipped=%d failed=%d findings=%d deletion_markers=%d\n",
				envelope.Data.Root.NodeID,
				envelope.Data.Root.RootKey,
				envelope.Data.Status,
				dashIfEmpty(latestBatch),
				envelope.Data.BatchCount,
				envelope.Data.ItemCount,
				envelope.Data.AcceptedCount,
				envelope.Data.SkippedCount,
				envelope.Data.FailedCount,
				len(envelope.Data.LatestFindings),
				envelope.Data.DeletionMarkerCount,
			)
			for _, finding := range envelope.Data.LatestFindings {
				fmt.Fprintf(cmd.OutOrStdout(), "finding severity=%s kind=%s path=%s summary=%s\n",
					finding.Severity,
					finding.Kind,
					dashIfEmpty(finding.RelativePath),
					finding.Summary,
				)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addWatchedRootBackupFilterFlags(cmd, &filter)
	return cmd
}

func newWatchedRootsBackupBatchesCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.BackupFilter{Limit: 50}
	cmd := &cobra.Command{
		Use:   "batches",
		Short: "List watched-root backup batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListWatchedRootBackupBatches(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list watched-root backup batches.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, batch := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), batch.WatchedRootBackupBatchID)
				}
				return nil
			}
			for _, batch := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\titems=%d accepted=%d failed=%d bytes=%d\n",
					batch.WatchedRootBackupBatchID,
					batch.NodeID,
					batch.RootKey,
					batch.Status,
					batch.ItemCount,
					batch.AcceptedCount,
					batch.FailedCount,
					batch.TotalBytes,
				)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addWatchedRootBackupFilterFlags(cmd, &filter)
	return cmd
}

func newWatchedRootsBackupItemsCommand(opts *options) *cobra.Command {
	filter := mainwatchedroots.BackupItemFilter{Limit: 50}
	cmd := &cobra.Command{
		Use:   "items",
		Short: "List watched-root backup items",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListWatchedRootBackupItems(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list watched-root backup items.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, item := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), item.WatchedRootBackupItemID)
				}
				return nil
			}
			for _, item := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\t%s\tbytes=%d artifact=%s error=%s message=%s\n",
					item.WatchedRootBackupItemID,
					item.NodeID,
					item.RootKey,
					item.Status,
					item.ItemKind,
					item.RelativePath,
					item.SizeBytes,
					dashIfEmpty(item.ArtifactRef),
					dashIfEmpty(item.ErrorCode),
					dashIfEmpty(item.ErrorMessage),
				)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	cmd.Flags().StringVar(&filter.RootKey, "root", "", "filter by watched-root key")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by backup item status")
	cmd.Flags().StringVar(&filter.BatchRef, "batch", "", "filter by watched-root backup batch ID")
	cmd.Flags().StringVar(&filter.Path, "path", "", "filter by watched-root relative path")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum items to return")
	return cmd
}

func addWatchedRootBackupFilterFlags(cmd *cobra.Command, filter *mainwatchedroots.BackupFilter) {
	cmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by node ID or key")
	cmd.Flags().StringVar(&filter.RootKey, "root", "", "filter by watched-root key")
	cmd.Flags().StringVar(&filter.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by backup status")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum records to return")
}
