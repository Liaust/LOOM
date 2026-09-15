package search

import (
	"math"
	"sort"
	"strings"
	"time"
)

const (
	DefaultRecencyHalfLife     = 180 * 24 * time.Hour
	DefaultRecencyRankWeight   = 0.15
	DefaultRelevanceRankWeight = 1.0
)

type RecencyCandidate struct {
	ID string
	At time.Time
}

type RecencyResult struct {
	ID    string
	At    time.Time
	Score float64
	Rank  int
}

// RecencyDecayScore returns a bounded exponential-decay score. A timestamp at
// the query instant scores 1, and a timestamp one half-life old scores 0.5.
// Future timestamps remain bounded at 1; missing timestamps score 0.
func RecencyDecayScore(now, at time.Time, halfLife time.Duration) float64 {
	if now.IsZero() || at.IsZero() {
		return 0
	}
	if halfLife <= 0 {
		halfLife = DefaultRecencyHalfLife
	}
	age := now.UTC().Sub(at.UTC())
	if age <= 0 {
		return 1
	}
	score := math.Pow(0.5, float64(age)/float64(halfLife))
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// RankRecencyCandidates ranks only the supplied candidates. It never discovers
// candidates by recency, which keeps relevance selection as the search gate.
func RankRecencyCandidates(now time.Time, candidates []RecencyCandidate, halfLife time.Duration) []RecencyResult {
	results := make([]RecencyResult, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		id := strings.TrimSpace(candidate.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		at := candidate.At.UTC()
		results = append(results, RecencyResult{ID: id, At: at, Score: RecencyDecayScore(now, at, halfLife)})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if !results[i].At.Equal(results[j].At) {
			return results[i].At.After(results[j].At)
		}
		return results[i].ID < results[j].ID
	})
	for index := range results {
		rank := index + 1
		if index > 0 && results[index].Score == results[index-1].Score && results[index].At.Equal(results[index-1].At) {
			rank = results[index-1].Rank
		}
		results[index].Rank = rank
	}
	return results
}
