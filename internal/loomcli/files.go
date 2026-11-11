package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/workspaceactions"
	"loom.local/loom/internal/workspacefiles"
)

func newFilesCommand(opts *options) *cobra.Command {
	command := &cobra.Command{Use: "files", Short: "Browse actual files without indexing or Notes enrollment"}
	var applicationProject string
	var applicationCursor, applicationLimit int
	applications := &cobra.Command{Use: "application-data", Short: "List approved read-only application content views", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cc, e := resolveCommandContext(opts)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		result, e := cc.Client.ApplicationFileRoots(ctx, cc.CorrelationID, applicationProject, applicationCursor, applicationLimit)
		if e != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, e)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		for _, root := range result.Data.Roots {
			fmt.Fprintf(cmd.OutOrStdout(), "%s\n  Root: %s\n  Source: %s\n  Available: %t %s\n", root.Label, root.Ref, root.SourcePath, root.Available, root.Reason)
		}
		if result.Data.NextCursor > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Next page: --cursor %d\n", result.Data.NextCursor)
		}
		return nil
	}}
	applications.Flags().StringVar(&applicationProject, "project", "", "exact registered project ID")
	applications.Flags().IntVar(&applicationCursor, "cursor", 0, "continuation from the preceding page")
	applications.Flags().IntVar(&applicationLimit, "limit", 100, "maximum approved views per page")
	command.AddCommand(applications)
	var operation workspaceactions.Input
	review := &cobra.Command{Use: "review <path>", Short: "Check project lifecycle and managed-path requirements without changing files", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		operation.Path = args[0]
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := cc.Client.FileOperationReview(ctx, cc.CorrelationID, operation)
		if err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result.Data.Decision, result.Data.Message)
		if p := result.Data.Project; p != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Project: %s (%s)\nCurrent path: %s\n", p.Name, p.ID, p.Root)
		}
		return nil
	}}
	review.Flags().StringVar(&operation.Root, "root", "box", "configured source root")
	review.Flags().StringVar(&operation.Action, "action", "inspect", "inspect, move, delete, copy or edit")
	review.Flags().StringVar(&operation.ToRoot, "to-root", "", "configured destination root")
	review.Flags().StringVar(&operation.ToPath, "to-path", "", "exact destination including final name")
	command.AddCommand(review)
	relocate := &cobra.Command{Use: "relocate", Short: "Review and apply a same-filesystem retained Storage relocation"}
	for _, action := range []string{"plan", "status", "apply", "cancel"} {
		var hash string
		var yes, operator bool
		use, arguments := action+" <id>", cobra.ExactArgs(1)
		if action == "plan" {
			use, arguments = "plan <storage-source> <storage-destination>", cobra.ExactArgs(2)
		}
		child := &cobra.Command{Use: use, Args: arguments, RunE: func(cmd *cobra.Command, args []string) error {
			if (action == "apply" || action == "cancel") && (!yes || hash == "") {
				return fmt.Errorf("pass the exact --plan-hash and --yes from a reviewed plan")
			}
			if operator {
				if err := runRetainedOperator(cmd, opts, action, args, hash); err != nil {
					return renderFileCommandError(cmd, opts, opts.correlationID, loomerrors.Wrap("files.operator_refused", "files", "retained-move", err.Error(), nil))
				}
				return nil
			}
			cc, err := resolveCommandContext(opts)
			if err != nil {
				return renderFileCommandError(cmd, opts, opts.correlationID, err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 4*time.Minute)
			defer cancel()
			input := map[string]any{"action": action}
			if action == "plan" {
				input["from"], input["to"] = args[0], args[1]
			} else {
				input["id"] = args[0]
			}
			if action == "apply" || action == "cancel" {
				input["plan_hash"], input["confirm"] = hash, yes
			}
			result, err := cc.Client.RetainedFileMove(ctx, cc.CorrelationID, input)
			if err != nil {
				return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}}
		if action == "apply" || action == "cancel" {
			child.Flags().StringVar(&hash, "plan-hash", "", "exact reviewed plan hash")
			child.Flags().BoolVar(&yes, "yes", false, "confirm this exact reviewed action; cancellation requires proof that nothing moved")
		}
		child.Flags().BoolVar(&operator, "operator", false, "local root-only migration owner; preserve legacy ownership without changing permissions")
		relocate.AddCommand(child)
	}
	command.AddCommand(relocate)
	roots := &cobra.Command{Use: "roots", Short: "List configured original-file roots", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := cc.Client.FileRoots(ctx, cc.CorrelationID)
		if err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tNODE\tAVAILABLE\tSOURCE")
		for _, root := range result.Data {
			fmt.Fprintf(writer, "%s\t%s\t%t\t%s\n", root.Ref, root.Node, root.Available, root.SourcePath)
		}
		return writer.Flush()
	}}
	var listInput workspacefiles.ListInput
	list := &cobra.Command{Use: "ls [path]", Aliases: []string{"list"}, Short: "List one directory of real files", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		listInput.Path = ""
		if len(args) > 0 {
			listInput.Path = args[0]
		}
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := cc.Client.FileEntries(ctx, cc.CorrelationID, listInput)
		if err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "KIND\tBYTES\tPATH")
		for _, item := range result.Data.Entries {
			fmt.Fprintf(writer, "%s\t%d\t%s\n", item.Kind, item.Size, item.RelativePath)
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if result.Data.HasMore {
			fmt.Fprintf(cmd.OutOrStdout(), "Next page: --cursor %s\n", result.Data.NextCursor)
		}
		return nil
	}}
	list.Flags().StringVar(&listInput.Root, "root", "box", "configured file root")
	list.Flags().IntVar(&listInput.Limit, "limit", workspacefiles.DefaultLimit, "maximum entries per page")
	list.Flags().StringVar(&listInput.Cursor, "cursor", "", "continuation from the preceding page")
	list.Flags().BoolVar(&listInput.ShowIgnored, "show-ignored", false, "show rebuildable folders; mandatory restrictions remain")
	var statRoot string
	stat := &cobra.Command{Use: "stat <path>", Short: "Inspect a real file or directory", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := cc.Client.FileStat(ctx, cc.CorrelationID, statRoot, args[0])
		if err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		item := result.Data
		fmt.Fprintf(cmd.OutOrStdout(), "%s\nKind: %s\nBytes: %d\nModified: %s\nRevision (weak): %s\n", item.RelativePath, item.Kind, item.Size, item.ModifiedAt.Format(time.RFC3339Nano), item.Revision)
		return nil
	}}
	stat.Flags().StringVar(&statRoot, "root", "box", "configured file root")
	var explainRoot string
	explain := &cobra.Command{Use: "explain <path>", Short: "Explain source access and descriptor intent without claiming delivery or protection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		result, err := cc.Client.FileExplain(ctx, cc.CorrelationID, explainRoot, args[0])
		if err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		item := result.Data
		enrichment, protect := "unknown", "unknown"
		if item.Intent.Enrichment != nil {
			enrichment = "none"
			if len(*item.Intent.Enrichment) > 0 {
				enrichment = strings.Join(*item.Intent.Enrichment, ", ")
			}
		}
		if item.Intent.Protect != nil {
			protect = fmt.Sprint(*item.Intent.Protect)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\nKind: %s\nRead-only: %t\nVerification: %s\nIntent: %s\nSearch intent: %s\nEnrichment intent: %s\nProtect intent: %s\n", item.SourcePath, item.Source.Kind, item.ReadOnly, item.Verification, item.Intent.State, item.Intent.Search, enrichment, protect)
		fields := make([]string, 0, len(item.Intent.Sources))
		for field := range item.Intent.Sources {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			source := item.Intent.Sources[field]
			qualifier := ""
			if source.Default {
				qualifier = " (default)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s rule: %s :: %s [path=%q]%s\n", field, source.Descriptor, source.Field, source.Pattern, qualifier)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Root registration: %s\nDevice-local availability: %s\nApplied config: %s\nBackup coverage: %s\n%s\n%s\nNext: %s\n", item.RootRegistration, item.DeviceLocalAvailability, item.AppliedConfig, item.BackupCoverage, item.Intent.Reason, item.Qualification, item.NextAction)
		return nil
	}}
	explain.Flags().StringVar(&explainRoot, "root", "box", "configured file root (box or archive)")
	var readRoot string
	var archiveRoot, member, memberRevision string
	members := &cobra.Command{Use: "archive-members <path>", Short: "List safe attachment members of one approved application ZIP", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		cc, e := resolveCommandContext(opts)
		if e != nil {
			return e
		}
		result, e := cc.Client.FileArchiveMembers(cmd.Context(), cc.CorrelationID, archiveRoot, args[0])
		if e != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, e)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
	}}
	members.Flags().StringVar(&archiveRoot, "root", "", "exact approved application root from files application-data")
	command.AddCommand(members)
	var offset, length int64
	read := &cobra.Command{Use: "read <path>", Short: "Stream original bytes, optionally a byte range", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if opts.jsonOutput {
			return renderFileCommandError(cmd, opts, opts.correlationID, loomerrors.New("files.binary_json_unsupported", "files", "read", "File content is a byte stream; omit --json or use files stat for metadata."))
		}
		cc, err := resolveCommandContext(opts)
		if err != nil {
			return renderFileCommandError(cmd, opts, opts.correlationID, err)
		}
		if member != "" {
			if memberRevision == "" || offset != 0 || length != 0 {
				return fmt.Errorf("member reads require --revision from archive-members and cannot use byte ranges")
			}
			return cc.Client.ReadArchiveMember(cmd.Context(), cc.CorrelationID, readRoot, args[0], member, memberRevision, cmd.OutOrStdout())
		}
		if err := cc.Client.ReadFile(cmd.Context(), cc.CorrelationID, readRoot, args[0], offset, length, cmd.OutOrStdout()); err != nil {
			return renderFileCommandError(cmd, opts, cc.CorrelationID, err)
		}
		return nil
	}}
	read.Flags().StringVar(&readRoot, "root", "box", "configured file root")
	read.Flags().Int64Var(&offset, "offset", 0, "first byte to read")
	read.Flags().Int64Var(&length, "length", 0, "maximum bytes to read; zero streams to the end")
	read.Flags().StringVar(&member, "member", "", "exact member ID from archive-members; read-only decoded stream")
	read.Flags().StringVar(&memberRevision, "revision", "", "exact archive revision returned by archive-members")
	metadata := &cobra.Command{Use: "metadata", Short: "Explicit lightweight Markdown navigation refresh, without text indexing or embeddings"}
	for _, action := range []string{"start", "step", "status", "latest", "pause", "cancel", "retry"} {
		var root, relative string
		args := cobra.ExactArgs(1)
		use := action + " <refresh-id>"
		if action == "start" || action == "latest" {
			args = cobra.NoArgs
			use = action
		}
		child := &cobra.Command{Use: use, Args: args, RunE: func(cmd *cobra.Command, args []string) error {
			cc, e := resolveCommandContext(opts)
			if e != nil {
				return renderFileCommandError(cmd, opts, opts.correlationID, e)
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			result, e := cc.Client.FileMetadataRefresh(ctx, cc.CorrelationID, action, root, relative, id)
			if e != nil {
				return renderFileCommandError(cmd, opts, cc.CorrelationID, e)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\nState: %s\nScope: %s/%s\nCounts: %v\n%s\n", result.Data.ID, result.Data.State, result.Data.Root, result.Data.Path, result.Data.Counts, result.Data.Qualification)
			return nil
		}}
		child.Flags().StringVar(&root, "root", "box", "current Box root")
		if action == "start" {
			child.Flags().StringVar(&relative, "path", "", "visible Box folder; empty means Box")
		}
		metadata.AddCommand(child)
	}
	command.AddCommand(roots, list, stat, explain, read, metadata)
	return command
}

func renderFileCommandError(cmd *cobra.Command, opts *options, correlationID string, err error) error {
	return renderError(cmd, opts, correlation.Normalize(correlationID), err)
}
