package search

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestLexicalTermKeyPreservesCallerTokenBoundaries(t *testing.T) {
	for _, term := range []string{"", "ordinary", "not_a_secret", "café"} {
		if got := LexicalTermKey(term); got != term {
			t.Fatalf("ordinary normalized term changed: %q -> %q", term, got)
		}
	}
	long := strings.Repeat("identifier_", 200)
	if got := LexicalTermKey(long); !strings.HasPrefix(got, "sha256:") || len(got) != 71 {
		t.Fatalf("long identifier key = %q", got)
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

func TestTokenizeLexicalBoundsBytesAndPreservesLongTermIdentity(t *testing.T) {
	literal := strings.Repeat("a", maxLiteralLexicalTermBytes)
	long := literal + "b"
	different := literal + "c"
	tokens := TokenizeLexical(literal + " " + long + " " + different + " " + strings.ToUpper(long))
	if len(tokens) != 4 || tokens[0] != literal {
		t.Fatalf("literal boundary changed: %#v", tokens)
	}
	digest := sha256.Sum256([]byte(long))
	want := "sha256:" + hex.EncodeToString(digest[:])
	if tokens[1] != want || tokens[3] != want || tokens[2] == want {
		t.Fatalf("long term identity/normalization changed: %#v", tokens[1:])
	}
	// Unicode terms may exceed the byte limit with far fewer than 1024 runes.
	unicodeLiteral := strings.Repeat("界", maxLiteralLexicalTermBytes/3)
	unicodeTokens := TokenizeLexical(unicodeLiteral + " " + unicodeLiteral + "界")
	if unicodeTokens[0] != unicodeLiteral || !strings.HasPrefix(unicodeTokens[1], "sha256:") {
		t.Fatalf("UTF-8 byte boundary: %#v", unicodeTokens)
	}
	for _, token := range tokens {
		if len(token) > maxLiteralLexicalTermBytes {
			t.Fatalf("unbounded term: %d bytes", len(token))
		}
	}
	// A user typing the reserved representation does not create the same term.
	for _, literalToken := range TokenizeLexical(want) {
		if literalToken == want {
			t.Fatal("digest key collides with a literal token")
		}
	}
}
