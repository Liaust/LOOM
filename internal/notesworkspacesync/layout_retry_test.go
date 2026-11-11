package notesworkspacesync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/notesworkspace"
)

type layoutRetrySource struct {
	NotesSource
	reads         map[string]int
	metadataReads int
	fail          error
}

func (s *layoutRetrySource) File(ctx context.Context, id string) (notesworkspace.File, error) {
	s.metadataReads++
	return s.NotesSource.File(ctx, id)
}

func (s *layoutRetrySource) Read(ctx context.Context, collection, path string) (notesworkspace.ReadResult, error) {
	s.reads[path]++
	if path == "a.md" && s.fail != nil {
		return notesworkspace.ReadResult{}, s.fail
	}
	return s.NotesSource.Read(ctx, collection, path)
}

func layoutRetryFixture(t *testing.T) (*Service, *layoutRetrySource, *layoutScanStore, *sourceStore, *time.Time, Binding) {
	t.Helper()
	s, native, source, root, old := fixture(t)
	if err := os.WriteFile(filepath.Join(root, "b.md"), []byte("B"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := s.Source.Read(t.Context(), "source", "b.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Export(t.Context(), "c", read.File.ID, read.Base.ID); err != nil {
		t.Fatal(err)
	}
	prior := s.Scopes[0]
	s.Scopes[0].Root = "Projects/_Passive/Example/Notes"
	s.Native, s.LayoutPublication = &layoutNative{fakeNative: native}, true
	if err := s.prepareLayouts(t.Context(), []RuntimeSelection{{Scope: s.Scopes[0], LayoutFrom: prior.Root}}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	reader := &layoutRetrySource{NotesSource: s.Source.(NotesSource), reads: map[string]int{}, fail: ErrHeld}
	s.Source = reader
	counter := &layoutScanStore{Store: s.Store}
	s.Store = counter
	more, moved, held, err := s.advanceLayouts(t.Context(), 32)
	if err != nil || more || moved != 1 || held != 1 || reader.reads["b.md"] != 1 {
		t.Fatal("initial layout", more, moved, held, reader.reads, err)
	}
	return s, reader, counter, source, &now, old
}

func retainedLayoutCursor(t *testing.T, s *Service) layoutCursor {
	t.Helper()
	var cursor layoutCursor
	if err := s.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
		var err error
		cursor, err = load[layoutCursor](t.Context(), r, "cursor", layoutCursorID(s.Scopes[0]))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return cursor
}

func TestLayoutRetryKeepsDiscoveryCompleteAndTargetsOnlyHolds(t *testing.T) {
	s, source, scans, _, now, old := layoutRetryFixture(t)
	cursor := retainedLayoutCursor(t, s)
	if !cursor.Complete || !cursor.RetryKnown || len(cursor.Holds) != 1 || cursor.Holds[0].Reason != "source_read_evidence_held" || !cursor.RetryAt.Equal(now.Add(6*time.Hour)) {
		t.Fatal("missing targeted retry navigation", cursor)
	}
	if s.LayoutHoldReasons["source_read_evidence_held"] != 1 || s.LayoutInspected != 2 {
		t.Fatal("initial diagnostics", s.LayoutHoldReasons, s.LayoutInspected)
	}
	*now = now.Add(5 * time.Minute)
	metadataReads := source.metadataReads
	s.RetryRevision = "unrelated-note-edit"
	ready, next, err := s.layoutReadiness(t.Context(), *now)
	if err != nil || ready || !next.Equal(cursor.RetryAt) {
		t.Fatal("unrelated edit woke retry", ready, next, err)
	}
	for i := 0; i < 2; i++ {
		// A new Service instance must retain the same completed discovery/deadline.
		restarted := *s
		more, moved, held, err := restarted.advanceLayouts(t.Context(), 32)
		if err != nil || more || moved != 0 || held != 0 || scans.fileLists != 1 || source.reads["a.md"] != 1 || source.reads["b.md"] != 1 || restarted.LayoutInspected != 0 || source.metadataReads != metadataReads || restarted.LayoutHoldReasons["source_read_evidence_held"] != 1 {
			t.Fatal("unchanged hold revisited discovery or bytes", more, moved, held, scans.fileLists, source.reads, err)
		}
	}
	*now = cursor.RetryAt
	more, moved, held, err := s.advanceLayouts(t.Context(), 32)
	after := retainedLayoutCursor(t, s)
	if err != nil || more || moved != 0 || held != 0 || scans.fileLists != 1 || source.reads["a.md"] != 1 || source.reads["b.md"] != 1 || after.After != cursor.After || !after.Complete || len(after.Holds) != 1 || after.Holds[0].Attempts != cursor.Holds[0].Attempts {
		t.Fatal("deadline replayed successful mapping", more, moved, held, scans.fileLists, source.reads, after, err)
	}
	var retained BindingRecord
	if err := scans.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error { return r.Get(t.Context(), "binding", old.ID, &retained) }); err != nil || retained.Binding != old {
		t.Fatal("held receipt changed", retained, err)
	}
}

func TestLayoutRetryExactDependencyChangeWakesOnlyHeldIdentity(t *testing.T) {
	for _, change := range []string{"file-state", "mapping-base", "binding-receipt"} {
		t.Run(change, func(t *testing.T) {
			s, reader, scans, source, now, old := layoutRetryFixture(t)
			cursor := retainedLayoutCursor(t, s)
			*now = now.Add(time.Minute)
			if change == "file-state" {
				for id, file := range source.files {
					if file.RelativePath == "a.md" {
						file.PathVersion++
						source.files[id] = file
						s.dependencyCache().invalidate("source:"+id, false, false)
					}
				}
			} else if err := scans.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
				if change == "mapping-base" {
					mapping, err := load[FileMapping](t.Context(), r, "file", cursor.Holds[0].MappingID)
					if err != nil {
						return err
					}
					mapping.Latest.SourceBase = "base_changed"
					return r.Put(t.Context(), "file", cursor.Holds[0].MappingID, mapping)
				}
				receipt, err := load[BindingRecord](t.Context(), r, "binding", old.ID)
				if err != nil {
					return err
				}
				receipt.SourceBaseID = "changed"
				return r.Put(t.Context(), "binding", old.ID, receipt)
			}); err != nil {
				t.Fatal(err)
			}
			if change == "mapping-base" {
				s.dependencyCache().invalidate("record:file:"+cursor.Holds[0].MappingID, false, false)
			} else if change == "binding-receipt" {
				s.dependencyCache().invalidate("record:binding:"+old.ID, false, false)
			}
			ready, _, err := s.layoutReadiness(t.Context(), *now)
			if err != nil || !ready {
				t.Fatal("exact dependency did not wake hold", ready, err)
			}
			more, moved, held, err := s.advanceLayouts(t.Context(), 32)
			if err != nil || more || moved != 0 || held != 1 || scans.fileLists != 1 || reader.reads["a.md"] != 2 || reader.reads["b.md"] != 1 {
				t.Fatal("dependency retry broadened work or accepted held evidence", more, moved, held, scans.fileLists, reader.reads, err)
			}
			if ready, _, err := s.layoutReadiness(t.Context(), *now); err != nil || ready {
				t.Fatal("unchanged dependency hot retry", ready, err)
			}
		})
	}
}

func TestLayoutRetryLegacyV3CatchupHappensOnlyOnce(t *testing.T) {
	s, reader, scans, _, now, _ := layoutRetryFixture(t)
	before := retainedLayoutCursor(t, s)
	deadline := now.Add(time.Minute)
	legacy := struct {
		After           string
		Complete, Retry bool
		RetryAt         time.Time
	}{before.After, true, true, deadline}
	if err := scans.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error { return r.Put(t.Context(), "cursor", layoutCursorID(s.Scopes[0]), legacy) }); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().reset() // Loading legacy persisted state models restart.
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || scans.fileLists != 1 || retainedLayoutCursor(t, s).After != before.After {
		t.Fatal("legacy deadline/cursor lost", err)
	}
	*now = deadline
	if more, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || more || moved != 0 || held != 1 || scans.fileLists != 2 {
		t.Fatal("legacy catch-up", more, moved, held, err)
	}
	after := retainedLayoutCursor(t, s)
	if !after.Complete || !after.RetryKnown || len(after.Holds) != 1 || !after.RetryAt.Equal(now.Add(6*time.Hour)) {
		t.Fatal(after)
	}
	*now = after.RetryAt
	if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 0 || scans.fileLists != 2 || reader.reads["b.md"] != 1 {
		t.Fatal("legacy catch-up repeated", moved, held, scans.fileLists, reader.reads, err)
	}
}

func TestLayoutRetryLegacyIncompleteCursorContinuesBeforeCatchup(t *testing.T) {
	s, _, scans, _, now, _ := layoutRetryFixture(t)
	ids, err := scans.Store.(*memoryRecords).List(t.Context(), "file", "", 32)
	if err != nil || len(ids) != 2 {
		t.Fatal(ids, err)
	}
	legacy := layoutCursor{After: ids[0], Retry: true, RetryAt: now.Add(time.Minute)}
	if err := scans.Store.WithReplica(t.Context(), s.Replica, s.Epoch, func(r Records) error {
		return r.Put(t.Context(), "cursor", layoutCursorID(s.Scopes[0]), legacy)
	}); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().reset()
	if _, _, _, err := s.advanceLayouts(t.Context(), 32); err != nil || s.LayoutInspected != 1 {
		t.Fatal("legacy continuation restarted at the beginning", s.LayoutInspected, err)
	}
	after := retainedLayoutCursor(t, s)
	if after.After != ids[1] || !after.Complete || after.RetryKnown || !after.Retry {
		t.Fatal("legacy continuation forgot undiscovered prior holds", after)
	}
}

func TestLayoutRetryMissingOriginStaysHeldAndExactOriginWakesIt(t *testing.T) {
	s, _, scans, _, now, _ := layoutRetryFixture(t)
	cursor := retainedLayoutCursor(t, s)
	r := scans.Store.(*memoryRecords)
	mapping, err := load[FileMapping](t.Context(), r, "file", cursor.Holds[0].MappingID)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce a legacy relabel with no intact origin or receipt. Neither
	// checkpoint migration nor a retry can manufacture either authorization.
	mapping.Latest.Generation = "origin-generation"
	mapping.Latest.ID = "layout_dir_missing_origin"
	mapping.Latest.EntryKind = "directory"
	if !mapping.Latest.valid() {
		t.Fatal("invalid regression fixture")
	}
	if err := r.Put(t.Context(), "file", cursor.Holds[0].MappingID, mapping); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().invalidate("record:file:"+cursor.Holds[0].MappingID, false, false)
	if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 1 || s.LayoutHoldReasons["mapping_origin_missing"] != 1 {
		t.Fatal("missing origin was not held explicitly", moved, held, s.LayoutHoldReasons, err)
	}
	if ready, _, err := s.layoutReadiness(t.Context(), *now); err != nil || ready {
		t.Fatal("missing unchanged origin hot retry", ready, err)
	}
	origin := mapping
	origin.Scope.Generation = mapping.Latest.Generation
	originID := mapKey(Intent{Workspace: origin.Scope.Workspace, Collection: origin.Scope.Collection, Generation: origin.Scope.Generation, FileID: origin.ClientFileID})
	if err := r.Put(t.Context(), "file", originID, origin); err != nil {
		t.Fatal(err)
	}
	s.dependencyCache().invalidate("record:file:"+originID, false, false)
	*now = now.Add(time.Second)
	if ready, _, err := s.layoutReadiness(t.Context(), *now); err != nil || !ready {
		t.Fatal("exact origin did not wake dependent hold", ready, err)
	}
	if _, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || moved != 0 || held != 1 || s.LayoutHoldReasons["mapping_origin_receipt_missing"] != 1 || scans.fileLists != 1 {
		t.Fatal("origin without receipt was accepted or discovery restarted", moved, held, s.LayoutHoldReasons, scans.fileLists, err)
	}
	if len(retainedLayoutCursor(t, s).Holds) != 1 {
		t.Fatal("unverified origin history forgotten")
	}
}

func TestLayoutRetryMissingMappingRemainsHeldAndTransientReadBacksOff(t *testing.T) {
	s, reader, scans, _, now, _ := layoutRetryFixture(t)
	cursor := retainedLayoutCursor(t, s)
	reader.fail = errors.New("temporary read failure")
	// A real source change re-admits work; merely changing a test double's
	// outcome is not a durable dependency and cannot release a stable hold.
	if err := reader.Service.Sources.WithSource(t.Context(), "source", "a.md", func(src notesworkspace.Source) error {
		return os.WriteFile(filepath.Join(src.Path, "a.md"), []byte("changed"), 0600)
	}); err != nil {
		t.Fatal(err)
	}
	*now = cursor.RetryAt
	if more, _, held, err := s.advanceLayouts(t.Context(), 32); err != nil || more || held != 1 || !s.LayoutRetryAt.Equal(now.Add(time.Minute)) || s.LayoutHoldReasons["source_read_unavailable"] != 1 {
		t.Fatal("transport backoff", more, held, s.LayoutRetryAt, s.LayoutHoldReasons, err)
	}
	for _, delay := range []time.Duration{2 * time.Minute, 4 * time.Minute, 4 * time.Minute} {
		*now = s.LayoutRetryAt
		if _, _, held, err := s.advanceLayouts(t.Context(), 32); err != nil || held != 1 || !s.LayoutRetryAt.Equal(now.Add(delay)) {
			t.Fatal("transport backoff was not bounded", delay, s.LayoutRetryAt, err)
		}
	}
	// Navigation is not authority to forget a missing retained mapping.
	delete(scans.Store.(*memoryRecords).data, "file:"+cursor.Holds[0].MappingID)
	s.dependencyCache().invalidate("record:file:"+cursor.Holds[0].MappingID, false, false)
	*now = now.Add(time.Second)
	if more, moved, held, err := s.advanceLayouts(t.Context(), 32); err != nil || more || moved != 0 || held != 1 || s.LayoutHoldReasons["mapping_missing"] != 1 || len(retainedLayoutCursor(t, s).Holds) != 1 {
		t.Fatal("missing mapping dropped", more, moved, held, s.LayoutHoldReasons, err)
	}
}
