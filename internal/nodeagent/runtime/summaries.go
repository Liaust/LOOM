package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrQueueRecordsAbsent = errors.New("queue record store absent")

func (s Store) BuildLocalSummary() (LocalSummary, error) {
	return s.BuildLocalSummaryContext(context.Background())
}

func (s Store) BuildLocalSummaryContext(ctx context.Context) (LocalSummary, error) {
	summary, _, err := s.buildLocalSummary(ctx)
	return summary, err
}

func (s Store) buildLocalSummary(ctx context.Context) (LocalSummary, fileQueueSnapshot, error) {
	previous, _ := s.LoadLatestSummary()
	now := time.Now().UTC()
	instances, err := s.LoadInstances()
	if err != nil {
		return LocalSummary{}, fileQueueSnapshot{}, err
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
			return LocalSummary{}, fileQueueSnapshot{}, err
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

	files, fileErr := s.fileQueueSummary()
	outbox, inbox := files.Outbox, files.Inbox
	var snapshot QueueSnapshot
	var queueErr error
	if previous.QueueUnavailableReason != "" && previous.RetryAt != nil && now.Before(*previous.RetryAt) {
		queueErr = errors.New(previous.QueueUnavailableReason)
	} else {
		snapshot, queueErr = s.queueSnapshot(ctx)
	}
	if queueErr != nil {
		snapshot = QueueSnapshot{Revision: previous.QueueRevision,
			SyncPending: previous.Queues.SyncPending, SyncFailed: previous.Queues.SyncFailed, SyncConflicted: previous.Queues.SyncConflicted,
			BackupPending: previous.Queues.BackupPending, BackupRetryable: previous.Queues.BackupRetryable,
			BackupFailed: previous.Queues.BackupFailed, BackupManualAction: previous.Queues.BackupManualAction, BackupPendingBytes: previous.Queues.BackupPendingBytes}
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
		LegacyOutboxRecords: files.LegacyOutbox,
		SyncPending:         snapshot.SyncPending,
		SyncFailed:          snapshot.SyncFailed,
		SyncConflicted:      snapshot.SyncConflicted,
		BackupPending:       snapshot.BackupPending,
		BackupRetryable:     snapshot.BackupRetryable,
		BackupFailed:        snapshot.BackupFailed,
		BackupManualAction:  snapshot.BackupManualAction,
		BackupPendingBytes:  snapshot.BackupPendingBytes,
		PendingBytes:        outbox.TotalPendingBytes,
	}
	summary := LocalSummary{
		GeneratedAt:     now,
		CheckedAt:       now,
		Availability:    "available",
		QueueRevision:   snapshot.Revision,
		QueueObservedAt: now,
		FileRevision:    files.Revision,
		Workers:         workerCounts,
		Queues:          queues,
		OldestPending: OldestPending{
			Outbox: outbox.OldestPendingAt,
		},
		HighestStatus: highestStatus(workerCounts, queues),
	}
	if queueErr != nil {
		summary.QueueObservedAt, summary.QueueUnavailableReason = previous.QueueObservedAt, queueErr.Error()
	}
	if unavailable := errors.Join(queueErr, fileErr); unavailable != nil {
		summary.Availability = "unavailable"
		if (queueErr == nil || !previous.QueueObservedAt.IsZero()) && (fileErr == nil || files.Revision != 0) {
			summary.Availability = "stale"
		}
		summary.UnavailableReason = unavailable.Error()
		retry := now.Add(30 * time.Second)
		if errors.Is(fileErr, errFileSummaryRebuilding) {
			retry = now.Add(time.Second)
		}
		if previous.RetryAt != nil && now.Before(*previous.RetryAt) {
			retry = *previous.RetryAt
		}
		summary.RetryAt = &retry
		if summary.HighestStatus == WorkerStatusHealthy {
			summary.HighestStatus = WorkerStatusDegraded
		}
	}
	if sameSummaryMaterial(previous, summary) {
		summary.GeneratedAt = previous.GeneratedAt
	}
	if err := s.SaveLatestSummary(summary); err != nil {
		return LocalSummary{}, files, err
	}
	return summary, files, nil
}

func (s Store) queueSnapshot(ctx context.Context) (QueueSnapshot, error) {
	if s.ReadQueueSnapshot != nil {
		snapshot, err := s.ReadQueueSnapshot(ctx)
		if !errors.Is(err, ErrQueueRecordsAbsent) {
			return snapshot, err
		}
	} else if _, err := os.Lstat(filepath.Join(s.DataDir, "sync", "records.db")); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			return QueueSnapshot{}, err
		}
		return QueueSnapshot{}, errors.New("queue owner summary reader unavailable")
	}
	sync, err := s.localSyncCounts()
	if err != nil {
		return QueueSnapshot{}, err
	}
	backup, err := s.localWatchedRootBackupCounts()
	return QueueSnapshot{SyncPending: sync.Pending, SyncFailed: sync.Failed, SyncConflicted: sync.Conflicted,
		BackupPending: backup.Pending, BackupRetryable: backup.Retryable, BackupFailed: backup.Failed,
		BackupManualAction: backup.ManualAction, BackupPendingBytes: backup.PendingBytes}, err
}

type syncStatusCounts struct {
	Pending    int
	Failed     int
	Conflicted int
}

func (s Store) localSyncCounts() (syncStatusCounts, error) {
	var counts syncStatusCounts
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
	var counts watchedRootBackupStatusCounts
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
