package search

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"
)

// Leave room for the document ID and field key in lexical_terms' composite
// B-tree key. Measure UTF-8 bytes, not runes; PostgreSQL indexes bytes.
const maxLiteralLexicalTermBytes = 1024

// LexicalTermKey bounds an already normalized term without changing the caller's
// token boundaries (in particular, identifier underscores in Notes queries).
func LexicalTermKey(term string) string {
	if len(term) <= maxLiteralLexicalTermBytes {
		return term
	}
	digest := sha256.Sum256([]byte(term))
	// The separator cannot occur in a normalized literal token. Hash the
	// entire normalized term, retaining distinctions beyond any prefix.
	return "sha256:" + hex.EncodeToString(digest[:])
}

// TokenizeLexical normalizes text for deterministic lexical ranking. Index and
// query callers share the same bounded representation for oversized tokens.
func TokenizeLexical(text string) []string {
	tokens := []string{}
	var builder strings.Builder
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		tokens = append(tokens, LexicalTermKey(builder.String()))
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
