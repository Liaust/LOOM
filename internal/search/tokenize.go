package search

import (
	"strings"
	"unicode"
)

// TokenizeLexical normalizes text for deterministic lexical ranking.
func TokenizeLexical(text string) []string {
	tokens := []string{}
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		tokens = append(tokens, builder.String())
		builder.Reset()
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return tokens
}

func TermFrequencies(tokens []string) map[string]int {
	frequencies := map[string]int{}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		frequencies[token]++
	}
	return frequencies
}

func UniqueTerms(tokens []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}
