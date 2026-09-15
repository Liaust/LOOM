package knowledge

import (
	"errors"
	"testing"
)

func TestNotesArchiveSurfacesRejectInvalidLifecycle(t *testing.T) {
	s := NewService(nil)
	for _, filter := range []SourceLifecycleFilter{"unknown", "Active", " all", "archived' OR true"} {
		_, err := s.store.ListKnowledgeObjects(t.Context(), KnowledgeObjectFilter{SourceLifecycle: filter})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("objects: %v", err)
		}
		_, err = s.store.GetKnowledgeObjectWithLifecycle(t.Context(), "exact", filter)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("get: %v", err)
		}
		_, err = s.store.ListReadableSourceRoots(t.Context(), SourceRootFilter{SourceLifecycle: filter})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("roots: %v", err)
		}
		_, err = s.GetNotesOverview(t.Context(), NotesOverviewInput{SourceLifecycle: filter})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("overview: %v", err)
		}
	}
}
