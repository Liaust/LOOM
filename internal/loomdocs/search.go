package loomdocs

import (
	"fmt"
	"sort"
	"strings"

	loomsearch "loom.local/loom/internal/search"
)

func Search(corpus *Corpus, query string, filters SearchFilters) (SearchResponse, error) {
	if corpus == nil {
		return SearchResponse{}, fmt.Errorf("documentation corpus is required")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResponse{}, fmt.Errorf("search query is required")
	}
	if filters.Limit <= 0 {
		filters.Limit = 10
	}
	if filters.Limit > 100 {
		filters.Limit = 100
	}

	type candidate struct {
		document Document
		bm25     loomsearch.BM25Document
	}
	candidates := []candidate{}
	for _, document := range corpus.Documents {
		if !matchesFilter(document.Tags, filters.Tag) || !matchesFilter(document.Audience, filters.Audience) || (filters.Status != "" && !strings.EqualFold(document.Status, filters.Status)) {
			continue
		}
		tokens := weightedDocumentTokens(document)
		candidates = append(candidates, candidate{document: document, bm25: loomsearch.NewBM25Document(document.RelativePath, tokens)})
	}
	bm25Documents := make([]loomsearch.BM25Document, 0, len(candidates))
	byPath := map[string]Document{}
	for _, candidate := range candidates {
		bm25Documents = append(bm25Documents, candidate.bm25)
		byPath[candidate.document.RelativePath] = candidate.document
	}
	stats := loomsearch.BM25StatsFromDocuments(bm25Documents)
	queryTerms := loomsearch.TokenizeLexical(query)
	baseScores := loomsearch.ScoreBM25Documents(queryTerms, bm25Documents, stats, loomsearch.DefaultBM25Parameters())

	results := make([]SearchResult, 0, len(baseScores))
	for _, base := range baseScores {
		document := byPath[base.DocumentID]
		score := base.Score
		matches := []string{"body or metadata term"}
		if strings.EqualFold(strings.TrimSpace(document.Title), query) {
			score += 30
			matches = append(matches, "exact title")
		}
		for _, alias := range document.Aliases {
			if strings.EqualFold(strings.TrimSpace(alias), query) {
				score += 25
				matches = append(matches, "exact alias")
			}
		}
		if anyExact(document.Tags, query) {
			score += 12
			matches = append(matches, "exact tag")
		}
		for _, heading := range document.Headings {
			if strings.EqualFold(strings.TrimSpace(heading.Title), query) {
				score += 15
				matches = append(matches, "exact heading")
				break
			}
		}
		results = append(results, SearchResult{Title: document.Title, RelativePath: document.RelativePath, Status: document.Status, Tags: append([]string(nil), document.Tags...), Score: score, Matches: uniqueStrings(matches), Snippet: searchSnippet(document, queryTerms)})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].RelativePath < results[j].RelativePath
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > filters.Limit {
		results = results[:filters.Limit]
	}
	for index := range results {
		results[index].Rank = index + 1
	}
	warnings := []GraphIssue{}
	for _, issue := range corpus.Issues {
		if issue.Severity == SeverityWarning || issue.Kind == "parse_error" || issue.Kind == "missing_metadata" {
			warnings = append(warnings, issue)
		}
	}
	return SearchResponse{Root: corpus.Root, Query: query, Filters: filters, Results: results, Warnings: warnings}, nil
}

func weightedDocumentTokens(document Document) []string {
	fields := []struct {
		value  string
		weight int
	}{
		{document.Title, 6},
		{strings.Join(document.Aliases, " "), 5},
		{strings.Join(document.Tags, " "), 4},
		{headingsText(document.Headings), 3},
		{document.Description, 2},
		{document.RelativePath, 2},
		{strings.Join(document.Audience, " "), 1},
		{document.Body, 1},
	}
	tokens := []string{}
	for _, field := range fields {
		fieldTokens := loomsearch.TokenizeLexical(field.value)
		for count := 0; count < field.weight; count++ {
			tokens = append(tokens, fieldTokens...)
		}
	}
	return tokens
}

func headingsText(headings []Heading) string {
	values := make([]string, 0, len(headings))
	for _, heading := range headings {
		values = append(values, heading.Title)
	}
	return strings.Join(values, " ")
}

func matchesFilter(values []string, filter string) bool {
	if strings.TrimSpace(filter) == "" {
		return true
	}
	return anyExact(values, filter)
}

func anyExact(values []string, expected string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}

func searchSnippet(document Document, terms []string) string {
	text := strings.TrimSpace(document.Description + "\n" + document.Body)
	lower := strings.ToLower(text)
	start := 0
	for _, term := range terms {
		if index := strings.Index(lower, strings.ToLower(term)); index >= 0 {
			start = index - 80
			if start < 0 {
				start = 0
			}
			break
		}
	}
	end := start + 240
	if end > len(text) {
		end = len(text)
	}
	return strings.Join(strings.Fields(text[start:end]), " ")
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
