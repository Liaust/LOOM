package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/knowledge"
)

func newNotesReadCommand(opts *options) *cobra.Command {
	var input knowledge.FileReadInput
	cmd := &cobra.Command{Use: "read <file-id-or-path>", Short: "Read the current managed source file", Long: "Read current source or published extraction text. Human processing follow-ups go to stderr; --plain keeps text-only output and --json includes structured processing context.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		input.Ref = args[0]
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
		defer cancel()
		envelope, err := commandCtx.Client.ReadKnowledgeFile(ctx, commandCtx.CorrelationID, input)
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("notes.read_failed", "knowledge", input.Ref, "Could not read the current file.", err))
		}
		result := envelope.Data
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		}
		if result.Text == "" && result.Status != "source_text" {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result.Path, result.Status)
			if result.Processing != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Publication: %s\nInspect: %s\n", result.Processing.Publication, result.Processing.InspectCommand)
			}
			return nil
		}
		fmt.Fprint(cmd.OutOrStdout(), result.Text)
		if result.Truncated {
			fmt.Fprintf(cmd.ErrOrStderr(), "\nTruncated; continue with --offset %d\n", result.NextOffset)
		}
		if result.Processing != nil && !opts.plainOutput {
			fmt.Fprintf(cmd.ErrOrStderr(), "\nPublication: %s\nInspect: %s\n", declarationDisplay(result.Processing.Publication), declarationDisplay(result.Processing.InspectCommand))
		}
		return nil
	}}
	cmd.Flags().Int64Var(&input.Offset, "offset", 0, "start at this byte offset")
	cmd.Flags().IntVar(&input.Limit, "limit", knowledge.DefaultFileReadBytes, "maximum returned text bytes (up to 1 MiB)")
	return cmd
}

func newNotesFilesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "files", Short: "Maintain current file metadata without reprocessing bytes"}
	var input knowledge.FileIdentityReconcileInput
	reconcile := &cobra.Command{Use: "reconcile", Short: "Preview or adopt one bounded page of existing source metadata", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		id := correlation.Normalize(opts.correlationID)
		cfg, client, err := commandClient(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := client.ReconcileKnowledgeFiles(ctx, id, input)
		if err != nil {
			return renderError(cmd, opts, id, loomerrors.Wrap("notes.files_reconcile_failed", "knowledge", cfg.SocketPath, "File metadata reconciliation failed.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Files observed: %d; adopted: %d; preview: %t\n", len(out.Data.Mappings), out.Data.Applied, out.Data.DryRun)
		for _, m := range out.Data.Mappings {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", m.Presence, m.Path, m.Reason)
		}
		if out.Data.NextAfter != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Continue with --after %q\n", out.Data.NextAfter)
		}
		return nil
	}}
	reconcile.Flags().IntVar(&input.Limit, "limit", 100, "maximum metadata entries (up to 200)")
	reconcile.Flags().StringVar(&input.After, "after", "", "continuation from the previous page")
	reconcile.Flags().BoolVar(&input.Confirm, "yes", false, "adopt reviewed metadata; no source/derived payload deletion")
	cmd.AddCommand(reconcile)
	var retained knowledge.RetainedFileOutputsInput
	inventory := &cobra.Command{Use: "retained", Short: "Inventory historical derived outputs without deleting them", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		out, err := commandCtx.Client.RetainedKnowledgeFileOutputs(ctx, commandCtx.CorrelationID, retained)
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, err)
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(out)
		}
		for _, item := range out.Data.Outputs {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s artifacts=%d text_bytes=%d chunks=%d vectors=%d\n", item.FileID, item.VersionID, item.Artifacts, item.TextBytes, item.Chunks, item.Vectors)
		}
		fmt.Fprintln(cmd.OutOrStdout(), out.Data.Notice)
		if out.Data.NextAfter != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Continue with --after %q\n", out.Data.NextAfter)
		}
		return nil
	}}
	inventory.Flags().IntVar(&retained.Limit, "limit", 100, "maximum historical versions (up to 200)")
	inventory.Flags().StringVar(&retained.After, "after", "", "continuation from the previous page")
	cmd.AddCommand(inventory)
	return cmd
}

func renderFileSearchResults(cmd *cobra.Command, opts *options, result knowledge.FileSearchResultSet) {
	if len(result.Results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No matching current files.")
	}
	for _, file := range result.Results {
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), file.FileID)
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\n  %s\n  %s", file.Title, file.Path, file.FileID)
		if file.Project != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " | %s", file.Project)
		}
		fmt.Fprintln(cmd.OutOrStdout())
		if file.Excerpt != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", file.Excerpt)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", file.Content)
		}
	}
	if result.Notice != "" {
		fmt.Fprintln(cmd.OutOrStdout(), result.Notice)
	}
}
