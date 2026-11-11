package knowledge

import (
	"strings"
	"testing"
)

func TestNotesBM25ProjectionPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	var count int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_bm25_documents`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("projection absent: %d %v", count, err)
	}
	var id, object string
	if err := f.s.store.db.QueryRow(`SELECT id,object_id FROM knowledge.notes_bm25_documents WHERE chunk_id<>'' LIMIT 1`).Scan(&id, &object); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE search.search_documents SET body=body || ' bm25_fixture_marker' WHERE search_document_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := f.s.store.db.QueryRow(`SELECT body FROM knowledge.notes_bm25_documents WHERE id=$1`, id).Scan(&body); err != nil || !strings.Contains(body, "bm25_fixture_marker") {
		t.Fatalf("text update missing: %v", err)
	}
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET title='BM25 updated title' WHERE knowledge_object_id=$1`, object); err != nil {
		t.Fatal(err)
	}
	var title string
	if err := f.s.store.db.QueryRow(`SELECT title FROM knowledge.notes_bm25_documents WHERE id=$1`, id).Scan(&title); err != nil || title != "BM25 updated title" {
		t.Fatalf("navigation update missing: %v", err)
	}
	if _, err := f.s.store.db.Exec(`DELETE FROM search.search_documents WHERE search_document_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_bm25_documents WHERE id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("withdrawal not reflected: %v", err)
	}
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_bm25_documents WHERE lifecycle='archived'`).Scan(&count); err != nil || count == 0 {
		t.Fatalf("archive state not reflected: %v", err)
	}
}

func TestNotesBM25EnginePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	var available bool
	if err := f.s.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_available_extensions WHERE name='pg_search' AND default_version='0.25.11')`).Scan(&available); err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Skip("pinned pg_search is not installed")
	}
	if _, err := f.s.store.db.Exec(`CREATE EXTENSION IF NOT EXISTS pg_search VERSION '0.25.11'`); err != nil {
		t.Fatal(err)
	}
	f.s.notesSearchBackend = NotesSearchBackendBM25
	if err := f.s.PrepareNotesSearchBackend(t.Context()); err != nil {
		t.Fatal(err)
	}
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/unruh.md", "# Unruh acceleration\nAn accelerated observer measures thermal radiation in the Unruh effect.")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/common.md", "An ordinary temperature measurement involves thermal radiation and relates to heat.")
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	// Filename/title/heading repetition must not add independent BM25 evidence.
	// Compare against additive matching, not exact floating-point BM25 scores:
	// the engine may refresh segment statistics between statements.
	terms := "unruh effect relate acceleration temperature"
	additive := map[string]float64{}
	{
		rows, err := f.s.store.db.Query(`SELECT id,pdb.score(id)::double precision
 FROM knowledge.notes_bm25_documents WHERE body ||| $1::text OR title ||| $1::text
 OR heading ||| $1::text OR path ||| $1::text`, terms)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var score float64
			if err := rows.Scan(&id, &score); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			additive[id] = score
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	input := NotesSearchInput{Query: terms, Limit: 4, SourceLifecycle: SourceLifecycleFilterActive}
	query, err := buildBM25NotesLexicalCandidates(input, []notesLexicalVariant{{SourceLifecycleFilterActive, false, 4}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.store.db.Query(query.SQL, query.Args...)
	if err != nil {
		t.Fatal(err)
	}
	repetitionRemoved := false
	for rows.Next() {
		var ordinal int
		var id, object string
		var truncated, fallback bool
		var score float64
		if err := rows.Scan(&ordinal, &id, &object, &truncated, &score, &fallback); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if id != "" && score < .8*additive[id] {
			repetitionRemoved = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !repetitionRemoved {
		t.Fatal("repeated fields still use additive BM25 evidence")
	}
	for _, scope := range []NotesSearchInput{{}, {Path: "neighbour/"}, {NotesSourceRootID: f.root.NotesSourceRootID}} {
		scope.Query = "How does the Unruh effect relate acceleration to temperature?"
		scope.Mode = NotesSearchModeLexical
		scope.Limit = 1
		out, err := f.s.SearchNotes(t.Context(), scope)
		if err != nil || len(out.Results) != 1 || out.Results[0].RelativePath != "neighbour/unruh.md" {
			t.Fatalf("BM25 relevant result missing: %+v %v", out, err)
		}
		if !strings.Contains(out.Results[0].Snippet, "accelerated observer") || !strings.Contains(out.Results[0].Snippet, "Unruh") {
			t.Fatalf("engine excerpt lost the selected source evidence: %q", out.Results[0].Snippet)
		}
	}
}
