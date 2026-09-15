package knowledge

import (
	"strings"
	"testing"
	"time"
)

func TestNotesSearchTemporalInputIsUTCAndDeterministic(t *testing.T) {
	input, err := normalizeNotesSearchTemporalInput(NotesSearchInput{
		After:  "2026-01-02T01:30:00+02:00",
		Before: "2026-01-03",
	})
	if err != nil {
		t.Fatalf("normalizeNotesSearchTemporalInput returned error: %v", err)
	}
	if input.After != "2026-01-01T23:30:00Z" {
		t.Fatalf("after = %q, want UTC instant", input.After)
	}
	if input.Before != "2026-01-03T00:00:00Z" {
		t.Fatalf("before = %q, want date-only midnight UTC", input.Before)
	}
	if input.Sort != NotesSearchSortRelevance {
		t.Fatalf("sort = %q, want relevance", input.Sort)
	}
}

func TestNotesSearchTemporalInputRejectsInvalidBoundsAndSort(t *testing.T) {
	for _, test := range []struct {
		name  string
		input NotesSearchInput
		want  string
	}{
		{name: "invalid timestamp", input: NotesSearchInput{After: "01/02/2026"}, want: "invalid notes search after timestamp"},
		{name: "reversed range", input: NotesSearchInput{After: "2026-02-01", Before: "2026-01-01"}, want: "must be earlier"},
		{name: "unsupported sort", input: NotesSearchInput{Sort: "indexed"}, want: "is not supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeNotesSearchTemporalInput(test.input)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseNotesSearchInputParsesAbsoluteTimeTokens(t *testing.T) {
	input := ParseNotesSearchInput(`after:2026-01-01 before:2026-02-01T00:00:00+01:00 sort:newest time:source loom`)
	if input.Query != "time:source loom" {
		t.Fatalf("query = %q, want unknown token preserved", input.Query)
	}
	if input.After != "2026-01-01" || input.Before != "2026-02-01T00:00:00+01:00" || input.Sort != NotesSearchSortNewest {
		t.Fatalf("temporal tokens = %#v", input)
	}
}

func TestNotesSearchQueriesUseRecencyBoundsAndNeverIndexTimeForRanking(t *testing.T) {
	input := NotesSearchInput{
		Query:  "loom",
		After:  "2026-01-02T01:30:00+02:00",
		Before: "2026-01-03",
		Limit:  5,
	}
	queries := []struct {
		name  string
		build func() (notesSearchQuery, error)
	}{
		{name: "lexical", build: func() (notesSearchQuery, error) { return buildNotesSearchQuery(input) }},
		{name: "semantic", build: func() (notesSearchQuery, error) {
			return buildSemanticNotesSearchQuery(input, EmbeddingSettings{Dimensions: 3}, []float32{0.1, 0.2, 0.3})
		}},
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			query, err := test.build()
			if err != nil {
				t.Fatalf("build query returned error: %v", err)
			}
			if !strings.Contains(query.SQL, "COALESCE(sd.recency_at, ko.recency_at) >=") ||
				!strings.Contains(query.SQL, "COALESCE(sd.recency_at, ko.recency_at) <") {
				t.Fatalf("query does not filter selected recency time:\n%s", query.SQL)
			}
			if strings.Contains(query.SQL, "indexed_at DESC") || strings.Contains(query.SQL, "indexed_at ASC") {
				t.Fatalf("query ranks by index time:\n%s", query.SQL)
			}
			var times []time.Time
			for _, arg := range query.Args {
				if value, ok := arg.(time.Time); ok {
					times = append(times, value)
				}
			}
			if len(times) != 2 || times[0].Format(time.RFC3339) != "2026-01-01T23:30:00Z" || times[1].Format(time.RFC3339) != "2026-01-03T00:00:00Z" {
				t.Fatalf("time args = %#v, want normalized inclusive-after/exclusive-before bounds", times)
			}
		})
	}
}

func TestNotesSearchRecencyRerankingKeepsRelevanceDominantAndCandidateBounded(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-4 * 180 * 24 * time.Hour)
	recent := now.Add(-time.Hour)

	equal := rerankNotesSearchResults([]NotesSearchResult{
		{SearchDocumentID: "old", RankScore: 5, FinalScore: 5, RecencyAt: old},
		{SearchDocumentID: "recent", RankScore: 5, FinalScore: 5, RecencyAt: recent},
	}, NotesSearchInput{Sort: NotesSearchSortRelevance}, now, NotesSearchModeLexical)
	if equal[0].SearchDocumentID != "recent" {
		t.Fatalf("equal relevance order = %#v, want recent first", equal)
	}

	dominant := rerankNotesSearchResults([]NotesSearchResult{
		{SearchDocumentID: "strong-old", RankScore: 100, FinalScore: 100, RecencyAt: old},
		{SearchDocumentID: "weak-recent", RankScore: 1, FinalScore: 1, RecencyAt: recent},
	}, NotesSearchInput{Sort: NotesSearchSortRelevance}, now, NotesSearchModeLexical)
	if dominant[0].SearchDocumentID != "strong-old" {
		t.Fatalf("dominant relevance order = %#v, want strong old result first", dominant)
	}

	bounded := rerankNotesSearchResults([]NotesSearchResult{
		{SearchDocumentID: "relevant", RankScore: 1, RecencyAt: old},
	}, NotesSearchInput{}, now, NotesSearchModeLexical)
	if len(bounded) != 1 || bounded[0].SearchDocumentID != "relevant" {
		t.Fatalf("bounded candidates = %#v, recent unrelated object must not enter", bounded)
	}
}

func TestNotesSearchRecencyPolicyIsStableAcrossModesAndReindexing(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	candidates := []NotesSearchResult{
		{SearchDocumentID: "older", RankScore: 2, SemanticScore: 2, RecencyAt: now.Add(-365 * 24 * time.Hour), RecencyBasis: AbsoluteTimeBasisSourceFilesystemMtime, IndexedAt: now},
		{SearchDocumentID: "newer", RankScore: 2, SemanticScore: 2, RecencyAt: now.Add(-24 * time.Hour), RecencyBasis: AbsoluteTimeBasisObservedAtFallback, IndexedAt: now.Add(-365 * 24 * time.Hour)},
	}
	lexicalResults := rerankNotesSearchResults(candidates, NotesSearchInput{}, now, NotesSearchModeLexical)
	semanticResults := rerankNotesSearchResults(candidates, NotesSearchInput{}, now, NotesSearchModeSemantic)
	hybridResults := fuseNotesSearchResults(candidates, candidates, NotesSearchInput{Limit: 10}, now)
	for name, results := range map[string][]NotesSearchResult{
		"lexical":  lexicalResults,
		"semantic": semanticResults,
		"hybrid":   hybridResults,
	} {
		if len(results) != 2 || results[0].SearchDocumentID != "newer" {
			t.Fatalf("%s results = %#v, want shared recency policy", name, results)
		}
		if results[0].RecencyBasis != AbsoluteTimeBasisObservedAtFallback {
			t.Fatalf("%s fallback basis = %q, want labelled observation fallback", name, results[0].RecencyBasis)
		}
	}

	reindexed := append([]NotesSearchResult(nil), candidates...)
	reindexed[0].IndexedAt = now.Add(10 * 365 * 24 * time.Hour)
	reindexed[1].IndexedAt = now.Add(-10 * 365 * 24 * time.Hour)
	results := rerankNotesSearchResults(reindexed, NotesSearchInput{}, now, NotesSearchModeLexical)
	if results[0].SearchDocumentID != lexicalResults[0].SearchDocumentID {
		t.Fatalf("reindex changed order from %#v to %#v", lexicalResults, results)
	}
}

func TestNotesSearchExplicitChronologicalSortAndGrouping(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	candidates := []NotesSearchResult{
		{SearchDocumentID: "a-chunk-1", KnowledgeObjectID: "object-a", RankScore: 9, RecencyAt: now.Add(-48 * time.Hour)},
		{SearchDocumentID: "a-chunk-2", KnowledgeObjectID: "object-a", RankScore: 8, RecencyAt: now.Add(-24 * time.Hour)},
		{SearchDocumentID: "b-chunk-1", KnowledgeObjectID: "object-b", RankScore: 7, RecencyAt: now.Add(-72 * time.Hour)},
	}
	newest := rerankNotesSearchResults(candidates, NotesSearchInput{Sort: NotesSearchSortNewest}, now, NotesSearchModeLexical)
	if newest[0].SearchDocumentID != "a-chunk-2" || newest[len(newest)-1].SearchDocumentID != "b-chunk-1" {
		t.Fatalf("newest order = %#v", newest)
	}
	oldest := rerankNotesSearchResults(candidates, NotesSearchInput{Sort: NotesSearchSortOldest}, now, NotesSearchModeLexical)
	if oldest[0].SearchDocumentID != "b-chunk-1" || oldest[len(oldest)-1].SearchDocumentID != "a-chunk-2" {
		t.Fatalf("oldest order = %#v", oldest)
	}
	grouped := groupNotesSearchResults(newest, 2)
	if len(grouped) != 2 || grouped[0].KnowledgeObjectID != "object-a" || grouped[1].KnowledgeObjectID != "object-b" {
		t.Fatalf("grouped results = %#v, chunks must not crowd out another object", grouped)
	}
}
