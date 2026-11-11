package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
)

func addProjectActionCommand(parent *cobra.Command, opts *options) {
	var input pc.ProjectActionRequest
	var yes, noWait bool
	cmd := &cobra.Command{Use: "run-action <project-ref> <application> <action>", Short: "Run a declared action with its installed package, credentials and state", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		if !input.Check && !yes {
			return fmt.Errorf("--yes confirms the task-authorized named action; --status only observes")
		}
		input.ProjectRef, input.Resource, input.Action = args[0], pc.ResourceKey(args[1]), args[2]
		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		for {
			env, err := client.RunProjectAction(ctx, cid, input)
			if err != nil {
				return renderDeclarationFailure(cmd, opts, cid, err, nil, args[0])
			}
			x := env.Data
			if input.Check || noWait || (x.State != "pending" && x.State != "awaiting_finalization") {
				if opts.jsonOutput {
					if err := json.NewEncoder(cmd.OutOrStdout()).Encode(x); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s / %s / %s: %s\n", x.ProjectID, x.Resource, x.Action, x.State)
					if x.ErrorCode != "" {
						fmt.Fprintln(cmd.OutOrStdout(), "Cause: "+x.ErrorCode)
					}
					if x.CallRef != "" {
						fmt.Fprintln(cmd.OutOrStdout(), "Call: "+x.CallRef)
					}
				}
				if x.State == "failed" || x.State == "uncertain" {
					return fmt.Errorf("action held; no automatic replay")
				}
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}}
	cmd.Flags().StringVar(&input.NodeRef, "node", "", "owning node")
	cmd.Flags().StringVar(&input.Run, "run", "", "intent label for a deliberately separate invocation; default runs once per installed generation")
	cmd.Flags().BoolVar(&input.Check, "status", false, "read-only action observation; never starts or resumes work")
	cmd.Flags().BoolVar(&input.ReleaseFailed, "release-failed", false, "acknowledge a reviewed failed action and release its hold; never reruns or resumes")
	cmd.Flags().BoolVar(&yes, "yes", false, "perform the already-authorized action")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "return the current phase instead of awaiting completion")
	parent.AddCommand(cmd)
}
