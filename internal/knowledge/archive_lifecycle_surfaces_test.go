package knowledge

import (
	"errors"
	"strings"
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

func TestSourceRootReadableAuthorityAndCountsAreStatementLocal(t *testing.T) {
	for _, lifecycle := range []SourceLifecycleFilter{SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
		query := notesReadableRootsQuerySQL("node_key=$1 AND notes_source_root_id=$2", lifecycle, false)
		for _, relation := range []string{"selected_roots", "authorized_roots", "visible_objects", "lifecycle_counts"} {
			if strings.Count(query, relation+" AS MATERIALIZED (") != 1 {
				t.Fatal("missing single statement-local relation", relation)
			}
		}
		if strings.Count(query, "projects.declaration_watch_binding_matches(") != 1 {
			t.Fatal("registration binding must be evaluated only in root authority")
		}
		if strings.Count(query, "projects.declaration_watch_read_sources(") != 1 {
			t.Fatal("retained compiler receipts must also be evaluated once per root")
		}
		objects := query[strings.Index(query, "visible_objects AS MATERIALIZED ("):]
		if strings.Contains(objects, "projects.declaration_watch_binding_matches(") || strings.Contains(objects, "projects.declaration_application_data_matches(") || strings.Contains(objects, "JOIN projects.declaration_operations") || strings.Contains(objects, "projects.declaration_watch_source_matches(") {
			t.Fatal("per-object visibility expanded registration authority")
		}
		if strings.Count(query, "FROM visible_objects GROUP BY notes_source_root_id") != 1 || strings.Contains(query, "SELECT count(*) FROM (") {
			t.Fatal("existence/counts repeated object visibility")
		}
		if !strings.Contains(objects, "private_no_index") || !strings.Contains(objects, "source_presence IN ('missing','unavailable')") || !strings.Contains(objects, "notes_custody_transitions") {
			t.Fatal("object privacy/presence/custody fences lost")
		}
	}
}
