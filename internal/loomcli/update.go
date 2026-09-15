package loomcli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	"loom.local/loom/internal/db"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/setup"
	"loom.local/loom/internal/update"
)

type updateFlags struct {
	releasePath         string
	releasesDir         string
	stateDir            string
	manifestPath        string
	activePath          string
	activeMigrationsDir string
	targetMigrationsDir string
	homeDir             string
	localBinDir         string
	nodeKey             string
	mainHost            string
	serviceManager      string
	launchAgentLabel    string
	launchAgentPlist    string
	loomBinary          string
	nodeAgentBinary     string
	flakeOutput         string
	backupPath          string
	backupRef           string
	backupScope         string
	toReleasePath       string
	currentMigration    int64
	limit               int
	yes                 bool
	dryRun              bool
	allowNonProduction  bool
	skipBackup          bool
	skipRebuild         bool
	skipHealthCheck     bool
	noPauseSchedules    bool
	noPauseDirectEvents bool
	noPauseWorkers      bool
	serviceOnly         bool
	restoreRequired     bool
	skipServiceRestart  bool
	sourcePath          string
	stagePath           string
	targetPath          string
	releaseID           string
	version             string
	commit              string
	planHash            string
	pins                []string
	keepSuccessful      int
	protectYoungerThan  time.Duration
	workspace           bool
	overwrite           bool
}

func newUpdateCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Plan, apply, rollback, and inspect LOOM production updates",
	}
	cmd.AddCommand(newUpdateStatusCommand(opts))
	cmd.AddCommand(newUpdatePlanCommand(opts))
	cmd.AddCommand(newUpdateApplyCommand(opts))
	cmd.AddCommand(newUpdateRollbackCommand(opts))
	cmd.AddCommand(newUpdateMaintenanceCommand(opts))
	cmd.AddCommand(newUpdateManifestCommand(opts))
	cmd.AddCommand(newUpdateHistoryCommand(opts))
	cmd.AddCommand(newUpdateWorkspaceCommand(opts))
	cmd.AddCommand(newUpdateReleaseCommand(opts))
	return cmd
}

func newUpdateReleaseCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Prepare release artifacts for production updates",
	}
	cmd.AddCommand(newUpdateReleaseStageCommand(opts))
	cmd.AddCommand(newUpdateReleaseRetentionCommand(opts))
	return cmd
}

func newUpdateReleaseRetentionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Plan and apply bounded retention for immutable release copies",
	}
	cmd.AddCommand(newUpdateReleaseRetentionPlanCommand(opts))
	cmd.AddCommand(newUpdateReleaseRetentionApplyCommand(opts))
	return cmd
}

func newUpdateReleaseRetentionPlanCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Build a read-only release retention plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			input, err := releaseRetentionInputFromFlags(flags)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			plan, err := update.PlanReleaseRetention(input)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(plan); err != nil {
					return err
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanHash)
			} else {
				update.RenderReleaseRetentionPlan(cmd.OutOrStdout(), plan)
			}
			if update.IsReleaseRetentionBlocked(plan) {
				return fmt.Errorf("release retention plan is blocked")
			}
			return nil
		},
	}
	addUpdateReleaseRetentionPlanFlags(cmd, &flags)
	return cmd
}

func newUpdateReleaseRetentionApplyCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Delete releases from an unchanged reviewed retention plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			planInput, err := releaseRetentionInputFromFlags(flags)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			result, err := update.ApplyReleaseRetention(update.ReleaseRetentionApplyInput{
				PlanInput:        planInput,
				ExpectedPlanHash: flags.planHash,
				Yes:              flags.yes,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if err := json.NewEncoder(cmd.OutOrStdout()).Encode(result); err != nil {
					return err
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderReleaseRetentionApplyResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("release retention apply refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addUpdateReleaseRetentionPlanFlags(cmd, &flags)
	cmd.Flags().StringVar(&flags.planHash, "plan-hash", "", "exact hash from the reviewed retention plan")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm release deletion")
	_ = cmd.MarkFlagRequired("plan-hash")
	return cmd
}

func newUpdateReleaseStageCommand(opts *options) *cobra.Command {
	flags := updateFlags{sourcePath: "."}
	cmd := &cobra.Command{
		Use:   "stage --stage-path <path> --target-path <path>",
		Short: "Copy the current release tree and write a target-safe release manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			backupScope, err := releaseBackupScopeFromFlag(flags.backupScope)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			result, err := update.StageRelease(ctx, update.ReleaseStageInput{
				SourcePath:  flags.sourcePath,
				StagePath:   flags.stagePath,
				TargetPath:  flags.targetPath,
				ReleaseID:   flags.releaseID,
				Version:     flags.version,
				Commit:      flags.commit,
				FlakeOutput: flags.flakeOutput,
				BackupScope: backupScope,
				Overwrite:   flags.overwrite,
				DryRun:      flags.dryRun,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.StagePath)
				return nil
			}
			update.RenderReleaseStageResult(cmd.OutOrStdout(), result)
			return nil
		},
	}
	addUpdateReleaseStageFlags(cmd, &flags)
	return cmd
}

func releaseBackupScopeFromFlag(value string) (*update.ReleaseBackupScope, error) {
	switch value {
	case "":
		return nil, nil
	case update.ReleaseBackupScopeOperationalOnly, update.ReleaseBackupScopeCanonicalUserData:
		return &update.ReleaseBackupScope{
			SchemaVersion: update.ReleaseBackupScopeSchemaVersion,
			Class:         value,
		}, nil
	default:
		return nil, fmt.Errorf("--backup-scope must be %q or %q", update.ReleaseBackupScopeOperationalOnly, update.ReleaseBackupScopeCanonicalUserData)
	}
}

func newUpdateWorkspaceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Plan, apply, rollback, and inspect workspace updates",
	}
	cmd.AddCommand(newUpdateWorkspacePlanCommand(opts))
	cmd.AddCommand(newUpdateWorkspaceApplyCommand(opts))
	cmd.AddCommand(newUpdateWorkspaceRollbackCommand(opts))
	cmd.AddCommand(newUpdateWorkspaceStatusCommand(opts))
	cmd.AddCommand(newUpdateWorkspaceHistoryCommand(opts))
	return cmd
}

func newUpdateWorkspacePlanCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "plan --release-path <path>",
		Short: "Build a read-only workspace update plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			plan, err := update.PlanWorkspaceUpdate(ctx, update.WorkspaceUpdatePlanInput{Spec: workspaceUpdateSpecFromFlags(flags)})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanHash)
				return nil
			}
			update.RenderWorkspacePlan(cmd.OutOrStdout(), plan)
			return nil
		},
	}
	addWorkspaceUpdatePlanFlags(cmd, &flags)
	return cmd
}

func newUpdateWorkspaceApplyCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "apply --release-path <path> --yes",
		Short: "Apply a workspace update by switching local release symlinks",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := update.ApplyWorkspaceUpdate(ctx, update.WorkspaceApplyInput{
				Spec:   workspaceUpdateSpecFromFlags(flags),
				Yes:    flags.yes,
				DryRun: flags.dryRun,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderWorkspaceApplyResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("workspace update apply refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addWorkspaceUpdatePlanFlags(cmd, &flags)
	addWorkspaceUpdateApplyFlags(cmd, &flags)
	return cmd
}

func newUpdateWorkspaceRollbackCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "rollback --yes",
		Short: "Rollback the active workspace update manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			result, err := update.RollbackWorkspaceUpdate(ctx, update.WorkspaceRollbackInput{
				StateDir:           flags.stateDir,
				ManifestPath:       flags.manifestPath,
				ToReleasePath:      flags.toReleasePath,
				HomeDir:            flags.homeDir,
				ActivePath:         flags.activePath,
				LocalBinDir:        flags.localBinDir,
				NodeKey:            flags.nodeKey,
				MainHost:           flags.mainHost,
				ServiceManager:     flags.serviceManager,
				LaunchAgentLabel:   flags.launchAgentLabel,
				LaunchAgentPlist:   flags.launchAgentPlist,
				LoomBinary:         flags.loomBinary,
				NodeAgentBinary:    flags.nodeAgentBinary,
				SkipServiceRestart: flags.skipServiceRestart,
				SkipHealthCheck:    flags.skipHealthCheck,
				Yes:                flags.yes,
				DryRun:             flags.dryRun,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderRollbackResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("workspace update rollback refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addWorkspaceUpdateRollbackFlags(cmd, &flags)
	return cmd
}

func newUpdateWorkspaceStatusCommand(opts *options) *cobra.Command {
	flags := updateFlags{limit: 20}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show workspace update state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			status := update.Status(update.StatusInput{StateDir: workspaceUpdateStateDir(flags), Limit: flags.limit})
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			if opts.plainOutput {
				if status.Active != nil {
					fmt.Fprintln(cmd.OutOrStdout(), status.Active.Status)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "none")
				}
				return nil
			}
			update.RenderStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	addWorkspaceUpdateStateFlags(cmd, &flags)
	return cmd
}

func newUpdateWorkspaceHistoryCommand(opts *options) *cobra.Command {
	flags := updateFlags{limit: 20}
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List workspace update history",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			history, diagnostics := update.ListHistory(workspaceUpdateStateDir(flags), flags.limit)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					History     []update.UpdateManifest   `json:"history"`
					Diagnostics []update.UpdateDiagnostic `json:"diagnostics,omitempty"`
				}{History: history, Diagnostics: diagnostics})
			}
			if opts.plainOutput {
				for _, item := range history {
					fmt.Fprintln(cmd.OutOrStdout(), item.UpdateID)
				}
				return nil
			}
			update.RenderHistory(cmd.OutOrStdout(), history)
			for _, diagnostic := range diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
			}
			return nil
		},
	}
	addWorkspaceUpdateStateFlags(cmd, &flags)
	return cmd
}

func newUpdatePlanCommand(opts *options) *cobra.Command {
	flags := updateFlags{currentMigration: -1}
	cmd := &cobra.Command{
		Use:   "plan --release-path <path>",
		Short: "Build a read-only production update plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			spec := updateSpecFromFlags(flags, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			plan, err := update.Plan(ctx, update.PlannerInput{Spec: spec})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(plan)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.PlanHash)
				return nil
			}
			update.RenderPlan(cmd.OutOrStdout(), plan)
			return nil
		},
	}
	addUpdatePlanFlags(cmd, &flags)
	return cmd
}

func newUpdateApplyCommand(opts *options) *cobra.Command {
	flags := updateFlags{currentMigration: -1}
	cmd := &cobra.Command{
		Use:   "apply --release-path <path> --backup-path <path> --yes",
		Short: "Apply a planned LOOM production update",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			correlationID := correlation.Normalize(opts.correlationID)
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile, SocketPath: opts.socketPath})
			if cfgErr != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			client, clientErr := resolveCLIClient(cfg, opts)
			if clientErr != nil {
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("config.invalid", "runtime", "transport", "Configuration transport is invalid.", clientErr))
			}
			spec := updateSpecFromFlags(flags, cfg)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			policy := maintenancePolicyFromFlags(flags)
			var coordinator update.MaintenanceCoordinator
			if flags.yes {
				resolvedCoordinator, closeCoordinator, coordErr := updateMaintenanceCoordinator(ctx, cfg, flags, policy, correlationID, flags.dryRun)
				if coordErr != nil {
					return renderError(cmd, opts, correlationID, loomerrors.Wrap("update.maintenance_coordinator_unavailable", "update", flags.releasePath, "Could not prepare update maintenance window.", coordErr))
				}
				defer closeCoordinator()
				coordinator = resolvedCoordinator
			}
			result, err := update.Apply(ctx, update.ApplyInput{
				Spec:               spec,
				Runtime:            updateRuntimeIdentity(cfg),
				Yes:                flags.yes,
				DryRun:             flags.dryRun,
				AllowNonProduction: flags.allowNonProduction,
				SkipBackup:         flags.skipBackup,
				SkipRebuild:        flags.skipRebuild,
				SkipHealthCheck:    flags.skipHealthCheck,
				BackupPath:         flags.backupPath,
				BackupRef:          flags.backupRef,
				BackupVerifier:     updateBackupVerifier(client, correlationID),
				MaintenancePolicy:  policy,
				Maintenance:        coordinator,
			})
			if err != nil {
				if !opts.jsonOutput && !opts.plainOutput && result.Status != "" {
					update.RenderApplyResult(cmd.ErrOrStderr(), result)
				}
				return renderError(cmd, opts, correlationID, loomerrors.Wrap("update.apply_failed", "update", flags.releasePath, "Update apply failed.", err))
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderApplyResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("update apply refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addUpdatePlanFlags(cmd, &flags)
	addUpdateApplyFlags(cmd, &flags)
	return cmd
}

func updateBackupVerifier(client localclient.Client, correlationID string) update.BackupVerifier {
	return func(ctx context.Context, input update.BackupVerificationInput) (maintenance.BackupVerification, error) {
		envelope, err := client.VerifyMaintenanceBackup(ctx, correlationID, maintenance.BackupVerifyInput{BackupRef: input.BackupRef})
		if err != nil {
			return maintenance.BackupVerification{}, err
		}
		return envelope.Data, nil
	}
}

func newUpdateRollbackCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "rollback --service-only --yes",
		Short: "Rollback the active LOOM update manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			result, err := update.Rollback(ctx, update.RollbackInput{
				StateDir:           firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir)),
				ManifestPath:       flags.manifestPath,
				ToReleasePath:      flags.toReleasePath,
				Yes:                flags.yes,
				DryRun:             flags.dryRun,
				AllowNonProduction: flags.allowNonProduction,
				ServiceOnly:        flags.serviceOnly,
				RestoreRequired:    flags.restoreRequired,
				SkipRebuild:        flags.skipRebuild,
				SkipHealthCheck:    flags.skipHealthCheck,
				Runtime:            updateRuntimeIdentity(cfg),
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderRollbackResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("update rollback refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addUpdateRollbackFlags(cmd, &flags)
	return cmd
}

func newUpdateMaintenanceCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maintenance",
		Short: "Inspect or resume an update maintenance window",
	}
	cmd.AddCommand(newUpdateMaintenanceStatusCommand(opts))
	cmd.AddCommand(newUpdateMaintenanceResumeCommand(opts))
	return cmd
}

func newUpdateMaintenanceStatusCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the active update maintenance window",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			path := strings.TrimSpace(flags.manifestPath)
			if path == "" {
				path = update.ActiveManifestPath(firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir)))
			}
			manifest, err := update.ReadUpdateManifest(path)
			if err != nil {
				if !os.IsNotExist(err) {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
			}
			activeExists := err == nil
			updateID := ""
			var window *update.MaintenanceWindow
			if activeExists {
				updateID = manifest.UpdateID
				window = update.MaintenanceStatusFromManifest(manifest)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					ActiveExists      bool                      `json:"active_exists"`
					UpdateID          string                    `json:"update_id"`
					ManifestPath      string                    `json:"manifest_path"`
					MaintenanceWindow *update.MaintenanceWindow `json:"maintenance_window,omitempty"`
				}{ActiveExists: activeExists, UpdateID: updateID, ManifestPath: path, MaintenanceWindow: window})
			}
			if opts.plainOutput {
				if window == nil {
					fmt.Fprintln(cmd.OutOrStdout(), "none")
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), window.Status)
				}
				return nil
			}
			update.RenderMaintenanceWindow(cmd.OutOrStdout(), path, updateID, window)
			return nil
		},
	}
	addUpdateMaintenanceFlags(cmd, &flags)
	return cmd
}

func newUpdateMaintenanceResumeCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "resume --yes",
		Short: "Resume resources paused by an update maintenance window",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			policy := update.DefaultMaintenancePausePolicy()
			coordinator, closeCoordinator, coordErr := updateMaintenanceCoordinator(ctx, cfg, flags, policy, correlation.Normalize(opts.correlationID), false)
			if coordErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), coordErr)
			}
			defer closeCoordinator()
			result, err := update.ResumeMaintenance(ctx, update.ResumeMaintenanceInput{
				StateDir:           firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir)),
				ManifestPath:       flags.manifestPath,
				Yes:                flags.yes,
				AllowNonProduction: flags.allowNonProduction,
				Runtime:            updateRuntimeIdentity(cfg),
				Maintenance:        coordinator,
			})
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				if encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result); encodeErr != nil {
					return encodeErr
				}
			} else if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
			} else {
				update.RenderResumeMaintenanceResult(cmd.OutOrStdout(), result)
			}
			if result.Refused {
				return fmt.Errorf("update maintenance resume refused: %s", result.Refusal)
			}
			return nil
		},
	}
	addUpdateMaintenanceFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the maintenance resume operation")
	cmd.Flags().BoolVar(&flags.allowNonProduction, "allow-non-production", false, "allow dev/staging maintenance resume drills outside production main")
	return cmd
}

func newUpdateStatusCommand(opts *options) *cobra.Command {
	flags := updateFlags{limit: 20}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local update state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			stateDir := firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir))
			status := update.Status(update.StatusInput{StateDir: stateDir, Limit: flags.limit})
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			if opts.plainOutput {
				if status.Active != nil {
					fmt.Fprintln(cmd.OutOrStdout(), status.Active.Status)
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "none")
				}
				return nil
			}
			update.RenderStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	addUpdateStateFlags(cmd, &flags)
	return cmd
}

func newUpdateManifestCommand(opts *options) *cobra.Command {
	flags := updateFlags{}
	cmd := &cobra.Command{
		Use:   "manifest",
		Short: "Inspect update manifests",
	}
	inspectCmd := &cobra.Command{
		Use:   "inspect [path]",
		Short: "Inspect an update manifest",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			path := ""
			if len(args) > 0 {
				path = strings.TrimSpace(args[0])
			}
			if path == "" {
				cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
				if cfgErr != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
				}
				stateDir := firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir))
				path = update.ActiveManifestPath(stateDir)
			}
			manifest, err := update.ReadUpdateManifest(path)
			if err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(manifest)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), manifest.UpdateID)
				return nil
			}
			update.RenderManifest(cmd.OutOrStdout(), path, manifest)
			return nil
		},
	}
	addUpdateStateFlags(inspectCmd, &flags)
	cmd.AddCommand(inspectCmd)
	return cmd
}

func newUpdateHistoryCommand(opts *options) *cobra.Command {
	flags := updateFlags{limit: 20}
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List update history manifests",
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := resolveOutputMode(opts); err != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
			}
			cfg, cfgErr := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if cfgErr != nil {
				return renderError(cmd, opts, correlation.Normalize(opts.correlationID), loomerrors.Wrap("config.invalid", "runtime", "config", "Configuration is invalid.", cfgErr))
			}
			stateDir := firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir))
			history, diagnostics := update.ListHistory(stateDir, flags.limit)
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
					History     []update.UpdateManifest   `json:"history"`
					Diagnostics []update.UpdateDiagnostic `json:"diagnostics,omitempty"`
				}{History: history, Diagnostics: diagnostics})
			}
			if opts.plainOutput {
				for _, item := range history {
					fmt.Fprintln(cmd.OutOrStdout(), item.UpdateID)
				}
				return nil
			}
			update.RenderHistory(cmd.OutOrStdout(), history)
			for _, diagnostic := range diagnostics {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
			}
			return nil
		},
	}
	addUpdateStateFlags(cmd, &flags)
	return cmd
}

func addUpdatePlanFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.releasePath, "release-path", "", "target release path to plan")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "update state directory")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "install manifest path")
	cmd.Flags().StringVar(&flags.activePath, "active-path", "", "active source path")
	cmd.Flags().StringVar(&flags.activeMigrationsDir, "active-migrations-dir", "", "active migration directory")
	cmd.Flags().StringVar(&flags.targetMigrationsDir, "target-migrations-dir", "", "target migration directory")
	cmd.Flags().StringVar(&flags.flakeOutput, "flake", "", "target Nix flake output; defaults to release manifest or .#loom-main")
	cmd.Flags().Int64Var(&flags.currentMigration, "current-migration", -1, "override current database migration version for planning")
	_ = cmd.MarkFlagRequired("release-path")
}

func addUpdateReleaseStageFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.sourcePath, "source-path", ".", "source checkout path to stage")
	cmd.Flags().StringVar(&flags.stagePath, "stage-path", "", "local staging directory to write")
	cmd.Flags().StringVar(&flags.targetPath, "target-path", "", "final target path on the machine that will apply the update")
	cmd.Flags().StringVar(&flags.releaseID, "release-id", "", "release id to write into loom-release.yaml")
	cmd.Flags().StringVar(&flags.version, "version", "", "release version to write into loom-release.yaml")
	cmd.Flags().StringVar(&flags.commit, "commit", "", "commit to write into loom-release.yaml; defaults to source git HEAD")
	cmd.Flags().StringVar(&flags.flakeOutput, "flake", "", "target Nix flake output; defaults to .#loom-main")
	cmd.Flags().StringVar(&flags.backupScope, "backup-scope", "", "trusted canonical-data effect: operational_only or canonical_user_data; omission requires a complete backup")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show staging changes without writing files")
	cmd.Flags().BoolVar(&flags.overwrite, "overwrite", false, "replace an existing non-empty stage directory")
	_ = cmd.MarkFlagRequired("stage-path")
	_ = cmd.MarkFlagRequired("target-path")
}

func addUpdateReleaseRetentionPlanFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().BoolVar(&flags.workspace, "workspace", false, "use current-user workspace release defaults")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "workspace home directory; defaults to current user home")
	cmd.Flags().StringVar(&flags.releasesDir, "releases-dir", "", "release directory; defaults to /srv/loom/releases or the workspace release directory")
	cmd.Flags().StringVar(&flags.activePath, "active-path", "", "active release symlink; defaults to /srv/loom/current or the workspace current symlink")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "update state directory; defaults to /var/lib/loom/update or the workspace update state")
	cmd.Flags().IntVar(&flags.keepSuccessful, "keep-successful", 10, "number of newest existing successful update targets to retain")
	cmd.Flags().DurationVar(&flags.protectYoungerThan, "protect-younger-than", 48*time.Hour, "retain releases newer than this duration")
	cmd.Flags().StringArrayVar(&flags.pins, "pin", nil, "release name or direct path to retain; repeatable")
}

func addUpdateApplyFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.backupPath, "backup-path", "", "verified backup directory to bind to this update")
	cmd.Flags().StringVar(&flags.backupRef, "backup-ref", "", "operator-readable backup reference")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the mutating update operation")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "plan the apply without changing local state")
	cmd.Flags().BoolVar(&flags.allowNonProduction, "allow-non-production", false, "allow dev/staging update drills outside production main")
	cmd.Flags().BoolVar(&flags.skipBackup, "skip-backup", false, "skip backup verification; allowed only with --allow-non-production")
	cmd.Flags().BoolVar(&flags.skipRebuild, "skip-rebuild", false, "skip nixos-rebuild switch; dev drills only")
	cmd.Flags().BoolVar(&flags.skipHealthCheck, "skip-health-check", false, "skip post-switch loom health check; dev drills only")
	cmd.Flags().BoolVar(&flags.noPauseSchedules, "no-pause-schedules", false, "do not pause active schedules during the update maintenance window")
	cmd.Flags().BoolVar(&flags.noPauseDirectEvents, "no-pause-direct-events", false, "do not pause active direct-event endpoints during the update maintenance window")
	cmd.Flags().BoolVar(&flags.noPauseWorkers, "no-pause-workers", false, "do not record optional-worker quieting during the update maintenance window")
}

func addUpdateRollbackFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "update state directory")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "update manifest to roll back; defaults to active update manifest")
	cmd.Flags().StringVar(&flags.toReleasePath, "to", "", "release path to roll back to; defaults to the manifest active release")
	cmd.Flags().BoolVar(&flags.serviceOnly, "service-only", false, "confirm this rollback is service-only and does not restore the database")
	cmd.Flags().BoolVar(&flags.restoreRequired, "restore-required", false, "print database restore runbook instead of attempting service rollback")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the rollback operation")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "plan the rollback without changing local state")
	cmd.Flags().BoolVar(&flags.allowNonProduction, "allow-non-production", false, "allow dev/staging rollback drills outside production main")
	cmd.Flags().BoolVar(&flags.skipRebuild, "skip-rebuild", false, "skip nixos-rebuild switch; dev drills only")
	cmd.Flags().BoolVar(&flags.skipHealthCheck, "skip-health-check", false, "skip post-switch loom health check; dev drills only")
}

func addUpdateStateFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "update state directory")
	cmd.Flags().IntVar(&flags.limit, "limit", 20, "maximum history entries to return")
}

func addUpdateMaintenanceFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "update state directory")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "update manifest path; defaults to active update manifest")
}

func addWorkspaceUpdatePlanFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.releasePath, "release-path", "", "target workspace release path")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "workspace home directory; defaults to current user home")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "workspace update state directory")
	cmd.Flags().StringVar(&flags.activePath, "active-path", "", "workspace active release symlink")
	cmd.Flags().StringVar(&flags.localBinDir, "local-bin-dir", "", "workspace local binary directory")
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", setup.DefaultWorkspaceNodeKey, "workspace node key")
	cmd.Flags().StringVar(&flags.mainHost, "main-host", update.DefaultWorkspaceMainHost, "SSH host used for main-side node health checks")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", update.DefaultWorkspaceService, "workspace service manager")
	cmd.Flags().StringVar(&flags.launchAgentLabel, "launch-agent-label", setup.LaunchAgentLabel, "workspace LaunchAgent label")
	cmd.Flags().StringVar(&flags.launchAgentPlist, "launch-agent-plist", "", "workspace LaunchAgent plist path")
	cmd.Flags().StringVar(&flags.loomBinary, "loom-binary", "", "installed loom binary path")
	cmd.Flags().StringVar(&flags.nodeAgentBinary, "node-agent-binary", "", "installed loom-node-agent binary path")
	cmd.Flags().BoolVar(&flags.skipServiceRestart, "skip-service-restart", false, "skip LaunchAgent stop/start")
	cmd.Flags().BoolVar(&flags.skipHealthCheck, "skip-health-check", false, "skip workspace post-update health checks")
	_ = cmd.MarkFlagRequired("release-path")
}

func addWorkspaceUpdateApplyFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the mutating workspace update")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "plan the workspace update without changing local state")
}

func addWorkspaceUpdateRollbackFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "workspace update state directory")
	cmd.Flags().StringVar(&flags.manifestPath, "manifest", "", "workspace update manifest to roll back; defaults to active workspace update manifest")
	cmd.Flags().StringVar(&flags.toReleasePath, "to", "", "release path to roll back to; defaults to the manifest active release")
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "workspace home directory; defaults to current user home")
	cmd.Flags().StringVar(&flags.activePath, "active-path", "", "workspace active release symlink")
	cmd.Flags().StringVar(&flags.localBinDir, "local-bin-dir", "", "workspace local binary directory")
	cmd.Flags().StringVar(&flags.nodeKey, "node-key", setup.DefaultWorkspaceNodeKey, "workspace node key")
	cmd.Flags().StringVar(&flags.mainHost, "main-host", update.DefaultWorkspaceMainHost, "SSH host used for main-side node health checks")
	cmd.Flags().StringVar(&flags.serviceManager, "service-manager", update.DefaultWorkspaceService, "workspace service manager")
	cmd.Flags().StringVar(&flags.launchAgentLabel, "launch-agent-label", setup.LaunchAgentLabel, "workspace LaunchAgent label")
	cmd.Flags().StringVar(&flags.launchAgentPlist, "launch-agent-plist", "", "workspace LaunchAgent plist path")
	cmd.Flags().StringVar(&flags.loomBinary, "loom-binary", "", "installed loom binary path")
	cmd.Flags().StringVar(&flags.nodeAgentBinary, "node-agent-binary", "", "installed loom-node-agent binary path")
	cmd.Flags().BoolVar(&flags.skipServiceRestart, "skip-service-restart", false, "skip LaunchAgent stop/start")
	cmd.Flags().BoolVar(&flags.skipHealthCheck, "skip-health-check", false, "skip workspace post-rollback health checks")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "confirm the mutating workspace rollback")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "plan the workspace rollback without changing local state")
}

func addWorkspaceUpdateStateFlags(cmd *cobra.Command, flags *updateFlags) {
	cmd.Flags().StringVar(&flags.homeDir, "home-dir", "", "workspace home directory; defaults to current user home")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "workspace update state directory")
	cmd.Flags().IntVar(&flags.limit, "limit", 20, "maximum history entries to return")
}

func updateRuntimeIdentity(cfg config.Config) update.RuntimeIdentity {
	return update.RuntimeIdentity{
		Environment: cfg.Env,
		NodeID:      cfg.NodeID,
		NodeRole:    cfg.NodeRole,
	}
}

func workspaceUpdateSpecFromFlags(flags updateFlags) update.WorkspaceUpdateSpec {
	return update.WorkspaceUpdateSpec{
		ReleasePath:        flags.releasePath,
		HomeDir:            flags.homeDir,
		StateDir:           flags.stateDir,
		ActivePath:         flags.activePath,
		LocalBinDir:        flags.localBinDir,
		NodeKey:            flags.nodeKey,
		MainHost:           flags.mainHost,
		ServiceManager:     flags.serviceManager,
		LaunchAgentLabel:   flags.launchAgentLabel,
		LaunchAgentPlist:   flags.launchAgentPlist,
		LoomBinary:         flags.loomBinary,
		NodeAgentBinary:    flags.nodeAgentBinary,
		SkipServiceRestart: flags.skipServiceRestart,
		SkipHealthCheck:    flags.skipHealthCheck,
	}
}

func workspaceUpdateStateDir(flags updateFlags) string {
	if strings.TrimSpace(flags.stateDir) != "" {
		return flags.stateDir
	}
	home := strings.TrimSpace(flags.homeDir)
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if home == "" {
		return update.DefaultStateDir("")
	}
	return filepath.Join(home, ".local", "state", "loom", "update")
}

func releaseRetentionInputFromFlags(flags updateFlags) (update.ReleaseRetentionInput, error) {
	releasesDir := strings.TrimSpace(flags.releasesDir)
	activePath := strings.TrimSpace(flags.activePath)
	stateDir := strings.TrimSpace(flags.stateDir)
	if flags.workspace {
		home := strings.TrimSpace(flags.homeDir)
		if home == "" {
			resolved, err := os.UserHomeDir()
			if err != nil {
				return update.ReleaseRetentionInput{}, fmt.Errorf("resolve workspace home: %w", err)
			}
			home = resolved
		}
		absoluteHome, err := filepath.Abs(home)
		if err != nil {
			return update.ReleaseRetentionInput{}, fmt.Errorf("resolve workspace home: %w", err)
		}
		home = absoluteHome
		if releasesDir == "" {
			releasesDir = filepath.Join(home, ".local", "share", "loom", "releases")
		}
		if activePath == "" {
			activePath = filepath.Join(home, ".local", "share", "loom", "current")
		}
		if stateDir == "" {
			stateDir = filepath.Join(home, ".local", "state", "loom", "update")
		}
	} else {
		if releasesDir == "" {
			releasesDir = "/srv/loom/releases"
		}
		if activePath == "" {
			activePath = "/srv/loom/current"
		}
		if stateDir == "" {
			stateDir = update.DefaultStateDir("")
		}
	}
	return update.ReleaseRetentionInput{
		ReleasesDir:        releasesDir,
		ActivePath:         activePath,
		StateDir:           stateDir,
		KeepSuccessful:     flags.keepSuccessful,
		ProtectYoungerThan: flags.protectYoungerThan,
		Pins:               append([]string(nil), flags.pins...),
	}, nil
}

func maintenancePolicyFromFlags(flags updateFlags) update.MaintenancePausePolicy {
	policy := update.DefaultMaintenancePausePolicy()
	if flags.noPauseSchedules {
		policy.PauseSchedules = false
	}
	if flags.noPauseDirectEvents {
		policy.PauseDirectEventEndpoints = false
	}
	if flags.noPauseWorkers {
		policy.PauseOptionalWorkers = false
	}
	return policy
}

func updateMaintenanceCoordinator(ctx context.Context, cfg config.Config, flags updateFlags, policy update.MaintenancePausePolicy, correlationID string, dryRun bool) (update.MaintenanceCoordinator, func(), error) {
	noop := func(reason string) (update.MaintenanceCoordinator, func(), error) {
		return update.NoopMaintenanceCoordinator{Reason: reason}, func() {}, nil
	}
	if dryRun || !maintenancePolicyRequestsPauseCLI(policy) {
		return noop("Maintenance pause was skipped by update flags or dry-run.")
	}
	if strings.TrimSpace(cfg.DBURL) == "" {
		if flags.allowNonProduction {
			return noop("LOOM_DB_URL is not configured; non-production update drill recorded no maintenance pauses.")
		}
		return nil, func() {}, fmt.Errorf("LOOM_DB_URL is required for production maintenance pause/resume")
	}
	sqlDB, err := openUpdateMaintenanceSQL(ctx, cfg.DBURL)
	if err != nil {
		if flags.allowNonProduction {
			return noop("Could not open LOOM_DB_URL for non-production maintenance pause/resume: " + err.Error())
		}
		return nil, func() {}, err
	}
	req, err := requestctx.ResolveBootstrap(ctx, sqlDB, correlationID)
	if err != nil {
		_ = sqlDB.Close()
		if flags.allowNonProduction {
			return noop("Could not resolve bootstrap request context for non-production maintenance pause/resume: " + err.Error())
		}
		return nil, func() {}, err
	}
	service := automation.NewService(sqlDB, routing.NewService(sqlDB))
	return update.AutomationMaintenanceCoordinator{Service: service, Request: req}, func() { _ = sqlDB.Close() }, nil
}

func maintenancePolicyRequestsPauseCLI(policy update.MaintenancePausePolicy) bool {
	return policy.PauseSchedules || policy.PauseDirectEventEndpoints || policy.PauseOptionalWorkers
}

func openUpdateMaintenanceSQL(ctx context.Context, dbURL string) (*sql.DB, error) {
	sqlDB, err := db.OpenSQL(ctx, dbURL)
	if err == nil {
		return sqlDB, nil
	}
	if !isPostgresPeerAuthFailure(err) {
		return nil, err
	}
	fallbackURL, peerUser, ok := updateMaintenancePeerFallbackDBURL(dbURL, currentPeerDBUser())
	if !ok {
		return nil, err
	}
	sqlDB, fallbackErr := db.OpenSQL(ctx, fallbackURL)
	if fallbackErr == nil {
		return sqlDB, nil
	}
	return nil, fmt.Errorf("%w; update maintenance admin peer fallback for OS user %q also failed: %v", err, peerUser, fallbackErr)
}

func isPostgresPeerAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "peer authentication failed")
}

func updateMaintenancePeerFallbackDBURL(dbURL, peerUser string) (string, string, bool) {
	peerUser = strings.TrimSpace(peerUser)
	if peerUser == "" {
		return "", "", false
	}
	fields := strings.Fields(strings.TrimSpace(dbURL))
	if len(fields) == 0 {
		return "", "", false
	}
	hasLocalSocketHost := false
	hasPassword := false
	userField := -1
	for i, field := range fields {
		key, value, ok := splitKeywordDBField(field)
		if !ok {
			return "", "", false
		}
		switch strings.ToLower(key) {
		case "host":
			clean := strings.Trim(value, `"'`)
			if strings.HasPrefix(clean, "/run/postgresql") || strings.HasPrefix(clean, "/var/run/postgresql") {
				hasLocalSocketHost = true
			}
		case "password", "passfile":
			hasPassword = true
		case "user":
			userField = i
		}
	}
	if !hasLocalSocketHost || hasPassword {
		return "", "", false
	}
	if userField >= 0 {
		key, _, _ := splitKeywordDBField(fields[userField])
		fields[userField] = key + "=" + peerUser
	} else {
		fields = append([]string{"user=" + peerUser}, fields...)
	}
	fallback := strings.Join(fields, " ")
	if fallback == strings.TrimSpace(dbURL) {
		return "", "", false
	}
	return fallback, peerUser, true
}

func splitKeywordDBField(field string) (string, string, bool) {
	key, value, ok := strings.Cut(field, "=")
	key = strings.TrimSpace(key)
	if !ok || key == "" {
		return "", "", false
	}
	return key, strings.TrimSpace(value), true
}

func currentPeerDBUser() string {
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	current, err := osuser.Current()
	if err != nil || current == nil {
		return ""
	}
	username := strings.TrimSpace(current.Username)
	if i := strings.LastIndexAny(username, `\`); i >= 0 && i+1 < len(username) {
		username = username[i+1:]
	}
	return username
}

func updateSpecFromFlags(flags updateFlags, cfg config.Config) update.UpdateSpec {
	manifestPath := firstNonEmptyString(flags.manifestPath, discoverInstallManifestPath(cfg))
	activePath := flags.activePath
	activeMigrationsDir := flags.activeMigrationsDir
	if manifestPath != "" {
		if manifest, err := setup.ReadManifest(manifestPath); err == nil {
			activePath = firstNonEmptyString(activePath, manifest.SourcePath)
			activeMigrationsDir = firstNonEmptyString(activeMigrationsDir, manifest.MigrationsDir)
		}
	}
	if activeMigrationsDir == "" {
		activeMigrationsDir = cfg.MigrationsDir
	}
	spec := update.UpdateSpec{
		ReleasePath:         flags.releasePath,
		StateDir:            firstNonEmptyString(flags.stateDir, update.DefaultStateDir(cfg.DataDir)),
		ManifestPath:        manifestPath,
		ActivePath:          activePath,
		ActiveMigrationsDir: activeMigrationsDir,
		TargetMigrationsDir: flags.targetMigrationsDir,
		FlakeOutput:         flags.flakeOutput,
		DBURL:               cfg.DBURL,
	}
	if flags.currentMigration >= 0 {
		spec.CurrentMigrationVersion = &flags.currentMigration
	}
	return spec
}

func discoverInstallManifestPath(cfg config.Config) string {
	candidates := []string{}
	if cfg.ConfigFile != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(cfg.ConfigFile), "install.yaml"))
	}
	candidates = append(candidates, "/etc/loom/install.yaml")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, filepath.Join(home, ".config", "loom", "install.yaml"))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}
