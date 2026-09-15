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
	"loom.local/loom/internal/maintenance"
)

func newMaintenanceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Inspect LOOM maintenance workers and findings",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show maintenance status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.MaintenanceStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read maintenance status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.OverallStatus)
				return nil
			}
			renderMaintenanceStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	findingsCmd := &cobra.Command{
		Use:   "findings",
		Short: "Inspect maintenance findings",
	}
	filter := maintenance.FindingFilter{}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List maintenance findings",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListMaintenanceFindings(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list maintenance findings.", err))
			}
			if opts.jsonOutput {
				findings := envelope.Data
				if findings == nil {
					findings = []maintenance.Finding{}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					OK   bool                  `json:"ok"`
					Data []maintenance.Finding `json:"data"`
					Meta any                   `json:"meta"`
				}{
					OK:   envelope.OK,
					Data: findings,
					Meta: envelope.Meta,
				})
			}
			if opts.plainOutput {
				for _, finding := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), finding.FindingKey)
				}
				return nil
			}
			renderMaintenanceFindings(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of findings to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by finding status")
	listCmd.Flags().StringVar(&filter.Severity, "severity", "", "filter by finding severity")
	listCmd.Flags().StringVar(&filter.WorkerRef, "worker", "", "filter by worker key, ID, or kind")
	findingsCmd.AddCommand(listCmd)
	cmd.AddCommand(findingsCmd)

	retentionCmd := &cobra.Command{
		Use:   "retention",
		Short: "Inspect maintenance retention plans without deleting rows",
	}
	retentionInput := maintenance.DatabaseCompactInput{DryRun: true, RecentSuccessDays: 14}
	var retentionDryRunOutPath string
	retentionDryRunCmd := &cobra.Command{
		Use:   "dry-run",
		Short: "Report database retention candidates without mutating the database",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			retentionInput.DryRun = true
			retentionInput.Confirm = false
			envelope, err := commandCtx.Client.CompactMaintenanceDatabase(ctx, commandCtx.CorrelationID, retentionInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not build maintenance retention dry-run.", err))
			}
			if retentionDryRunOutPath != "" {
				if err := writeDatabaseCompactPlanFile(retentionDryRunOutPath, envelope.Data); err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceRetentionDryRun(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	retentionDryRunCmd.Flags().IntVar(&retentionInput.RecentSuccessDays, "recent-success-days", 14, "preserve detailed routine successes for this many recent days")
	retentionDryRunCmd.Flags().StringVar(&retentionDryRunOutPath, "out", "", "write the reviewed compaction plan JSON to this path")
	retentionDryRunCmd.Flags().Int64Var(&retentionInput.MaxRowsPerBatch, "max-rows-per-batch", 0, "maximum rows to delete per table batch")
	retentionDryRunCmd.Flags().Int64Var(&retentionInput.MaxTotalRows, "max-total-rows", 0, "maximum rows to delete across all tables")
	retentionCmd.AddCommand(retentionDryRunCmd)
	retentionApplyInput := maintenance.DatabaseCompactInput{RecentSuccessDays: 14}
	retentionApplyYes := false
	var retentionApplyPlanPath string
	retentionApplyCmd := &cobra.Command{
		Use:   "apply --yes",
		Short: "Apply reviewed database retention guardrails",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !retentionApplyYes {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), fmt.Errorf("maintenance retention apply requires --yes after reviewing `loom maintenance retention dry-run --json`"))
			}
			if retentionApplyPlanPath != "" {
				raw, err := readDatabaseCompactPlanFile(retentionApplyPlanPath)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				retentionApplyInput.Plan = raw
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			retentionApplyInput.DryRun = false
			retentionApplyInput.Confirm = true
			envelope, err := commandCtx.Client.CompactMaintenanceDatabase(ctx, commandCtx.CorrelationID, retentionApplyInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not apply maintenance retention guardrails.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceRetentionApply(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	retentionApplyCmd.Flags().BoolVar(&retentionApplyYes, "yes", false, "confirm reviewed database retention apply")
	retentionApplyCmd.Flags().IntVar(&retentionApplyInput.RecentSuccessDays, "recent-success-days", 14, "preserve detailed routine successes for this many recent days")
	retentionApplyCmd.Flags().StringVar(&retentionApplyPlanPath, "plan", "", "read a reviewed compaction plan JSON to apply")
	retentionApplyCmd.Flags().StringVar(&retentionApplyInput.PlanHash, "plan-hash", "", "expected reviewed plan hash")
	retentionApplyCmd.Flags().Int64Var(&retentionApplyInput.MaxRowsPerBatch, "max-rows-per-batch", 0, "maximum rows to delete per table batch")
	retentionApplyCmd.Flags().Int64Var(&retentionApplyInput.MaxTotalRows, "max-total-rows", 0, "maximum rows to delete across all tables")
	retentionApplyCmd.Flags().StringVar(&retentionApplyInput.Reason, "reason", "", "operator reason recorded with the compaction evidence")
	retentionCmd.AddCommand(retentionApplyCmd)
	cmd.AddCommand(retentionCmd)

	dbCmd := &cobra.Command{
		Use:   "db",
		Short: "Inspect database maintenance",
	}
	dbCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show database maintenance status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.MaintenanceDBStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read database maintenance status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceDBStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(dbCmd)

	backupCmd := &cobra.Command{
		Use:   "backup",
		Short: "Inspect and run main-node backups",
	}
	backupCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show main backup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.MaintenanceBackupStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read backup maintenance status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceBackupStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})

	backupFilter := maintenance.OperationFilter{Limit: 50}
	listBackupsCmd := &cobra.Command{
		Use:   "list",
		Short: "List main backup operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.ListMaintenanceBackups(ctx, commandCtx.CorrelationID, backupFilter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list backup operations.", err))
			}
			if opts.jsonOutput {
				backups := envelope.Data
				if backups == nil {
					backups = []maintenance.BackupOperation{}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					OK   bool                          `json:"ok"`
					Data []maintenance.BackupOperation `json:"data"`
					Meta any                           `json:"meta"`
				}{
					OK:   envelope.OK,
					Data: backups,
					Meta: envelope.Meta,
				})
			}
			if opts.plainOutput {
				for _, backup := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), backup.Operation.MaintenanceOperationID)
				}
				return nil
			}
			renderMaintenanceBackupList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listBackupsCmd.Flags().IntVar(&backupFilter.Limit, "limit", 50, "maximum number of backups to return")
	listBackupsCmd.Flags().StringVar(&backupFilter.Status, "status", "", "filter by operation status")
	backupCmd.AddCommand(listBackupsCmd)

	var backupOnce bool
	var backupReason string
	var backupIdempotencyKey string
	runBackupCmd := &cobra.Command{
		Use:   "run",
		Short: "Run main backup once",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !backupOnce {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("maintenance.backup_run_mode_required", "maintenance", "backup", "Pass --once to run a main backup."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, backupIdempotencyKey, "maintenance.backup.run")

			envelope, err := client.RunMaintenanceBackup(ctx, commandCtx.CorrelationID, maintenance.BackupRunInput{
				Reason:         backupReason,
				IdempotencyKey: backupIdempotencyKey,
				Metadata:       json.RawMessage(`{"source":"loom_cli"}`),
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run main backup.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				if operationID := backupOperationIDFromRun(envelope.Data.Run.ResultSummaryJSON); operationID != "" {
					fmt.Fprintln(cmd.OutOrStdout(), operationID)
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Run.WorkerRunID)
				return nil
			}
			renderMaintenanceBackupRun(cmd, envelope.Data.Run.WorkerRunID, envelope.Data.Run.ResultSummaryJSON)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	runBackupCmd.Flags().BoolVar(&backupOnce, "once", false, "run main backup once")
	runBackupCmd.Flags().StringVar(&backupReason, "reason", "", "reason recorded on the backup worker run")
	runBackupCmd.Flags().StringVar(&backupIdempotencyKey, "idempotency-key", "", "explicit idempotency key for the backup run")
	backupCmd.AddCommand(runBackupCmd)

	backupCmd.AddCommand(&cobra.Command{
		Use:   "verify <backup-ref>",
		Short: "Verify a registered main backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()

			envelope, err := commandCtx.Client.VerifyMaintenanceBackup(ctx, commandCtx.CorrelationID, maintenance.BackupVerifyInput{BackupRef: args[0]})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not verify main backup.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceBackupVerification(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(backupCmd)

	objectStoreCmd := &cobra.Command{
		Use:   "object-store",
		Short: "Inspect object-store maintenance",
	}
	var scanSample bool
	var scanBlobRef string
	var scanReason string
	var scanIdempotencyKey string
	scanCmd := &cobra.Command{
		Use:   "scan",
		Short: "Run an object-store integrity scan",
		RunE: func(cmd *cobra.Command, args []string) error {
			scanBlobRef = strings.TrimSpace(scanBlobRef)
			if scanSample == (scanBlobRef != "") {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("maintenance.object_store_scan_mode_required", "maintenance", "object-store", "Pass exactly one of --sample or --blob."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			client, _ := withEffectIdempotency(commandCtx.Client, scanIdempotencyKey, "maintenance.object_store.scan")

			mode := "sample"
			if scanBlobRef != "" {
				mode = "target"
			}
			envelope, err := client.RunMaintenanceObjectStoreScan(ctx, commandCtx.CorrelationID, maintenance.ObjectStoreScanInput{
				Mode:           mode,
				TargetBlobRef:  scanBlobRef,
				Reason:         scanReason,
				IdempotencyKey: scanIdempotencyKey,
				Metadata:       json.RawMessage(`{"source":"loom_cli"}`),
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not run object-store integrity scan.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				if status := objectStoreScanStatusFromRun(envelope.Data.Run.ResultSummaryJSON); status != "" {
					fmt.Fprintln(cmd.OutOrStdout(), status)
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Run.WorkerRunID)
				return nil
			}
			renderMaintenanceObjectStoreScan(cmd, envelope.Data.Run.WorkerRunID, envelope.Data.Run.ResultSummaryJSON)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	scanCmd.Flags().BoolVar(&scanSample, "sample", false, "scan the configured sample of object-store blobs")
	scanCmd.Flags().StringVar(&scanBlobRef, "blob", "", "scan one blob by blob ID, hash URI, or hash hex")
	scanCmd.Flags().StringVar(&scanReason, "reason", "", "reason recorded on the object-store integrity worker run")
	scanCmd.Flags().StringVar(&scanIdempotencyKey, "idempotency-key", "", "explicit idempotency key for the object-store scan")
	objectStoreCmd.AddCommand(scanCmd)
	objectStoreCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show object-store maintenance status",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			envelope, err := commandCtx.Client.MaintenanceObjectStoreStatus(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read object-store maintenance status.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
				return nil
			}
			renderMaintenanceObjectStoreStatus(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	})
	cmd.AddCommand(objectStoreCmd)

	return cmd
}

func renderMaintenanceStatus(cmd *cobra.Command, status maintenance.Status) {
	healthy, degraded, failed := maintenanceWorkerHealthCounts(status.Workers)
	fmt.Fprintf(cmd.OutOrStdout(), "Maintenance: %s\n", status.OverallStatus)
	fmt.Fprintf(cmd.OutOrStdout(), "Workers: healthy=%d degraded=%d failed=%d\n", healthy, degraded, failed)
	fmt.Fprintf(cmd.OutOrStdout(), "Findings: open=%d critical=%d error=%d warning=%d info=%d\n",
		status.Findings.Open,
		status.Findings.Critical,
		status.Findings.Error,
		status.Findings.Warning,
		status.Findings.Info,
	)
	if len(status.Workers) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "WORKER\tKIND\tHEALTH\tLAST SUCCESS\tFINDINGS")
		for _, worker := range status.Workers {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\n",
				worker.WorkerKey,
				worker.WorkerKind,
				worker.HealthStatus,
				timePtrOrDash(worker.LastSuccessAt),
				worker.OpenFindings,
			)
		}
		_ = writer.Flush()
	}
}

func renderMaintenanceFindings(cmd *cobra.Command, findings []maintenance.Finding) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "FINDING\tSEVERITY\tSTATUS\tWORKER\tSUMMARY")
	for _, finding := range findings {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
			finding.FindingKey,
			finding.Severity,
			finding.Status,
			finding.WorkerKey,
			finding.Summary,
		)
	}
	_ = writer.Flush()
}

func renderMaintenanceRetentionDryRun(cmd *cobra.Command, result maintenance.DatabaseCompactResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Maintenance retention dry-run: %s cutoff=%s\n", result.Status, result.CutoffAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Mutates database: %t\n", result.MutatesDatabase)
	if result.PlanHash != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s %s\n", result.PlanID, result.PlanHash)
	}
	if result.MaxRowsPerBatch > 0 || result.MaxTotalRows > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Limits: batch=%d total=%d\n", result.MaxRowsPerBatch, result.MaxTotalRows)
	}
	if len(result.Plans) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRetention plan:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tCLASS\tCANDIDATES\tEST_BYTES\tFUTURE_ACTION")
		for _, plan := range result.Plans {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n",
				plan.Table,
				plan.CandidateClass,
				plan.CandidateRows,
				databaseHumanBytes(plan.EstimatedBytes),
				plan.FutureAction,
			)
		}
		writer.Flush()
	}
	if len(result.AuditCriticalExclusions) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nAudit-critical exclusions:")
		for _, exclusion := range result.AuditCriticalExclusions {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s (%s)\n", exclusion.Table, exclusion.Rule, exclusion.Reason)
		}
	}
	if len(result.Warnings) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nWarnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
	if result.PhysicalStorageNote != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "\nNote: %s\n", result.PhysicalStorageNote)
	}
}

func renderMaintenanceRetentionApply(cmd *cobra.Command, result maintenance.DatabaseCompactResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Maintenance retention apply: %s cutoff=%s\n", result.Status, result.CutoffAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(cmd.OutOrStdout(), "Mutates database: %t\n", result.MutatesDatabase)
	if result.PlanHash != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s %s\n", result.PlanID, result.PlanHash)
	}
	if result.MaxRowsPerBatch > 0 || result.MaxTotalRows > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Limits: batch=%d total=%d\n", result.MaxRowsPerBatch, result.MaxTotalRows)
	}
	if len(result.Candidates) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRetention changes:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tCANDIDATES\tDELETED")
		for table, count := range result.Candidates {
			fmt.Fprintf(writer, "%s\t%d\t%d\n", table, count, result.Deleted[table])
		}
		writer.Flush()
	}
	if len(result.Rollups) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "\nRollups written: %d\n", len(result.Rollups))
	}
	if len(result.AuditCriticalExclusions) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nProtected classes:")
		for _, exclusion := range result.AuditCriticalExclusions {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", exclusion.Table, exclusion.Rule)
		}
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", warning)
	}
	if result.EvidenceOperationID != "" || result.EvidenceStatus != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Evidence: %s %s\n", result.EvidenceStatus, result.EvidenceOperationID)
	}
	if result.PhysicalStorageNote != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: %s\n", result.PhysicalStorageNote)
	}
}

func renderMaintenanceDBStatus(cmd *cobra.Command, status maintenance.DBStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Database: %s current=%d latest=%d migration=%s\n",
		status.Status,
		status.CurrentVersion,
		status.LatestVersion,
		status.MigrationStatus,
	)
	if status.Worker != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s health=%s last_success=%s\n",
			status.Worker.WorkerKey,
			status.Worker.HealthStatus,
			timePtrOrDash(status.Worker.LastSuccessAt),
		)
	}
	if status.LatestRun != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last run: %s status=%s trigger=%s started=%s\n",
			status.LatestRun.WorkerRunID,
			status.LatestRun.RunStatus,
			status.LatestRun.TriggerKind,
			status.LatestRun.StartedAt.UTC().Format(time.RFC3339),
		)
	}
}

func renderMaintenanceBackupStatus(cmd *cobra.Command, status maintenance.BackupStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", status.Status)
	if status.Worker != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s health=%s last_success=%s\n",
			status.Worker.WorkerKey,
			status.Worker.HealthStatus,
			timePtrOrDash(status.Worker.LastSuccessAt),
		)
	}
	if status.LatestSuccessful != nil {
		phase, packageID := maintenanceBackupPhase(status.LatestSuccessful.Operation.ResultJSON)
		fmt.Fprintf(cmd.OutOrStdout(), "Latest success: %s started=%s artifacts=%d\n",
			status.LatestSuccessful.Operation.MaintenanceOperationID,
			status.LatestSuccessful.Operation.StartedAt.UTC().Format(time.RFC3339),
			len(status.LatestSuccessful.Artifacts),
		)
		if phase != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "Latest phase: %s package=%s\n", phase, firstNonEmptyString(packageID, "-"))
		}
	}
	if status.LatestFailed != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Latest failure: %s started=%s\n",
			status.LatestFailed.Operation.MaintenanceOperationID,
			status.LatestFailed.Operation.StartedAt.UTC().Format(time.RFC3339),
		)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Findings: open=%d critical=%d error=%d warning=%d\n",
		status.OpenFindings.Open,
		status.OpenFindings.Critical,
		status.OpenFindings.Error,
		status.OpenFindings.Warning,
	)
}

func renderMaintenanceBackupList(cmd *cobra.Command, backups []maintenance.BackupOperation) {
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "BACKUP\tSTATUS\tPHASE\tSTARTED\tARTIFACTS")
	for _, backup := range backups {
		phase, _ := maintenanceBackupPhase(backup.Operation.ResultJSON)
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\n",
			backup.Operation.MaintenanceOperationID,
			backup.Operation.Status,
			firstNonEmptyString(phase, "historical_v0.9"),
			backup.Operation.StartedAt.UTC().Format(time.RFC3339),
			len(backup.Artifacts),
		)
	}
	_ = writer.Flush()
}

func maintenanceBackupPhase(raw json.RawMessage) (string, string) {
	var result struct {
		Phase     string `json:"phase"`
		PackageID string `json:"package_id"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Phase, result.PackageID
}

func renderMaintenanceBackupRun(cmd *cobra.Command, runID string, raw json.RawMessage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", runID)
	var summary struct {
		BackupOperationID string `json:"backup_operation_id"`
		BackupDir         string `json:"backup_dir"`
		Phase             string `json:"phase"`
		PackageID         string `json:"package_id"`
		ManifestSHA256    string `json:"manifest_sha256"`
		Committed         bool   `json:"committed"`
		Idempotent        bool   `json:"idempotent"`
		ArtifactCount     int    `json:"artifact_count"`
		TotalBytes        int64  `json:"total_bytes"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return
	}
	if summary.BackupOperationID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", summary.BackupOperationID)
	}
	if summary.Phase != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Phase: %s committed=%t idempotent=%t\n", summary.Phase, summary.Committed, summary.Idempotent)
	}
	if summary.PackageID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Operational package: %s\n", summary.PackageID)
	}
	if summary.BackupDir != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Directory: %s\n", summary.BackupDir)
	}
	if summary.ArtifactCount > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Artifacts: %d total_bytes=%d\n", summary.ArtifactCount, summary.TotalBytes)
	}
	if summary.ManifestSHA256 != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s\n", summary.ManifestSHA256)
	}
}

func renderMaintenanceBackupVerification(cmd *cobra.Command, verification maintenance.BackupVerification) {
	fmt.Fprintf(cmd.OutOrStdout(), "Verification: %s\n", verification.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", verification.BackupOperationID)
	fmt.Fprintf(cmd.OutOrStdout(), "Directory: %s\n", verification.BackupDir)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "CHECK\tSTATUS")
	for check, status := range verification.Checks {
		fmt.Fprintf(writer, "%s\t%s\n", check, status)
	}
	_ = writer.Flush()
	for _, message := range verification.Errors {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", message)
	}
}

func renderMaintenanceObjectStoreStatus(cmd *cobra.Command, status maintenance.ObjectStoreStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Object store: %s\n", status.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Blobs: total=%d verified=%d pending=%d missing=%d corrupt=%d\n",
		status.BlobCounts.Total,
		status.BlobCounts.Verified,
		status.BlobCounts.Pending,
		status.BlobCounts.Missing,
		status.BlobCounts.Corrupt,
	)
	if status.Worker != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Worker: %s health=%s last_success=%s\n",
			status.Worker.WorkerKey,
			status.Worker.HealthStatus,
			timePtrOrDash(status.Worker.LastSuccessAt),
		)
	}
	if status.LatestRun != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last scan: %s status=%s started=%s\n",
			status.LatestRun.WorkerRunID,
			status.LatestRun.RunStatus,
			status.LatestRun.StartedAt.UTC().Format(time.RFC3339),
		)
	}
}

func renderMaintenanceObjectStoreScan(cmd *cobra.Command, runID string, raw json.RawMessage) {
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", runID)
	var summary struct {
		Status           string `json:"status"`
		Mode             string `json:"mode"`
		Checked          int64  `json:"checked"`
		Verified         int64  `json:"verified"`
		Missing          int64  `json:"missing"`
		Corrupt          int64  `json:"corrupt"`
		FindingsOpened   int64  `json:"findings_opened"`
		FindingsResolved int64  `json:"findings_resolved"`
	}
	if err := json.Unmarshal(raw, &summary); err != nil {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Object store scan: %s mode=%s\n", summary.Status, summary.Mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Blobs: checked=%d verified=%d missing=%d corrupt=%d\n",
		summary.Checked,
		summary.Verified,
		summary.Missing,
		summary.Corrupt,
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Findings: opened=%d resolved=%d\n",
		summary.FindingsOpened,
		summary.FindingsResolved,
	)
}

func objectStoreScanStatusFromRun(raw json.RawMessage) string {
	var summary struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &summary)
	return summary.Status
}

func backupOperationIDFromRun(raw json.RawMessage) string {
	var summary struct {
		BackupOperationID string `json:"backup_operation_id"`
	}
	_ = json.Unmarshal(raw, &summary)
	return summary.BackupOperationID
}

func maintenanceWorkerHealthCounts(workers []maintenance.WorkerStatus) (healthy, degraded, failed int) {
	for _, worker := range workers {
		switch worker.HealthStatus {
		case "healthy":
			healthy++
		case "failed":
			failed++
		default:
			degraded++
		}
	}
	return healthy, degraded, failed
}
