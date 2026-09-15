package ui

import (
	"sort"
	"strings"
	"unicode"
)

type Candidate struct {
	ID          string
	Title       string
	Description string
	Domain      string
	Keywords    []string
	Attention   int
	Recent      int
}

type FuzzyResult struct {
	Candidate Candidate
	Score     int
}

func RankFuzzy(query string, candidates []Candidate, limit int) []FuzzyResult {
	query = normalizeSearch(query)
	if query == "" {
		results := make([]FuzzyResult, 0, len(candidates))
		for _, candidate := range candidates {
			results = append(results, FuzzyResult{Candidate: candidate, Score: candidate.Attention + candidate.Recent})
		}
		sortFuzzy(results)
		return limitFuzzy(results, limit)
	}

	results := []FuzzyResult{}
	for _, candidate := range candidates {
		score := fuzzyScore(query, candidate)
		if score > 0 {
			results = append(results, FuzzyResult{Candidate: candidate, Score: score})
		}
	}
	sortFuzzy(results)
	return limitFuzzy(results, limit)
}

func fuzzyScore(query string, candidate Candidate) int {
	fields := []string{candidate.ID, candidate.Title, candidate.Domain, candidate.Description}
	fields = append(fields, candidate.Keywords...)
	best := 0
	for _, field := range fields {
		score := scoreField(query, normalizeSearch(field))
		if score > best {
			best = score
		}
	}
	if best == 0 {
		return 0
	}
	return best + candidate.Attention + candidate.Recent
}

func scoreField(query, field string) int {
	if field == "" {
		return 0
	}
	if field == query {
		return 1000
	}
	if strings.HasPrefix(field, query) {
		return 850
	}
	words := strings.Fields(field)
	for _, word := range words {
		if word == query {
			return 800
		}
		if strings.HasPrefix(word, query) {
			return 720
		}
	}
	if strings.Contains(field, query) {
		return 600
	}
	if acronym(words) == query {
		return 560
	}
	if orderedRunes(query, field) {
		return 350
	}
	return 0
}

func normalizeSearch(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastSpace := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			builder.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(builder.String())
}

func acronym(words []string) string {
	var builder strings.Builder
	for _, word := range words {
		if word == "" {
			continue
		}
		builder.WriteByte(word[0])
	}
	return builder.String()
}

func orderedRunes(query, field string) bool {
	if query == "" {
		return true
	}
	queryRunes := []rune(query)
	idx := 0
	for _, r := range field {
		if queryRunes[idx] == r {
			idx++
			if idx == len(queryRunes) {
				return true
			}
		}
	}
	return false
}

func sortFuzzy(results []FuzzyResult) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Candidate.Title != results[j].Candidate.Title {
			return results[i].Candidate.Title < results[j].Candidate.Title
		}
		return results[i].Candidate.ID < results[j].Candidate.ID
	})
}

func limitFuzzy(results []FuzzyResult, limit int) []FuzzyResult {
	if limit <= 0 || len(results) <= limit {
		return results
	}
	return results[:limit]
}
