package knowledge

import (
	"testing"
	"time"

	"loom.local/loom/internal/notesprojection"
)

func TestProjectionDirtyBatchMixedAndNewerSequencePostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	now := time.Now().Add(time.Second)
	selected, err := s.PendingProjectionDirty(t.Context(), now, 64)
	if err != nil || len(selected) != 3 {
		t.Fatalf("initial dirty set: %+v %v", selected, err)
	}
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkProjectionDirtyTx(t.Context(), tx, ProjectionDirtyInput{ObjectIDs: []string{selected[2].ObjectID}}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	retry := now.Add(time.Hour)
	if err := s.FinishProjectionDirtyBatch(t.Context(), []notesprojection.DirtyCompletion{
		{Change: selected[0]}, {Change: selected[1], RetryAt: &retry}, {Change: selected[2], RetryAt: &retry},
	}); err != nil {
		t.Fatal(err)
	}
	ready, err := s.PendingProjectionDirty(t.Context(), now, 64)
	if err != nil || len(ready) != 1 || ready[0].Key != selected[2].Key || ready[0].Sequence != selected[2].Sequence+1 {
		t.Fatalf("batch deferred a newer sequence: %+v %v", ready, err)
	}
	if err := s.FinishProjectionDirtyBatch(t.Context(), []notesprojection.DirtyCompletion{{Change: ready[0]}, {Change: ready[0]}}); err == nil {
		t.Fatal("duplicate batch was accepted")
	}
	remaining, err := s.PendingProjectionDirty(t.Context(), retry.Add(time.Second), 64)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("invalid batch partially changed the held set: %+v %v", remaining, err)
	}
}

func TestProjectionDirtyTransactionAndNewerSequencePostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	sources, err := s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil {
		t.Fatal(err)
	}
	id := sources[0].KnowledgeObjectID
	// Owner hooks already marked fixture admissions. Acknowledge that baseline
	// through the real consumer API, without deleting durable queue history.
	initial, err := s.PendingProjectionDirty(t.Context(), time.Now().Add(time.Second), 64)
	if err != nil {
		t.Fatal(err)
	}
	var baseline int64
	for _, change := range initial {
		if change.ObjectID == id {
			baseline = change.Sequence
		}
		if err := s.FinishProjectionDirty(t.Context(), change, nil); err != nil {
			t.Fatal(err)
		}
	}
	mark := func(rollback bool) {
		t.Helper()
		tx, err := s.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := MarkProjectionDirtyTx(t.Context(), tx, ProjectionDirtyInput{ObjectIDs: []string{id, id}}); err != nil {
			t.Fatal(err)
		}
		if !rollback {
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	pending := func() []notesprojection.DirtyChange {
		t.Helper()
		changes, err := s.PendingProjectionDirty(t.Context(), time.Now().Add(time.Second), 64)
		if err != nil {
			t.Fatal(err)
		}
		return changes
	}
	mark(true)
	if len(pending()) != 0 {
		t.Fatal("rolled back mark survived")
	}
	mark(false)
	first := pending()
	if len(first) != 1 || first[0].Sequence != baseline+1 {
		t.Fatalf("first: %+v", first)
	}
	mark(false)
	if err := s.FinishProjectionDirty(t.Context(), first[0], nil); err != nil {
		t.Fatal(err)
	}
	newer := pending()
	if len(newer) != 1 || newer[0].Sequence != baseline+2 {
		t.Fatalf("new mark lost during acknowledgement: %+v", newer)
	}
	retry := time.Now().Add(time.Hour)
	if err := s.FinishProjectionDirty(t.Context(), first[0], &retry); err != nil {
		t.Fatal(err)
	}
	if len(pending()) != 1 {
		t.Fatal("stale failure deferred a newer mark")
	}
	if err := s.FinishProjectionDirty(t.Context(), newer[0], &retry); err != nil {
		t.Fatal(err)
	}
	if len(pending()) != 0 {
		t.Fatal("retry ignored")
	}
	mark(false)
	latest := pending()
	if len(latest) != 1 || latest[0].Sequence != baseline+3 {
		t.Fatal("new mark did not reset retry")
	}
	if err := s.FinishProjectionDirty(t.Context(), latest[0], nil); err != nil {
		t.Fatal(err)
	}
	if next, err := s.NextProjectionDirtyAt(t.Context()); err != nil || next != nil {
		t.Fatalf("acknowledged queue not idle: %v %v", next, err)
	}
	mark(false)
	if got := pending(); len(got) != 1 || got[0].Sequence != baseline+4 {
		t.Fatalf("sequence reset after acknowledgement: %+v", got)
	}
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := MarkProjectionDirtyTx(t.Context(), tx, ProjectionDirtyInput{RootID: roots[0].NotesSourceRootID, FullReconcile: true}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(pending()) < 2 {
		t.Fatal("root/full marks absent")
	}
	var full notesprojection.DirtyChange
	for _, change := range pending() {
		if change.Key == "full" {
			full = change
		}
	}
	if err := s.FinishProjectionDirty(t.Context(), full, &retry); err != nil {
		t.Fatal(err)
	}
	if err := s.WakeProjectionReconcile(t.Context(), full.Sequence+1); err != nil {
		t.Fatal(err)
	}
	for _, change := range pending() {
		if change.Key == "full" {
			t.Fatal("wrong sequence bypassed held full failure")
		}
	}
	if err := s.WakeProjectionReconcile(t.Context(), full.Sequence); err != nil {
		t.Fatal(err)
	}
	var woke bool
	for _, change := range pending() {
		woke = woke || change.Key == "full" && change.Sequence == full.Sequence
	}
	if !woke {
		t.Fatal("exact repaired dependency did not wake retained failure")
	}
	if err := s.FinishProjectionDirty(t.Context(), full, &retry); err != nil {
		t.Fatal(err)
	}
	tx, err = s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := MarkProjectionDirtyTx(t.Context(), tx, ProjectionDirtyInput{RootID: roots[0].NotesSourceRootID}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var advanced bool
	for _, change := range pending() {
		advanced = advanced || change.Key == "full" && change.Sequence == full.Sequence+1
	}
	if !advanced {
		t.Fatal("root change did not advance the exact retained full failure")
	}
	if err := s.FinishProjectionDirty(t.Context(), full, &retry); err != nil {
		t.Fatal(err)
	}
	var stillDue bool
	for _, change := range pending() {
		stillDue = stillDue || change.Key == "full" && change.Sequence == full.Sequence+1
	}
	if !stillDue {
		t.Fatal("old failure suppressed a newer root change")
	}
}
