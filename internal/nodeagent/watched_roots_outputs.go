package nodeagent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	"loom.local/loom/internal/nodeagent/watchedroots"
)

const (
	watchedRootOutputBatchSize = 64
	watchedRootOutputStatePage = 4096
	watchedRootPlanningBudget  = 10 * time.Second
)

func runWatchedRootReconcileAndPlan(ctx context.Context, store Store, config Config, state State, instance noderuntime.WorkerInstance, root watchedroots.ValidatedRoot, mode string, planOutputs bool, flushOutputs bool, reportToMain bool, correlationID string) (watchedroots.ScanResult, error) {
	if err := validateApplicationSource(ctx, config, state, root.SafeRoot, root.Config); err != nil {
		root.RootReachable = false
		result := watchedroots.ScanResult{Status: watchedroots.RunStatusBlocked, Message: err.Error()}
		if reportToMain {
			report := reportWatchedRootRun(ctx, store, config, state, instance, root, result, correlationID)
			result.MainReport = &report
		}
		return result, nil
	}
	// A concurrent flush must not acknowledge into a path snapshot that this scan
	// later replaces. Use the same cross-process lock as queue and acknowledgement.
	store, unlock, err := store.lockLocalSync(ctx)
	if err != nil {
		return watchedroots.ScanResult{}, err
	}
	defer unlock()
	turnStarted := time.Now()
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
		if err := validateApplicationSource(ctx, config, state, root.SafeRoot, root.Config); err != nil {
			root.RootReachable = false
			result.Status, result.Message = watchedroots.RunStatusBlocked, err.Error()
			if reportToMain {
				report := reportWatchedRootRun(ctx, store, config, state, instance, root, result, correlationID)
				result.MainReport = &report
			}
			return result, nil
		}
		plan := watchedroots.OutputPlan{RootKey: root.Config.RootKey, WorkerKey: instance.WorkerKey, GeneratedAt: time.Now().UTC()}
		if (result.Mode == watchedroots.RunStatusSkipped || result.InventoryUnchanged) && result.Checkpoint.OutputsCurrent {
			plan.Counts.AlreadyCurrent = result.Checkpoint.OutputsAlreadyCurrent
		} else {
			paths, scoped := watchedRootIncrementalOutputPaths(result)
			if scoped {
				plan, err = watchedroots.PlanOutputsForPaths(watchedStore, root, instance.WorkerKey, paths)
			} else {
				if result.Mode != watchedroots.RunStatusSkipped && !result.InventoryUnchanged {
					if err := watchedStore.SaveOutputAfter(root.Config.RootKey, result.Checkpoint, ""); err != nil {
						return result, err
					}
				}
				after, loadErr := watchedStore.LoadOutputAfter(root.Config.RootKey, result.Checkpoint)
				if loadErr != nil {
					return result, loadErr
				}
				plan, err = watchedroots.PlanOutputsPage(watchedStore, root, instance.WorkerKey, after, watchedRootOutputStatePage, watchedRootOutputBatchSize)
			}
			if err != nil {
				return watchedroots.ScanResult{}, err
			}
			morePages := plan.Page != nil && plan.Page.More
			result.Checkpoint.OutputsCurrent = len(plan.Actions) == 0 && !morePages
			result.Checkpoint.OutputsAlreadyCurrent = plan.Counts.AlreadyCurrent
			if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
				return watchedroots.ScanResult{}, err
			}
			if len(plan.Actions) > 0 {
				store, err = store.selectRecordTurn(root.Config.RootKey, plan.Actions)
				if err != nil {
					return result, err
				}
				store, err = store.beginSyncBatch()
				if err != nil {
					return result, err
				}
			}
			plan = applyWatchedRootOutputPlan(ctx, store, config, state, watchedStore, root, plan)
			// A bounded batch with more unstaged actions is useful continuation,
			// unlike a failed transport or unavailable source which needs backoff.
			result.Checkpoint.OutputContinuationReady = plan.Counts.Deferred > 0 && plan.Counts.Failed == 0 && ctx.Err() == nil
			if morePages && watchedRootOutputPlanComplete(plan, true) && ctx.Err() == nil {
				// The queue/path redo has committed. A crash before this cursor save
				// only rechecks this page; it cannot skip an unstaged action.
				if err := watchedStore.SaveOutputAfter(root.Config.RootKey, result.Checkpoint, plan.Page.Next); err != nil {
					return result, err
				}
				result.Checkpoint.OutputContinuationReady = true
			}
			if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
				return result, err
			}
			if !morePages && !result.Checkpoint.OutputsCurrent && watchedRootOutputPlanComplete(plan, scoped) {
				result.Checkpoint.OutputsCurrent = true
				if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
					return result, err
				}
			}
		}
		result.OutputPlan = &plan
		for _, action := range plan.Actions {
			if action.ReasonCode == "queue_batch_commit_failed" || action.ReasonCode == "queue_batch_failed" {
				// A partially installed redo record must recover under a fresh lock
				// before any delivery or acknowledgement can modify its snapshots.
				return result, fmt.Errorf("watched-root queue batch unavailable: %s", action.Error)
			}
		}
		if plan.Counts.Failed > 0 && result.Status == watchedroots.RunStatusHealthy {
			result.Status = watchedroots.RunStatusDegraded
			result.Message = "watched-root scan completed with output queue failures"
		}
		if flushOutputs {
			// Admit a new root generation before sending its metadata stream. The
			// normal end-of-run report still records delivery truth; unchanged
			// generations do not need a second report.
			if reportToMain && result.Checkpoint.MainReportedConfigHash != root.ConfigHash {
				report := reportWatchedRootRun(ctx, store, config, state, instance, root, result, correlationID)
				result.MainReport = &report
				if report.Status == watchedroots.OutputStatusRecorded {
					result.Checkpoint.MainReportedConfigHash = root.ConfigHash
					if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
						return result, err
					}
				}
			}
			flush := flushWatchedRootOutputs(ctx, store, config, state, correlationID, root.Config.RootKey)
			result.OutputFlush = &flush
			if err := store.commitSyncBatch(); err != nil {
				return result, err
			}
			if flush.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
				result.Status = watchedroots.RunStatusDegraded
				result.Message = "watched-root scan completed with queued output still pending"
			}
			if flush.Status == watchedroots.OutputStatusFailed && result.Checkpoint.OutputContinuationReady {
				result.Checkpoint.OutputContinuationReady = false
				if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
					return result, err
				}
			}
			backupFlush := flushWatchedRootBackupsForRoot(ctx, store, config, state, correlationID, root.Config.RootKey)
			result.BackupFlush = &backupFlush
			if err := store.commitSyncBatch(); err != nil {
				return result, err
			}
			if backupFlush.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
				result.Status = watchedroots.RunStatusDegraded
				result.Message = "watched-root scan completed with queued backup still pending"
			}
			if backupFlush.Status == watchedroots.OutputStatusFailed && result.Checkpoint.OutputContinuationReady {
				result.Checkpoint.OutputContinuationReady = false
				if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
					return result, err
				}
			}
		}
		if backupStatus, err := store.backupStatusForWatchedRoot(config, state, root.Config.RootKey); err == nil {
			result.BackupStatus = backupStatus
		}
	}
	if store.syncBatch != nil {
		stats := store.syncBatch.stats
		stats.LockHeldMillis = time.Since(turnStarted).Milliseconds()
		data := map[string]int64{"snapshot_reads": int64(stats.SnapshotReads), "snapshot_writes": int64(stats.SnapshotWrites), "snapshot_bytes": stats.SnapshotBytes, "journal_bytes": stats.JournalBytes, "commit_pages": int64(stats.CommitPages), "commit_millis": stats.CommitMillis, "lock_held_millis": stats.LockHeldMillis, "delivery_millis": stats.DeliveryMillis}
		data["record_reads"], data["record_writes"], data["record_bytes"] = int64(stats.RecordReads), int64(stats.RecordWrites), stats.RecordBytes
		data["record_page_alloc_bytes"], data["record_write_calls"] = stats.RecordPageAllocBytes, stats.RecordWriteCalls
		data["record_index_reads"], data["record_index_build_rows"] = int64(stats.RecordIndexReads), int64(stats.RecordIndexBuildRows)
		result.Persistence = data
	}
	if reportToMain {
		report := reportWatchedRootRun(ctx, store, config, state, instance, root, result, correlationID)
		result.MainReport = &report
		if report.Status == watchedroots.OutputStatusRecorded && result.Checkpoint.RootKey != "" && result.Checkpoint.MainReportedConfigHash != root.ConfigHash {
			result.Checkpoint.MainReportedConfigHash = root.ConfigHash
			if err := watchedStore.SaveCheckpoint(root.Config.RootKey, result.Checkpoint); err != nil {
				return result, err
			}
		}
		if report.Status == watchedroots.OutputStatusFailed && result.Status == watchedroots.RunStatusHealthy {
			result.Status = watchedroots.RunStatusDegraded
			result.Message = "watched-root scan completed but main status report failed"
		}
	}
	return result, nil
}

func watchedRootIncrementalOutputPaths(result watchedroots.ScanResult) ([]string, bool) {
	if result.Mode != watchedroots.ScanModeDirty || !result.OutputsPreviouslyCurrent || result.Status == watchedroots.RunStatusBlocked ||
		result.Counts.Errors != 0 || result.Counts.BudgetExhausted != 0 || result.Counts.HashDeferred != 0 ||
		result.Checkpoint.PendingRescan || len(result.Checkpoint.DeferredHashPaths) != 0 || result.Checkpoint.FullScanID != "" ||
		len(result.ChangedPaths) == 0 || result.Counts.Changed != len(result.ChangedPaths) {
		return nil, false
	}
	paths := make([]string, 0, len(result.ChangedPaths))
	seen := make(map[string]bool, len(result.ChangedPaths))
	for _, change := range result.ChangedPaths {
		if change.RelativePath == "" || change.RelativePath == "." || change.RelativePath == "__rescan__" || seen[change.RelativePath] {
			return nil, false
		}
		seen[change.RelativePath] = true
		paths = append(paths, change.RelativePath)
	}
	return paths, true
}

func watchedRootOutputPlanSettled(plan watchedroots.OutputPlan) bool {
	return watchedRootOutputPlanComplete(plan, false)
}

func watchedRootOutputPlanComplete(plan watchedroots.OutputPlan, scoped bool) bool {
	if plan.Counts.Deferred > 0 {
		return false
	}
	terminalFailures := 0
	for _, action := range plan.Actions {
		// A scoped delta starts from globally settled planning. Once every new
		// action is durable, transport deadlines own delivery independently.
		if scoped && action.Status == watchedroots.OutputStatusQueued {
			switch action.ActionKind {
			case watchedroots.OutputActionFileObservation, watchedroots.OutputActionSyncObject, watchedroots.OutputActionSyncMetadata,
				watchedroots.OutputActionDeletionRequest, watchedroots.OutputActionBackupFile, watchedroots.OutputActionBackupMetadata, watchedroots.OutputActionBackupDeletionMarker:
				continue
			}
		}
		if action.ActionKind != watchedroots.OutputActionSkipped && action.ActionKind != watchedroots.OutputActionBackupSkipped {
			return false
		}
		switch action.ReasonCode {
		case watchedroots.ReasonSkippedTooLarge, watchedroots.FindingBackupFileTooLarge:
		default:
			// Missing hashes may be transient failures or stability deferrals.
			return false
		}
		if action.Status == watchedroots.OutputStatusFailed && action.ActionKind == watchedroots.OutputActionBackupSkipped && action.ReasonCode == watchedroots.FindingBackupFileTooLarge {
			// Keep the degraded finding visible without retrying unchanged policy.
			terminalFailures++
		} else if action.Status != watchedroots.OutputStatusSkipped {
			return false
		}
	}
	return plan.Counts.Failed == terminalFailures
}

func flushWatchedRootOutputs(ctx context.Context, store Store, config Config, state State, correlationID, rootKey string) watchedroots.OutputFlush {
	before, err := store.localSyncRootStatus(config, state, rootKey)
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
		if before.Counts.Failed > 0 || before.Counts.Conflicted > 0 {
			flush.Status = watchedroots.OutputStatusFailed
		}
		flush.PendingAfter = before.Counts.Pending
		flush.AcceptedAfter = before.Counts.Accepted
		flush.ConflictedAfter = before.Counts.Conflicted
		flush.FailedAfter = before.Counts.Failed
		return flush
	}
	push, err := pushLocalSyncRootOnce(ctx, store, config, state, correlationID, watchedRootOutputBatchSize, false, rootKey)
	if err != nil {
		flush.Status = watchedroots.OutputStatusFailed
		flush.Error = err.Error()
		if after, statusErr := store.localSyncRootStatus(config, state, rootKey); statusErr == nil {
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
	if push.LocalStatus.Counts.Failed > 0 || push.LocalStatus.Counts.Conflicted > 0 {
		flush.Status = watchedroots.OutputStatusFailed
	} else if push.LocalStatus.Counts.Pending > 0 {
		flush.Status = watchedroots.OutputStatusQueued
	} else {
		flush.Status = watchedroots.OutputStatusRecorded
	}
	return flush
}

func applyWatchedRootOutputPlan(ctx context.Context, store Store, config Config, state State, watchedStore watchedroots.Store, root watchedroots.ValidatedRoot, plan watchedroots.OutputPlan) watchedroots.OutputPlan {
	if len(plan.Actions) == 0 {
		return plan
	}
	if ctx.Err() != nil {
		plan.Counts.Deferred = len(plan.Actions)
		plan.Actions = nil
		return plan
	}
	store, unlock, err := store.lockLocalSync(ctx)
	if err == nil {
		defer unlock()
		store, _, err = store.joinSyncBatch()
	}
	if err != nil {
		for idx := range plan.Actions {
			markWatchedRootOutputFailed(&plan.Actions[idx], "queue_batch_failed", err.Error())
		}
		plan.Counts.Failed += len(plan.Actions)
		return plan
	}
	started := time.Now()
	queued := 0
	for idx := range plan.Actions {
		// Leave unstaged paths untouched so the next reconcile can plan them again.
		// Reserve time for delivery instead of holding the queue lock for a whole vault.
		if queued >= watchedRootOutputBatchSize || ctx.Err() != nil || time.Since(started) >= watchedRootPlanningBudget {
			plan.Counts.Deferred = len(plan.Actions) - idx
			plan.Actions = plan.Actions[:idx]
			break
		}
		action := &plan.Actions[idx]
		if action.ActionKind != watchedroots.OutputActionSkipped && action.ActionKind != watchedroots.OutputActionBackupSkipped {
			queued++
		}
		key := watchedroots.PathKey(action.RootKey, action.RelativePath)
		current, found := store.syncBatch.paths[key]
		var err error
		if !found {
			current, err = watchedStore.LoadPathState(action.RootKey, action.RelativePath)
		}
		if err != nil {
			markWatchedRootOutputFailed(action, "load_path_state_failed", err.Error())
			plan.Counts.Failed++
			continue
		}
		now := time.Now().UTC()
		current.LastOutputPlannedAt = &plan.GeneratedAt
		switch action.ActionKind {
		case watchedroots.OutputActionFileObservation:
			item, err := store.QueueWatchedRootFileObservation(state, *action)
			if err != nil {
				markWatchedRootOutputFailed(action, "queue_file_observation_failed", err.Error())
				plan.Counts.Failed++
			} else {
				current.LocalObjectID = action.LocalObjectID
				current.LastQueuedFileObservation = action.ObservationFingerprint
				action.LocalSyncOutboxID = item.LocalOutboxID
				action.Status = watchedroots.OutputStatusQueued
			}
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
				backupItem, err := localWatchedRootBackupItemForBatch(store, batch.LocalBatchID)
				if err != nil {
					markWatchedRootOutputFailed(action, "backup_item_lookup_failed", err.Error())
					plan.Counts.Failed++
					continue
				}
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
		store.syncBatch.paths[key] = current
	}
	if err := store.commitSyncBatch(); err != nil {
		for idx := range plan.Actions {
			markWatchedRootOutputFailed(&plan.Actions[idx], "queue_batch_commit_failed", err.Error())
		}
		plan.Counts.Failed += len(plan.Actions)
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

func localWatchedRootBackupItemForBatch(store Store, localBatchID string) (LocalWatchedRootBackupItem, error) {
	var items []LocalWatchedRootBackupItem
	var err error
	if store.queueDB != nil {
		items, err = readBackupRecordSelection[LocalWatchedRootBackupItem](store, "backup/items", "backup-batch\x00"+localBatchID+"\x00", nil, 1)
	} else {
		items, err = store.LoadWatchedRootBackupItems()
	}
	if err != nil {
		return LocalWatchedRootBackupItem{}, err
	}
	for _, item := range items {
		if item.LocalBatchID == localBatchID {
			return item, nil
		}
	}
	return LocalWatchedRootBackupItem{}, fmt.Errorf("backup item unavailable for batch %s", localBatchID)
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
