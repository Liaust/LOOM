package nodeagent

import (
	"bytes"
	"os"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestDeliverySummaryReadsOnlyCountersWithoutWriterRepair(t *testing.T) {
	s, config, state := queueReportingFixture(t)
	if err := s.withRecordAccess(func(Store) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.queueRecordsPath())
	if err := s.withQueueSummary(t.Context(), func(tx *bolt.Tx) error {
		if tx.Writable() {
			t.Fatal("observer opened writer transaction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.rootHasQueuedDelivery(t.Context(), "notes"); err != nil || !pending {
		t.Fatal("pending backup disappeared", pending, err)
	}
	if pending, err := s.rootHasQueuedDelivery(t.Context(), "unrelated"); err != nil || pending {
		t.Fatal("other root inherited pending backup", pending, err)
	}
	now := time.Now().UTC()
	if next, err := watchedRootDeliveryDeadline(s, config, state, "notes", now.Add(time.Hour), now, time.Hour); err != nil || !next.Equal(now.Add(time.Minute)) {
		t.Fatal(next, err)
	}
	after, _ := os.ReadFile(s.queueRecordsPath())
	if !bytes.Equal(before, after) {
		t.Fatal("summary observer modified database")
	}
	locked, release, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
		// A detached reader must not decode this unrelated historical row.
		if err := recordBucket(tx, "backup/items", "rows").Put([]byte("corrupt-history"), []byte("broken")); err != nil {
			return err
		}
		return carryQueueReport(tx)
	}); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.rootHasQueuedDelivery(t.Context(), "notes"); err != nil || !pending {
		t.Fatal("summary decoded retained history", pending, err)
	}
	if err := os.WriteFile(s.syncBatchJournalPath(), []byte("pending redo"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rootHasQueuedDelivery(t.Context(), "notes"); err == nil {
		t.Fatal("incomplete recovery reported healthy summary")
	}
}

func TestBackupItemLookupUsesExactBatchWithoutHistoryReads(t *testing.T) {
	s, _, _, original := backupSelectionHistory(t, 128)
	locked, release, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	batch, err := locked.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	item, err := localWatchedRootBackupItemForBatch(batch, original.items[70].LocalBatchID)
	if err != nil || item.LocalItemID != original.items[70].LocalItemID || batch.syncBatch.stats.RecordReads != 1 || batch.syncBatch.stats.RecordIndexBuildRows != 0 {
		t.Fatal(item, batch.syncBatch.stats, err)
	}
	if _, err := localWatchedRootBackupItemForBatch(batch, "missing"); err == nil {
		t.Fatal("missing batch silently returned an empty item")
	}
}
