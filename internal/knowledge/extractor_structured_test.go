package knowledge

import (
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestStructuredDataExtractorParsesJSON(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassCode, "data.json")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:  object,
		Content: `{"name":"loom","nested":{"answer":42}}`,
		Chunker: ChunkerOptions{TargetCharacters: 100},
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.ExtractorKey != ExtractorKeyStructuredData {
		t.Fatalf("extractor = %q, want structured", extraction.ExtractorKey)
	}
	if extraction.TextSections[0].TextSource != TextSourceStructuredText {
		t.Fatalf("text source = %q, want structured_text", extraction.TextSections[0].TextSource)
	}
	if !strings.Contains(extraction.Document.Text, "nested.answer: 42") {
		t.Fatalf("document text = %q, want flattened nested value", extraction.Document.Text)
	}
	keys, _ := extraction.Metadata["top_level_keys"].([]string)
	if len(keys) != 2 || keys[0] != "name" || keys[1] != "nested" {
		t.Fatalf("top level keys = %#v", extraction.Metadata["top_level_keys"])
	}
}

func TestStructuredDataExtractorFallsBackOnInvalidJSON(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassCode, "broken.json")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:  object,
		Content: `{"broken":`,
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Metadata["parse_status"] != "fallback_text" {
		t.Fatalf("parse status = %#v, want fallback_text", extraction.Metadata["parse_status"])
	}
	if len(extraction.Warnings) == 0 || !strings.Contains(extraction.Warnings[0], "json_parse_failed") {
		t.Fatalf("warnings = %#v, want parse warning", extraction.Warnings)
	}
	if extraction.Document.Text != `{"broken":` {
		t.Fatalf("fallback text = %q", extraction.Document.Text)
	}
}

func TestStructuredDataExtractorParsesCSV(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassText, "table.csv")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:  object,
		Content: "name,value\nalpha,10\nbeta,20\n",
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Metadata["row_count"] != 3 {
		t.Fatalf("row count = %#v, want 3", extraction.Metadata["row_count"])
	}
	if !strings.Contains(extraction.Document.Text, "name=alpha") || !strings.Contains(extraction.Document.Text, "value=20") {
		t.Fatalf("document text = %q, want row values", extraction.Document.Text)
	}
}
