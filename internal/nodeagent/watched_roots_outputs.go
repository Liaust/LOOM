package nodeagent

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

func runWatchedRootReconcileAndPlan(ctx context.Context, store Store, config Config, state State, instance noderuntime.WorkerInstance, root watchedroots.ValidatedRoot, mode string, planOutputs bool, flushOutputs bool, reportToMain bool, correlationID string) (watchedroots.ScanResult, error) {
	// A concurrent flush must not acknowledge into a path snapshot that this scan
	// later replaces. Use the same cross-process lock as queue and acknowledgement.
	store, unlock, err := store.lockLocalSync(ctx)
	if err != nil {
		return watchedroots.ScanResult{}, err
	}
	defer unlock()
	watchedStore := watchedroots.NewStore(store.DataDir)
	result, err := watchedroots.Reconcile(ctx, watchedStore, watchedroots.ScanRequest{
		Mode:      mode,
		Root:      root,
		WorkerKey: instance.WorkerKey,
	})
	if err != nil {
		return watchedroots.ScanResult{}, err
	}
	if planOutputs && result.Status != watchedroots.RunStatusBlocked {
		plan, err := watchedroots.PlanOutputs(watchedStore, root, instance.WorkerKey)
		if err != nil {
			return watchedroots.ScanResult{}, err
		}
		plan = applyWatchedRootOutputPlan(store, config, state, watchedStore, root, plan)
		result.OutputPlan = &plan
		if backupStatus, err := store.backupStatusForWatchedRoot(config, state, root.Config.RootKey); err == nil {
			result.BackupStatus = backupStatus
		}
		if plan.Counts.Failed > 0 && result.Status == watchedroots.RunStatusHealthy {
			result.Status = watchedroots.RunStatusDegraded
			result.Message = "watched-root scan completed with output queue failures"
		}
		if flushOutputs {
			flush := flushWatchedRootOutputs(ctx, store, config, state, correlationID)
			result.OutputFlush = &flush
			if flush.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
				result.Status = watchedroots.RunStatusDegraded
				result.Message = "watched-root scan completed with queued output still pending"
			}
			backupFlush := flushWatchedRootBackups(ctx, store, config, state, correlationID)
			result.BackupFlush = &backupFlush
			if backupFlush.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
				result.Status = watchedroots.RunStatusDegraded
				result.Message = "watched-root scan completed with queued backup still pending"
			}
			if backupStatus, err := store.backupStatusForWatchedRoot(config, state, root.Config.RootKey); err == nil {
				result.BackupStatus = backupStatus
			}
		}
	}
	if reportToMain {
		report := reportWatchedRootRun(ctx, store, config, state, instance, root, result, correlationID)
		result.MainReport = &report
		if report.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
			result.Status = watchedroots.RunStatusDegraded
			result.Message = "watched-root scan completed but main status report failed"
		}
	}
	return result, nil
}

func flushWatchedRootOutputs(ctx context.Context, store Store, config Config, state State, correlationID string) watchedroots.OutputFlush {
	before, err := store.LocalSyncStatus(config, state)
	if err != nil {
		return watchedroots.OutputFlush{Attempted: false, Status: watchedroots.OutputStatusFailed, Error: err.Error()}
	}
	flush := watchedroots.OutputFlush{
		Attempted:     true,
		Status:        watchedroots.OutputStatusQueued,
		PendingBefore: before.Counts.Pending,
	}
	if strings.TrimSpace(state.NodeID) == "" || strings.TrimSpace(state.CredentialToken) == "" {
		flush.Attempted = false
		flush.Status = "skipped_missing_credentials"
		flush.PendingAfter = before.Counts.Pending
		flush.AcceptedAfter = before.Counts.Accepted
		flush.ConflictedAfter = before.Counts.Conflicted
		flush.FailedAfter = before.Counts.Failed
		return flush
	}
	if before.Counts.Pending == 0 {
		flush.Status = watchedroots.OutputStatusAlreadyCurrent
		flush.PendingAfter = before.Counts.Pending
		flush.AcceptedAfter = before.Counts.Accepted
		flush.ConflictedAfter = before.Counts.Conflicted
		flush.FailedAfter = before.Counts.Failed
		return flush
	}
	push, err := pushLocalSyncOnce(ctx, store, config, state, correlationID, before.Counts.Pending, false)
	if err != nil {
		flush.Status = watchedroots.OutputStatusFailed
		flush.Error = err.Error()
		if after, statusErr := store.LocalSyncStatus(config, state); statusErr == nil {
			flush.PendingAfter = after.Counts.Pending
			flush.AcceptedAfter = after.Counts.Accepted
			flush.ConflictedAfter = after.Counts.Conflicted
			flush.FailedAfter = after.Counts.Failed
		}
		return flush
	}
	flush.SubmittedItems = push.SubmittedItems
	flush.PendingAfter = push.LocalStatus.Counts.Pending
	flush.AcceptedAfter = push.LocalStatus.Counts.Accepted
	flush.ConflictedAfter = push.LocalStatus.Counts.Conflicted
	flush.FailedAfter = push.LocalStatus.Counts.Failed
	if push.LocalStatus.Counts.Pending > 0 || push.LocalStatus.Counts.Failed > 0 || push.LocalStatus.Counts.Conflicted > 0 {
		flush.Status = watchedroots.OutputStatusFailed
	} else {
		flush.Status = watchedroots.OutputStatusRecorded
	}
	return flush
}

func applyWatchedRootOutputPlan(store Store, config Config, state State, watchedStore watchedroots.Store, root watchedroots.ValidatedRoot, plan watchedroots.OutputPlan) watchedroots.OutputPlan {
	for idx := range plan.Actions {
		action := &plan.Actions[idx]
		current, err := watchedStore.LoadPathState(action.RootKey, action.RelativePath)
		if err != nil {
			markWatchedRootOutputFailed(action, "load_path_state_failed", err.Error())
			plan.Counts.Failed++
			continue
		}
		now := time.Now().UTC()
		current.LastOutputPlannedAt = &plan.GeneratedAt
		switch action.ActionKind {
		case watchedroots.OutputActionSkipped:
			current.SyncStatus = watchedroots.OutputStatusSkipped
			current.LastOutputErrorCode = action.ReasonCode
			current.LastOutputErrorMessage = action.Reason
			current.LastOutputAppliedAt = &now
			action.Status = watchedroots.OutputStatusSkipped
		case watchedroots.OutputActionBackupSkipped:
			current.BackupStatus = watchedroots.OutputStatusSkipped
			if watchedRootBackupPermanentLocalError(action.ReasonCode) {
				current.BackupStatus = watchedroots.OutputStatusFailed
				action.Status = watchedroots.OutputStatusFailed
				plan.Counts.Failed++
			}
			current.BackupMode = action.BackupMode
			current.LastBackupErrorCode = action.ReasonCode
			current.LastBackupErrorMessage = action.Reason
			current.LastOutputAppliedAt = &now
			if action.Status == "" {
				action.Status = watchedroots.OutputStatusSkipped
			}
			saveWatchedRootBackupFinding(watchedStore, action, action.ReasonCode, action.Reason)
		case watchedroots.OutputActionSyncObject:
			contentPath := filepath.Join(root.RootPath, filepath.FromSlash(action.RelativePath))
			object, item, err := store.QueueWatchedRootObject(config, state, *action, contentPath)
			if err != nil {
				current.SyncStatus = watchedroots.OutputStatusFailed
				current.LastOutputErrorCode = "queue_sync_object_failed"
				current.LastOutputErrorMessage = err.Error()
				markWatchedRootOutputFailed(action, current.LastOutputErrorCode, err.Error())
				plan.Counts.Failed++
			} else {
				current.SyncStatus = watchedroots.OutputStatusQueued
				current.IndexStatus = watchedRootIndexStatus(action.IndexPolicy)
				current.LocalObjectID = object.LocalObjectID
				current.LocalVersionID = object.LocalVersionID
				current.LocalSyncOutboxID = item.LocalOutboxID
				current.LastQueuedHashURI = object.HashURI
				current.LastQueuedMetadataMtime = nil
				current.LastOutputErrorCode = ""
				current.LastOutputErrorMessage = ""
				current.LastOutputAppliedAt = &now
				action.Status = watchedroots.OutputStatusQueued
				action.LocalObjectID = object.LocalObjectID
				action.LocalVersionID = object.LocalVersionID
				action.LocalSyncOutboxID = item.LocalOutboxID
				action.ContentHashURI = object.HashURI
			}
		case watchedroots.OutputActionSyncMetadata:
			item, err := store.QueueWatchedRootMetadata(state, *action)
			if err != nil {
				markWatchedRootOutputFailed(action, "queue_sync_metadata_failed", err.Error())
				plan.Counts.Failed++
			} else {
				current.LastQueuedMetadataMtime = action.ModifiedAt
				action.Status = watchedroots.OutputStatusQueued
				action.LocalSyncOutboxID = item.LocalOutboxID
			}
		case watchedroots.OutputActionDeletionRequest:
			deletion, item, err := store.QueueWatchedRootDeletion(config, state, *action)
			if err != nil {
				current.DeletionStatus = watchedroots.OutputStatusFailed
				current.LastOutputErrorCode = "queue_deletion_request_failed"
				current.LastOutputErrorMessage = err.Error()
				markWatchedRootOutputFailed(action, current.LastOutputErrorCode, err.Error())
				plan.Counts.Failed++
			} else {
				current.DeletionStatus = watchedroots.OutputStatusQueued
				current.DeletionRequestID = deletion.LocalDeletionID
				current.LocalSyncOutboxID = item.LocalOutboxID
				current.LastOutputErrorCode = ""
				current.LastOutputErrorMessage = ""
				current.LastOutputAppliedAt = &now
				action.Status = watchedroots.OutputStatusQueued
				action.LocalSyncOutboxID = item.LocalOutboxID
			}
		case watchedroots.OutputActionBackupFile, watchedroots.OutputActionBackupMetadata, watchedroots.OutputActionBackupDeletionMarker:
			contentPath := ""
			if action.ActionKind == watchedroots.OutputActionBackupFile {
				contentPath = filepath.Join(root.RootPath, filepath.FromSlash(action.RelativePath))
			}
			batch, outboxItem, err := store.QueueWatchedRootBackupAction(config, state, *action, contentPath)
			if err != nil {
				code := watchedRootBackupErrorCode(err)
				current.BackupStatus = watchedroots.OutputStatusFailed
				current.BackupMode = action.BackupMode
				current.LastBackupErrorCode = code
				current.LastBackupErrorMessage = err.Error()
				markWatchedRootOutputFailed(action, code, err.Error())
				saveWatchedRootBackupFinding(watchedStore, action, code, err.Error())
				plan.Counts.Failed++
			} else {
				backupItem := localWatchedRootBackupItemForBatch(store, batch.LocalBatchID)
				current.BackupStatus = watchedroots.OutputStatusQueued
				current.BackupMode = action.BackupMode
				current.LocalBackupBatchID = batch.LocalBatchID
				current.LocalBackupOutboxID = outboxItem.LocalOutboxID
				current.LocalBackupArtifactID = backupItem.LocalArtifactID
				current.LastBackupQueuedAt = &now
				if action.ActionKind == watchedroots.OutputActionBackupFile || action.ActionKind == watchedroots.OutputActionBackupMetadata {
					current.LastQueuedBackupHashURI = action.ContentHashURI
				}
				if action.ActionKind == watchedroots.OutputActionBackupDeletionMarker {
					current.BackupDeletionMarkerID = backupItem.LocalItemID
				}
				current.LastBackupErrorCode = ""
				current.LastBackupErrorMessage = ""
				current.LastOutputAppliedAt = &now
				action.Status = watchedroots.OutputStatusQueued
				action.LocalBackupBatchID = batch.LocalBatchID
				action.LocalBackupOutboxID = outboxItem.LocalOutboxID
				action.LocalBackupItemID = backupItem.LocalItemID
				action.LocalBackupArtifactID = backupItem.LocalArtifactID
			}
		}
		if err := watchedStore.SavePathState(current); err != nil {
			markWatchedRootOutputFailed(action, "save_path_state_failed", err.Error())
			plan.Counts.Failed++
		}
	}
	return plan
}

func watchedRootIndexStatus(mode string) string {
	switch strings.TrimSpace(mode) {
	case watchedroots.IndexModeNone:
		return watchedroots.OutputStatusDisabled
	default:
		return watchedroots.OutputStatusQueued
	}
}

func markWatchedRootOutputFailed(action *watchedroots.OutputAction, code string, message string) {
	action.Status = watchedroots.OutputStatusFailed
	action.ReasonCode = code
	action.Error = message
}

func localWatchedRootBackupItemForBatch(store Store, localBatchID string) LocalWatchedRootBackupItem {
	items, err := store.LoadWatchedRootBackupItems()
	if err != nil {
		return LocalWatchedRootBackupItem{}
	}
	for _, item := range items {
		if item.LocalBatchID == localBatchID {
			return item
		}
	}
	return LocalWatchedRootBackupItem{}
}

func saveWatchedRootBackupFinding(store watchedroots.Store, action *watchedroots.OutputAction, code string, message string) {
	if strings.TrimSpace(code) == "" {
		code = watchedroots.FindingBackupStageFailed
	}
	if strings.TrimSpace(message) == "" {
		message = code
	}
	_ = store.SaveFinding(watchedroots.Finding{
		RootKey:      action.RootKey,
		Severity:     watchedRootBackupFindingSeverity(code),
		Status:       watchedroots.FindingStatusOpen,
		Kind:         code,
		RelativePath: action.RelativePath,
		Summary:      message,
	})
}

func watchedRootBackupFindingSeverity(code string) string {
	if watchedRootBackupPermanentLocalError(code) {
		return watchedroots.FindingSeverityCritical
	}
	return watchedroots.FindingSeverityWarning
}
