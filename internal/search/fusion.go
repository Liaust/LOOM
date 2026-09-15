package search

import (
	"sort"
	"strings"
)

const DefaultReciprocalRankFusionK = 60.0

type RankedCandidate struct {
	ID     string
	Rank   int
	Weight float64
}

type FusionResult struct {
	ID    string
	Score float64
	Ranks map[string]int
}

type RankedList struct {
	Name       string
	Candidates []RankedCandidate
	Weight     float64
}

func ReciprocalRankFusion(lists []RankedList, k float64) []FusionResult {
	if k <= 0 {
		k = DefaultReciprocalRankFusionK
	}
	scores := map[string]*FusionResult{}
	for _, list := range lists {
		listName := strings.TrimSpace(list.Name)
		if listName == "" {
			listName = "ranked"
		}
		listWeight := list.Weight
		if listWeight <= 0 {
			listWeight = 1
		}
		seen := map[string]struct{}{}
		for idx, candidate := range list.Candidates {
			id := strings.TrimSpace(candidate.ID)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			rank := candidate.Rank
			if rank <= 0 {
				rank = idx + 1
			}
			weight := candidate.Weight
			if weight <= 0 {
				weight = listWeight
			}
			result := scores[id]
			if result == nil {
				result = &FusionResult{ID: id, Ranks: map[string]int{}}
				scores[id] = result
			}
			result.Score += weight / (k + float64(rank))
			if previous, ok := result.Ranks[listName]; !ok || rank < previous {
				result.Ranks[listName] = rank
			}
		}
	}
	results := make([]FusionResult, 0, len(scores))
	for _, result := range scores {
		results = append(results, *result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	return results
}
