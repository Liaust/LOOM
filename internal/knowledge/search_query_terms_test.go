package knowledge

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIndexedNotesQueryTermsPreserveLiteralBoundaries(t *testing.T) {
	input := NotesSearchInput{Query: `How does F1 NOT_A_SECRET relate to "to be"?`, Tags: []string{"in"}, Path: "the/path"}
	got := map[string]bool{}
	for _, term := range notesQueryTerms(input) {
		got[term.Term] = term.Protected
	}
	for _, term := range []string{"f1", "not_a_secret", "to", "be", "in"} {
		if !got[term] {
			t.Fatalf("literal %q can be filtered: %+v", term, got)
		}
	}
	if got["how"] || got["does"] || got["relate"] || len(got) != 8 {
		t.Fatalf("ordinary words or explicit path changed interpretation: %+v", got)
	}
	for _, term := range []string{"in", "or", "was"} {
		got := notesQueryTerms(NotesSearchInput{Query: term})
		if len(got) != 1 || !got[0].Protected {
			t.Fatalf("standalone literal lost: %+v", got)
		}
	}
	long := strings.Repeat("x", 2200)
	gotLong := notesQueryTerms(NotesSearchInput{Query: long})
	if len(gotLong) != 1 || gotLong[0].Term != long || !strings.HasPrefix(gotLong[0].Key, "sha256:") || !gotLong[0].Protected {
		t.Fatalf("oversize literal/hash identity lost: %+v", gotLong)
	}
}

func TestIndexedNotesPreparedQueryReusedWithoutDictionary(t *testing.T) {
	input := NotesSearchInput{Query: "  How does acceleration relate to temperature?  ", preparedQuery: `{"terms":[],"discovery_terms":["acceleration"],"query":"'acceleration'","fallback_query":"'acceleration'"}`}
	cte := notesIndexedQueryCTE(input)
	if strings.Contains(cte, "ts_lexize") || strings.Contains(cte, "plainto_tsquery") || !strings.Contains(cte, "'query')::tsquery") {
		t.Fatal("prepared query was reinterpreted")
	}
	if args := notesIndexedQueryArgs(input); args[0] != input.Query || args[1] != input.preparedQuery || notesSearchDisplayQuery(input) != input.Query {
		t.Fatal("original query or shared prepared payload changed")
	}
}

func TestIndexedNotesSimpleDictionaryInterpretationPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tc := range []struct {
		query string
		terms []string
	}{
		{"How does the Unruh effect relate acceleration to temperature?", []string{"acceleration", "effect", "relate", "temperature", "unruh"}},
		{"accelerations and temperatures", []string{"accelerations", "temperatures"}},
		{`"to be" acceleration`, []string{"acceleration", "be", "to"}},
		{"to be", []string{"be", "to"}},
		{"was", []string{"was"}},
		{"in_stock and F1", []string{"f1", "in_stock"}},
		{"chaleur acc\u00e9l\u00e9ration", []string{"acc\u00e9l\u00e9ration", "chaleur"}},
	} {
		input := NotesSearchInput{Query: tc.query, readTx: tx}
		if err := f.s.prepareNotesNativeQuery(t.Context(), &input); err != nil {
			t.Fatal(err)
		}
		var prepared struct {
			Terms []string `json:"discovery_terms"`
			Query string   `json:"query"`
		}
		if err := json.Unmarshal([]byte(input.preparedQuery), &prepared); err != nil || !reflect.DeepEqual(prepared.Terms, tc.terms) {
			t.Fatalf("%q: %+v, want %v: %v", tc.query, prepared, tc.terms, err)
		}
		var matches bool
		if err := tx.QueryRowContext(t.Context(), `SELECT to_tsvector('simple',$1) @@ $2::tsquery`, tc.query, prepared.Query).Scan(&matches); err != nil || !matches {
			t.Fatalf("unstemmed index no longer matches %q: %v", tc.query, err)
		}
	}
	for _, tc := range []struct {
		query, body, contains string
		plain                 bool
	}{
		{"How does the Unruh effect relate acceleration to temperature?", strings.Repeat("In the preparation for the exercise there is general background. ", 20) + "The Unruh effect relates acceleration to temperature for an accelerated observer. The thermal spectrum is the evidence discussed here, not a proposed experiment.", "Unruh", false},
		{`"horizontal throw"`, strings.Repeat("General background and orientation notes precede the worked example. ", 20) + "The horizontal throw experiment measures a projectile trajectory from the release point to the impact position. This example uses the F1 manual's apparatus and measurement procedure.", "horizontal throw", false},
		{"acceleration temperature", "This passage discusses a memory palace proposal. It proposes a future experiment; it does not report a completed experiment or a temperature measurement.", "proposes a future experiment", false},
		{"unmatchedmarker", "This semantic-only passage describes an accelerated observer measuring thermal radiation. It has no lexical overlap with the query, but remains readable evidence.", "semantic-only passage", true},
	} {
		input := NotesSearchInput{Query: tc.query, readTx: tx}
		if err := f.s.prepareNotesNativeQuery(t.Context(), &input); err != nil {
			t.Fatal(err)
		}
		args := append(notesIndexedQueryArgs(input), tc.body)
		var excerpt string
		if err := tx.QueryRowContext(t.Context(), `WITH `+notesIndexedQueryCTE(input)+` SELECT `+notesIndexedExcerptSQL("$4::text")+` FROM native_query nq`, args...).Scan(&excerpt); err != nil {
			t.Fatal(err)
		}
		plain := compactFileExcerpt(excerpt, 700)
		if !strings.Contains(plain, tc.contains) || (tc.plain && excerpt != tc.body) {
			t.Fatalf("excerpt lost actual source context: %q", excerpt)
		}
	}
}

func TestIndexedNotesBothExcerptLanesReuseDiscoveryQuery(t *testing.T) {
	input := NotesSearchInput{Query: "How does Unruh relate to temperature?", indexed: &notesIndexedSearch{},
		preparedQuery: `{"terms":[],"discovery_terms":["unruh","temperature"],"query":"'unruh' | 'temperature'","fallback_query":"'how' | 'unruh' | 'temperature'"}`}
	lexical, err := buildIndexedNotesLexicalHydration(input, notesLexicalVariants(input))
	if err != nil {
		t.Fatal(err)
	}
	semantic, err := buildIndexedNotesSemanticHydration(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, notesLexicalVariants(input))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []notesSearchQuery{lexical, semantic} {
		if query.Args[0] != input.Query || query.Args[1] != input.preparedQuery || strings.Contains(query.SQL, "ts_lexize") || !strings.Contains(query.SQL, "nq.query,'MaxFragments=1, MaxWords=56, MinWords=24, ShortWord=0'") {
			t.Fatal("excerpt lane lost prepared discovery interpretation or context")
		}
	}
}
