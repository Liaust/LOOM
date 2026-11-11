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
	"loom.local/loom/internal/response"
)

func newProvenanceCommand(opts *options) *cobra.Command {
	command := &cobra.Command{
		Use:   "provenance",
		Short: "Search qualified provenance and load exact lifecycle state",
	}
	command.AddCommand(newProvenanceSearchCommand(opts))
	command.AddCommand(newProvenanceRepoCommand(opts))
	command.AddCommand(newProvenanceProjectCommand(opts))
	command.AddCommand(newProvenanceRecordCommand(opts))
	command.AddCommand(newProvenanceCandidateCommand(opts))
	command.AddCommand(newProvenanceCaseCommand(opts))
	command.AddCommand(newProvenanceRelationshipCommand(opts))
	command.AddCommand(newProvenanceOperationsCommand(opts))
	command.AddCommand(newProvenanceReviewCommand(opts))
	return command
}

func newProvenanceProjectCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "project", Short: "Read captured project-wide context"}
	var snapshot string
	get := &cobra.Command{Use: "get <project-id>", Short: "Read current or exact historical project context", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
		defer cancel()
		env, err := cc.Client.GetProvenanceProject(ctx, cc.CorrelationID, args[0], provenance.SemanticID(snapshot))
		if err != nil {
			return renderError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(env)
		}
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), env.Data.ProjectID)
			return nil
		}
		p := env.Data
		fmt.Fprintf(cmd.OutOrStdout(), "Project: %s (%s) [%s]\nSnapshot: %s; observation revision: %d; source revision: %d; observed: %s\n", declarationDisplay(p.Context.Name), p.ProjectID, p.Context.Lifecycle, p.SnapshotID, p.ProjectionRevision, p.SourceRevision, p.ObservedAt.UTC().Format(time.RFC3339))
		if d := p.Context.Development; d != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Context: %s; complete: %t; omitted files: %d\n", d.Posture, d.Complete, d.OmittedFiles)
			if d.Archive != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Historical archive: %s; archived: %s; manifest: %s. Captured context does not establish current runtime health.\n", declarationDisplay(d.Archive.OperationID), d.Archive.ArchivedAt.UTC().Format(time.RFC3339), declarationDisplay(d.Archive.ManifestDigest))
			}
			if len(d.MissingDocuments) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Missing retained documents: "+declarationDisplay(strings.Join(d.MissingDocuments, ", ")))
			}
			for _, f := range []struct{ name, value string }{{"Purpose", d.Purpose}, {"Focus", d.CurrentFocus}, {"Progress", d.Progress}, {"Blockers", d.Blockers}, {"Next", d.NextAction}, {"Structure", d.Structure}} {
				if f.value != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", f.name, declarationDisplay(strings.Join(strings.Fields(f.value), " ")))
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Source-declared context, not accepted semantic decisions. Use --json for captured documents and hashes.")
		}
		return nil
	}}
	get.Flags().StringVar(&snapshot, "snapshot", "", "exact captured snapshot UUID from a search citation")
	command.AddCommand(get, newProvenanceRepoSyncCommand(opts))
	return command
}

func newProvenanceRepoCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "repo", Short: "Find and load deterministic repository awareness"}
	command.AddCommand(newProvenanceRepoListCommand(opts))
	command.AddCommand(newProvenanceRepoGetCommand(opts))
	command.AddCommand(newProvenanceRepoSyncCommand(opts))
	return command
}

func newProvenanceRepoListCommand(opts *options) *cobra.Command {
	var request provenance.RepositoryProjectionListRequest
	command := &cobra.Command{
		Use:   "list",
		Short: "List compact semantic repository awareness cards",
		RunE: func(cmd *cobra.Command, _ []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			envelope, err := commandCtx.Client.ListProvenanceRepositories(ctx, commandCtx.CorrelationID, request)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			for _, card := range envelope.Data.Items {
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), card.RepositoryID)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %s [%s; %s; %s]\n", card.RepositoryID, card.Name, card.OwningProject.Name, card.Role, card.TrackingStatus)
				if len(card.ActiveFocus) > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  Focus: %s\n", strings.Join(card.ActiveFocus, "; "))
				} else if card.Purpose != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  Purpose: %s\n", card.Purpose)
				}
			}
			if !opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "Returned: %d; truncated: %t; ordering: %s\n", envelope.Data.Returned, envelope.Data.Truncated, envelope.Data.Ordering)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	command.Flags().StringVar(&request.Query, "query", "", "lexical repository query")
	command.Flags().StringVar(&request.Project, "project", "", "owning project id or slug")
	command.Flags().StringVar(&request.Topic, "topic", "", "exact repository topic")
	command.Flags().StringVar(&request.Role, "role", "", "repository role (primary or component)")
	command.Flags().Var((*repositoryTrackingStatusFlag)(&request.TrackingStatus), "tracking-status", "tracking status filter")
	command.Flags().IntVar(&request.Limit, "limit", 0, fmt.Sprintf("repository limit (default and maximum %d)", provenance.MaximumRepositoryProjectionLimit))
	return command
}

type repositoryTrackingStatusFlag provenance.RepositoryTrackingStatus

func (value *repositoryTrackingStatusFlag) String() string { return string(*value) }
func (value *repositoryTrackingStatusFlag) Set(raw string) error {
	*value = repositoryTrackingStatusFlag(strings.TrimSpace(raw))
	return nil
}
func (*repositoryTrackingStatusFlag) Type() string { return "tracking-status" }

func newProvenanceRepoGetCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "get <repository-id>",
		Short: "Load one exact deterministic repository awareness card",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			envelope, err := commandCtx.Client.GetProvenanceRepository(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			card := envelope.Data
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), card.RepositoryID)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Repository %s: %s\n", card.RepositoryID, card.Name)
			fmt.Fprintf(cmd.OutOrStdout(), "Project: %s (%s); role: %s; tracking: %s\n", card.OwningProject.Name, card.OwningProject.ProjectID, card.Role, card.TrackingStatus)
			if card.OwningProject.Lifecycle == "archived" {
				fmt.Fprintln(cmd.OutOrStdout(), "Historical repository snapshot from an archived project; captured state does not establish current runtime health.")
				if card.Freshness.SourceDigest != nil {
					fmt.Fprintln(cmd.OutOrStdout(), "Source digest: "+declarationDisplay(*card.Freshness.SourceDigest))
				}
				if card.Freshness.ObservedCommit != nil {
					fmt.Fprintln(cmd.OutOrStdout(), "Observed commit: "+declarationDisplay(*card.Freshness.ObservedCommit))
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Purpose: %s\n", card.Purpose)
			fmt.Fprintf(cmd.OutOrStdout(), "Focus: %s\n", strings.Join(card.ActiveFocus, "; "))
			fmt.Fprintf(cmd.OutOrStdout(), "Accepted context: %d; diagnostics: %s\n", len(card.AcceptedContext), strings.Join(card.Diagnostics, ", "))
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newProvenanceRepoSyncCommand(opts *options) *cobra.Command {
	var idempotencyKey string
	command := &cobra.Command{
		Use:   "sync <project-ref>",
		Short: "Manually sync one bounded project repository projection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "provenance.project_projection.sync."+args[0])
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			envelope, err := client.SyncProvenanceProject(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			receipt := envelope.Data.Receipt
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), receipt.ProjectSnapshotID)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Synced project %s at source version %d\n", receipt.ProjectID, receipt.ProjectSourceVersion)
			fmt.Fprintf(cmd.OutOrStdout(), "Repositories: %d; replayed: %t\n", len(receipt.Repositories), receipt.Replayed)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	command.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit retry key for this one-project sync")
	return command
}

func newProvenanceSearchCommand(opts *options) *cobra.Command {
	var project, repository, cursor string
	var collections []string
	var includePending, details bool
	var limit int
	command := &cobra.Command{
		Use:   "search <query>",
		Short: "Search project/repository context, accepted records, and unresolved cases",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			selected := make([]provenance.SearchCollection, 0, len(collections))
			for _, collection := range collections {
				selected = append(selected, provenance.SearchCollection(strings.TrimSpace(collection)))
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			envelope, err := commandCtx.Client.SearchProvenance(ctx, commandCtx.CorrelationID, provenance.SearchRequest{
				Query: strings.Join(args, " "), Project: project, Repository: repository,
				Collections: selected, IncludePending: includePending, Limit: limit, Cursor: cursor,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "provenance", "search", "Could not search provenance.", err))
			}
			view := newProvenanceSearchView(envelope.Data)
			if opts.jsonOutput || details && !opts.plainOutput {
				return renderProvenanceView(cmd, opts, envelope, view, details)
			}
			if opts.plainOutput {
				renderProvenanceSearchIDs(cmd, envelope.Data)
			} else {
				renderProvenanceSearchView(cmd, view)
				renderResponseMeta(cmd, opts, envelope.Meta)
			}
			return nil
		},
	}
	command.Flags().StringVar(&project, "project", "", "filter by exact project ID or slug")
	command.Flags().StringVar(&repository, "repo", "", "filter by exact repository identity")
	command.Flags().StringSliceVar(&collections, "collection", nil, "typed collection (project_state, repo_state, accepted_records, unresolved_cases, pending_candidates)")
	command.Flags().BoolVar(&includePending, "include-pending", false, "include unaccepted candidates in a separate typed collection")
	command.Flags().IntVar(&limit, "limit", 0, fmt.Sprintf("total result limit (default %d, maximum %d)", provenance.DefaultSearchLimit, provenance.MaximumSearchLimit))
	command.Flags().StringVar(&cursor, "cursor", "", "opaque next cursor from a prior identical search")
	command.Flags().BoolVar(&details, "details", false, "show the full search representation including match diagnostics")
	return command
}

func newProvenanceRecordCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "record", Short: "Load exact accepted provenance records"}
	command.AddCommand(newProvenanceRecordGetCommand(opts))
	return command
}

func newProvenanceRecordGetCommand(opts *options) *cobra.Command {
	limit := searchExactProjectionLimitCLI
	var sources, details bool
	command := &cobra.Command{
		Use:   "get <record-id>",
		Short: "Load one accepted record through the foundation exact-get operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, commandCtx, err := provenanceExactCommandContext(opts, args[0], limit)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			var envelope response.Envelope[provenance.RecordLifecycleProjection]
			var expansion *provenance.LinkedSources
			if sources {
				expanded, err := commandCtx.Client.GetProvenanceRecordWithSources(ctx, commandCtx.CorrelationID, id, limit)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput && details {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(expanded)
				}
				envelope = response.Envelope[provenance.RecordLifecycleProjection]{OK: expanded.OK, Data: expanded.Data.RecordLifecycleProjection, Meta: expanded.Meta}
				expansion = &expanded.Data.Sources
			} else {
				envelope, err = commandCtx.Client.GetProvenanceRecord(ctx, commandCtx.CorrelationID, id, limit)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Record.ID)
				return nil
			}
			if details && expansion != nil {
				full := response.Envelope[provenance.RecordWithSources]{OK: envelope.OK, Data: provenance.RecordWithSources{RecordLifecycleProjection: envelope.Data, Sources: *expansion}, Meta: envelope.Meta}
				return renderProvenanceView(cmd, opts, full, newProvenanceRecordView(envelope.Data, expansion), true)
			}
			return renderProvenanceView(cmd, opts, envelope, newProvenanceRecordView(envelope.Data, expansion), details)
		},
	}
	command.Flags().IntVar(&limit, "limit", searchExactProjectionLimitCLI, "maximum lifecycle items per bounded exact collection")
	command.Flags().BoolVar(&sources, "sources", false, "include linked stored source receipts without accessing current sources")
	command.Flags().BoolVar(&details, "details", false, "show the full record lifecycle representation")
	return command
}

func newProvenanceCandidateCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "candidate", Short: "Load exact provenance candidates across lifecycle states"}
	command.AddCommand(newProvenanceCandidateGetCommand(opts))
	command.AddCommand(newProvenanceCandidateListCommand(opts))
	command.AddCommand(newProvenanceCandidateRegisterCommand(opts))
	return command
}

func newProvenanceCandidateGetCommand(opts *options) *cobra.Command {
	limit := searchExactProjectionLimitCLI
	var sources, details bool
	command := &cobra.Command{
		Use:   "get <candidate-id>",
		Short: "Load one candidate through the foundation exact-get operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, commandCtx, err := provenanceExactCommandContext(opts, args[0], limit)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			var envelope response.Envelope[provenance.CandidateLifecycleProjection]
			var expansion *provenance.LinkedSources
			if sources {
				expanded, err := commandCtx.Client.GetProvenanceCandidateWithSources(ctx, commandCtx.CorrelationID, id, limit)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput && details {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(expanded)
				}
				envelope = response.Envelope[provenance.CandidateLifecycleProjection]{OK: expanded.OK, Data: expanded.Data.CandidateLifecycleProjection, Meta: expanded.Meta}
				expansion = &expanded.Data.Sources
			} else {
				envelope, err = commandCtx.Client.GetProvenanceCandidate(ctx, commandCtx.CorrelationID, id, limit)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Candidate.ID)
				return nil
			}
			if details && expansion != nil {
				full := response.Envelope[provenance.CandidateWithSources]{OK: envelope.OK, Data: provenance.CandidateWithSources{CandidateLifecycleProjection: envelope.Data, Sources: *expansion}, Meta: envelope.Meta}
				return renderProvenanceView(cmd, opts, full, newProvenanceCandidateView(envelope.Data, expansion), true)
			}
			return renderProvenanceView(cmd, opts, envelope, newProvenanceCandidateView(envelope.Data, expansion), details)
		},
	}
	command.Flags().IntVar(&limit, "limit", searchExactProjectionLimitCLI, "maximum lifecycle items per bounded exact collection")
	command.Flags().BoolVar(&sources, "sources", false, "include linked stored source receipts without accessing current sources")
	command.Flags().BoolVar(&details, "details", false, "show the full candidate lifecycle representation")
	return command
}

func newProvenanceCaseCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "case", Short: "Load exact provenance resolution cases"}
	command.AddCommand(newProvenanceCaseGetCommand(opts))
	return command
}

func newProvenanceCaseGetCommand(opts *options) *cobra.Command {
	limit := searchExactProjectionLimitCLI
	var details bool
	command := &cobra.Command{
		Use:   "get <case-id>",
		Short: "Load one resolution case through the foundation exact-get operation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, commandCtx, err := provenanceExactCommandContext(opts, args[0], limit)
			if err != nil {
				return renderError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), provenance.FoundationRequestTimeout)
			defer cancel()
			envelope, err := commandCtx.Client.GetProvenanceCase(ctx, commandCtx.CorrelationID, id, limit)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Case.ID)
				return nil
			}
			return renderProvenanceView(cmd, opts, envelope, newProvenanceCaseView(envelope.Data), details)
		},
	}
	command.Flags().IntVar(&limit, "limit", searchExactProjectionLimitCLI, "maximum lifecycle items per bounded exact collection")
	command.Flags().BoolVar(&details, "details", false, "show the full resolution case lifecycle representation")
	return command
}

const searchExactProjectionLimitCLI = 16

func newProvenanceRelationshipCommand(opts *options) *cobra.Command {
	parent := &cobra.Command{Use: "relationship", Short: "Read an exact Provenance relationship"}
	limit := searchExactProjectionLimitCLI
	var details bool
	get := &cobra.Command{Use: "get <relationship-id>", Args: cobra.ExactArgs(1), Short: "Read relationship direction, evidence and effective state", RunE: func(cmd *cobra.Command, args []string) error {
		id, cc, err := provenanceExactCommandContext(opts, args[0], limit)
		if err != nil {
			return renderError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), provenance.FoundationRequestTimeout)
		defer cancel()
		env, err := cc.Client.GetProvenanceRelationship(ctx, cc.CorrelationID, id, limit)
		if err != nil {
			return renderError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), env.Data.Relationship.ID)
			return nil
		}
		return renderProvenanceView(cmd, opts, env, newProvenanceRelationshipView(env.Data), details)
	}}
	get.Flags().IntVar(&limit, "limit", searchExactProjectionLimitCLI, "maximum bounded relationship lifecycle events")
	get.Flags().BoolVar(&details, "details", false, "return the full relationship lifecycle representation")
	parent.AddCommand(get)
	return parent
}

func provenanceExactCommandContext(opts *options, rawID string, limit int) (provenance.SemanticID, commandContext, error) {
	id, err := provenance.ParseSemanticID(rawID)
	if err != nil {
		return "", commandContext{}, loomerrors.Wrap("provenance.invalid_request", "provenance", "exact_get", "Exact provenance UUID is invalid.", err)
	}
	if limit < 1 || limit > searchExactProjectionLimitCLI {
		return "", commandContext{}, loomerrors.Wrap("provenance.invalid_request", "provenance", "exact_get", fmt.Sprintf("Exact provenance limit must be between 1 and %d.", searchExactProjectionLimitCLI), nil)
	}
	commandCtx, err := resolveCommandContext(opts)
	return id, commandCtx, err
}

func renderProvenanceSearchIDs(cmd *cobra.Command, result provenance.SearchResponse) {
	for _, item := range result.AcceptedRecords.Items {
		fmt.Fprintln(cmd.OutOrStdout(), item.RecordID)
	}
	for _, item := range result.UnresolvedCases.Items {
		fmt.Fprintln(cmd.OutOrStdout(), item.CaseID)
	}
	for _, item := range result.PendingCandidates.Items {
		fmt.Fprintln(cmd.OutOrStdout(), item.CandidateID)
	}
	for _, item := range result.RepositoryState.Items {
		fmt.Fprintln(cmd.OutOrStdout(), item.RepositoryID)
	}
	for _, item := range result.ProjectState.Items {
		fmt.Fprintln(cmd.OutOrStdout(), item.ProjectID)
	}
}
