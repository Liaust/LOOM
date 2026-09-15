package search

import (
	"math"
	"testing"
	"time"
)

func TestRecencyDecayScoreUsesBoundedHalfLife(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		at   time.Time
		want float64
	}{
		{name: "now", at: now, want: 1},
		{name: "future bounded", at: now.Add(24 * time.Hour), want: 1},
		{name: "one half life", at: now.Add(-DefaultRecencyHalfLife), want: 0.5},
		{name: "two half lives", at: now.Add(-2 * DefaultRecencyHalfLife), want: 0.25},
		{name: "missing", at: time.Time{}, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := RecencyDecayScore(now, test.at, 0)
			if math.Abs(got-test.want) > 1e-12 {
				t.Fatalf("RecencyDecayScore = %.12f, want %.12f", got, test.want)
			}
		})
	}
}

func TestRankRecencyCandidatesIsDeterministicAndCandidateBounded(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	results := RankRecencyCandidates(now, []RecencyCandidate{
		{ID: "old", At: now.Add(-2 * DefaultRecencyHalfLife)},
		{ID: "recent-b", At: now.Add(-time.Hour)},
		{ID: "recent-a", At: now.Add(-time.Hour)},
		{ID: "recent-a", At: now},
	}, 0)
	if len(results) != 3 {
		t.Fatalf("results len = %d, want only three supplied unique candidates", len(results))
	}
	if results[0].ID != "recent-a" || results[1].ID != "recent-b" || results[0].Rank != 1 || results[1].Rank != 1 {
		t.Fatalf("tied recent results = %#v, want stable ID order and shared rank", results)
	}
	if results[2].ID != "old" || results[2].Rank != 3 {
		t.Fatalf("old result = %#v", results[2])
	}
}
