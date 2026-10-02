package nodeagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
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
