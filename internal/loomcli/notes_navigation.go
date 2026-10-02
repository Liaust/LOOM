package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"loom.local/loom/internal/knowledge"
)

func newNotesLocateCommand(opts *options) *cobra.Command {
	var input knowledge.NotesPassageInput
	cmd := &cobra.Command{Use: "locate <chunk-id>", Short: "Locate the verified current workspace file for an exact citation", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input.KnowledgeChunkID = args[0]
			if err := knowledge.ValidateNotesPassageInput(input); err != nil {
				return err
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.LocateKnowledgeNotesPassage(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope.Data)
			}
			out := envelope.Data
			fmt.Fprintf(cmd.OutOrStdout(), "Workspace navigation: %s (%s)\n", out.Status, out.Reason)
			if out.Binding != nil {
				b := out.Binding
				fmt.Fprintf(cmd.OutOrStdout(), "Workspace: %s\nCollection: %s\nFile: %s\nPath: %s\nRevision: %s\nReference only: %t\n", b.Workspace, b.Collection, b.FileID, b.Path, b.NativeRevision, !b.Writable)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Local search: %s\nSync: %s\nLexical index: %s\nSemantic index: %s\n", out.LocalSearch, out.Sync, out.LexicalIndex, out.SemanticIndex)
			fmt.Fprintln(cmd.OutOrStdout(), "Open only after the local file matches the returned binding and revision. Local offline search covers device files; canonical acknowledgement and lexical/semantic indexing complete separately.")
			return nil
		}}
	cmd.Flags().StringVar(&input.KnowledgeObjectID, "object", "", "exact knowledge object ID")
	cmd.Flags().StringVar(&input.KnowledgeObjectVersionID, "version", "", "exact retained knowledge version ID")
	cmd.Flags().StringVar(&input.SourceHash, "source-hash", "", "exact sha256 source content address")
	addNotesSourceLifecycleFlag(cmd, &input.SourceLifecycle)
	return cmd
}

func notesNavigationFollowupLine(result knowledge.NotesSearchResult, number int) string {
	line := notesSearchFollowupLine(result, number)
	if line == "" {
		return ""
	}
	return strings.Replace(strings.Replace(line, "Follow-up [", "Workspace [", 1), "notes passage get ", "notes passage locate ", 1)
}
