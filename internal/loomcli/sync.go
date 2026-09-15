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
	"loom.local/loom/internal/response"
	loomsync "loom.local/loom/internal/sync"
)

func newSyncCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Inspect LOOM node sync state",
	}

	statusOpts := loomsync.ListFilter{}
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Inspect sync cursor and backlog state for a node",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if strings.TrimSpace(statusOpts.NodeRef) == "" {
				return renderError(cmd, opts, correlationID, loomerrors.New("sync.node_required", "sync", "node", "Pass --node to inspect sync status for a node."))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetSyncStatus(ctx, correlationID, statusOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect sync status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderSyncStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	statusCmd.Flags().StringVar(&statusOpts.NodeRef, "node", "", "node ID or key")
	cmd.AddCommand(statusCmd)

	batchOpts := loomsync.ListFilter{Limit: 50}
	batchesCmd := &cobra.Command{
		Use:   "batches",
		Short: "List sync batches",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListSyncBatches(ctx, correlationID, batchOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list sync batches.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, batch := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\titems=%d accepted=%d conflicts=%d failed=%d\n",
					batch.SyncBatchID, batch.OriginNodeID, batch.Status, batch.ItemCount, batch.AcceptedCount, batch.ConflictCount, batch.FailedCount)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	batchesCmd.Flags().StringVar(&batchOpts.NodeRef, "node", "", "node ID or key")
	batchesCmd.Flags().StringVar(&batchOpts.Status, "status", "", "filter by status")
	batchesCmd.Flags().IntVar(&batchOpts.Limit, "limit", 50, "maximum number of batches to return")
	cmd.AddCommand(batchesCmd)

	conflictOpts := loomsync.ListFilter{Limit: 50}
	conflictsCmd := &cobra.Command{
		Use:   "conflicts",
		Short: "List sync conflicts",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListSyncConflicts(ctx, correlationID, conflictOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list sync conflicts.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, conflict := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n",
					conflict.SyncConflictID, conflict.OriginNodeID, conflict.Status, conflict.ConflictType, conflict.LocalRef)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	conflictsCmd.Flags().StringVar(&conflictOpts.NodeRef, "node", "", "node ID or key")
	conflictsCmd.Flags().StringVar(&conflictOpts.Status, "status", "", "filter by status")
	conflictsCmd.Flags().IntVar(&conflictOpts.Limit, "limit", 50, "maximum number of conflicts to return")
	cmd.AddCommand(conflictsCmd)

	replicaOpts := loomsync.ListFilter{Limit: 50}
	replicasCmd := &cobra.Command{
		Use:   "replicas",
		Short: "List sync replicas",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListSyncReplicas(ctx, correlationID, replicaOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list sync replicas.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, replica := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n",
					replica.ReplicaID, replica.SourceNodeID, replica.ReplicaMode, replica.FreshnessState, replica.ReplicatedID)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	replicasCmd.Flags().StringVar(&replicaOpts.NodeRef, "node", "", "node ID or key")
	replicasCmd.Flags().StringVar(&replicaOpts.ProjectRef, "project", "", "filter by project ID, slug, or scope key")
	replicasCmd.Flags().StringVar(&replicaOpts.Status, "status", "", "filter by freshness status")
	replicasCmd.Flags().IntVar(&replicaOpts.Limit, "limit", 50, "maximum number of replicas to return")
	cmd.AddCommand(replicasCmd)

	privateBackupOpts := loomsync.ListFilter{Limit: 50}
	privateBackupsCmd := &cobra.Command{
		Use:   "private-backups",
		Short: "List private raw backup operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.ListPrivateBackups(ctx, correlationID, privateBackupOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list private backup operations.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, operation := range envelope.Data {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\tcoarse_size=%d\t%s\n",
					operation.PrivateBackupOperationID, operation.OriginNodeID, operation.Status, operation.CoarseSizeBytes, operation.StorageRef)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	privateBackupsCmd.Flags().StringVar(&privateBackupOpts.NodeRef, "node", "", "node ID or key")
	privateBackupsCmd.Flags().StringVar(&privateBackupOpts.Status, "status", "", "filter by status")
	privateBackupsCmd.Flags().IntVar(&privateBackupOpts.Limit, "limit", 50, "maximum number of private backup operations to return")
	cmd.AddCommand(privateBackupsCmd)

	deletionRequestOpts := loomsync.ListFilter{Limit: 50, ActiveOnly: true}
	var deletionRequestsAll bool
	deletionRequestsCmd := &cobra.Command{
		Use:   "deletion-requests",
		Short: "List active deletion requests recorded by sync",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			filter := deletionRequestOpts
			if deletionRequestsAll {
				filter.ActiveOnly = false
				filter.IncludeResolved = true
				filter.Status = loomsync.DeletionRequestStatusAll
			}
			envelope, err := client.ListDeletionRequests(ctx, correlationID, filter)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not list deletion requests.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, request := range envelope.Data {
				renderDeletionRequestRow(cmd, request)
			}
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	deletionRequestsCmd.Flags().StringVar(&deletionRequestOpts.NodeRef, "node", "", "node ID or key")
	deletionRequestsCmd.Flags().StringVar(&deletionRequestOpts.Status, "status", "", "filter by status")
	deletionRequestsCmd.Flags().BoolVar(&deletionRequestsAll, "all", false, "include all deletion requests, including resolved history")
	deletionRequestsCmd.Flags().BoolVar(&deletionRequestOpts.IncludeResolved, "include-resolved", false, "include resolved deletion requests when not using a status filter")
	deletionRequestsCmd.Flags().IntVar(&deletionRequestOpts.Limit, "limit", 50, "maximum number of deletion requests to return")
	cmd.AddCommand(deletionRequestsCmd)

	cmd.AddCommand(newSyncDeletionRequestCommand(opts))

	return cmd
}

func newSyncDeletionRequestCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deletion-request",
		Short: "Inspect and resolve sync deletion requests",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "inspect <deletion-request-id>",
		Short: "Inspect a deletion request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			envelope, err := client.GetDeletionRequest(ctx, correlationID, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not inspect deletion request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDeletionRequestDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	addDeletionRequestUpdateCommand(cmd, opts, "review", "Mark a deletion request as pending review")
	addDeletionRequestUpdateCommand(cmd, opts, "approve", "Approve deletion request semantics without deleting files")
	addDeletionRequestUpdateCommand(cmd, opts, "deny", "Deny a deletion request with a reason")
	addDeletionRequestUpdateCommand(cmd, opts, "complete", "Mark a deletion request completed after safe action happened elsewhere")
	return cmd
}

func addDeletionRequestUpdateCommand(parent *cobra.Command, opts *options, action string, short string) {
	flags := struct {
		reason         string
		dryRun         bool
		idempotencyKey string
	}{}
	command := &cobra.Command{
		Use:   action + " <deletion-request-id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if flags.dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "Dry run: would %s deletion request %s\n", action, args[0])
				return nil
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cfg, client, err := commandClient(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", err))
			}
			client, _ = withEffectIdempotency(client, flags.idempotencyKey, "sync.deletion_request."+action+"."+args[0])
			input := loomsync.DeletionRequestUpdateInput{RequestRef: args[0], Reason: flags.reason}
			var envelope response.Envelope[loomsync.DeletionRequest]
			switch action {
			case "review":
				envelope, err = client.ReviewDeletionRequest(ctx, correlationID, input)
			case "approve":
				envelope, err = client.ApproveDeletionRequest(ctx, correlationID, input)
			case "deny":
				envelope, err = client.DenyDeletionRequest(ctx, correlationID, input)
			case "complete":
				envelope, err = client.CompleteDeletionRequest(ctx, correlationID, input)
			default:
				err = fmt.Errorf("unsupported deletion request action %s", action)
			}
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "runtime", cfg.SocketPath, "Could not update deletion request.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderDeletionRequestDetail(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	command.Flags().StringVar(&flags.reason, "reason", "", "operator reason to store with the lifecycle update")
	command.Flags().BoolVar(&flags.dryRun, "dry-run", false, "print the intended lifecycle update without changing the request")
	command.Flags().StringVar(&flags.idempotencyKey, "idempotency-key", "", "retry key for this effectful request")
	parent.AddCommand(command)
}

func renderDeletionRequestRow(cmd *cobra.Command, request loomsync.DeletionRequest) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%s\t%s\n",
		request.DeletionRequestID, request.OriginNodeID, request.Status, request.TargetKind, request.TargetRef)
}

func renderDeletionRequestDetail(cmd *cobra.Command, request loomsync.DeletionRequest) {
	renderDeletionRequestRow(cmd, request)
	fmt.Fprintf(cmd.OutOrStdout(), "Action: %s\n", request.RequestedAction)
	fmt.Fprintf(cmd.OutOrStdout(), "Reason: %s\n", request.Reason)
	fmt.Fprintf(cmd.OutOrStdout(), "Requested: %s\n", request.RequestedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Reviewed: %s\n", timePtrOrDash(request.ReviewedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Reviewed by: %s\n", ptrOrDash(request.ReviewedByActorID))
}

func renderSyncStatus(cmd *cobra.Command, status loomsync.SyncStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s (%s)\n", status.Node.NodeKey, status.Node.NodeID)
	fmt.Fprintf(cmd.OutOrStdout(), "Cursors: %d\n", status.Summary.CursorCount)
	for _, cursor := range status.Cursors {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: sequence=%d local_event=%s\n",
			cursor.StreamName, cursor.LastAcceptedSequence, dashIfEmpty(cursor.LastAcceptedLocalEventID))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Recent batches: %d\n", status.Summary.RecentBatchCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Open conflicts: %d\n", status.Summary.OpenConflictCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Replicas: %d\n", status.Summary.ReplicaCount)
}
