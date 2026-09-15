package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/response"
)

func newDatabaseCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Inspect and compact LOOM database history",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show database status, pressure, and retention candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, meta, err := loadDatabaseStatusForCLI(cmd, opts)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					OK   bool                 `json:"ok"`
					Data maintenance.DBStatus `json:"data"`
					Meta any                  `json:"meta"`
				}{OK: true, Data: status, Meta: meta})
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), status.Status)
				return nil
			}
			renderDatabaseStatus(cmd, status)
			renderResponseMeta(cmd, opts, meta)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Show database warnings and compaction advice",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, meta, err := loadDatabaseStatusForCLI(cmd, opts)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					OK   bool                 `json:"ok"`
					Data maintenance.DBStatus `json:"data"`
					Meta any                  `json:"meta"`
				}{OK: true, Data: status, Meta: meta})
			}
			if opts.plainOutput {
				if len(status.Warnings) == 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "ok")
				} else {
					for _, warning := range status.Warnings {
						fmt.Fprintln(cmd.OutOrStdout(), warning)
					}
				}
				return nil
			}
			renderDatabaseDoctor(cmd, status)
			renderResponseMeta(cmd, opts, meta)
			return nil
		},
	})

	var compactInput maintenance.DatabaseCompactInput
	var compactOutPath string
	var compactPlanPath string
	compactInput.DryRun = true
	compactCmd := &cobra.Command{
		Use:   "compact",
		Short: "Plan or apply guarded database retention compaction",
		RunE: func(cmd *cobra.Command, args []string) error {
			if compactPlanPath != "" {
				raw, err := readDatabaseCompactPlanFile(compactPlanPath)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				compactInput.Plan = raw
				if compactInput.Confirm {
					compactInput.DryRun = false
				}
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.CompactMaintenanceDatabase(ctx, commandCtx.CorrelationID, compactInput)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not compact database history.", err))
			}
			if compactOutPath != "" {
				if err := writeDatabaseCompactPlanFile(compactOutPath, envelope.Data); err != nil {
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
			renderDatabaseCompact(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	compactCmd.Flags().BoolVar(&compactInput.DryRun, "dry-run", true, "plan compaction without writing rollups or deleting rows")
	compactCmd.Flags().BoolVar(&compactInput.Confirm, "confirm", false, "confirm reviewed retention apply; also pass --dry-run=false")
	compactCmd.Flags().IntVar(&compactInput.RecentSuccessDays, "recent-success-days", 14, "preserve detailed routine successes for this many recent days")
	compactCmd.Flags().StringVar(&compactOutPath, "out", "", "write the reviewed compaction plan JSON to this path")
	compactCmd.Flags().StringVar(&compactPlanPath, "plan", "", "read a reviewed compaction plan JSON to apply with --confirm")
	compactCmd.Flags().StringVar(&compactInput.PlanHash, "plan-hash", "", "expected reviewed plan hash")
	compactCmd.Flags().Int64Var(&compactInput.MaxRowsPerBatch, "max-rows-per-batch", 0, "maximum rows to delete per table batch")
	compactCmd.Flags().Int64Var(&compactInput.MaxTotalRows, "max-total-rows", 0, "maximum rows to delete across all tables")
	compactCmd.Flags().StringVar(&compactInput.Reason, "reason", "", "operator reason recorded with the compaction evidence")
	cmd.AddCommand(compactCmd)

	return cmd
}

func loadDatabaseStatusForCLI(cmd *cobra.Command, opts *options) (maintenance.DBStatus, response.Meta, error) {
	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		return maintenance.DBStatus{}, response.Meta{}, renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	envelope, err := commandCtx.Client.MaintenanceDBStatus(ctx, commandCtx.CorrelationID)
	if err != nil {
		return maintenance.DBStatus{}, response.Meta{}, renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read database status.", err))
	}
	return envelope.Data, envelope.Meta, nil
}

func renderDatabaseStatus(cmd *cobra.Command, status maintenance.DBStatus) {
	renderMaintenanceDBStatus(cmd, status)
	if len(status.Tables) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nTables:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tROWS\tSIZE\tPRESSURE\tRETENTION")
		for _, table := range status.Tables {
			fmt.Fprintf(writer, "%s\t%d\t%s\t%s\t%s\n", table.QualifiedName, table.Rows, databaseHumanBytes(table.TotalBytes), table.Pressure, table.RetentionRole)
		}
		writer.Flush()
	}
	if len(status.Retention.Candidates) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRetention candidates:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tCANDIDATES")
		for table, count := range status.Retention.Candidates {
			fmt.Fprintf(writer, "%s\t%d\n", table, count)
		}
		writer.Flush()
	}
	if len(status.Rollups) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRecent rollups:")
		renderDatabaseRollups(cmd, status.Rollups, 8)
	}
}

func renderDatabaseDoctor(cmd *cobra.Command, status maintenance.DBStatus) {
	fmt.Fprintf(cmd.OutOrStdout(), "Database: %s migration=%s pending=%d\n", status.Status, status.MigrationStatus, status.Pending)
	if len(status.Warnings) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings: none")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Warnings:")
		for _, warning := range status.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", warning)
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nCompaction:")
	fmt.Fprintf(cmd.OutOrStdout(), "  cutoff: %s\n", status.Retention.CutoffAt.UTC().Format(time.RFC3339))
	fmt.Fprintln(cmd.OutOrStdout(), "  diagnostic: loom maintenance retention dry-run --json")
	fmt.Fprintln(cmd.OutOrStdout(), "  apply:      loom maintenance retention apply --yes")
}

func renderDatabaseCompact(cmd *cobra.Command, result maintenance.DatabaseCompactResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Database compaction: %s dry_run=%t cutoff=%s\n", result.Status, result.DryRun, result.CutoffAt.UTC().Format(time.RFC3339))
	if result.PlanHash != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Plan: %s %s\n", result.PlanID, result.PlanHash)
	}
	if result.MaxRowsPerBatch > 0 || result.MaxTotalRows > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Limits: batch=%d total=%d\n", result.MaxRowsPerBatch, result.MaxTotalRows)
	}
	if len(result.Candidates) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tCANDIDATES\tDELETED")
		for table, count := range result.Candidates {
			fmt.Fprintf(writer, "%s\t%d\t%d\n", table, count, result.Deleted[table])
		}
		writer.Flush()
	}
	if len(result.Rollups) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRollups:")
		renderDatabaseRollups(cmd, result.Rollups, 12)
	}
	if len(result.Plans) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\nRetention plan:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "TABLE\tCLASS\tCANDIDATES\tEST_BYTES\tKEEP_RULE")
		for _, plan := range result.Plans {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\n",
				plan.Table,
				plan.CandidateClass,
				plan.CandidateRows,
				databaseHumanBytes(plan.EstimatedBytes),
				plan.KeepRule,
			)
		}
		writer.Flush()
	}
	if result.EvidenceOperationID != "" || result.EvidenceStatus != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Evidence: %s %s\n", result.EvidenceStatus, result.EvidenceOperationID)
	}
	if result.PhysicalStorageNote != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: %s\n", result.PhysicalStorageNote)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", warning)
	}
}

func renderDatabaseRollups(cmd *cobra.Command, rollups []maintenance.DatabaseRollup, limit int) {
	if limit <= 0 || limit > len(rollups) {
		limit = len(rollups)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "DAY\tKIND\tKEY\tTOTAL\tSUCCESS\tFAILURE\tDURATION")
	for _, rollup := range rollups[:limit] {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%d\t%d\t%dms\n",
			rollup.RollupDay.UTC().Format("2006-01-02"),
			rollup.RollupKind,
			rollup.RollupKey,
			rollup.TotalCount,
			rollup.SuccessCount,
			rollup.FailureCount,
			rollup.TotalDurationMS,
		)
	}
	writer.Flush()
	if len(rollups) > limit {
		fmt.Fprintf(cmd.OutOrStdout(), "%d more rollup(s) hidden.\n", len(rollups)-limit)
	}
}

func databaseHumanBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func readDatabaseCompactPlanFile(path string) (json.RawMessage, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read database compaction plan %s: %w", path, err)
	}
	return json.RawMessage(raw), nil
}

func writeDatabaseCompactPlanFile(path string, result maintenance.DatabaseCompactResult) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create database compaction plan directory %s: %w", dir, err)
		}
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode database compaction plan: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("write database compaction plan %s: %w", path, err)
	}
	return nil
}
