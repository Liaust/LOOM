package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/storagearchive"
)

func addProjectPhysicalArchiveCommands(parent *cobra.Command, opts *options, restore bool) {
	var reason string
	plan := &cobra.Command{Use: "plan <project-ref>", Short: "Review a physical project move; runtime remains inactive", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		var out response.Envelope[storagearchive.ProjectPhysicalPlanReview]
		if restore {
			out, err = cc.Client.ReviewProjectPhysicalRestore(ctx, cc.CorrelationID, args[0], reason)
		} else {
			out, err = cc.Client.ReviewProjectPhysicalArchive(ctx, cc.CorrelationID, args[0], reason)
		}
		if err != nil {
			return renderProjectPhysicalFailure(cmd, opts, cc.CorrelationID, storagearchive.ProjectPhysicalMutationSummary{}, err)
		}
		if err := storagearchive.ValidateProjectPhysicalPlanReview(out.Data); err != nil {
			return renderError(cmd, opts, cc.CorrelationID, loomerrors.Wrap("project_archive.invalid_review", "project_archive", "plan", "Invalid project review returned.", nil))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out.Data)
		}
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), out.Data.PlanDigest)
			return nil
		}
		w := out.Data.Workspace
		fmt.Fprintf(cmd.OutOrStdout(), "Project %s: %s\nOperation: %s\nProject plan digest: %s\nMove: %s:%s -> %s:%s\nInventory: %d entries, %d bytes\nDeactivations: %d facets, %d actions\nResulting runtime: inactive\n", out.Data.ProjectID, w.OperationKind, w.OperationID, out.Data.PlanDigest, w.Source.Root, w.Source.RelativePath, w.Destination.Root, w.Destination.RelativePath, w.InventoryEntries, w.InventoryBytes, out.Data.DeactivationFacets, out.Data.DeactivationActions)
		return nil
	}}
	plan.Flags().StringVar(&reason, "reason", "", "reason for this reviewed move")
	_ = plan.MarkFlagRequired("reason")
	parent.AddCommand(plan)
	for _, recover := range []bool{false, true} {
		var digest string
		var yes bool
		use, short := "apply <project-ref> <plan-json-file>", "Apply the exact compact project review"
		if recover {
			use, short = "recover <project-ref> <operation-id>", "Recover the exact preserved project operation"
		}
		command := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			corr := correlation.Normalize(opts.correlationID)
			invalid := func() error {
				return renderError(cmd, opts, corr, loomerrors.Wrap("project_archive.review_confirmation_required", "project_archive", "review", "An exact reviewed digest, valid compact plan and --yes are required.", nil))
			}
			if !yes || digest == "" {
				return invalid()
			}
			var review storagearchive.ProjectPhysicalPlanReview
			if !recover {
				var err error
				review, err = readProjectPhysicalReview(args[1])
				kind := storagearchive.WorkspaceOperationArchive
				if restore {
					kind = storagearchive.WorkspaceOperationRestore
				}
				if err != nil || review.PlanDigest != digest || review.Workspace.OperationKind != kind {
					return invalid()
				}
			}
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, corr, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			var out response.Envelope[storagearchive.ProjectPhysicalMutationSummary]
			if recover {
				input := storagearchive.ProjectPhysicalRecoverRequest{OperationID: args[1], PlanDigest: digest, Confirm: true}
				if restore {
					out, err = cc.Client.RecoverProjectPhysicalRestore(ctx, cc.CorrelationID, args[0], input)
				} else {
					out, err = cc.Client.RecoverProjectPhysicalArchive(ctx, cc.CorrelationID, args[0], input)
				}
			} else {
				input := storagearchive.ProjectPhysicalApplyRequest{Plan: review, PlanDigest: digest, Confirm: true}
				if restore {
					out, err = cc.Client.ApplyProjectPhysicalRestore(ctx, cc.CorrelationID, args[0], input)
				} else {
					out, err = cc.Client.ApplyProjectPhysicalArchive(ctx, cc.CorrelationID, args[0], input)
				}
			}
			if err != nil {
				return renderProjectPhysicalFailure(cmd, opts, cc.CorrelationID, out.Data, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
			}
			renderProjectPhysicalSummary(cmd.OutOrStdout(), out.Data)
			return nil
		}}
		command.Flags().StringVar(&digest, "plan-digest", "", "exact project plan digest")
		command.Flags().BoolVar(&yes, "yes", false, "confirm only the exact reviewed operation")
		parent.AddCommand(command)
	}
}

func readProjectPhysicalReview(path string) (storagearchive.ProjectPhysicalPlanReview, error) {
	var review storagearchive.ProjectPhysicalPlanReview
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return review, errors.New("review unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > storagearchive.MaximumWorkspaceSurfaceRequestBytes {
		return review, errors.New("invalid review file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, storagearchive.MaximumWorkspaceSurfaceRequestBytes+1))
	if err != nil || len(raw) > storagearchive.MaximumWorkspaceSurfaceRequestBytes {
		return review, errors.New("invalid bounded review")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&review); err != nil {
		return review, err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return review, errors.New("trailing review data")
	}
	return review, storagearchive.ValidateProjectPhysicalPlanReview(review)
}

func renderProjectPhysicalSummary(w io.Writer, result storagearchive.ProjectPhysicalMutationSummary) {
	fmt.Fprintf(w, "Operation: %s\nPhase: %s\nRuntime: %s\nMutation blocked: %t; recoverable: %t; replay: %t\n", result.OperationID, result.Phase, result.ActivationState, result.MutationBlocked, result.Recoverable, result.Replay)
}

func renderProjectPhysicalFailure(cmd *cobra.Command, opts *options, corr string, result storagearchive.ProjectPhysicalMutationSummary, err error) error {
	if result.OperationID != "" {
		if opts.jsonOutput {
			failure := response.Failure(corr, loomerrors.Wrap("project_archive.outcome_unavailable", "project_archive", "operation", "Inspect the operation before retrying.", nil))
			var requestErr *localclient.RequestError
			if errors.As(err, &requestErr) {
				failure = requestErr.Envelope
			}
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
				response.ErrorEnvelope
				Data storagearchive.ProjectPhysicalMutationSummary `json:"data"`
			}{failure, result})
			return err
		}
		renderProjectPhysicalSummary(cmd.ErrOrStderr(), result)
	}
	return renderError(cmd, opts, corr, loomerrors.Wrap("project_archive.outcome_unavailable", "project_archive", "operation", "Request did not complete; inspect before retrying.", err))
}
