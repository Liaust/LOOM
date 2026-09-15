package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/correlation"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/restoreauthority"
	"loom.local/loom/internal/workers"
)

func newCloudCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Inspect LOOM cloud storage readiness",
	}
	cmd.AddCommand(newCloudStatusCommand(opts))
	cmd.AddCommand(newCloudDoctorCommand(opts))
	cmd.AddCommand(newCloudSnapshotCommand(opts))
	return cmd
}

func cloudLocalSuccess[T any](correlationID string, data T) response.Envelope[T] {
	meta := response.NewMeta(correlationID)
	meta.Source = "local-cli"
	meta.Freshness = "local"
	return response.Envelope[T]{
		OK:   true,
		Data: data,
		Meta: meta,
	}
}

func newCloudStatusCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var live bool
	var forceLive bool
	var cached bool
	var local bool
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show cloud storage status",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if live && cached {
				return renderError(cmd, opts, correlationID, fmt.Errorf("--live and --cached cannot be used together"))
			}
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.CloudStatusLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudStatusLiveInput{
					ConfigPath: cloudConfigPath,
					ForceLive:  forceLive,
					Cached:     cached,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudStatus(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			mode := cloudstorage.StatusModeCached
			if live || forceLive {
				mode = cloudstorage.StatusModeLive
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			report, err := cloudstorage.Status(ctx, cloudstorage.StatusInput{
				ConfigPath: cloudConfigPath,
				Mode:       mode,
				ForceLive:  forceLive,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, report))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
				return nil
			}
			renderCloudStatus(cmd, report)
			return nil
		},
	}
	statusCmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	statusCmd.Flags().BoolVar(&live, "live", false, "perform an explicit live remote probe instead of reading cached state")
	statusCmd.Flags().BoolVar(&forceLive, "force-live", false, "perform a live remote probe even if cached state is cooling down")
	statusCmd.Flags().BoolVar(&cached, "cached", false, "read cached cloud status without contacting remote storage")
	statusCmd.Flags().BoolVar(&local, "local", false, "run live cloud checks in this CLI process instead of delegating to loomd")
	return statusCmd
}

func newCloudDoctorCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var live bool
	var forceLive bool
	var local bool
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run read-only cloud storage diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.CloudDoctorLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudDoctorLiveInput{
					ConfigPath: cloudConfigPath,
					ForceLive:  forceLive,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudDoctor(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			report, err := cloudstorage.Doctor(ctx, cloudstorage.DoctorInput{
				ConfigPath:    cloudConfigPath,
				RuntimeConfig: runtimeConfig,
				Live:          live || forceLive,
				ForceLive:     forceLive,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, report))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), report.Status)
				return nil
			}
			renderCloudDoctor(cmd, report)
			return nil
		},
	}
	doctorCmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	doctorCmd.Flags().BoolVar(&live, "live", false, "perform explicit live remote diagnostics instead of local-only checks")
	doctorCmd.Flags().BoolVar(&forceLive, "force-live", false, "perform live remote diagnostics even if cached state is cooling down")
	doctorCmd.Flags().BoolVar(&local, "local", false, "run live cloud diagnostics in this CLI process instead of delegating to loomd")
	return doctorCmd
}

func renderCloudStatus(cmd *cobra.Command, report cloudstorage.StatusReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud: %s\n", report.Status)
	if report.Mode != "" {
		fmt.Fprintf(out, "Mode: %s\n", report.Mode)
	}
	fmt.Fprintf(out, "Config: %s", report.Config.Path)
	if !report.Config.Exists {
		fmt.Fprint(out, " (missing)")
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Remote: %s:%s\n", report.Config.RemoteName, report.Config.RemoteRoot)
	if report.LockPath != "" {
		fmt.Fprintf(out, "Remote lock: %s\n", report.LockPath)
	}
	fmt.Fprintf(out, "Snapshot backend: %s\n", report.Config.SnapshotBackend)
	if report.Remote != nil {
		fmt.Fprintf(out, "Reachable: %t entries=%d\n", report.Remote.Reachable, report.Remote.Entries)
	}
	if report.SnapshotStore != nil {
		fmt.Fprintf(out, "Snapshot store: %s initialized=%t\n", report.SnapshotStore.Status, report.SnapshotStore.Initialized)
	}
	renderCloudRemoteState(out, report.RemoteState)
	if len(report.Roots) > 0 {
		fmt.Fprintln(out, "Roots:")
		for _, root := range report.Roots {
			fmt.Fprintf(out, "  %s  %s  %s\n", root.Status, root.Name, root.RemoteURI)
		}
	}
}

func renderCloudDoctor(cmd *cobra.Command, report cloudstorage.DoctorReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud doctor: %s\n", report.Status)
	fmt.Fprintf(out, "Config: %s", report.Config.Path)
	if !report.Config.Exists {
		fmt.Fprint(out, " (missing)")
	}
	fmt.Fprintln(out)
	renderCloudRemoteState(out, report.RemoteState)
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "STATUS\tSEVERITY\tCODE\tMESSAGE")
	for _, finding := range report.Findings {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", finding.Status, finding.Severity, finding.Code, finding.Message)
	}
	_ = writer.Flush()
}

func renderCloudRemoteState(out interface {
	Write([]byte) (int, error)
}, state *cloudstorage.RemoteState) {
	if state == nil {
		return
	}
	fmt.Fprintf(out, "Remote state: %s\n", state.State)
	if state.LastSuccessAt != nil {
		fmt.Fprintf(out, "Last success: %s\n", timePtrOrDash(state.LastSuccessAt))
	}
	if state.LastFailureAt != nil {
		fmt.Fprintf(out, "Last failure: %s\n", timePtrOrDash(state.LastFailureAt))
	}
	if state.NextLiveCheckAfter != nil {
		fmt.Fprintf(out, "Next live check: %s\n", timePtrOrDash(state.NextLiveCheckAfter))
	}
	if state.LastErrorClass != "" {
		fmt.Fprintf(out, "Last error class: %s\n", state.LastErrorClass)
	}
	if state.LiveProbeSkipReason != "" {
		fmt.Fprintf(out, "Live probe skipped: %s\n", state.LiveProbeSkipReason)
	}
}

func renderCloudSnapshotBackendStatus(cmd *cobra.Command, result cloudstorage.SnapshotBackendStatusReport) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot backend: %s\n", result.Status)
	fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	if result.Repository != "" {
		fmt.Fprintf(out, "Repository: %s\n", result.Repository)
	}
	fmt.Fprintf(out, "Initialized: %t\n", result.Initialized)
	if result.ArchiveCount > 0 {
		fmt.Fprintf(out, "Archives: %d\n", result.ArchiveCount)
	}
	if result.Cached {
		fmt.Fprintf(out, "Cached: true")
		if result.CacheAgeSeconds > 0 {
			fmt.Fprintf(out, " age=%ds", result.CacheAgeSeconds)
		}
		fmt.Fprintln(out)
	}
	if result.CachePath != "" {
		fmt.Fprintf(out, "Cache: %s\n", result.CachePath)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	if result.RepairHint != "" {
		fmt.Fprintf(out, "Repair: %s\n", result.RepairHint)
	}
	if result.CommandSummary != "" {
		fmt.Fprintf(out, "Summary: %s\n", result.CommandSummary)
	}
	renderCloudRemoteState(out, result.RemoteState)
	renderStringMap(cmd, "Checks", result.Checks)
}

func renderCloudSnapshotBackendInit(cmd *cobra.Command, result cloudstorage.SnapshotBackendInitResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot backend init: %s\n", result.Status)
	fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	if result.Repository != "" {
		fmt.Fprintf(out, "Repository: %s\n", result.Repository)
	}
	fmt.Fprintf(out, "Initialized: %t\n", result.Initialized)
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	renderStringMap(cmd, "Checks", result.Checks)
}

func newCloudSnapshotCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Upload, inspect, and restore-drill cloud backup snapshots",
	}
	cmd.AddCommand(newCloudSnapshotStatusCommand(opts))
	cmd.AddCommand(newCloudSnapshotBackendCommand(opts))
	cmd.AddCommand(newCloudSnapshotPushCommand(opts))
	cmd.AddCommand(newCloudSnapshotListCommand(opts))
	cmd.AddCommand(newCloudSnapshotVerifyCommand(opts))
	cmd.AddCommand(newCloudSnapshotFetchCommand(opts))
	cmd.AddCommand(newCloudSnapshotRestoreDrillCommand(opts))
	cmd.AddCommand(newCloudRestoreCleanupCommand(opts))
	cmd.AddCommand(newCloudSnapshotRetentionCommand(opts))
	return cmd
}

func newCloudSnapshotBackendCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backend",
		Short: "Inspect and initialize the configured cloud snapshot backend",
	}
	cmd.AddCommand(newCloudSnapshotBackendStatusCommand(opts))
	cmd.AddCommand(newCloudSnapshotBackendDoctorCommand(opts))
	cmd.AddCommand(newCloudSnapshotBackendInitCommand(opts))
	return cmd
}

func newCloudSnapshotBackendStatusCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var live bool
	var forceLive bool
	var cached bool
	var local bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show configured cloud snapshot backend status",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if cached && (live || forceLive) {
				return renderError(cmd, opts, correlationID, fmt.Errorf("--cached cannot be combined with --live or --force-live"))
			}
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotBackendStatusLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotBackendStatusLiveInput{
					ConfigPath: cloudConfigPath,
					ForceLive:  forceLive,
					Cached:     cached,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudSnapshotBackendStatus(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			_, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := cloudstorage.SnapshotBackendStatus(ctx, cloudstorage.SnapshotBackendStatusInput{
				Config:    cloudConfig,
				Live:      live || forceLive,
				ForceLive: forceLive,
				UseCache:  cached,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderCloudSnapshotBackendStatus(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().BoolVar(&live, "live", false, "probe Borg live once when cooldown allows it")
	cmd.Flags().BoolVar(&forceLive, "force-live", false, "probe Borg live once even when remote cooldown is active")
	cmd.Flags().BoolVar(&cached, "cached", false, "read cached Borg backend status without opening a remote connection")
	cmd.Flags().BoolVar(&local, "local", false, "run live Borg status in this CLI process instead of delegating to loomd")
	return cmd
}

func newCloudSnapshotBackendDoctorCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run cloud snapshot backend diagnostics",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			runtimeConfig, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			report, err := cloudstorage.Doctor(ctx, cloudstorage.DoctorInput{
				ConfigPath:    cloudConfigPath,
				RuntimeConfig: runtimeConfig,
			})
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
			renderCloudDoctor(cmd, report)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	return cmd
}

func newCloudSnapshotBackendInitCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var confirm bool
	var local bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the configured cloud snapshot backend repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !confirm {
				return renderError(cmd, opts, correlationID, loomerrors.New("cloud.snapshot_backend_init_confirm_required", "cloud", "confirm", "Pass --confirm to initialize the cloud snapshot backend."))
			}
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotBackendInitLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotBackendInitLiveInput{
					ConfigPath: cloudConfigPath,
					Confirm:    confirm,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudSnapshotBackendInit(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			_, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			result, err := cloudstorage.InitializeSnapshotBackend(ctx, cloudstorage.SnapshotBackendInitInput{Config: cloudConfig, Confirm: confirm})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderCloudSnapshotBackendInit(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().BoolVar(&confirm, "confirm", false, "confirm initializing the snapshot backend repository")
	cmd.Flags().BoolVar(&local, "local", false, "initialize the snapshot backend in this CLI process instead of delegating to loomd")
	return cmd
}

func newCloudSnapshotStatusCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var live bool
	var forceLive bool
	var local bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show cloud snapshot status",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotStatusWithProducerLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotStatusLiveInput{
					ConfigPath: cloudConfigPath,
					NodeID:     nodeID,
					ForceLive:  forceLive,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Cloud.Status)
					return nil
				}
				renderCloudStatus(cmd, envelope.Data.Cloud)
				fmt.Fprintf(cmd.OutOrStdout(), "Producer: %s verification=%s available=%t last_success=%s\n",
					envelope.Data.Producer.WorkerState,
					envelope.Data.Producer.Verification,
					envelope.Data.Producer.Available,
					timePtrOrDash(envelope.Data.Producer.LastSuccessAt),
				)
				if envelope.Data.Snapshots != nil {
					renderCloudSnapshotList(cmd, *envelope.Data.Snapshots)
				} else if envelope.Data.Cloud.Config.Enabled {
					fmt.Fprintln(cmd.OutOrStdout(), "Snapshot inventory: live cloud status did not return remote snapshots.")
				}
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			mode := cloudstorage.StatusModeCached
			if live || forceLive {
				mode = cloudstorage.StatusModeLive
			}
			status, err := cloudstorage.Status(ctx, cloudstorage.StatusInput{ConfigPath: cloudConfigPath, Mode: mode, ForceLive: forceLive})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			report := struct {
				Cloud     cloudstorage.StatusReport        `json:"cloud"`
				Snapshots *cloudstorage.SnapshotListResult `json:"snapshots,omitempty"`
			}{Cloud: status}
			if cloudConfig.Enabled && (live || forceLive) && status.Status == "reachable" {
				list, err := cloudstorage.ListSnapshots(ctx, cloudstorage.SnapshotListInput{Config: cloudConfig, NodeID: firstNonEmptyCLI(nodeID, runtimeConfig.NodeID)})
				if err == nil {
					report.Snapshots = &list
				}
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, report))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), status.Status)
				return nil
			}
			renderCloudStatus(cmd, status)
			if report.Snapshots != nil {
				renderCloudSnapshotList(cmd, *report.Snapshots)
			} else if cloudConfig.Enabled && !opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), "Snapshot inventory: cached status only; run `loom cloud snapshot status --live` to list remote snapshots.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().BoolVar(&live, "live", false, "perform an explicit live remote probe and list cloud snapshots")
	cmd.Flags().BoolVar(&forceLive, "force-live", false, "perform live cloud snapshot status even if cached state is cooling down")
	cmd.Flags().BoolVar(&local, "local", false, "run live cloud snapshot status in this CLI process instead of delegating to loomd")
	return cmd
}

func newCloudSnapshotPushCommand(opts *options) *cobra.Command {
	var production bool
	var allowNonProduction bool
	var dryRun bool
	var reason string
	var idempotencyKey string
	var backupOperationID string
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Archive canonical roots and the verified operational package into Borg history",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if production && allowNonProduction {
				return renderError(cmd, opts, correlationID, loomerrors.New("cloud.snapshot_push_mode_invalid", "cloud", "snapshot", "Choose either --production or --allow-non-production, not both."))
			}
			if !production && !allowNonProduction {
				return renderError(cmd, opts, correlationID, loomerrors.New("cloud.snapshot_push_confirmation_required", "cloud", "snapshot", "Pass --production for the main runtime or --allow-non-production for a development drill."))
			}
			if cmd.Flags().Changed("backup-operation-id") {
				trimmed := strings.TrimSpace(backupOperationID)
				if trimmed == "" || trimmed != backupOperationID || ids.Validate(ids.MaintenanceOperationPrefix, trimmed) != nil {
					return renderError(cmd, opts, correlationID, loomerrors.New("cloud.snapshot_push_backup_operation_invalid", "cloud", "backup_operation_id", "--backup-operation-id must be one exact maintenance operation ID."))
				}
				backupOperationID = trimmed
			}
			if dryRun {
				plan := map[string]any{
					"status": "planned", "worker": "main.cloud_snapshot_upload", "policy": "manual",
					"phase": "direct_borg_archive", "requires_production": production,
					"sources": []string{"reviewed daemon canonical roots", "latest exact-verified operational package"},
					"creates_complete_local_user_data_generation": false,
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, plan))
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), "planned")
					return nil
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Cloud snapshot push: planned")
				fmt.Fprintln(cmd.OutOrStdout(), "Phase: direct_borg_archive")
				fmt.Fprintln(cmd.OutOrStdout(), "Local full generation: false")
				return nil
			}
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := cloudSnapshotPushCommandContext(cmd.Context())
			defer cancel()
			if production {
				healthEnvelope, healthErr := commandCtx.Client.Health(ctx, commandCtx.CorrelationID)
				if healthErr != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, healthErr)
				}
				if !isProductionMainHealth(healthEnvelope.Data.Environment, healthEnvelope.Data.Node.ID, healthEnvelope.Data.Node.Role) {
					return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New("cloud.snapshot_push_production_required", "cloud", healthEnvelope.Data.Node.ID, "Cloud snapshot push --production must run against the production main runtime."))
				}
			}
			client, resolvedKey := withEffectIdempotency(commandCtx.Client, idempotencyKey, "cloud.snapshot.push")
			metadata, _ := json.Marshal(cloudSnapshotPushRequestMetadata{Source: "loom_cloud_cli", ProductionRequested: production, BackupOperationID: backupOperationID})
			envelope, err := client.RunCloudSnapshot(ctx, commandCtx.CorrelationID, workers.RunOnceInput{Reason: firstNonEmptyString(reason, "manual archive requested by loom cloud snapshot push"), IdempotencyKey: resolvedKey, Metadata: metadata})
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), cloudSnapshotRunStatus(envelope.Data.Run.ResultSummaryJSON))
				return nil
			}
			renderCloudSnapshotWorkerRun(cmd, envelope.Data.Run.WorkerRunID, envelope.Data.Run.ResultSummaryJSON)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().BoolVar(&production, "production", false, "confirm this archive is for the production main runtime")
	cmd.Flags().BoolVar(&allowNonProduction, "allow-non-production", false, "allow a direct-archive drill against a non-production runtime")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the bounded worker plan without creating an archive")
	cmd.Flags().StringVar(&reason, "reason", "", "reason recorded on the cloud snapshot worker run")
	cmd.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "explicit idempotency key for the cloud snapshot worker run")
	cmd.Flags().StringVar(&backupOperationID, "backup-operation-id", "", "select one exact succeeded recovery-package backup operation")
	return cmd
}

type cloudSnapshotPushRequestMetadata struct {
	Source              string `json:"source"`
	ProductionRequested bool   `json:"production_requested"`
	BackupOperationID   string `json:"backup_operation_id,omitempty"`
}

func cloudSnapshotPushCommandContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, 3*time.Hour)
}

func newCloudSnapshotListCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var local bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List cloud snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotListLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotListLiveInput{
					ConfigPath: cloudConfigPath,
					NodeID:     nodeID,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					for _, item := range envelope.Data.Snapshots {
						fmt.Fprintln(cmd.OutOrStdout(), item.Ref)
					}
					return nil
				}
				renderCloudSnapshotList(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := cloudstorage.ListSnapshots(ctx, cloudstorage.SnapshotListInput{Config: cloudConfig, NodeID: firstNonEmptyCLI(nodeID, runtimeConfig.NodeID)})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, result))
			}
			if opts.plainOutput {
				for _, item := range result.Snapshots {
					fmt.Fprintln(cmd.OutOrStdout(), item.Ref)
				}
				return nil
			}
			renderCloudSnapshotList(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().BoolVar(&local, "local", false, "list cloud snapshots in this CLI process instead of delegating to loomd")
	return cmd
}

func newCloudSnapshotVerifyCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var stateDir string
	var profile string
	var maxDuration time.Duration
	var local bool
	cmd := &cobra.Command{
		Use:   "verify [cloud-snapshot-ref]",
		Short: "Run an explicit cloud snapshot assurance profile",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			ref := ""
			if len(args) == 1 {
				ref = strings.TrimSpace(args[0])
			}
			if maxDuration%time.Second != 0 {
				return renderError(cmd, opts, correlationID, fmt.Errorf("--max-duration must be a whole number of seconds"))
			}
			options, err := cloudstorage.NormalizeSnapshotVerifyOptions(ref, cloudstorage.SnapshotVerifyOptions{
				Profile:            cloudstorage.SnapshotVerifyProfile(profile),
				MaxDurationSeconds: int64(maxDuration / time.Second),
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx := cmd.Context()
			cancel := func() {}
			switch options.Profile {
			case cloudstorage.SnapshotVerifyProfileMetadata:
				ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
			case cloudstorage.SnapshotVerifyProfileRollingRepository:
				ctx, cancel = context.WithTimeout(ctx, time.Duration(options.MaxDurationSeconds)*time.Second+time.Minute)
			}
			defer cancel()
			if !local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				envelope, err := commandCtx.Client.CloudSnapshotVerifyLive(ctx, commandCtx.CorrelationID, cloudstorage.SnapshotVerifyLiveInput{
					CloudSnapshotVerifyLiveInput: cloudstorage.CloudSnapshotVerifyLiveInput{
						ConfigPath: cloudConfigPath,
						NodeID:     nodeID,
						Ref:        ref,
						StateDir:   stateDir,
					},
					SnapshotVerifyOptions: options,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudSnapshotVerify(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfigForLocalSnapshotVerify(opts, cloudConfigPath, ref)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			result, err := cloudstorage.VerifySnapshot(ctx, cloudstorage.SnapshotVerifyInput{
				Config:                cloudConfig,
				NodeID:                firstNonEmptyCLI(nodeID, runtimeConfig.NodeID),
				Ref:                   ref,
				StateDir:              firstNonEmptyCLI(stateDir, cloudConfig.StateDir),
				SnapshotVerifyOptions: options,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, cloudSnapshotVerifyLocalError(ref, err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderCloudSnapshotVerify(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "cloud state dir for temporary manifest downloads")
	cmd.Flags().StringVar(&profile, "profile", string(cloudstorage.SnapshotVerifyProfileMetadata), "assurance profile: metadata, rolling_repository, or archive_data")
	cmd.Flags().DurationVar(&maxDuration, "max-duration", 0, "positive rolling_repository time budget, for example 30m")
	cmd.Flags().BoolVar(&local, "local", false, "verify cloud snapshots in this CLI process instead of delegating to loomd")
	return cmd
}

func newCloudSnapshotFetchCommand(opts *options) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var to string
	cmd := &cobra.Command{
		Use:   "fetch <cloud-snapshot-ref>",
		Short: "Fetch a cloud snapshot to a local staging directory and verify it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			result, err := cloudstorage.FetchSnapshot(ctx, cloudstorage.SnapshotFetchInput{
				Config: cloudConfig,
				NodeID: firstNonEmptyCLI(nodeID, runtimeConfig.NodeID),
				Ref:    args[0],
				To:     to,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(response.Success(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.TargetDir)
				return nil
			}
			renderCloudSnapshotFetch(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&to, "to", "", "empty local target directory")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

func newCloudSnapshotRestoreDrillCommand(opts *options) *cobra.Command {
	return cloudSnapshotRestoreDrillCommand(opts, loadCloudCLIConfig)
}

func cloudSnapshotRestoreDrillContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 2*time.Hour)
}

func cloudSnapshotRestoreDrillCommand(opts *options, loadLocalConfig func(*options, string) (config.Config, cloudstorage.Config, error)) *cobra.Command {
	var cloudConfigPath string
	var nodeID string
	var stateDir string
	var targetDatabase string
	var provenanceTargetDatabase string
	var keep bool
	var dryRun bool
	var local bool
	cmd := &cobra.Command{
		Use:   "restore-drill <cloud-snapshot-ref>",
		Short: "Fetch a cloud snapshot and run a restore drill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if keep {
				return renderError(cmd, opts, correlationID, fmt.Errorf("--keep is unavailable with the mandatory restore authority cleanup contract"))
			}
			ctx, cancel := cloudSnapshotRestoreDrillContext(cmd.Context())
			defer cancel()
			if !local {
				if cloudConfigPath != "" && cloudConfigPath != cloudstorage.DefaultConfigPath {
					return renderError(cmd, opts, correlationID, loomerrors.New("cloud.config_override_forbidden", "cloud", "config_path", "Cloud config overrides are only available in explicit local mode."))
				}
				if stateDir != "" {
					return renderError(cmd, opts, correlationID, loomerrors.New("cloud.state_dir_override_forbidden", "cloud", "state_dir", "Cloud restore staging overrides are only available in explicit local mode."))
				}
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				envelope, err := commandCtx.Client.CloudSnapshotRestoreDrillLive(ctx, commandCtx.CorrelationID, localclient.CloudSnapshotRestoreDrillInput{
					ConfigPath: cloudConfigPath, NodeID: nodeID, Ref: args[0],
					TargetDatabase: targetDatabase, ProvenanceTargetDatabase: provenanceTargetDatabase,
					DryRun: dryRun,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudRestoreDrill(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadLocalConfig(opts, cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			restoreInput := cloudstorage.CloudRestoreDrillInput{
				Config:         cloudConfig,
				NodeID:         firstNonEmptyCLI(nodeID, runtimeConfig.NodeID),
				Ref:            args[0],
				StateDir:       firstNonEmptyCLI(stateDir, cloudConfig.StateDir),
				TargetDatabase: targetDatabase,
				KeepDatabase:   keep,
				DryRun:         dryRun,
			}
			if err := configureCloudRestoreAuthority(&restoreInput, runtimeConfig, provenanceTargetDatabase); err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			result, err := cloudstorage.RestoreDrill(ctx, restoreInput)
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
			renderCloudRestoreDrill(cmd, result)
			return nil
		},
	}
	cmd.Flags().StringVar(&cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "local mode only: cloud state dir for restore-drill staging")
	cmd.Flags().StringVar(&targetDatabase, "target-database", "", "temporary drill database name; must start with loom_restore_drill_")
	cmd.Flags().StringVar(&provenanceTargetDatabase, "provenance-target-database", "", "temporary Provenance drill database name; must start with loom_provenance_restore_drill_")
	cmd.Flags().BoolVar(&keep, "keep", false, "refused: restore-authority cleanup is mandatory")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "verify the cloud snapshot and show the drill target without restoring")
	cmd.Flags().BoolVar(&local, "local", false, "run in this CLI process for disposable/dev drills instead of delegating to loomd")
	return cmd
}

func configureCloudRestoreAuthority(input *cloudstorage.CloudRestoreDrillInput, runtimeConfig config.Config, targetDatabase string) error {
	input.ActiveDatabase = runtimeConfig.RestoreOperationalDatabase
	input.Owner = runtimeConfig.RestoreOperationalOwner
	input.ProvenanceDatabaseURL = runtimeConfig.ProvenanceDBURL
	input.ProvenanceTargetDatabase = strings.TrimSpace(targetDatabase)
	input.ProvenanceActiveDatabase = runtimeConfig.RestoreProvenanceDatabase
	input.ProvenanceOwner = runtimeConfig.RestoreProvenanceOwner
	if input.DryRun {
		return nil
	}
	socketUID, err := restoreauthority.ResolveUserID(runtimeConfig.RestoreAuthoritySocketOwner)
	if err != nil {
		return err
	}
	socketGID, err := restoreauthority.ResolveGroupID(runtimeConfig.RestoreAuthoritySocketGroup)
	if err != nil {
		return err
	}
	socketActivatorUID, err := restoreauthority.ResolveUserID(runtimeConfig.RestoreActivatorUser)
	if err != nil {
		return err
	}
	authority, err := restoreauthority.NewClient(restoreauthority.ClientConfig{
		SocketPath:         runtimeConfig.RestoreAuthoritySocketPath,
		SocketUID:          socketUID,
		SocketGID:          socketGID,
		SocketActivatorUID: &socketActivatorUID,
	})
	if err != nil {
		return err
	}
	input.RestoreAuthority = authority
	return nil
}

func newCloudSnapshotRetentionCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "retention",
		Short: "Plan and apply cloud snapshot retention",
	}
	cmd.AddCommand(newCloudSnapshotRetentionStatusCommand(opts))
	cmd.AddCommand(newCloudSnapshotRetentionPlanCommand(opts))
	cmd.AddCommand(newCloudSnapshotRetentionApplyCommand(opts))
	return cmd
}

type cloudSnapshotRetentionFlags struct {
	cloudConfigPath string
	nodeID          string
	stateDir        string
	keepLatest      int
	local           bool
}

func bindCloudSnapshotRetentionFlags(cmd *cobra.Command, flags *cloudSnapshotRetentionFlags) {
	cmd.Flags().StringVar(&flags.cloudConfigPath, "cloud-config", cloudstorage.DefaultConfigPath, "path to LOOM cloud config JSON")
	cmd.Flags().StringVar(&flags.nodeID, "node-id", "", "snapshot owner node id; defaults to runtime node id")
	cmd.Flags().StringVar(&flags.stateDir, "state-dir", "", "cloud state dir for temporary manifest downloads")
	cmd.Flags().IntVar(&flags.keepLatest, "keep-latest", cloudstorage.DefaultSnapshotKeepLatest, "number of latest valid cloud snapshots to keep")
	cmd.Flags().BoolVar(&flags.local, "local", false, "run cloud snapshot retention in this CLI process instead of delegating to loomd")
}

func newCloudSnapshotRetentionStatusCommand(opts *options) *cobra.Command {
	var flags cloudSnapshotRetentionFlags
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show cloud snapshot retention status",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !flags.local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotRetentionPlanLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotRetentionPlanLiveInput{
					ConfigPath: flags.cloudConfigPath,
					NodeID:     flags.nodeID,
					StateDir:   flags.stateDir,
					KeepLatest: flags.keepLatest,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudSnapshotRetentionPlan(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, flags.cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			plan, err := cloudstorage.PlanSnapshotRetention(ctx, cloudstorage.SnapshotRetentionInput{
				Config:     cloudConfig,
				NodeID:     firstNonEmptyCLI(flags.nodeID, runtimeConfig.NodeID),
				StateDir:   firstNonEmptyCLI(flags.stateDir, cloudConfig.StateDir),
				KeepLatest: flags.keepLatest,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, plan))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), plan.Status)
				return nil
			}
			renderCloudSnapshotRetentionPlan(cmd, plan)
			return nil
		},
	}
	bindCloudSnapshotRetentionFlags(cmd, &flags)
	return cmd
}

func newCloudSnapshotRetentionPlanCommand(opts *options) *cobra.Command {
	var flags cloudSnapshotRetentionFlags
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Plan cloud snapshot retention without mutating the remote",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !flags.local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotRetentionPlanLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotRetentionPlanLiveInput{
					ConfigPath: flags.cloudConfigPath,
					NodeID:     flags.nodeID,
					StateDir:   flags.stateDir,
					KeepLatest: flags.keepLatest,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					for _, item := range envelope.Data.Remove {
						fmt.Fprintln(cmd.OutOrStdout(), item.Ref)
					}
					return nil
				}
				renderCloudSnapshotRetentionPlan(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, flags.cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			plan, err := cloudstorage.PlanSnapshotRetention(ctx, cloudstorage.SnapshotRetentionInput{
				Config:     cloudConfig,
				NodeID:     firstNonEmptyCLI(flags.nodeID, runtimeConfig.NodeID),
				StateDir:   firstNonEmptyCLI(flags.stateDir, cloudConfig.StateDir),
				KeepLatest: flags.keepLatest,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, plan))
			}
			if opts.plainOutput {
				for _, item := range plan.Remove {
					fmt.Fprintln(cmd.OutOrStdout(), item.Ref)
				}
				return nil
			}
			renderCloudSnapshotRetentionPlan(cmd, plan)
			return nil
		},
	}
	bindCloudSnapshotRetentionFlags(cmd, &flags)
	return cmd
}

func newCloudSnapshotRetentionApplyCommand(opts *options) *cobra.Command {
	var flags cloudSnapshotRetentionFlags
	var confirm bool
	var planPath string
	var confirmDigest string
	var compact bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply an exact reviewed cloud snapshot retention plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			correlationID := correlation.Normalize(opts.correlationID)
			if !confirm {
				return renderError(cmd, opts, correlationID, fmt.Errorf("pass --confirm to apply cloud snapshot retention"))
			}
			var reviewedPlan cloudstorage.SnapshotRetentionPlan
			if strings.TrimSpace(planPath) != "" {
				var err error
				reviewedPlan, err = readCloudRetentionPlan(planPath)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
			}
			if (reviewedPlan.PlanDigest == "") != (strings.TrimSpace(confirmDigest) == "") {
				return renderError(cmd, opts, correlationID, fmt.Errorf("--plan and --confirm-digest must be provided together"))
			}
			if !flags.local {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlationID, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				envelope, err := commandCtx.Client.CloudSnapshotRetentionApplyLive(ctx, commandCtx.CorrelationID, cloudstorage.CloudSnapshotRetentionApplyLiveInput{
					ConfigPath: flags.cloudConfigPath,
					NodeID:     flags.nodeID,
					StateDir:   flags.stateDir,
					KeepLatest: flags.keepLatest,
					Plan:       reviewedPlan, Confirm: true, ConfirmDigest: confirmDigest, Compact: compact,
				})
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				if opts.plainOutput {
					fmt.Fprintln(cmd.OutOrStdout(), envelope.Data.Status)
					return nil
				}
				renderCloudSnapshotRetentionApply(cmd, envelope.Data)
				renderResponseMeta(cmd, opts, envelope.Meta)
				return nil
			}
			runtimeConfig, cloudConfig, err := loadCloudCLIConfig(opts, flags.cloudConfigPath)
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			result, err := cloudstorage.ApplySnapshotRetention(ctx, cloudstorage.SnapshotRetentionApplyInput{
				SnapshotRetentionInput: cloudstorage.SnapshotRetentionInput{
					Config:     cloudConfig,
					NodeID:     firstNonEmptyCLI(flags.nodeID, runtimeConfig.NodeID),
					StateDir:   firstNonEmptyCLI(flags.stateDir, cloudConfig.StateDir),
					KeepLatest: flags.keepLatest,
				},
				Plan: reviewedPlan, Confirm: true, ConfirmDigest: confirmDigest, Compact: compact,
			})
			if err != nil {
				return renderError(cmd, opts, correlationID, err)
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(cloudLocalSuccess(correlationID, result))
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return nil
			}
			renderCloudSnapshotRetentionApply(cmd, result)
			return nil
		},
	}
	bindCloudSnapshotRetentionFlags(cmd, &flags)
	cmd.Flags().BoolVar(&confirm, "confirm", false, "confirm applying the exact reviewed retention plan")
	cmd.Flags().StringVar(&planPath, "plan", "", "path to the exact reviewed retention plan JSON (required for Borg)")
	cmd.Flags().StringVar(&confirmDigest, "confirm-digest", "", "exact plan digest to confirm (required for Borg)")
	cmd.Flags().BoolVar(&compact, "compact", false, "run separately reported Borg compact after verified prune")
	return cmd
}

func readCloudRetentionPlan(planPath string) (cloudstorage.SnapshotRetentionPlan, error) {
	planPath = strings.TrimSpace(planPath)
	if planPath == "" {
		return cloudstorage.SnapshotRetentionPlan{}, fmt.Errorf("retention plan path is required")
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		return cloudstorage.SnapshotRetentionPlan{}, fmt.Errorf("read retention plan: %w", err)
	}
	var plan cloudstorage.SnapshotRetentionPlan
	if err := json.Unmarshal(raw, &plan); err == nil && plan.Status != "" {
		return plan, nil
	}
	var envelope struct {
		Data cloudstorage.SnapshotRetentionPlan `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Data.Status == "" {
		return cloudstorage.SnapshotRetentionPlan{}, fmt.Errorf("retention plan JSON does not contain a plan")
	}
	return envelope.Data, nil
}

func loadCloudCLIConfig(opts *options, cloudConfigPath string) (config.Config, cloudstorage.Config, error) {
	runtimeConfig, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
	if err != nil {
		return config.Config{}, cloudstorage.Config{}, err
	}
	load, err := cloudstorage.LoadConfig(cloudConfigPath)
	if err != nil {
		return config.Config{}, cloudstorage.Config{}, err
	}
	return runtimeConfig, load.Config, nil
}

func loadCloudCLIConfigForLocalSnapshotVerify(opts *options, cloudConfigPath, ref string) (config.Config, cloudstorage.Config, error) {
	runtimeConfig, err := config.Load(config.Overrides{ConfigFile: opts.configFile})
	if err != nil {
		return config.Config{}, cloudstorage.Config{}, err
	}
	load, err := cloudstorage.LoadConfig(cloudConfigPath)
	if err != nil {
		return config.Config{}, cloudstorage.Config{}, cloudSnapshotVerifyLocalError(ref, err)
	}
	if !load.Exists {
		return config.Config{}, cloudstorage.Config{}, loomerrors.New(
			"cloud.snapshot_verify_local_config_missing",
			"cloud",
			load.Path,
			"Local cloud snapshot verify requires a readable cloud config; omit --local to verify through loomd service context.",
		)
	}
	return runtimeConfig, load.Config, nil
}

func cloudSnapshotVerifyLocalError(ref string, cause error) error {
	if cause == nil {
		return nil
	}
	if cloudstorage.ServiceContextHint(cause) == "" {
		return cause
	}
	target := strings.TrimSpace(ref)
	if target == "" {
		target = "latest"
	}
	return loomerrors.Wrap(
		"cloud.snapshot_verify_service_context_required",
		"cloud",
		target,
		"Cloud snapshot verify requires the LOOM service context because cloud credentials or lock state are not readable by this user.",
		cause,
	)
}

func renderCloudSnapshotPush(cmd *cobra.Command, result cloudstorage.SnapshotPushResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot push: %s\n", result.Status)
	if result.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	}
	fmt.Fprintf(out, "Backup: %s\n", result.BackupDir)
	if result.Archive != "" {
		fmt.Fprintf(out, "Archive: %s\n", result.Archive)
	}
	fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	if result.DryRun {
		fmt.Fprintln(out, "Dry-run: true")
	}
	fmt.Fprintf(out, "Files: %d bytes=%d\n", result.FileCount, result.TotalBytes)
	if snapshotMetricsAny(result.Metrics) {
		fmt.Fprintf(out, "Timing: total=%s verify=%s coverage=%s inventory=%s remote_list=%s remote_copy=%s remote_check=%s promote=%s manifest_upload=%s manifest_write=%s borg_create=%s borg_info=%s borg_check=%s\n",
			formatDurationMS(result.Metrics.TotalDurationMS),
			formatDurationMS(result.Metrics.LocalBackupVerifyDurationMS),
			formatDurationMS(result.Metrics.CoverageCheckDurationMS),
			formatDurationMS(result.Metrics.InventoryDurationMS),
			formatDurationMS(result.Metrics.RemoteListDurationMS),
			formatDurationMS(result.Metrics.RemoteCopyDurationMS),
			formatDurationMS(result.Metrics.RemoteCheckDurationMS),
			formatDurationMS(result.Metrics.RemotePromoteDurationMS),
			formatDurationMS(result.Metrics.ManifestUploadDurationMS),
			formatDurationMS(result.Metrics.ManifestWriteDurationMS),
			formatDurationMS(result.Metrics.BorgCreateDurationMS),
			formatDurationMS(result.Metrics.BorgInfoDurationMS),
			formatDurationMS(result.Metrics.BorgCheckDurationMS),
		)
	}
	if result.Error != "" {
		fmt.Fprintf(out, "Error: %s\n", result.Error)
	}
	renderStringMap(cmd, "Checks", result.Checks)
}

func cloudSnapshotRunStatus(raw json.RawMessage) string {
	var summary struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &summary)
	return firstNonEmptyString(summary.Status, "unknown")
}

func renderCloudSnapshotWorkerRun(cmd *cobra.Command, runID string, raw json.RawMessage) {
	var summary struct {
		Status               string `json:"status"`
		Phase                string `json:"phase"`
		Committed            bool   `json:"committed"`
		Idempotent           bool   `json:"idempotent"`
		Archive              string `json:"archive"`
		ArchiveRef           string `json:"archive_ref"`
		RemoteURI            string `json:"remote_uri"`
		OperationalPackageID string `json:"operational_package_id"`
		ManifestSHA256       string `json:"manifest_sha256"`
	}
	_ = json.Unmarshal(raw, &summary)
	fmt.Fprintf(cmd.OutOrStdout(), "Run: %s\n", runID)
	fmt.Fprintf(cmd.OutOrStdout(), "Cloud snapshot push: %s\n", firstNonEmptyString(summary.Status, "unknown"))
	fmt.Fprintf(cmd.OutOrStdout(), "Phase: %s committed=%t idempotent=%t\n", firstNonEmptyString(summary.Phase, "direct_borg_archive"), summary.Committed, summary.Idempotent)
	if summary.Archive != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Archive: %s ref=%s\n", summary.Archive, summary.ArchiveRef)
	}
	if summary.RemoteURI != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Remote: %s\n", summary.RemoteURI)
	}
	if summary.OperationalPackageID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Operational package: %s\n", summary.OperationalPackageID)
	}
	if summary.ManifestSHA256 != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Manifest: %s\n", summary.ManifestSHA256)
	}
}

func snapshotMetricsAny(metrics cloudstorage.SnapshotMetrics) bool {
	return metrics.TotalDurationMS > 0 ||
		metrics.LocalBackupVerifyDurationMS > 0 ||
		metrics.CoverageCheckDurationMS > 0 ||
		metrics.InventoryDurationMS > 0 ||
		metrics.RemoteListDurationMS > 0 ||
		metrics.RemoteCopyDurationMS > 0 ||
		metrics.RemoteCheckDurationMS > 0 ||
		metrics.RemotePromoteDurationMS > 0 ||
		metrics.ManifestUploadDurationMS > 0 ||
		metrics.ManifestWriteDurationMS > 0 ||
		metrics.BorgCreateDurationMS > 0 ||
		metrics.BorgInfoDurationMS > 0 ||
		metrics.BorgCheckDurationMS > 0
}

func renderCloudSnapshotList(cmd *cobra.Command, result cloudstorage.SnapshotListResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshots: %s\n", result.Status)
	if result.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	}
	if result.Repository != "" {
		fmt.Fprintf(out, "Repository: %s\n", result.Repository)
	}
	if result.Code != "" {
		fmt.Fprintf(out, "Code: %s\n", result.Code)
	}
	if len(result.Snapshots) == 0 {
		fmt.Fprintln(out, "No cloud snapshots found.")
		if result.Error != "" {
			fmt.Fprintf(out, "Error: %s\n", result.Error)
		}
		if result.RepairHint != "" {
			fmt.Fprintf(out, "Repair: %s\n", result.RepairHint)
		}
		return
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(writer, "REF\tBACKEND\tSTATUS\tREMOTE")
	for _, item := range result.Snapshots {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", item.Ref, item.Backend, item.Status, item.RemoteURI)
	}
	_ = writer.Flush()
}

func renderCloudSnapshotVerify(cmd *cobra.Command, result cloudstorage.SnapshotVerifyResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot verify: %s\n", result.Status)
	fmt.Fprintf(out, "Profile: %s\n", result.Profile)
	if result.Coverage != "" {
		fmt.Fprintf(out, "Coverage: %s\n", result.Coverage)
	}
	if result.MaxDurationSeconds > 0 {
		fmt.Fprintf(out, "Maximum duration: %s\n", (time.Duration(result.MaxDurationSeconds) * time.Second).String())
	}
	if result.Ref != "" {
		fmt.Fprintf(out, "Ref: %s\n", result.Ref)
	}
	if result.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	}
	if result.Archive != "" {
		fmt.Fprintf(out, "Archive: %s\n", result.Archive)
	}
	fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	if result.ManifestPath != "" {
		fmt.Fprintf(out, "Manifest: %s\n", result.ManifestPath)
	}
	renderStringMap(cmd, "Checks", result.Checks)
	for _, err := range result.Errors {
		fmt.Fprintf(out, "Error: %s\n", err)
	}
}

func renderCloudSnapshotFetch(cmd *cobra.Command, result cloudstorage.SnapshotFetchResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot fetch: %s\n", result.Status)
	fmt.Fprintf(out, "Ref: %s\n", result.Ref)
	if result.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	}
	if result.Archive != "" {
		fmt.Fprintf(out, "Archive: %s\n", result.Archive)
	}
	fmt.Fprintf(out, "Target: %s\n", result.TargetDir)
	fmt.Fprintf(out, "Verification: %s\n", result.Verification.Status)
}

func renderCloudRestoreDrill(cmd *cobra.Command, result cloudstorage.CloudRestoreDrillResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud restore drill: %s\n", result.Status)
	fmt.Fprintf(out, "Ref: %s\n", result.Ref)
	if result.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", result.Backend)
	}
	if result.Archive != "" {
		fmt.Fprintf(out, "Archive: %s\n", result.Archive)
	}
	if result.RemoteURI != "" {
		fmt.Fprintf(out, "Remote: %s\n", result.RemoteURI)
	}
	fmt.Fprintf(out, "Staging: %s\n", result.StagingDir)
	if result.Plan != nil {
		fmt.Fprintf(out, "Target database: %s\n", result.Plan.TargetDatabase)
	}
	if result.Result != nil {
		fmt.Fprintf(out, "Target database: %s\n", result.Result.TargetDatabase)
	}
	if result.DirectPlan != nil {
		fmt.Fprintf(out, "Direct manifest: %s\n", result.DirectPlan.ManifestSHA256)
		fmt.Fprintf(out, "Operational package: %s\n", result.DirectPlan.OperationalVerification.ManifestSHA256)
		fmt.Fprintf(out, "Provenance package: %s\n", result.DirectPlan.ProvenanceVerification.ManifestSHA256)
	}
	if result.DirectResult != nil {
		fmt.Fprintf(out, "Operational recovery: %s\n", result.DirectResult.OperationalRecovery.Status)
		fmt.Fprintf(out, "Provenance recovery: %s\n", result.DirectResult.ProvenanceRecovery.Status)
	}
}

func renderCloudSnapshotRetentionPlan(cmd *cobra.Command, plan cloudstorage.SnapshotRetentionPlan) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot retention: %s\n", plan.Status)
	fmt.Fprintf(out, "Node: %s\n", plan.NodeID)
	if plan.Backend != "" {
		fmt.Fprintf(out, "Backend: %s\n", plan.Backend)
	}
	if plan.Repository != "" {
		fmt.Fprintf(out, "Repository: %s\n", plan.Repository)
		fmt.Fprintf(out, "Repository ID: %s\n", plan.RepositoryID)
	}
	fmt.Fprintf(out, "Remote root: %s\n", plan.RemoteRoot)
	if plan.Backend == cloudstorage.SnapshotBackendBorg {
		fmt.Fprintf(out, "Policy: %d daily / %d weekly / %d monthly\n", plan.Policy.Daily, plan.Policy.Weekly, plan.Policy.Monthly)
		fmt.Fprintf(out, "Inventory digest: %s\nPlan digest: %s\n", plan.InventoryDigest, plan.PlanDigest)
	} else {
		fmt.Fprintf(out, "Keep latest: %d\n", plan.KeepLatest)
	}
	if plan.LatestKeptRef != "" {
		fmt.Fprintf(out, "Latest retained: %s\n", plan.LatestKeptRef)
	}
	removeLabel := "Move"
	if plan.Backend == cloudstorage.SnapshotBackendBorg {
		removeLabel = "Prune"
	}
	fmt.Fprintf(out, "Kept: %d  %s: %d  Ignored: %d\n", len(plan.Kept), removeLabel, len(plan.Remove), len(plan.Ignored))
	renderCloudSnapshotRetentionDecisions(cmd, removeLabel+" Candidates", plan.Remove)
	renderCloudSnapshotRetentionDecisions(cmd, "Ignored", plan.Ignored)
}

func renderCloudSnapshotRetentionApply(cmd *cobra.Command, result cloudstorage.SnapshotRetentionApplyResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cloud snapshot retention apply: %s\n", result.Status)
	actionLabel := "Moved"
	if result.Plan.Backend == cloudstorage.SnapshotBackendBorg {
		actionLabel = "Pruned"
	}
	fmt.Fprintf(out, "%s: %d\n", actionLabel, len(result.Moved))
	if len(result.Moved) > 0 {
		writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "REF\tARCHIVE\tFROM\tTO")
		for _, moved := range result.Moved {
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", moved.Ref, moved.Archive, moved.FromRemoteURI, moved.ToRemoteURI)
		}
		_ = writer.Flush()
	}
	for _, moveErr := range result.Errors {
		fmt.Fprintf(out, "Error: %s %s\n", moveErr.Ref, moveErr.Error)
	}
	if result.Plan.Backend == cloudstorage.SnapshotBackendBorg {
		fmt.Fprintf(out, "Prune: %s (%s)\n", result.Prune.Status, result.Prune.Verification)
		fmt.Fprintf(out, "Compact: %s (%s)\n", result.Compact.Status, result.Compact.Verification)
		if result.ReclaimedBytes == nil {
			fmt.Fprintln(out, "Reclaimed bytes: not claimed")
		} else {
			fmt.Fprintf(out, "Reclaimed bytes: %d\n", *result.ReclaimedBytes)
		}
	}
}

func renderCloudSnapshotRetentionDecisions(cmd *cobra.Command, title string, items []cloudstorage.SnapshotRetentionDecision) {
	if len(items) == 0 {
		return
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintf(writer, "%s:\n", title)
	fmt.Fprintln(writer, "REF\tACTION\tARCHIVE\tREASON\tREMOTE")
	for _, item := range items {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", item.Ref, item.Action, item.Archive, item.Reason, item.RemoteURI)
	}
	_ = writer.Flush()
}

func renderStringMap(cmd *cobra.Command, title string, values map[string]string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s:\n", title)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s\n", key, values[key])
	}
}

func firstNonEmptyCLI(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newCloudRestoreCleanupCommand(opts *options) *cobra.Command {
	parent := &cobra.Command{Use: "restore-cleanup", Short: "Review and clean exactly one failed restore attempt through the daemon"}
	for _, apply := range []bool{false, true} {
		input := cloudstorage.RestoreCleanupApplyInput{}
		verb := "plan"
		if apply {
			verb = "apply"
		}
		cmd := &cobra.Command{
			Use: verb + " <attempt-basename>", Short: "Review one attempt; apply requires its exact digest and explicit confirmation", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				commandCtx, err := resolveCommandContext(opts)
				if err != nil {
					return renderError(cmd, opts, correlation.Normalize(opts.correlationID), err)
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
				defer cancel()
				input.Attempt = args[0]
				var envelope response.Envelope[cloudstorage.RestoreCleanupResult]
				if apply {
					envelope, err = commandCtx.Client.CloudRestoreCleanupApply(ctx, commandCtx.CorrelationID, input)
				} else {
					envelope, err = commandCtx.Client.CloudRestoreCleanupPlan(ctx, commandCtx.CorrelationID, cloudstorage.RestoreCleanupPlanInput{Attempt: args[0]})
				}
				if err != nil {
					return renderError(cmd, opts, commandCtx.CorrelationID, err)
				}
				if opts.jsonOutput {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
				}
				result := envelope.Data
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s\nplan_sha256: %s\nentries: %d; logical_bytes: %d; confirmed_removed: %d\n", result.Status, result.Attempt, result.PlanDigest, result.Entries, result.LogicalBytes, result.ConfirmedRemoved)
				return nil
			},
		}
		if apply {
			cmd.Flags().StringVar(&input.ConfirmDigest, "confirm-digest", "", "exact reviewed SHA-256 from plan (also required for dry-run)")
			cmd.Flags().BoolVar(&input.Yes, "yes", false, "explicitly authorize deletion of the exact reviewed backup payload")
			cmd.Flags().BoolVar(&input.DryRun, "dry-run", false, "revalidate the complete plan without writing or deleting")
		}
		parent.AddCommand(cmd)
	}
	return parent
}
