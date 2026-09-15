package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/storagearchive"
)

func newStorageWorkspaceArchiveCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Plan, inspect, archive, restore, and recover mapped workspaces",
	}
	cmd.AddCommand(
		newStorageWorkspaceArchivePlanCommand(opts),
		newStorageWorkspaceArchiveApplyCommand(opts, false),
		newStorageWorkspaceArchiveInspectCommand(opts),
		newStorageWorkspaceArchiveRecoverCommand(opts),
		newStorageWorkspaceRestorePlanCommand(opts),
		newStorageWorkspaceArchiveApplyCommand(opts, true),
	)
	return cmd
}

func newStorageWorkspaceArchivePlanCommand(opts *options) *cobra.Command {
	var operationID, reason string
	cmd := &cobra.Command{
		Use:   "plan <kind> <object-id> <slug>",
		Short: "Create a bounded workspace archive plan",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.PlanWorkspaceArchive(ctx, commandCtx.CorrelationID, storagearchive.WorkspaceArchivePlanRequest{
				OperationID: operationID, Kind: storagearchive.WorkspaceKind(args[0]),
				ObjectID: args[1], Slug: args[2], Reason: reason,
			})
			if err != nil {
				return renderWorkspaceArchiveTransportError(cmd, opts, commandCtx.CorrelationID, "plan", err)
			}
			return renderWorkspaceMovePlanReview(cmd, opts, envelope.Data)
		},
	}
	cmd.Flags().StringVar(&operationID, "operation-id", "", "stable operation id (generated when omitted)")
	cmd.Flags().StringVar(&reason, "reason", "", "bounded reason for the archive")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func newStorageWorkspaceRestorePlanCommand(opts *options) *cobra.Command {
	var operationID, reason string
	cmd := &cobra.Command{
		Use:   "restore-plan <archive-operation-id>",
		Short: "Create a bounded restore plan for a completed archive operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.PlanWorkspaceRestore(ctx, commandCtx.CorrelationID, storagearchive.WorkspaceRestorePlanRequest{
				OperationID: operationID, ArchiveOperationID: args[0], Reason: reason,
			})
			if err != nil {
				return renderWorkspaceArchiveTransportError(cmd, opts, commandCtx.CorrelationID, "restore-plan", err)
			}
			return renderWorkspaceMovePlanReview(cmd, opts, envelope.Data)
		},
	}
	cmd.Flags().StringVar(&operationID, "operation-id", "", "stable restore operation id (generated when omitted)")
	cmd.Flags().StringVar(&reason, "reason", "", "bounded reason for the restore")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func newStorageWorkspaceArchiveApplyCommand(opts *options, restore bool) *cobra.Command {
	name, short := "apply", "Apply an exactly reviewed workspace archive plan"
	if restore {
		name, short = "restore-apply", "Apply an exactly reviewed workspace restore plan"
	}
	var digest string
	var confirm bool
	cmd := &cobra.Command{
		Use:   name + " <plan-json-file>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm || strings.TrimSpace(digest) == "" {
				return fmt.Errorf("--yes and --plan-digest are required for workspace mutation")
			}
			review, err := readWorkspaceMovePlanReview(args[0])
			if err != nil {
				return err
			}
			wantKind := storagearchive.WorkspaceOperationArchive
			if restore {
				wantKind = storagearchive.WorkspaceOperationRestore
			}
			if review.OperationKind != wantKind || review.PlanDigest != digest {
				return fmt.Errorf("plan file does not match the exact %s digest", wantKind)
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			input := storagearchive.WorkspaceMoveApplyRequest{Plan: review, PlanDigest: digest, Confirm: true}
			var output storagearchive.WorkspaceMoveInspectionSummary
			if restore {
				envelope, requestErr := commandCtx.Client.ApplyWorkspaceRestore(ctx, commandCtx.CorrelationID, input)
				err, output = requestErr, envelope.Data
			} else {
				envelope, requestErr := commandCtx.Client.ApplyWorkspaceArchive(ctx, commandCtx.CorrelationID, input)
				err, output = requestErr, envelope.Data
			}
			if err != nil {
				return renderWorkspaceArchiveTransportError(cmd, opts, commandCtx.CorrelationID, name, err)
			}
			return renderWorkspaceMoveInspection(cmd, opts, output)
		},
	}
	cmd.Flags().StringVar(&digest, "plan-digest", "", "exact digest printed by the reviewed plan")
	cmd.Flags().BoolVar(&confirm, "yes", false, "confirm the reviewed workspace mutation")
	return cmd
}

func newStorageWorkspaceArchiveInspectCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <operation-id>",
		Short: "Inspect one authorized workspace archive lifecycle operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.InspectWorkspaceArchive(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderWorkspaceArchiveTransportError(cmd, opts, commandCtx.CorrelationID, "inspect", err)
			}
			return renderWorkspaceMoveInspection(cmd, opts, envelope.Data)
		},
	}
}

func newStorageWorkspaceArchiveRecoverCommand(opts *options) *cobra.Command {
	var digest string
	var confirm bool
	cmd := &cobra.Command{
		Use:   "recover <operation-id>",
		Short: "Resume one durable workspace archive lifecycle operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !confirm || strings.TrimSpace(digest) == "" {
				return fmt.Errorf("--yes and --plan-digest are required for workspace recovery")
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.RecoverWorkspaceArchive(ctx, commandCtx.CorrelationID, storagearchive.WorkspaceMoveRecoverRequest{
				OperationID: args[0], PlanDigest: digest, Confirm: true,
			})
			if err != nil {
				return renderWorkspaceArchiveTransportError(cmd, opts, commandCtx.CorrelationID, "recover", err)
			}
			return renderWorkspaceMoveInspection(cmd, opts, envelope.Data)
		},
	}
	cmd.Flags().StringVar(&digest, "plan-digest", "", "exact digest printed by the reviewed plan")
	cmd.Flags().BoolVar(&confirm, "yes", false, "confirm recovery of the reviewed operation")
	return cmd
}

func readWorkspaceMovePlanReview(filename string) (storagearchive.WorkspaceMovePlanReview, error) {
	file, err := os.Open(filename)
	if err != nil {
		return storagearchive.WorkspaceMovePlanReview{}, fmt.Errorf("open workspace plan: %w", err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, storagearchive.MaximumWorkspaceSurfaceRequestBytes+1))
	if err != nil {
		return storagearchive.WorkspaceMovePlanReview{}, fmt.Errorf("read workspace plan: %w", err)
	}
	if len(payload) > storagearchive.MaximumWorkspaceSurfaceRequestBytes {
		return storagearchive.WorkspaceMovePlanReview{}, fmt.Errorf("workspace plan exceeds %d bytes", storagearchive.MaximumWorkspaceSurfaceRequestBytes)
	}
	var review storagearchive.WorkspaceMovePlanReview
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		return storagearchive.WorkspaceMovePlanReview{}, fmt.Errorf("decode workspace plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil || !errors.Is(err, io.EOF) {
		return storagearchive.WorkspaceMovePlanReview{}, fmt.Errorf("workspace plan must contain one JSON object")
	}
	if err := storagearchive.ValidateWorkspaceMovePlanReview(review); err != nil {
		return storagearchive.WorkspaceMovePlanReview{}, err
	}
	return review, nil
}

func renderWorkspaceMovePlanReview(cmd *cobra.Command, opts *options, review storagearchive.WorkspaceMovePlanReview) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(review)
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), review.PlanDigest)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", strings.ToUpper(string(review.OperationKind)), review.OperationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s %s (%s)\n", review.Kind, review.ObjectID, review.Slug)
	fmt.Fprintf(cmd.OutOrStdout(), "Move: %s:%s -> %s:%s\n", review.Source.Root, review.Source.RelativePath, review.Destination.Root, review.Destination.RelativePath)
	fmt.Fprintf(cmd.OutOrStdout(), "Inventory: %d entries, %d bytes, %s\n", review.InventoryEntries, review.InventoryBytes, review.InventoryDigest)
	fmt.Fprintf(cmd.OutOrStdout(), "Catalog rebinds: %d\nPlan digest: %s\n", review.CatalogRebinds, review.PlanDigest)
	return nil
}

func renderWorkspaceMoveInspection(cmd *cobra.Command, opts *options, inspection storagearchive.WorkspaceMoveInspectionSummary) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(inspection)
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), inspection.OperationID)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s (%s)\n", strings.ToUpper(string(inspection.OperationKind)), inspection.OperationID, inspection.Phase, inspection.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Custody: %s; lifecycle: %s; activation: %s\n", inspection.Custody, inspection.LifecycleState, inspection.ActivationState)
	fmt.Fprintf(cmd.OutOrStdout(), "Move: %s:%s -> %s:%s\n", inspection.Source.Root, inspection.Source.RelativePath, inspection.Destination.Root, inspection.Destination.RelativePath)
	fmt.Fprintf(cmd.OutOrStdout(), "Plan digest: %s\n", inspection.PlanDigest)
	for _, finding := range inspection.Findings {
		fmt.Fprintf(cmd.OutOrStdout(), "Finding %s (%s): %s\n", finding.Code, finding.Severity, finding.Summary)
	}
	return nil
}

func renderWorkspaceArchiveTransportError(cmd *cobra.Command, opts *options, correlationID, operation string, err error) error {
	return renderError(cmd, opts, correlationID, loomerrors.Wrap("transport.unavailable", "workspace_archive", operation, "Workspace archive request failed.", err))
}
