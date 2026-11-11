package knowledge

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFileSearchNoticePreservesPartialAndFallbackQualifications(t *testing.T) {
	for _, fallback := range []string{"", "semantic_runtime_unavailable"} {
		for _, partial := range []bool{false, true} {
			notice := fileSearchNotice(NotesSearchResultSet{FallbackReason: fallback, CandidateRetrievalTruncated: partial})
			if (fallback != "" && !strings.Contains(notice, fallback)) || strings.Contains(notice, "Results are bounded") != partial {
				t.Fatalf("lost honest continuation: fallback=%q partial=%t notice=%q", fallback, partial, notice)
			}
		}
	}
}

func TestFileSearchCompactSourceExcerpt(t *testing.T) {
	if got := compactFileExcerpt(`&quot;memory&quot; &amp; &lt;literal&gt;`, 700); got != `"memory" & <literal>` {
		t.Fatalf("engine HTML escaped text leaked into plain excerpt: %q", got)
	}
	if got := compactFileExcerpt("  <b>thermal</b> <mark>radiation</mark> <loom-hit>source</loom-hit>  ", 700); got != "thermal radiation source" {
		t.Fatalf("unexpected compact source excerpt: %q", got)
	}
	text := strings.Repeat("\u03bb", 710)
	got := compactFileExcerpt(text, 700)
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 703 || !strings.HasSuffix(got, "...") {
		t.Fatal("source excerpt truncation split UTF-8 or exceeded its bound")
	}
}

func TestFileSearchDiversifiesScopedAndArchivedCandidates(t *testing.T) {
	for _, lifecycle := range []SourceLifecycleFilter{SourceLifecycleFilterActive, SourceLifecycleFilterArchived, SourceLifecycleFilterAll} {
		input := NotesSearchInput{Query: "thermal radiation", ProjectID: "project_test", fileView: true, RequireCurrent: true, SourceLifecycle: lifecycle, Limit: 4}
		variants := notesLexicalVariants(input)
		lexical, err := buildIndexedNotesLexicalCandidates(input, variants)
		if err != nil || !strings.Contains(lexical.SQL, "AND object_position <= 4") {
			t.Fatalf("one file can consume scoped lexical pool: %v", err)
		}
		semantic, err := buildIndexedNotesSemanticCandidates(input, EmbeddingSettings{Dimensions: 3}, []float32{1, 2, 3}, variants)
		if err != nil || strings.Count(semantic.SQL, "diverse WHERE object_position<=4") != len(variants) {
			t.Fatalf("one file can consume scoped semantic pool: %v", err)
		}
		eligible := strings.Index(semantic.SQL, "eligible AS MATERIALIZED")
		if eligible < 0 || strings.Contains(semantic.SQL[:eligible], "latest_version") || strings.Contains(semantic.SQL[:eligible], "kc.chunk_text") {
			t.Fatal("semantic file publication/body checks escaped the cap")
		}
	}
}

func TestFileSearchMetadataOnlyQualificationPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	advanceBoxSyncedFixture(t, s)
	if _, err := s.store.db.Exec(`UPDATE search.search_documents SET metadata=metadata || '{"metadata_only":true,"text_source":"metadata_text"}'::jsonb
 WHERE notes_object_id IN (SELECT knowledge_object_id FROM knowledge.knowledge_objects WHERE notes_source_root_id=$1)`, roots[0].NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	out, err := s.SearchFiles(t.Context(), NotesSearchInput{Query: "cobalt observatory", NotesSourceRootID: roots[0].NotesSourceRootID, Mode: NotesSearchModeLexical, Limit: 4})
	if err != nil || len(out.Results) != 1 || out.Results[0].Content != "metadata_only" || out.Results[0].Excerpt != "" {
		t.Fatalf("reference-only source became a body answer: %+v %v", out, err)
	}
}
