package loomcli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/provenance"
)

func newProvenanceReviewCommand(opts *options) *cobra.Command {
	parent := &cobra.Command{Use: "review", Short: "Read bounded effective provenance review work"}
	parent.AddCommand(newProvenanceReviewPrepareCommand(opts))
	var request provenance.ReviewRequest
	var reviewedFile string
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List effective pending candidates and unresolved cases without ledger bodies",
		Long: "Read effective review work without mutation. Exact domain exclusions are caller-controlled. --reviewed-file is a bounded JSON array of observed {object_kind,object_id,change_token} checkpoints; unchanged items are suppressed before the page limit. Only checkpoint observed review results. Restart without --cursor each sweep so older due deferrals and changed evidence remain discoverable. A failed or truncated read is not an empty ledger; use next_cursor to finish traversal. Re-read exact lifecycle before any mutation.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOpts := *opts
			jsonOpts.jsonOutput = true
			invalid := func(err error) error {
				return renderError(cmd, &jsonOpts, opts.correlationID, loomerrors.Wrap("provenance.invalid_request", "provenance", "review", "Use a bounded limit, returned page cursor, exact domain names and observed review checkpoints.", err))
			}
			if reviewedFile != "" {
				if err := readProvenanceJSON(reviewedFile, provenance.MaximumReviewCheckpointBytes, &request.Reviewed); err != nil {
					return invalid(err)
				}
				if request.Reviewed == nil {
					return invalid(fmt.Errorf("review checkpoint file must contain an array, not null"))
				}
			}
			if request.Limit < 1 {
				return invalid(fmt.Errorf("limit must be positive"))
			}
			if err := provenance.ValidateReviewRequest(request); err != nil {
				return invalid(err)
			}
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, &jsonOpts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
			defer cancel()
			env, err := cc.Client.ListProvenanceReview(ctx, cc.CorrelationID, request)
			if err != nil {
				return renderError(cmd, &jsonOpts, cc.CorrelationID, err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(env)
		}}
	list.Flags().IntVar(&request.Limit, "limit", provenance.DefaultReviewLimit, fmt.Sprintf("page size, maximum %d", provenance.MaximumPageLimit))
	list.Flags().StringVar(&request.Cursor, "cursor", "", "opaque next_cursor for this traversal only; restart each sweep")
	list.Flags().StringVar(&request.Domain, "domain", "", "exact domain")
	list.Flags().StringVar(&request.Visibility, "visibility", "", "exact visibility")
	list.Flags().StringArrayVar(&request.ExcludeDomains, "exclude-domain", nil, "exclude this exact domain (repeatable); no implicit fixture exclusions")
	list.Flags().StringVar(&reviewedFile, "reviewed-file", "", "bounded JSON checkpoint array; unchanged reviewed tokens are excluded")
	parent.AddCommand(list)
	return parent
}
