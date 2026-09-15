package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/storagedoctor"
)

func newStorageDoctorCommand(opts *options) *cobra.Command {
	var doctorOpts storagedoctor.Options
	var strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run storage catalog, physical-root, retention, archive, and mount health checks",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			report, err := storagedoctor.Run(ctx, commandCtx.Client, commandCtx.CorrelationID, doctorOpts)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("storage.doctor_failed", "storage", "doctor", "Could not run storage doctor.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(report)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
			} else {
				renderStorageDoctorReport(cmd, report)
			}
			if strict && report.Status == storagedoctor.StatusError {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New("storage.doctor_failed", "storage", "doctor", "Storage doctor reported errors."))
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&doctorOpts.MaxEntries, "limit", 5000, "maximum catalog entries to inspect")
	cmd.Flags().IntVar(&doctorOpts.MaxInspections, "inspect-limit", 500, "maximum entries to inspect in detail")
	cmd.Flags().BoolVar(&doctorOpts.IncludeMount, "include-mount", false, "include local LOOM Main mount preflight checks")
	cmd.Flags().BoolVar(&doctorOpts.IncludeFidelity, "include-fidelity", false, "include filesystem fidelity findings and safe-delete blockers")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when doctor status is error")
	return cmd
}

func newStorageRepairCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Run safe storage repair and repair-planning commands",
	}
	cmd.AddCommand(newStorageRepairCatalogCommand(opts))
	cmd.AddCommand(newStorageRepairRetentionCommand(opts))
	return cmd
}

func newStorageRepairCatalogCommand(opts *options) *cobra.Command {
	var source string
	var dryRun bool
	var yes bool
	var reason string
	var createdBy string
	cmd := &cobra.Command{
		Use:   "catalog",
		Short: "Plan or run source-specific catalog repair",
		RunE: func(cmd *cobra.Command, args []string) error {
			source = strings.TrimSpace(source)
			effectiveDryRun := dryRun || !yes
			if source == "main-documents" {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.ReconcileMainDocuments(ctx, commandCtx.CorrelationID, mainstorage.ReconcileInput{
					DryRun:    effectiveDryRun,
					Yes:       yes,
					Reason:    reason,
					CreatedBy: createdBy,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not repair main Documents catalog state.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.MissingCataloged)
					return nil
				}
				renderMainDocumentsReconcileResult(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			plan := storagedoctor.RepairCatalogPlan(source, effectiveDryRun)
			if plan.Status == storagedoctor.StatusError {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("storage.repair_source_invalid", "storage", "catalog", "Choose --source dropzone, watched-roots, or main-documents."))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
			} else {
				renderStorageRepairPlan(cmd, "Storage catalog repair", plan)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "catalog source lane: dropzone, watched-roots, or main-documents")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "plan the repair without attempting mutation; this is the default unless --yes is passed")
	cmd.Flags().BoolVar(&yes, "yes", false, "apply supported catalog repairs")
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason stored on repair tombstones when supported")
	cmd.Flags().StringVar(&createdBy, "created-by", "", "actor recorded on repair tombstones when supported")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func newStorageRepairRetentionCommand(opts *options) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Plan retention/tombstone repair from current retention status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetStorageRetentionStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read storage retention status.", err))
			}
			plan := storagedoctor.RepairRetentionPlan(envelope.Data, dryRun)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
				return nil
			}
			renderStorageRepairPlan(cmd, "Storage retention repair", plan)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "plan the retention repair without attempting mutation")
	return cmd
}

func newStorageMountStatusCommand(opts *options) *cobra.Command {
	var mountOpts storagedoctor.MountOptions
	var doctor bool
	cmd := &cobra.Command{
		Use:   "mount-status",
		Short: "Check local LOOM Main mount readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			protocol, err := storagedoctor.ResolveMountProtocol(mountOpts.Protocol)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("storage.mount_protocol_invalid", "storage", "mount", "Unsupported LOOM Main mount protocol.", err))
			}
			mountOpts.Protocol = protocol
			status := storagedoctor.RunMountPreflight(mountOpts)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), status.Status)
			} else {
				renderStorageMountStatus(cmd, status)
			}
			if doctor && mountOpts.Strict && status.Status == storagedoctor.StatusError {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("storage.mount_preflight_failed", "storage", "mount", "LOOM Main mount preflight reported errors."))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&doctor, "doctor", false, "run this command as a doctor/preflight check")
	cmd.Flags().StringVar(&mountOpts.Protocol, "protocol", "", "mount protocol: smb or rclone")
	cmd.Flags().StringVar(&mountOpts.MountPath, "mount-path", "", "local LOOM Main mount path")
	cmd.Flags().StringVar(&mountOpts.RcloneConfig, "rclone-config", "", "managed rclone config path")
	cmd.Flags().StringVar(&mountOpts.RemoteName, "remote", "", "rclone remote name")
	cmd.Flags().StringVar(&mountOpts.SMBHost, "smb-host", "", "SMB host")
	cmd.Flags().StringVar(&mountOpts.SMBShare, "smb-share", "", "SMB share name")
	cmd.Flags().StringVar(&mountOpts.SMBUser, "smb-user", "", "SMB user")
	cmd.Flags().BoolVar(&mountOpts.Strict, "strict", false, "exit non-zero when preflight status is error")
	return cmd
}

func renderStorageDoctorReport(cmd *cobra.Command, report storagedoctor.Report) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM storage doctor: %s\n", report.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Checks: ok=%d warning=%d error=%d skipped=%d total=%d\n",
		report.Summary.OK, report.Summary.Warnings, report.Summary.Errors, report.Summary.Skipped, report.Summary.Checks)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCHECK\tSUMMARY")
	for _, check := range report.Checks {
		fmt.Fprintf(writer, "%s\t%s\t%s\n", check.Status, check.ID, check.Summary)
	}
	_ = writer.Flush()
	if len(report.Findings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Findings:")
		for _, finding := range report.Findings {
			target := ""
			if finding.Target != "" {
				target = " " + finding.Target
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s %s%s: %s\n", finding.Severity, finding.Kind, target, finding.Message)
			if finding.RepairHint != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "    repair: %s\n", finding.RepairHint)
			}
		}
	}
}

func renderStorageRepairPlan(cmd *cobra.Command, title string, plan storagedoctor.RepairPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", title, plan.Status)
	if plan.Source != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Source: %s\n", plan.Source)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Dry run: %t\n", plan.DryRun)
	if len(plan.Actions) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Actions:")
		for _, action := range plan.Actions {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", action)
		}
	}
	if len(plan.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range plan.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
}

func renderStorageMountStatus(cmd *cobra.Command, status storagedoctor.MountStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Main mount status: %s\n", status.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Protocol: %s\n", status.Protocol)
	fmt.Fprintf(cmd.OutOrStdout(), "Mount path: %s\n", status.MountPath)
	switch status.Protocol {
	case "smb":
		fmt.Fprintf(cmd.OutOrStdout(), "SMB host: %s\n", status.SMBHost)
		fmt.Fprintf(cmd.OutOrStdout(), "SMB share: %s\n", status.SMBShare)
		fmt.Fprintf(cmd.OutOrStdout(), "SMB user: %s\n", status.SMBUser)
	case "rclone":
		fmt.Fprintf(cmd.OutOrStdout(), "Rclone config: %s\n", status.RcloneConfig)
		fmt.Fprintf(cmd.OutOrStdout(), "Remote: %s\n", status.RemoteName)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCHECK\tSUMMARY\tDETAIL")
	for _, check := range status.Checks {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", check.Status, check.ID, check.Summary, check.Detail)
	}
	_ = writer.Flush()
}
