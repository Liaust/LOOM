package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func historyFixture(t *testing.T, store Store, bucket, status string, offset int) WorkerRun {
	t.Helper()
	started := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Second)
	finished := started.Add(time.Millisecond)
	run := WorkerRun{WorkerKey: WorkerKeyPoll, LocalRunID: fmt.Sprintf("local_worker_run_%s_000001", started.Format("20060102150405")),
		Kind: KindPoll, Status: status, StartedAt: started, FinishedAt: &finished}
	path := filepath.Join(store.runsDir(), run.WorkerKey, bucket, run.LocalRunID+".json")
	if err := writeJSONFile(path, run, 0o600); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestRunHistoryAppendBoundsBucketsAndProtectsReferences(t *testing.T) {
	store := NewStore(t.TempDir())
	var first, second WorkerRun
	for i := 0; i < retainedCompletedRuns+4; i++ {
		run := historyFixture(t, store, "completed", RunStatusSucceeded, i)
		if i == 0 {
			first = run
		}
		if i == 1 {
			second = run
		}
	}
	if err := store.SaveCheckpoint(WorkerCheckpoint{WorkerKey: WorkerKeyPoll, LastRunID: first.LocalRunID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHealth(WorkerHealth{WorkerKey: WorkerKeyPoll, LastRunID: second.LocalRunID}); err != nil {
		t.Fatal(err)
	}
	newest := historyFixture(t, store, "completed", RunStatusSucceeded, 9999)
	if err := store.AppendRun(newest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(store.runsDir(), WorkerKeyPoll, "completed"))
	if err != nil || len(entries) > retainedCompletedRuns+3 {
		t.Fatal("completed history is unbounded", len(entries), err)
	}
	for _, id := range []string{first.LocalRunID, second.LocalRunID, newest.LocalRunID} {
		if _, err := os.Stat(filepath.Join(store.runsDir(), WorkerKeyPoll, "completed", id+".json")); err != nil {
			t.Fatal("protected record removed", id, err)
		}
	}
	for i := 0; i < retainedFailedRuns+4; i++ {
		historyFixture(t, store, "failed", RunStatusFailed, i+30000)
	}
	failed := historyFixture(t, store, "failed", RunStatusFailed, 40000)
	if err := store.AppendRun(failed); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(filepath.Join(store.runsDir(), WorkerKeyPoll, "failed"))
	if err != nil || len(entries) > retainedFailedRuns+1 {
		t.Fatal("failed history is unbounded", len(entries), err)
	}
	runs, err := store.ListRuns(WorkerKeyPoll, 2)
	if err != nil || len(runs) != 2 || runs[0].LocalRunID != failed.LocalRunID {
		t.Fatal("bucket history not navigable", runs, err)
	}
}

func TestRunHistoryLegacyPreviewApplyPreservesUnknownAndQueues(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	var pinned WorkerRun
	for i := 0; i < retainedCompletedRuns+3; i++ {
		run := historyFixture(t, store, "", RunStatusSucceeded, i)
		if i == 0 {
			pinned = run
		}
	}
	for i := 0; i < retainedFailedRuns+3; i++ {
		historyFixture(t, store, "", RunStatusFailed, i+10000)
	}
	if err := store.SaveCheckpoint(WorkerCheckpoint{WorkerKey: WorkerKeyPoll, LastRunID: pinned.LocalRunID}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveHealth(WorkerHealth{WorkerKey: WorkerKeyPoll, LastRunID: pinned.LocalRunID}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(store.runsDir(), WorkerKeyPoll)
	unfinished := historyFixture(t, store, "", RunStatusStarted, 20000)
	unfinished.FinishedAt = nil
	if err := writeJSONFile(filepath.Join(base, unfinished.LocalRunID+".json"), unfinished, 0o600); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(base, "malformed.json")
	if err := os.WriteFile(malformed, []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(store.DataDir, "do-not-touch")
	if err := os.WriteFile(victim, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(base, "linked.json")); err != nil {
		t.Fatal(err)
	}
	queue, err := store.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck, PayloadJSON: []byte(`{"pending":true}`)})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(store.checkpointPath(WorkerKeyPoll))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PruneRunHistory(context.Background(), WorkerKeyPoll, false)
	if err != nil || preview.Candidates != 5 || preview.Deleted != 0 || preview.Moved != 0 || preview.Held != 3 {
		t.Fatal("incorrect preview", preview, err)
	}
	if _, err := os.Stat(filepath.Join(base, pinned.LocalRunID+".json")); err != nil {
		t.Fatal("preview changed history", err)
	}
	result, err := store.PruneRunHistory(context.Background(), WorkerKeyPoll, true)
	if err != nil || result.Deleted != preview.Candidates || result.Held != 3 || result.Moved != retainedCompletedRuns+retainedFailedRuns+1 {
		t.Fatal("incorrect applied retention", result, err)
	}
	after, err := os.ReadFile(store.checkpointPath(WorkerKeyPoll))
	if err != nil || string(before) != string(after) {
		t.Fatal("checkpoint changed", err)
	}
	for _, path := range []string{malformed, filepath.Join(base, unfinished.LocalRunID+".json"), store.outboxPath(queue.Status, queue.LocalOutboxID), victim} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("unrelated or held file removed", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "completed", pinned.LocalRunID+".json")); err != nil {
		t.Fatal("pinned legacy run missing", err)
	}
	for _, key := range []string{"..", "../inbox", ""} {
		if _, err := store.PruneRunHistory(context.Background(), key, true); err == nil {
			t.Fatal("unsafe worker key admitted", key)
		}
	}
}

func TestRunHistoryDuplicateIdentityIsHeld(t *testing.T) {
	store := NewStore(t.TempDir())
	historyFixture(t, store, "", RunStatusSucceeded, 1)
	historyFixture(t, store, "completed", RunStatusSucceeded, 1)
	result, err := store.PruneRunHistory(context.Background(), WorkerKeyPoll, true)
	if err != nil || result.Held != 2 || result.Deleted != 0 || result.Moved != 0 {
		t.Fatal("duplicate evidence changed", result, err)
	}
}
