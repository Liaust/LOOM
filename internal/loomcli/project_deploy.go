package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	pc "loom.local/loom/internal/projectcontracts"
)

func addProjectDeployCommand(parent *cobra.Command, opts *options) {
	addProjectActionCommand(parent, opts)
	addProjectCredentialImportCommand(parent, opts)
	var node, resource string
	var check, yes, details, noWait bool
	command := &cobra.Command{Use: "deploy <project-folder-or-ref>", Short: "Build and deploy one declared application through its existing owner", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !check && !yes {
			return fmt.Errorf("use --check to inspect without effects, or --yes for an already-authorized deployment")
		}
		ref := args[0]
		if node == "" && !filepath.IsAbs(ref) {
			if info, err := os.Stat(ref); err == nil && info.IsDir() {
				ref, err = filepath.Abs(ref)
				if err != nil {
					return err
				}
			}
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Minute)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		env, err := client.DeployProject(ctx, cid, pc.ProjectDeploymentRequest{ProjectRef: ref, NodeRef: node, Resource: pc.ResourceKey(resource), Check: check})
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil, ref)
		}
		if !check && !noWait && env.Data.BuildJobID != "" && env.Data.Operation != nil {
			for env.Data.Operation.State == pc.DeclarationOperationPartial || env.Data.Operation.State == pc.DeclarationOperationRunning || env.Data.Operation.State == pc.DeclarationOperationQueued {
				pending := false
				for _, issue := range env.Data.Operation.Errors {
					if issue.Code == pc.DeclarationOwnerPending {
						pending = true
					}
				}
				if len(env.Data.Operation.Errors) > 0 && !pending {
					break
				}
				select {
				case <-ctx.Done():
					return fmt.Errorf("wait ended; Main continues build %s for operation %s", env.Data.BuildJobID, env.Data.Operation.OperationID)
				case <-time.After(time.Second):
				}
				status, e := client.GetProjectDeclarationOperation(ctx, cid, env.Data.Operation.OperationID)
				if e != nil {
					return renderDeclarationFailure(cmd, opts, cid, e, nil, ref)
				}
				env.Data.Operation = &status.Data
				env.Data.State = string(status.Data.State)
			}
		}
		emit := func(cmd *cobra.Command) error {
			if opts.jsonOutput || details {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(projectDeploymentOutput(env.Data, details))
			}
			x := env.Data
			fmt.Fprintf(cmd.OutOrStdout(), "%s / %s: %s\nNode: %s\nNetwork: %s\n", x.ProjectID, x.Resource, x.State, x.NodeID, x.Scope)
			if x.Operation != nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Operation: "+x.Operation.OperationID)
			}
			if x.Builder != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Builder: %s; program: %s\n", x.Builder, x.Program)
			}
			if x.BuildJobID != "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Build: "+x.BuildJobID)
			}
			return nil
		}
		if env.Data.Operation != nil {
			for _, issue := range env.Data.Operation.Errors {
				if !pc.DeclarationWaitingForOwner(issue.Code, issue.CauseCode) {
					return renderOwnedCLIError(cmd, fmt.Errorf("deployment stopped: %s: %s", issue.Code, issue.CauseCode), emit)
				}
			}
		}
		return emit(cmd)
	}}
	command.Flags().StringVar(&node, "node", "", "owning node; never resolves a remote path on this machine")
	command.Flags().StringVar(&resource, "resource", "", "application key; optional when exactly one exists")
	command.Flags().BoolVar(&check, "check", false, "read-only source and policy compatibility check; no build or credential acquisition")
	command.Flags().BoolVar(&yes, "yes", false, "perform the task-authorized deployment within existing policy")
	command.Flags().BoolVar(&details, "details", false, "include generated plan and operation details")
	command.Flags().BoolVar(&noWait, "no-wait", false, "return the accepted operation while Main owns build and deployment continuation")
	parent.AddCommand(command)
}

func projectDeploymentOutput(x pc.ProjectDeploymentResult, details bool) any {
	if details {
		return x
	}
	out := map[string]any{"project_id": x.ProjectID, "node_id": x.NodeID, "resource": x.Resource, "state": x.State, "network": x.Network, "network_scope": x.Scope}
	if x.Builder != "" {
		out["builder"], out["program"] = x.Builder, x.Program
	}
	if x.BuildJobID != "" {
		out["build_job_id"] = x.BuildJobID
	}
	if x.Plan != nil {
		out["check"] = "compatibility_only"
	}
	if x.Operation != nil {
		out["operation_id"] = x.Operation.OperationID
		if len(x.Operation.Errors) > 0 {
			out["errors"] = x.Operation.Errors
		}
	}
	return out
}

func addProjectCredentialImportCommand(parent *cobra.Command, opts *options) {
	var input pc.ProjectCredentialImportRequest
	var yes bool
	cmd := &cobra.Command{Use: "import-credential <project-ref> <application>", Short: "Bind an existing protected Google OAuth credential without repeating consent", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return fmt.Errorf("--yes confirms the already-authorized protected import; this does not perform OAuth consent")
		}
		input.ProjectRef, input.Resource = args[0], pc.ResourceKey(args[1])
		ctx, cancel := context.WithTimeout(cmd.Context(), 80*time.Second)
		defer cancel()
		cid, client, err := declarationClient(cmd, opts)
		if err != nil {
			return err
		}
		result, err := client.ImportProjectCredential(ctx, cid, input)
		if err != nil {
			return renderDeclarationFailure(cmd, opts, cid, err, nil, args[0])
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result.Data)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Runtime credential: "+result.Data.Reference)
		return nil
	}}
	cmd.Flags().StringVar(&input.NodeRef, "node", "", "owning node")
	cmd.Flags().StringVar(&input.SourcePath, "source-file", "", "existing private file on the owning node; no secret values enter the request")
	cmd.Flags().StringVar(&input.Account, "account", "", "expected provider account verified inside the protected import")
	cmd.Flags().StringSliceVar(&input.Scopes, "scope", nil, "exact expected OAuth scopes; repeatable")
	cmd.Flags().BoolVar(&yes, "yes", false, "confirm task-authorized protected credential import")
	for _, flag := range []string{"source-file", "account", "scope"} {
		_ = cmd.MarkFlagRequired(flag)
	}
	parent.AddCommand(cmd)
}
