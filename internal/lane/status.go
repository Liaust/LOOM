package lane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

const defaultMaxItems = 20

func BuildStatus(input StatusInput) Status {
	now := time.Now().UTC()
	if input.Now != nil {
		now = input.Now().UTC()
	}
	root := strings.TrimSpace(input.RootPath)
	laneRel := filepath.ToSlash(strings.TrimSpace(input.LaneRelPath))
	if laneRel == "" {
		laneRel = DefaultLaneRelPath
	}
	stateRel := filepath.ToSlash(strings.TrimSpace(input.StateRelPath))
	if stateRel == "" {
		stateRel = DefaultStateRelPath
	}
	statePath := strings.TrimSpace(input.StatePath)
	if statePath == "" {
		statePath = filepath.Join(root, filepath.FromSlash(stateRel))
	} else if !filepath.IsAbs(statePath) {
		status := Status{
			SchemaVersion: StatusSchemaVersion,
			State:         StateError,
			RootPath:      root,
			StatePath:     statePath,
			InspectedAt:   now,
			Diagnostics: []Diagnostic{{
				Severity: "error",
				Code:     "lane.state_path_invalid",
				Message:  "LOOM Lane state path must be absolute when configured explicitly.",
				Path:     statePath,
			}},
		}
		return status
	}
	status := Status{
		SchemaVersion:    StatusSchemaVersion,
		State:            StateNotInitialized,
		RootPath:         root,
		LaneRelativePath: laneRel,
		LanePath:         filepath.Join(root, filepath.FromSlash(laneRel)),
		StatePath:        filepath.Clean(statePath),
		InspectedAt:      now,
		Preflight:        BuildPreflight(input),
		Profile:          input.Profile,
	}
	if status.Profile == "" {
		status.Profile = "faithful"
	}
	if root == "" {
		status.State = StateError
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "lane.root_missing",
			Message:    "LOOM Lane cannot be inspected without a Box root path.",
			Suggestion: "initialize or repair the local LOOM Box",
		})
		return status
	}
	recovery, recoveryErr := InspectRecoveryStorageAt(status.StatePath)
	status.RecoveryStorage = recovery
	if recoveryErr != nil && !errors.Is(recoveryErr, os.ErrNotExist) {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.recovery_storage_inspect_failed",
			Message:    recoveryErr.Error(),
			Path:       status.StatePath,
			Suggestion: "inspect retained Lane recovery storage and batch records before operator cleanup",
		})
	}
	warningBytes := input.RecoveryWarningBytes
	if warningBytes <= 0 {
		warningBytes = DefaultRecoveryStorageWarningBytes
	}
	if recovery.ProtectedEvidenceBytes >= warningBytes && recovery.ProtectedEvidenceBytes > 0 {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.recovery_storage_protected",
			Message:    fmt.Sprintf("Retained protected or untracked Lane recovery evidence uses %d bytes and will not be removed automatically.", recovery.ProtectedEvidenceBytes),
			Path:       status.StatePath,
			Suggestion: "repair or retry the protected batches, then use explicit operator cleanup after custody is confirmed",
		})
	}
	if recovery.UntrackedPathCount > 0 {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.recovery_storage_untracked",
			Message:    fmt.Sprintf("Lane recovery storage contains %d untracked path(s) using %d bytes; they were preserved.", recovery.UntrackedPathCount, recovery.UntrackedBytes),
			Path:       status.StatePath,
			Suggestion: "inspect the untracked recovery paths and batch records before explicit operator cleanup",
		})
	}
	if recovery.RedundantSuccessfulArtifactCount > 0 {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.recovery_housekeeping_pending",
			Message:    fmt.Sprintf("%d redundant successful Lane transport artifact(s) await automatic housekeeping.", recovery.RedundantSuccessfulArtifactCount),
			Path:       status.StatePath,
			Suggestion: "check the Lane housekeeping worker if this remains after its next interval",
		})
	}
	info, err := os.Stat(status.LanePath)
	if errors.Is(err, os.ErrNotExist) {
		status.State = StateMissing
		return status
	}
	if err != nil {
		status.State = StateError
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity: "error",
			Code:     "lane.path_unavailable",
			Message:  err.Error(),
			Path:     status.LanePath,
		})
		return status
	}
	if !info.IsDir() {
		status.State = StateError
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "lane.path_blocked",
			Message:    "LOOM Lane path exists but is not a directory.",
			Path:       status.LanePath,
			Suggestion: "move the blocking file and run loom box repair",
		})
		return status
	}
	status.State = ""
	scanLane(&status, maxItems(input.MaxItems))
	if last, ok := loadLatestTransfer(status.StatePath); ok {
		status.LastTransfer = &last
		if laneTransferFailureNeedsAttention(last.Status) && last.AttentionStatus == AttentionStatusActive {
			suggestion := "inspect the failure, then run loom lane acknowledge-transfer or loom lane archive-transfer if no retry is needed"
			switch last.Status {
			case BatchStatusPromotionFailed, BatchStatusAcceptedOnMain, BatchStatusCatalogFailed, BatchStatusSourceCleanupFailed:
				suggestion = fmt.Sprintf("inspect the retained evidence, then run loom lane repair %s; acknowledge or archive only if no retry is needed", shellQuote(last.BatchID))
			}
			status.Diagnostics = append(status.Diagnostics, Diagnostic{
				Severity:   "warning",
				Code:       "lane.transfer_failed",
				Message:    fmt.Sprintf("LOOM Lane transfer %s stopped in %s and is still active attention.", last.BatchID, last.Status),
				Path:       status.StatePath,
				Suggestion: suggestion,
			})
		} else if last.Status == BatchStatusLocalCleanupWithheld && last.AttentionStatus == AttentionStatusActive {
			status.Diagnostics = append(status.Diagnostics, Diagnostic{
				Severity:   "warning",
				Code:       "lane.local_cleanup_withheld",
				Message:    fmt.Sprintf("LOOM Lane transfer %s is promoted and cataloged on main, but local cleanup was withheld because the source changed.", last.BatchID),
				Path:       status.LanePath,
				Suggestion: "inspect the changed Lane source, then send it as a new batch or acknowledge the retained cleanup attention",
			})
		}
	}
	if status.State == "" {
		if status.PendingItems > 0 {
			status.State = StatePending
		} else {
			status.State = StateEmpty
		}
	}
	return status
}

func BuildPreflight(input StatusInput) PreflightStatus {
	mainHost := strings.TrimSpace(input.MainHost)
	if mainHost == "" {
		mainHost = DefaultMainHost
	}
	lookup := exec.LookPath
	if input.LookupPath != nil {
		lookup = input.LookupPath
	}
	preflight := PreflightStatus{
		Status:          PreflightReady,
		MainHost:        mainHost,
		SSHConfigStatus: "unchecked",
	}
	if rsyncPath, err := preferredRsyncPath(lookup); err == nil {
		preflight.RsyncPath = rsyncPath
	} else {
		preflight.Status = PreflightMissingTools
		preflight.Diagnostics = append(preflight.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "lane.rsync_missing",
			Message:    "rsync is not available on this node.",
			Suggestion: "install rsync before sending LOOM Lane to main",
		})
	}
	if sshPath, err := lookup("ssh"); err == nil {
		preflight.SSHPath = sshPath
	} else {
		preflight.Status = PreflightMissingTools
		preflight.Diagnostics = append(preflight.Diagnostics, Diagnostic{
			Severity:   "error",
			Code:       "lane.ssh_missing",
			Message:    "ssh is not available on this node.",
			Suggestion: "install OpenSSH before sending LOOM Lane to main",
		})
	}
	if preflight.SSHPath == "" || mainHost == "" {
		return preflight
	}
	check := input.SSHConfigCheck
	if check == nil {
		check = defaultSSHConfigCheck
	}
	if err := check(mainHost); err != nil {
		if preflight.Status == PreflightReady {
			preflight.Status = PreflightDegraded
		}
		preflight.SSHConfigStatus = "unavailable"
		preflight.Diagnostics = append(preflight.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.ssh_config_unavailable",
			Message:    fmt.Sprintf("ssh config for %s could not be resolved: %v", mainHost, err),
			Suggestion: "check the loom-main SSH alias before sending LOOM Lane",
		})
	} else {
		preflight.SSHConfigStatus = "ok"
	}
	return preflight
}

func normalizeLaneStatePath(rootPath, stateRelPath, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		stateRelPath = filepath.ToSlash(strings.TrimSpace(stateRelPath))
		if stateRelPath == "" {
			stateRelPath = DefaultStateRelPath
		}
		stateRoot, err := safeCleanupRelativeRoot(stateRelPath, "Lane state")
		if err != nil {
			return "", err
		}
		return filepath.Clean(filepath.Join(rootPath, stateRoot)), nil
	}
	if !filepath.IsAbs(configured) {
		return "", fmt.Errorf("Lane state path must be absolute when configured explicitly")
	}
	configured = filepath.Clean(configured)
	if configured == string(filepath.Separator) {
		return "", fmt.Errorf("Lane state path must not be the filesystem root")
	}
	return configured, nil
}

func scanLane(status *Status, maxItems int) {
	plan, err := BuildTransferPlan(status.LanePath, status.RootPath, status.Profile)
	if err != nil {
		status.State = StateError
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity: "error",
			Code:     "lane.policy_plan_failed",
			Message:  err.Error(),
			Path:     status.LanePath,
		})
		return
	}
	status.transferPlan = plan
	status.PolicyVersion = plan.PolicyVersion
	status.PolicyFingerprint = plan.PolicyFingerprint
	status.InventoryHash = plan.InventoryHash
	status.PolicyHashes = plan.PolicyHashes
	status.IgnoredEntryCount = len(plan.Ignored)
	status.IgnoredFileCount = plan.IgnoredFileCount
	status.IgnoredBytes = plan.IgnoredBytes
	status.PendingItems = len(plan.Items)
	status.PendingFiles = plan.FileCount
	status.PendingDirs = plan.DirCount
	status.PendingBytes = plan.TotalBytes
	status.Warnings = append(status.Warnings, plan.Warnings...)
	status.Transport = plan.Transport
	for _, warning := range plan.Warnings {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{Severity: "warning", Code: "lane.transfer_policy_warning", Message: warning, Path: status.LanePath})
	}
	attention := loadAttentionState(status.StatePath)
	staleActivePending := 0
	for _, plannedItem := range plan.Items {
		item := plannedItem
		applyPendingItemAttention(&item, attention, status.InspectedAt)
		switch item.AttentionStatus {
		case AttentionStatusAcknowledged:
			status.AcknowledgedPendingItems++
		default:
			status.ActivePendingItems++
			if item.AgeSeconds >= int64(PendingAttentionStaleAfter.Seconds()) {
				staleActivePending++
			}
		}
		if len(status.Items) < maxItems {
			status.Items = append(status.Items, item)
		}
	}
	if staleActivePending > 0 {
		status.Diagnostics = append(status.Diagnostics, Diagnostic{
			Severity:   "warning",
			Code:       "lane.pending_stale",
			Message:    fmt.Sprintf("%d stale LOOM Lane pending item(s) still need a user decision.", staleActivePending),
			Path:       status.LanePath,
			Suggestion: "send the Lane explicitly or acknowledge the current pending item state without deleting files",
		})
	}
}

func laneFidelityWarnings(observation filesystemmeta.Observation) []string {
	var warnings []string
	warnings = append(warnings, observation.Risks...)
	if observation.IsSparse {
		warnings = append(warnings, "sparse_file")
	}
	if observation.Executable {
		warnings = append(warnings, "executable_bit")
	}
	return warnings
}

func loadLatestTransfer(statePath string) (TransferSummary, bool) {
	pattern := filepath.Join(statePath, "batches", "*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return TransferSummary{}, false
	}
	var latest BatchRecord
	for _, match := range matches {
		record, err := readBatchRecord(match)
		if err != nil || strings.TrimSpace(record.BatchID) == "" {
			continue
		}
		if strings.TrimSpace(latest.BatchID) == "" || transferRecordAfter(record, latest) {
			latest = record
		}
	}
	if strings.TrimSpace(latest.BatchID) == "" {
		return TransferSummary{}, false
	}
	summary := TransferSummary{
		BatchID:                             latest.BatchID,
		Status:                              latest.Status,
		VisibleStoragePath:                  latest.VisibleStoragePath,
		FileCount:                           latest.FileCount,
		TotalBytes:                          latest.TotalBytes,
		Progress:                            latest.Progress,
		Metrics:                             latest.Metrics,
		StartedAt:                           latest.StartedAt,
		CompletedAt:                         latest.CompletedAt,
		ErrorMessage:                        latest.ErrorMessage,
		AttentionStatus:                     transferAttentionStatus(latest),
		AttentionNote:                       latest.AttentionNote,
		AttentionUpdatedAt:                  latest.AttentionUpdatedAt,
		Profile:                             latest.Profile,
		PolicyVersion:                       latest.PolicyVersion,
		PolicyFingerprint:                   latest.PolicyFingerprint,
		InventoryHash:                       latest.InventoryHash,
		IgnoredFileCount:                    latest.IgnoredFileCount,
		IgnoredBytes:                        latest.IgnoredBytes,
		RequestedTransport:                  latest.RequestedTransport,
		SelectedTransport:                   latest.SelectedTransport,
		TransportReason:                     latest.TransportReason,
		LocalCleanupIntent:                  latest.LocalCleanupIntent,
		AllowCrossDevicePromotion:           latest.AllowCrossDevicePromotion,
		BundleArtifactPath:                  latest.BundleArtifactPath,
		BundleArchiveSHA256:                 latest.BundleArchiveSHA256,
		BundleCleanupState:                  latest.BundleCleanupState,
		LocalSafetyCleanupState:             latest.LocalSafetyCleanupState,
		LocalSafetyRemovedAt:                latest.LocalSafetyRemovedAt,
		LocalSafetyRemovalReason:            latest.LocalSafetyRemovalReason,
		LocalCleanupQuarantinePath:          latest.LocalCleanupQuarantinePath,
		LocalCleanupQuarantineState:         latest.LocalCleanupQuarantineState,
		LocalCleanupQuarantineBytes:         latest.LocalCleanupQuarantineBytes,
		LocalCleanupQuarantineExpiresAt:     latest.LocalCleanupQuarantineExpiresAt,
		LocalCleanupQuarantineRemovedAt:     latest.LocalCleanupQuarantineRemovedAt,
		LocalCleanupQuarantineRemovalReason: latest.LocalCleanupQuarantineRemovalReason,
		QuarantinedLocalItems:               append([]string{}, latest.QuarantinedLocalItems...),
		RestoredLocalItems:                  append([]string{}, latest.RestoredLocalItems...),
		RemovedLocalItems:                   append([]string{}, latest.RemovedLocalItems...),
	}
	summary.NextActions = transferSafeActions(summary)
	return summary, true
}

func transferRecordAfter(left, right BatchRecord) bool {
	leftTime := transferRecordSortTime(left)
	rightTime := transferRecordSortTime(right)
	if !leftTime.Equal(rightTime) {
		if rightTime.IsZero() {
			return true
		}
		if leftTime.IsZero() {
			return false
		}
		return leftTime.After(rightTime)
	}
	leftRank := transferStatusRank(left.Status)
	rightRank := transferStatusRank(right.Status)
	if leftRank != rightRank {
		return leftRank > rightRank
	}
	return left.BatchID > right.BatchID
}

func transferRecordSortTime(record BatchRecord) time.Time {
	if record.Status != BatchStatusSuperseded && record.CompletedAt != nil {
		return record.CompletedAt.UTC()
	}
	if record.StartedAt != nil {
		return record.StartedAt.UTC()
	}
	if record.CompletedAt != nil {
		return record.CompletedAt.UTC()
	}
	return time.Time{}
}

func transferStatusRank(status string) int {
	switch status {
	case BatchStatusLocalCleanupDone:
		return 90
	case BatchStatusLocalCleanupWithheld:
		return 85
	case BatchStatusPublishedStorageView:
		return 80
	case BatchStatusAccepted, BatchStatusCataloged:
		return 70
	case BatchStatusCatalogFailed, BatchStatusSourceCleanupFailed:
		return 65
	case BatchStatusAcceptedOnMain:
		return 60
	case BatchStatusTransferred:
		return 55
	case BatchStatusPromotionFailed, BatchStatusObsoleteAcceptedMigrationRequired:
		return 45
	case BatchStatusFailed:
		return 40
	case BatchStatusTransferring:
		return 30
	case BatchStatusBundleReady:
		return 27
	case BatchStatusBundling:
		return 25
	case BatchStatusPrepared:
		return 20
	case BatchStatusSuperseded:
		return 10
	default:
		return 0
	}
}

func laneBatchHasMainCustody(status string) bool {
	return transferStatusRank(status) >= transferStatusRank(BatchStatusAcceptedOnMain)
}

func laneBatchIsCataloged(status string) bool {
	switch status {
	case BatchStatusAccepted, BatchStatusCataloged, BatchStatusSourceCleanupFailed,
		BatchStatusLocalCleanupWithheld, BatchStatusLocalCleanupDone, BatchStatusPublishedStorageView:
		return true
	default:
		return false
	}
}

func laneTransferFailureNeedsAttention(status string) bool {
	switch status {
	case BatchStatusFailed, BatchStatusPromotionFailed, BatchStatusAcceptedOnMain, BatchStatusCatalogFailed, BatchStatusSourceCleanupFailed, BatchStatusObsoleteAcceptedMigrationRequired:
		return true
	default:
		return false
	}
}

func preferredRsyncPath(lookup func(string) (string, error)) (string, error) {
	if path, err := os.Stat("/opt/homebrew/bin/rsync"); err == nil && !path.IsDir() {
		return "/opt/homebrew/bin/rsync", nil
	}
	return lookup("rsync")
}

func defaultSSHConfigCheck(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := execCommandRunner(ctx, "ssh", "-G", host)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(output.Stdout+"\n"+output.Stderr))
	}
	return nil
}

func maxItems(value int) int {
	if value <= 0 {
		return defaultMaxItems
	}
	return value
}
