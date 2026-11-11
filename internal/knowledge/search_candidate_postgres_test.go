package knowledge

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestSearchCandidateInlineScopeEquivalencePostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/literal.md", "Only marker not_a_secret has a 50% reference and an exact needle phrase.")
	seedNotesArchiveSibling(t, f.s, f.root, "topic-one/literal.md", "A 50% reference and an exact needle phrase outside the selected folder.")
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	if _, err := f.s.store.db.Exec(`UPDATE search.search_documents SET metadata=metadata || '{"tags":["special_scope_tag"]}'::jsonb
 WHERE notes_object_id IN (SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE relative_path='neighbour/literal.md')`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	type result struct {
		ordinal          int
		document, object string
		truncated        bool
		score            float64
		unfiltered       bool
	}
	collect := func(query notesSearchQuery) []result {
		t.Helper()
		rows, err := tx.QueryContext(t.Context(), query.SQL, query.Args...)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := []result{}
		for rows.Next() {
			var r result
			if err := rows.Scan(&r.ordinal, &r.document, &r.object, &r.truncated, &r.score, &r.unfiltered); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, scope := range []NotesSearchInput{
		{SourceNodeKey: f.root.NodeKey}, {NotesSourceRootID: f.root.NotesSourceRootID},
		{Path: "neighbour/"}, {SourceCategory: "topics"}, {Tags: []string{"special_scope_tag"}},
		{ProjectID: "project_not_selected"}, {Before: "2020-01-01T00:00:00Z"},
	} {
		for _, text := range []string{"50% reference", "exact needle phrase", "special_scope_tag"} {
			input := scope
			input.Query, input.Phrases = `"`+text+`"`, []string{text}
			if text == "special_scope_tag" {
				input.Query, input.Phrases = text, nil
			}
			input.Mode, input.Limit, input.SourceLifecycle = NotesSearchModeLexical, 10, SourceLifecycleFilterActive
			current, err := buildIndexedNotesLexicalCandidates(input, notesLexicalVariants(input))
			if err != nil {
				t.Fatal(err)
			}
			previous := current
			previous.SQL = strings.Replace(previous.SQL, "scoped_documents AS NOT MATERIALIZED", "scoped_documents AS MATERIALIZED", 1)
			newTags := "sd WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1' AND (sd.metadata->'tags') ?| ARRAY(SELECT term FROM query_terms)"
			oldTags := "sd,query_terms qt WHERE sd.source_kind IN ('knowledge_chunk','knowledge_object_metadata') AND sd.index_version='knowledge_bm25_v1' AND (sd.metadata->'tags') ? qt.term"
			previous.SQL = strings.Replace(previous.SQL, newTags, oldTags, 1)
			if before, after := collect(previous), collect(current); !reflect.DeepEqual(before, after) {
				t.Fatalf("scope/literal rewrite changed candidates, scores or partial state: scope=%+v query=%q before=%+v after=%+v", scope, text, before, after)
			}
		}
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: `"50% reference"`, SourceNodeKey: f.root.NodeKey, Path: "neighbour/", Mode: NotesSearchModeLexical, Limit: 10})
	if err != nil || len(out.Results) != 1 || out.Results[0].RelativePath != "neighbour/literal.md" {
		t.Fatalf("literal source navigation or folder isolation changed: %+v %v", out, err)
	}
}

func TestSearchCandidateNaturalQuestionRelevanceAndScopesPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/unruh.md", "# Unruh acceleration\nAn accelerated observer measures thermal radiation in the Unruh effect.")
	for i := 0; i < 12; i++ {
		seedNotesArchiveSibling(t, f.s, f.root, fmt.Sprintf("neighbour/common-%02d.md", i), fmt.Sprintf("What can you tell me about ordinary notes? What can you tell me about general information? Fixture %d.", i))
	}
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	question := "What can you tell me about Unruh acceleration?"
	for _, scope := range []NotesSearchInput{{}, {Path: "neighbour/"}, {NotesSourceRootID: f.root.NotesSourceRootID}} {
		scope.Query, scope.Mode, scope.Limit = question, NotesSearchModeLexical, 1
		out, err := f.s.SearchNotes(t.Context(), scope)
		if err != nil || len(out.Results) != 1 || out.Results[0].RelativePath != "neighbour/unruh.md" || out.Query != question {
			t.Fatalf("natural question lost relevant source: %+v %v", out, err)
		}
		if hit := out.Results[0]; hit.BM25Score != 0 || hit.BoostScore != 0 || hit.FTSScore <= 0 || hit.LexicalMethod != "postgres_fts" || hit.lexicalFallback {
			t.Fatalf("native scoring attribution or meaningful-first selection changed: %+v", hit)
		}
		passage, err := f.s.GetNotesPassage(t.Context(), *out.Results[0].PassageFollowup)
		if err != nil || !strings.Contains(passage.Text, "thermal radiation") {
			t.Fatalf("relevance was not grounded in source: %+v %v", passage, err)
		}
	}
}

func TestSearchCandidateLiteralRoutesPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	long := strings.Repeat("x", 2100)
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/literal.md", "# Lab identifiers\nOnly marker not_a_secret has a 50% reference. A naive literal underscores report. The co instrument works. Reference https://example.org/records/10369330688/details.")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/decoy.md", "# Lab decoy\nA secret is not the same as the exact identifier. A 50 reference without punctuation is different.")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/oversize-a.md", long+"a")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/oversize-b.md", long+"b")
	seedNotesArchiveSibling(t, f.s, f.root, "neighbour/french.md", "On mesure la chaleur et l'acc\u00e9l\u00e9ration dans ce passage.")
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	if _, err := f.s.store.db.Exec(`UPDATE search.search_documents SET metadata=metadata || '{"tags":["special_tag"]}'::jsonb
 WHERE notes_object_id IN (SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE relative_path='neighbour/literal.md')`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"not_a_secret", `"50% reference"`, `"10369330688"`, "co", "nderscores"} {
		out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: query, Path: "neighbour/", Mode: NotesSearchModeLexical, Limit: 10})
		found := false
		for _, hit := range out.Results {
			found = found || hit.RelativePath == "neighbour/literal.md"
		}
		if err != nil || !found || (query != "co" && len(out.Results) != 1) {
			t.Fatalf("literal/phrase/short-token route %q: %+v %v", query, out, err)
		}
	}
	tagged, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: "lab", Tags: []string{"special_tag"}, Path: "neighbour/", Mode: NotesSearchModeLexical, Limit: 10})
	if err != nil || len(tagged.Results) != 1 || tagged.Results[0].RelativePath != "neighbour/literal.md" {
		t.Fatalf("underscore tag was treated as a required body identifier: %+v %v", tagged, err)
	}
	for _, tc := range []struct{ query, path string }{
		{long + "a", "neighbour/oversize-a.md"},
		{"chaleur acc\u00e9l\u00e9ration", "neighbour/french.md"},
		{"absentmarker la", "neighbour/french.md"},
		{"absentmarker on", "neighbour/french.md"},
		{`"neighbour/french.md"`, "neighbour/french.md"},
		{"neighbour/french.md", "neighbour/french.md"},
	} {
		out, err := f.s.SearchNotes(t.Context(), NotesSearchInput{Query: tc.query, Path: "neighbour/", Mode: NotesSearchModeLexical, Limit: 1})
		if err != nil || len(out.Results) != 1 || out.Results[0].RelativePath != tc.path {
			t.Fatalf("cross-language/oversize route %q: %+v %v", tc.query, out, err)
		}
	}
}

func TestSearchCandidateCappedUnpublishedPoolRemainsPartialPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	for i := 0; i < 3; i++ {
		seedNotesArchiveSibling(t, f.s, f.root, fmt.Sprintf("neighbour/selective-%d.md", i), fmt.Sprintf("A selective_zebra source. Fixture %d.", i))
	}
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	// Fixture-only mismatched publication leaves ranked candidates but no
	// eligible passages. No production root or source identity is changed.
	if _, err := f.s.store.db.Exec(`UPDATE knowledge.knowledge_objects SET lexical_version_id=NULL,source_revision='fixture-unpublished'
 WHERE relative_path LIKE 'neighbour/selective-%'`); err != nil {
		t.Fatal(err)
	}
	input := NotesSearchInput{Query: "selective_zebra", SourceLifecycle: SourceLifecycleFilterActive, Limit: 1, indexedBudget: 2}
	query, err := buildIndexedNotesLexicalCandidates(input, []notesLexicalVariant{{SourceLifecycleFilterActive, false, 1}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.store.db.QueryContext(t.Context(), query.SQL, query.Args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ordinal int
	var document, object string
	var truncated bool
	var score float64
	var unfiltered bool
	if !rows.Next() || rows.Scan(&ordinal, &document, &object, &truncated, &score, &unfiltered) != nil || !truncated || document != "" || object != "" {
		t.Fatal("capped denied pool looked like a complete empty search")
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatal("unexpected surviving unpublished passage")
	}
}

func TestSearchCandidateRefillAndAdmissionBoundPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	for i := 0; i < 5; i++ {
		seedNotesArchiveSibling(t, f.s, f.root, fmt.Sprintf("neighbour/zebra-%d.md", i), fmt.Sprintf("A selective zebra source. Fixture %d.", i))
	}
	reconcileBoxSyncedFixture(t, f.s, []SourceRoot{f.root})
	advanceBoxSyncedFixture(t, f.s)
	if _, err := f.s.store.db.Exec(`UPDATE files.file_metadata SET index_policy='private_no_index'
 WHERE object_id IN (SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE relative_path LIKE 'neighbour/zebra-%' AND relative_path<>'neighbour/zebra-4.md')`); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	input := NotesSearchInput{Query: "selective zebra", SourceLifecycle: SourceLifecycleFilterActive, Limit: 1, indexedBudget: 2, readTx: tx}
	if err := f.s.prepareIndexedNotesSearch(t.Context(), &input, NotesSearchModeLexical, "", EmbeddingSettings{}); err != nil {
		t.Fatal(err)
	}
	key := notesLexicalVariant{SourceLifecycleFilterActive, false, 1}
	if input.indexedBudget <= 2 || len(input.indexedAdmission.checked) != 5 {
		t.Fatalf("refill did not reuse bounded admission: budget=%d checked=%v", input.indexedBudget, input.indexedAdmission.checked)
	}
	raw, err := notesIndexedWantedJSON(input, []notesLexicalVariant{key}, false)
	if err != nil || strings.Count(raw, `"id"`) != 1 {
		t.Fatalf("refill hydrated denied passages: %s %v", raw, err)
	}
}
