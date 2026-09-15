package knowledge

import (
	"testing"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/storagecatalog"
)

func TestDefaultExtractionRegistryExtractsMarkdownWithProvenance(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassMarkdown, "daily.md")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:  object,
		Content: "# Daily\n\nSee [[Project]].",
		Chunker: ChunkerOptions{TargetCharacters: 100},
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusExtracted {
		t.Fatalf("status = %q, want extracted", extraction.Status)
	}
	if extraction.ExtractorKey != ExtractorKeyMarkdownText || extraction.ExtractorVersion == "" {
		t.Fatalf("extractor = %q/%q", extraction.ExtractorKey, extraction.ExtractorVersion)
	}
	if len(extraction.TextSections) != 1 || extraction.TextSections[0].TextSource != TextSourceEmbeddedText {
		t.Fatalf("text sections = %#v, want one embedded section", extraction.TextSections)
	}
	if len(extraction.Chunks) != 1 {
		t.Fatalf("chunks len = %d, want 1", len(extraction.Chunks))
	}
	chunk := extraction.Chunks[0]
	if chunk.TextSource != TextSourceEmbeddedText ||
		chunk.ExtractorKey != ExtractorKeyMarkdownText ||
		chunk.ExtractionStatus != ExtractionStatusExtracted {
		t.Fatalf("chunk provenance = %#v", chunk)
	}
	if len(extraction.Links) != 1 {
		t.Fatalf("links len = %d, want 1", len(extraction.Links))
	}
}

func TestDefaultExtractionRegistryReturnsMetadataOnlyForUnsupportedClass(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassImage, "diagram.png")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{Object: object})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusUnsupportedBodyExtraction {
		t.Fatalf("status = %q, want unsupported_body_extraction", extraction.Status)
	}
	if extraction.ExtractorKey != ExtractorKeyMetadataOnly {
		t.Fatalf("extractor key = %q, want metadata_only", extraction.ExtractorKey)
	}
	if len(extraction.Chunks) != 0 {
		t.Fatalf("chunks = %#v, want none", extraction.Chunks)
	}
	if len(extraction.TextSections) != 1 || extraction.TextSections[0].TextSource != TextSourceMetadataText {
		t.Fatalf("text sections = %#v, want metadata section", extraction.TextSections)
	}
}

func TestExtractorRespectsExtractedTextLimit(t *testing.T) {
	object := testKnowledgeObjectForExtraction(storagecatalog.FileClassText, "large.txt")

	extraction, err := DefaultExtractionRegistry().Extract(ExtractionInput{
		Object:                object,
		Content:               "0123456789",
		MaxExtractedTextBytes: 5,
	})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if extraction.Status != ExtractionStatusTooLarge {
		t.Fatalf("status = %q, want too_large", extraction.Status)
	}
	if len(extraction.Chunks) != 0 {
		t.Fatalf("chunks = %#v, want none", extraction.Chunks)
	}
}

func testKnowledgeObjectForExtraction(fileClass, relativePath string) KnowledgeObject {
	return KnowledgeObject{
		KnowledgeObjectID: ids.NewKnowledgeObjectID(),
		NotesSourceRootID: ids.NewNotesSourceRootID(),
		RelativePath:      relativePath,
		FileClass:         fileClass,
		ProcessingState:   ProcessingStateMetadataOnly,
	}
}
