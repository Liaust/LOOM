package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/backup"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/ops"
	"loom.local/loom/internal/response"
)

type backupCreatePlan struct {
	Status             string   `json:"status"`
	RequiresProduction bool     `json:"requires_production"`
	Captures           []string `json:"captures"`
	Excludes           []string `json:"excludes"`
	Backend            string   `json:"backend"`
}

func newBackupCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create, verify, and drill LOOM main-node backups",
	}

	cmd.AddCommand(newBackupStatusCommand(opts))
	cmd.AddCommand(newBackupListCommand(opts))
	cmd.AddCommand(newBackupCreateCommand(opts))
	cmd.AddCommand(newBackupVerifyCommand(opts))
	cmd.AddCommand(newBackupRestoreDrillCommand(opts))
	cmd.AddCommand(newBackupCoverageCommand(opts))
	cmd.AddCommand(newBackupContractsCommand(opts))
	cmd.AddCommand(newBackupContractCommand(opts))
	return cmd
}

func newBackupContractCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Run backup contract maintenance",
	}
	cmd.AddCommand(newBackupContractMigrateIgnorePolicyCommand(opts))
	return cmd
}

func newBackupContractMigrateIgnorePolicyCommand(opts *options) *cobra.Command {
	var apply bool
	var dryRun bool
	var yes bool
	var idempotencyKey string
	migrateCmd := &cobra.Command{
		Use:   "migrate-ignore-policy",
		Short: "Migrate legacy expanded excludes to the managed ignore profile",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if apply == dryRun {
				return renderError(cmd, opts, correlationID, loomerrors.New("backup_contracts.migration_mode_required", "backup", "migrate-ignore-policy", "Choose exactly one of --dry-run or --apply."))
			}
			if apply && !yes {
				return renderError(cmd, opts, correlationID, loomerrors.New("backup_contracts.migration_confirmation_required", "backup", "migrate-ignore-policy", "Pass --yes with --apply after reviewing the dry run."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			client := commandCtx.Client
			if apply {
				client, _ = withEffectIdempotency(client, idempotencyKey, "backup.contract.migrate_ignore_policy")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			envelope, err := client.MigrateBackupContractIgnorePolicy(ctx, commandCtx.CorrelationID, backupcontracts.MigrateIgnorePolicyRequest{DryRun: dryRun, Apply: apply, Yes: yes})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "migrate-ignore-policy", "Could not migrate backup contract ignore policies.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "%d\n", envelope.Data.Migration.ChangedCount)
				return nil
			}
			renderBackupContractMigration(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	migrateCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show migration changes without writing contracts")
	migrateCmd.Flags().BoolVar(&apply, "apply", false, "apply reviewed migration changes")
	migrateCmd.Flags().BoolVar(&yes, "yes", false, "confirm migration apply")
	migrateCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for migration apply")
	return migrateCmd
}

func newBackupStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
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
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not read backup status.", err))
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
	}
}

func newBackupListCommand(opts *options) *cobra.Command {
	filter := maintenance.OperationFilter{Limit: 50}
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List main backup operations",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListMaintenanceBackups(ctx, commandCtx.CorrelationID, filter)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not list backups.", err))
			}
			if opts.jsonOutput {
				backups := envelope.Data
				if backups == nil {
					backups = []maintenance.BackupOperation{}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Envelope[[]maintenance.BackupOperation]{
					OK:   envelope.OK,
					Data: backups,
					Meta: envelope.Meta,
				})
			}
			if opts.plainOutput {
				for _, item := range envelope.Data {
					fmt.Fprintln(cmd.OutOrStdout(), item.Operation.MaintenanceOperationID)
				}
				return nil
			}
			renderMaintenanceBackupList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	listCmd.Flags().IntVar(&filter.Limit, "limit", 50, "maximum number of backups to return")
	listCmd.Flags().StringVar(&filter.Status, "status", "", "filter by operation status")
	return listCmd
}

func newBackupCreateCommand(opts *options) *cobra.Command {
	var production bool
	var allowNonProduction bool
	var dryRun bool
	var reason string
	var idempotencyKey string
	createCmd := &cobra.Command{
		Use:   "create",
		Short: "Create a bounded operational recovery package",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if production && allowNonProduction {
				return renderError(cmd, opts, correlationID, loomerrors.New("backup.create_mode_invalid", "backup", "create", "Choose either --production or --allow-non-production, not both."))
			}
			if !production && !allowNonProduction {
				return renderError(cmd, opts, correlationID, loomerrors.New("backup.create_confirmation_required", "backup", "create", "Pass --production for main-node backups or --allow-non-production for dev drills."))
			}
			if dryRun {
				plan := backupCreatePlan{
					Status:             "planned",
					RequiresProduction: production,
					Backend:            "maintenance.main_backup.operational_package",
					Captures: []string{
						"PostgreSQL custom-format dump",
						"redacted service, install, and active release configuration",
						"migration, update, and health state",
						"authenticated operational package manifest",
					},
					Excludes: append(defaultBackupExclusions(), "complete local user-data generation"),
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, plan))
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
					return nil
				}
				renderBackupCreatePlan(cmd, plan)
				return nil
			}

			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
			defer cancel()

			if production {
				healthEnvelope, err := commandCtx.Client.Health(ctx, commandCtx.CorrelationID)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not verify production runtime before backup.", err))
				}
				if !isProductionMainHealth(healthEnvelope.Data.Environment, healthEnvelope.Data.Node.ID, healthEnvelope.Data.Node.Role) {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New("backup.production_required", "backup", healthEnvelope.Data.Node.ID, "Backup create --production must run against the production main runtime."))
				}
			}

			client, resolvedIdempotencyKey := withEffectIdempotency(commandCtx.Client, idempotencyKey, "backup.create")
			metadata, _ := json.Marshal(map[string]any{
				"source":               "loom_backup_cli",
				"production_requested": production,
			})
			envelope, err := client.RunMaintenanceBackup(ctx, commandCtx.CorrelationID, maintenance.BackupRunInput{
				Reason:         firstNonEmptyString(reason, "manual backup requested by loom backup create"),
				IdempotencyKey: resolvedIdempotencyKey,
				Metadata:       metadata,
			})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not create backup.", err))
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
	createCmd.Flags().BoolVar(&production, "production", false, "confirm this backup is for the production main runtime")
	createCmd.Flags().BoolVar(&allowNonProduction, "allow-non-production", false, "allow backup creation against a non-production runtime for drills")
	createCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be captured without creating a backup")
	createCmd.Flags().StringVar(&reason, "reason", "", "reason recorded on the backup run")
	createCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for the backup run")
	return createCmd
}

func newBackupCoverageCommand(opts *options) *cobra.Command {
	var dataDir string
	var objectStoreRoot string
	var privateBackupsRoot string
	var mainDocumentsRoot string
	var storageRetentionRoot string
	var storageArchiveRoot string
	var storageExportRoot string
	var mainBoxPath string
	var boxNotesRoot string
	var notesProjectionRoot string
	var mainBoxPolicy string
	var backupRoot string
	var manifestPath string
	var localOnly bool
	coverageCmd := &cobra.Command{
		Use:   "coverage",
		Short: "Check main-backed backup coverage for cloud readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			localInspection := localOnly || backupCoverageHasLocalOverrides(dataDir, objectStoreRoot, privateBackupsRoot, mainDocumentsRoot, storageRetentionRoot, storageArchiveRoot, storageExportRoot, mainBoxPath, boxNotesRoot, notesProjectionRoot, mainBoxPolicy, backupRoot, manifestPath)
			if !localInspection {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.BackupCoverage(ctx, commandCtx.CorrelationID)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "coverage", "Could not read main-backed backup coverage.", err))
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderBackupCoverage(cmd, envelope.Data)
				return nil
			}

			correlationID := correlation.Normalize(opts.correlationID)
			cfg, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "backup", "coverage", "Could not load LOOM config for backup coverage.", err))
			}
			coverageOpts := backupcoverage.OptionsFromConfig(cfg)
			coverageOpts.Mode = backupcoverage.ModeLocal
			if strings.TrimSpace(dataDir) != "" {
				coverageOpts.DataDir = dataDir
				if strings.TrimSpace(objectStoreRoot) == "" {
					coverageOpts.ObjectStoreRoot = filepath.Join(dataDir, "object-store")
				}
				if strings.TrimSpace(privateBackupsRoot) == "" {
					coverageOpts.PrivateBackupsRoot = filepath.Join(dataDir, "private-backups")
				}
				if strings.TrimSpace(storageRetentionRoot) == "" {
					coverageOpts.StorageRetentionRoot = filepath.Join(dataDir, "storage-retention")
				}
				if strings.TrimSpace(storageArchiveRoot) == "" {
					coverageOpts.StorageArchiveRoot = filepath.Join(dataDir, "storage-archive")
				}
				if strings.TrimSpace(storageExportRoot) == "" {
					coverageOpts.StorageExportRoot = filepath.Join(dataDir, "storage-views", "main-export")
				}
				if strings.TrimSpace(notesProjectionRoot) == "" {
					coverageOpts.NotesProjectionRoot = filepath.Join(dataDir, "generated", config.DefaultNotesProjection)
				}
				if strings.TrimSpace(backupRoot) == "" {
					coverageOpts.BackupRoot = filepath.Join(dataDir, "backups", "main")
				}
			}
			if strings.TrimSpace(objectStoreRoot) != "" {
				coverageOpts.ObjectStoreRoot = objectStoreRoot
			}
			if strings.TrimSpace(privateBackupsRoot) != "" {
				coverageOpts.PrivateBackupsRoot = privateBackupsRoot
			}
			if strings.TrimSpace(mainDocumentsRoot) != "" {
				coverageOpts.MainDocumentsRoot = mainDocumentsRoot
			}
			if strings.TrimSpace(storageRetentionRoot) != "" {
				coverageOpts.StorageRetentionRoot = storageRetentionRoot
			}
			if strings.TrimSpace(storageArchiveRoot) != "" {
				coverageOpts.StorageArchiveRoot = storageArchiveRoot
			}
			if strings.TrimSpace(storageExportRoot) != "" {
				coverageOpts.StorageExportRoot = storageExportRoot
			}
			if strings.TrimSpace(mainBoxPath) != "" {
				coverageOpts.MainBoxPath = mainBoxPath
				if strings.TrimSpace(mainDocumentsRoot) == "" {
					coverageOpts.MainDocumentsRoot = filepath.Join(mainBoxPath, "Documents")
				}
				if strings.TrimSpace(boxNotesRoot) == "" {
					coverageOpts.BoxNotesRoot = filepath.Join(mainBoxPath, "Notes")
				}
			}
			if strings.TrimSpace(boxNotesRoot) != "" {
				coverageOpts.BoxNotesRoot = boxNotesRoot
			}
			if strings.TrimSpace(notesProjectionRoot) != "" {
				coverageOpts.NotesProjectionRoot = notesProjectionRoot
			}
			if strings.TrimSpace(mainBoxPolicy) != "" {
				coverageOpts.MainBoxPolicy = mainBoxPolicy
			}
			if strings.TrimSpace(backupRoot) != "" {
				coverageOpts.BackupRoot = backupRoot
			}
			if strings.TrimSpace(manifestPath) != "" {
				coverageOpts.ManifestPath = manifestPath
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			report, err := backupcoverage.Check(ctx, coverageOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, report))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
				return nil
			}
			renderBackupCoverage(cmd, report)
			return nil
		},
	}
	coverageCmd.Flags().StringVar(&dataDir, "data-dir", "", "LOOM data directory")
	coverageCmd.Flags().StringVar(&objectStoreRoot, "object-store-root", "", "object-store root")
	coverageCmd.Flags().StringVar(&privateBackupsRoot, "private-backups-root", "", "private-backups root")
	coverageCmd.Flags().StringVar(&mainDocumentsRoot, "main-documents-root", "", "canonical Box Documents root")
	coverageCmd.Flags().StringVar(&storageRetentionRoot, "storage-retention-root", "", "storage retention root")
	coverageCmd.Flags().StringVar(&storageArchiveRoot, "storage-archive-root", "", "storage archive root")
	coverageCmd.Flags().StringVar(&storageExportRoot, "storage-export-root", "", "generated storage export root")
	coverageCmd.Flags().StringVar(&mainBoxPath, "main-box-path", "", "main LOOM Box path")
	coverageCmd.Flags().StringVar(&boxNotesRoot, "box-notes-root", "", "Box Notes source root")
	coverageCmd.Flags().StringVar(&notesProjectionRoot, "notes-projection-root", "", "generated Notes projection root (explicit compatibility override)")
	coverageCmd.Flags().StringVar(&mainBoxPolicy, "main-box-policy", "", "main Box backup policy")
	coverageCmd.Flags().StringVar(&backupRoot, "backup-root", "", "main backup root")
	coverageCmd.Flags().StringVar(&manifestPath, "manifest", "", "specific manifest.json to compare")
	coverageCmd.Flags().BoolVar(&localOnly, "local", false, "check local filesystem coverage instead of querying the main daemon")
	return coverageCmd
}

func backupCoverageHasLocalOverrides(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func newBackupContractsCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contracts",
		Short: "Manage Box backup contracts",
	}
	cmd.AddCommand(newBackupContractsListCommand(opts))
	cmd.AddCommand(newBackupContractsStatusCommand(opts))
	cmd.AddCommand(newBackupContractsInspectCommand(opts))
	cmd.AddCommand(newBackupContractsPreflightCommand(opts))
	cmd.AddCommand(newBackupContractsCreateCommand(opts))
	cmd.AddCommand(newBackupContractsEnableCommand(opts))
	cmd.AddCommand(newBackupContractsDisableCommand(opts))
	cmd.AddCommand(newBackupContractsRecheckCommand(opts))
	cmd.AddCommand(newBackupContractsRetryActivationCommand(opts))
	cmd.AddCommand(newBackupContractsDeleteCommand(opts))
	cmd.AddCommand(newBackupContractsPlanCommand(opts))
	return cmd
}

func newBackupContractsStatusCommand(opts *options) *cobra.Command {
	filter := backupcontracts.ProtectedFolderFilter{}
	statusCmd := &cobra.Command{Use: "status", Short: "Show protected-folder lifecycle status", RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		envelope, err := commandCtx.Client.ListProtectedFolders(ctx, commandCtx.CorrelationID, filter)
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "contracts", "Could not read protected-folder status.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		if opts.plainOutput {
			for _, folder := range envelope.Data.Folders {
				fmt.Fprintln(cmd.OutOrStdout(), folder.Key)
			}
			return nil
		}
		renderProtectedFolderList(cmd, envelope.Data)
		renderResponseMeta(cmd, opts, envelope.Meta)
		return nil
	}}
	statusCmd.Flags().StringVar(&filter.Lifecycle, "status", "", "filter by lifecycle status")
	statusCmd.Flags().StringVar(&filter.NodeRef, "node", "", "filter by owner node")
	statusCmd.Flags().IntVar(&filter.Limit, "limit", 0, "maximum protected folders")
	return statusCmd
}

func newBackupContractsListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List Box backup contracts",
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.ListBackupContracts(ctx, commandCtx.CorrelationID)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "contracts", "Could not list backup contracts.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				for _, contract := range envelope.Data.Contracts {
					fmt.Fprintln(cmd.OutOrStdout(), contract.Key)
				}
				return nil
			}
			renderBackupContractList(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newBackupContractsInspectCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <contract-key>",
		Short: "Inspect one Box backup contract",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			envelope, err := commandCtx.Client.GetProtectedFolder(ctx, commandCtx.CorrelationID, args[0])
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not inspect backup contract.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Key)
				return nil
			}
			renderProtectedFolderRecord(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
}

func newBackupContractsPreflightCommand(opts *options) *cobra.Command {
	input := backupcontracts.PreflightCreateRequest{}
	cmd := &cobra.Command{Use: "preflight --node <node> --path <path>", Short: "Check a folder safely on its owner node", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(input.NodeRef) == "" || strings.TrimSpace(input.Path) == "" {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("backup_contracts.preflight_target_required", "backup", "preflight", "Pass both --node and --path for folder preflight."))
		}
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		client := commandCtx.Client
		if input.IdempotencyKey != "" {
			client, _ = withEffectIdempotency(client, input.IdempotencyKey, "backup.contract.preflight")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		envelope, err := client.CreateBackupContractPreflight(ctx, commandCtx.CorrelationID, input)
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "preflight", "Could not request folder preflight.", err))
		}
		return renderBackupPreflightEnvelope(cmd, opts, envelope)
	}}
	cmd.Flags().StringVar(&input.NodeRef, "node", "", "node that owns the folder")
	cmd.Flags().StringVar(&input.Path, "path", "", "absolute folder path on the owner node")
	cmd.Flags().StringArrayVar(&input.Include, "include", nil, "include glob; repeat for multiple patterns")
	cmd.Flags().StringArrayVar(&input.Exclude, "exclude", nil, "exclude glob; repeat for multiple patterns")
	cmd.Flags().StringVar(&input.IdempotencyKey, "idempotency-key", "", "explicit idempotency key")
	return cmd
}

func newBackupContractsCreateCommand(opts *options) *cobra.Command {
	input := backupcontracts.CreateRequest{}
	var idempotencyKey string
	createCmd := &cobra.Command{
		Use:   "create <contract-key> --path <path>",
		Short: "Create a Box backup contract on the main daemon",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input.Key = args[0]
			if strings.TrimSpace(input.TargetPath) == "" {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("backup_contracts.path_required", "backup", input.Key, "Pass --path for the folder this contract should back up."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client := commandCtx.Client
			if !input.DryRun {
				client, _ = withEffectIdempotency(commandCtx.Client, idempotencyKey, "backup.contract.create")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			envelope, err := client.CreateBackupContract(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", input.Key, "Could not create backup contract.", err))
			}
			return renderBackupContractLifecycleEnvelope(cmd, opts, envelope)
		},
	}
	createCmd.Flags().StringVar(&input.TargetPath, "path", "", "folder path to back up")
	createCmd.Flags().StringVar(&input.TargetScope, "target-scope", "", "target scope: owner_node_absolute or box_relative; defaults from --path")
	createCmd.Flags().StringVar(&input.DisplayName, "display-name", "", "human-readable contract label")
	createCmd.Flags().StringVar(&input.OwnerNode, "owner-node", "", "node that owns the target path; defaults to daemon node")
	createCmd.Flags().StringVar(&input.OwnerNode, "node", "", "node that owns the target path")
	createCmd.Flags().StringVar(&input.PreflightID, "preflight", "", "completed matching preflight ID")
	createCmd.Flags().StringArrayVar(&input.Include, "include", nil, "include glob; repeat for multiple patterns")
	createCmd.Flags().StringArrayVar(&input.Exclude, "exclude", nil, "exclude glob; repeat for multiple patterns")
	createCmd.Flags().StringVar(&input.BackupMode, "backup-mode", backupcontracts.BackupModeIncrementalRaw, "backup mode: incremental_raw, metadata_only, or none")
	createCmd.Flags().Int64Var(&input.MaxFileBytes, "max-file-bytes", 0, "maximum file bytes accepted by this contract")
	createCmd.Flags().Int64Var(&input.MaxBatchBytes, "max-batch-bytes", 0, "maximum batch bytes accepted by this contract")
	createCmd.Flags().BoolVar(&input.Replace, "replace", false, "replace an existing contract with the same key")
	createCmd.Flags().BoolVar(&input.DryRun, "dry-run", false, "validate and show the planned contract without writing")
	createCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for create")
	return createCmd
}

func newBackupContractsEnableCommand(opts *options) *cobra.Command {
	var dryRun bool
	var idempotencyKey string
	cmd := &cobra.Command{Use: "enable <contract-key>", Short: "Enable a protected folder and queue owner-node activation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		client := commandCtx.Client
		if !dryRun {
			client, _ = withEffectIdempotency(client, idempotencyKey, "backup.contract.enable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		envelope, err := client.EnableBackupContract(ctx, commandCtx.CorrelationID, args[0], backupcontracts.EnableRequest{DryRun: dryRun})
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not enable protected folder.", err))
		}
		return renderBackupContractLifecycleEnvelope(cmd, opts, envelope)
	}}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show enable without writing")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key")
	return cmd
}

func newBackupContractsRecheckCommand(opts *options) *cobra.Command {
	var idempotencyKey string
	cmd := &cobra.Command{Use: "recheck <contract-key>", Short: "Run a new owner-node folder preflight", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		client := commandCtx.Client
		client, _ = withEffectIdempotency(client, idempotencyKey, "backup.contract.recheck")
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		envelope, err := client.RecheckBackupContract(ctx, commandCtx.CorrelationID, args[0])
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not recheck protected folder.", err))
		}
		return renderBackupPreflightEnvelope(cmd, opts, envelope)
	}}
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key")
	return cmd
}

func newBackupContractsRetryActivationCommand(opts *options) *cobra.Command {
	var reason, idempotencyKey string
	cmd := &cobra.Command{Use: "retry-activation <contract-key>", Short: "Queue another owner-node activation attempt", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		commandCtx, err := resolveCommandContext(opts)
		if err != nil {
			return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
		}
		client := commandCtx.Client
		client, _ = withEffectIdempotency(client, idempotencyKey, "backup.contract.retry_activation")
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		envelope, err := client.RetryBackupContractActivation(ctx, commandCtx.CorrelationID, args[0], backupcontracts.RetryActivationRequest{Reason: reason})
		if err != nil {
			return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not retry protected-folder activation.", err))
		}
		if opts.jsonOutput {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
		}
		if opts.plainOutput {
			fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.DesiredRevision)
			return nil
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Protected folder activation queued\nNode: %s\nDesired revision: %d\nNode applied: false\n", envelope.Data.NodeID, envelope.Data.DesiredRevision)
		renderResponseMeta(cmd, opts, envelope.Meta)
		return nil
	}}
	cmd.Flags().StringVar(&reason, "reason", "", "operator reason for retry")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key")
	return cmd
}

func newBackupContractsDisableCommand(opts *options) *cobra.Command {
	var dryRun bool
	var idempotencyKey string
	disableCmd := &cobra.Command{
		Use:   "disable <contract-key>",
		Short: "Disable a Box backup contract without deleting its YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client := commandCtx.Client
			if !dryRun {
				client, _ = withEffectIdempotency(commandCtx.Client, idempotencyKey, "backup.contract.disable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			envelope, err := client.DisableBackupContract(ctx, commandCtx.CorrelationID, args[0], backupcontracts.DisableRequest{DryRun: dryRun})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not disable backup contract.", err))
			}
			return renderBackupContractLifecycleEnvelope(cmd, opts, envelope)
		},
	}
	disableCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the disable operation without writing")
	disableCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for disable")
	return disableCmd
}

func newBackupContractsDeleteCommand(opts *options) *cobra.Command {
	var dryRun bool
	var yes bool
	var idempotencyKey string
	deleteCmd := &cobra.Command{
		Use:   "delete <contract-key>",
		Short: "Delete a Box backup contract YAML file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !dryRun && !yes {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.New("backup_contracts.delete_confirmation_required", "backup", args[0], "Pass --yes to delete a backup contract."))
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			client := commandCtx.Client
			if !dryRun {
				client, _ = withEffectIdempotency(commandCtx.Client, idempotencyKey, "backup.contract.delete")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			envelope, err := client.DeleteBackupContract(ctx, commandCtx.CorrelationID, args[0], backupcontracts.DeleteRequest{DryRun: dryRun})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", args[0], "Could not delete backup contract.", err))
			}
			return renderBackupContractLifecycleEnvelope(cmd, opts, envelope)
		},
	}
	deleteCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the delete operation without removing the YAML file")
	deleteCmd.Flags().BoolVar(&yes, "yes", false, "confirm backup contract deletion")
	deleteCmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for delete")
	return deleteCmd
}

func newBackupContractsPlanCommand(opts *options) *cobra.Command {
	var localOnly bool
	var dataDir string
	var backupRoot string
	var manifestPath string
	planCmd := &cobra.Command{
		Use:   "plan",
		Short: "Build a read-only backup contract plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			localInspection := localOnly || backupCoverageHasLocalOverrides(dataDir, backupRoot, manifestPath)
			if !localInspection {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.BackupCoverage(ctx, commandCtx.CorrelationID)
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "backup", "contracts", "Could not read main-backed backup coverage for contract planning.", err))
				}
				plan := backupcoverage.PlanContractsFromReport(envelope.Data)
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(commandCtx.CorrelationID, plan))
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
					return nil
				}
				renderBackupContractPlan(cmd, plan)
				return nil
			}

			correlationID := correlation.Normalize(opts.correlationID)
			cfg, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if err != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "backup", "contracts", "Could not load LOOM config for backup contract planning.", err))
			}
			coverageOpts := backupcoverage.OptionsFromConfig(cfg)
			coverageOpts.Mode = backupcoverage.ModeLocal
			if strings.TrimSpace(dataDir) != "" {
				coverageOpts.DataDir = dataDir
				coverageOpts.ObjectStoreRoot = filepath.Join(dataDir, "object-store")
				coverageOpts.PrivateBackupsRoot = filepath.Join(dataDir, "private-backups")
				coverageOpts.StorageRetentionRoot = filepath.Join(dataDir, "storage-retention")
				coverageOpts.StorageArchiveRoot = filepath.Join(dataDir, "storage-archive")
				coverageOpts.StorageExportRoot = filepath.Join(dataDir, "storage-views", "main-export")
				coverageOpts.NotesProjectionRoot = filepath.Join(dataDir, "generated", config.DefaultNotesProjection)
				coverageOpts.BackupRoot = filepath.Join(dataDir, "backups", "main")
			}
			if strings.TrimSpace(backupRoot) != "" {
				coverageOpts.BackupRoot = backupRoot
			}
			if strings.TrimSpace(manifestPath) != "" {
				coverageOpts.ManifestPath = manifestPath
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			plan, err := backupcoverage.PlanContracts(ctx, coverageOpts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, plan))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
				return nil
			}
			renderBackupContractPlan(cmd, plan)
			return nil
		},
	}
	planCmd.Flags().BoolVar(&localOnly, "local", false, "plan contracts from local filesystem coverage instead of querying the main daemon")
	planCmd.Flags().StringVar(&dataDir, "data-dir", "", "LOOM data directory for local planning")
	planCmd.Flags().StringVar(&backupRoot, "backup-root", "", "main backup root for local planning")
	planCmd.Flags().StringVar(&manifestPath, "manifest", "", "specific manifest.json to compare in local planning")
	return planCmd
}

func newBackupVerifyCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <backup-ref-or-path>",
		Short: "Verify a registered backup or local backup directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref := strings.TrimSpace(args[0])
			if isLocalDirectory(ref) {
				correlationID := correlation.Normalize(opts.correlationID)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				verification, err := maintenance.VerifyBackupDirectory(ctx, ref)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				return renderBackupVerificationEnvelope(cmd, opts, correlationID, verification)
			}

			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.VerifyMaintenanceBackup(ctx, commandCtx.CorrelationID, maintenance.BackupVerifyInput{BackupRef: ref})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not verify backup.", err))
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
	}
}

func newBackupRestoreDrillCommand(opts *options) *cobra.Command {
	var targetDatabase string
	var activeDatabase string
	var owner string
	var keep bool
	var dryRun bool
	restoreCmd := &cobra.Command{
		Use:   "restore-drill <backup-ref-or-path>",
		Short: "Restore a backup into a temporary drill database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			backupDir, err := resolveBackupDirectoryForDrill(cmd, opts, args[0])
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			defer cancel()
			input := backup.RestoreDrillInput{
				BackupDir:      backupDir,
				TargetDatabase: targetDatabase,
				ActiveDatabase: activeDatabase,
				Owner:          owner,
				KeepDatabase:   keep,
			}
			if dryRun {
				plan, err := backup.PlanRestoreDrill(ctx, input)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, plan))
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
					return nil
				}
				renderRestoreDrillPlan(cmd, plan)
				return nil
			}
			result, err := backup.RunRestoreDrill(ctx, input)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderRestoreDrillResult(cmd, result)
			return nil
		},
	}
	restoreCmd.Flags().StringVar(&targetDatabase, "target-database", "", "temporary drill database name; must start with loom_restore_drill_")
	restoreCmd.Flags().StringVar(&activeDatabase, "active-database", "", "active LOOM database name to refuse as a target")
	restoreCmd.Flags().StringVar(&owner, "owner", "loom", "database owner used for pg_restore and psql")
	restoreCmd.Flags().BoolVar(&keep, "keep", false, "keep temporary drill database after verification")
	restoreCmd.Flags().BoolVar(&dryRun, "dry-run", false, "verify the backup and show the drill target without restoring")
	return restoreCmd
}

func resolveBackupDirectoryForDrill(cmd *cobra.Command, opts *options, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if isLocalDirectory(ref) {
		return ref, nil
	}
	commandCtx, err := resolveCommandContext(opts)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	envelope, err := commandCtx.Client.VerifyMaintenanceBackup(ctx, commandCtx.CorrelationID, maintenance.BackupVerifyInput{BackupRef: ref})
	if err != nil {
		return "", loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not resolve registered backup for restore drill.", err)
	}
	if isLocalDirectory(envelope.Data.BackupDir) {
		return envelope.Data.BackupDir, nil
	}
	return "", loomerrors.New("backup.restore_drill_requires_local_directory", "backup", ref, "Restore drill needs a readable local backup directory.")
}

func renderBackupVerificationEnvelope(cmd *cobra.Command, opts *options, correlationID string, verification maintenance.BackupVerification) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, verification))
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), verification.Status)
		return nil
	}
	renderMaintenanceBackupVerification(cmd, verification)
	return nil
}

func renderBackupCreatePlan(cmd *cobra.Command, plan backupCreatePlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "Backup create: %s\n", plan.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Backend: %s\n", plan.Backend)
	fmt.Fprintf(cmd.OutOrStdout(), "Production required: %t\n", plan.RequiresProduction)
	fmt.Fprintln(cmd.OutOrStdout(), "Captures:")
	for _, item := range plan.Captures {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", item)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Excludes:")
	for _, item := range plan.Excludes {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", item)
	}
}

func renderRestoreDrillPlan(cmd *cobra.Command, plan backup.RestoreDrillPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "Restore drill: %s\n", plan.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", plan.BackupDir)
	fmt.Fprintf(cmd.OutOrStdout(), "Target database: %s\n", plan.TargetDatabase)
	fmt.Fprintf(cmd.OutOrStdout(), "Active database refused: %s\n", plan.ActiveDatabase)
	fmt.Fprintf(cmd.OutOrStdout(), "Keep database: %t\n", plan.KeepDatabase)
}

func renderRestoreDrillResult(cmd *cobra.Command, result backup.RestoreDrillResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Restore drill: %s\n", result.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup: %s\n", result.BackupDir)
	fmt.Fprintf(cmd.OutOrStdout(), "Target database: %s\n", result.TargetDatabase)
	if result.KeptDatabase {
		fmt.Fprintln(cmd.OutOrStdout(), "Cleanup: kept")
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Cleanup: dropped")
	}
	if len(result.DatabaseSummary) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "FIELD\tVALUE")
		for key, value := range result.DatabaseSummary {
			fmt.Fprintf(writer, "%s\t%v\n", key, value)
		}
		_ = writer.Flush()
	}
}

func renderBackupCoverage(cmd *cobra.Command, report backupcoverage.Report) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM backup coverage: %s\n", report.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Coverage mode: %s\n", firstNonEmpty(report.Mode, backupcoverage.ModeLocal))
	if report.BackupRoot != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Backup root: %s\n", report.BackupRoot)
	}
	if report.LatestManifestPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Latest manifest: %s\n", report.LatestManifestPath)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tITEM\tPATH\tDETAIL")
	for _, entry := range report.Entries {
		detail := entry.Message
		if detail == "" {
			detail = entry.Policy
		}
		if detail == "" && entry.Exists {
			detail = fmt.Sprintf("%d files, %d bytes", entry.FileCount, entry.TotalBytes)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", entry.Status, entry.Label, entry.Path, detail)
	}
	_ = writer.Flush()
	if len(report.CriticalMissing) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Critical missing: %s\n", strings.Join(report.CriticalMissing, ", "))
	}
}

func renderBackupContractList(cmd *cobra.Command, result backupcontracts.ListResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM backup contracts: %d\n", len(result.Contracts))
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tKEY\tTARGET\tPATH")
	for _, record := range result.Contracts {
		target := ""
		if record.Contract != nil {
			target = record.Contract.Target.Path
		}
		status := firstNonEmpty(record.Status, "invalid")
		if record.Error != "" {
			status = "invalid"
			target = record.Error
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", status, record.Key, target, record.Path)
	}
	_ = writer.Flush()
	if len(result.Problems) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Problems:")
		for _, problem := range result.Problems {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", firstNonEmpty(problem.Key, problem.Path), problem.Message)
		}
	}
}

func renderProtectedFolderList(cmd *cobra.Command, result backupcontracts.ProtectedFolderListResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Protected folders: %d\n", len(result.Folders))
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tKEY\tNODE\tPATH\tLAST BACKUP")
	for _, folder := range result.Folders {
		lastBackup := "-"
		if folder.LastBackupAcceptedAt != nil {
			lastBackup = folder.LastBackupAcceptedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", folder.Lifecycle, folder.Key, firstNonEmpty(folder.OwnerNodeKey, folder.Contract.OwnerNode, "-"), folder.Contract.Target.Path, lastBackup)
	}
	_ = writer.Flush()
	if len(result.Counts) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Counts: protected=%d active=%d activating=%d waiting=%d attention=%d disabled=%d\n", result.Counts[backupcontracts.ProtectedFolderStatusProtected], result.Counts[backupcontracts.ProtectedFolderStatusActive], result.Counts[backupcontracts.ProtectedFolderStatusActivating], result.Counts[backupcontracts.ProtectedFolderStatusWaitingForNode], result.Counts[backupcontracts.ProtectedFolderStatusAttention], result.Counts[backupcontracts.ProtectedFolderStatusDisabled])
	}
}

func renderBackupContractMigration(cmd *cobra.Command, result backupcontracts.MigrateIgnorePolicyResponse) {
	mode := "dry run"
	if result.Migration.Applied {
		mode = "applied"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM backup contract ignore-policy migration: %s\n", mode)
	fmt.Fprintf(cmd.OutOrStdout(), "Changed: %d\n", result.Migration.ChangedCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Needs attention: %d\n", result.Migration.AttentionCount)
	fmt.Fprintf(cmd.OutOrStdout(), "Watched roots reconciled: %t\n", result.Reconciled)
	for _, item := range result.Migration.Items {
		fmt.Fprintf(cmd.OutOrStdout(), "- %s: %s", item.Key, item.Status)
		if item.Reason != "" {
			fmt.Fprintf(cmd.OutOrStdout(), " (%s)", item.Reason)
		}
		fmt.Fprintln(cmd.OutOrStdout())
	}
}

func renderBackupContractRecord(cmd *cobra.Command, record backupcontracts.ContractRecord) {
	fmt.Fprintf(cmd.OutOrStdout(), "Backup contract: %s\n", record.Key)
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", record.Path)
	if record.Error != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Status: invalid\nError: %s\n", record.Error)
		return
	}
	if record.Contract == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", firstNonEmpty(record.Status, "unknown"))
		return
	}
	contract := record.Contract
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", contract.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", contract.OwnerNode)
	fmt.Fprintf(cmd.OutOrStdout(), "Target scope: %s\n", contract.Target.Scope)
	fmt.Fprintf(cmd.OutOrStdout(), "Target path: %s\n", contract.Target.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup mode: %s\n", contract.Backup.Mode)
	if len(contract.Include) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Include: %s\n", strings.Join(contract.Include, ", "))
	}
	if len(contract.Exclude) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Exclude: %s\n", strings.Join(contract.Exclude, ", "))
	}
}

func renderProtectedFolderRecord(cmd *cobra.Command, record backupcontracts.ProtectedFolderRecord) {
	fmt.Fprintf(cmd.OutOrStdout(), "Protected folder: %s\n", record.Key)
	fmt.Fprintf(cmd.OutOrStdout(), "Lifecycle: %s\n", record.Lifecycle)
	fmt.Fprintf(cmd.OutOrStdout(), "Owner node: %s\n", firstNonEmpty(record.OwnerNodeKey, record.Contract.OwnerNode, "unknown"))
	fmt.Fprintf(cmd.OutOrStdout(), "Target path: %s\n", record.Contract.Target.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "Backup mode: %s\n", record.Contract.Backup.Mode)
	if record.Contract.Ignore != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Ignore policy: %s (discover user rules: %t)\n", record.Contract.Ignore.Profile, record.Contract.Ignore.DiscoverUserRules)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Desired revision: %d\nApplied revision: %d\n", record.DesiredRevision, record.AppliedRevision)
	fmt.Fprintf(cmd.OutOrStdout(), "Node applied: %t\nRoot reported: %t\nConfiguration matches: %t\n", record.NodeApplied, record.RootReported, record.ConfigHashMatches)
	if record.LastBackupAcceptedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last accepted backup: %s\n", record.LastBackupAcceptedAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Last accepted backup: none")
	}
	if record.AttentionReason != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Attention: %s\n", record.AttentionReason)
	}
}

func renderBackupPreflightEnvelope(cmd *cobra.Command, opts *options, envelope response.Envelope[backupcontracts.PreflightRecord]) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.PreflightID)
		return nil
	}
	record := envelope.Data
	fmt.Fprintf(cmd.OutOrStdout(), "Folder preflight: %s\nStatus: %s\nNode: %s\nPath: %s\n", record.PreflightID, record.Status, record.TargetNodeID, record.RequestedPath)
	if record.Status == backupcontracts.PreflightStatusPending {
		fmt.Fprintln(cmd.OutOrStdout(), "Result: queued; waiting for owner node")
	}
	if record.Result != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Readable: %t\nPolicy profile: %s\nIncluded: %d files, %d bytes\nIgnored: %d files, %d bytes\n", record.Result.Readable, firstNonEmpty(record.Result.Policy.Profile, "managed"), record.Result.Policy.IncludedCount, record.Result.Policy.IncludedBytes, record.Result.Policy.IgnoredCount, record.Result.Policy.IgnoredBytes)
		if len(record.Result.Policy.PolicyFiles) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "Policy files: none discovered")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Policy files: %s\n", strings.Join(record.Result.Policy.PolicyFiles, ", "))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Estimate truncated: %t\n", record.Result.Truncated)
		for _, finding := range record.Result.Findings {
			fmt.Fprintf(cmd.OutOrStdout(), "Finding [%s]: %s\n", firstNonEmpty(finding.Severity, "info"), firstNonEmpty(finding.Message, finding.Code))
		}
	}
	if record.ErrorMessage != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Attention: %s\n", record.ErrorMessage)
	}
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func renderBackupContractLifecycleEnvelope(cmd *cobra.Command, opts *options, envelope response.Envelope[backupcontracts.LifecycleResult]) error {
	if opts.jsonOutput {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
	}
	if opts.plainOutput {
		fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Action)
		return nil
	}
	renderBackupContractLifecycle(cmd, envelope.Data)
	renderResponseMeta(cmd, opts, envelope.Meta)
	return nil
}

func renderBackupContractLifecycle(cmd *cobra.Command, result backupcontracts.LifecycleResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "Backup contract: %s\n", result.Contract.Key)
	fmt.Fprintf(cmd.OutOrStdout(), "Action: %s\n", result.Action)
	fmt.Fprintf(cmd.OutOrStdout(), "Dry run: %t\n", result.DryRun)
	if result.Path != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", result.Path)
	}
	if result.Contract.Target.Path != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Target: %s\n", result.Contract.Target.Path)
	}
	if result.Contract.Status != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", result.Contract.Status)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Desired state registered: %t\n", result.DesiredStateRegistered)
	fmt.Fprintf(cmd.OutOrStdout(), "Node work queued: %t\n", result.NodeApplyQueued)
	fmt.Fprintf(cmd.OutOrStdout(), "Node applied: %t\n", result.NodeApplied)
	if result.DesiredRevision > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Desired revision: %d\n", result.DesiredRevision)
	}
	if result.AppliedRevision > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Applied revision: %d\n", result.AppliedRevision)
	}
	if result.NodeApplyQueued && !result.NodeApplied {
		fmt.Fprintln(cmd.OutOrStdout(), "Protection state: queued; waiting for owner-node acknowledgement")
	}
	if len(result.WatchedRoots) > 0 {
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "ROOT\tNODE\tBACKUP\tINDEX\tPATH")
		for _, root := range result.WatchedRoots {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", root.BackendRootKey, root.OwnerNode, root.BackupMode, root.IndexMode, root.RootRelativePath)
		}
		_ = writer.Flush()
	}
	if len(result.Problems) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "Problems:")
		for _, problem := range result.Problems {
			fmt.Fprintf(cmd.OutOrStdout(), "  - %s: %s\n", firstNonEmpty(problem.Key, problem.Path), problem.Message)
		}
	}
}

func renderBackupContractPlan(cmd *cobra.Command, plan backupcoverage.ContractPlan) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM backup contracts: %s\n", plan.Status)
	fmt.Fprintf(cmd.OutOrStdout(), "Coverage mode: %s\n", firstNonEmpty(plan.Mode, backupcoverage.ModeLocal))
	fmt.Fprintf(cmd.OutOrStdout(), "Satisfied: %d  Needs attention: %d  Critical: %d\n", plan.Summary.Satisfied, plan.Summary.NeedsAttention, plan.Summary.Critical)
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tCONTRACT\tCOVERAGE\tACTION")
	for _, contract := range plan.Contracts {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n",
			contract.ContractStatus,
			contract.Label,
			contract.CoverageStatus,
			contract.Action,
		)
	}
	_ = writer.Flush()
}

func defaultBackupExclusions() []string {
	return []string{
		"wireguard_private_keys",
		"ssh_private_keys",
		"node_credential_tokens",
		"enrollment_tokens",
		"environment_secret_files",
		"raw_loom_db_url",
	}
}

func isLocalDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func isProductionMainHealth(environment, nodeID, nodeRole string) bool {
	if !ops.IsProductionEnvironment(environment) {
		return false
	}
	nodeID = strings.ToLower(strings.TrimSpace(nodeID))
	nodeRole = strings.ToLower(strings.TrimSpace(nodeRole))
	return nodeID == "main" || nodeRole == "main"
}
