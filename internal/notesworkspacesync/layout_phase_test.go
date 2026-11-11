package notesworkspacesync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type layoutCursorCountStore struct {
	Store
	reads int
	onPut func()
}

type layoutCursorCountRecords struct {
	Records
	owner *layoutCursorCountStore
}

func (s *layoutCursorCountStore) WithReplica(ctx context.Context, replica, epoch string, fn func(Records) error) error {
	return s.Store.WithReplica(ctx, replica, epoch, func(r Records) error {
		return fn(layoutCursorCountRecords{r, s})
	})
}

func (r layoutCursorCountRecords) Get(ctx context.Context, kind, id string, out any) error {
	if kind == "cursor" && strings.HasPrefix(id, "layout_scan_v3_") {
		r.owner.reads++
	}
	return r.Records.Get(ctx, kind, id, out)
}

func (r layoutCursorCountRecords) Put(ctx context.Context, kind, id string, value any) error {
	err := r.Records.Put(ctx, kind, id, value)
	if err == nil && kind == "cursor" && strings.HasPrefix(id, "layout_scan_v3_") && r.owner.onPut != nil {
		r.owner.onPut()
	}
	return err
}

func TestLayoutPhaseSkipsCursorUntilExactWorkOrBaseline(t *testing.T) {
	s, source, _, _, now, old := layoutRetryFixture(t)
	store := &layoutCursorCountStore{Store: s.Store}
	s.Store = store
	deadline := s.LayoutRetryAt
	*now = now.Add(time.Minute)
	advance := func(wantReads int) {
		t.Helper()
		store.reads = 0
		if more, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || more || store.reads != wantReads {
			t.Fatal("layout phase selection", more, store.reads, wantReads, err)
		}
	}
	for i := 0; i < 3; i++ {
		s.dependencyCache().invalidate("record:binding:unrelated", false, false)
		advance(0)
		if !s.LayoutRetryAt.Equal(deadline) || s.LayoutHoldReasons["source_read_evidence_held"] != 1 || source.reads["a.md"] != 1 {
			t.Fatal("skipped phase lost retained diagnostics or performed source work")
		}
	}
	s.dependencyCache().invalidate("record:binding:"+old.ID, false, false)
	advance(1)
	advance(0)
	s.dependencyCache().invalidate(layoutPathKey("source", "a.md"), true, true)
	advance(1)
	if source.reads["a.md"] != 1 {
		t.Fatal("unchanged forced hint retried source bytes", source.reads)
	}
	if err := source.Service.Sources.WithSource(t.Context(), "source", "a.md", func(src notesworkspace.Source) error {
		return os.WriteFile(filepath.Join(src.Path, "a.md"), []byte("actual edit"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().invalidate(layoutPathKey("source", "a.md"), true, true)
	advance(1)
	if source.reads["a.md"] != 2 {
		t.Fatal("material source change failed to retry", source.reads)
	}
	s.dependencyCache().reset()
	advance(1)
	advance(0)
	current := s.Native.(*layoutNative)
	s.Native = &layoutNative{fakeNative: current.fakeNative}
	advance(1)
	s.Scopes[0].PreviousGeneration = "replacement"
	advance(1)
	s.dependencyCache().retainScopes(nil)
	advance(1)
	*now = now.Add(layoutEvidenceRetry)
	advance(1)
}

func TestLayoutPhaseRetainsIncompleteAndConcurrentWork(t *testing.T) {
	s, _, _, _, now, old := layoutRetryFixture(t)
	store := &layoutCursorCountStore{Store: s.Store}
	s.Store = store
	cache := s.dependencyCache()
	*now = now.Add(layoutEvidenceRetry)
	cache.invalidate("record:binding:"+old.ID, false, true)
	store.onPut = func() {
		store.onPut = nil
		cache.invalidate("record:binding:"+old.ID, false, true)
	}
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil {
		t.Fatal(err)
	}
	store.reads = 0
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || store.reads != 1 {
		t.Fatal("hint during phase was cleared", store.reads, err)
	}
	scope := s.Scopes[0]
	cache.finishLayout(scope, s.Native, s.Epoch, s.AdoptExisting, layoutCursor{}, *now, cache.version())
	if due, _, _ := cache.beginLayout(scope, s.Native, s.Epoch, s.AdoptExisting, *now); !due {
		t.Fatal("incomplete discovery was suppressed")
	}
	version := cache.version()
	cache.reset()
	cache.finishLayout(scope, s.Native, s.Epoch, s.AdoptExisting, layoutCursor{Complete: true}, *now, version)
	if due, _, _ := cache.beginLayout(scope, s.Native, s.Epoch, s.AdoptExisting, *now); !due {
		t.Fatal("reconnect during phase lost baseline")
	}
}

func TestLayoutPhaseSparseRepairDeadlineDoesNotSlide(t *testing.T) {
	s, _, _, _, now, _ := layoutRetryFixture(t)
	cache := s.dependencyCache()
	scope := s.Scopes[0]
	cache.finishLayout(scope, s.Native, s.Epoch, s.AdoptExisting, layoutCursor{Complete: true}, *now, cache.version())
	deadline := now.Add(layoutEvidenceRetry)
	store := &layoutCursorCountStore{Store: s.Store}
	s.Store = store
	for i := 0; i < 3; i++ {
		*now = now.Add(time.Hour)
		if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || store.reads != 0 || !s.LayoutRetryAt.Equal(deadline) {
			t.Fatal("unrelated runs slid the sparse repair deadline", s.LayoutRetryAt, deadline, store.reads, err)
		}
	}
	*now = deadline
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || store.reads != 1 {
		t.Fatal("sparse deadline did not reload the durable cursor", store.reads, err)
	}
}
