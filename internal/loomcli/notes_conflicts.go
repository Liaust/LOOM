package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/spf13/cobra"
	"io"
	"loom.local/loom/internal/notesworkspacesync"
	"os"
	"time"
)

func newNotesConflictsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{Use: "conflicts", Short: "Review Notes text conflicts and submit source-checked resolutions"}
	var after string
	list := &cobra.Command{Use: "list", Short: "List unresolved source conflicts", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		c, e := resolveCommandContext(opts)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		result, e := c.Client.ListNotesConflicts(ctx, c.CorrelationID, after)
		if e != nil {
			return e
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		for _, item := range result.Data.Items {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s\n", item.ID, item.Status, item.Path)
		}
		if result.Data.Next != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Next: loom notes conflicts list --after %s\n", result.Data.Next)
		}
		return nil
	}}
	list.Flags().StringVar(&after, "after", "", "continue after the returned cursor")
	show := &cobra.Command{Use: "show <conflict-id>", Short: "Compare the original, device and current source text", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, e := resolveCommandContext(opts)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		result, e := c.Client.ShowNotesConflict(ctx, c.CorrelationID, args[0])
		if e != nil {
			return e
		}
		v := result.Data
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(v)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s [%s]\nReview: %s\n\nORIGINAL\n%s\n\nDEVICE\n%s\n\nSOURCE\n%s\n\nResolve: loom notes conflicts resolve %s --review %s --choice source|device|merge --yes\n", v.Path, v.Status, v.Review, v.Base, v.Device, v.Source, v.ID, v.Review)
		return nil
	}}
	var input notesworkspacesync.ResolveConflictInput
	var file string
	resolve := &cobra.Command{Use: "resolve <conflict-id>", Short: "Submit an explicit choice; acceptance is completed by the workspace worker", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		input.ID = args[0]
		if input.Choice == "merge" {
			if file == "" {
				return fmt.Errorf("pass --file for the merged text, or --file - for stdin")
			}
			var reader io.Reader = cmd.InOrStdin()
			if file != "-" {
				f, e := os.Open(file)
				if e != nil {
					return e
				}
				defer f.Close()
				reader = f
			}
			bytes, e := io.ReadAll(io.LimitReader(reader, notesworkspacesync.MaxBytes+1))
			if e != nil {
				return e
			}
			input.Text = string(bytes)
		} else if file != "" {
			return fmt.Errorf("--file is only used with --choice merge")
		}
		if input.Validate() != nil {
			return fmt.Errorf("pass an exact --review, --choice source|device|merge and --yes")
		}
		c, e := resolveCommandContext(opts)
		if e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
		defer cancel()
		result, e := c.Client.ResolveNotesConflict(ctx, c.CorrelationID, input)
		if e != nil {
			return e
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Resolution %s: %s (%s)\n", result.Data.ID, result.Data.Status, result.Data.Reason)
		return nil
	}}
	resolve.Flags().StringVar(&input.Review, "review", "", "exact review token from conflicts show")
	resolve.Flags().StringVar(&input.Choice, "choice", "", "source, device or merge")
	resolve.Flags().StringVar(&file, "file", "", "merged UTF-8 text file; - reads stdin")
	resolve.Flags().BoolVar(&input.Confirm, "yes", false, "confirm this reviewed resolution")
	cmd.AddCommand(list, show, resolve)
	return cmd
}
