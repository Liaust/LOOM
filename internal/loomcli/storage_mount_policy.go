package loomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/nodeagent/macmount"
)

func newStorageMountPolicyCommand(opts *options) *cobra.Command {
	var local bool
	cmd := &cobra.Command{
		Use:   "mount-policy",
		Short: "Manage local LOOM Main storage mount desired state",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if !local {
				return errors.New("mount-policy is a local workspace-node command; pass --local")
			}
			return nil
		},
	}
	cmd.PersistentFlags().BoolVar(&local, "local", false, "operate on this local machine's node-agent mount policy")
	cmd.AddCommand(newStorageMountPolicyStatusCommand(opts))
	cmd.AddCommand(newStorageMountPolicyEnableCommand(opts))
	cmd.AddCommand(newStorageMountPolicyDisableCommand(opts))
	cmd.AddCommand(newStorageMountPolicyRepairCommand(opts))
	return cmd
}

func newStorageMountPolicyStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local LOOM Main storage mount desired and actual state",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := macmount.NewManager("")
			if err != nil {
				return err
			}
			status, err := manager.Status(cmd.Context())
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			renderStorageMountPolicyStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
}

func newStorageMountPolicyEnableCommand(opts *options) *cobra.Command {
	var repair bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Set local LOOM Main storage desired state to mounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := macmount.NewManager("")
			if err != nil {
				return err
			}
			status, err := manager.Enable(cmd.Context())
			if err != nil {
				return err
			}
			if repair {
				ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
				defer cancel()
				status, err = manager.RepairOnce(ctx, macmount.RepairOptions{Force: true})
				if err != nil {
					return err
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			renderStorageMountPolicyStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "attempt an immediate mount repair after enabling")
	return cmd
}

func newStorageMountPolicyDisableCommand(opts *options) *cobra.Command {
	var keepMounted bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Set local LOOM Main storage desired state to unmounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := macmount.NewManager("")
			if err != nil {
				return err
			}
			status, err := manager.Disable(cmd.Context(), !keepMounted)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			renderStorageMountPolicyStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepMounted, "keep-mounted", false, "only update policy; do not unmount current SMB mounts")
	return cmd
}

func newStorageMountPolicyRepairCommand(opts *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "repair-once",
		Short: "Run one non-interactive local storage mount repair cycle",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := macmount.NewManager("")
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Second)
			defer cancel()
			status, err := manager.RepairOnce(ctx, macmount.RepairOptions{Force: force})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			renderStorageMountPolicyStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "ignore mount-repair backoff for this run")
	return cmd
}

func renderStorageMountPolicyStatus(writer io.Writer, status macmount.Status) {
	fmt.Fprintf(writer, "LOOM Main storage mount policy: desired=%s actual=%s\n", status.DesiredState, status.ActualState)
	fmt.Fprintf(writer, "Protocol: %s\n", status.Protocol)
	fmt.Fprintf(writer, "Mount path: %s\n", status.MountPath)
	if strings.TrimSpace(status.FinderMountPath) != "" {
		fmt.Fprintf(writer, "Finder mount: %s\n", status.FinderMountPath)
	}
	fmt.Fprintf(writer, "SMB: //%s@%s/%s\n", status.User, status.Host, status.Share)
	if strings.TrimSpace(status.LastErrorMessage) != "" {
		fmt.Fprintf(writer, "Last error: %s (%s)\n", status.LastErrorMessage, status.LastErrorCategory)
	}
	if status.NextRetryAt != nil {
		fmt.Fprintf(writer, "Next retry: %s\n", status.NextRetryAt.Format(time.RFC3339))
	}
	fmt.Fprintf(writer, "Policy: %s\n", status.PolicyPath)
	fmt.Fprintf(writer, "Status: %s\n", status.StatusPath)
	if len(status.Checks) == 0 {
		return
	}
	fmt.Fprintln(writer, "\nChecks:")
	table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "STATUS\tCHECK\tSUMMARY\tDETAIL")
	for _, check := range status.Checks {
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", check.Status, check.ID, check.Summary, check.Detail)
	}
	_ = table.Flush()
}
