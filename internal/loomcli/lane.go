package loomcli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"loom.local/loom/internal/box"
	loomerrors "loom.local/loom/internal/errors"
	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/lane"
)

func newLaneCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lane",
		Short: "Inspect and send LOOM Lane pending transfer state",
	}
	cmd.AddCommand(newLaneStatusCommand(opts))
	cmd.AddCommand(newLaneSendCommand(opts))
	cmd.AddCommand(newLanePublishCommand(opts, false))
	cmd.AddCommand(newLanePublishCommand(opts, true))
	cmd.AddCommand(newLaneAcknowledgePendingCommand(opts))
	cmd.AddCommand(newLaneTransferAttentionCommand(opts, false))
	cmd.AddCommand(newLaneTransferAttentionCommand(opts, true))
	cmd.AddCommand(newLaneStagingCommand(opts))
	cmd.AddCommand(newLaneAcceptReceivedCommand(opts))
	cmd.AddCommand(newLaneAcceptBundleCommand(opts))
	return cmd
}

func newLaneStagingCommand(opts *options) *cobra.Command {
	var operation string
	var sourceNodeKey string
	var batchID string
	var expectedRuntimeRoot string
	var expectedReceiverUser string
	cmd := &cobra.Command{
		Use:    "staging",
		Short:  "Manage one confined received Lane staging batch on main",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := resolveCLIConfig(opts)
			if err != nil {
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(cfg.NodeKind), "main") && !strings.EqualFold(strings.TrimSpace(cfg.NodeRole), "main") {
				return fmt.Errorf("Lane staging lifecycle operations are only available on main")
			}
			result, err := lane.ManageRemoteStaging(lane.RemoteStagingInput{
				RuntimeRoot:          filepath.Join(cfg.DataDir, "lane"),
				ExpectedRuntimeRoot:  expectedRuntimeRoot,
				ExpectedReceiverUser: expectedReceiverUser,
				SourceNodeKey:        sourceNodeKey,
				BatchID:              batchID,
				Operation:            lane.RemoteStagingOperation(operation),
			})
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Lane staging %s: %s/%s (%s)\n", result.Operation, result.SourceNodeKey, result.BatchID, result.Status)
			return nil
		},
	}
	cmd.Flags().StringVar(&operation, "operation", "", "confined staging operation: prepare, resume, or cleanup")
	cmd.Flags().StringVar(&sourceNodeKey, "source-node", "", "normalized source node identity")
	cmd.Flags().StringVar(&batchID, "batch-id", "", "normalized Lane batch identity")
	cmd.Flags().StringVar(&expectedRuntimeRoot, "expected-runtime-root", "", "persisted Lane runtime root that main must attest before the operation")
	cmd.Flags().StringVar(&expectedReceiverUser, "expected-receiver-user", "", "persisted Lane receiver identity that main must attest before cleanup")
	_ = cmd.MarkFlagRequired("operation")
	_ = cmd.MarkFlagRequired("source-node")
	_ = cmd.MarkFlagRequired("batch-id")
	return cmd
}

func newLaneStatusCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	var includeFidelity bool
	var sourceOnly bool
	var exact bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show local LOOM Lane pending items and rsync/SSH preflight",
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := laneTransferProfile(sourceOnly, exact)
			if err != nil {
				return err
			}
			boxStatus, laneStatus, err := loadLaneStatusForCLI(opts, flags, profile)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(laneStatus)
			}
			if opts.plainOutput {
				fmt.Fprintln(cmd.OutOrStdout(), laneStatus.State)
				return nil
			}
			renderLaneStatus(cmd, boxStatus, laneStatus)
			if includeFidelity {
				renderLaneFidelitySummary(cmd, laneStatus)
			}
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&includeFidelity, "include-fidelity", false, "show pending-item fidelity warnings and transfer safety state")
	cmd.Flags().BoolVar(&sourceOnly, "source-only", false, "inspect managed reconstructible exclusions plus .loomignore")
	cmd.Flags().BoolVar(&exact, "exact", false, "inspect only mandatory safety exclusions and bypass .loomignore")
	return cmd
}

func newLaneSendCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	var sourceNode string
	var sourceBoxID string
	var mainHost string
	var remoteRoot string
	var keepLocal bool
	var allowCrossDevicePromotion bool
	var resume bool
	var sourceOnly bool
	var exact bool
	var bundle bool
	var noBundle bool
	var expectedPolicyFingerprint string
	cmd := &cobra.Command{
		Use:   "send",
		Short: "Send pending LOOM Lane files to main over rsync/SSH",
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, err := laneTransferProfile(sourceOnly, exact)
			if err != nil {
				return err
			}
			transport, err := laneRequestedTransport(bundle, noBundle)
			if err != nil {
				return err
			}
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			status := box.Inspect(resolved)
			statePath, err := laneStatePathForMutation(status)
			if err != nil {
				return err
			}
			source := strings.TrimSpace(sourceNode)
			if source == "" {
				source = status.OwnerNode
			}
			laneRelPath := laneRelPathFromBoxStatus(status)
			if strings.TrimSpace(sourceBoxID) == "" && status.Contract != nil {
				sourceBoxID = strings.TrimSpace(status.Contract.BoxID)
			}
			input := lane.SendInput{
				RootPath:                  resolved.RootPath,
				LaneRelPath:               laneRelPath,
				StatePath:                 statePath,
				SourceNodeKey:             source,
				SourceBoxID:               sourceBoxID,
				MainHost:                  mainHost,
				RemoteRoot:                remoteRoot,
				DryRun:                    flags.dryRun,
				KeepLocal:                 keepLocal,
				AllowCrossDevicePromotion: allowCrossDevicePromotion,
				Resume:                    resume,
				Profile:                   profile,
				ExpectedPolicyFingerprint: expectedPolicyFingerprint,
				RequestedTransport:        transport,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
			defer cancel()
			result, err := lane.Send(ctx, input)
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			renderLaneSendResult(cmd, result)
			return err
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show transfer commands without sending or deleting files")
	cmd.Flags().StringVar(&sourceNode, "source-node", "", "source node key, defaults to the resolved Box owner")
	cmd.Flags().StringVar(&sourceBoxID, "source-box-id", "", "optional source Box identifier to attach to catalog metadata")
	cmd.Flags().StringVar(&mainHost, "main-host", lane.DefaultMainHost, "SSH host alias for LOOM main")
	cmd.Flags().StringVar(&remoteRoot, "remote-root", lane.DefaultRemoteRoot, "remote Lane root on main")
	cmd.Flags().BoolVar(&keepLocal, "keep-local", false, "keep visible Lane files after successful transfer")
	cmd.Flags().BoolVar(&allowCrossDevicePromotion, "allow-cross-device-promotion", false, "explicitly allow free-space-checked copy/verify/rename when Lane staging and imports use different filesystems")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume an interrupted transfer with rsync append-verify when supported")
	cmd.Flags().BoolVar(&sourceOnly, "source-only", false, "exclude managed reconstructible dependencies while honoring .loomignore")
	cmd.Flags().BoolVar(&exact, "exact", false, "bypass .loomignore while retaining mandatory transfer safety exclusions")
	cmd.Flags().BoolVar(&bundle, "bundle", false, "force a verified uncompressed PAX bundle instead of the automatic transport choice")
	cmd.Flags().BoolVar(&noBundle, "no-bundle", false, "force the existing file-tree transfer instead of the automatic transport choice")
	cmd.Flags().StringVar(&expectedPolicyFingerprint, "expected-policy-fingerprint", "", "refuse send if policy differs from a reviewed dry run")
	return cmd
}

func laneTransferProfile(sourceOnly, exact bool) (filepolicy.Profile, error) {
	if sourceOnly && exact {
		return "", fmt.Errorf("choose either --source-only or --exact, not both")
	}
	if sourceOnly {
		return filepolicy.ProfileSourceOnly, nil
	}
	if exact {
		return filepolicy.ProfileExact, nil
	}
	return filepolicy.ProfileFaithful, nil
}

func laneRequestedTransport(bundle, noBundle bool) (lane.TransportMode, error) {
	if bundle && noBundle {
		return "", fmt.Errorf("choose either --bundle or --no-bundle, not both")
	}
	if bundle {
		return lane.TransportModeBundleSeed, nil
	}
	if noBundle {
		return lane.TransportModeFileTree, nil
	}
	return lane.TransportModeAuto, nil
}

func newLaneAcknowledgePendingCommand(opts *options) *cobra.Command {
	flags := boxCommandFlags{}
	var note string
	cmd := &cobra.Command{
		Use:   "acknowledge-pending <relative-path>",
		Short: "Acknowledge a pending LOOM Lane item without deleting or sending it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			status := box.Inspect(resolved)
			statePath, err := laneStatePathForMutation(status)
			if err != nil {
				return err
			}
			result, err := lane.AcknowledgePendingItem(lane.AcknowledgePendingInput{
				RootPath:     resolved.RootPath,
				LaneRelPath:  laneRelPathFromBoxStatus(status),
				StatePath:    statePath,
				RelativePath: args[0],
				Note:         note,
			})
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			if err != nil {
				return err
			}
			renderLaneAttentionResult(cmd, "LOOM Lane pending item acknowledged", result)
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().StringVar(&note, "note", "", "optional acknowledgement note")
	return cmd
}

func newLaneTransferAttentionCommand(opts *options, archive bool) *cobra.Command {
	flags := boxCommandFlags{}
	var note string
	use := "acknowledge-transfer <batch-id>"
	short := "Acknowledge failed LOOM Lane transfer attention without deleting history"
	label := "LOOM Lane failed transfer acknowledged"
	if archive {
		use = "archive-transfer <batch-id>"
		short = "Archive failed LOOM Lane transfer attention without deleting history"
		label = "LOOM Lane failed transfer attention archived"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			status := box.Inspect(resolved)
			statePath, err := laneStatePathForMutation(status)
			if err != nil {
				return err
			}
			input := lane.AcknowledgeTransferInput{
				RootPath:  resolved.RootPath,
				StatePath: statePath,
				BatchID:   args[0],
				Note:      note,
			}
			var result lane.AttentionResult
			if archive {
				result, err = lane.ArchiveTransferAttention(input)
			} else {
				result, err = lane.AcknowledgeTransferAttention(input)
			}
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			if err != nil {
				return err
			}
			renderLaneAttentionResult(cmd, label, result)
			return nil
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().StringVar(&note, "note", "", "optional acknowledgement note")
	return cmd
}

func laneRelPathFromBoxStatus(status box.Status) string {
	if status.Lane != nil && strings.TrimSpace(status.Lane.LaneRelativePath) != "" {
		return status.Lane.LaneRelativePath
	}
	if status.Contract != nil {
		if area, ok := status.Contract.Areas[box.AreaLane]; ok && strings.TrimSpace(area.Path) != "" {
			return area.Path
		}
	}
	return lane.DefaultLaneRelPath
}

func newLanePublishCommand(opts *options, repairAlias bool) *cobra.Command {
	flags := boxCommandFlags{}
	var mainHost string
	var remoteRoot string
	use := "publish <batch-id>"
	short := "Compatibility alias for repairing Lane main custody without re-uploading"
	if repairAlias {
		use = "repair <batch-id>"
		short = "Repair Lane imports promotion, catalog, or staging cleanup without re-uploading"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolveBoxForCLI(opts, flags)
			if err != nil {
				return err
			}
			status := box.Inspect(resolved)
			statePath, err := laneStatePathForMutation(status)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()
			result, err := lane.Publish(ctx, lane.PublishInput{
				RootPath:    resolved.RootPath,
				LaneRelPath: laneRelPathFromBoxStatus(status),
				StatePath:   statePath,
				BatchID:     args[0],
				MainHost:    mainHost,
				RemoteRoot:  remoteRoot,
				DryRun:      flags.dryRun,
			})
			if opts.jsonOutput {
				encodeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				if err != nil {
					return err
				}
				return encodeErr
			}
			renderLanePublishResult(cmd, result, repairAlias)
			return err
		},
	}
	addBoxFlags(cmd, &flags)
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "show main-custody repair commands without running them")
	cmd.Flags().StringVar(&mainHost, "main-host", lane.DefaultMainHost, "SSH host alias for LOOM main")
	cmd.Flags().StringVar(&remoteRoot, "remote-root", lane.DefaultRemoteRoot, "remote Lane root on main")
	return cmd
}

func newLaneAcceptReceivedCommand(opts *options) *cobra.Command {
	var input lane.AcceptInput
	cmd := &cobra.Command{
		Use:    "accept-received",
		Short:  "Accept an already-rsynced Lane batch on main",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.AcceptLaneCustody(ctx, commandCtx.CorrelationID, input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not accept LOOM Lane batch.", err))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(envelope)
			}
			renderLaneAcceptResult(cmd, envelope.Data)
			renderResponseMeta(cmd, opts, envelope.Meta)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.SourceNodeKey, "source-node", "", "source node key that sent the Lane batch")
	cmd.Flags().StringVar(&input.SourceBoxID, "source-box-id", "", "optional source Box identifier")
	cmd.Flags().StringVar(&input.BatchID, "batch-id", "", "Lane batch ID")
	cmd.Flags().StringVar(&input.AcceptedPath, "accepted-path", "", "staged batch path on main")
	cmd.Flags().StringVar(&input.RemoteRoot, "remote-root", lane.DefaultRemoteRoot, "remote Lane root on main")
	cmd.Flags().StringVar(&input.AcceptedDate, "date", "", "visible Lane date in YYYY-MM-DD")
	cmd.Flags().StringVar(&input.CustodyNodeKey, "custody-node", "main", "custody node key")
	cmd.Flags().BoolVar(&input.SkipAppleDouble, "skip-apple-double", true, "skip macOS AppleDouble sidecar files")
	cmd.Flags().BoolVar(&input.AllowCrossDevicePromotion, "allow-cross-device-promotion", false, "explicitly allow free-space-checked copy/verify/rename into imports")
	_ = cmd.MarkFlagRequired("source-node")
	_ = cmd.MarkFlagRequired("batch-id")
	_ = cmd.MarkFlagRequired("accepted-path")
	return cmd
}

func newLaneAcceptBundleCommand(opts *options) *cobra.Command {
	var input lane.BundleAcceptInput
	cmd := &cobra.Command{
		Use:    "accept-bundle",
		Short:  "Verify, unpack, and accept one staged Lane bundle on main",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			commandCtx, err := resolveCommandContext(opts)
			if err != nil {
				return renderError(cmd, opts, "", err)
			}
			input.RemoteRoot = filepath.Join(commandCtx.Config.DataDir, "lane")
			input.TrustedRemoteRoot = true
			input.ImportsRoot = commandCtx.Config.ImportsRoot
			unpack, err := lane.UnpackBundle(input)
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("lane.bundle_invalid", "filesystem", input.ManifestPath, "Could not verify and unpack the staged LOOM Lane bundle.", err))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()
			envelope, err := commandCtx.Client.AcceptLaneCustody(ctx, commandCtx.CorrelationID, bundleLaneAcceptInput(input, unpack))
			if err != nil {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.Wrap("transport.unavailable", "runtime", commandCtx.Config.SocketPath, "Could not catalog the unpacked LOOM Lane bundle.", err))
			}
			result := lane.BundleAcceptResult{Unpack: unpack, Catalog: envelope.Data}
			if result.Catalog.FilesCataloged != unpack.FileCount || result.Catalog.TotalBytes != unpack.TotalBytes {
				return renderError(cmd, opts, commandCtx.CorrelationID, loomerrors.New("lane.bundle_catalog_mismatch", "storage", unpack.AcceptedPath, "Cataloged Lane bundle totals do not match the verified manifest; inspect the accepted batch before retrying publication."))
			}
			if opts.jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			renderLaneAcceptResult(cmd, result.Catalog)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.SourceNodeKey, "source-node", "", "source node key that sent the Lane bundle")
	cmd.Flags().StringVar(&input.SourceBoxID, "source-box-id", "", "source Box identifier recorded in the bundle manifest")
	cmd.Flags().StringVar(&input.BatchID, "batch-id", "", "Lane bundle batch ID")
	cmd.Flags().StringVar(&input.AcceptedPath, "accepted-path", "", "exact verified bundle tree under the Lane staging batch")
	cmd.Flags().StringVar(&input.RemoteRoot, "remote-root", lane.DefaultRemoteRoot, "remote Lane root on main")
	cmd.Flags().StringVar(&input.AcceptedDate, "date", "", "visible Lane date in YYYY-MM-DD")
	cmd.Flags().StringVar(&input.CustodyNodeKey, "custody-node", "main", "custody node key")
	cmd.Flags().StringVar(&input.ManifestPath, "manifest-path", "", "staged Lane bundle manifest path")
	cmd.Flags().StringVar(&input.ArchivePath, "archive-path", "", "staged Lane PAX archive path")
	cmd.Flags().BoolVar(&input.AllowCrossDevicePromotion, "allow-cross-device-promotion", false, "explicitly allow free-space-checked copy/verify/rename into imports")
	for _, name := range []string{"source-node", "batch-id", "accepted-path", "date", "manifest-path", "archive-path"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func bundleLaneAcceptInput(input lane.BundleAcceptInput, unpack lane.BundleUnpackResult) lane.AcceptInput {
	return lane.AcceptInput{
		AcceptedPath:              unpack.AcceptedPath,
		RemoteRoot:                input.RemoteRoot,
		TrustedRemoteRoot:         input.TrustedRemoteRoot,
		ImportsRoot:               input.ImportsRoot,
		SourceNodeKey:             input.SourceNodeKey,
		SourceBoxID:               input.SourceBoxID,
		BatchID:                   input.BatchID,
		AcceptedDate:              input.AcceptedDate,
		CustodyNodeKey:            input.CustodyNodeKey,
		SkipAppleDouble:           false,
		AllowCrossDevicePromotion: input.AllowCrossDevicePromotion,
	}
}

func loadLaneStatusForCLI(opts *options, flags boxCommandFlags, profile filepolicy.Profile) (box.Status, lane.Status, error) {
	resolved, err := resolveBoxForCLI(opts, flags)
	if err != nil {
		return box.Status{}, lane.Status{}, err
	}
	boxStatus := box.Inspect(resolved)
	if boxStatus.State == "invalid" || strings.TrimSpace(boxStatus.RuntimeStateReadRoot) == "" {
		return boxStatus, lane.Status{}, fmt.Errorf("LOOM Box runtime state is unavailable at %s; inspect Box status and reconcile any divergent state roots", boxStatus.RootPath)
	}
	if boxStatus.Lane != nil && profile == filepolicy.ProfileFaithful {
		return boxStatus, *boxStatus.Lane, nil
	}
	laneStatus := lane.BuildStatus(lane.StatusInput{
		RootPath:    resolved.RootPath,
		LaneRelPath: lane.DefaultLaneRelPath,
		StatePath:   filepath.Join(boxStatus.RuntimeStateReadRoot, "lane"),
		MainHost:    lane.DefaultMainHost,
		Profile:     profile,
	})
	return boxStatus, laneStatus, nil
}

func laneStatePathForMutation(status box.Status) (string, error) {
	root := strings.TrimSpace(status.RuntimeStateWriteRoot)
	if status.State == "invalid" || root == "" {
		return "", fmt.Errorf("LOOM Box runtime state is unavailable at %s; inspect Box status and reconcile any divergent state roots", status.RootPath)
	}
	return filepath.Join(root, "lane"), nil
}

func renderLaneStatus(cmd *cobra.Command, boxStatus box.Status, status lane.Status) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane: %s\n", dashIfEmpty(status.State))
	fmt.Fprintf(cmd.OutOrStdout(), "Box: %s\n", dashIfEmpty(boxStatus.RootPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Box profile: %s\n", dashIfEmpty(boxStatus.Profile))
	fmt.Fprintf(cmd.OutOrStdout(), "Transfer policy: %s version=%s fingerprint=%s\n", status.Profile, dashIfEmpty(status.PolicyVersion), dashIfEmpty(status.PolicyFingerprint))
	fmt.Fprintf(cmd.OutOrStdout(), "Path: %s\n", dashIfEmpty(status.LanePath))
	fmt.Fprintf(cmd.OutOrStdout(), "State path: %s\n", dashIfEmpty(status.StatePath))
	fmt.Fprintf(cmd.OutOrStdout(), "Pending: %d items, %d files, %d dirs, %s\n",
		status.PendingItems,
		status.PendingFiles,
		status.PendingDirs,
		formatLaneBytes(status.PendingBytes),
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Ignored: %d files, %s\n", status.IgnoredFileCount, formatLaneBytes(status.IgnoredBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Recovery storage: retained=%s grace=%s protected=%s cleanup=%s safety=%s untracked=%s next_expiry=%s\n",
		formatLaneBytes(status.RecoveryStorage.RetainedBytes),
		formatLaneBytes(status.RecoveryStorage.SuccessfulGraceBytes),
		formatLaneBytes(status.RecoveryStorage.ProtectedEvidenceBytes),
		formatLaneBytes(status.RecoveryStorage.CleanupQuarantineBytes),
		formatLaneBytes(status.RecoveryStorage.TransportSafetyBytes),
		formatLaneBytes(status.RecoveryStorage.UntrackedBytes),
		laneTimePtrOrDash(status.RecoveryStorage.NextSuccessfulQuarantineExpiresAt),
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Transport recommendation: %s reason=%s average=%s temporary=%s\n",
		dashIfEmpty(string(status.Transport.RecommendedMode)),
		dashIfEmpty(status.Transport.Reason),
		formatLaneBytes(status.Transport.AverageFileBytes),
		formatLaneBytes(status.Transport.EstimatedTemporaryBytes),
	)
	if status.PendingItems > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Pending attention: active=%d acknowledged=%d\n", status.ActivePendingItems, status.AcknowledgedPendingItems)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Preflight: %s\n", dashIfEmpty(status.Preflight.Status))
	fmt.Fprintf(cmd.OutOrStdout(), "  main: %s\n", dashIfEmpty(status.Preflight.MainHost))
	fmt.Fprintf(cmd.OutOrStdout(), "  rsync: %s\n", dashIfEmpty(status.Preflight.RsyncPath))
	fmt.Fprintf(cmd.OutOrStdout(), "  ssh: %s\n", dashIfEmpty(status.Preflight.SSHPath))
	fmt.Fprintf(cmd.OutOrStdout(), "  ssh config: %s\n", dashIfEmpty(status.Preflight.SSHConfigStatus))
	if status.LastTransfer != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Last transfer: %s status=%s files=%d bytes=%s completed=%s\n",
			dashIfEmpty(status.LastTransfer.BatchID),
			dashIfEmpty(status.LastTransfer.Status),
			status.LastTransfer.FileCount,
			formatLaneBytes(status.LastTransfer.TotalBytes),
			laneTimePtrOrDash(status.LastTransfer.CompletedAt),
		)
		fmt.Fprintf(cmd.OutOrStdout(), "  transport: requested=%s selected=%s reason=%s\n",
			dashIfEmpty(string(status.LastTransfer.RequestedTransport)),
			dashIfEmpty(string(status.LastTransfer.SelectedTransport)),
			dashIfEmpty(status.LastTransfer.TransportReason),
		)
		if status.LastTransfer.BundleArtifactPath != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  bundle: artifact=%s archive_sha256=%s cleanup=%s\n",
				status.LastTransfer.BundleArtifactPath,
				dashIfEmpty(status.LastTransfer.BundleArchiveSHA256),
				dashIfEmpty(status.LastTransfer.BundleCleanupState),
			)
		}
		if status.LastTransfer.LocalCleanupQuarantinePath != "" || len(status.LastTransfer.RestoredLocalItems) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "  cleanup recovery: quarantine=%s state=%s bytes=%s expires=%s removed_at=%s reason=%s quarantined=%d restored=%d removed=%d\n",
				dashIfEmpty(status.LastTransfer.LocalCleanupQuarantinePath),
				dashIfEmpty(status.LastTransfer.LocalCleanupQuarantineState),
				formatLaneBytes(status.LastTransfer.LocalCleanupQuarantineBytes),
				laneTimePtrOrDash(status.LastTransfer.LocalCleanupQuarantineExpiresAt),
				laneTimePtrOrDash(status.LastTransfer.LocalCleanupQuarantineRemovedAt),
				dashIfEmpty(status.LastTransfer.LocalCleanupQuarantineRemovalReason),
				len(status.LastTransfer.QuarantinedLocalItems),
				len(status.LastTransfer.RestoredLocalItems),
				len(status.LastTransfer.RemovedLocalItems),
			)
		}
		if status.LastTransfer.AttentionStatus != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  attention: %s\n", status.LastTransfer.AttentionStatus)
		}
		if status.LastTransfer.ErrorMessage != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "  error: %s\n", status.LastTransfer.ErrorMessage)
		}
		if laneMetricsAny(status.LastTransfer.Metrics) {
			fmt.Fprintf(cmd.OutOrStdout(), "  timing: total=%s rsync=%s accept=%s catalog=%s legacy_export=%s cleanup=%s\n",
				formatDurationMS(status.LastTransfer.Metrics.TotalDurationMS),
				formatDurationMS(status.LastTransfer.Metrics.RsyncDurationMS),
				formatDurationMS(status.LastTransfer.Metrics.RemoteAcceptDurationMS),
				formatDurationMS(status.LastTransfer.Metrics.CatalogDurationMS),
				formatDurationMS(firstNonZeroInt64(status.LastTransfer.Metrics.ExportRefreshDurationMS, status.LastTransfer.Metrics.ExportRebuildDurationMS)),
				formatDurationMS(status.LastTransfer.Metrics.LocalCleanupDurationMS),
			)
		}
		renderLaneProgress(cmd, "  progress", status.LastTransfer.Progress)
	}
	if len(status.Items) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Pending items:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "PATH\tKIND\tFILES\tDIRS\tSIZE\tAGE\tATTENTION\tMODIFIED")
		for _, item := range status.Items {
			fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\n",
				item.RelativePath,
				item.Kind,
				item.FileCount,
				item.DirCount,
				formatLaneBytes(item.Bytes),
				formatLaneAge(item.AgeSeconds),
				dashIfEmpty(item.AttentionStatus),
				item.ModifiedAt.Format(time.RFC3339),
			)
		}
		_ = writer.Flush()
	}
	renderLaneNextActions(cmd, status)
	renderLaneDiagnostics(cmd, "Diagnostics", status.Diagnostics)
	renderLaneDiagnostics(cmd, "Preflight diagnostics", status.Preflight.Diagnostics)
	if status.State == lane.StateMissing {
		fmt.Fprintf(cmd.OutOrStdout(), "Next: loom box repair --path %s --profile %s\n", shellQuoteIfNeeded(boxStatus.RootPath), boxStatus.Profile)
	}
}

func renderLaneNextActions(cmd *cobra.Command, status lane.Status) {
	if len(status.Items) == 0 && (status.LastTransfer == nil || len(status.LastTransfer.NextActions) == 0) {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Safe next actions:")
	for _, item := range status.Items {
		if len(item.NextActions) == 0 {
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  %s (%s): %s\n", item.RelativePath, dashIfEmpty(item.AttentionStatus), item.AttentionReason)
		for _, action := range item.NextActions {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s [%s]: %s\n", action.Label, action.Risk, dashIfEmpty(action.Command))
		}
	}
	if status.LastTransfer != nil && len(status.LastTransfer.NextActions) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "  transfer %s (%s): %s\n", status.LastTransfer.BatchID, status.LastTransfer.AttentionStatus, dashIfEmpty(status.LastTransfer.ErrorMessage))
		for _, action := range status.LastTransfer.NextActions {
			fmt.Fprintf(cmd.OutOrStdout(), "    - %s [%s]: %s\n", action.Label, action.Risk, dashIfEmpty(action.Command))
		}
	}
}

func renderLaneAttentionResult(cmd *cobra.Command, title string, result lane.AttentionResult) {
	fmt.Fprintln(cmd.OutOrStdout(), title)
	fmt.Fprintf(cmd.OutOrStdout(), "Target: %s %s\n", dashIfEmpty(result.TargetKind), dashIfEmpty(result.TargetRef))
	fmt.Fprintf(cmd.OutOrStdout(), "Status: %s\n", dashIfEmpty(result.Status))
	fmt.Fprintf(cmd.OutOrStdout(), "Attention: %s\n", dashIfEmpty(result.AttentionStatus))
	fmt.Fprintf(cmd.OutOrStdout(), "Updated: %s\n", result.UpdatedAt.UTC().Format(time.RFC3339))
	if result.FileCount > 0 || result.DirCount > 0 || result.Bytes > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Items: files=%d dirs=%d size=%s\n", result.FileCount, result.DirCount, formatLaneBytes(result.Bytes))
	}
	if result.ModifiedAt != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "Modified: %s\n", result.ModifiedAt.UTC().Format(time.RFC3339))
	}
	if result.Note != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Note: %s\n", result.Note)
	}
}

func renderLaneFidelitySummary(cmd *cobra.Command, status lane.Status) {
	warningItems := 0
	observedItems := 0
	for _, item := range status.Items {
		if item.Fidelity != nil {
			observedItems++
		}
		if len(item.Warnings) > 0 {
			warningItems++
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "Fidelity:")
	fmt.Fprintf(cmd.OutOrStdout(), "  pending_items=%d observed_metadata=%d warning_items=%d ignored_entries=%d\n",
		status.PendingItems,
		observedItems,
		warningItems,
		status.IgnoredEntryCount,
	)
	if status.LastTransfer != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "  transfer_status=%s visible_on_main=%s\n",
			dashIfEmpty(status.LastTransfer.Status),
			dashIfEmpty(status.LastTransfer.VisibleStoragePath),
		)
	}
	for _, item := range status.Items {
		for _, warning := range item.Warnings {
			fmt.Fprintf(cmd.OutOrStdout(), "  warning path=%s %s\n", item.RelativePath, warning)
		}
	}
	renderLaneDiagnostics(cmd, "Fidelity diagnostics", status.Diagnostics)
}

func renderLaneSendResult(cmd *cobra.Command, result lane.SendResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane send: %s\n", dashIfEmpty(result.Status))
	if result.BatchID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Batch: %s\n", result.BatchID)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Source node: %s\n", dashIfEmpty(result.SourceNodeKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Transfer policy: %s version=%s fingerprint=%s\n", result.Profile, dashIfEmpty(result.PolicyVersion), dashIfEmpty(result.PolicyFingerprint))
	fmt.Fprintf(cmd.OutOrStdout(), "Transport: requested=%s recommended=%s selected=%s reason=%s\n",
		dashIfEmpty(string(result.RequestedTransport)),
		dashIfEmpty(string(result.Transport.RecommendedMode)),
		dashIfEmpty(string(result.SelectedTransport)),
		dashIfEmpty(result.TransportReason),
	)
	fmt.Fprintf(cmd.OutOrStdout(), "Transport estimate: archive=%s temporary=%s average_file=%s\n",
		formatLaneBytes(result.Transport.EstimatedArchiveBytes),
		formatLaneBytes(result.Transport.EstimatedTemporaryBytes),
		formatLaneBytes(result.Transport.AverageFileBytes),
	)
	if result.Transport.ForceWarning != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Transport warning: %s\n", result.Transport.ForceWarning)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Pending: %d items, %d files, %s\n", result.PendingItems, result.FileCount, formatLaneBytes(result.TotalBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "Ignored: %d files, %s\n", result.IgnoredFileCount, formatLaneBytes(result.IgnoredBytes))
	for _, warning := range result.Warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", warning)
	}
	if result.VisibleStoragePath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Canonical on main: %s\n", result.VisibleStoragePath)
	}
	if result.LocalSafetyPath != "" {
		label := "Safety copy"
		if result.SelectedTransport == lane.TransportModeBundleSeed {
			label = "Bundle safety artifact"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s cleanup=%s removed_at=%s reason=%s\n", label, result.LocalSafetyPath, dashIfEmpty(result.LocalSafetyCleanupState), laneTimePtrOrDash(result.LocalSafetyRemovedAt), dashIfEmpty(result.LocalSafetyRemovalReason))
	}
	if result.BundleArtifactPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Bundle: archive=%s manifest=%s bytes=%s archive_sha256=%s manifest_sha256=%s cleanup=%s resumed=%t\n",
			dashIfEmpty(result.BundleArchivePath),
			dashIfEmpty(result.BundleManifestPath),
			formatLaneBytes(result.BundleArchiveBytes),
			dashIfEmpty(result.BundleArchiveSHA256),
			dashIfEmpty(result.BundleManifestSHA256),
			dashIfEmpty(result.BundleCleanupState),
			result.BundleResumed,
		)
	}
	if result.RemoteAcceptedPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Remote custody reference: %s\n", result.RemoteAcceptedPath)
	}
	if result.LocalCleanupQuarantinePath != "" || len(result.RestoredLocalItems) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Cleanup recovery: quarantine=%s state=%s bytes=%s expires=%s removed_at=%s reason=%s quarantined=%d restored=%d removed=%d\n",
			dashIfEmpty(result.LocalCleanupQuarantinePath),
			dashIfEmpty(result.LocalCleanupQuarantineState),
			formatLaneBytes(result.LocalCleanupQuarantineBytes),
			laneTimePtrOrDash(result.LocalCleanupQuarantineExpiresAt),
			laneTimePtrOrDash(result.LocalCleanupQuarantineRemovedAt),
			dashIfEmpty(result.LocalCleanupQuarantineRemovalReason),
			len(result.QuarantinedLocalItems),
			len(result.RestoredLocalItems),
			len(result.RemovedLocalItems),
		)
	}
	if result.RecoveryStorage.RetainedBytes > 0 || result.RecoveryStorage.SuccessfulQuarantineCount > 0 || result.RecoveryStorage.ProtectedEvidenceCount > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Recovery storage after send: retained=%s grace=%s protected=%s cleanup=%s safety=%s next_expiry=%s\n",
			formatLaneBytes(result.RecoveryStorage.RetainedBytes),
			formatLaneBytes(result.RecoveryStorage.SuccessfulGraceBytes),
			formatLaneBytes(result.RecoveryStorage.ProtectedEvidenceBytes),
			formatLaneBytes(result.RecoveryStorage.CleanupQuarantineBytes),
			formatLaneBytes(result.RecoveryStorage.TransportSafetyBytes),
			laneTimePtrOrDash(result.RecoveryStorage.NextSuccessfulQuarantineExpiresAt),
		)
	}
	renderLaneProgress(cmd, "Progress", result.Progress)
	if laneMetricsAny(result.Metrics) {
		fmt.Fprintf(cmd.OutOrStdout(), "Timing: total=%s preflight=%s pending_scan=%s safety_copy=%s bundle_create=%s bundle_verify=%s bundle_archive=%s remote_prepare=%s rsync=%s accept=%s catalog=%s legacy_export=%s cleanup=%s\n",
			formatDurationMS(result.Metrics.TotalDurationMS),
			formatDurationMS(result.Metrics.PreflightDurationMS),
			formatDurationMS(result.Metrics.PendingScanDurationMS),
			formatDurationMS(result.Metrics.SafetyCopyDurationMS),
			formatDurationMS(result.Metrics.BundleCreateDurationMS),
			formatDurationMS(result.Metrics.BundleVerifyDurationMS),
			formatLaneBytes(result.Metrics.BundleArchiveBytes),
			formatDurationMS(result.Metrics.RemotePrepareDurationMS),
			formatDurationMS(result.Metrics.RsyncDurationMS),
			formatDurationMS(result.Metrics.RemoteAcceptDurationMS),
			formatDurationMS(result.Metrics.CatalogDurationMS),
			formatDurationMS(firstNonZeroInt64(result.Metrics.ExportRefreshDurationMS, result.Metrics.ExportRebuildDurationMS)),
			formatDurationMS(result.Metrics.LocalCleanupDurationMS),
		)
	}
	if len(result.RemovedLocalItems) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Removed local Lane items: %d\n", len(result.RemovedLocalItems))
	}
	if result.ErrorMessage != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", result.ErrorMessage)
	}
	if laneSendStatusNeedsRepair(result.Status) && result.BatchID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Next: loom lane repair %s\n", shellQuoteIfNeeded(result.BatchID))
	}
	if len(result.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Commands:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATUS\tCOMMAND")
		for _, command := range result.Commands {
			fmt.Fprintf(writer, "%s\t%s %s\n", command.Status, command.Name, strings.Join(command.Args, " "))
		}
		_ = writer.Flush()
	}
}

func laneSendStatusNeedsRepair(status string) bool {
	switch status {
	case lane.BatchStatusPromotionFailed, lane.BatchStatusAcceptedOnMain, lane.BatchStatusCatalogFailed, lane.BatchStatusSourceCleanupFailed:
		return true
	default:
		return false
	}
}

func renderLaneAcceptResult(cmd *cobra.Command, result lane.AcceptResult) {
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane accepted\n")
	fmt.Fprintf(cmd.OutOrStdout(), "Batch: %s\n", dashIfEmpty(result.BatchID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source node: %s\n", dashIfEmpty(result.SourceNodeKey))
	fmt.Fprintf(cmd.OutOrStdout(), "Canonical custody path: %s\n", dashIfEmpty(result.AcceptedPath))
	fmt.Fprintf(cmd.OutOrStdout(), "Canonical on main: %s\n", dashIfEmpty(result.VisibleStoragePath))
	fmt.Fprintf(cmd.OutOrStdout(), "Cataloged: %d files, %s\n", result.FilesCataloged, formatLaneBytes(result.TotalBytes))
	if laneMetricsAny(result.Metrics) {
		fmt.Fprintf(cmd.OutOrStdout(), "Timing: total=%s scan=%s hash=%s register=%s\n",
			formatDurationMS(result.Metrics.TotalDurationMS),
			formatDurationMS(result.Metrics.AcceptedScanDurationMS),
			formatDurationMS(result.Metrics.AcceptedHashDurationMS),
			formatDurationMS(result.Metrics.AcceptedRegisterDurationMS),
		)
		fmt.Fprintf(cmd.OutOrStdout(), "Operations: hash=%d register=%d\n",
			result.Metrics.AcceptedHashOperations,
			result.Metrics.AcceptedRegisterOperations,
		)
	}
}

func renderLanePublishResult(cmd *cobra.Command, result lane.PublishResult, repairAlias bool) {
	label := "publish"
	if repairAlias {
		label = "repair"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "LOOM Lane %s: %s\n", label, dashIfEmpty(result.Status))
	fmt.Fprintf(cmd.OutOrStdout(), "Batch: %s\n", dashIfEmpty(result.BatchID))
	fmt.Fprintf(cmd.OutOrStdout(), "Source node: %s\n", dashIfEmpty(result.SourceNodeKey))
	if result.VisibleStoragePath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Canonical on main: %s\n", result.VisibleStoragePath)
	}
	if result.RemoteAcceptedPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Remote custody reference: %s\n", result.RemoteAcceptedPath)
	}
	if laneMetricsAny(result.Metrics) {
		fmt.Fprintf(cmd.OutOrStdout(), "Timing: total=%s catalog=%s legacy_export=%s\n",
			formatDurationMS(result.Metrics.TotalDurationMS),
			formatDurationMS(result.Metrics.CatalogDurationMS),
			formatDurationMS(firstNonZeroInt64(result.Metrics.ExportRefreshDurationMS, result.Metrics.ExportRebuildDurationMS)),
		)
	}
	if result.ErrorMessage != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Error: %s\n", result.ErrorMessage)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: %s\n", warning)
	}
	if len(result.Commands) > 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "")
		fmt.Fprintln(cmd.OutOrStdout(), "Commands:")
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(writer, "STATUS\tCOMMAND")
		for _, command := range result.Commands {
			fmt.Fprintf(writer, "%s\t%s %s\n", command.Status, command.Name, strings.Join(command.Args, " "))
		}
		_ = writer.Flush()
	}
	if result.ErrorMessage != "" && result.BatchID != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Next: loom lane repair %s\n", shellQuoteIfNeeded(result.BatchID))
	}
}

func renderLaneDiagnostics(cmd *cobra.Command, title string, diagnostics []lane.Diagnostic) {
	if len(diagnostics) == 0 {
		return
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), title+":")
	for _, diagnostic := range diagnostics {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s %s: %s\n", diagnostic.Severity, diagnostic.Code, diagnostic.Message)
		if diagnostic.Path != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    path: %s\n", diagnostic.Path)
		}
		if diagnostic.Suggestion != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "    suggestion: %s\n", diagnostic.Suggestion)
		}
	}
}

func laneMetricsAny(metrics lane.Metrics) bool {
	return metrics.TotalDurationMS > 0 ||
		metrics.PreflightDurationMS > 0 ||
		metrics.PendingScanDurationMS > 0 ||
		metrics.SafetyCopyDurationMS > 0 ||
		metrics.BundleCreateDurationMS > 0 ||
		metrics.BundleVerifyDurationMS > 0 ||
		metrics.BundleArchiveBytes > 0 ||
		metrics.RemotePrepareDurationMS > 0 ||
		metrics.RsyncDurationMS > 0 ||
		metrics.RsyncRateBytesPerSecond > 0 ||
		metrics.RsyncETASeconds > 0 ||
		metrics.RemoteAcceptDurationMS > 0 ||
		metrics.CatalogDurationMS > 0 ||
		metrics.ExportRefreshDurationMS > 0 ||
		metrics.ExportRebuildDurationMS > 0 ||
		metrics.LocalCleanupDurationMS > 0 ||
		metrics.AcceptedScanDurationMS > 0 ||
		metrics.AcceptedHashDurationMS > 0 ||
		metrics.AcceptedRegisterDurationMS > 0 ||
		metrics.AcceptedHashOperations > 0 ||
		metrics.AcceptedRegisterOperations > 0
}

func renderLaneProgress(cmd *cobra.Command, label string, progress lane.Progress) {
	if !progress.Observed {
		return
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %.1f%% %s/%s speed=%s/s eta=%s\n",
		label,
		progress.Percent,
		formatLaneBytes(progress.BytesTransferred),
		formatLaneBytes(progress.TotalBytes),
		formatLaneBytes(int64(progress.RateBytesPerSecond)),
		formatDurationMS(progress.ETASeconds*1000),
	)
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func formatLaneBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	scaled := float64(value)
	unit := "B"
	for _, candidate := range units {
		scaled = scaled / 1024
		unit = candidate
		if scaled < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", scaled, unit)
}

func formatLaneAge(seconds int64) string {
	if seconds <= 0 {
		return "-"
	}
	return formatDurationMS(seconds * 1000)
}

func laneTimePtrOrDash(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}
