package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/response"
)

const provenanceRegistrationExample = `{"candidates":[{"claim":"Use Caddy for LOOM HTTP ingress.","record_kind":"decision","record_context":"LOOM architecture, AGENTS.md","domain":"loom-development","visibility":"private","temporal_interpretation":{"interpretation":"Current architecture"},"assertion_posture":"source_claim","producer":{"producer_id":"project-codex","producer_kind":"working_agent","task_id":"actual-task-id","run_id":"11111111-1111-4111-8111-111111111111"},"sources":[{"kind":"file","status":"resolved","verification_posture":"unknown","resolver_name":"working-agent","resolver_version":"1.0","canonical_locator":"AGENTS.md","version_address":"actual-git-commit","content_digest":"actual-source-hash","submitted":{"path":"AGENTS.md","excerpt":"Caddy is the selected reverse proxy/TLS ingress"}}]}]}`

func newProvenanceCandidateRegisterCommand(opts *options) *cobra.Command {
	var file, key string
	var yes bool
	cmd := &cobra.Command{Use: "register", Args: cobra.NoArgs, Short: "Register source-backed pending candidates, without acceptance",
		Long:    "Submit a saved CandidateRegistrationRequest through the authorized backend. Preserve the actual producer/task/run, source version/hash and resolver posture. Registration is pending, not acceptance. On uncertain delivery retry the exact file with the same key. Replace example attribution and source observations with actual evidence.",
		Example: "  loom provenance candidate register --file candidate.json --idempotency-key stable-task-key --yes --json\n\n  candidate.json (CandidateRegistrationRequest):\n  " + provenanceRegistrationExample,
		RunE: func(cmd *cobra.Command, _ []string) error {
			jsonOpts := *opts
			jsonOpts.jsonOutput = true
			invalid := func(err error) error {
				return renderError(cmd, &jsonOpts, opts.correlationID, loomerrors.Wrap("provenance.invalid_request", "provenance", "candidate_register", "Supply a valid CandidateRegistrationRequest with --file, a stable --idempotency-key and --yes.", err))
			}
			if !yes || strings.TrimSpace(key) == "" || strings.TrimSpace(file) == "" {
				return invalid(nil)
			}
			var input provenance.CandidateRegistrationRequest
			if err := readProvenanceJSON(file, provenance.MaximumFoundationRequestBytes, &input); err != nil {
				return invalid(err)
			}
			if err := provenance.ValidateCandidateRegistrationRequest(key, input); err != nil {
				return invalid(err)
			}
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, &jsonOpts, opts.correlationID, err)
			}
			client, _ := withEffectIdempotency(cc.Client, key, "provenance.candidates")
			ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
			defer cancel()
			env, err := client.RegisterProvenanceCandidates(ctx, cc.CorrelationID, input)
			if err == nil && (!env.OK || env.Data.Receipt.SchemaVersion != provenance.SchemaVersion || len(env.Data.Receipt.Receipts) != len(input.Candidates)) {
				err = errors.New("candidate registration response is missing its complete receipt")
			}
			if err != nil {
				var rejection *localclient.RequestError
				if errors.As(err, &rejection) && rejection.StatusCode < 500 {
					return renderError(cmd, &jsonOpts, cc.CorrelationID, err)
				}
				uncertain := loomerrors.New("provenance.delivery_uncertain", "provenance", "candidate_register", "Registration delivery is uncertain; retry the exact file with the same idempotency key.")
				return renderOwnedCLIError(cmd, uncertain, func(view *cobra.Command) error {
					return json.NewEncoder(view.OutOrStdout()).Encode(response.FailureWithIdempotency(cc.CorrelationID, strings.TrimSpace(key), uncertain))
				})
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(env)
		}}
	cmd.Flags().StringVar(&file, "file", "", "JSON CandidateRegistrationRequest; preserve actual producer and source evidence")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "stable key retained with the exact request for same-key retry")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm pending candidate registration, not acceptance")
	return cmd
}

func readProvenanceJSON(file string, maxBytes int64, target any) error {
	var payload json.RawMessage
	if err := readBoundedJSON(file, maxBytes, &payload); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

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
