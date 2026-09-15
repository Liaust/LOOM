package lane

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func Publish(ctx context.Context, input PublishInput) (PublishResult, error) {
	started := time.Now()
	normalized, err := normalizePublishInput(input)
	if err != nil {
		return PublishResult{}, err
	}
	now := normalized.now()
	recordPath := filepath.Join(normalized.statePath(), "batches", normalized.BatchID+".json")
	record, err := readBatchRecord(recordPath)
	if err != nil {
		return PublishResult{}, fmt.Errorf("read Lane batch %s: %w", normalized.BatchID, err)
	}
	if strings.TrimSpace(record.BatchID) == "" || record.BatchID != normalized.BatchID || safeToken(record.BatchID, "") != record.BatchID {
		return PublishResult{}, fmt.Errorf("Lane batch record identity does not match requested batch %s", normalized.BatchID)
	}
	if strings.TrimSpace(record.RemoteStagingPath) == "" {
		return PublishResult{}, fmt.Errorf("Lane batch %s has no staging path; re-run lane send with --resume", normalized.BatchID)
	}
	if !laneBatchHasMainCustody(record.Status) && record.Status != BatchStatusPromotionFailed {
		return PublishResult{}, fmt.Errorf("Lane batch %s is %s, not accepted_on_main; re-run lane send with --resume", normalized.BatchID, record.Status)
	}
	date := laneAcceptedDate(record, now)
	sourceNode := strings.TrimSpace(record.SourceNodeKey)
	if sourceNode == "" {
		sourceNode = "workspace"
	}
	if safeToken(sourceNode, "") != sourceNode {
		return PublishResult{}, fmt.Errorf("Lane batch %s has an invalid source node identity", normalized.BatchID)
	}
	remoteRuntimeRoot := strings.TrimSpace(record.RemoteRuntimeRoot)
	if remoteRuntimeRoot == "" {
		remoteRuntimeRoot = normalized.RemoteRoot
	} else if remoteRuntimeRoot, err = normalizeTrustedLaneRuntimeRoot(remoteRuntimeRoot); err != nil {
		return PublishResult{}, fmt.Errorf("Lane batch %s has unsafe runtime root: %w", normalized.BatchID, err)
	}
	expectedStagingPath := remotePathJoin(remoteRuntimeRoot, "staging", sourceNode, record.BatchID)
	if record.RemoteStagingPath != expectedStagingPath {
		err = fmt.Errorf("staging path does not match the batch identity")
		return PublishResult{}, fmt.Errorf("Lane batch %s has unsafe staging path: %w", normalized.BatchID, err)
	}
	switch record.LocalCleanupIntent {
	case "", LocalCleanupIntentKeep, LocalCleanupIntentQuarantine:
	default:
		return PublishResult{}, fmt.Errorf("Lane batch %s has unsupported local cleanup intent %q", normalized.BatchID, record.LocalCleanupIntent)
	}
	visibleStoragePath := strings.TrimSpace(record.VisibleStoragePath)
	if visibleStoragePath == "" {
		visibleStoragePath = filepath.ToSlash(filepath.Join("imports", sourceNode, date, record.BatchID))
	}
	result := PublishResult{
		BatchID:            record.BatchID,
		Status:             record.Status,
		DryRun:             normalized.DryRun,
		SourceNodeKey:      sourceNode,
		SourceBoxID:        record.SourceBoxID,
		VisibleStoragePath: visibleStoragePath,
		RemoteAcceptedPath: record.RemoteAcceptedPath,
		StartedAt:          now,
		Metrics:            record.Metrics,
	}
	acceptCommand := fmt.Sprintf("loom --json lane accept-received --source-node %s --batch-id %s --accepted-path %s --date %s --source-box-id %s --remote-root %s",
		shellQuote(sourceNode),
		shellQuote(record.BatchID),
		shellQuote(record.RemoteStagingPath),
		shellQuote(date),
		shellQuote(record.SourceBoxID),
		shellQuote(remoteRuntimeRoot),
	)
	if record.SelectedTransport == TransportModeBundleSeed {
		acceptCommand = fmt.Sprintf("loom --json lane accept-bundle --source-node %s --batch-id %s --accepted-path %s --date %s --source-box-id %s --remote-root %s --manifest-path %s --archive-path %s",
			shellQuote(sourceNode),
			shellQuote(record.BatchID),
			shellQuote(remotePathJoin(record.RemoteStagingPath, "tree")),
			shellQuote(date),
			shellQuote(record.SourceBoxID),
			shellQuote(remoteRuntimeRoot),
			shellQuote(remotePathJoin(record.RemoteStagingPath, bundleManifestFileName)),
			shellQuote(remotePathJoin(record.RemoteStagingPath, bundleArchiveFileName)),
		)
	}
	if record.AllowCrossDevicePromotion {
		acceptCommand += " --allow-cross-device-promotion"
	}
	acceptCommand, err = remoteRuntimeCommand(acceptCommand, remoteStagingReceiver{
		User: record.RemoteReceiverUser, SwitchRequired: record.RemoteReceiverSwitchRequired,
	})
	if err != nil {
		return PublishResult{}, fmt.Errorf("Lane batch %s has unsafe acceptance receiver identity: %w", normalized.BatchID, err)
	}
	if normalized.DryRun {
		if !laneBatchIsCataloged(record.Status) {
			result.Commands = append(result.Commands, CommandSummary{Name: "ssh", Args: []string{normalized.MainHost, "resume imports promotion and catalog lane batch"}, Status: "planned"})
		}
		if record.SelectedTransport == TransportModeBundleSeed || record.AllowCrossDevicePromotion || record.Status == BatchStatusSourceCleanupFailed {
			result.Commands = append(result.Commands, CommandSummary{Name: "ssh", Args: []string{normalized.MainHost, laneTransportCleanupLabel(record.SelectedTransport)}, Status: "planned"})
		}
		if record.LocalCleanupIntent == LocalCleanupIntentQuarantine && !laneRecordHasCompletedLocalCleanup(record) {
			result.Commands = append(result.Commands, CommandSummary{Name: "local", Args: []string{"revalidate reviewed Lane inventory and quarantine matching local payloads"}, Status: "planned"})
		}
		return result, nil
	}
	run := func(label, command string) error {
		output, err := executeCommand(ctx, normalized.Runner, "ssh", normalized.MainHost, command)
		appendPublishCommand(&result, "ssh", []string{normalized.MainHost, label}, output, err)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}
	originalStatus := record.Status
	originalError := record.ErrorMessage
	if !laneBatchIsCataloged(record.Status) {
		stageStarted := time.Now()
		if err := run("resume imports promotion and catalog lane batch", acceptCommand); err != nil {
			if classified := laneRemoteFailureStatus(result.Commands); classified != BatchStatusFailed {
				record.Status = classified
			}
			return publishFailure(recordPath, record, result, started, err)
		}
		record.Status = BatchStatusCataloged
		record.ErrorMessage = ""
		record.Metrics.CatalogDurationMS += durationMSSince(stageStarted)
		if err := writeBatchRecord(normalized.statePath(), record); err != nil {
			return publishFailure(recordPath, record, result, started, fmt.Errorf("record cataloged Lane phase: %w", err))
		}
		result.Status = BatchStatusCataloged
		result.Metrics = record.Metrics
	}
	if record.SelectedTransport == TransportModeBundleSeed || record.AllowCrossDevicePromotion || originalStatus == BatchStatusSourceCleanupFailed {
		label := laneTransportCleanupLabel(record.SelectedTransport)
		_, output, cleanupErr := invokeRemoteStaging(ctx, normalized.Runner, normalized.MainHost, RemoteStagingCleanup, sourceNode, record.BatchID, remoteRuntimeRoot, remoteStagingReceiver{
			User: record.RemoteReceiverUser, SwitchRequired: record.RemoteReceiverSwitchRequired,
		})
		appendPublishCommand(&result, "ssh", []string{normalized.MainHost, label}, output, cleanupErr)
		if cleanupErr != nil {
			record.Status = BatchStatusSourceCleanupFailed
			record.AttentionStatus = AttentionStatusActive
			return publishFailure(recordPath, record, result, started, fmt.Errorf("%s: %w", label, cleanupErr))
		}
	}
	completedAt := normalized.now()
	record.Status = repairedLaneCompletionStatus(record, originalStatus)
	record.VisibleStoragePath = visibleStoragePath
	if record.Status != BatchStatusLocalCleanupWithheld && record.LocalCleanupIntent == LocalCleanupIntentQuarantine && !laneRecordHasCompletedLocalCleanup(record) {
		cleanupStarted := time.Now()
		currentPlan, planErr := BuildTransferPlan(filepath.Join(normalized.RootPath, filepath.FromSlash(normalized.LaneRelPath)), normalized.RootPath, record.Profile)
		if planErr != nil {
			record.Status = BatchStatusLocalCleanupWithheld
			record.ErrorMessage = "main custody is promoted and cataloged, but local cleanup could not be revalidated: " + planErr.Error()
		} else if currentPlan.Profile != record.Profile || currentPlan.PolicyVersion != record.PolicyVersion || currentPlan.PolicyFingerprint != record.PolicyFingerprint || currentPlan.InventoryHash != record.InventoryHash {
			record.Status = BatchStatusLocalCleanupWithheld
			record.ErrorMessage = "main custody is promoted and cataloged, but the Lane source or reviewed cleanup policy changed; local cleanup was withheld"
		} else {
			outcome, cleanupErr := cleanupTransferredEntries(normalized.RootPath, normalized.LaneRelPath, normalized.statePath(), record.BatchID, currentPlan, nil, nil)
			applyPublishLocalCleanupOutcome(&record, &result, outcome)
			if cleanupErr != nil {
				record.Status = BatchStatusLocalCleanupWithheld
				record.ErrorMessage = "main custody is promoted and cataloged, but local cleanup could not finish safely: " + cleanupErr.Error()
			} else {
				record.Status = BatchStatusLocalCleanupDone
			}
		}
		record.Metrics.LocalCleanupDurationMS += durationMSSince(cleanupStarted)
	}
	if record.Status == BatchStatusLocalCleanupWithheld {
		if record.ErrorMessage == "" {
			record.ErrorMessage = originalError
		}
		record.CompletedAt = &completedAt
		record.AttentionStatus = AttentionStatusActive
		result.Warnings = append(result.Warnings, "Main custody is promoted and cataloged; local Lane content and recovery evidence were preserved because safe cleanup could not be proven.")
	} else {
		record.CompletedAt = &completedAt
		record.ErrorMessage = ""
		record.AttentionStatus = ""
		record.AttentionNote = ""
		record.AttentionUpdatedAt = nil
	}
	if record.Status == BatchStatusLocalCleanupDone && record.LocalCleanupQuarantinePath != "" && record.LocalCleanupQuarantineState == CleanupQuarantineRetainedForRecovery && record.LocalCleanupQuarantineExpiresAt == nil {
		expiresAt := completedAt.Add(SuccessfulCleanupQuarantineGrace)
		record.LocalCleanupQuarantineExpiresAt = &expiresAt
	}
	record.Metrics.TotalDurationMS = durationMSSince(started)
	if err := writeBatchRecord(normalized.statePath(), record); err != nil {
		return publishFailure(recordPath, record, result, started, fmt.Errorf("record cataloged Lane completion: %w", err))
	}
	if record.Status == BatchStatusCataloged || record.Status == BatchStatusLocalCleanupDone {
		if _, err := Housekeep(HousekeepingInput{
			RootPath:       normalized.RootPath,
			StatePath:      normalized.statePath(),
			CurrentBatchID: record.BatchID,
			Now:            func() time.Time { return completedAt },
		}); err != nil {
			result.Warnings = append(result.Warnings, "Lane recovery housekeeping needs repair: "+err.Error())
		} else if refreshed, err := readBatchRecord(recordPath); err == nil {
			record = refreshed
		}
	}
	result.Status = record.Status
	result.CompletedAt = record.CompletedAt
	result.ErrorMessage = record.ErrorMessage
	result.Metrics = record.Metrics
	result.LocalCleanupQuarantinePath = record.LocalCleanupQuarantinePath
	result.LocalCleanupQuarantineState = record.LocalCleanupQuarantineState
	result.QuarantinedLocalItems = append([]string{}, record.QuarantinedLocalItems...)
	result.RestoredLocalItems = append([]string{}, record.RestoredLocalItems...)
	result.RemovedLocalItems = append([]string{}, record.RemovedLocalItems...)
	return result, nil
}

func laneTransportCleanupLabel(transport TransportMode) string {
	if transport == TransportModeBundleSeed {
		return "remove cataloged Lane bundle transport staging"
	}
	return "remove cataloged Lane transport staging"
}

func repairedLaneCompletionStatus(record BatchRecord, originalStatus string) string {
	if originalStatus == BatchStatusLocalCleanupWithheld {
		return BatchStatusLocalCleanupWithheld
	}
	if laneRecordHasCompletedLocalCleanup(record) {
		return BatchStatusLocalCleanupDone
	}
	return BatchStatusCataloged
}

func laneRecordHasCompletedLocalCleanup(record BatchRecord) bool {
	switch record.LocalCleanupQuarantineState {
	case CleanupQuarantineRetainedForRecovery, CleanupQuarantineRemovalPending, CleanupQuarantineRemovedAfterGrace:
		return true
	default:
		return len(record.QuarantinedLocalItems) > 0 || len(record.RemovedLocalItems) > 0
	}
}

func applyPublishLocalCleanupOutcome(record *BatchRecord, result *PublishResult, outcome localCleanupOutcome) {
	if record != nil {
		record.LocalCleanupQuarantinePath = outcome.QuarantinePath
		record.LocalCleanupQuarantineState = outcome.QuarantineState
		record.QuarantinedLocalItems = append([]string{}, outcome.Quarantined...)
		record.RestoredLocalItems = append([]string{}, outcome.Restored...)
		record.RemovedLocalItems = append([]string{}, outcome.Removed...)
	}
	if result != nil {
		result.LocalCleanupQuarantinePath = outcome.QuarantinePath
		result.LocalCleanupQuarantineState = outcome.QuarantineState
		result.QuarantinedLocalItems = append([]string{}, outcome.Quarantined...)
		result.RestoredLocalItems = append([]string{}, outcome.Restored...)
		result.RemovedLocalItems = append([]string{}, outcome.Removed...)
	}
}

func normalizePublishInput(input PublishInput) (PublishInput, error) {
	input.RootPath = strings.TrimSpace(input.RootPath)
	if input.RootPath == "" {
		return PublishInput{}, fmt.Errorf("root path is required")
	}
	absRoot, err := filepath.Abs(input.RootPath)
	if err != nil {
		return PublishInput{}, fmt.Errorf("resolve root path: %w", err)
	}
	input.RootPath = filepath.Clean(absRoot)
	input.LaneRelPath = filepath.ToSlash(strings.TrimSpace(input.LaneRelPath))
	if input.LaneRelPath == "" {
		input.LaneRelPath = DefaultLaneRelPath
	}
	if _, err := safeCleanupRelativeRoot(input.LaneRelPath, "Lane"); err != nil {
		return PublishInput{}, err
	}
	input.StateRelPath = filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if input.StateRelPath == "" {
		input.StateRelPath = DefaultStateRelPath
	}
	input.StatePath, err = normalizeLaneStatePath(input.RootPath, input.StateRelPath, input.StatePath)
	if err != nil {
		return PublishInput{}, err
	}
	requestedBatchID := strings.TrimSpace(input.BatchID)
	input.BatchID = safeToken(requestedBatchID, "")
	if input.BatchID == "" || input.BatchID != requestedBatchID {
		return PublishInput{}, fmt.Errorf("batch id is required")
	}
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
		return PublishInput{}, err
	}
	input.RemoteRoot = remoteRoot
	return input, nil
}

func (input PublishInput) now() time.Time {
	if input.Now != nil {
		return input.Now().UTC()
	}
	return time.Now().UTC()
}

func (input PublishInput) statePath() string {
	return input.StatePath
}

func publishFailure(recordPath string, record BatchRecord, result PublishResult, started time.Time, err error) (PublishResult, error) {
	message := err.Error()
	record.ErrorMessage = message
	record.Metrics.TotalDurationMS = durationMSSince(started)
	result.ErrorMessage = message
	result.Status = record.Status
	result.Metrics = record.Metrics
	_ = writeBatchRecord(filepath.Dir(filepath.Dir(recordPath)), record)
	return result, err
}

func appendPublishCommand(result *PublishResult, name string, args []string, output commandOutput, err error) {
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

func laneAcceptedDate(record BatchRecord, fallback time.Time) string {
	for _, pathValue := range []string{record.RemoteAcceptedPath, record.VisibleStoragePath} {
		parts := strings.Split(strings.Trim(filepath.ToSlash(strings.TrimSpace(pathValue)), "/"), "/")
		if len(parts) >= 2 && parts[len(parts)-1] == record.BatchID {
			if parsed, err := time.Parse("2006-01-02", parts[len(parts)-2]); err == nil {
				return parsed.Format("2006-01-02")
			}
		}
	}
	if record.StartedAt != nil && !record.StartedAt.IsZero() {
		return record.StartedAt.UTC().Format("2006-01-02")
	}
	return fallback.UTC().Format("2006-01-02")
}
