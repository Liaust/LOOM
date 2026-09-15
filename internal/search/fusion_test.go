package search

import "testing"

func TestReciprocalRankFusionCombinesStableRanks(t *testing.T) {
	results := ReciprocalRankFusion([]RankedList{
		{
			Name: "lexical",
			Candidates: []RankedCandidate{
				{ID: "a"},
				{ID: "b"},
			},
		},
		{
			Name: "semantic",
			Candidates: []RankedCandidate{
				{ID: "b"},
				{ID: "c"},
			},
		},
	}, 60)
	if len(results) != 3 {
		t.Fatalf("result count = %d, want 3", len(results))
	}
	if results[0].ID != "b" {
		t.Fatalf("top result = %q, want b: %#v", results[0].ID, results)
	}
	if results[0].Ranks["lexical"] != 2 || results[0].Ranks["semantic"] != 1 {
		t.Fatalf("top ranks = %#v", results[0].Ranks)
	}
}

func TestReciprocalRankFusionUsesStableTieOrder(t *testing.T) {
	results := ReciprocalRankFusion([]RankedList{{
		Name: "lexical",
		Candidates: []RankedCandidate{
			{ID: "b", Rank: 1},
			{ID: "a", Rank: 1},
		},
	}}, 60)
	if len(results) != 2 || results[0].ID != "a" || results[1].ID != "b" {
		t.Fatalf("tie order = %#v", results)
	}
}
