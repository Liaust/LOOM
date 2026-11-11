package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestSyncBatchCommitAndInterruptedReplay(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, err = s.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		items, err := s.LoadSyncOutbox()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.SaveSyncOutbox(append(items, LocalSyncOutboxItem{LocalSequence: int64(i + 1)})); err != nil {
			t.Fatal(err)
		}
	}
	var disk []LocalSyncOutboxItem
	if err := readJSONFile(s.syncOutboxPath(), &disk); err != nil || len(disk) != 0 {
		t.Fatalf("batch escaped early: %d %v", len(disk), err)
	}
	state := watchedroots.PathState{RootKey: "notes", RelativePath: "a.md", LocalObjectID: "preserved-object", LocalVersionID: "preserved-version"}
	s.syncBatch.paths["a"] = state
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(s.syncOutboxPath(), &disk); err != nil || len(disk) != 64 {
		t.Fatalf("commit lost items: %d %v", len(disk), err)
	}
	// Simulate exit after journaling but before replacing every snapshot.
	data, _ := json.Marshal([]LocalSyncOutboxItem{{LocalSequence: 65}})
	journal := queueBatchJournal{Version: 1, Files: map[string]json.RawMessage{filepath.Base(s.syncOutboxPath()): data}, Paths: []watchedroots.PathState{state}}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), journal); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverSyncBatch(); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverSyncBatch(); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(s.syncOutboxPath(), &disk); err != nil || len(disk) != 1 || disk[0].LocalSequence != 65 {
		t.Fatalf("replay: %+v %v", disk, err)
	}
	got, err := watchedroots.NewStore(s.DataDir).LoadPathState("notes", "a.md")
	if err != nil || got.LocalObjectID != state.LocalObjectID || got.LocalVersionID != state.LocalVersionID {
		t.Fatalf("identity lost: %+v %v", got, err)
	}
	if _, err := os.Stat(s.syncBatchJournalPath()); !os.IsNotExist(err) {
		t.Fatal("journal not retired")
	}
}

func TestSyncBatchRejectsUnknownSnapshotBeforeWriting(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	journal := queueBatchJournal{Version: 1, Files: map[string]json.RawMessage{"../state.json": json.RawMessage(`[]`)}}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), journal); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.lockLocalSync(context.Background()); err == nil {
		t.Fatal("unsafe journal admitted")
	}
	if _, err := os.Stat(s.syncBatchJournalPath()); err != nil {
		t.Fatal("failed journal discarded")
	}
}

func TestSyncBatchRetainsDecodedStateAndAdvancesImmutableBaseline(t *testing.T) {
	s, unlock, err := (Store{DataDir: t.TempDir()}).lockLocalSync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, err = s.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.LoadSyncOutbox()
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, LocalSyncOutboxItem{LocalOutboxID: "one", Status: "pending"})
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	reads := s.syncBatch.stats.SnapshotReads
	items, err = s.LoadSyncOutbox()
	if err != nil || s.syncBatch.stats.SnapshotReads != reads {
		t.Fatalf("cache lost: %v", err)
	}
	items[0].Status = "accepted"
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	var disk []LocalSyncOutboxItem
	if err := readJSONFile(s.syncOutboxPath(), &disk); err != nil || disk[0].Status != "accepted" {
		t.Fatalf("baseline aliased: %+v %v", disk, err)
	}
	if err := s.SaveSyncOutbox(items); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil || s.syncBatch.stats.CommitPages != 2 {
		t.Fatalf("unchanged replay wrote: %+v %v", s.syncBatch.stats, err)
	}
}

func TestQueueBatchBackupPageAndUnchangedNoWrite(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	s, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		t.Fatal(err)
	}
	s, err = s.beginSyncBatch()
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(s.watchedRootBackupArtifactsPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, _, err := s.QueueWatchedRootBackupAction(Config{NodeKey: "fixture"}, State{NodeID: "node_fixture"}, watchedroots.OutputAction{
			ActionKind: watchedroots.OutputActionBackupMetadata, RootKey: "notes", RelativePath: fmt.Sprintf("folder-%d", i),
			BackupItemKind: mainwatchedroots.BackupItemKindDirectory, BackupMode: watchedroots.BackupModeIncrementalRaw,
		}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	var disk []LocalWatchedRootBackupOutboxItem
	if err := readJSONFile(s.watchedRootBackupOutboxPath(), &disk); err != nil || len(disk) != 0 {
		t.Fatalf("page escaped before commit: %+v %v", disk, err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	if err := readJSONFile(s.watchedRootBackupOutboxPath(), &disk); err != nil || len(disk) != 4 {
		t.Fatalf("lost backup companions: %+v %v", disk, err)
	}
	after, err := os.Stat(s.watchedRootBackupArtifactsPath())
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("unchanged artifacts rewritten: %v", err)
	}
	if s.syncBatch.stats.SnapshotReads != 4 || s.syncBatch.stats.SnapshotWrites != 3 || s.syncBatch.stats.CommitPages != 1 {
		t.Fatalf("per-item work survived: %+v", s.syncBatch.stats)
	}
	outboxInfo, _ := os.Stat(s.watchedRootBackupOutboxPath())
	logInfo, _ := os.Stat(s.watchedRootBackupQueueEventsPath())
	cached, err := s.LoadWatchedRootBackupOutbox()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveWatchedRootBackupOutbox(cached); err != nil {
		t.Fatal(err)
	}
	if err := s.commitSyncBatch(); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := os.Stat(s.watchedRootBackupOutboxPath())
	logAfter, _ := os.Stat(s.watchedRootBackupQueueEventsPath())
	if !os.SameFile(outboxInfo, unchanged) || logAfter.Size() != logInfo.Size() || s.syncBatch.stats.CommitPages != 1 {
		t.Fatal("no-op changed snapshot/log")
	}
	t.Logf("4 metadata actions: 3 snapshot writes instead of 16 (empty artifacts unchanged); snapshot bytes=%d, redo bytes=%d", s.syncBatch.stats.SnapshotBytes, s.syncBatch.stats.JournalBytes)
}

func TestQueueBatchBackupInterruptedReplayAndVersionCompatibility(t *testing.T) {
	s := Store{DataDir: t.TempDir()}
	if err := s.EnsureSyncDataDirs(); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureWatchedRootBackupDataDirs(); err != nil {
		t.Fatal(err)
	}
	backupItems, _ := json.Marshal([]LocalWatchedRootBackupItem{{LocalItemID: "exact-item", LocalBatchID: "exact-batch", ContentHashURI: "sha256:retained", Status: localBackupStatusAccepted}})
	outbox, _ := json.Marshal([]LocalSyncOutboxItem{{LocalOutboxID: "exact-sync", Status: localSyncStatusAccepted}})
	journal := queueBatchJournal{Version: 2, Files: map[string]json.RawMessage{filepath.Base(s.syncOutboxPath()): outbox}, BackupFiles: map[string]json.RawMessage{localBackupItemsFile: backupItems}}
	if err := writeDurableQueueJSON(s.syncBatchJournalPath(), journal); err != nil {
		t.Fatal(err)
	}
	// Simulate failure after the sync snapshot has installed, before backup install.
	if err := os.Remove(s.watchedRootBackupItemsPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.watchedRootBackupItemsPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.lockLocalSync(context.Background()); err == nil {
		t.Fatal("interrupted install unexpectedly succeeded")
	}
	if _, err := os.Stat(s.syncBatchJournalPath()); err != nil {
		t.Fatal("redo discarded on partial install")
	}
	if err := os.Remove(s.watchedRootBackupItemsPath()); err != nil {
		t.Fatal(err)
	}
	locked, unlock, err := s.lockLocalSync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	items, err := locked.LoadWatchedRootBackupItems()
	if err != nil || len(items) != 1 || items[0].LocalItemID != "exact-item" || items[0].ContentHashURI != "sha256:retained" || items[0].Status != localBackupStatusAccepted {
		t.Fatalf("lost retained identity: %+v %v", items, err)
	}
	// A v1 label cannot hide extended fields; unknown names fail before any write.
	for _, invalid := range []queueBatchJournal{
		{Version: 1, Files: journal.Files, BackupFiles: journal.BackupFiles},
		{Version: 2, Files: journal.Files, BackupFiles: map[string]json.RawMessage{"../config.json": json.RawMessage(`[]`)}},
		{Version: 3, Files: journal.Files},
	} {
		if err := writeDurableQueueJSON(s.syncBatchJournalPath(), invalid); err != nil {
			t.Fatal(err)
		}
		before, _ := os.Stat(s.syncOutboxPath())
		if err := locked.recoverSyncBatch(); err == nil {
			t.Fatal("unsupported journal admitted")
		}
		after, _ := os.Stat(s.syncOutboxPath())
		if !os.SameFile(before, after) {
			t.Fatal("invalid journal partially wrote snapshot")
		}
		if _, err := os.Stat(s.syncBatchJournalPath()); err != nil {
			t.Fatal("invalid journal discarded")
		}
	}
}
