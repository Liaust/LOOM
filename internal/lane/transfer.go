package lane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loom.local/loom/internal/filepolicy"
)

var rsyncProgressLineRE = regexp.MustCompile(`(?m)([0-9][0-9,]*)\s+([0-9]+(?:\.[0-9]+)?)%\s+([0-9.]+)([kKmMgGtTpP]?B)/s\s+([0-9:]+)`)

var rsyncSupportsOption = func(rsyncPath, option string) bool {
	rsyncPath = strings.TrimSpace(rsyncPath)
	option = strings.TrimSpace(option)
	if rsyncPath == "" || option == "" {
		return false
	}
	output, err := execCommandRunner(context.Background(), rsyncPath, "--help")
	if err != nil {
		return false
	}
	return strings.Contains(output.Stdout+"\n"+output.Stderr, option)
}

func Send(ctx context.Context, input SendInput) (SendResult, error) {
	started := time.Now()
	metrics := Metrics{}
	normalized, err := normalizeSendInput(input)
	if err != nil {
		return SendResult{}, err
	}
	now := normalized.now()
	stageStarted := time.Now()
	status := BuildStatus(StatusInput{
		RootPath:       normalized.RootPath,
		LaneRelPath:    normalized.LaneRelPath,
		StateRelPath:   normalized.StateRelPath,
		StatePath:      normalized.StatePath,
		MainHost:       normalized.MainHost,
		MaxItems:       1000,
		Now:            normalized.Now,
		LookupPath:     normalized.LookupPath,
		SSHConfigCheck: normalized.SSHConfigCheck,
		Profile:        normalized.Profile,
	})
	metrics.PreflightDurationMS = durationMSSince(stageStarted)
	if status.State == StateError {
		message := "LOOM Lane status could not compile a transfer plan"
		if len(status.Diagnostics) > 0 && strings.TrimSpace(status.Diagnostics[0].Message) != "" {
			message = status.Diagnostics[0].Message
		}
		return SendResult{}, fmt.Errorf("%s", message)
	}
	plan := status.transferPlan
	plan.Transport, err = SelectBundleTransport(plan, normalized.RequestedTransport)
	if err != nil {
		return SendResult{}, err
	}
	if expected := strings.TrimSpace(normalized.ExpectedPolicyFingerprint); expected != "" && expected != plan.PolicyFingerprint {
		return SendResult{}, fmt.Errorf("Lane policy fingerprint changed from %s to %s; re-run the dry run", expected, plan.PolicyFingerprint)
	}
	if status.State != StatePending {
		metrics.TotalDurationMS = durationMSSince(started)
		return SendResult{
			Status:             "noop",
			DryRun:             normalized.DryRun,
			SourceNodeKey:      normalized.SourceNodeKey,
			SourceBoxID:        normalized.SourceBoxID,
			LanePath:           status.LanePath,
			PendingItems:       status.PendingItems,
			StartedAt:          now,
			Metrics:            metrics,
			Profile:            plan.Profile,
			PolicyVersion:      plan.PolicyVersion,
			PolicyFingerprint:  plan.PolicyFingerprint,
			InventoryHash:      plan.InventoryHash,
			PolicyHashes:       plan.PolicyHashes,
			IgnoredFileCount:   plan.IgnoredFileCount,
			IgnoredBytes:       plan.IgnoredBytes,
			Warnings:           plan.Warnings,
			RequestedTransport: plan.Transport.RequestedMode,
			SelectedTransport:  plan.Transport.SelectedMode,
			TransportReason:    plan.Transport.Reason,
			Transport:          plan.Transport,
		}, nil
	}
	if status.Preflight.Status == PreflightMissingTools {
		return SendResult{}, fmt.Errorf("LOOM Lane preflight failed: missing rsync or ssh")
	}
	metrics.PendingScanDurationMS = durationMSSince(stageStarted)
	items := plan.Items
	if len(items) == 0 {
		metrics.TotalDurationMS = durationMSSince(started)
		return SendResult{
			Status:             "noop",
			DryRun:             normalized.DryRun,
			SourceNodeKey:      normalized.SourceNodeKey,
			LanePath:           status.LanePath,
			StartedAt:          now,
			Metrics:            metrics,
			Profile:            plan.Profile,
			PolicyVersion:      plan.PolicyVersion,
			PolicyFingerprint:  plan.PolicyFingerprint,
			InventoryHash:      plan.InventoryHash,
			PolicyHashes:       plan.PolicyHashes,
			IgnoredFileCount:   plan.IgnoredFileCount,
			IgnoredBytes:       plan.IgnoredBytes,
			Warnings:           plan.Warnings,
			RequestedTransport: plan.Transport.RequestedMode,
			SelectedTransport:  plan.Transport.SelectedMode,
			TransportReason:    plan.Transport.Reason,
			Transport:          plan.Transport,
		}, nil
	}
	batchID := normalized.batchID(now)
	batchStartedAt := now
	var resumedRecord BatchRecord
	if normalized.Resume {
		if plan.Transport.SelectedMode == TransportModeBundleSeed {
			resumedRecord, err = resumableBundleRecord(status, normalized)
		} else {
			resumedRecord, err = resumableFileTreeRecord(status, normalized, plan)
		}
		if err != nil {
			return SendResult{}, err
		}
		batchID = resumedRecord.BatchID
		if resumedRecord.StartedAt != nil {
			batchStartedAt = resumedRecord.StartedAt.UTC()
		}
		if normalized.AllowCrossDevicePromotion && !resumedRecord.AllowCrossDevicePromotion {
			return SendResult{}, fmt.Errorf("Lane batch %s was not originally authorized for cross-filesystem promotion; start a new send to make a new explicit authorization", resumedRecord.BatchID)
		}
		normalized.AllowCrossDevicePromotion = resumedRecord.AllowCrossDevicePromotion
	}
	localCleanupIntent := LocalCleanupIntentQuarantine
	if normalized.KeepLocal {
		localCleanupIntent = LocalCleanupIntentKeep
	}
	if resumedRecord.LocalCleanupIntent != "" {
		localCleanupIntent = resumedRecord.LocalCleanupIntent
	}
	date := batchStartedAt.Format("2006-01-02")
	remoteRoot := normalized.RemoteRoot
	remoteStagingPath := remotePathJoin(remoteRoot, "staging", normalized.SourceNodeKey, batchID)
	remoteAcceptedPath := filepath.ToSlash(filepath.Join("imports", normalized.SourceNodeKey, date, batchID))
	if err := validateRemoteChild(remoteRoot, remoteStagingPath); err != nil {
		return SendResult{}, err
	}
	visibleStoragePath := filepath.ToSlash(filepath.Join("imports", normalized.SourceNodeKey, date, batchID))
	localSafetyPath := filepath.Join(status.StatePath, "sent", batchID)
	transferManifestPath := filepath.Join(status.StatePath, "plans", batchID+".json")
	transferFileListPath := filepath.Join(status.StatePath, "plans", batchID+".files")
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		localSafetyPath = filepath.Join(status.StatePath, "bundles", batchID)
		transferManifestPath = filepath.Join(localSafetyPath, bundleManifestFileName)
	}
	record := BatchRecord{
		SchemaVersion:             BatchSchemaVersion,
		BatchID:                   batchID,
		Status:                    BatchStatusPrepared,
		SourceNodeKey:             normalized.SourceNodeKey,
		SourceBoxID:               normalized.SourceBoxID,
		VisibleStoragePath:        visibleStoragePath,
		RemoteStagingPath:         remoteStagingPath,
		RemoteAcceptedPath:        remoteAcceptedPath,
		LocalSafetyPath:           localSafetyPath,
		FileCount:                 plan.FileCount,
		TotalBytes:                plan.TotalBytes,
		Items:                     plan.Items,
		Metrics:                   metrics,
		StartedAt:                 &batchStartedAt,
		Profile:                   plan.Profile,
		PolicyVersion:             plan.PolicyVersion,
		PolicyFingerprint:         plan.PolicyFingerprint,
		InventoryHash:             plan.InventoryHash,
		PolicyHashes:              plan.PolicyHashes,
		IgnoredFileCount:          plan.IgnoredFileCount,
		IgnoredBytes:              plan.IgnoredBytes,
		TransferManifestPath:      transferManifestPath,
		RequestedTransport:        plan.Transport.RequestedMode,
		SelectedTransport:         plan.Transport.SelectedMode,
		TransportReason:           plan.Transport.Reason,
		LocalCleanupIntent:        localCleanupIntent,
		AllowCrossDevicePromotion: normalized.AllowCrossDevicePromotion,
		LocalSafetyCleanupState:   SafetyArtifactRetainedForRetry,
	}
	if resumedRecord.BatchID != "" {
		carryBundleRecordEvidence(&record, resumedRecord)
	}
	result := SendResult{
		BatchID:                   batchID,
		Status:                    BatchStatusPrepared,
		DryRun:                    normalized.DryRun,
		SourceNodeKey:             normalized.SourceNodeKey,
		SourceBoxID:               normalized.SourceBoxID,
		VisibleStoragePath:        visibleStoragePath,
		LanePath:                  status.LanePath,
		LocalSafetyPath:           localSafetyPath,
		RemoteStagingPath:         remoteStagingPath,
		RemoteAcceptedPath:        remoteAcceptedPath,
		PendingItems:              len(items),
		FileCount:                 plan.FileCount,
		TotalBytes:                plan.TotalBytes,
		StartedAt:                 batchStartedAt,
		Metrics:                   metrics,
		Profile:                   plan.Profile,
		PolicyVersion:             plan.PolicyVersion,
		PolicyFingerprint:         plan.PolicyFingerprint,
		InventoryHash:             plan.InventoryHash,
		PolicyHashes:              plan.PolicyHashes,
		IgnoredFileCount:          plan.IgnoredFileCount,
		IgnoredBytes:              plan.IgnoredBytes,
		Warnings:                  plan.Warnings,
		RequestedTransport:        plan.Transport.RequestedMode,
		SelectedTransport:         plan.Transport.SelectedMode,
		TransportReason:           plan.Transport.Reason,
		Transport:                 plan.Transport,
		LocalCleanupIntent:        localCleanupIntent,
		AllowCrossDevicePromotion: normalized.AllowCrossDevicePromotion,
	}
	if normalized.DryRun {
		result.Status = "dry_run"
		result.Commands = plannedCommands(status.LanePath, normalized, plan.Transport.SelectedMode, remoteStagingPath, remoteAcceptedPath, date, batchID)
		metrics.TotalDurationMS = durationMSSince(started)
		result.Metrics = metrics
		return result, nil
	}
	result.LocalSafetyCleanupState = SafetyArtifactRetainedForRetry
	if plan.Transport.SelectedMode == TransportModeFileTree {
		if err := writeTransferPlanFiles(transferManifestPath, transferFileListPath, plan); err != nil {
			return SendResult{}, err
		}
	}
	if err := writeBatchRecord(status.StatePath, record); err != nil {
		return SendResult{}, err
	}
	writePhase := func(phase string) error {
		record.Status = phase
		record.Metrics = metrics
		record.Progress = result.Progress
		result.Status = phase
		result.Metrics = metrics
		return writeBatchRecord(status.StatePath, record)
	}
	fail := func(err error) (SendResult, error) {
		message := err.Error()
		metrics.TotalDurationMS = durationMSSince(started)
		record.ErrorMessage = message
		record.Metrics = metrics
		failureStatus := laneRemoteFailureStatus(result.Commands)
		result.Status = failureStatus
		result.ErrorMessage = message
		result.Metrics = metrics
		if failureStatus != BatchStatusFailed {
			record.Status = failureStatus
			record.AttentionStatus = AttentionStatusActive
			record.CompletedAt = nil
			_ = writeBatchRecord(status.StatePath, record)
			if laneBatchHasMainCustody(failureStatus) {
				return result, nil
			}
			return result, err
		}
		record.Status = BatchStatusFailed
		_ = writeBatchRecord(status.StatePath, record)
		return result, err
	}

	var bundleArtifact BundleArtifact
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		if err := writePhase(BatchStatusBundling); err != nil {
			return SendResult{}, err
		}
		stageStarted = time.Now()
		bundleArtifact, result.BundleResumed, err = prepareBundleArtifact(status, normalized, plan, batchID, now)
		if err != nil {
			return fail(fmt.Errorf("create or resume local Lane bundle: %w", err))
		}
		if result.BundleResumed {
			metrics.BundleVerifyDurationMS = durationMSSince(stageStarted)
		} else {
			metrics.BundleCreateDurationMS = durationMSSince(stageStarted)
		}
		metrics.BundleArchiveBytes = bundleArtifact.ArchiveBytes
		applyBundleArtifact(&record, &result, bundleArtifact, result.BundleResumed)
		if err := writePhase(BatchStatusBundleReady); err != nil {
			return SendResult{}, err
		}
	} else {
		if err := writePhase(BatchStatusTransferring); err != nil {
			return SendResult{}, err
		}
		stageStarted = time.Now()
		if err := copySafetyPlan(status.LanePath, localSafetyPath, plan); err != nil {
			return fail(fmt.Errorf("create local safety copy: %w", err))
		}
		metrics.SafetyCopyDurationMS = durationMSSince(stageStarted)
	}
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		if err := writePhase(BatchStatusTransferring); err != nil {
			return SendResult{}, err
		}
	}
	stageStarted = time.Now()
	stagingOperation := RemoteStagingPrepare
	if normalized.Resume {
		stagingOperation = RemoteStagingResume
	}
	expectedRuntimeRoot := ""
	if normalized.Resume {
		expectedRuntimeRoot, err = batchRuntimeRoot(resumedRecord, normalized.RemoteRoot)
		if err != nil {
			return fail(fmt.Errorf("validate resumable Lane runtime root: %w", err))
		}
	}
	stagingResult, err := runRemoteStaging(ctx, &result, normalized, stagingOperation, normalized.SourceNodeKey, batchID, expectedRuntimeRoot)
	if err != nil {
		return fail(err)
	}
	receiverCommand, err := remoteReceiverCommand(stagingResult)
	if err != nil {
		return fail(fmt.Errorf("validate main Lane receiver identity: %w", err))
	}
	normalized.remoteReceiverCommand = receiverCommand
	normalized.remoteReceiverUser = stagingResult.ReceiverUser
	normalized.remoteReceiverSwitch = stagingResult.ReceiverSwitchRequired
	remoteRoot = stagingResult.RuntimeRoot
	remoteStagingPath = stagingResult.StagingPath
	remoteAcceptedPath = filepath.ToSlash(filepath.Join("imports", normalized.SourceNodeKey, date, batchID))
	record.RemoteRuntimeRoot = remoteRoot
	record.RemoteStagingPath = remoteStagingPath
	record.RemoteReceiverUser = stagingResult.ReceiverUser
	record.RemoteReceiverSwitchRequired = stagingResult.ReceiverSwitchRequired
	result.RemoteRuntimeRoot = remoteRoot
	result.RemoteStagingPath = remoteStagingPath
	result.RemoteReceiverUser = stagingResult.ReceiverUser
	result.RemoteReceiverSwitchRequired = stagingResult.ReceiverSwitchRequired
	if err := writeBatchRecord(status.StatePath, record); err != nil {
		return fail(fmt.Errorf("record resolved main Lane staging path: %w", err))
	}
	metrics.RemotePrepareDurationMS = durationMSSince(stageStarted)
	verifiedPlan, err := BuildTransferPlan(status.LanePath, normalized.RootPath, normalized.Profile)
	if err != nil {
		return fail(fmt.Errorf("re-plan Lane transfer before rsync: %w", err))
	}
	if verifiedPlan.PolicyFingerprint != plan.PolicyFingerprint || verifiedPlan.InventoryHash != plan.InventoryHash {
		return fail(fmt.Errorf("Lane transfer policy or inventory changed after planning; re-plan before sending"))
	}
	stageStarted = time.Now()
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		if err := runBundleRsync(ctx, &result, normalized, bundleArtifact, remoteStagingPath); err != nil {
			return fail(err)
		}
	} else {
		if err := runRsync(ctx, &result, normalized, status.LanePath, remoteStagingPath, transferFileListPath); err != nil {
			return fail(err)
		}
	}
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		postTransferPlan, err := BuildTransferPlan(status.LanePath, normalized.RootPath, normalized.Profile)
		if err != nil {
			return fail(fmt.Errorf("re-plan Lane source after bundle transfer: %w", err))
		}
		if !samePlannedInventory(plan, postTransferPlan) {
			return fail(fmt.Errorf("Lane source changed during bundle transfer; main acceptance was not attempted"))
		}
	}
	metrics.RsyncDurationMS = durationMSSince(stageStarted)
	if result.Progress.Observed {
		metrics.RsyncRateBytesPerSecond = int64(result.Progress.RateBytesPerSecond)
		metrics.RsyncETASeconds = result.Progress.ETASeconds
		record.Progress = result.Progress
	}
	if err := writePhase(BatchStatusTransferred); err != nil {
		return fail(fmt.Errorf("record transferred lane phase: %w", err))
	}
	if plan.Transport.SelectedMode == TransportModeBundleSeed {
		stageStarted = time.Now()
		acceptanceInput := normalized
		acceptanceInput.RemoteRoot = remoteRoot
		acceptCommand, commandErr := remoteRuntimeCommand(bundleAcceptanceCommand(acceptanceInput, remoteStagingPath, date, batchID), remoteStagingReceiver{
			User: stagingResult.ReceiverUser, SwitchRequired: stagingResult.ReceiverSwitchRequired,
		})
		if commandErr != nil {
			return fail(fmt.Errorf("validate main Lane acceptance identity: %w", commandErr))
		}
		if err := runRemote(ctx, &result, normalized, "verify, promote, and catalog Lane bundle", acceptCommand); err != nil {
			return fail(err)
		}
		metrics.RemoteAcceptDurationMS = durationMSSince(stageStarted)
		metrics.CatalogDurationMS = metrics.RemoteAcceptDurationMS
		if err := writePhase(BatchStatusAcceptedOnMain); err != nil {
			return fail(fmt.Errorf("record accepted Lane bundle phase: %w", err))
		}
		if err := writePhase(BatchStatusCataloged); err != nil {
			return fail(fmt.Errorf("record cataloged Lane bundle phase: %w", err))
		}
	} else {
		stageStarted = time.Now()
		acceptCommand := fmt.Sprintf("loom --json lane accept-received --source-node %s --batch-id %s --accepted-path %s --date %s --source-box-id %s --remote-root %s",
			shellQuote(normalized.SourceNodeKey),
			shellQuote(batchID),
			shellQuote(remoteStagingPath),
			shellQuote(date),
			shellQuote(normalized.SourceBoxID),
			shellQuote(remoteRoot),
		)
		if normalized.AllowCrossDevicePromotion {
			acceptCommand += " --allow-cross-device-promotion"
		}
		acceptCommand, err = remoteRuntimeCommand(acceptCommand, remoteStagingReceiver{
			User: stagingResult.ReceiverUser, SwitchRequired: stagingResult.ReceiverSwitchRequired,
		})
		if err != nil {
			return fail(fmt.Errorf("validate main Lane acceptance identity: %w", err))
		}
		stageStarted = time.Now()
		if err := runRemote(ctx, &result, normalized, "promote and catalog lane batch", acceptCommand); err != nil {
			return fail(err)
		}
		metrics.RemoteAcceptDurationMS = durationMSSince(stageStarted)
		metrics.CatalogDurationMS = durationMSSince(stageStarted)
		if err := writePhase(BatchStatusAcceptedOnMain); err != nil {
			return fail(fmt.Errorf("record accepted Lane phase: %w", err))
		}
		if err := writePhase(BatchStatusCataloged); err != nil {
			return fail(fmt.Errorf("record cataloged Lane phase: %w", err))
		}
	}
	if record.LocalCleanupIntent == LocalCleanupIntentQuarantine {
		stageStarted = time.Now()
		cleanupPlan, cleanupErr := BuildTransferPlan(status.LanePath, normalized.RootPath, normalized.Profile)
		if cleanupErr != nil {
			return withholdLocalCleanup(&result, &record, status.StatePath, &metrics, normalized.now(), stageStarted, started, fmt.Errorf("re-plan Lane source immediately before local cleanup: %w", cleanupErr))
		}
		if !samePlannedInventory(plan, cleanupPlan) {
			return withholdLocalCleanup(&result, &record, status.StatePath, &metrics, normalized.now(), stageStarted, started, fmt.Errorf("Lane source changed after main promoted and cataloged the transfer; local cleanup was withheld"))
		}
		cleanupOutcome, err := cleanupTransferredEntries(normalized.RootPath, normalized.LaneRelPath, status.StatePath, batchID, plan, normalized.BeforeCleanupEntry, normalized.AfterCleanupStage)
		applyLocalCleanupOutcome(&record, &result, cleanupOutcome)
		if err != nil {
			return withholdLocalCleanup(&result, &record, status.StatePath, &metrics, normalized.now(), stageStarted, started, fmt.Errorf("remove visible Lane items safely: %w", err))
		}
		metrics.LocalCleanupDurationMS = durationMSSince(stageStarted)
		if plan.Transport.SelectedMode != TransportModeBundleSeed {
			if err := writePhase(BatchStatusLocalCleanupDone); err != nil {
				return fail(fmt.Errorf("record local cleanup Lane phase: %w", err))
			}
		}
	}
	var transportStagingCleanupErr error
	needsTransportStagingCleanup := plan.Transport.SelectedMode == TransportModeBundleSeed || normalized.AllowCrossDevicePromotion
	if needsTransportStagingCleanup {
		pendingMessage := "main promoted and cataloged the Lane transfer; transport staging cleanup is pending"
		record.ErrorMessage = pendingMessage
		record.AttentionStatus = AttentionStatusActive
		result.ErrorMessage = pendingMessage
		if err := writePhase(BatchStatusSourceCleanupFailed); err != nil {
			return fail(fmt.Errorf("record pending transport-staging cleanup Lane phase: %w", err))
		}
		_, transportStagingCleanupErr = runRemoteStaging(ctx, &result, normalized, RemoteStagingCleanup, normalized.SourceNodeKey, batchID, remoteRoot)
	}
	completedAt := normalized.now()
	metrics.TotalDurationMS = durationMSSince(started)
	record.Metrics = metrics
	record.Progress = result.Progress
	if transportStagingCleanupErr != nil {
		message := "main promoted and cataloged the Lane transfer, but transport staging cleanup needs repair: " + transportStagingCleanupErr.Error()
		record.Status = BatchStatusSourceCleanupFailed
		record.ErrorMessage = message
		record.AttentionStatus = AttentionStatusActive
		record.CompletedAt = nil
		result.Status = BatchStatusSourceCleanupFailed
		result.ErrorMessage = message
		result.CompletedAt = nil
		result.Warnings = append(result.Warnings, message)
		result.Metrics = metrics
		if err := writeBatchRecord(status.StatePath, record); err != nil {
			return SendResult{}, err
		}
		if accounting, err := InspectRecoveryStorageAt(status.StatePath); err == nil {
			result.RecoveryStorage = accounting
		} else {
			result.Warnings = append(result.Warnings, "Lane recovery storage accounting needs repair: "+err.Error())
		}
		return result, nil
	}
	record.CompletedAt = &completedAt
	record.ErrorMessage = ""
	record.AttentionStatus = ""
	record.AttentionNote = ""
	record.AttentionUpdatedAt = nil
	result.ErrorMessage = ""
	if record.LocalCleanupIntent == LocalCleanupIntentQuarantine {
		record.Status = BatchStatusLocalCleanupDone
		result.Status = BatchStatusLocalCleanupDone
	} else {
		record.Status = BatchStatusCataloged
		result.Status = BatchStatusCataloged
	}
	result.CompletedAt = &completedAt
	result.Metrics = metrics
	if record.LocalCleanupQuarantinePath != "" && record.LocalCleanupQuarantineState == CleanupQuarantineRetainedForRecovery {
		expiresAt := completedAt.Add(SuccessfulCleanupQuarantineGrace)
		record.LocalCleanupQuarantineExpiresAt = &expiresAt
		result.LocalCleanupQuarantineExpiresAt = &expiresAt
		if bytes, sizeErr := stateDirectoryBytes(record.LocalCleanupQuarantinePath); sizeErr == nil {
			record.LocalCleanupQuarantineBytes = bytes
			result.LocalCleanupQuarantineBytes = bytes
		} else {
			result.Warnings = append(result.Warnings, "Lane cleanup quarantine byte accounting needs repair: "+sizeErr.Error())
		}
	}
	if err := writeBatchRecord(status.StatePath, record); err != nil {
		return SendResult{}, err
	}
	if normalized.AfterCompletedRecord != nil {
		if err := normalized.AfterCompletedRecord(record); err != nil {
			return fail(fmt.Errorf("after durable completed Lane record: %w", err))
		}
	}
	cleanupSupersededTransferBatches(ctx, &result, normalized, status.StatePath, remoteRoot, batchID, completedAt)
	housekeeping, housekeepingErr := Housekeep(HousekeepingInput{
		RootPath:       normalized.RootPath,
		StateRelPath:   normalized.StateRelPath,
		StatePath:      status.StatePath,
		CurrentBatchID: batchID,
		Now:            func() time.Time { return completedAt },
	})
	if housekeepingErr != nil {
		result.Warnings = append(result.Warnings, "Lane recovery housekeeping needs repair: "+housekeepingErr.Error())
		accounting, accountingErr := InspectRecoveryStorageAt(status.StatePath)
		result.RecoveryStorage = accounting
		if accountingErr != nil {
			result.Warnings = append(result.Warnings, "Lane recovery storage accounting needs repair: "+accountingErr.Error())
		}
	} else {
		result.RecoveryStorage = housekeeping.RecoveryStorage
	}
	if refreshed, refreshErr := readBatchRecord(filepath.Join(status.StatePath, "batches", batchID+".json")); refreshErr == nil {
		applyBatchRecoveryEvidence(&result, refreshed)
	}
	return result, nil
}

func resumableFileTreeRecord(status Status, input SendInput, plan TransferPlan) (BatchRecord, error) {
	latest := status.LastTransfer
	if latest == nil || !fileTreeResumeEligibleStatus(latest.Status) || latest.SelectedTransport != TransportModeFileTree || strings.TrimSpace(latest.BatchID) == "" || safeToken(latest.BatchID, "batch") != latest.BatchID {
		return BatchRecord{}, fmt.Errorf("no interrupted or failed Lane file-tree batch is available to resume; run a new send without --resume")
	}
	record, err := readBatchRecord(filepath.Join(status.StatePath, "batches", latest.BatchID+".json"))
	if err != nil {
		return BatchRecord{}, fmt.Errorf("read resumable Lane file-tree record: %w", err)
	}
	if !fileTreeResumeEligibleStatus(record.Status) || record.SelectedTransport != TransportModeFileTree || record.SourceNodeKey != input.SourceNodeKey || record.SourceBoxID != input.SourceBoxID || record.Profile != plan.Profile || record.PolicyFingerprint != plan.PolicyFingerprint || record.InventoryHash != plan.InventoryHash {
		return BatchRecord{}, fmt.Errorf("latest interrupted or failed Lane file-tree batch does not match the current source and reviewed transfer plan")
	}
	return record, nil
}

func fileTreeResumeEligibleStatus(status string) bool {
	switch status {
	case BatchStatusFailed, BatchStatusTransferring, BatchStatusTransferred, BatchStatusPromotionFailed, BatchStatusCatalogFailed:
		return true
	default:
		return false
	}
}

func applyBatchRecoveryEvidence(result *SendResult, record BatchRecord) {
	if result == nil {
		return
	}
	result.BundleCleanupState = record.BundleCleanupState
	result.LocalSafetyCleanupState = record.LocalSafetyCleanupState
	result.LocalSafetyRemovedAt = record.LocalSafetyRemovedAt
	result.LocalSafetyRemovalReason = record.LocalSafetyRemovalReason
	result.LocalCleanupQuarantinePath = record.LocalCleanupQuarantinePath
	result.LocalCleanupQuarantineState = record.LocalCleanupQuarantineState
	result.LocalCleanupQuarantineBytes = record.LocalCleanupQuarantineBytes
	result.LocalCleanupQuarantineExpiresAt = record.LocalCleanupQuarantineExpiresAt
	result.LocalCleanupQuarantineRemovedAt = record.LocalCleanupQuarantineRemovedAt
	result.LocalCleanupQuarantineRemovalReason = record.LocalCleanupQuarantineRemovalReason
}

func withholdLocalCleanup(result *SendResult, record *BatchRecord, statePath string, metrics *Metrics, completedAt, cleanupStarted, transferStarted time.Time, err error) (SendResult, error) {
	message := err.Error()
	metrics.LocalCleanupDurationMS = durationMSSince(cleanupStarted)
	metrics.TotalDurationMS = durationMSSince(transferStarted)
	record.Status = BatchStatusLocalCleanupWithheld
	record.ErrorMessage = message
	record.AttentionStatus = AttentionStatusActive
	record.CompletedAt = &completedAt
	record.Metrics = *metrics
	result.Status = BatchStatusLocalCleanupWithheld
	result.ErrorMessage = message
	result.CompletedAt = &completedAt
	result.Metrics = *metrics
	result.Warnings = append(result.Warnings, "Main custody is promoted and cataloged; remaining visible content, cleanup quarantine evidence, and the original transport safety artifact were preserved for recovery.")
	if writeErr := writeBatchRecord(statePath, *record); writeErr != nil {
		return *result, fmt.Errorf("record withheld Lane cleanup after %v: %w", err, writeErr)
	}
	return *result, nil
}

func normalizeSendInput(input SendInput) (SendInput, error) {
	input.RootPath = strings.TrimSpace(input.RootPath)
	if input.RootPath == "" {
		return SendInput{}, fmt.Errorf("root path is required")
	}
	absRoot, err := filepath.Abs(input.RootPath)
	if err != nil {
		return SendInput{}, fmt.Errorf("resolve root path: %w", err)
	}
	input.RootPath = filepath.Clean(absRoot)
	input.LaneRelPath = filepath.ToSlash(strings.TrimSpace(input.LaneRelPath))
	if input.LaneRelPath == "" {
		input.LaneRelPath = DefaultLaneRelPath
	}
	input.StateRelPath = filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if input.StateRelPath == "" {
		input.StateRelPath = DefaultStateRelPath
	}
	statePath, err := normalizeLaneStatePath(input.RootPath, input.StateRelPath, input.StatePath)
	if err != nil {
		return SendInput{}, err
	}
	input.StatePath = statePath
	input.SourceNodeKey = safeToken(strings.TrimSpace(input.SourceNodeKey), "workspace")
	if input.SourceNodeKey == "" {
		return SendInput{}, fmt.Errorf("source node key is required")
	}
	input.SourceBoxID = strings.TrimSpace(input.SourceBoxID)
	input.MainHost = strings.TrimSpace(input.MainHost)
	if input.MainHost == "" {
		input.MainHost = DefaultMainHost
	}
	input.RemoteRoot = strings.TrimSpace(input.RemoteRoot)
	if input.RemoteRoot == "" {
		input.RemoteRoot = DefaultRemoteRoot
	}
	remoteRoot, err := normalizeRemoteRoot(input.RemoteRoot)
	if err != nil {
		return SendInput{}, err
	}
	input.RemoteRoot = remoteRoot
	if input.Profile == "" {
		input.Profile = filepolicy.ProfileFaithful
	}
	profile, err := filepolicy.ParseProfile(string(input.Profile))
	if err != nil {
		return SendInput{}, err
	}
	input.Profile = profile
	if input.RequestedTransport == "" {
		input.RequestedTransport = TransportModeAuto
	}
	if input.RequestedTransport != TransportModeAuto && input.RequestedTransport != TransportModeFileTree && input.RequestedTransport != TransportModeBundleSeed {
		return SendInput{}, fmt.Errorf("unsupported Lane transport request %q", input.RequestedTransport)
	}
	return input, nil
}

func normalizeRemoteRoot(remoteRoot string) (string, error) {
	remoteRoot = strings.TrimSpace(strings.ReplaceAll(remoteRoot, "\\", "/"))
	if remoteRoot == "" {
		return "", fmt.Errorf("remote root is required")
	}
	if !strings.HasPrefix(remoteRoot, "/") {
		return "", fmt.Errorf("remote root must be absolute")
	}
	cleaned := path.Clean(remoteRoot)
	if cleaned == "/" || cleaned == "." || cleaned == ".." || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("remote root must not be /, ., or contain ..")
	}
	boundary := path.Clean(remoteRootBoundary)
	if !remotePathWithin(boundary, cleaned) {
		return "", fmt.Errorf("remote root must stay under %s", boundary)
	}
	return cleaned, nil
}

func remotePathJoin(root string, elems ...string) string {
	parts := append([]string{root}, elems...)
	return path.Clean(path.Join(parts...))
}

func validateRemoteChild(root, candidate string) error {
	root = path.Clean(root)
	candidate = path.Clean(candidate)
	if candidate == root || !remotePathWithin(root, candidate) {
		return fmt.Errorf("remote path %s is outside remote root %s", candidate, root)
	}
	return nil
}

func remotePathWithin(root, candidate string) bool {
	root = path.Clean(root)
	candidate = path.Clean(candidate)
	return candidate == root || strings.HasPrefix(candidate, strings.TrimRight(root, "/")+"/")
}

// ManageRemoteStaging performs the only destructive lifecycle operations for a
// received Lane staging batch. Callers provide identities, never a deletion
// path; the main-side CLI derives runtimeRoot from its trusted configuration.
func ManageRemoteStaging(input RemoteStagingInput) (RemoteStagingResult, error) {
	runtimeRoot := filepath.Clean(strings.TrimSpace(input.RuntimeRoot))
	if runtimeRoot == "." || runtimeRoot == string(filepath.Separator) || !filepath.IsAbs(runtimeRoot) {
		return RemoteStagingResult{}, fmt.Errorf("Lane runtime root must be a bounded absolute path")
	}
	expectedRuntimeRootValue := strings.TrimSpace(input.ExpectedRuntimeRoot)
	if input.Operation == RemoteStagingResume || input.Operation == RemoteStagingCleanup {
		if expectedRuntimeRootValue == "" {
			return RemoteStagingResult{}, fmt.Errorf("persisted expected Lane runtime root is required for %s", input.Operation)
		}
	}
	if expectedRuntimeRootValue != "" {
		expectedRuntimeRoot := filepath.Clean(expectedRuntimeRootValue)
		if expectedRuntimeRoot == "." || expectedRuntimeRoot == string(filepath.Separator) || !filepath.IsAbs(expectedRuntimeRoot) {
			return RemoteStagingResult{}, fmt.Errorf("expected Lane runtime root must be a bounded absolute path")
		}
		if expectedRuntimeRoot != expectedRuntimeRootValue {
			return RemoteStagingResult{}, fmt.Errorf("expected Lane runtime root must be clean")
		}
		if expectedRuntimeRoot != runtimeRoot {
			return RemoteStagingResult{}, fmt.Errorf("configured Lane runtime root %s does not match persisted expected root %s", runtimeRoot, expectedRuntimeRoot)
		}
	}
	sourceNodeKey := strings.TrimSpace(input.SourceNodeKey)
	if sourceNodeKey == "" || safeToken(sourceNodeKey, "") != sourceNodeKey {
		return RemoteStagingResult{}, fmt.Errorf("source node key must be a normalized Lane identity")
	}
	batchID := strings.TrimSpace(input.BatchID)
	if batchID == "" || safeToken(batchID, "") != batchID {
		return RemoteStagingResult{}, fmt.Errorf("batch id must be a normalized Lane identity")
	}
	switch input.Operation {
	case RemoteStagingPrepare, RemoteStagingResume, RemoteStagingCleanup:
	default:
		return RemoteStagingResult{}, fmt.Errorf("unsupported Lane staging operation %q", input.Operation)
	}
	result := RemoteStagingResult{
		SourceNodeKey: sourceNodeKey,
		BatchID:       batchID,
		Operation:     input.Operation,
		RuntimeRoot:   runtimeRoot,
		StagingPath:   filepath.Join(runtimeRoot, "staging", sourceNodeKey, batchID),
	}
	runtimeFD, err := unix.Open(runtimeRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return RemoteStagingResult{}, fmt.Errorf("open trusted Lane runtime root without following symlinks: %w", err)
	}
	defer unix.Close(runtimeFD)
	result.ReceiverUser, result.ReceiverSwitchRequired, err = laneRuntimeReceiverIdentity(runtimeFD)
	if err != nil {
		return RemoteStagingResult{}, err
	}
	if expectedReceiverUser := strings.TrimSpace(input.ExpectedReceiverUser); expectedReceiverUser != "" {
		if !validRemoteReceiverUser(expectedReceiverUser) || expectedReceiverUser != input.ExpectedReceiverUser {
			return RemoteStagingResult{}, fmt.Errorf("expected Lane receiver user must be a normalized account identity")
		}
		if expectedReceiverUser != result.ReceiverUser {
			return RemoteStagingResult{}, fmt.Errorf("trusted Lane runtime root owner %s does not match persisted expected receiver %s", result.ReceiverUser, expectedReceiverUser)
		}
	}
	createParents := input.Operation != RemoteStagingCleanup
	stagingFD, missing, err := openLaneStagingDirectoryAt(runtimeFD, "staging", createParents)
	if err != nil {
		return RemoteStagingResult{}, fmt.Errorf("open confined Lane staging root: %w", err)
	}
	if missing {
		result.Status = "absent"
		return result, nil
	}
	defer unix.Close(stagingFD)
	if err := normalizeSharedLaneStagingDirectory(runtimeFD, stagingFD, "staging root"); err != nil {
		return RemoteStagingResult{}, err
	}
	sourceFD, missing, err := openLaneStagingDirectoryAt(stagingFD, sourceNodeKey, createParents)
	if err != nil {
		return RemoteStagingResult{}, fmt.Errorf("open confined Lane source staging root: %w", err)
	}
	if missing {
		result.Status = "absent"
		return result, nil
	}
	defer unix.Close(sourceFD)
	if err := normalizeSharedLaneStagingDirectory(stagingFD, sourceFD, "source staging root"); err != nil {
		return RemoteStagingResult{}, err
	}

	var batchStat unix.Stat_t
	statErr := unix.Fstatat(sourceFD, batchID, &batchStat, unix.AT_SYMLINK_NOFOLLOW)
	if statErr != nil && !errors.Is(statErr, unix.ENOENT) {
		return RemoteStagingResult{}, fmt.Errorf("inspect Lane staging batch: %w", statErr)
	}
	if statErr == nil && batchStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return RemoteStagingResult{}, fmt.Errorf("Lane staging batch %s is not a no-follow directory", batchID)
	}

	if input.Operation == RemoteStagingCleanup {
		if errors.Is(statErr, unix.ENOENT) {
			result.Status = "absent"
			return result, nil
		}
		if err := removeLaneStagingDirectoryAt(sourceFD, batchID); err != nil {
			return RemoteStagingResult{}, fmt.Errorf("remove confined Lane staging batch: %w", err)
		}
		if err := unix.Fstatat(sourceFD, batchID, &batchStat, unix.AT_SYMLINK_NOFOLLOW); err == nil || !errors.Is(err, unix.ENOENT) {
			return RemoteStagingResult{}, fmt.Errorf("Lane staging batch %s remained after cleanup", batchID)
		}
		result.Status = "removed"
		result.Removed = true
		return result, nil
	}

	if input.Operation == RemoteStagingResume && statErr == nil {
		batchFD, err := unix.Openat(sourceFD, batchID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return RemoteStagingResult{}, fmt.Errorf("open confined Lane staging batch: %w", err)
		}
		defer unix.Close(batchFD)
		if err := normalizeSharedLaneStagingDirectory(sourceFD, batchFD, "staging batch"); err != nil {
			return RemoteStagingResult{}, err
		}
		result.Status = "ready"
		return result, nil
	}
	if statErr == nil {
		if err := removeLaneStagingDirectoryAt(sourceFD, batchID); err != nil {
			return RemoteStagingResult{}, fmt.Errorf("reset confined Lane staging batch: %w", err)
		}
	}
	if err := unix.Mkdirat(sourceFD, batchID, 0o700); err != nil {
		return RemoteStagingResult{}, fmt.Errorf("create confined Lane staging batch: %w", err)
	}
	if err := unix.Fstatat(sourceFD, batchID, &batchStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || batchStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return RemoteStagingResult{}, fmt.Errorf("created Lane staging batch is not a no-follow directory")
	}
	batchFD, err := unix.Openat(sourceFD, batchID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return RemoteStagingResult{}, fmt.Errorf("open created Lane staging batch: %w", err)
	}
	defer unix.Close(batchFD)
	if err := normalizeSharedLaneStagingDirectory(sourceFD, batchFD, "staging batch"); err != nil {
		return RemoteStagingResult{}, err
	}
	_ = unix.Fsync(sourceFD)
	result.Status = "ready"
	result.Created = true
	return result, nil
}

func normalizeSharedLaneStagingDirectory(parentFD, directoryFD int, label string) error {
	var parentStat unix.Stat_t
	if err := unix.Fstat(parentFD, &parentStat); err != nil {
		return fmt.Errorf("inspect Lane %s parent: %w", label, err)
	}
	var directoryStat unix.Stat_t
	if err := unix.Fstat(directoryFD, &directoryStat); err != nil {
		return fmt.Errorf("inspect Lane %s: %w", label, err)
	}
	if parentStat.Gid != directoryStat.Gid {
		return fmt.Errorf("Lane %s group does not match its trusted parent", label)
	}
	if directoryStat.Mode&0o7777 == 0o2770 {
		return nil
	}
	if err := unix.Fchmod(directoryFD, 0o2770); err != nil {
		return fmt.Errorf("set shared Lane %s permissions: %w", label, err)
	}
	return nil
}

func laneRuntimeReceiverIdentity(runtimeFD int) (string, bool, error) {
	var runtimeStat unix.Stat_t
	if err := unix.Fstat(runtimeFD, &runtimeStat); err != nil {
		return "", false, fmt.Errorf("inspect trusted Lane runtime root owner: %w", err)
	}
	ownerUID := strconv.FormatUint(uint64(runtimeStat.Uid), 10)
	account, err := user.LookupId(ownerUID)
	if err != nil {
		return "", false, fmt.Errorf("resolve trusted Lane runtime root owner %s: %w", ownerUID, err)
	}
	username := strings.TrimSpace(account.Username)
	if !validRemoteReceiverUser(username) {
		return "", false, fmt.Errorf("trusted Lane runtime root owner has an unsafe account name")
	}
	switchRequired := uint32(os.Geteuid()) != runtimeStat.Uid
	if switchRequired && runtimeStat.Uid == 0 {
		return "", false, fmt.Errorf("refusing to switch the Lane rsync receiver to root")
	}
	return username, switchRequired, nil
}

func openLaneStagingDirectoryAt(parentFD int, name string, create bool) (fd int, missing bool, err error) {
	fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err == nil {
		return fd, false, nil
	}
	if errors.Is(err, unix.ENOENT) && !create {
		return -1, true, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, false, err
	}
	if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, false, err
	}
	fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, false, err
	}
	_ = unix.Fsync(parentFD)
	return fd, false, nil
}

func removeLaneStagingDirectoryAt(parentFD int, name string) error {
	childFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	duplicate, err := unix.Dup(childFD)
	if err != nil {
		unix.Close(childFD)
		return err
	}
	directory := os.NewFile(uintptr(duplicate), name)
	entries, err := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		unix.Close(childFD)
		return err
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(childFD, entry, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			unix.Close(childFD)
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			if err := removeLaneStagingDirectoryAt(childFD, entry); err != nil {
				unix.Close(childFD)
				return err
			}
		} else if err := unix.Unlinkat(childFD, entry, 0); err != nil {
			unix.Close(childFD)
			return err
		}
	}
	_ = unix.Fsync(childFD)
	if err := unix.Close(childFD); err != nil {
		return err
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	_ = unix.Fsync(parentFD)
	return nil
}

func (input SendInput) now() time.Time {
	if input.Now != nil {
		return input.Now().UTC()
	}
	return time.Now().UTC()
}

func (input SendInput) batchID(now time.Time) string {
	if input.NewBatchID != nil {
		return safeToken(input.NewBatchID(now), "batch")
	}
	return "lane_" + now.UTC().Format("20060102T150405Z")
}

func plannedCommands(lanePath string, input SendInput, mode TransportMode, remoteStagingPath, remoteAcceptedPath, date, batchID string) []CommandSummary {
	if mode == TransportModeBundleSeed {
		artifactPath := filepath.Join(input.StatePath, "bundles", batchID)
		artifactAction := "create"
		if input.Resume {
			artifactAction = "verify"
		}
		return []CommandSummary{
			{Name: "bundle", Args: []string{artifactAction, artifactPath}, Status: "planned"},
			{Name: "ssh", Args: []string{input.MainHost, "prepare remote staging"}, Status: "planned"},
			{Name: firstNonEmpty(input.preferredRsync(), "rsync"), Args: bundleRsyncArgs(input, filepath.Join(artifactPath, bundleArchiveFileName), filepath.Join(artifactPath, bundleManifestFileName), remoteStagingPath), Status: "planned"},
			{Name: "ssh", Args: []string{input.MainHost, bundleAcceptanceCommand(input, remoteStagingPath, date, batchID)}, Status: "planned"},
		}
	}
	return []CommandSummary{
		{Name: "ssh", Args: []string{input.MainHost, "prepare remote staging"}, Status: "planned"},
		{Name: firstNonEmpty(input.preferredRsync(), "rsync"), Args: rsyncArgs(input, lanePath, remoteStagingPath), Status: "planned"},
		{Name: "ssh", Args: []string{input.MainHost, "loom --json lane accept-received --source-node " + input.SourceNodeKey + " --batch-id " + batchID + " --accepted-path " + remoteStagingPath + " --date " + date + " --remote-root " + input.RemoteRoot + crossDevicePromotionFlag(input.AllowCrossDevicePromotion)}, Status: "planned"},
	}
}

func (input SendInput) preferredRsync() string {
	lookup := exec.LookPath
	if input.LookupPath != nil {
		lookup = input.LookupPath
	}
	path, _ := preferredRsyncPath(lookup)
	return path
}

func runRemote(ctx context.Context, result *SendResult, input SendInput, label, command string) error {
	output, err := executeCommand(ctx, input.Runner, "ssh", input.MainHost, command)
	appendCommand(result, "ssh", []string{input.MainHost, label}, output, err)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func runRemoteStaging(ctx context.Context, result *SendResult, input SendInput, operation RemoteStagingOperation, sourceNodeKey, batchID, expectedRuntimeRoot string) (RemoteStagingResult, error) {
	label := "prepare remote staging"
	if operation == RemoteStagingResume {
		label = "resume remote staging"
	} else if operation == RemoteStagingCleanup {
		label = laneTransportCleanupLabel(result.SelectedTransport)
	}
	remote, output, err := invokeRemoteStaging(ctx, input.Runner, input.MainHost, operation, sourceNodeKey, batchID, expectedRuntimeRoot, remoteStagingReceiver{
		User: input.remoteReceiverUser, SwitchRequired: input.remoteReceiverSwitch,
	})
	appendCommand(result, "ssh", []string{input.MainHost, label}, output, err)
	if err != nil {
		return RemoteStagingResult{}, fmt.Errorf("%s: %w", label, err)
	}
	return remote, nil
}

type remoteStagingReceiver struct {
	User           string
	SwitchRequired bool
}

func invokeRemoteStaging(ctx context.Context, runner CommandRunner, mainHost string, operation RemoteStagingOperation, sourceNodeKey, batchID, expectedRuntimeRoot string, receivers ...remoteStagingReceiver) (RemoteStagingResult, commandOutput, error) {
	var receiver remoteStagingReceiver
	if len(receivers) > 1 {
		return RemoteStagingResult{}, commandOutput{}, fmt.Errorf("multiple Lane staging receiver identities are not permitted")
	}
	if len(receivers) == 1 {
		receiver = receivers[0]
	}
	command, err := remoteStagingInvocationCommand(operation, sourceNodeKey, batchID, expectedRuntimeRoot, receiver)
	if err != nil {
		return RemoteStagingResult{}, commandOutput{}, err
	}
	output, err := executeCommand(ctx, runner, "ssh", mainHost, command)
	if err != nil {
		return RemoteStagingResult{}, output, err
	}
	if strings.TrimSpace(output.Stdout) == "" {
		return RemoteStagingResult{}, output, fmt.Errorf("main returned an empty Lane staging response")
	}
	if output.StdoutTruncated || output.StdoutBytes > int64(DefaultCommandOutputLimit) {
		return RemoteStagingResult{}, output, fmt.Errorf("main Lane staging response exceeds %d bytes", DefaultCommandOutputLimit)
	}
	var remote RemoteStagingResult
	if err := json.Unmarshal([]byte(output.Stdout), &remote); err != nil {
		return RemoteStagingResult{}, output, fmt.Errorf("decode main Lane staging response: %w", err)
	}
	root, err := normalizeTrustedLaneRuntimeRoot(remote.RuntimeRoot)
	if err != nil {
		return RemoteStagingResult{}, output, fmt.Errorf("validate main Lane staging root: %w", err)
	}
	if expectedRuntimeRoot != "" {
		expectedRoot, err := normalizeTrustedLaneRuntimeRoot(expectedRuntimeRoot)
		if err != nil {
			return RemoteStagingResult{}, output, fmt.Errorf("validate expected Lane staging root: %w", err)
		}
		if root != expectedRoot {
			return RemoteStagingResult{}, output, fmt.Errorf("main Lane staging root %s does not match persisted expected root %s", root, expectedRoot)
		}
	}
	expectedPath := remotePathJoin(root, "staging", sourceNodeKey, batchID)
	if remote.SourceNodeKey != sourceNodeKey || remote.BatchID != batchID || remote.Operation != operation || remote.StagingPath != expectedPath {
		return RemoteStagingResult{}, output, fmt.Errorf("main returned mismatched Lane staging identity")
	}
	switch operation {
	case RemoteStagingPrepare, RemoteStagingResume:
		if remote.Status != "ready" {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned non-ready Lane staging status %q", remote.Status)
		}
		if remote.Removed {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned ready Lane staging status with contradictory removal evidence")
		}
		if _, err := remoteReceiverCommand(remote); err != nil {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned invalid Lane receiver identity: %w", err)
		}
	case RemoteStagingCleanup:
		if remote.Status != "removed" && remote.Status != "absent" {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned non-terminal Lane staging cleanup status %q", remote.Status)
		}
		if remote.Status == "removed" && !remote.Removed {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned removed Lane staging status without removal evidence")
		}
		if remote.Status == "absent" && remote.Removed {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned absent Lane staging status with contradictory removal evidence")
		}
		if remote.Created {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned Lane staging cleanup status with contradictory creation evidence")
		}
		if receiver.SwitchRequired && (remote.ReceiverUser != receiver.User || remote.ReceiverSwitchRequired) {
			return RemoteStagingResult{}, output, fmt.Errorf("main returned mismatched Lane cleanup receiver identity")
		}
	}
	remote.RuntimeRoot = root
	remote.StagingPath = expectedPath
	return remote, output, nil
}

var remoteReceiverUserRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*[$]?$`)

func validRemoteReceiverUser(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && remoteReceiverUserRE.MatchString(value)
}

func remoteReceiverCommand(remote RemoteStagingResult) (string, error) {
	if !validRemoteReceiverUser(remote.ReceiverUser) {
		return "", fmt.Errorf("receiver user is missing or unsafe")
	}
	if !remote.ReceiverSwitchRequired {
		return "", nil
	}
	if remote.ReceiverUser == "root" {
		return "", fmt.Errorf("receiver user root is not permitted for an identity switch")
	}
	return "sudo -n -u " + remote.ReceiverUser + " -- rsync", nil
}

func remoteRuntimeCommand(command string, receiver remoteStagingReceiver) (string, error) {
	if !receiver.SwitchRequired {
		return command, nil
	}
	if !validRemoteReceiverUser(receiver.User) || receiver.User == "root" {
		return "", fmt.Errorf("runtime receiver user is missing or unsafe")
	}
	return "sudo -n -u " + receiver.User + " -- " + command, nil
}

func remoteStagingInvocationCommand(operation RemoteStagingOperation, sourceNodeKey, batchID, expectedRuntimeRoot string, receiver remoteStagingReceiver) (string, error) {
	command := remoteStagingCommand(operation, sourceNodeKey, batchID, expectedRuntimeRoot)
	if operation != RemoteStagingCleanup || !receiver.SwitchRequired {
		return command, nil
	}
	if !validRemoteReceiverUser(receiver.User) || receiver.User == "root" {
		return "", fmt.Errorf("persisted Lane cleanup receiver user is missing or unsafe")
	}
	command += " --expected-receiver-user " + receiver.User
	return "sudo -n -u " + receiver.User + " -- " + command, nil
}

func batchRuntimeRoot(record BatchRecord, fallback string) (string, error) {
	root := strings.TrimSpace(record.RemoteRuntimeRoot)
	if root == "" {
		root = strings.TrimSpace(fallback)
	}
	root, err := normalizeTrustedLaneRuntimeRoot(root)
	if err != nil {
		return "", err
	}
	expectedPath := remotePathJoin(root, "staging", record.SourceNodeKey, record.BatchID)
	if record.RemoteStagingPath != expectedPath {
		return "", fmt.Errorf("persisted staging path does not match runtime root and batch identity")
	}
	return root, nil
}

func normalizeTrustedLaneRuntimeRoot(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || !strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("Lane runtime root must be absolute")
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "/" || cleaned == "." || cleaned == ".." {
		return "", fmt.Errorf("Lane runtime root must be a clean bounded path")
	}
	return cleaned, nil
}

func laneRemoteFailureStatus(commands []CommandSummary) string {
	if len(commands) == 0 {
		return BatchStatusFailed
	}
	last := commands[len(commands)-1]
	evidence := last.Stdout + "\n" + last.Stderr
	switch {
	case strings.Contains(evidence, "storage.lane_catalog_failed"):
		return BatchStatusCatalogFailed
	case strings.Contains(evidence, "storage.lane_source_cleanup_failed"):
		return BatchStatusSourceCleanupFailed
	case strings.Contains(evidence, "storage.lane_promotion_failed"):
		return BatchStatusPromotionFailed
	case strings.Contains(evidence, "storage.lane_obsolete_accepted_migration_failed"):
		return BatchStatusObsoleteAcceptedMigrationRequired
	default:
		return BatchStatusFailed
	}
}

func crossDevicePromotionFlag(enabled bool) string {
	if enabled {
		return " --allow-cross-device-promotion"
	}
	return ""
}

func runRsync(ctx context.Context, result *SendResult, input SendInput, lanePath, remoteStagingPath, fileListPath string) error {
	rsyncPath := input.preferredRsync()
	if rsyncPath == "" {
		rsyncPath = "rsync"
	}
	args := rsyncArgsForManifest(input, lanePath, remoteStagingPath, fileListPath)
	output, err := executeCommand(ctx, input.Runner, rsyncPath, args...)
	appendCommand(result, rsyncPath, args, output, err)
	if progress := parseRsyncProgress(output.Stdout+"\n"+output.Stderr, result.TotalBytes, time.Now().UTC()); progress.Observed {
		result.Progress = progress
	}
	if err != nil {
		return fmt.Errorf("rsync lane batch: %w", err)
	}
	return nil
}

func rsyncArgs(input SendInput, lanePath, remoteStagingPath string) []string {
	return rsyncArgsForManifest(input, lanePath, remoteStagingPath, "<compiled-transfer-plan>")
}

func rsyncArgsForManifest(input SendInput, lanePath, remoteStagingPath, fileListPath string) []string {
	args := []string{
		"-a",
		"--no-recursive",
		"--partial",
		"--progress",
		"--stats",
		"--relative",
		"--from0",
		"--files-from=" + fileListPath,
		lanePath + string(os.PathSeparator),
		input.MainHost + ":" + remoteStagingPath + "/",
	}
	if input.remoteReceiverCommand != "" {
		args = append([]string{"--rsync-path=" + input.remoteReceiverCommand}, args...)
	}
	if !input.Resume {
		args = append([]string{"--partial-dir=.loom-partial"}, args...)
	}
	if input.Resume {
		args = append([]string{rsyncResumeArg(input)}, args...)
	}
	return args
}

func rsyncResumeArg(input SendInput) string {
	if rsyncSupportsOption(input.preferredRsync(), "--append-verify") {
		return "--append-verify"
	}
	return "--append"
}

func appendCommand(result *SendResult, name string, args []string, output commandOutput, err error) {
	status := "ok"
	if err != nil {
		status = "failed"
	}
	result.Commands = append(result.Commands, CommandSummary{
		Name:            name,
		Args:            append([]string{}, args...),
		Status:          status,
		Stdout:          output.Stdout,
		Stderr:          output.Stderr,
		StdoutBytes:     output.StdoutBytes,
		StderrBytes:     output.StderrBytes,
		StdoutTruncated: output.StdoutTruncated,
		StderrTruncated: output.StderrTruncated,
		OutputTruncated: output.StdoutTruncated || output.StderrTruncated,
	})
}

type commandOutput struct {
	Stdout          string
	Stderr          string
	StdoutBytes     int64
	StderrBytes     int64
	StdoutTruncated bool
	StderrTruncated bool
}

func executeCommand(ctx context.Context, runner CommandRunner, name string, args ...string) (commandOutput, error) {
	if runner == nil {
		return execCommandRunner(ctx, name, args...)
	}
	stdout, stderr, err := runner(ctx, name, args...)
	return commandOutputFromStrings(stdout, stderr, DefaultCommandOutputLimit), err
}

func commandOutputFromStrings(stdout, stderr string, limit int) commandOutput {
	stdoutText, stdoutTruncated, stdoutBytes := boundedCommandOutput(stdout, limit)
	stderrText, stderrTruncated, stderrBytes := boundedCommandOutput(stderr, limit)
	return commandOutput{
		Stdout:          stdoutText,
		Stderr:          stderrText,
		StdoutBytes:     stdoutBytes,
		StderrBytes:     stderrBytes,
		StdoutTruncated: stdoutTruncated,
		StderrTruncated: stderrTruncated,
	}
}

func boundedCommandOutput(value string, limit int) (string, bool, int64) {
	capture := newBoundedStreamCapture(limit)
	_, _ = capture.Write([]byte(value))
	return capture.Result()
}

func parseRsyncProgress(output string, totalBytes int64, updatedAt time.Time) Progress {
	matches := rsyncProgressLineRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return Progress{}
	}
	match := matches[len(matches)-1]
	bytesTransferred, _ := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
	percent, _ := strconv.ParseFloat(match[2], 64)
	rateValue, _ := strconv.ParseFloat(match[3], 64)
	rate := rateValue * rsyncRateUnitMultiplier(match[4])
	eta := parseRsyncETASeconds(match[5])
	if totalBytes <= 0 {
		totalBytes = bytesTransferred
	}
	return Progress{
		Observed:           true,
		Percent:            percent,
		BytesTransferred:   bytesTransferred,
		TotalBytes:         totalBytes,
		RateBytesPerSecond: rate,
		ETASeconds:         eta,
		Raw:                strings.TrimSpace(match[0]),
		UpdatedAt:          &updatedAt,
	}
}

func rsyncRateUnitMultiplier(unit string) float64 {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "KB":
		return 1024
	case "MB":
		return 1024 * 1024
	case "GB":
		return 1024 * 1024 * 1024
	case "TB":
		return 1024 * 1024 * 1024 * 1024
	case "PB":
		return 1024 * 1024 * 1024 * 1024 * 1024
	default:
		return 1
	}
}

func parseRsyncETASeconds(value string) int64 {
	parts := strings.Split(strings.TrimSpace(value), ":")
	var seconds int64
	for _, part := range parts {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return 0
		}
		seconds = seconds*60 + n
	}
	return seconds
}

func execCommandRunner(ctx context.Context, name string, args ...string) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout := newBoundedStreamCapture(DefaultCommandOutputLimit)
	stderr := newBoundedStreamCapture(DefaultCommandOutputLimit)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	stdoutText, stdoutTruncated, stdoutBytes := stdout.Result()
	stderrText, stderrTruncated, stderrBytes := stderr.Result()
	return commandOutput{
		Stdout:          stdoutText,
		Stderr:          stderrText,
		StdoutBytes:     stdoutBytes,
		StderrBytes:     stderrBytes,
		StdoutTruncated: stdoutTruncated,
		StderrTruncated: stderrTruncated,
	}, err
}

type boundedStreamCapture struct {
	limit     int
	head      []byte
	tail      []byte
	tailStart int
	tailLen   int
	total     int64
}

func newBoundedStreamCapture(limit int) *boundedStreamCapture {
	if limit <= 0 {
		limit = DefaultCommandOutputLimit
	}
	headCapacity := limit / 3
	if headCapacity == 0 && limit > 1 {
		headCapacity = 1
	}
	return &boundedStreamCapture{
		limit: limit,
		head:  make([]byte, 0, headCapacity),
		tail:  make([]byte, limit-headCapacity),
	}
}

func (capture *boundedStreamCapture) Write(value []byte) (int, error) {
	written := len(value)
	capture.total += int64(written)
	headCapacity := cap(capture.head)
	if len(capture.head) < headCapacity {
		count := headCapacity - len(capture.head)
		if count > len(value) {
			count = len(value)
		}
		capture.head = append(capture.head, value[:count]...)
		value = value[count:]
	}
	capture.writeTail(value)
	return written, nil
}

func (capture *boundedStreamCapture) writeTail(value []byte) {
	capacity := len(capture.tail)
	if capacity == 0 || len(value) == 0 {
		return
	}
	if len(value) >= capacity {
		copy(capture.tail, value[len(value)-capacity:])
		capture.tailStart = 0
		capture.tailLen = capacity
		return
	}
	for len(value) > 0 {
		writeAt := (capture.tailStart + capture.tailLen) % capacity
		count := capacity - writeAt
		if count > len(value) {
			count = len(value)
		}
		copy(capture.tail[writeAt:writeAt+count], value[:count])
		value = value[count:]
		if capture.tailLen < capacity {
			capture.tailLen += count
			if capture.tailLen > capacity {
				capture.tailLen = capacity
			}
		} else {
			capture.tailStart = (capture.tailStart + count) % capacity
		}
	}
}

func (capture *boundedStreamCapture) Result() (string, bool, int64) {
	tail := capture.tailBytes()
	truncated := capture.total > int64(capture.limit)
	retained := make([]byte, 0, len(capture.head)+len(tail)+80)
	retained = append(retained, capture.head...)
	if truncated {
		retained = append(retained, fmt.Sprintf("\n... truncated; %d bytes total ...\n", capture.total)...)
	}
	retained = append(retained, tail...)
	return string(retained), truncated, capture.total
}

func (capture *boundedStreamCapture) tailBytes() []byte {
	if capture.tailLen == 0 {
		return nil
	}
	if capture.tailStart+capture.tailLen <= len(capture.tail) {
		return append([]byte(nil), capture.tail[capture.tailStart:capture.tailStart+capture.tailLen]...)
	}
	first := capture.tail[capture.tailStart:]
	secondLen := capture.tailLen - len(first)
	result := make([]byte, 0, capture.tailLen)
	result = append(result, first...)
	result = append(result, capture.tail[:secondLen]...)
	return result
}

func copySafetyPlan(lanePath, safetyPath string, plan TransferPlan) error {
	if err := os.RemoveAll(safetyPath); err != nil {
		return err
	}
	for _, entry := range plan.Entries {
		src := filepath.Join(lanePath, filepath.FromSlash(entry.RelativePath))
		dst := filepath.Join(safetyPath, filepath.FromSlash(entry.RelativePath))
		if entry.Kind == "directory" {
			if err := os.MkdirAll(dst, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			continue
		}
		if err := copyRegularPath(src, dst, os.FileMode(entry.Mode)); err != nil {
			return err
		}
	}
	return nil
}

func copyRegularPath(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if cloned, err := cloneRegularFile(src, dst, mode.Perm()); cloned || err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func writeBatchRecord(statePath string, record BatchRecord) error {
	if safeToken(record.BatchID, "") == "" || safeToken(record.BatchID, "") != record.BatchID {
		return fmt.Errorf("unsafe Lane batch identity %q", record.BatchID)
	}
	batchDir := filepath.Join(statePath, "batches")
	if err := os.MkdirAll(batchDir, 0o755); err != nil {
		return err
	}
	payload, err := marshalLaneBatch(record)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(batchDir, "."+record.BatchID+".*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(batchDir, record.BatchID+".json")); err != nil {
		return err
	}
	return syncDirectory(batchDir)
}

func marshalLaneBatch(record BatchRecord) ([]byte, error) {
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	return payload, nil
}

func cleanupSupersededTransferBatches(ctx context.Context, result *SendResult, input SendInput, statePath, remoteRoot, currentBatchID string, completedAt time.Time) {
	batchDir := filepath.Join(statePath, "batches")
	entries, err := os.ReadDir(batchDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		pathValue := filepath.Join(batchDir, entry.Name())
		record, err := readBatchRecord(pathValue)
		if err != nil {
			continue
		}
		if record.BatchID == "" || record.BatchID == currentBatchID || record.SourceNodeKey != input.SourceNodeKey {
			continue
		}
		if record.Status != BatchStatusPending && record.Status != BatchStatusBundling && record.Status != BatchStatusBundleReady && record.Status != BatchStatusTransferring && record.Status != BatchStatusTransferred && record.Status != BatchStatusFailed && record.Status != BatchStatusPromotionFailed {
			continue
		}
		stagingPath := strings.TrimSpace(record.RemoteStagingPath)
		recordRuntimeRoot, err := batchRuntimeRoot(record, remoteRoot)
		if err != nil {
			continue
		}
		expectedStagingPath := remotePathJoin(recordRuntimeRoot, "staging", record.SourceNodeKey, record.BatchID)
		if stagingPath == "" || stagingPath != expectedStagingPath {
			continue
		}
		cleanupInput := input
		cleanupInput.remoteReceiverUser = record.RemoteReceiverUser
		cleanupInput.remoteReceiverSwitch = record.RemoteReceiverSwitchRequired
		if _, err := runRemoteStaging(ctx, result, cleanupInput, RemoteStagingCleanup, record.SourceNodeKey, record.BatchID, recordRuntimeRoot); err != nil {
			continue
		}
		record.Status = BatchStatusSuperseded
		record.CompletedAt = &completedAt
		record.ErrorMessage = "superseded by " + currentBatchID
		_ = writeBatchRecord(statePath, record)
	}
}

func readBatchRecord(pathValue string) (BatchRecord, error) {
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		return BatchRecord{}, err
	}
	var record BatchRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return BatchRecord{}, err
	}
	return record, nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '/' || r == '_' || r == '-' || r == '.' || r == ':' || r == '=')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func durationMSSince(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	ms := time.Since(start).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}
