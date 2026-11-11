package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestFileQueueHistoryRebuildResumesWithoutDecodingOrNoopWrites(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Ensure(); err != nil {
		t.Fatal(err)
	}
	for i := range fileSummaryPageSize + 3 {
		// Retained history is intentionally not a decodable OutboxItem.
		if err := os.WriteFile(s.outboxPath(OutboxStatusDone, fmt.Sprint(i)), []byte("retained history"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.OutboxSummary(); !errors.Is(err, errFileSummaryRebuilding) {
		t.Fatal(err)
	}
	// Progress is a derived cursor on disk, not a live iterator or a process cache.
	s = NewStore(s.DataDir)
	summary, err := s.OutboxSummary()
	if err != nil || summary.Counts[OutboxStatusDone] != fileSummaryPageSize+3 {
		t.Fatal(summary, err)
	}
	before, err := os.ReadFile(s.fileQueueCachePath())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.BuildLocalSummary()
	if err != nil {
		t.Fatal(err)
	}
	latest, err := os.ReadFile(s.latestSummaryPath())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := s.BuildLocalSummary()
		if err != nil || again.Availability != "available" || !again.GeneratedAt.Equal(first.GeneratedAt) {
			t.Fatal(again, err)
		}
	}
	after, err := os.ReadFile(s.fileQueueCachePath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("no-op changed file summary", err)
	}
	after, err = os.ReadFile(s.latestSummaryPath())
	if err != nil || !bytes.Equal(latest, after) {
		t.Fatal("no-op republished latest summary", err)
	}
	item, err := s.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MarkOutboxDone(item, nil); err != nil {
		t.Fatal(err)
	}
	part := s.loadFileQueueCache().Directories["outbox/done"]
	if !part.Complete || part.Count != fileSummaryPageSize+4 {
		t.Fatal("known completion invalidated history instead of incrementing it", part)
	}
	if summary, err := s.OutboxSummary(); err != nil || summary.Counts[OutboxStatusDone] != fileSummaryPageSize+4 {
		t.Fatal(summary, err)
	}
	// A file publication by an older writer invalidates the directory stamp.
	if err := writeJSONFile(s.inboxPath(OutboxStatusDone, "older-writer"), map[string]any{"anything": "retained"}, 0600); err != nil {
		t.Fatal(err)
	}
	if inbox, err := s.InboxSummary(); err != nil || inbox.Counts[OutboxStatusDone] != 1 {
		t.Fatal(inbox, err)
	}
}

func TestFileQueueSparseRepairCoversInPlaceOlderWriter(t *testing.T) {
	s := NewStore(t.TempDir())
	item, err := s.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck, CreatedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.OutboxSummary()
	if err != nil {
		t.Fatal(err)
	}
	path := s.outboxPath(OutboxStatusPending, item.LocalOutboxID)
	// An older in-place writer does not change the parent directory's stamp.
	item.CreatedAt = item.CreatedAt.Add(-time.Hour)
	item.SourceMessageID = "older-in-place-content-with-different-size"
	raw, _ := json.Marshal(item)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cache := s.loadFileQueueCache()
	cache.RepairAt = time.Now().Add(-time.Second)
	if err := writeDurableRuntimeJSON(s.fileQueueCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	after, err := NewStore(s.DataDir).OutboxSummary()
	if err != nil || after.TotalPendingBytes == before.TotalPendingBytes || after.OldestPendingAt == nil || !after.OldestPendingAt.Equal(item.CreatedAt) {
		t.Fatal(before, after, err)
	}
}

func TestFileQueueDirtyCrashAndTransitionsReconcile(t *testing.T) {
	s := NewStore(t.TempDir())
	item, err := s.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OutboxSummary(); err != nil {
		t.Fatal(err)
	}
	cache := s.loadFileQueueCache()
	cache.Dirty = true
	delete(cache.Directories, "outbox/pending")
	delete(cache.Directories, "outbox/done")
	if err := writeDurableRuntimeJSON(s.fileQueueCachePath(), cache); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after a file move but before the derived summary commit.
	if err := os.Rename(s.outboxPath(OutboxStatusPending, item.LocalOutboxID), s.outboxPath(OutboxStatusDone, item.LocalOutboxID)); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(s.DataDir)
	summary, err := restarted.OutboxSummary()
	if err != nil || summary.Counts[OutboxStatusPending] != 0 || summary.Counts[OutboxStatusDone] != 1 || restarted.loadFileQueueCache().Dirty {
		t.Fatal(summary, err)
	}
	item, err = s.QueueOutbox(OutboxItem{Kind: OutboxKindMessageAck})
	if err != nil {
		t.Fatal(err)
	}
	item, _, err = s.MarkOutboxInflight(item)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MarkOutboxDone(item, nil); err != nil {
		t.Fatal(err)
	}
	summary, err = s.OutboxSummary()
	if err != nil || summary.Counts[OutboxStatusPending] != 0 || summary.Counts[OutboxStatusInflight] != 0 || summary.Counts[OutboxStatusDone] != 2 {
		t.Fatal(summary, err)
	}
}

func TestReporterOwnCompletionDoesNotRequestAnotherRun(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.EnsureDefaultInstances(DefaultInstanceInput{}); err != nil {
		t.Fatal(err)
	}
	registry := DefaultRegistry()
	for range 2 {
		if _, err := registry.RunOnce(context.Background(), s, Env{}, WorkerKeyLocalQueueReporter, "test"); err != nil {
			t.Fatal(err)
		}
	}
	instance, _ := s.LoadInstance(WorkerKeyLocalQueueReporter)
	checkpoint, _ := s.LoadCheckpoint(instance.WorkerKey)
	var metadata struct {
		CheckedAt time.Time `json:"summary_checked_at"`
	}
	if err := json.Unmarshal(checkpoint.Metadata, &metadata); err != nil || metadata.CheckedAt.IsZero() {
		t.Fatal(metadata, err)
	}
	now := time.Now()
	next, err := (QueueReporterRunner{}).NextRunAt(s, Env{}, instance, now, *checkpoint.LastStartedAt, true)
	if err != nil || !next.After(now) {
		t.Fatal("completion retriggered reporter", next, err)
	}
	// A health transition is material; a repeated success timestamp is not.
	if err := s.SaveHealth(WorkerHealth{WorkerKey: WorkerKeyPoll, Kind: KindPoll, Status: WorkerStatusBlocked}); err != nil {
		t.Fatal(err)
	}
	hint, _ := os.ReadFile(s.healthSummaryWakePath())
	if err := s.SaveHealth(WorkerHealth{WorkerKey: WorkerKeyPoll, Kind: KindPoll, Status: WorkerStatusBlocked}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.healthSummaryWakePath())
	if !bytes.Equal(hint, after) {
		t.Fatal("no-op health emitted a material hint")
	}
	next, err = (QueueReporterRunner{}).NextRunAt(s, Env{}, instance, now, *checkpoint.LastStartedAt, true)
	if err != nil || next.After(now.Add(time.Second)) {
		t.Fatal(next, err)
	}
}
