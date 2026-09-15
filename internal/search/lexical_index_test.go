package search

import (
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
