package search

import (
	"strings"
	"testing"
)

func TestTokenizeLexicalNormalizesUnicodeAndSeparators(t *testing.T) {
	tokens := TokenizeLexical("Threat-Intel: Café OSINT_2026 #Tag [[Node/Link]]")

	got := strings.Join(tokens, ",")
	want := "threat,intel,café,osint,2026,tag,node,link"
	if got != want {
		t.Fatalf("tokens = %q, want %q", got, want)
	}
}

func TestTermFrequenciesCountsRepeatedTokens(t *testing.T) {
	frequencies := TermFrequencies([]string{"loom", "notes", "loom", "", "notes", "loom"})

	if frequencies["loom"] != 3 || frequencies["notes"] != 2 {
		t.Fatalf("frequencies = %#v, want loom=3 notes=2", frequencies)
	}
	if _, ok := frequencies[""]; ok {
		t.Fatalf("frequencies should not include empty token: %#v", frequencies)
	}
}

func TestUniqueTermsPreservesFirstOccurrenceOrder(t *testing.T) {
	got := strings.Join(UniqueTerms([]string{"notes", "loom", "notes", "search", "loom"}), ",")
	want := "notes,loom,search"
	if got != want {
		t.Fatalf("unique terms = %q, want %q", got, want)
	}
}
