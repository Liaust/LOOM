package knowledge

import (
	"testing"
	"time"

	"loom.local/loom/internal/notesprojection"
)

func TestProjectionOwnerHooksPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	pending := func() []notesprojection.DirtyChange {
		t.Helper()
		changes, err := s.PendingProjectionDirty(t.Context(), time.Now().Add(time.Second), 64)
		if err != nil {
			t.Fatal(err)
		}
		return changes
	}
	initial := pending()
	if len(initial) != 3 {
		t.Fatalf("initial admissions did not mark each source: %+v", initial)
	}
	for _, change := range initial {
		if err := s.FinishProjectionDirty(t.Context(), change, nil); err != nil {
			t.Fatal(err)
		}
	}
	object, err := s.store.GetKnowledgeObject(t.Context(), initial[0].ObjectID)
	if err != nil {
		t.Fatal(err)
	}
	object.LastSeenAt = time.Now().Add(time.Second)
	if _, err := s.store.UpsertKnowledgeObject(t.Context(), object); err != nil {
		t.Fatal(err)
	}
	if len(pending()) != 0 {
		t.Fatal("equivalent observation manufactured work")
	}
	object.Title += " updated"
	if _, err := s.store.UpsertKnowledgeObject(t.Context(), object); err != nil {
		t.Fatal(err)
	}
	changed := pending()
	if len(changed) != 1 || changed[0].ObjectID != object.KnowledgeObjectID {
		t.Fatalf("object hook missing: %+v", changed)
	}
	if err := s.FinishProjectionDirty(t.Context(), changed[0], nil); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		root.UpdatedAt = time.Now().Add(time.Second)
		if _, err := s.store.UpsertSourceRoot(t.Context(), root); err != nil {
			t.Fatal(err)
		}
	}
	if len(pending()) != 0 {
		t.Fatal("equivalent root observation manufactured work")
	}
	root := roots[0]
	root.Status = SourceRootStatusDisabled
	if _, err := s.store.UpsertSourceRoot(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	if got := pending(); len(got) != 1 {
		t.Fatalf("root policy did not invalidate its exact sources: %+v", got)
	} else if delta, err := s.ReadProjectionSources(t.Context(), []string{got[0].ObjectID}); err != nil || delta.Withdrawn[got[0].ObjectID] != "root_withdrawn" {
		t.Fatalf("disabled root lacks positive withdrawal evidence: %+v %v", delta, err)
	}
}
