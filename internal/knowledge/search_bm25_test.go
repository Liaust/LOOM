package knowledge

import (
	"strings"
	"testing"
)

func TestNotesBM25CandidateQuery(t *testing.T) {
	input := NotesSearchInput{Query: "How does the Unruh effect relate acceleration to temperature?", Limit: 4,
		ProjectID: "project_exact", Path: "Physics", RequireCurrent: true, SourceLifecycle: SourceLifecycleFilterActive}
	query, err := buildBM25NotesLexicalCandidates(input, []notesLexicalVariant{{SourceLifecycleFilterActive, true, 4}})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"knowledge.notes_bm25_documents", "paradedb.disjunction_max", "paradedb.match('body',$4::text)", "pdb.score(d.id)", "d.id_sort LIMIT", "d.project_id=$5::text", "d.path ILIKE $6::text", "d.is_current", "candidate_pool AS MATERIALIZED", "pool_status AS MATERIALIZED", "notes_fields", "pipeline_runs", "latest_file"} {
		if !strings.Contains(query.SQL, part) {
			t.Errorf("missing %s", part)
		}
	}
	for _, value := range []string{input.Query, input.ProjectID, input.Path} {
		if strings.Contains(query.SQL, value) {
			t.Errorf("user value interpolated: %s", value)
		}
	}
	if strings.Contains(query.SQL, "ts_rank") || strings.Contains(query.SQL, "LN(") {
		t.Fatal("LOOM must not recompute BM25")
	}
}

func TestNotesBM25LiteralRouting(t *testing.T) {
	for _, input := range []NotesSearchInput{{Query: "ab"}, {Query: "file/path.md"}, {Query: "exact_identifier"}, {Query: "10369330688", Phrases: []string{"10369330688"}}, {Query: "tag", Tags: []string{"tag"}}} {
		if !notesBM25LiteralQuery(input) {
			t.Errorf("literal route missing: %+v", input)
		}
	}
	for _, query := range []string{"Unruh", "How does acceleration relate to temperature?", "memory palace"} {
		if notesBM25LiteralQuery(NotesSearchInput{Query: query}) {
			t.Errorf("ordinary question sent to literal route: %s", query)
		}
	}
}

func TestNotesBM25ScoreAttribution(t *testing.T) {
	input := NotesSearchInput{indexed: &notesIndexedSearch{bm25: true, lexicalScores: map[string]float64{"doc": 12.5}}}
	result := NotesSearchResult{SearchDocumentID: "doc", FTSScore: 12.5}
	annotateIndexedNotesLexicalResult(input, &result)
	if result.LexicalMethod != "pg_search_bm25" || result.BM25Score != 12.5 || result.FTSScore != 0 {
		t.Fatalf("incorrect engine attribution: %+v", result)
	}
}

func TestNotesBM25ExcerptsOnlySelectedIDs(t *testing.T) {
	input := NotesSearchInput{Query: "How does Unruh relate to acceleration?", Limit: 4,
		preparedQuery: `{"discovery_terms":["unruh","acceleration"]}`,
		indexed:       &notesIndexedSearch{bm25: true}}
	variants := []notesLexicalVariant{{SourceLifecycleFilterActive, true, 4}}
	query, err := buildIndexedNotesLexicalHydration(input, variants)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"bm25_excerpts AS MATERIALIZED", "paradedb.snippet(d.body", "max_num_chars=>700",
		"d.id=ANY(ARRAY(SELECT DISTINCT id FROM wanted))", "LIMIT (SELECT count(DISTINCT id) FROM wanted)",
		"LEFT JOIN bm25_excerpts bx ON bx.id=sd.search_document_id", "COALESCE(NULLIF(bx.excerpt,''),CASE"} {
		if !strings.Contains(query.SQL, fragment) {
			t.Fatalf("missing bounded engine excerpt contract: %s", fragment)
		}
	}
	if query.Args[len(query.Args)-1] != "unruh acceleration" || strings.Contains(query.SQL, "unruh") {
		t.Fatal("excerpt did not reuse parameterized discovery terms")
	}
	input.indexed.bm25 = false
	query, err = buildIndexedNotesLexicalHydration(input, variants)
	if err != nil || strings.Contains(query.SQL, "paradedb") {
		t.Fatalf("native rollback depends on BM25 snippets: %v", err)
	}
}
