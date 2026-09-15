package knowledge

import (
	"fmt"
	"strings"
	"testing"
)

func TestNotesArchiveSearchLifecycleQueryContracts(t *testing.T) {
	for _, filter := range []SourceLifecycleFilter{"", "active", "archived", "all"} {
		input := NotesSearchInput{Query: "cobalt", SourceLifecycle: filter, Limit: 2}
		lexical, err := buildNotesSearchQuery(input)
		if err != nil {
			t.Fatal(err)
		}
		semantic, err := buildSemanticNotesSearchQuery(input, EmbeddingSettings{}, []float32{1})
		if err != nil {
			t.Fatal(err)
		}
		for _, query := range []notesSearchQuery{lexical, semantic} {
			predicate := notesLifecycleSelectionSQL("ko", filter)
			if !strings.Contains(query.SQL, "AND "+predicate) || !strings.Contains(query.SQL, "notes_custody_projection_receipts") || !strings.Contains(query.SQL, "visibility_file.index_policy") {
				t.Fatal("lifecycle/current-evidence predicate missing")
			}
			if strings.Index(query.SQL, "AND "+predicate) > strings.LastIndex(query.SQL, "LIMIT $") {
				t.Fatal("lifecycle applied after candidate limit")
			}
		}
	}
	for _, filter := range []SourceLifecycleFilter{"latest", "ACTIVE", " archived", "archived' OR true --"} {
		input := NotesSearchInput{Query: "cobalt", SourceLifecycle: filter}
		if ValidateNotesSearchInput(input) == nil {
			t.Fatal("invalid lifecycle accepted")
		}
		if _, err := buildNotesSearchQuery(input); err == nil {
			t.Fatal("invalid lexical lifecycle accepted")
		}
		if _, err := buildSemanticNotesSearchQuery(input, EmbeddingSettings{}, []float32{1}); err == nil {
			t.Fatal("invalid semantic lifecycle accepted")
		}
	}
	parsed := ParseNotesSearchInput("cobalt lifecycle:archived")
	if parsed.Query != "cobalt" || parsed.SourceLifecycle != SourceLifecycleFilterArchived {
		t.Fatalf("parsed lifecycle: %+v", parsed)
	}
}

func TestNotesArchiveSearchGroupBoundsAndOmittedTruth(t *testing.T) {
	for limit := 1; limit <= 50; limit++ {
		for active := 0; active <= 55; active++ {
			for archived := 0; archived <= 55; archived++ {
				a, b := notesLifecycleGroupLimits(active, archived, limit)
				if a > active || b > archived || a+b != minInt(active+archived, limit) || a < 0 || b < 0 {
					t.Fatalf("group budget: %d/%d/%d -> %d/%d", active, archived, limit, a, b)
				}
				if active >= limit && archived >= limit && (a != (limit+1)/2 || b != limit/2) {
					t.Fatal("groups do not share budget")
				}
			}
		}
	}
	rows := []NotesSearchResult{{KnowledgeObjectID: "one"}, {KnowledgeObjectID: "one"}, {KnowledgeObjectID: "two"}}
	if n, truncated := notesPartitionMatchCount(rows, rows, 4); n != 2 || truncated {
		t.Fatalf("duplicate count: %d/%t", n, truncated)
	}
	if n, truncated := notesPartitionMatchCount(rows, nil, 3); n != 2 || !truncated {
		t.Fatal("candidate exhaustion represented as exact")
	}
	rows = nil
	for i := 0; i < 501; i++ {
		rows = append(rows, NotesSearchResult{KnowledgeObjectID: fmt.Sprint(i)})
	}
	if n, truncated := notesPartitionMatchCount(rows, nil, 1000); n != 500 || !truncated {
		t.Fatal("count cap not explicit")
	}
}
