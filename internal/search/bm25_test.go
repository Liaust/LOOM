package search

import "testing"

func TestBM25ScoreRewardsTermFrequencyWithLengthNormalization(t *testing.T) {
	short := NewBM25Document("short", TokenizeLexical("loom notes search"))
	long := NewBM25Document("long", TokenizeLexical("loom notes search "+repeatTerm("filler", 80)))
	documents := []BM25Document{short, long}
	stats := BM25StatsFromDocuments(documents)

	shortScore := BM25Score([]string{"loom"}, short, stats, DefaultBM25Parameters())
	longScore := BM25Score([]string{"loom"}, long, stats, DefaultBM25Parameters())

	if shortScore <= longScore {
		t.Fatalf("short score = %.6f long score = %.6f, want short document ranked higher", shortScore, longScore)
	}
}

func TestBM25ScoreUsesRareTermIDF(t *testing.T) {
	docA := NewBM25Document("a", TokenizeLexical("loom common rare"))
	docB := NewBM25Document("b", TokenizeLexical("loom common"))
	docC := NewBM25Document("c", TokenizeLexical("common"))
	stats := BM25StatsFromDocuments([]BM25Document{docA, docB, docC})

	rareScore := BM25Score([]string{"rare"}, docA, stats, DefaultBM25Parameters())
	commonScore := BM25Score([]string{"common"}, docA, stats, DefaultBM25Parameters())

	if rareScore <= commonScore {
		t.Fatalf("rare score = %.6f common score = %.6f, want rare term to score higher", rareScore, commonScore)
	}
}

func TestScoreBM25DocumentsReturnsStableScoreOrder(t *testing.T) {
	documents := []BM25Document{
		NewBM25Document("b", TokenizeLexical("notes search")),
		NewBM25Document("a", TokenizeLexical("notes search")),
		NewBM25Document("c", TokenizeLexical("unrelated")),
	}
	stats := BM25StatsFromDocuments(documents)

	results := ScoreBM25Documents([]string{"notes"}, documents, stats, DefaultBM25Parameters())

	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].DocumentID != "a" || results[1].DocumentID != "b" {
		t.Fatalf("results = %#v, want stable ID tie-break order", results)
	}
}

func TestBM25MissingTermScoresZero(t *testing.T) {
	document := NewBM25Document("doc", TokenizeLexical("loom notes"))
	stats := BM25StatsFromDocuments([]BM25Document{document})

	if score := BM25Score([]string{"missing"}, document, stats, DefaultBM25Parameters()); score != 0 {
		t.Fatalf("score = %.6f, want zero", score)
	}
}

func repeatTerm(term string, count int) string {
	out := ""
	for idx := 0; idx < count; idx++ {
		if idx > 0 {
			out += " "
		}
		out += term
	}
	return out
}
