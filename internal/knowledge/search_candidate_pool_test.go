package knowledge

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchCandidateBoundsServingChecksAndLiteralFallback(t *testing.T) {
	for _, input := range []NotesSearchInput{
		{Query: "What can you tell me about Unruh acceleration?"},
		{Query: "co"}, {Query: "not_a_secret"}, {Query: `"50% reference"`, Phrases: []string{"50% reference"}},
		{Query: "naive", ProjectID: "project_test", Path: "physics", Tags: []string{"lab"}, After: "2026-10-01T00:00:00Z"},
	} {
		input.Limit, input.SourceLifecycle = 4, SourceLifecycleFilterActive
		query, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
		if err != nil {
			t.Fatal(err)
		}
		eligible := strings.Index(query.SQL, "eligible AS MATERIALIZED")
		if eligible < 0 {
			t.Fatal("missing bounded eligibility boundary")
		}
		before := query.SQL[:eligible]
		for _, forbidden := range []string{"latest_version", "observed_file", "pipeline_runs", "lt.term LIKE"} {
			if strings.Contains(before, forbidden) {
				t.Fatalf("%q ran before the candidate cap", forbidden)
			}
		}
		for _, required := range []string{"LIMIT request.candidate_limit", "PARTITION BY ordinal,knowledge_object_id", "pool_status AS MATERIALIZED", "LEFT JOIN eligible result", "object_position>"} {
			if !strings.Contains(query.SQL, required) {
				t.Fatalf("missing bounded/partial/diverse retrieval: %q", required)
			}
		}
		if !strings.Contains(query.SQL, "FROM literal_terms qt CROSS JOIN LATERAL") || !strings.Contains(query.SQL, "qt.term ~ '[[:alnum:]]{3}'") || !strings.Contains(query.SQL, "(SELECT count(*) FROM all_query_terms)=1") || strings.Contains(query.SQL, "lt.term LIKE") {
			t.Fatal("literal fallback lost the selective indexed document route")
		}
		if strings.Count(query.SQL, "lower(sd.body) LIKE") != 1 || !strings.Contains(query.SQL, "LIMIT 128\n ) hit") {
			t.Fatal("document trigram fallback must have its own relevance-ordered bound")
		}
		if query.Args[0] != input.Query {
			t.Fatal("candidate preparation rewrote the original query")
		}
	}
}

func TestSearchCandidateLiteralFallbackUsesExistingDocumentIndexes(t *testing.T) {
	input := NotesSearchInput{Query: "unruh", SourceLifecycle: SourceLifecycleFilterActive, Limit: 4}
	query, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"qt.key<>qt.term AND lt.term=qt.key", "sd.notes_fields LIKE", "lower(sd.body) LIKE",
		"(sd.metadata->'tags') ?| ARRAY(SELECT term FROM query_terms)",
		"sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1'",
		"ORDER BY ts_rank_cd(sd.tsv,nq.query,32) DESC", "LIMIT 128\n ) hit",
	} {
		if !strings.Contains(query.SQL, required) {
			t.Fatalf("missing selective indexed fallback guard: %q", required)
		}
	}
	if strings.Contains(query.SQL, "lt.term LIKE") || strings.Contains(query.SQL, "strpos(lt.term") {
		t.Fatal("underfilled exact query scanned the B-tree postings table for substrings")
	}
	if strings.Contains(query.SQL, "sd,query_terms qt") {
		t.Fatal("tag membership must use an indexable query array, not one probe per document and term")
	}
	if !strings.Contains(query.SQL, "char_length(term)<3") || !strings.Contains(query.SQL, "::text || ':*'") || !strings.Contains(query.SQL, "qt.term ~ '[[:alnum:]]{3}'") {
		t.Fatal("short token lost its indexed route or fell into a nonselective trigram scan")
	}
}

func TestSearchCandidateHydrationCarriesIndexedRelevanceAndAdmission(t *testing.T) {
	key := notesLexicalVariant{SourceLifecycleFilterActive, true, 1}
	input := NotesSearchInput{Query: "What can you tell me about Unruh?", indexed: &notesIndexedSearch{
		lexical:         map[notesLexicalVariant][]string{key: {"denied", "selective", "common"}},
		lexicalScores:   map[string]float64{"denied": 9, "selective": 8.5, "common": 0.1},
		documentObjects: map[string]string{"denied": "private", "selective": "source", "common": "other"},
		visible:         map[string]bool{"source": true, "other": true},
	}}
	raw, err := notesIndexedWantedJSON(input, []notesLexicalVariant{key}, false)
	if err != nil {
		t.Fatal(err)
	}
	var wanted []notesIndexedWanted
	if err := json.Unmarshal([]byte(raw), &wanted); err != nil {
		t.Fatal(err)
	}
	if len(wanted) != 2 || wanted[0].ID != "selective" || wanted[0].Score != 8.5 || wanted[1].Score != 0.1 {
		t.Fatalf("lost indexed relevance or admitted-only bound: %+v", wanted)
	}
	if notesSearchDisplayQuery(input) != input.Query {
		t.Fatal("display query lost the natural-language question")
	}
}
