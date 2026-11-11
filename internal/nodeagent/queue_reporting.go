package nodeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
)

// A derived byte total is qualified by the bbolt transaction, so a crash or an
// older writer cannot leave a clean-looking total. Revision ignores no-op txs.
type queueReportState struct {
	Transaction  int    `json:"transaction"`
	Revision     uint64 `json:"revision"`
	PendingBytes int64  `json:"pending_bytes"`
}

var queueReportKey = []byte("queue-report-v1")

func readQueueReport(tx *bolt.Tx) (queueReportState, bool) {
	var state queueReportState
	err := json.Unmarshal(tx.Bucket([]byte("meta")).Get(queueReportKey), &state)
	return state, err == nil && (state.Transaction == tx.ID() || tx.Writable() && state.Transaction == tx.ID()-1)
}

func saveQueueReport(tx *bolt.Tx, state queueReportState) error {
	state.Transaction = tx.ID()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return tx.Bucket([]byte("meta")).Put(queueReportKey, raw)
}

func carryQueueReport(tx *bolt.Tx) error {
	state, current := readQueueReport(tx)
	if !current {
		return nil
	}
	return saveQueueReport(tx, state)
}

func pendingBackupBytes(tx *bolt.Tx, raw []byte) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	var item LocalWatchedRootBackupOutboxItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return 0, err
	}
	_, counts, _ := backupContribution("backup/outbox", raw)
	if counts.Pending == 0 {
		return 0, nil
	}
	var batch LocalWatchedRootBackupBatch
	if raw := recordBucket(tx, "backup/batches", "rows").Get([]byte(item.LocalRef)); raw != nil {
		if err := json.Unmarshal(raw, &batch); err != nil {
			return 0, err
		}
	}
	return batch.TotalBytes, nil
}

// Called before changing a row. Batch edits visit only that batch's deliveries.
func updateQueueReport(tx *bolt.Tx, collection, id string, old, raw []byte) error {
	state, current := readQueueReport(tx)
	if !current {
		return nil
	}
	changed := false
	switch collection {
	case "sync/outbox", "backup/outbox":
		before, err := queueReportContribution(tx, collection, old)
		if err != nil {
			return err
		}
		after, err := queueReportContribution(tx, collection, raw)
		if err != nil {
			return err
		}
		state.PendingBytes += after.BackupPendingBytes - before.BackupPendingBytes
		changed = before != after
	case "backup/batches":
		previousBytes := state.PendingBytes
		var before, after LocalWatchedRootBackupBatch
		if len(old) > 0 {
			if err := json.Unmarshal(old, &before); err != nil {
				return err
			}
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &after); err != nil {
				return err
			}
		}
		prefix := []byte("ref\x00" + id + "\x00")
		cursor := recordBucket(tx, "backup/outbox", "index").Cursor()
		for k, key := cursor.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, key = cursor.Next() {
			_, counts, _ := backupContribution("backup/outbox", recordBucket(tx, "backup/outbox", "rows").Get(key))
			state.PendingBytes += int64(counts.Pending) * (after.TotalBytes - before.TotalBytes)
		}
		changed = previousBytes != state.PendingBytes
	}
	if changed {
		state.Revision++
	}
	// Keep the transaction watermark current even when only source cursors,
	// retained history or retry timestamps changed, without republishing status.
	return saveQueueReport(tx, state)
}

func queueReportContribution(tx *bolt.Tx, collection string, raw []byte) (out noderuntime.QueueSnapshot, err error) {
	if len(raw) == 0 {
		return
	}
	if collection == "sync/outbox" {
		var item LocalSyncOutboxItem
		if err = json.Unmarshal(raw, &item); err != nil {
			return
		}
		var counts LocalSyncCounts
		syncCountDelta(&counts, item.Status, 1)
		out.SyncPending, out.SyncFailed, out.SyncConflicted = counts.Pending, counts.Failed, counts.Conflicted
		return
	}
	_, counts, _ := backupContribution(collection, raw)
	out.BackupPending, out.BackupRetryable, out.BackupFailed, out.BackupManualAction = counts.Pending, counts.Retryable, counts.Failed, counts.ManualAction
	out.BackupPendingBytes, err = pendingBackupBytes(tx, raw)
	return
}

func ensureQueueReport(db *bolt.DB) error {
	var current bool
	if err := db.View(func(tx *bolt.Tx) error { _, current = readQueueReport(tx); return nil }); err != nil || current {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		prior, _ := readQueueReport(tx)
		state := queueReportState{Revision: prior.Revision + 1}
		prefix := []byte("pending-all\x00")
		cursor := recordBucket(tx, "backup/outbox", "index").Cursor()
		for key, id := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, id = cursor.Next() {
			size, err := pendingBackupBytes(tx, recordBucket(tx, "backup/outbox", "rows").Get(id))
			if err != nil {
				return err
			}
			state.PendingBytes += size
		}
		if err := carryBackupSelectionWatermark(tx); err != nil {
			return err
		}
		return saveQueueReport(tx, state)
	})
}

func (s Store) runtimeStore() noderuntime.Store {
	runtime := noderuntime.NewStore(s.DataDir)
	runtime.ReadQueueSnapshot = s.readQueueSnapshot
	return runtime
}

// Observers share the writer's execution lock but do not open a writer, load its
// freelist or replay/upgrade anything. Incomplete owner state is unknown, not idle.
func (s Store) withQueueSummary(ctx context.Context, read func(*bolt.Tx) error) error {
	if s.queueDB == nil {
		wait, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancel()
		unlock, err := noderuntime.NewStore(s.DataDir).AcquireWorkerExecutionLock(wait, "node-agent.shared-sync-queue")
		if err != nil {
			return fmt.Errorf("queue owner unavailable: %w", err)
		}
		defer unlock()
		info, err := os.Lstat(s.queueRecordsPath())
		if os.IsNotExist(err) {
			return noderuntime.ErrQueueRecordsAbsent
		}
		if err != nil {
			return err
		}
		if !validQueueRecordFile(info) {
			return errors.New("invalid queue record store file")
		}
		if _, err := os.Stat(s.syncBatchJournalPath()); err == nil {
			return errors.New("queue owner recovery pending")
		} else if !os.IsNotExist(err) {
			return err
		}
		db, err := bolt.Open(s.queueRecordsPath(), 0600, &bolt.Options{ReadOnly: true, Timeout: time.Millisecond})
		if err != nil {
			return err
		}
		defer db.Close()
		s.queueDB = db
	}
	return s.queueDB.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		if meta == nil || string(meta.Get([]byte("format"))) != queueRecordsFormat {
			return errors.New("queue record summary requires current owner format")
		}
		for _, name := range []string{"counts", "backup-summary", "backup/outbox/index", "backup/outbox/rows", "backup/batches/rows"} {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("queue record counters unavailable: %s", name)
			}
		}
		return read(tx)
	})
}

func (s Store) readQueueSnapshot(ctx context.Context) (snapshot noderuntime.QueueSnapshot, err error) {
	// The actual writer may refresh its derived report; detached observers never
	// turn status/scheduling into a repair transaction.
	if s.queueDB != nil && !s.queueDB.IsReadOnly() {
		if err := ensureQueueReport(s.queueDB); err != nil {
			return snapshot, err
		}
	}
	err = s.withQueueSummary(ctx, func(tx *bolt.Tx) error {
		state, current := readQueueReport(tx)
		if !current {
			return errors.New("queue record summary revision unavailable")
		}
		snapshot.Revision, snapshot.BackupPendingBytes = state.Revision, state.PendingBytes
		prefix := []byte("sync/outbox\x00")
		cursor := tx.Bucket([]byte("counts")).Cursor()
		for key, raw := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, raw = cursor.Next() {
			var counts LocalSyncCounts
			if err := json.Unmarshal(raw, &counts); err != nil {
				return err
			}
			snapshot.SyncPending += counts.Pending
			snapshot.SyncFailed += counts.Failed
			snapshot.SyncConflicted += counts.Conflicted
		}
		return tx.Bucket([]byte("backup-summary")).ForEach(func(_, raw []byte) error {
			var counts LocalWatchedRootBackupCounts
			if err := json.Unmarshal(raw, &counts); err != nil {
				return err
			}
			snapshot.BackupPending += counts.Pending
			snapshot.BackupRetryable += counts.Retryable
			snapshot.BackupFailed += counts.Failed
			snapshot.BackupManualAction += counts.ManualAction
			return nil
		})
	})
	return snapshot, err
}
