package knowledge

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

func TestNotesLexicalBatchPartitions(t *testing.T) {
	input := NotesSearchInput{Query: "cobalt observatory", SourceLifecycle: SourceLifecycleFilterActive, RequireCurrent: true, Limit: 3}
	want := []notesLexicalVariant{
		{SourceLifecycleFilterActive, false, 50},
		{SourceLifecycleFilterActive, true, 3},
		{SourceLifecycleFilterArchived, false, 50},
		{SourceLifecycleFilterArchived, true, 50},
	}
	if got := notesLexicalVariants(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("partitions: %#v", got)
	}
	query, err := buildNotesSearchVariantsQuery(input, want)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"visible_objects AS MATERIALIZED", "search_corpus AS MATERIALIZED", "CROSS JOIN LATERAL", "search_lifecycle=request.lifecycle", "NOT request.require_current OR publication_current", "LIMIT request.candidate_limit"} {
		if !strings.Contains(query.SQL, fragment) {
			t.Fatalf("missing %q", fragment)
		}
	}
	if strings.Count(query.SQL, "body_matches AS MATERIALIZED") != 1 || strings.Contains(query.SQL, "AS normalized_body") {
		t.Fatal("partitions must share indexed body matching, not normalize the entire corpus")
	}
}

func TestNotesIndexedLexicalParityPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	if _, err := f.s.store.db.Exec(`UPDATE search.search_documents SET
	 body=body || ' A literal not_a_secret, naïve café, and 50% reference.',
	 metadata=jsonb_set(metadata,'{tags}','["specialtag"]'::jsonb)
	 WHERE source_kind IN ('knowledge_chunk','knowledge_object_metadata')`); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"cobalt observatory", "obalt", "co", "not_a_secret", "not_b_secret", "naïve", "specialtag", `"cobalt observatory"`, `"50% reference"`, `"topic-one"`, "no-such-phrase"} {
		t.Run(text, func(t *testing.T) {
			input := NotesSearchInput{Query: text, SourceLifecycle: SourceLifecycleFilterAll, Limit: 50}
			if strings.HasPrefix(text, `"`) {
				input.Phrases = []string{strings.Trim(text, `"`)}
			}
			query, err := buildNotesSearchQuery(input)
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.s.searchLexicalNotes(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			// Freeze the pre-optimization match expressions as the behavior oracle.
			query.SQL = strings.Replace(query.SQL, notesLexicalDocumentMatchesSQL(), legacyNotesLexicalDocumentMatchesSQL, 1)
			rows, err := f.s.store.db.QueryContext(t.Context(), query.SQL, query.Args...)
			if err != nil {
				t.Fatal(err)
			}
			want := []NotesSearchResult{}
			for rows.Next() {
				r, err := scanNotesSearchResult(rows)
				if err != nil {
					rows.Close()
					t.Fatal(err)
				}
				r.LexicalRank = len(want) + 1
				r.Citation = notesSearchCitation(r)
				want = append(want, r)
			}
			err = rows.Err()
			rows.Close()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("indexed matching changed results: got=%+v want=%+v err=%v", got, want, err)
			}
		})
	}
}

const legacyNotesLexicalDocumentMatchesSQL = `SELECT fd.search_document_id, fd.source_kind, fd.knowledge_object_id,
 fd.knowledge_object_version_id, fd.knowledge_chunk_id, fd.relative_path,
 fd.lexical_document_id, fd.document_length, fd.search_lifecycle, fd.publication_current,
 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.title,'')), qt.term)>0) AS title_match,
 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.structural_path,'')), qt.term)>0) AS heading_match,
 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(lower(COALESCE(fd.relative_path,'') || ' ' || COALESCE(fd.source_path,'')), qt.term)>0) AS path_match,
 EXISTS (SELECT 1 FROM query_terms qt WHERE (matched_source.metadata->'tags') ? qt.term) AS tag_match,
 EXISTS (SELECT 1 FROM query_terms qt WHERE strpos(body_text.normalized_body,qt.term)>0) AS body_match,
 EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(fd.title,'') || ' ' || COALESCE(fd.structural_path,'') || ' ' || COALESCE(fd.relative_path,'') || ' ' || COALESCE(matched_source.body,'')),qp.phrase)>0) AS phrase_match,
 NOT EXISTS (SELECT 1 FROM query_phrases qp WHERE strpos(lower(COALESCE(fd.title,'') || ' ' || COALESCE(fd.structural_path,'') || ' ' || COALESCE(fd.relative_path,'') || ' ' || COALESCE(matched_source.body,'')),qp.phrase)=0) AS phrases_match,
 CASE WHEN matched_source.tsv @@ websearch_to_tsquery('simple',$1)
 THEN ts_rank_cd(matched_source.tsv,websearch_to_tsquery('simple',$1)) ELSE 0.0 END AS fts_score
 FROM raw_corpus fd
 JOIN search.search_documents matched_source ON matched_source.search_document_id=fd.search_document_id
 CROSS JOIN LATERAL (SELECT lower(COALESCE(matched_source.body,'')) AS normalized_body OFFSET 0) body_text`

func TestNotesLexicalBatchParityPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	check := func(t *testing.T) {
		for _, lifecycle := range []SourceLifecycleFilter{SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
			input := NotesSearchInput{Query: "cobalt observatory", SourceLifecycle: lifecycle, RequireCurrent: true, Limit: 3}
			tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			input.readTx = tx
			input.visibleObjectIDsJSON, err = f.s.loadNotesVisibleObjectIDs(t.Context(), input)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			batch, err := f.s.loadNotesLexicalBatch(t.Context(), input)
			if err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			for key, got := range batch {
				single := input
				single.visibleObjectIDsJSON = ""
				single.SourceLifecycle, single.RequireCurrent, single.Limit = key.Lifecycle, key.Current, key.Limit
				want, err := f.s.searchLexicalNotes(t.Context(), single)
				if err != nil || !reflect.DeepEqual(got, want) {
					tx.Rollback()
					t.Fatalf("partition %+v differs: got=%+v want=%+v err=%v", key, got, want, err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("active", check)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	t.Run("archived", check)
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	t.Run("restored", check)
}

func TestNotesEmbeddingAvailabilityPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSearchEmbeddings(t, f)
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if available, err := f.s.store.HasActiveCurrentEmbeddings(t.Context(), settings); err != nil || !available {
		t.Fatalf("published embeddings unavailable: %t %v", available, err)
	}
	settings.ModelKey = "unpublished-test-model"
	if available, err := f.s.store.HasActiveCurrentEmbeddings(t.Context(), settings); err != nil || available {
		t.Fatalf("wrong model accepted: %t %v", available, err)
	}
}

func TestNotesSemanticBatchParityPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	seedNotesArchiveSearchEmbeddings(t, f)
	settings, err := f.s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	vector := make([]float32, settings.Dimensions)
	for i := range vector {
		vector[i] = 1
	}
	check := func(t *testing.T) {
		for _, mode := range []string{NotesSearchModeSemantic, NotesSearchModeHybrid} {
			input := NotesSearchInput{Query: "cobalt observatory", Mode: mode, SourceLifecycle: SourceLifecycleFilterAll, RequireCurrent: true, Limit: 3}
			tx, err := f.s.store.db.BeginTx(t.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			input.readTx = tx
			input.visibleObjectIDsJSON, err = f.s.loadNotesVisibleObjectIDs(t.Context(), input)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := f.s.loadNotesSemanticBatch(t.Context(), input, settings, vector)
			if err != nil {
				t.Fatal(err)
			}
			for key, got := range batch {
				single := input
				single.visibleObjectIDsJSON = ""
				single.SourceLifecycle, single.RequireCurrent, single.Limit = key.Lifecycle, key.Current, key.Limit
				want, err := f.s.searchSemanticNotesVector(t.Context(), single, settings, vector)
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("%s partition %+v differs: got=%+v want=%+v err=%v", mode, key, got, want, err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Run("active", check)
	f.archive(t)
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	t.Run("archived", check)
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); err != nil {
		t.Fatal(err)
	}
	t.Run("restored", check)
}
