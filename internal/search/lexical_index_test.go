package search

import (
	"bytes"
	"compress/zlib"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestBuildLexicalDocumentStoresFieldLengthsAndTermFrequencies(t *testing.T) {
	document, err := BuildLexicalDocument(LexicalDocumentInput{
		SearchDocumentID: "search_document_test",
		IndexVersion:     "knowledge_bm25_v1",
		SourceKind:       "knowledge_chunk",
		SourceID:         "knowledge_chunk_test",
		SourceVersionID:  "knowledge_object_version_test",
		ObjectID:         "knowledge_object_test",
		Fields: []LexicalFieldInput{
			{Key: LexicalFieldTitle, Text: "OSINT Tools"},
			{Key: LexicalFieldPath, Text: "reports/osint-tools.md"},
			{Key: LexicalFieldTag, Text: "Research #OSINT"},
			{Key: LexicalFieldBody, Text: "OSINT search search"},
		},
	})
	if err != nil {
		t.Fatalf("BuildLexicalDocument returned error: %v", err)
	}

	if document.DocumentLength != 11 {
		t.Fatalf("document length = %d, want 11", document.DocumentLength)
	}
	if document.FieldLengths[LexicalFieldTitle] != 2 || document.FieldLengths[LexicalFieldBody] != 3 {
		t.Fatalf("field lengths = %#v", document.FieldLengths)
	}
	got := lexicalTermSummary(document.Terms)
	for _, want := range []string{
		"body:search=2",
		"tag:osint=1",
		"title:tools=1",
		"path:reports=1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("term summary missing %q: %s", want, got)
		}
	}
}

func TestBuildLexicalDocumentRejectsMissingIdentity(t *testing.T) {
	_, err := BuildLexicalDocument(LexicalDocumentInput{
		IndexVersion: "knowledge_bm25_v1",
		SourceKind:   "knowledge_chunk",
		SourceID:     "knowledge_chunk_test",
	})
	if err == nil {
		t.Fatal("expected missing search document id error")
	}
}

func lexicalTermSummary(terms []LexicalTerm) string {
	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		parts = append(parts, term.FieldKey+":"+term.Term+"="+strconv.Itoa(term.TermFrequency))
	}
	return strings.Join(parts, ",")
}

func TestBuildLexicalDocumentBoundsNonCompressibleTermsAndMatchesQuery(t *testing.T) {
	// Fixed pseudo-random alphanumerics reproduce a large, poorly compressible
	// token without retaining private source text from the failing document.
	rng := rand.New(rand.NewSource(85))
	alphabet := "abcdefghijklmnopqrstuvwxyz0123456789"
	var builder strings.Builder
	for i := 0; i < 8192; i++ {
		builder.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	long := builder.String()
	different := long[:len(long)-1] + "z"
	if different == long {
		different = long[:len(long)-1] + "y"
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte(long)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if compressed.Len() <= 2704 {
		t.Fatal("fixture compresses below the observed B-tree limit")
	}
	body := "ordinary " + long + " " + different + " " + strings.ToUpper(long)
	document, err := BuildLexicalDocument(LexicalDocumentInput{
		SearchDocumentID: "search_document_long_terms",
		IndexVersion:     "knowledge_bm25_v1", SourceKind: "knowledge_chunk", SourceID: "knowledge_chunk_long_terms",
		Fields: []LexicalFieldInput{{Key: LexicalFieldBody, Text: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queryTerms := UniqueTerms(TokenizeLexical(long + " " + different))
	if len(queryTerms) != 2 {
		t.Fatalf("distinct long query terms collapsed: %#v", queryTerms)
	}
	frequencies := map[string]int{}
	for _, term := range document.Terms {
		if len(term.Term) > maxLiteralLexicalTermBytes {
			t.Fatalf("index term too large: %d bytes", len(term.Term))
		}
		frequencies[term.Term] = term.TermFrequency
	}
	if len(frequencies) != 3 || frequencies["ordinary"] != 1 || frequencies[queryTerms[0]] != 2 || frequencies[queryTerms[1]] != 1 {
		t.Fatalf("index/query keys or frequencies differ: %#v", frequencies)
	}
	if document.DocumentLength != 4 || document.FieldLengths[LexicalFieldBody] != 4 {
		t.Fatalf("ranking lengths changed: %#v", document)
	}
}
