package nodeagent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/nodeagent/macmount"
)

func newStorageMountCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage-mount",
		Short: "Manage local LOOM Main storage mount desired state",
	}
	cmd.AddCommand(newStorageMountStatusCommand(opts))
	cmd.AddCommand(newStorageMountEnableCommand(opts))
	cmd.AddCommand(newStorageMountDisableCommand(opts))
	cmd.AddCommand(newStorageMountRepairCommand(opts))
	cmd.AddCommand(newStorageMountCloudCommand(opts))
	return cmd
}

func newStorageMountCloudCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Manage local LOOM Cloud storage mount desired state",
	}
	cmd.AddCommand(newStorageMountCloudStatusCommand(opts))
	cmd.AddCommand(newStorageMountCloudEnableCommand(opts))
	cmd.AddCommand(newStorageMountCloudDisableCommand(opts))
	cmd.AddCommand(newStorageMountCloudRepairCommand(opts))
	return cmd
}

func newStorageMountStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local LOOM Main storage mount policy and actual state",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			status, err := manager.Status(cmd.Context())
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderStorageMountStatus(opts.out, status)
			return nil
		},
	}
}

func newStorageMountEnableCommand(opts *rootOptions) *cobra.Command {
	var repair bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Set LOOM Main storage desired state to mounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
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
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderStorageMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "attempt an immediate mount repair after enabling")
	return cmd
}

func newStorageMountDisableCommand(opts *rootOptions) *cobra.Command {
	var keepMounted bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Set LOOM Main storage desired state to unmounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			status, err := manager.Disable(cmd.Context(), !keepMounted)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderStorageMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepMounted, "keep-mounted", false, "only update policy; do not unmount current SMB mounts")
	return cmd
}

func newStorageMountRepairCommand(opts *rootOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "repair-once",
		Short: "Run one non-interactive storage mount repair cycle",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
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
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderStorageMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "ignore mount-repair backoff for this run")
	return cmd
}

func newStorageMountCloudStatusCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show local LOOM Cloud storage mount policy and actual state",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			status, err := manager.CloudStatus(cmd.Context())
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderMountStatus(opts.out, status)
			return nil
		},
	}
}

func newStorageMountCloudEnableCommand(opts *rootOptions) *cobra.Command {
	var repair bool
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Set LOOM Cloud storage desired state to mounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			status, err := manager.EnableCloud(cmd.Context())
			if err != nil {
				return err
			}
			if repair {
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
				defer cancel()
				status, err = manager.RepairCloudOnce(ctx, macmount.RepairOptions{Force: true})
				if err != nil {
					return err
				}
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&repair, "repair", false, "attempt an immediate cloud mount repair after enabling")
	return cmd
}

func newStorageMountCloudDisableCommand(opts *rootOptions) *cobra.Command {
	var keepMounted bool
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Set LOOM Cloud storage desired state to unmounted",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			status, err := manager.DisableCloud(cmd.Context(), !keepMounted)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepMounted, "keep-mounted", false, "only update policy; do not unmount current SMB mounts")
	return cmd
}

func newStorageMountCloudRepairCommand(opts *rootOptions) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "repair-once",
		Short: "Run one LOOM Cloud storage mount repair cycle",
		RunE: func(cmd *cobra.Command, args []string) error {
			manager, err := opts.storageMountManager()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			status, err := manager.RepairCloudOnce(ctx, macmount.RepairOptions{Force: force})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return renderJSON(opts.out, localSuccess(correlation.Normalize(opts.correlationID), status))
			}
			renderMountStatus(opts.out, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "ignore cloud mount-repair backoff for this run")
	return cmd
}

func (opts *rootOptions) storageMountManager() (macmount.Manager, error) {
	store, err := opts.store()
	if err != nil {
		return macmount.Manager{}, err
	}
	return macmount.NewManager(store.DataDir)
}

func renderStorageMountStatus(writer io.Writer, status macmount.Status) {
	renderMountStatus(writer, macmount.MountStatus{
		Name:                "LOOM Main storage mount",
		DesiredState:        status.DesiredState,
		ActualState:         status.ActualState,
		Protocol:            status.Protocol,
		MountPath:           status.MountPath,
		FinderMountPath:     status.FinderMountPath,
		Host:                status.Host,
		HostIP:              status.HostIP,
		ExpectedIP:          status.ExpectedIP,
		Share:               status.Share,
		User:                status.User,
		MountedPaths:        status.MountedPaths,
		Checks:              status.Checks,
		LastCheckAt:         status.LastCheckAt,
		LastSuccessfulMount: status.LastSuccessfulMount,
		LastErrorCategory:   status.LastErrorCategory,
		LastErrorMessage:    status.LastErrorMessage,
		NextRetryAt:         status.NextRetryAt,
		ConsecutiveFailures: status.ConsecutiveFailures,
	})
	fmt.Fprintf(writer, "Policy: %s\n", status.PolicyPath)
	fmt.Fprintf(writer, "Status: %s\n", status.StatusPath)
	if status.CloudStorage != nil {
		fmt.Fprintln(writer)
		renderMountStatus(writer, *status.CloudStorage)
	}
}

func renderMountStatus(writer io.Writer, status macmount.MountStatus) {
	name := strings.TrimSpace(status.Name)
	if name == "" {
		name = "LOOM storage mount"
	}
	fmt.Fprintf(writer, "%s: desired=%s actual=%s\n", name, status.DesiredState, status.ActualState)
	fmt.Fprintf(writer, "Protocol: %s\n", status.Protocol)
	fmt.Fprintf(writer, "Mount path: %s\n", status.MountPath)
	if strings.TrimSpace(status.FinderMountPath) != "" {
		fmt.Fprintf(writer, "Finder mount: %s\n", status.FinderMountPath)
	}
	if strings.TrimSpace(status.FinderSubpath) != "" {
		fmt.Fprintf(writer, "Finder subpath: %s\n", status.FinderSubpath)
	}
	fmt.Fprintf(writer, "SMB: //%s@%s/%s\n", status.User, status.Host, status.Share)
	if strings.TrimSpace(status.HostIP) != "" {
		fmt.Fprintf(writer, "Host IP: %s\n", status.HostIP)
	}
	if len(status.MountedPaths) > 0 {
		fmt.Fprintln(writer, "Mounted paths:")
		for _, path := range status.MountedPaths {
			fmt.Fprintf(writer, "  - %s\n", path)
		}
	}
	if strings.TrimSpace(status.LastErrorMessage) != "" {
		fmt.Fprintf(writer, "Last error: %s (%s)\n", status.LastErrorMessage, status.LastErrorCategory)
	}
	if status.NextRetryAt != nil {
		fmt.Fprintf(writer, "Next retry: %s\n", status.NextRetryAt.Format(time.RFC3339))
	}
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
