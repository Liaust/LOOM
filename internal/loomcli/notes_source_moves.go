package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"loom.local/loom/internal/notesworkspacesync"
	"time"
)

func newNotesSourceMovesCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "moves", Short: "Review a Main-side move and reconcile its existing local replicas"}
	for _, apply := range []bool{false, true} {
		input := notesworkspacesync.SourceMoveInput{}
		name := "review"
		if apply {
			name = "apply"
		}
		child := &cobra.Command{Use: name + " <old-vault-path> <new-vault-path>", Short: "Verify exact source absence and destination binding; preserve local edits", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			input.From, input.To = args[0], args[1]
			if input.Validate() != nil || apply && !input.Confirm {
				return fmt.Errorf("apply requires the exact --review and --yes; review requires only two workspace paths")
			}
			c, err := resolveCommandContext(opts)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			result, err := c.Client.NotesSourceMove(ctx, c.CorrelationID, input)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
			}
			v := result.Data
			fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s\nStatus: %s\nContent differs: %t\nReview: %s\n", v.Previous.Path, v.Binding.Path, v.Status, v.ContentChanged, v.Review)
			if !apply {
				fmt.Fprintln(cmd.OutOrStdout(), "Apply with these same paths, --review <token> and --yes. A published move is not a device receipt.")
			}
			return nil
		}}
		if apply {
			child.Flags().StringVar(&input.Review, "review", "", "exact current move-review token")
			child.Flags().BoolVar(&input.Confirm, "yes", false, "confirm this exact reviewed source move")
		}
		cmd.AddCommand(child)
	}
	return cmd
}
