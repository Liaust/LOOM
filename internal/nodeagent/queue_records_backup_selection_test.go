package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

// The retained maximum deliberately precedes the final imported row. Selection
// must preserve both original array order and the global sequence high-water.
func backupSelectionHistory(t *testing.T, count int) (Store, Config, State, watchedRootBackupQueue) {
	t.Helper()
	s := Store{DataDir: t.TempDir()}
	config := Config{NodeKey: "fixture"}
	state := State{NodeID: "fixture-node", CredentialToken: "fixture-token"}
	var q watchedRootBackupQueue
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("history-%03d", i)
		root := "notes"
		if i%2 == 0 {
			root = "other"
		}
		seq := int64(i + 1)
		if i == 0 {
			seq = 9000
		}
		q.artifacts = append(q.artifacts, LocalWatchedRootBackupArtifact{LocalArtifactID: id, RootKey: root, RelativePath: id, Status: "accepted"})
		q.batches = append(q.batches, LocalWatchedRootBackupBatch{LocalBatchID: id, RootKey: root, LocalSequence: seq, ItemCount: 1, Status: "accepted"})
		q.items = append(q.items, LocalWatchedRootBackupItem{LocalItemID: id, LocalBatchID: id, LocalArtifactID: id, RootKey: root, RelativePath: id, Status: "accepted"})
		q.outbox = append(q.outbox, LocalWatchedRootBackupOutboxItem{LocalOutboxID: id, LocalRef: id, RootKey: root, LocalSequence: seq, Status: "accepted"})
	}
	for _, save := range []func() error{
		func() error { return s.SaveWatchedRootBackupArtifacts(q.artifacts) },
		func() error { return s.SaveWatchedRootBackupBatches(q.batches) },
		func() error { return s.SaveWatchedRootBackupItems(q.items) },
		func() error { return s.SaveWatchedRootBackupOutbox(q.outbox) },
	} {
		if err := save(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Measure the one-time derived-key build separately from steady-state turns.
	if err := s.withRecordAccess(func(locked Store) error {
		locked, err := locked.beginSyncBatch()
		if err != nil {
			return err
		}
		if err := locked.ensureBackupSelectionIndexes(); err != nil {
			return err
		}
		if locked.syncBatch.stats.RecordIndexBuildRows != count*2 {
			return fmt.Errorf("index build rows=%d, want %d", locked.syncBatch.stats.RecordIndexBuildRows, count*2)
		}
		t.Logf("derived index initialization: %+v", locked.syncBatch.stats)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// RawMessage's JSON null has an explicit representation after decoding.
	var err error
	q, err = s.loadWatchedRootBackupQueue()
	if err != nil {
		t.Fatal(err)
	}
	return s, config, state, q
}

func backupMetadataAction(root, path string) watchedroots.OutputAction {
	return watchedroots.OutputAction{ActionKind: watchedroots.OutputActionBackupMetadata, RootKey: root, RelativePath: path, BackupMode: watchedroots.BackupModeMetadataOnly, BackupItemKind: mainwatchedroots.BackupItemKindDirectory}
}

func TestQueueRecordsBackupTargetedReadsAndHistory(t *testing.T) {
	for _, history := range []int{0, 128} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			s, config, state, original := backupSelectionHistory(t, history)
			locked, unlock, err := s.lockLocalSync(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			locked, err = locked.beginSyncBatch()
			if err != nil {
				t.Fatal(err)
			}
			first, out, err := locked.queueWatchedRootBackupActionLocked(config, state, backupMetadataAction("notes", "first"), "")
			if err != nil {
				t.Fatal(err)
			}
			wantSequence := int64(1)
			if history > 0 {
				wantSequence = 9001
			}
			if first.LocalSequence != wantSequence {
				t.Fatalf("partial-history sequence: %d", first.LocalSequence)
			}
			duplicate, dupOut, err := locked.queueWatchedRootBackupActionLocked(config, state, backupMetadataAction("notes", "first"), "")
			if err != nil || duplicate.LocalBatchID != first.LocalBatchID || dupOut.LocalOutboxID != out.LocalOutboxID {
				t.Fatal("staged duplicate not found", err)
			}
			second, _, err := locked.queueWatchedRootBackupActionLocked(config, state, backupMetadataAction("notes", "second"), "")
			if err != nil || second.LocalSequence != wantSequence+1 {
				t.Fatal("staged global sequence lost", err)
			}
			if locked.syncBatch.stats.RecordReads != 0 || locked.syncBatch.stats.RecordIndexReads > 2 {
				t.Fatalf("producer read unrelated history: %+v", locked.syncBatch.stats)
			}
			if err := locked.commitSyncBatch(); err != nil {
				t.Fatal(err)
			}
			t.Logf("history=%d producer: %+v", history, locked.syncBatch.stats)
			if err := unlock(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.QueueWatchedRootBackupAction(config, state, backupMetadataAction("other", "pending-elsewhere"), ""); err != nil {
				t.Fatal(err)
			}
			counts, err := s.backupDeliveryCountsByRoot()
			if err != nil || counts["notes"].Pending != 2 || counts["other"].Pending != 1 {
				t.Fatal("scheduler lost eligible work", counts, err)
			}
			var received []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input mainwatchedroots.BackupBatchInput
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				received = append(received, input.Items[0].RelativePath)
				_ = json.NewEncoder(w).Encode(response.Success("test", mainwatchedroots.BackupBatchResult{
					Batch: mainwatchedroots.BackupBatch{WatchedRootBackupBatchID: "main-" + input.Items[0].LocalItemRef, Status: mainwatchedroots.BackupBatchStatusAccepted},
					Items: []mainwatchedroots.BackupItem{{LocalItemRef: input.Items[0].LocalItemRef, WatchedRootBackupItemID: "accepted", Status: mainwatchedroots.BackupItemStatusAccepted}},
				}))
			}))
			defer server.Close()
			config.MainURL = server.URL
			for turn := 0; turn < 2; turn++ {
				run, err := pushLocalWatchedRootBackupsForRoot(t.Context(), s, config, state, "test", "notes", 1)
				if err != nil || run.SubmittedItems != 1 || run.LocalStatus.Counts.Pending != 1-turn {
					t.Fatalf("page failed: %+v %v", run, err)
				}
				if run.Persistence.RecordReads > 18 || run.Persistence.RecordIndexReads > 5 || run.Persistence.RecordWrites != 3 || run.Persistence.SnapshotReads != 0 {
					t.Fatalf("delivery read unrelated history: %+v", run.Persistence)
				}
				t.Logf("history=%d delivery turn=%d: %+v", history, turn, run.Persistence)
			}
			if !reflect.DeepEqual(received, []string{"first", "second"}) {
				t.Fatal("root/order lost", received)
			}
			all, err := s.loadWatchedRootBackupQueue()
			if err != nil || len(all.outbox) != history+3 || len(all.items) != history+3 || len(all.batches) != history+3 || len(all.artifacts) != history {
				t.Fatal("history truncated", err)
			}
			if !reflect.DeepEqual(all.artifacts, original.artifacts) && history > 0 {
				t.Fatal("artifact history changed")
			}
			if history > 0 && (!reflect.DeepEqual(all.batches[:history], original.batches) || !reflect.DeepEqual(all.items[:history], original.items) || !reflect.DeepEqual(all.outbox[:history], original.outbox)) {
				t.Fatal("retained history/order changed")
			}
			if all.outbox[len(all.outbox)-1].Status != "pending" {
				t.Fatal("unrelated root consumed")
			}
		})
	}
}

func TestQueueRecordsBackupSelectionOverlayAndRedo(t *testing.T) {
	s, config, state, _ := backupSelectionHistory(t, 3)
	batch, out, err := s.QueueWatchedRootBackupAction(config, state, backupMetadataAction("notes", "redo"), "")
	if err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	locked, err = locked.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	q, err := locked.loadWatchedRootBackupBatch(out)
	if err != nil || len(q.items) != 1 {
		t.Fatal(q, err)
	}
	q.items[0].LastErrorCode = "retained-error"
	if err := locked.saveBackupQueueSelection(locked.watchedRootBackupItemsPath(), q.items); err != nil {
		t.Fatal(err)
	}
	// Full compatibility reads in the same page must include selected writes.
	all, err := locked.LoadWatchedRootBackupItems()
	if err != nil || len(all) != 4 || all[3].LastErrorCode != "retained-error" {
		t.Fatal("full reader missed staged change", all, err)
	}
	if err := locked.SaveWatchedRootBackupItems(all); err != nil {
		t.Fatal(err)
	}
	// Persist the existing version-3 redo without applying it, as at a crash.
	if err := writeDurableQueueJSON(locked.syncBatchJournalPath(), queueBatchJournal{Version: 3, Records: locked.syncBatch.recordChanges}); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err = s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	q, err = locked.loadWatchedRootBackupBatch(out)
	if err != nil || len(q.items) != 1 || q.items[0].LastErrorCode != "retained-error" || q.batches[0].LocalBatchID != batch.LocalBatchID {
		t.Fatal("selected redo not recovered", q, err)
	}
}

func TestQueueRecordsBackupSelectionDerivedIndexRebuild(t *testing.T) {
	s, _, _, original := backupSelectionHistory(t, 3)
	var before map[string][]byte
	if err := s.withRecordAccess(func(locked Store) error {
		before = map[string][]byte{}
		if err := locked.queueDB.View(func(tx *bolt.Tx) error {
			for _, c := range []string{"backup/items", "backup/outbox"} {
				if err := recordBucket(tx, c, "rows").ForEach(func(id, raw []byte) error { before[c+string(id)] = bytes.Clone(raw); return nil }); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
		// A transaction from an older writer leaves a stale derived-index watermark.
		return locked.queueDB.Update(func(tx *bolt.Tx) error {
			return recordBucket(tx, "backup/outbox", "index").Put([]byte("backup-ready-all\x00orphan"), []byte(original.outbox[0].LocalOutboxID))
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.withRecordAccess(func(locked Store) error {
		pending, err := locked.pendingWatchedRootBackupOutbox("", 1)
		if err != nil || len(pending) != 0 {
			return fmt.Errorf("stale ready key survived: %v %v", pending, err)
		}
		next, err := locked.nextBackupRecordSequence()
		if err != nil || next != 9001 {
			return fmt.Errorf("maximum not rebuilt: %d %v", next, err)
		}
		return locked.queueDB.View(func(tx *bolt.Tx) error {
			for _, c := range []string{"backup/items", "backup/outbox"} {
				if err := recordBucket(tx, c, "rows").ForEach(func(id, raw []byte) error {
					if !bytes.Equal(before[c+string(id)], raw) {
						return fmt.Errorf("rebuild changed a retained row")
					}
					return nil
				}); err != nil {
					return err
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueueRecordsBackupPendingOrderAndStagedTransitions(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	original := []LocalWatchedRootBackupOutboxItem{
		{LocalOutboxID: "unknown", RootKey: "notes", LocalSequence: 1, Status: "unrecognised"},
		{LocalOutboxID: "first", RootKey: "notes", LocalSequence: 30},
		{LocalOutboxID: "other", RootKey: "other", LocalSequence: 2, Status: "pending"},
		{LocalOutboxID: "second", RootKey: "notes", LocalSequence: 10, Status: "pending"},
		{LocalOutboxID: "terminal", RootKey: "notes", LocalSequence: 100, Status: "failed"},
	}
	if err := s.SaveWatchedRootBackupOutbox(original); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	locked, err = locked.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	first, err := locked.pendingWatchedRootBackupOutbox("notes", 1)
	if err != nil || len(first) != 1 || first[0].LocalOutboxID != "first" {
		t.Fatal("eligible array order changed", first, err)
	}
	first[0].Status = "accepted"
	if err := locked.saveBackupQueueSelection(locked.watchedRootBackupOutboxPath(), first); err != nil {
		t.Fatal(err)
	}
	next, err := locked.pendingWatchedRootBackupOutbox("notes", 1)
	if err != nil || len(next) != 1 || next[0].LocalOutboxID != "second" {
		t.Fatal("staged terminal item starved later work", next, err)
	}
	added := LocalWatchedRootBackupOutboxItem{LocalOutboxID: "appended", RootKey: "notes", LocalSequence: 101, Status: "pending"}
	if err := locked.saveBackupQueueSelection(locked.watchedRootBackupOutboxPath(), []LocalWatchedRootBackupOutboxItem{added}); err != nil {
		t.Fatal(err)
	}
	all, err := locked.pendingWatchedRootBackupOutbox("", 10)
	if err != nil || len(all) != 3 || all[0].LocalOutboxID != "other" || all[1].LocalOutboxID != "second" || all[2].LocalOutboxID != "appended" {
		t.Fatal("staged/global order lost", all, err)
	}
	if err := locked.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	again, err := locked.pendingWatchedRootBackupOutbox("", 10)
	if err != nil || !reflect.DeepEqual(all, again) {
		t.Fatal("commit changed selected order", again, err)
	}
}

func TestQueueRecordsBackupActionSelectsExactHashHistory(t *testing.T) {
	s, _, _, q := backupSelectionHistory(t, 128)
	for i := range q.items {
		q.items[i].RootKey = "notes"
		q.items[i].RelativePath = "same-file.md"
		q.items[i].ItemKind = mainwatchedroots.BackupItemKindFile
		q.items[i].ContentHashURI = fmt.Sprintf("sha256:version-%d", i)
		q.items[i].PreviousHashURI = fmt.Sprintf("sha256:previous-%d", i)
	}
	if err := s.SaveWatchedRootBackupItems(q.items); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	locked, err = locked.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	action := watchedroots.OutputAction{RootKey: "notes", RelativePath: "same-file.md", BackupItemKind: mainwatchedroots.BackupItemKindFile, ContentHashURI: q.items[7].ContentHashURI, PreviousHashURI: q.items[7].PreviousHashURI}
	selected, err := locked.loadWatchedRootBackupAction(action)
	if err != nil || len(selected.items) != 1 || selected.items[0].LocalItemID != q.items[7].LocalItemID {
		t.Fatal("wrong hash history selected", selected, err)
	}
	if locked.syncBatch.stats.RecordReads != 3 || locked.syncBatch.stats.RecordIndexReads != 2 || locked.syncBatch.stats.RecordIndexBuildRows != 0 {
		t.Fatalf("decoded unrelated versions: %+v", locked.syncBatch.stats)
	}
	selected.items[0].Status = localBackupStatusFailed
	if err := locked.saveBackupQueueSelection(locked.watchedRootBackupItemsPath(), selected.items); err != nil {
		t.Fatal(err)
	}
	selected, err = locked.loadWatchedRootBackupAction(action)
	if err != nil || len(selected.items) != 0 {
		t.Fatal("failed history suppressed requeue", selected, err)
	}
	t.Logf("128 retained versions: %+v", locked.syncBatch.stats)
}

func TestQueueRecordsBackupWarmIndexSurvivesNoRowRedo(t *testing.T) {
	for _, kind := range []string{"path-only", "already-applied"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, original := backupSelectionHistory(t, 3)
			locked, unlock, err := s.lockLocalSync(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer unlock()
			locked, err = locked.beginSyncBatch()
			if err != nil {
				t.Fatal(err)
			}
			journal := queueBatchJournal{Version: 3, Paths: []watchedroots.PathState{{RootKey: "notes", RelativePath: "path-only.md"}}}
			if kind == "already-applied" {
				id := original.items[0].LocalItemID
				if err := locked.queueDB.View(func(tx *bolt.Tx) error {
					raw := bytes.Clone(recordBucket(tx, "backup/items", "rows").Get([]byte(id)))
					journal.Records = map[string]map[string]json.RawMessage{"backup/items": {id: raw}}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			apply := func() {
				t.Helper()
				if err := writeDurableQueueJSON(locked.syncBatchJournalPath(), journal); err != nil {
					t.Fatal(err)
				}
				if err := locked.applyRecordJournal(journal); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				apply()
				if err := locked.ensureBackupSelectionIndexes(); err != nil {
					t.Fatal(err)
				}
			}
			if got := locked.syncBatch.stats.RecordIndexBuildRows; got != 0 {
				t.Fatalf("known-writer no-row redo rebuilt %d retained rows", got)
			}
			// A known no-row commit after an untracked transaction must not bless
			// the stale watermark left by that older/untracked writer.
			if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
				return tx.Bucket([]byte("meta")).Put([]byte("test-untracked-writer"), []byte("yes"))
			}); err != nil {
				t.Fatal(err)
			}
			apply()
			if err := locked.ensureBackupSelectionIndexes(); err != nil {
				t.Fatal(err)
			}
			if got := locked.syncBatch.stats.RecordIndexBuildRows; got != 6 {
				t.Fatalf("untracked transaction did not invalidate the index: build rows=%d", got)
			}
		})
	}
}
