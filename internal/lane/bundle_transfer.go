package lane

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func prepareBundleArtifact(status Status, input SendInput, plan TransferPlan, batchID string, createdAt time.Time) (BundleArtifact, bool, error) {
	artifactRoot := filepath.Join(status.StatePath, "bundles")
	artifactPath := filepath.Join(artifactRoot, batchID)
	archivePath := filepath.Join(artifactPath, bundleArchiveFileName)
	manifestPath := filepath.Join(artifactPath, bundleManifestFileName)
	if input.Resume {
		if info, err := os.Lstat(artifactPath); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return BundleArtifact{}, false, fmt.Errorf("resumable Lane bundle path is not a no-follow artifact directory")
			}
			manifest, err := VerifyBundle(manifestPath, archivePath)
			if err != nil {
				return BundleArtifact{}, false, fmt.Errorf("verify resumable Lane bundle: %w", err)
			}
			if manifest.BatchID != batchID || manifest.SourceNodeKey != input.SourceNodeKey || manifest.SourceBoxID != input.SourceBoxID || !samePlannedInventory(plan, manifest.Plan) {
				return BundleArtifact{}, false, fmt.Errorf("resumable Lane bundle does not match the current batch and canonical plan")
			}
			manifestChecksum, _, err := fileSHA256(manifestPath)
			if err != nil {
				return BundleArtifact{}, false, err
			}
			part := manifest.ArchiveParts[0]
			return BundleArtifact{
				BatchID:        batchID,
				ArtifactPath:   artifactPath,
				ArchivePath:    archivePath,
				ManifestPath:   manifestPath,
				ArchiveBytes:   part.SizeBytes,
				ArchiveSHA256:  part.SHA256,
				ManifestSHA256: "sha256:" + manifestChecksum,
				CreatedAt:      manifest.CreatedAt,
				CleanupState:   manifest.CleanupState,
			}, true, nil
		} else if !os.IsNotExist(err) {
			return BundleArtifact{}, false, err
		}
		return BundleArtifact{}, false, fmt.Errorf("resumable Lane bundle artifact is missing for batch %s", batchID)
	}
	artifact, err := CreateBundle(CreateBundleInput{
		LanePath:       status.LanePath,
		PolicyRoot:     input.RootPath,
		ArtifactRoot:   artifactRoot,
		BatchID:        batchID,
		SourceNodeKey:  input.SourceNodeKey,
		SourceBoxID:    input.SourceBoxID,
		Plan:           plan,
		CreatedAt:      createdAt,
		AvailableBytes: input.AvailableBytes,
		BeforeEntry:    input.BeforeBundleEntry,
	})
	return artifact, false, err
}

func resumableBundleRecord(status Status, input SendInput) (BatchRecord, error) {
	latest := status.LastTransfer
	if latest == nil || !bundleResumeEligibleStatus(latest.Status) || latest.SelectedTransport != TransportModeBundleSeed || strings.TrimSpace(latest.BatchID) == "" || safeToken(latest.BatchID, "batch") != latest.BatchID {
		return BatchRecord{}, fmt.Errorf("no interrupted or failed Lane bundle is available to resume; run a new send without --resume")
	}
	record, err := readBatchRecord(filepath.Join(status.StatePath, "batches", latest.BatchID+".json"))
	if err != nil {
		return BatchRecord{}, fmt.Errorf("read resumable Lane bundle record: %w", err)
	}
	expectedArtifact := filepath.Join(status.StatePath, "bundles", record.BatchID)
	if !bundleResumeEligibleStatus(record.Status) || record.SelectedTransport != TransportModeBundleSeed || record.SourceNodeKey != input.SourceNodeKey || record.SourceBoxID != input.SourceBoxID || filepath.Clean(record.BundleArtifactPath) != filepath.Clean(expectedArtifact) {
		return BatchRecord{}, fmt.Errorf("latest interrupted or failed Lane bundle does not match the current source and retained artifact boundary")
	}
	return record, nil
}

func bundleResumeEligibleStatus(status string) bool {
	switch status {
	case BatchStatusFailed, BatchStatusBundling, BatchStatusBundleReady, BatchStatusTransferring, BatchStatusTransferred, BatchStatusPromotionFailed:
		return true
	default:
		return false
	}
}

func carryBundleRecordEvidence(target *BatchRecord, previous BatchRecord) {
	target.LocalSafetyPath = previous.LocalSafetyPath
	target.TransferManifestPath = previous.TransferManifestPath
	target.BundleArtifactPath = previous.BundleArtifactPath
	target.BundleArchivePath = previous.BundleArchivePath
	target.BundleManifestPath = previous.BundleManifestPath
	target.BundleArchiveSHA256 = previous.BundleArchiveSHA256
	target.BundleManifestSHA256 = previous.BundleManifestSHA256
	target.BundleArchiveBytes = previous.BundleArchiveBytes
	target.BundleCleanupState = previous.BundleCleanupState
	target.LocalSafetyCleanupState = previous.LocalSafetyCleanupState
	target.LocalSafetyRemovedAt = previous.LocalSafetyRemovedAt
	target.LocalSafetyRemovalReason = previous.LocalSafetyRemovalReason
	target.BundleResumed = true
}

func applyBundleArtifact(record *BatchRecord, result *SendResult, artifact BundleArtifact, resumed bool) {
	if record != nil {
		record.LocalSafetyPath = artifact.ArtifactPath
		record.TransferManifestPath = artifact.ManifestPath
		record.BundleArtifactPath = artifact.ArtifactPath
		record.BundleArchivePath = artifact.ArchivePath
		record.BundleManifestPath = artifact.ManifestPath
		record.BundleArchiveSHA256 = artifact.ArchiveSHA256
		record.BundleManifestSHA256 = artifact.ManifestSHA256
		record.BundleArchiveBytes = artifact.ArchiveBytes
		record.BundleCleanupState = artifact.CleanupState
		record.LocalSafetyCleanupState = SafetyArtifactRetainedForRetry
		record.BundleResumed = resumed
	}
	if result != nil {
		result.LocalSafetyPath = artifact.ArtifactPath
		result.BundleArtifactPath = artifact.ArtifactPath
		result.BundleArchivePath = artifact.ArchivePath
		result.BundleManifestPath = artifact.ManifestPath
		result.BundleArchiveSHA256 = artifact.ArchiveSHA256
		result.BundleManifestSHA256 = artifact.ManifestSHA256
		result.BundleArchiveBytes = artifact.ArchiveBytes
		result.BundleCleanupState = artifact.CleanupState
		result.LocalSafetyCleanupState = SafetyArtifactRetainedForRetry
		result.BundleResumed = resumed
	}
}

func runBundleRsync(ctx context.Context, result *SendResult, input SendInput, artifact BundleArtifact, remoteStagingPath string) error {
	rsyncPath := input.preferredRsync()
	if rsyncPath == "" {
		rsyncPath = "rsync"
	}
	args := bundleRsyncArgs(input, artifact.ArchivePath, artifact.ManifestPath, remoteStagingPath)
	output, err := executeCommand(ctx, input.Runner, rsyncPath, args...)
	appendCommand(result, rsyncPath, args, output, err)
	if progress := parseRsyncProgress(output.Stdout+"\n"+output.Stderr, artifact.ArchiveBytes, time.Now().UTC()); progress.Observed {
		result.Progress = progress
	}
	if err != nil {
		return fmt.Errorf("rsync Lane bundle: %w", err)
	}
	return nil
}

func bundleRsyncArgs(input SendInput, archivePath, manifestPath, remoteStagingPath string) []string {
	args := []string{
		"-a",
		"--partial",
		"--progress",
		"--stats",
		archivePath,
		manifestPath,
		input.MainHost + ":" + remoteStagingPath + "/",
	}
	if input.remoteReceiverCommand != "" {
		args = append([]string{"--rsync-path=" + input.remoteReceiverCommand}, args...)
	}
	if input.Resume {
		args = append([]string{rsyncResumeArg(input)}, args...)
	} else {
		args = append([]string{"--partial-dir=.loom-partial"}, args...)
	}
	return args
}

func bundleAcceptanceCommand(input SendInput, remoteStagingPath, date, batchID string) string {
	return fmt.Sprintf("loom --json lane accept-bundle --source-node %s --batch-id %s --accepted-path %s --date %s --source-box-id %s --remote-root %s --manifest-path %s --archive-path %s%s",
		shellQuote(input.SourceNodeKey),
		shellQuote(batchID),
		shellQuote(remotePathJoin(remoteStagingPath, "tree")),
		shellQuote(date),
		shellQuote(input.SourceBoxID),
		shellQuote(input.RemoteRoot),
		shellQuote(remotePathJoin(remoteStagingPath, bundleManifestFileName)),
		shellQuote(remotePathJoin(remoteStagingPath, bundleArchiveFileName)),
		crossDevicePromotionFlag(input.AllowCrossDevicePromotion),
	)
}

func remoteStagingCommand(operation RemoteStagingOperation, sourceNodeKey, batchID string, expectedRuntimeRoot ...string) string {
	command := fmt.Sprintf("loom --json lane staging --operation %s --source-node %s --batch-id %s",
		shellQuote(string(operation)),
		shellQuote(sourceNodeKey),
		shellQuote(batchID),
	)
	if len(expectedRuntimeRoot) > 0 && strings.TrimSpace(expectedRuntimeRoot[0]) != "" {
		command += " --expected-runtime-root " + shellQuote(expectedRuntimeRoot[0])
	}
	return command
}
