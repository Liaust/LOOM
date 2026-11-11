package nodeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
	"loom.local/loom/internal/nodeagent/watchedroots"
	loomsync "loom.local/loom/internal/sync"
)

func recordQueueFixture(t *testing.T) (Store, Config, State) {
	t.Helper()
	s, config, state, _ := localSyncObjectTestStore(t)
	objects := make([]LocalSyncObject, 0, 1001)
	outbox := make([]LocalSyncOutboxItem, 0, 1001)
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("object-%04d", i)
		version := fmt.Sprintf("version-%04d", i)
		objects = append(objects, LocalSyncObject{LocalObjectID: id, LocalVersionID: version, LocalSequence: int64(i + 1), Metadata: marshalRaw(map[string]any{"watched_root": "notes", "relative_path": fmt.Sprintf("note-%04d.md", i)}), SyncStatus: "accepted"})
		status := "accepted"
		if i == 1 {
			status = "failed"
		}
		if i == 2 {
			status = "conflicted"
		}
		outbox = append(outbox, LocalSyncOutboxItem{LocalOutboxID: fmt.Sprintf("outbox-%04d", i), LocalRef: id, ItemKind: loomsync.ItemKindObjectBlob, StreamName: loomsync.StreamObjectBlobs, LocalSequence: int64(i + 1), Status: status, PayloadJSON: marshalRaw(map[string]any{"local_version_ref": version})})
	}
	if err := s.SaveSyncObjects(objects); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncOutbox(outbox); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncEvents([]LocalSyncEvent{{LocalEventID: "historical-event", StreamName: "test_events", LocalSequence: 1200, SyncStatus: "accepted"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupOutbox([]LocalWatchedRootBackupOutboxItem{{LocalOutboxID: "retained-backup", RootKey: "protected", Status: "accepted"}}); err != nil {
		t.Fatal(err)
	}
	return s, config, state
}
func TestQueueRecordsMigrationHistoryAndScopedWrites(t *testing.T) {
	s, config, state := recordQueueFixture(t)
	before, err := os.ReadFile(s.syncObjectsPath())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := s.MigrateQueueRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Collections["sync/objects"].Count != 1000 || receipt.Collections["sync/outbox"].Count != 1000 {
		t.Fatal(receipt)
	}
	preserved, err := os.ReadFile(filepath.Join(s.queueRecordsBaseline(), "sync.objects.json"))
	if err != nil || !bytes.Equal(preserved, before) {
		t.Fatal("baseline changed", err)
	}
	var legacy []LocalSyncObject
	if err := readJSONFile(s.syncObjectsPath(), &legacy); err == nil {
		t.Fatal("old writer not fenced")
	}
	all, err := s.LoadSyncObjects()
	if err != nil || len(all) != 1000 || all[0].LocalObjectID != "object-0000" || all[999].LocalObjectID != "object-0999" {
		t.Fatal("history/order lost", len(all), err)
	}
	s, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, err = s.selectRecordTurn("notes", []watchedroots.OutputAction{{RelativePath: "note-0000.md"}})
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := s.LoadSyncObjects()
	if err != nil || len(objects) != 1 {
		t.Fatal("full history read in normal turn", len(objects), err)
	}
	items, err := s.LoadSyncOutbox()
	if err != nil || len(items) != 1 {
		t.Fatal("outbox scope", len(items), err)
	}
	next, err := s.nextLocalSequence(loomsync.StreamObjectBlobs)
	if err != nil || next != 1001 {
		t.Fatal("sequence used partial history", next, err)
	}
	objects[0].SyncStatus = "pending"
	if err := s.SaveSyncObjects(objects); err != nil {
		t.Fatal(err)
	}
	items[0].Status = "pending"
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	counts, err := s.localSyncRootStatus(config, state, "notes")
	if err != nil || counts.Counts.Pending != 1 || counts.Counts.Failed != 1 || counts.Counts.Conflicted != 1 {
		t.Fatal("batch status lost history", counts, err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	objects[0].SyncStatus = "accepted"
	items[0].Status = "accepted"
	if err := s.SaveSyncObjects(objects); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	stats := s.syncBatch.stats
	if stats.SnapshotBytes != 0 || stats.RecordWrites != 4 || stats.RecordBytes > 16000 || stats.JournalBytes > 20000 || stats.RecordReads > 4 {
		t.Fatal("not record-level persistence", stats)
	}
	pages := stats.CommitPages
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil || s.syncBatch.stats.CommitPages != pages {
		t.Fatal("no-op wrote", err)
	}
	if err := s.queueDB.View(func(tx *bolt.Tx) error {
		if recordBucket(tx, "sync/objects", "rows").Stats().KeyN != 1000 || recordBucket(tx, "sync/outbox", "rows").Stats().KeyN != 1000 {
			return fmt.Errorf("partial view deleted unrelated history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Logf("1000 retained notes: %+v", stats)
}
func TestQueueRecordsRedoAndInterruptedCutover(t *testing.T) {
	s, _, _ := recordQueueFixture(t)
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item := LocalSyncOutboxItem{LocalOutboxID: "pending-replay", LocalRef: "object-0000", ItemKind: loomsync.ItemKindObjectBlob, LocalSequence: 1300, StreamName: loomsync.StreamObjectBlobs, Status: "pending", PayloadJSON: marshalRaw(map[string]any{"local_version_ref": "version-0000"})}
	raw, _ := json.Marshal(item)
	p := watchedroots.PathState{RootKey: "notes", RelativePath: "replay.md", LocalObjectID: "object-0000"}
	j := queueBatchJournal{Version: 3, Records: map[string]map[string]json.RawMessage{"sync/outbox": {item.LocalOutboxID: raw}}, Paths: []watchedroots.PathState{p}}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), j); err != nil {
		t.Fatal(err)
	}
	// Simulate committed records with the path side still outstanding.
	if err := locked.queueDB.Update(func(tx *bolt.Tx) error {
		_, err := putQueueRecord(tx, "sync/outbox", item.LocalOutboxID, raw)
		return err
	}); err != nil {
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
	state, err := watchedroots.NewStore(s.DataDir).LoadPathState("notes", "replay.md")
	if err != nil || state.LocalObjectID != p.LocalObjectID {
		t.Fatal("path not recovered", err)
	}
	next, err := locked.nextRecordSequence(loomsync.StreamObjectBlobs)
	if err != nil || next != 1301 {
		t.Fatal(next, err)
	}
	if _, err := os.Stat(s.syncBatchJournalPath()); !os.IsNotExist(err) {
		t.Fatal("redo not retired", err)
	}
	// A malformed journal must not partially install valid rows first.
	j.Records["unknown"] = map[string]json.RawMessage{"bad": raw}
	if err := locked.applyRecordJournal(j); err == nil {
		t.Fatal("unknown collection admitted")
	}
}
func TestQueueRecordsMigrationRejectsInvalidHistory(t *testing.T) {
	for _, kind := range []string{"duplicate", "unknown-field", "trailing"} {
		t.Run(kind, func(t *testing.T) {
			s := Store{DataDir: t.TempDir()}
			if err := s.EnsureSyncDataDirs(); err != nil {
				t.Fatal(err)
			}
			raw := []byte(`[{"local_outbox_id":"same"},{"local_outbox_id":"same"}]`)
			if kind == "unknown-field" {
				raw = []byte(`[{"local_outbox_id":"one","unknown":true}]`)
			}
			if kind == "trailing" {
				raw = []byte(`[] []`)
			}
			if err := os.WriteFile(s.syncOutboxPath(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.MigrateQueueRecords(t.Context()); err == nil {
				t.Fatal("invalid history imported")
			}
			if s.recordsExist() {
				t.Fatal("failed import activated")
			}
			got, _ := os.ReadFile(s.syncOutboxPath())
			if !bytes.Equal(raw, got) {
				t.Fatal("failed import changed original")
			}
		})
	}
}

func TestQueueRecordsDeleteReplayAndUnfinishedImport(t *testing.T) {
	s, _, _ := recordQueueFixture(t)
	// An unpublished store left by an interrupted importer can be retried.
	if err := s.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(s.queueRecordsPath()+".migrating", 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	j := queueBatchJournal{Version: 3, Records: map[string]map[string]json.RawMessage{"sync/outbox": {"outbox-0000": nil}}}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), j); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.queueDB.View(func(tx *bolt.Tx) error {
		if recordBucket(tx, "sync/outbox", "rows").Get([]byte("outbox-0000")) != nil {
			return fmt.Errorf("delete tombstone not replayed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	unlock()
	// This external scheduler view must see record changes, not cache markers.
	counts, err := s.syncCountsByRoot()
	if err != nil || counts["notes"].Accepted != 997 || counts["notes"].Failed != 1 || counts["notes"].Conflicted != 1 {
		t.Fatal(counts, err)
	}
	if err := s.withRecordAccess(func(s Store) error {
		item := LocalSyncOutboxItem{LocalOutboxID: "new-pending", LocalRef: "object-0000", ItemKind: loomsync.ItemKindObjectBlob, StreamName: loomsync.StreamObjectBlobs, LocalSequence: 1500, Status: "pending"}
		raw, _ := json.Marshal(item)
		return s.queueDB.Update(func(tx *bolt.Tx) error {
			_, err := putQueueRecord(tx, "sync/outbox", item.LocalOutboxID, raw)
			return err
		})
	}); err != nil {
		t.Fatal(err)
	}
	counts, err = s.syncCountsByRoot()
	if err != nil || counts["notes"].Pending != 1 {
		t.Fatal("scheduler cached frozen marker", counts, err)
	}
}

func TestQueueRecordsBackupSummaryAndV1Upgrade(t *testing.T) {
	s, config, state, _ := localSyncObjectTestStore(t)
	if err := s.SaveWatchedRootBackupArtifacts([]LocalWatchedRootBackupArtifact{{LocalArtifactID: "artifact", RootKey: "notes", Status: "accepted"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupBatches([]LocalWatchedRootBackupBatch{{LocalBatchID: "batch", RootKey: "notes", TotalBytes: 12345, Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupItems([]LocalWatchedRootBackupItem{{LocalItemID: "item", RootKey: "notes", Status: "failed", LastErrorCode: "test"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupOutbox([]LocalWatchedRootBackupOutboxItem{{LocalOutboxID: "out", LocalRef: "batch", RootKey: "notes", Status: "pending", LastErrorCode: "retry"}, {LocalOutboxID: "other", RootKey: "other", Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	legacy, err := s.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(legacy.Failed, func(i, j int) bool {
		a, _ := json.Marshal(legacy.Failed[i])
		b, _ := json.Marshal(legacy.Failed[j])
		return bytes.Compare(a, b) < 0
	})
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	records, err := s.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil || !reflect.DeepEqual(legacy, records) {
		t.Fatalf("summary differs: legacy=%+v records=%+v err=%v", legacy, records, err)
	}
	if err := s.withRecordAccess(func(s Store) error {
		return s.queueDB.Update(func(tx *bolt.Tx) error {
			if err := tx.DeleteBucket([]byte("backup-summary")); err != nil {
				return err
			}
			if err := tx.DeleteBucket([]byte("backup-issues")); err != nil {
				return err
			}
			return tx.Bucket([]byte("meta")).Put([]byte("format"), []byte("loom.node-queue.records.v1"))
		})
	}); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := s.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil || !reflect.DeepEqual(records, rebuilt) {
		t.Fatal("v1 additive rebuild changed status", rebuilt, err)
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
	items, err := locked.LoadWatchedRootBackupOutbox()
	if err != nil {
		t.Fatal(err)
	}
	items[0].Status = "accepted"
	items[0].LastErrorCode = ""
	if err := locked.SaveWatchedRootBackupOutbox(items); err != nil {
		t.Fatal(err)
	}
	updated, err := locked.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil || updated.Counts.Pending != 0 || updated.Counts.PendingBytes != 0 || updated.Counts.Accepted != 1 || updated.Counts.Retryable != 0 {
		t.Fatal("staged summary stale", updated, err)
	}
	if err := locked.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	updated, err = locked.LocalWatchedRootBackupStatus(config, state, "notes")
	if err != nil || updated.Counts.Accepted != 1 || updated.Counts.Pending != 0 {
		t.Fatal(updated, err)
	}
}

func TestQueueRecordsCLIExportPreservesAllCollections(t *testing.T) {
	s, config, state := recordQueueFixture(t)
	if err := s.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveState(state); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.MigrateQueueRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	opts := rootOptions{configPath: s.ConfigPath, statePath: s.StatePath, dataDir: s.DataDir, jsonOutput: true, out: &out}
	dir := filepath.Join(t.TempDir(), "export")
	cmd := newQueueRecordsCommand(&opts)
	cmd.SetArgs([]string{"export", dir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, c := range recordCollections {
		raw, err := os.ReadFile(filepath.Join(dir, strings.ReplaceAll(c, "/", ".")+".json"))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := canonicalQueueRows(raw)
		if err != nil || len(rows) != receipt.Collections[c].Count || queueDigest(rows) != receipt.Collections[c].CanonicalSHA256 {
			t.Fatal("export differs from imported history", c, err)
		}
	}
	cmd = newQueueRecordsCommand(&opts)
	cmd.SetArgs([]string{"export", dir})
	if err := cmd.Execute(); err == nil {
		t.Fatal("export overwrote existing directory")
	}
}

func TestQueueRecordsManagedReadOnlyACLMode(t *testing.T) {
	s, _, _ := recordQueueFixture(t)
	if _, err := s.MigrateQueueRecords(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.queueRecordsPath(), 0640); err != nil {
		t.Fatal(err)
	}
	rows, err := s.LoadSyncObjects()
	if err != nil || len(rows) != 1000 {
		t.Fatal("managed read-only ACL mode refused", len(rows), err)
	}
	if err := os.Chmod(s.queueRecordsPath(), 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadSyncObjects(); err == nil {
		t.Fatal("group-writable store admitted")
	}
}
