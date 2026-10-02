package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/provenance"
)

// Reviewed operations use the same authorized foundation client as the API;
// exposing them here does not introduce a second reconciliation policy.
func newProvenanceOperationsCommand(opts *options) *cobra.Command {
	parent := &cobra.Command{Use: "operations", Short: "Apply explicitly reviewed provenance lifecycle operations"}
	var file, key string
	var yes bool
	apply := &cobra.Command{Use: "apply", Args: cobra.NoArgs, Short: "Submit a saved lifecycle batch through the authorized backend", RunE: func(cmd *cobra.Command, _ []string) error {
		invalid := func(err error) error {
			return renderError(cmd, opts, opts.correlationID, loomerrors.Wrap("provenance.invalid_request", "provenance", "operations", "Supply --file, --idempotency-key and --yes for an explicitly reviewed batch.", err))
		}
		if !yes || strings.TrimSpace(key) == "" || strings.TrimSpace(file) == "" {
			return invalid(nil)
		}
		var input provenance.ManualOperationsRequest
		if err := readBoundedJSON(file, provenance.MaximumFoundationRequestBytes, &input); err != nil {
			return invalid(err)
		}
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, opts.correlationID, err)
		}
		client, _ := withEffectIdempotency(cc.Client, key, "provenance.operations")
		ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
		defer cancel()
		env, err := client.ApplyProvenanceOperations(ctx, cc.CorrelationID, input)
		if err != nil {
			return renderError(cmd, opts, cc.CorrelationID, err)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(env)
	}}
	apply.Flags().StringVar(&file, "file", "", "JSON ManualOperationsRequest with preserved source attribution and operation UUIDs")
	apply.Flags().StringVar(&key, "idempotency-key", "", "stable key retained with this exact batch for retries")
	apply.Flags().BoolVar(&yes, "yes", false, "confirm the reviewed lifecycle mutation")
	parent.AddCommand(apply)
	return parent
}

func newProvenanceCandidateListCommand(opts *options) *cobra.Command {
	var request provenance.PageRequest
	var afterTime, afterID string
	cmd := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "Page candidates in registration order; inspect effective lifecycle state before reviewing", RunE: func(cmd *cobra.Command, _ []string) error {
		invalid := func(err error) error {
			return renderError(cmd, opts, opts.correlationID, loomerrors.Wrap("provenance.invalid_request", "provenance", "candidate_list", "Use a bounded limit and both cursor fields returned by the preceding page.", err))
		}
		if request.Limit < 1 || request.Limit > provenance.MaximumPageLimit || (afterTime == "") != (afterID == "") {
			return invalid(nil)
		}
		if afterTime != "" {
			at, err := time.Parse(time.RFC3339Nano, afterTime)
			if err != nil {
				return invalid(err)
			}
			id, err := provenance.ParseSemanticID(afterID)
			if err != nil {
				return invalid(err)
			}
			request.AfterTime, request.AfterID = &at, &id
		}
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
		defer cancel()
		env, err := cc.Client.ListProvenanceCandidates(ctx, cc.CorrelationID, request)
		if err != nil {
			return renderError(cmd, opts, cc.CorrelationID, err)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(env)
	}}
	cmd.Flags().IntVar(&request.Limit, "limit", 10, fmt.Sprintf("page size, maximum %d", provenance.MaximumPageLimit))
	cmd.Flags().StringVar(&request.Domain, "domain", "", "exact domain")
	cmd.Flags().StringVar(&request.Visibility, "visibility", "", "exact visibility")
	cmd.Flags().StringVar(&afterTime, "after-time", "", "next_time returned by the preceding page")
	cmd.Flags().StringVar(&afterID, "after-id", "", "next_id returned by the preceding page")
	return cmd
}
