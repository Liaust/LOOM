package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

func newNotesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Manage LOOM notes knowledge surfaces",
	}
	cmd.AddCommand(newNotesOverviewCommand(opts))
	cmd.AddCommand(newNotesRootsCommand(opts))
	cmd.AddCommand(newNotesObjectsCommand(opts))
	cmd.AddCommand(newNotesPassageCommand(opts))
	cmd.AddCommand(newNotesSearchCommand(opts))
	cmd.AddCommand(newNotesEmbeddingsCommand(opts))
	cmd.AddCommand(newNotesPipelinesCommand(opts))
	cmd.AddCommand(newNotesProjectionCommand(opts))
	cmd.AddCommand(newNotesReprocessCommand(opts))
	cmd.AddCommand(newNotesRunCommand(opts))
	return cmd
}

func newNotesPassageCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "passage", Short: "Read an exact retained Notes passage"}
	var input knowledge.NotesPassageInput
	get := &cobra.Command{
		Use: "get <chunk-id>", Short: "Read a version-bound passage subject to current source access", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input.KnowledgeChunkID = args[0]
			if err := knowledge.ValidateNotesPassageInput(input); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("notes.invalid_passage", "knowledge", "notes_passage", "An exact object, version, chunk and source hash are required.", err))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetKnowledgeNotesPassage(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.passage_failed", "knowledge", "notes_passage", "Could not read the requested passage.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesCustody(cmd, envelope.Data.Custody)
			fmt.Fprintf(cmd.OutOrStdout(), "%s\nVersion: %s\nSource hash: %s\nHistorical: %t\nLocator: %s\n\n%s\n", envelope.Data.KnowledgeObjectID, envelope.Data.KnowledgeObjectVersionID, envelope.Data.SourceHash, envelope.Data.Historical, envelope.Data.StructuralPath, envelope.Data.Text)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	get.Flags().StringVar(&input.KnowledgeObjectID, "object", "", "exact knowledge object ID")
	get.Flags().StringVar(&input.KnowledgeObjectVersionID, "version", "", "exact retained knowledge version ID")
	get.Flags().StringVar(&input.SourceHash, "source-hash", "", "exact sha256 source content address")
	addNotesSourceLifecycleFlag(get, &input.SourceLifecycle)
	cmd.AddCommand(get)
	return cmd
}

func newNotesOverviewCommand(opts *options) *cobra.Command {
	input := knowledge.NotesOverviewInput{}
	cmd := &cobra.Command{
		Use:   "overview",
		Short: "Show the LOOM notes knowledge overview",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetKnowledgeNotesOverview(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.overview_failed", "knowledge", "notes_overview", "Could not load notes overview.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesOverview(cmd, opts, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.NodeKey, "node", "", "filter by node key")
	cmd.Flags().StringVar(&input.SourceCategory, "category", "", "source category: notes, topics, library, or projects")
	cmd.Flags().StringVar(&input.ProjectID, "project-id", "", "filter by project id")
	cmd.Flags().BoolVar(&input.IncludeInactive, "all", false, "include inactive notes source roots")
	addNotesSourceLifecycleFlag(cmd, &input.SourceLifecycle)
	return cmd
}

func newNotesRootsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roots",
		Short: "Manage indexed notes source roots",
	}
	cmd.AddCommand(newNotesRootsListCommand(opts))
	cmd.AddCommand(newNotesRootsReconcileCommand(opts))
	return cmd
}

func newNotesRootsListCommand(opts *options) *cobra.Command {
	filter := knowledge.SourceRootFilter{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered notes source roots",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListKnowledgeNotesRoots(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.roots_list_failed", "knowledge", "notes_roots", "Could not list notes source roots.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesRoots(cmd, opts, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.RootKind, "root-kind", "", "filter by root kind")
	cmd.Flags().StringVar(&filter.SourceCategory, "category", "", "source category: notes, topics, library, or projects")
	cmd.Flags().StringVar(&filter.NodeKey, "node", "", "filter by node key")
	cmd.Flags().StringVar(&filter.ProjectID, "project-id", "", "filter by project id")
	cmd.Flags().StringVar(&filter.Status, "status", "", "filter by source-root status")
	cmd.Flags().BoolVar(&filter.IncludeInactive, "all", false, "include inactive source roots")
	addNotesSourceLifecycleFlag(cmd, &filter.SourceLifecycle)
	return cmd
}

func newNotesRootsReconcileCommand(opts *options) *cobra.Command {
	var dryRun = true
	var apply bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile watched notes roots into the knowledge index",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply {
				dryRun = false
			}
			if !dryRun && !yes {
				return fmt.Errorf("pass --yes to apply notes source-root reconciliation")
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			input := knowledge.SourceRootReconcileInput{DryRun: dryRun}
			if err := seedLocalBoxNotesRootsForReconcile(ctx, commandCtx, &input); err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.roots_box_seed_failed", "knowledge", "notes_roots", "Could not seed local Box notes root before reconciliation.", err))
			}
			envelope, err := commandCtx.Client.ReconcileKnowledgeNotesRoots(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.roots_reconcile_failed", "knowledge", "notes_roots", "Could not reconcile notes source roots.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesRootReconcile(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "preview notes source-root reconciliation without writing")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply notes source-root reconciliation")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm applying notes source-root reconciliation")
	return cmd
}

func seedLocalBoxNotesRootsForReconcile(ctx context.Context, commandCtx commandContext, input *knowledge.SourceRootReconcileInput) error {
	if input == nil {
		return nil
	}
	cfg := commandCtx.Config
	if strings.TrimSpace(cfg.BoxPath) == "" || strings.EqualFold(strings.TrimSpace(cfg.NodeKind), "main") {
		return nil
	}
	resolved, err := box.Resolve(box.ResolveInput{
		ConfiguredPath:   cfg.BoxPath,
		RuntimeStateRoot: cfg.BoxStateRoot,
		ConfigProfile:    cfg.BoxProfile,
		NodeID:           cfg.NodeID,
		NodeRole:         cfg.NodeRole,
	})
	if err != nil {
		return err
	}
	plan, err := box.BuildWatchPlan(box.WatchStatusInput{Resolved: resolved})
	if err != nil {
		return nil
	}
	if input.DryRun {
		input.BoxRegistrations = append(input.BoxRegistrations, knowledge.BoxWatchPlanSourceRootRegistrations(plan, "")...)
		return nil
	}
	_, err = commandCtx.Client.ApplyBoxWatchPolicy(ctx, commandCtx.CorrelationID, box.WatchApplyInput{Resolved: resolved, Plan: &plan})
	return err
}

func newNotesObjectsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "objects",
		Short: "Manage indexed notes knowledge objects",
	}
	cmd.AddCommand(newNotesObjectsListCommand(opts))
	cmd.AddCommand(newNotesObjectsShowCommand(opts))
	cmd.AddCommand(newNotesObjectsReconcileCommand(opts))
	return cmd
}

func newNotesObjectsListCommand(opts *options) *cobra.Command {
	filter := knowledge.KnowledgeObjectFilter{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List indexed notes knowledge objects",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListKnowledgeNotesObjects(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.objects_list_failed", "knowledge", "notes_objects", "Could not list notes knowledge objects.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesObjects(cmd, opts, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&filter.NotesSourceRootID, "root", "", "filter by notes source root id")
	cmd.Flags().StringVar(&filter.ProjectID, "project-id", "", "filter by project id")
	cmd.Flags().StringVar(&filter.SourceNodeKey, "node", "", "filter by source node key")
	cmd.Flags().StringVar(&filter.ProcessingState, "state", "", "filter by processing state")
	cmd.Flags().BoolVar(&filter.IncludeDeleted, "all", false, "include deleted knowledge objects")
	cmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum objects to list")
	cmd.Flags().IntVar(&filter.Offset, "offset", 0, "object list offset")
	addNotesSourceLifecycleFlag(cmd, &filter.SourceLifecycle)
	cmd.Flags().StringVar(&filter.SourceCategory, "category", "", "source category: notes, topics, library, or projects")
	return cmd
}

func newNotesObjectsShowCommand(opts *options) *cobra.Command {
	var lifecycle knowledge.SourceLifecycleFilter
	cmd := &cobra.Command{
		Use:   "show <object-ref>",
		Short: "Show an indexed notes knowledge object",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetKnowledgeNotesObjectWithLifecycle(ctx, commandCtx.CorrelationID, args[0], lifecycle)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.object_show_failed", "knowledge", "notes_objects", "Could not show notes knowledge object.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesObject(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	addNotesSourceLifecycleFlag(cmd, &lifecycle)
	return cmd
}

func newNotesObjectsReconcileCommand(opts *options) *cobra.Command {
	var dryRun = true
	var apply bool
	var yes bool
	input := knowledge.KnowledgeObjectReconcileInput{}
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile catalogued notes files into knowledge objects",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply {
				dryRun = false
			}
			if !dryRun && !yes {
				return fmt.Errorf("pass --yes to apply notes object reconciliation")
			}
			input.DryRun = dryRun
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ReconcileKnowledgeNotesObjects(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.objects_reconcile_failed", "knowledge", "notes_objects", "Could not reconcile notes knowledge objects.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesObjectReconcile(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "preview notes object reconciliation without writing")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply notes object reconciliation")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm applying notes object reconciliation")
	cmd.Flags().StringVar(&input.SourceRootID, "root", "", "limit reconciliation to a notes source root id")
	cmd.Flags().BoolVar(&input.IncludeDeleted, "include-deleted", false, "include SQL-deleted storage catalogue rows")
	return cmd
}

func newNotesSearchCommand(opts *options) *cobra.Command {
	input := knowledge.NotesSearchInput{}
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search indexed notes knowledge",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("notes.search_query_required", "knowledge", "query", "Pass one non-empty notes search query."))
			}
			searchInput := mergeNotesSearchCommandInput(knowledge.ParseNotesSearchInput(args[0]), input)
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.SearchKnowledgeNotes(ctx, commandCtx.CorrelationID, searchInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.search_failed", "knowledge", "notes_search", "Could not search notes knowledge.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesSearchResults(cmd, opts, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.ProjectRef, "project", "", "filter by project id, slug, or scope key")
	cmd.Flags().StringVar(&input.SourceCategory, "category", "", "source category: notes, topics, library, or projects")
	cmd.Flags().StringVar(&input.SourceNodeKey, "node", "", "filter by source node key")
	cmd.Flags().StringVar(&input.RootRef, "root", "", "filter by notes source root id, backend key, or source path")
	cmd.Flags().StringVar(&input.FileClass, "file-class", "", "filter by file class")
	cmd.Flags().StringVar(&input.Path, "path", "", "filter by relative path substring")
	cmd.Flags().StringArrayVar(&input.Tags, "tag", nil, "filter by frontmatter tag; can be repeated")
	cmd.Flags().StringVar(&input.Mode, "mode", "", "search mode: lexical, semantic, or hybrid")
	cmd.Flags().StringVar(&input.After, "after", "", "include results at or after an RFC3339 timestamp or YYYY-MM-DD date")
	cmd.Flags().StringVar(&input.Before, "before", "", "include results before an RFC3339 timestamp or YYYY-MM-DD date")
	cmd.Flags().StringVar(&input.Sort, "sort", "", "result order: relevance, newest, or oldest")
	cmd.Flags().IntVar(&input.Limit, "limit", 10, "maximum number of results to return")
	addNotesSourceLifecycleFlag(cmd, &input.SourceLifecycle)
	return cmd
}

func mergeNotesSearchCommandInput(parsed knowledge.NotesSearchInput, flags knowledge.NotesSearchInput) knowledge.NotesSearchInput {
	if flags.SourceLifecycle != "" {
		parsed.SourceLifecycle = flags.SourceLifecycle
	}
	if flags.SourceCategory != "" {
		parsed.SourceCategory = flags.SourceCategory
	}
	if strings.TrimSpace(flags.ProjectRef) != "" {
		parsed.ProjectRef = flags.ProjectRef
	}
	if strings.TrimSpace(flags.ProjectID) != "" {
		parsed.ProjectID = flags.ProjectID
	}
	if strings.TrimSpace(flags.SourceNodeKey) != "" {
		parsed.SourceNodeKey = flags.SourceNodeKey
	}
	if strings.TrimSpace(flags.RootRef) != "" {
		parsed.RootRef = flags.RootRef
	}
	if strings.TrimSpace(flags.NotesSourceRootID) != "" {
		parsed.NotesSourceRootID = flags.NotesSourceRootID
	}
	if strings.TrimSpace(flags.FileClass) != "" {
		parsed.FileClass = flags.FileClass
	}
	if strings.TrimSpace(flags.Path) != "" {
		parsed.Path = flags.Path
	}
	if len(flags.Tags) > 0 {
		parsed.Tags = append(parsed.Tags, flags.Tags...)
	}
	if strings.TrimSpace(flags.Mode) != "" {
		parsed.Mode = flags.Mode
	}
	if strings.TrimSpace(flags.After) != "" {
		parsed.After = flags.After
	}
	if strings.TrimSpace(flags.Before) != "" {
		parsed.Before = flags.Before
	}
	if strings.TrimSpace(flags.Sort) != "" {
		parsed.Sort = flags.Sort
	}
	if flags.Limit > 0 {
		parsed.Limit = flags.Limit
	}
	return parsed
}

type notesSourceLifecycleFlag struct {
	target *knowledge.SourceLifecycleFilter
}

func (v notesSourceLifecycleFlag) String() string { return string(*v.target) }
func (v notesSourceLifecycleFlag) Type() string   { return "lifecycle" }
func (v notesSourceLifecycleFlag) Set(raw string) error {
	value, err := knowledge.NormalizeSourceLifecycleFilter(knowledge.SourceLifecycleFilter(raw))
	if err != nil {
		return err
	}
	*v.target = value
	return nil
}
func addNotesSourceLifecycleFlag(cmd *cobra.Command, target *knowledge.SourceLifecycleFilter) {
	cmd.Flags().Var(notesSourceLifecycleFlag{target: target}, "source-lifecycle", "source lifecycle: active (default), archived, or all; independent of --all")
}

func renderNotesCustody(cmd *cobra.Command, custody knowledge.NotesCustodyContext) {
	fmt.Fprintf(cmd.OutOrStdout(), "Source lifecycle: %s\nOriginal path: %s\nCanonical path: %s\n", dashIfEmpty(string(custody.SourceLifecycle)), dashIfEmpty(custody.OriginalPath), dashIfEmpty(custody.CanonicalPath))
	if custody.ArchiveOperationID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Archive operation: %s\nArchived at: %s\nWorkspace: %s %s\n", custody.ArchiveOperationID, timePtrOrDash(custody.ArchivedAt), custody.WorkspaceKind, custody.WorkspaceObjectID)
	}
}

func newNotesReprocessCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reprocess",
		Short: "Queue notes knowledge reprocessing",
	}
	cmd.AddCommand(newNotesReprocessScopeCommand(opts, "object <knowledge-object-ref>", "Queue one notes knowledge object for reprocessing", knowledge.ReprocessScopeObject))
	cmd.AddCommand(newNotesReprocessScopeCommand(opts, "root <root-ref>", "Queue notes knowledge objects under one source root", knowledge.ReprocessScopeRoot))
	cmd.AddCommand(newNotesReprocessScopeCommand(opts, "project <project-ref>", "Queue notes knowledge objects for one project", knowledge.ReprocessScopeProject))
	cmd.AddCommand(newNotesReprocessScopeCommand(opts, "node <node-ref>", "Queue notes knowledge objects for one node", knowledge.ReprocessScopeNode))
	cmd.AddCommand(newNotesReprocessScopeCommand(opts, "file-class <file-class>", "Queue notes knowledge objects for one file class", knowledge.ReprocessScopeFileClass))
	staleInput := knowledge.ReprocessInput{Scope: knowledge.ReprocessScopeStale, StaleOnly: true}
	var staleIDKey string
	staleCmd := &cobra.Command{
		Use:   "stale",
		Short: "Queue stale notes knowledge objects for reprocessing",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runNotesReprocess(cmd, opts, staleInput, staleIDKey)
		},
	}
	addNotesReprocessFlags(staleCmd, &staleInput, &staleIDKey)
	staleCmd.Flags().StringVar(&staleInput.PipelineKey, "pipeline", knowledge.KnowledgeObjectPipelineMarkdownText, "pipeline key to check for stale work")
	cmd.AddCommand(staleCmd)
	return cmd
}

func newNotesProjectionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "projection",
		Short: "Manage the read-only loom-notes projection",
	}
	cmd.AddCommand(newNotesProjectionStatusCommand(opts))
	cmd.AddCommand(newNotesProjectionRebuildCommand(opts))
	return cmd
}

func newNotesProjectionStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show loom-notes projection status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetKnowledgeNotesProjectionStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.projection_status_failed", "knowledge", "notes_projection", "Could not read notes projection status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesProjectionStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newNotesProjectionRebuildCommand(opts *options) *cobra.Command {
	input := notesprojection.RebuildInput{DryRun: true}
	var apply bool
	var yes bool
	cmd := &cobra.Command{
		Use:   "rebuild",
		Short: "Rebuild the read-only loom-notes projection",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apply {
				input.DryRun = false
			}
			if !input.DryRun && !yes {
				return fmt.Errorf("pass --yes to apply notes projection rebuild")
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.RebuildKnowledgeNotesProjection(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.projection_rebuild_failed", "knowledge", "notes_projection", "Could not rebuild notes projection.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			renderNotesProjectionRebuild(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&input.DryRun, "dry-run", true, "preview notes projection rebuild without writing")
	cmd.Flags().BoolVar(&apply, "apply", false, "apply notes projection rebuild")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm applying notes projection rebuild")
	cmd.Flags().BoolVar(&input.IncludeAll, "include-all", false, "include all changes/findings/manifest entries in the response")
	cmd.Flags().IntVar(&input.MaxResults, "max-results", notesprojection.DefaultRebuildResultLimit, "maximum changes/findings returned unless --include-all is set")
	cmd.Flags().IntVar(&input.MaxObjects, "max-objects", 0, "maximum knowledge objects to project")
	return cmd
}

func newNotesReprocessScopeCommand(opts *options, use, short, scope string) *cobra.Command {
	input := knowledge.ReprocessInput{Scope: scope}
	var idempotencyKey string
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch scope {
			case knowledge.ReprocessScopeObject:
				input.ObjectRef = args[0]
			case knowledge.ReprocessScopeRoot:
				input.RootRef = args[0]
			case knowledge.ReprocessScopeProject:
				input.ProjectRef = args[0]
			case knowledge.ReprocessScopeNode:
				input.NodeRef = args[0]
			case knowledge.ReprocessScopeFileClass:
				input.FileClass = args[0]
			}
			return runNotesReprocess(cmd, opts, input, idempotencyKey)
		},
	}
	addNotesReprocessFlags(cmd, &input, &idempotencyKey)
	if scope != knowledge.ReprocessScopeFileClass {
		cmd.Flags().StringVar(&input.FileClass, "file-class", "", "limit reprocessing to a supported file class")
	}
	return cmd
}

func addNotesReprocessFlags(cmd *cobra.Command, input *knowledge.ReprocessInput, idempotencyKey *string) {
	cmd.Flags().BoolVar(&input.Force, "force", false, "mark existing completed pipeline rows stale before queueing")
	cmd.Flags().IntVar(&input.Limit, "limit", 200, "maximum knowledge objects to queue")
	cmd.Flags().IntVar(&input.Priority, "priority", 100, "queue priority; lower values run first")
	cmd.Flags().StringVar(idempotencyKey, "idempotency-key", "", "explicit idempotency key for this reprocess request")
}

func runNotesReprocess(cmd *cobra.Command, opts *options, input knowledge.ReprocessInput, idempotencyKey string) error {
	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("transport.unavailable", "runtime", "client", "Could not connect to LOOM backend.", err))
	}
	client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "notes.reprocess."+input.Scope)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	envelope, err := client.ReprocessKnowledgeNotes(ctx, commandCtx.CorrelationID, input)
	if err != nil {
		return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.reprocess_failed", "knowledge", "notes_reprocess", "Could not queue notes reprocessing.", err))
	}
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
	}
	renderNotesReprocess(cmd, envelope.Data)
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func newNotesRunCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run notes knowledge workers",
	}
	var once bool
	var reason string
	var idempotencyKey string
	indexerCmd := &cobra.Command{
		Use:   "indexer",
		Short: "Run the notes knowledge indexer once",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !once {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("notes.run_mode_required", "knowledge", "main.knowledge_indexer", "Pass --once to run the notes knowledge indexer once."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client, _ := withEffectIdempotency(commandCtx.Client, idempotencyKey, "notes.run.indexer")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if strings.TrimSpace(reason) == "" {
				reason = "manual notes knowledge indexer run"
			}
			envelope, err := client.RunKnowledgeNotesIndexer(ctx, commandCtx.CorrelationID, workers.RunOnceInput{Reason: reason})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run notes knowledge indexer.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Run.WorkerRunID)
				return nil
			}
			renderWorkerRunOnce(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	indexerCmd.Flags().BoolVar(&once, "once", false, "run the notes knowledge indexer once")
	indexerCmd.Flags().StringVar(&reason, "reason", "", "reason recorded on the worker run")
	indexerCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for this run request")
	cmd.AddCommand(indexerCmd)
	cmd.AddCommand(newNotesWorkerOnceCommand(opts, "coordinator", "Run the Notes pipeline coordinator once", "notes.run.coordinator", func(ctx context.Context, client localclient.Client, correlationID string, input workers.RunOnceInput) (workers.RunOnceResult, error) {
		envelope, err := client.RunKnowledgeNotesCoordinator(ctx, correlationID, input)
		return envelope.Data, err
	}))
	cmd.AddCommand(newNotesWorkerOnceCommand(opts, "heavy", "Run the Notes heavy executor once", "notes.run.heavy", func(ctx context.Context, client localclient.Client, correlationID string, input workers.RunOnceInput) (workers.RunOnceResult, error) {
		envelope, err := client.RunKnowledgeNotesHeavy(ctx, correlationID, input)
		return envelope.Data, err
	}))
	return cmd
}

func renderNotesRoots(cmd *cobra.Command, opts *options, roots []knowledge.SourceRoot) {
	if opts.plainOutput {
		for _, root := range roots {
			fmt.Fprintln(cmd.OutOrStdout(), root.NotesSourceRootID)
		}
		return
	}
	if len(roots) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No notes source roots registered.")
		return
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tNODE\tPROJECT\tSTATUS\tPATH")
	for _, root := range roots {
		projectID := ""
		if root.ProjectID != nil {
			projectID = *root.ProjectID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", root.NotesSourceRootID, root.RootKind, root.NodeKey, projectID, root.Status, root.SourcePath)
		if root.LifecycleCounts != nil {
			fmt.Fprintf(tw, "\tactive objects=%d archived objects=%d\n", root.LifecycleCounts.Active, root.LifecycleCounts.Archived)
		}
	}
	_ = tw.Flush()
}

func renderNotesObjects(cmd *cobra.Command, opts *options, objects []knowledge.KnowledgeObject) {
	if opts.plainOutput {
		for _, object := range objects {
			fmt.Fprintln(cmd.OutOrStdout(), object.KnowledgeObjectID)
		}
		return
	}
	if len(objects) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No notes knowledge objects indexed.")
		return
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tROOT\tNODE\tPROJECT\tLIFECYCLE\tSTATE\tEXTRACTION\tCLASS\tPATH")
	for _, object := range objects {
		projectID := ""
		if object.ProjectID != nil {
			projectID = *object.ProjectID
		}
		lifecycle := "-"
		if object.NotesCustodyContext != nil {
			lifecycle = string(object.SourceLifecycle)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			object.KnowledgeObjectID,
			object.NotesSourceRootID,
			object.SourceNodeKey,
			projectID,
			lifecycle,
			object.ProcessingState,
			knowledge.KnowledgeObjectExtractionStatus(object),
			object.FileClass,
			object.RelativePath,
		)
	}
	_ = tw.Flush()
}

func renderNotesObject(cmd *cobra.Command, object knowledge.KnowledgeObject) {
	summary := knowledge.SummarizeKnowledgeObjectExtraction(object)
	fmt.Fprintf(cmd.OutOrStdout(), "Knowledge object: %s\n", object.KnowledgeObjectID)
	if object.NotesCustodyContext != nil {
		renderNotesCustody(cmd, *object.NotesCustodyContext)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", dashIfEmpty(object.NotesSourceRootID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source: category=%s posture=%s declaration=%s topic=%s collection=%s\n", dashIfEmpty(object.SourceCategory), dashIfEmpty(object.SourcePosture), dashIfEmpty(object.Declaration), dashIfEmpty(object.TopicKey), dashIfEmpty(object.CollectionKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Node: %s\n", dashIfEmpty(object.SourceNodeKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Project: %s\n", notesStringPtrOrDash(object.ProjectID))
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", dashIfEmpty(object.RelativePath))
	fmt.Fprintf(cmd.OutOrStdout(), "Class: %s mime=%s bytes=%s\n", dashIfEmpty(object.FileClass), dashIfEmpty(object.MimeType), int64PtrOrDash(object.SizeBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Processing: state=%s pipeline=%s version=%s\n", dashIfEmpty(object.ProcessingState), dashIfEmpty(object.PipelineKey), dashIfEmpty(object.PipelineVersion))
	fmt.Fprintf(cmd.OutOrStdout(), "Extraction: status=%s metadata_only=%t extractor=%s version=%s chunks=%d text_sections=%d links=%d headings=%d warnings=%d\n",
		dashIfEmpty(summary.Status),
		summary.MetadataOnly,
		dashIfEmpty(summary.ExtractorKey),
		dashIfEmpty(summary.ExtractorVersion),
		summary.ChunkCount,
		summary.TextSectionCount,
		summary.LinkCount,
		summary.HeadingCount,
		summary.WarningCount,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Last seen: %s\n", object.LastSeenAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Last processed: %s\n", timePtrOrDash(object.LastProcessedAt))
	if object.LastErrorCode != "" || object.LastErrorMessage != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Last error: %s %s\n", dashIfEmpty(object.LastErrorCode), dashIfEmpty(object.LastErrorMessage))
	}
}

func renderNotesOverview(cmd *cobra.Command, opts *options, overview knowledge.NotesOverview) {
	if opts.plainOutput {
		fmt.Fprintf(cmd.OutOrStdout(), "roots=%d active_roots=%d objects=%d files=%d directories=%d bytes=%d search_documents=%d\n",
			overview.Totals.RootCount,
			overview.Totals.ActiveRootCount,
			overview.Totals.ObjectCount,
			overview.Totals.FileCount,
			overview.Totals.DirectoryCount,
			overview.Totals.SizeBytes,
			overview.Totals.SearchDocumentCount,
		)
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "LOOM notes")
	fmt.Fprintf(cmd.OutOrStdout(), "Source lifecycle: %s; active objects=%d archived objects=%d\n", dashIfEmpty(string(overview.SourceLifecycle)), overview.Totals.LifecycleCounts.Active, overview.Totals.LifecycleCounts.Archived)
	if !overview.GeneratedAt.IsZero() {
		fmt.Fprintf(cmd.OutOrStdout(), "Generated: %s\n", overview.GeneratedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Roots: %d active=%d\n", overview.Totals.RootCount, overview.Totals.ActiveRootCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Objects: %d files=%d directories=%d bytes=%d\n",
		overview.Totals.ObjectCount,
		overview.Totals.FileCount,
		overview.Totals.DirectoryCount,
		overview.Totals.SizeBytes,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Search documents: %d\n", overview.Totals.SearchDocumentCount)
	renderNotesOverviewFileClasses(cmd, overview.FileClasses)
	renderNotesOverviewProcessingStates(cmd, overview.ProcessingStates)
	renderNotesOverviewExtractionStates(cmd, overview.ExtractionStates)
	renderNotesOverviewProjection(cmd, overview.Projection)
	renderNotesOverviewIndexHealth(cmd, overview.IndexHealth)
	renderNotesOverviewNodes(cmd, overview.Nodes)
}

func renderNotesOverviewFileClasses(cmd *cobra.Command, classes []knowledge.NotesFileClassCount) {
	if len(classes) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "File types")
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tCOUNT\tBYTES")
	for _, class := range classes {
		fmt.Fprintf(tw, "%s\t%d\t%d\n", class.FileClass, class.Count, class.SizeBytes)
	}
	_ = tw.Flush()
}

func renderNotesOverviewProcessingStates(cmd *cobra.Command, states []knowledge.NotesProcessingStateCount) {
	if len(states) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Processing")
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tCOUNT")
	for _, state := range states {
		fmt.Fprintf(tw, "%s\t%d\n", state.ProcessingState, state.Count)
	}
	_ = tw.Flush()
}

func renderNotesOverviewExtractionStates(cmd *cobra.Command, states []knowledge.NotesExtractionStateCount) {
	if len(states) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Extraction")
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tCOUNT")
	for _, state := range states {
		fmt.Fprintf(tw, "%s\t%d\n", state.ExtractionStatus, state.Count)
	}
	_ = tw.Flush()
}

func renderNotesOverviewProjection(cmd *cobra.Command, projection knowledge.NotesProjectionOverview) {
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Projection")
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", dashIfEmpty(projection.ProjectionRoot))
	fmt.Fprintf(cmd.OutOrStdout(), "Exists: %t read_only=%t raw_writes_supported=%t\n", projection.Exists, projection.ReadOnly, projection.RawWritesSupported)
	fmt.Fprintf(cmd.OutOrStdout(), "Entries: %d materialized=%d missing=%d skipped=%d findings=%d\n",
		projection.Entries,
		projection.Materialized,
		projection.Missing,
		projection.Skipped,
		projection.FindingCount,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Last rebuild: %s\n", timePtrOrDash(projection.LastRebuildAt))
}

func renderNotesOverviewIndexHealth(cmd *cobra.Command, health knowledge.NotesIndexHealth) {
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Search health")
	fmt.Fprintf(cmd.OutOrStdout(), "Search documents: %d\n", health.SearchDocumentCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Pipeline: queued=%d processing=%d complete=%d failed=%d skipped_unsupported=%d\n",
		health.Queued,
		health.Processing,
		health.Complete,
		health.Failed,
		health.SkippedUnsupported,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Last indexed: %s\n", timePtrOrDash(health.LastIndexedAt))
	fmt.Fprintf(cmd.OutOrStdout(), "Last pipeline update: %s\n", timePtrOrDash(health.LastPipelineUpdateAt))
	if health.LastFailureAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last failure: %s\n", timePtrOrDash(health.LastFailureAt))
	}
}

func renderNotesOverviewNodes(cmd *cobra.Command, nodes []knowledge.NotesNodeOverview) {
	fmt.Fprintln(cmd.OutOrStdout())
	fmt.Fprintln(cmd.OutOrStdout(), "Notes across network")
	if len(nodes) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No active notes roots.")
		return
	}
	for _, node := range nodes {
		fmt.Fprintf(cmd.OutOrStdout(), "Node %s: roots=%d files=%d search_documents=%d\n",
			dashIfEmpty(node.NodeKey),
			node.Totals.RootCount,
			node.Totals.FileCount,
			node.Totals.SearchDocumentCount,
		)
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tPROJECT\tROOT\tSTATUS\tFILES\tOBJECTS\tSEARCH\tPATH")
		for _, root := range node.Roots {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n",
				root.RootKind,
				notesOverviewProjectLabel(root),
				notesOverviewRootLabel(root),
				root.Status,
				root.Totals.FileCount,
				root.Totals.ObjectCount,
				root.Totals.SearchDocumentCount,
				dashIfEmpty(root.RootRelativePath),
			)
		}
		_ = tw.Flush()
	}
}

func notesOverviewProjectLabel(root knowledge.NotesRootOverview) string {
	if root.ProjectSlug != "" {
		return root.ProjectSlug
	}
	if root.ProjectID != "" {
		return root.ProjectID
	}
	return "-"
}

func notesOverviewRootLabel(root knowledge.NotesRootOverview) string {
	if root.DisplayName != "" {
		return root.DisplayName
	}
	if root.BackendRootKey != "" {
		return root.BackendRootKey
	}
	return root.NotesSourceRootID
}

func notesStringPtrOrDash(value *string) string {
	if value == nil {
		return "-"
	}
	return dashIfEmpty(*value)
}

func renderNotesSearchResults(cmd *cobra.Command, opts *options, results knowledge.NotesSearchResultSet) {
	if opts.plainOutput {
		for _, result := range results.Results {
			fmt.Fprintln(cmd.OutOrStdout(), result.KnowledgeObjectID)
		}
		return
	}
	if len(results.Results) == 0 {
		renderNotesSearchSummary(cmd, results)
		fmt.Fprintln(cmd.OutOrStdout(), "No notes search results.")
		return
	}
	renderNotesSearchSummary(cmd, results)
	followups := make([]string, len(results.Results))
	numbered := false
	for index, result := range results.Results {
		followups[index] = notesSearchFollowupLine(result, index+1)
		numbered = numbered || followups[index] != ""
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	if numbered {
		fmt.Fprint(tw, "RESULT\t")
	}
	fmt.Fprintln(tw, "LIFECYCLE\tSCORE\tDATE\tMATCH\tNODE\tPROJECT\tCATEGORY\tPOSTURE\tCLASS\tSOURCE\tEXTRACTION\tPATH\tSNIPPET")
	for index, result := range results.Results {
		for _, group := range results.LifecycleGroups {
			if group.Offset == index && group.ResultCount > 0 {
				fmt.Fprintf(tw, "[%s Notes]\n", group.SourceLifecycle)
			}
		}
		score := result.FinalScore
		if score == 0 {
			score = result.RankScore
		}
		if numbered {
			fmt.Fprintf(tw, "%d\t", index+1)
		}
		fmt.Fprintf(tw, "%s\t%.4f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			result.SourceLifecycle,
			score,
			notesSearchSelectedDateLabel(result),
			notesSearchMatchLabel(result),
			result.SourceNodeKey,
			result.ProjectID,
			result.SourceCategory,
			result.SourcePosture,
			result.FileClass,
			notesSearchTextSourceLabel(result),
			notesSearchExtractionLabel(result),
			notesSearchPathLabel(result),
			notesSearchSnippetLabel(result.Snippet),
		)
		if result.SourceLifecycle == knowledge.SourceLifecycleArchived {
			fmt.Fprintf(tw, "\toriginal=%s canonical=%s archived_at=%s archive=%s\n", result.OriginalPath, result.CanonicalPath, timePtrOrDash(result.ArchivedAt), result.ArchiveOperationID)
		}
		fmt.Fprint(tw, followups[index])
	}
	_ = tw.Flush()
}

func notesSearchFollowupLine(result knowledge.NotesSearchResult, number int) string {
	if !result.ValidPassageFollowup() {
		return ""
	}
	input := result.PassageFollowup
	line := fmt.Sprintf("Follow-up [%d]: loom notes passage get %s --object %s --version %s --source-hash %s --source-lifecycle %s\n",
		number, input.KnowledgeChunkID, input.KnowledgeObjectID, input.KnowledgeObjectVersionID, input.SourceHash, input.SourceLifecycle)
	if len(line) > 512 {
		return ""
	}
	return line
}

func notesSearchSelectedDateLabel(result knowledge.NotesSearchResult) string {
	if result.RecencyAt.IsZero() {
		return "-"
	}
	return result.RecencyAt.UTC().Format("2006-01-02")
}

func renderNotesSearchSummary(cmd *cobra.Command, results knowledge.NotesSearchResultSet) {
	fmt.Fprintf(cmd.OutOrStdout(), "Source lifecycle: %s; archived matches omitted=%d truncated=%t\n", dashIfEmpty(string(results.SourceLifecycle)), results.ArchivedMatchesOmitted, results.ArchivedMatchesOmittedTruncated)
	mode := strings.TrimSpace(results.Mode)
	if mode == "" {
		mode = "-"
	}
	query := strings.TrimSpace(results.Query)
	if query == "" {
		query = "-"
	}
	if results.FallbackReason != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Notes search: query=%q mode=%s fallback=%s results=%d\n", query, mode, results.FallbackReason, len(results.Results))
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Notes search: query=%q mode=%s results=%d\n", query, mode, len(results.Results))
}

func notesSearchMatchLabel(result knowledge.NotesSearchResult) string {
	if len(result.MatchReasons) == 0 {
		return "-"
	}
	return strings.Join(result.MatchReasons, ",")
}

func notesSearchPathLabel(result knowledge.NotesSearchResult) string {
	if strings.TrimSpace(result.StructuralPath) == "" {
		return result.RelativePath
	}
	return result.RelativePath + " > " + result.StructuralPath
}

func notesSearchTextSourceLabel(result knowledge.NotesSearchResult) string {
	if strings.TrimSpace(result.TextSource) != "" {
		return result.TextSource
	}
	if result.MetadataOnly {
		return knowledge.TextSourceMetadataText
	}
	return "-"
}

func notesSearchExtractionLabel(result knowledge.NotesSearchResult) string {
	status := strings.TrimSpace(result.ExtractionStatus)
	if status == "" && result.MetadataOnly {
		status = knowledge.ExtractionStatusMetadataOnly
	}
	if status == "" {
		return "-"
	}
	if result.MetadataOnly && status != knowledge.ExtractionStatusMetadataOnly {
		return status + ":metadata"
	}
	return status
}

func notesSearchSnippetLabel(snippet string) string {
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > 96 {
		return snippet[:93] + "..."
	}
	if snippet == "" {
		return "-"
	}
	return snippet
}

func renderNotesReprocess(cmd *cobra.Command, result knowledge.ReprocessResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Notes reprocess queued: %s\n", result.Scope)
	fmt.Fprintf(cmd.OutOrStdout(), "Pipeline: %s\n", result.PipelineKey)
	fmt.Fprintf(cmd.OutOrStdout(), "Queued: %d\n", result.Queued)
	fmt.Fprintf(cmd.OutOrStdout(), "Skipped: %d\n", result.Skipped)
	if len(result.Items) == 0 {
		return
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tPREVIOUS\tNODE\tPROJECT\tCLASS\tPATH")
	for _, item := range result.Items {
		status := item.KnowledgePipelineStatus.Status
		if item.SkippedReason != "" {
			status = "skipped:" + item.SkippedReason
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", status, dashIfEmpty(item.PreviousExtractionState), item.SourceNodeKey, item.ProjectID, item.FileClass, item.RelativePath)
	}
	_ = tw.Flush()
}

func renderNotesRootReconcile(cmd *cobra.Command, result knowledge.SourceRootReconcileResult) {
	mode := "applied"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Notes roots reconcile: %s\n", mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Candidates: %d\n", len(result.Candidates))
	fmt.Fprintf(cmd.OutOrStdout(), "Registered: %d\n", len(result.SourceRoots))
	if !result.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Applied: %d\n", result.Applied)
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Skipped: %d\n", len(result.Skipped))
		for _, skipped := range result.Skipped {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s: %s\n", skipped.Origin, strings.TrimSpace(skipped.OriginRef), skipped.Reason)
		}
	}
}

func renderNotesObjectReconcile(cmd *cobra.Command, result knowledge.KnowledgeObjectReconcileResult) {
	mode := "applied"
	if result.DryRun {
		mode = "dry-run"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Notes objects reconcile: %s\n", mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Candidates: %d\n", len(result.Candidates))
	fmt.Fprintf(cmd.OutOrStdout(), "Objects: %d\n", len(result.KnowledgeObjects))
	if !result.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Applied: %d\n", result.Applied)
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Skipped: %d\n", len(result.Skipped))
		for _, skipped := range result.Skipped {
			fmt.Fprintf(cmd.OutOrStdout(), "- %s %s: %s\n", skipped.Origin, strings.TrimSpace(skipped.OriginRef), skipped.Reason)
		}
	}
}

func renderNotesProjectionStatus(cmd *cobra.Command, status notesprojection.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM notes projection\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Root: %s\n", status.ProjectionRoot)
	fmt.Fprintf(cmd.OutOrStdout(), "Exists: %t\n", status.Exists)
	fmt.Fprintf(cmd.OutOrStdout(), "Read-only: %t\n", status.ReadOnly)
	fmt.Fprintf(cmd.OutOrStdout(), "Raw writes supported: %t\n", status.RawWritesSupported)
	if status.LastRebuildAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last rebuild: %s\n", status.LastRebuildAt.Format(time.RFC3339))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Last rebuild: -\n")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Entries: %d materialized=%d missing=%d skipped=%d\n",
		status.Counts.Entries, status.Counts.Materialized, status.Counts.Missing, status.Counts.Skipped)
	if len(status.Findings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Findings:")
		for _, finding := range status.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s %s\n", finding.Severity, finding.Kind, finding.Summary)
		}
	}
}

func renderNotesProjectionRebuild(cmd *cobra.Command, result notesprojection.RebuildResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM notes projection rebuilt\n")
	if result.DryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Mode: dry-run\n")
	}
	renderNotesProjectionStatus(cmd, result.Status)
	if result.ManifestEntriesTruncated {
		fmt.Fprintln(cmd.OutOrStdout(), "Manifest entries omitted from response; rerun with --include-all to return them.")
	}
	if len(result.Changes) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Changes:")
		limit := len(result.Changes)
		if limit > 20 {
			limit = 20
		}
		for _, change := range result.Changes[:limit] {
			source := ""
			if change.Source != "" {
				source = " <- " + change.Source
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s%s [%s]\n", change.Action, change.Path, source, change.Status)
		}
		if len(result.Changes) > limit {
			fmt.Fprintf(cmd.OutOrStdout(), "  ... %d more changes\n", len(result.Changes)-limit)
		}
		if result.ChangesTruncated {
			fmt.Fprintln(cmd.OutOrStdout(), "  ... response truncated; use --include-all for the full list.")
		}
	}
	if result.FindingsTruncated {
		fmt.Fprintln(cmd.OutOrStdout(), "Findings truncated; use --include-all for the full list.")
	}
}
