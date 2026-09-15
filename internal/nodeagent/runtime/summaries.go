package runtime

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s Store) BuildLocalSummary() (LocalSummary, error) {
	instances, err := s.LoadInstances()
	if err != nil {
		return LocalSummary{}, err
	}
	workerCounts := WorkerCounts{Total: len(instances)}
	for _, instance := range instances {
		if !instance.Enabled {
			workerCounts.Paused++
			continue
		}
		health, err := s.LoadHealth(instance.WorkerKey)
		if errors.Is(err, fs.ErrNotExist) {
			workerCounts.Degraded++
			continue
		}
		if err != nil {
			return LocalSummary{}, err
		}
		switch health.Status {
		case WorkerStatusHealthy:
			workerCounts.Healthy++
		case WorkerStatusBlocked:
			workerCounts.Blocked++
		case WorkerStatusRequiresManualAction:
			workerCounts.ManualAction++
		case WorkerStatusPaused:
			workerCounts.Paused++
		default:
			workerCounts.Degraded++
		}
	}

	outbox, err := s.OutboxSummary()
	if err != nil {
		return LocalSummary{}, err
	}
	inbox, err := s.InboxSummary()
	if err != nil {
		return LocalSummary{}, err
	}
	legacyOutbox, err := countLegacyJSONFiles(filepath.Join(s.DataDir, "outbox"))
	if err != nil {
		return LocalSummary{}, err
	}
	syncCounts, err := s.localSyncCounts()
	if err != nil {
		return LocalSummary{}, err
	}
	backupCounts, err := s.localWatchedRootBackupCounts()
	if err != nil {
		return LocalSummary{}, err
	}
	queues := QueueCounts{
		InboxPending:        inbox.Counts[OutboxStatusPending],
		InboxDone:           inbox.Counts[OutboxStatusDone],
		InboxFailed:         inbox.Counts[OutboxStatusFailed],
		OutboxPending:       outbox.Counts[OutboxStatusPending],
		OutboxInflight:      outbox.Counts[OutboxStatusInflight],
		OutboxDone:          outbox.Counts[OutboxStatusDone],
		OutboxFailed:        outbox.Counts[OutboxStatusFailed],
		OutboxManualAction:  outbox.Counts[OutboxStatusManualAction],
		LegacyInboxRecords:  inbox.LegacyRecords,
		LegacyOutboxRecords: legacyOutbox,
		SyncPending:         syncCounts.Pending,
		SyncFailed:          syncCounts.Failed,
		SyncConflicted:      syncCounts.Conflicted,
		BackupPending:       backupCounts.Pending,
		BackupRetryable:     backupCounts.Retryable,
		BackupFailed:        backupCounts.Failed,
		BackupManualAction:  backupCounts.ManualAction,
		BackupPendingBytes:  backupCounts.PendingBytes,
		PendingBytes:        outbox.TotalPendingBytes,
	}
	summary := LocalSummary{
		GeneratedAt: time.Now().UTC(),
		Workers:     workerCounts,
		Queues:      queues,
		OldestPending: OldestPending{
			Outbox: outbox.OldestPendingAt,
		},
		HighestStatus: highestStatus(workerCounts, queues),
	}
	return summary, nil
}

type syncStatusCounts struct {
	Pending    int
	Failed     int
	Conflicted int
}

func (s Store) localSyncCounts() (syncStatusCounts, error) {
	path := filepath.Join(s.DataDir, "sync", "outbox.json")
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return syncStatusCounts{}, nil
	}
	if err != nil {
		return syncStatusCounts{}, err
	}
	defer file.Close()
	var items []struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(file).Decode(&items); err != nil {
		return syncStatusCounts{}, err
	}
	var counts syncStatusCounts
	for _, item := range items {
		switch item.Status {
		case "pending":
			counts.Pending++
		case "failed":
			counts.Failed++
		case "conflicted":
			counts.Conflicted++
		}
	}
	return counts, nil
}

type watchedRootBackupStatusCounts struct {
	Pending      int
	Retryable    int
	Failed       int
	ManualAction int
	PendingBytes int64
}

func (s Store) localWatchedRootBackupCounts() (watchedRootBackupStatusCounts, error) {
	root := filepath.Join(s.DataDir, "watched-root-backups")
	outboxPath := filepath.Join(root, "outbox.json")
	file, err := os.Open(outboxPath)
	if errors.Is(err, fs.ErrNotExist) {
		return watchedRootBackupStatusCounts{}, nil
	}
	if err != nil {
		return watchedRootBackupStatusCounts{}, err
	}
	defer file.Close()

	var outbox []struct {
		LocalRef         string `json:"local_ref"`
		Status           string `json:"status"`
		LastErrorCode    string `json:"last_error_code,omitempty"`
		LastErrorMessage string `json:"last_error_message,omitempty"`
	}
	if err := json.NewDecoder(file).Decode(&outbox); err != nil {
		return watchedRootBackupStatusCounts{}, err
	}

	batchBytes := map[string]int64{}
	batchesPath := filepath.Join(root, "batches.json")
	batchesFile, err := os.Open(batchesPath)
	if err == nil {
		defer batchesFile.Close()
		var batches []struct {
			LocalBatchID string `json:"local_batch_id"`
			TotalBytes   int64  `json:"total_bytes"`
		}
		if err := json.NewDecoder(batchesFile).Decode(&batches); err != nil {
			return watchedRootBackupStatusCounts{}, err
		}
		for _, batch := range batches {
			batchBytes[batch.LocalBatchID] = batch.TotalBytes
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return watchedRootBackupStatusCounts{}, err
	}

	var counts watchedRootBackupStatusCounts
	for _, item := range outbox {
		switch item.Status {
		case "failed":
			counts.Failed++
			counts.ManualAction++
		case "accepted", "duplicate", "conflicted":
			continue
		default:
			counts.Pending++
			if strings.TrimSpace(item.LastErrorCode) != "" || strings.TrimSpace(item.LastErrorMessage) != "" {
				counts.Retryable++
			}
			counts.PendingBytes += batchBytes[item.LocalRef]
		}
	}
	return counts, nil
}

func highestStatus(workers WorkerCounts, queues QueueCounts) string {
	if workers.ManualAction > 0 || queues.OutboxManualAction > 0 || queues.BackupManualAction > 0 {
		return WorkerStatusRequiresManualAction
	}
	if workers.Blocked > 0 {
		return WorkerStatusBlocked
	}
	if workers.Degraded > 0 || queues.OutboxFailed > 0 || queues.SyncFailed > 0 || queues.SyncConflicted > 0 || queues.BackupRetryable > 0 {
		return WorkerStatusDegraded
	}
	return WorkerStatusHealthy
}
