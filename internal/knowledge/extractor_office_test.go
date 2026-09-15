package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/storagecatalog"
)

func TestOfficeMetadataExtractorHandlesGoogleDocsPointer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Cloud Note.gdoc")
	payload := `{"url":"https://docs.google.com/document/d/doc-123/edit","title":"Cloud Investigation","resource_id":"document:doc-123"}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("write gdoc: %v", err)
	}
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassOfficeDocument, "Cloud Note.gdoc")
	object.SourcePath = path

	extraction, err := officeMetadataExtractor{}.Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusMetadataOnly {
		t.Fatalf("status = %q, want metadata_only", extraction.Status)
	}
	if extraction.Metadata["doc_id"] != "doc-123" ||
		extraction.Metadata["title"] != "Cloud Investigation" ||
		extraction.Metadata["url"] != "https://docs.google.com/document/d/doc-123/edit" {
		t.Fatalf("metadata = %#v", extraction.Metadata)
	}
	if len(extraction.Chunks) != 0 {
		t.Fatalf("chunks = %#v, want none", extraction.Chunks)
	}
	if len(extraction.TextSections) != 1 || !strings.Contains(extraction.TextSections[0].Text, "Cloud Investigation") {
		t.Fatalf("text sections = %#v, want pointer metadata text", extraction.TextSections)
	}
}

func TestOfficeMetadataExtractorLegacyOfficeIsMetadataOnly(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassOfficeDocument, "Archive.DOC")

	extraction, err := officeMetadataExtractor{}.Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusUnsupportedBodyExtraction {
		t.Fatalf("status = %q, want unsupported_body_extraction", extraction.Status)
	}
	if len(extraction.Chunks) != 0 {
		t.Fatalf("chunks = %#v, want metadata-only", extraction.Chunks)
	}
	if len(extraction.Warnings) != 1 || !strings.Contains(extraction.Warnings[0], "doc_body_extraction_unsupported") {
		t.Fatalf("warnings = %#v", extraction.Warnings)
	}
}

func TestKnowledgeMetadataSearchBodyIncludesExtractionMetadata(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassOfficeDocument, "Cloud Note.gdoc")
	object.Metadata = mustJSON(t, map[string]any{
		"extraction": map[string]any{
			"metadata": map[string]any{
				"title":  "Cloud Investigation",
				"doc_id": "doc-123",
				"url":    "https://docs.google.com/document/d/doc-123/edit",
			},
		},
	})

	body := knowledgeMetadataSearchBody(object)
	for _, want := range []string{"Cloud Investigation", "doc-123", "docs.google.com"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metadata search body = %q, missing %q", body, want)
		}
	}
}
