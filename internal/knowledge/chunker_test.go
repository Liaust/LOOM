package knowledge

import (
	"strings"
	"testing"
)

func TestChunkTextDocumentUsesHeadingPaths(t *testing.T) {
	document := ExtractMarkdownDocument("# Runtime\n\nThe VM runs a headless node-agent.\n\n## Network\n\nThe Mac talks to main over LOOM routes.")

	chunks := ChunkTextDocument(document, ChunkerOptions{TargetCharacters: 80, MaxCharacters: 120})

	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, want 2: %#v", len(chunks), chunks)
	}
	if chunks[0].StructuralPath != "Runtime" {
		t.Fatalf("first structural path = %q, want Runtime", chunks[0].StructuralPath)
	}
	if chunks[1].StructuralPath != "Runtime / Network" {
		t.Fatalf("second structural path = %q, want Runtime / Network", chunks[1].StructuralPath)
	}
	if chunks[0].ChunkHash == chunks[1].ChunkHash || !strings.HasPrefix(chunks[0].ChunkHash, "sha256:") {
		t.Fatalf("chunk hashes = %q/%q, want stable sha256 hashes", chunks[0].ChunkHash, chunks[1].ChunkHash)
	}
}

func TestChunkTextDocumentSplitsLongPlainText(t *testing.T) {
	document := ExtractPlainTextDocument(strings.Repeat("word ", 80))

	chunks := ChunkTextDocument(document, ChunkerOptions{TargetCharacters: 80, MaxCharacters: 90})

	if len(chunks) < 2 {
		t.Fatalf("chunks len = %d, want split long text", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk.Text) > 90 {
			t.Fatalf("chunk text len = %d, want <= 90", len(chunk.Text))
		}
		if chunk.TokenCountEstimate == 0 {
			t.Fatalf("chunk token estimate = 0: %#v", chunk)
		}
	}
}

func TestChunkTextDocumentCarriesProvenance(t *testing.T) {
	document := ExtractPlainTextDocument("alpha beta gamma")

	chunks := ChunkTextDocumentWithProvenance(document, ChunkerOptions{TargetCharacters: 100}, ChunkProvenance{
		TextSource:       TextSourceEmbeddedText,
		ExtractorKey:     ExtractorKeyMarkdownText,
		ExtractorVersion: "test_extractor_v1",
		ExtractionStatus: ExtractionStatusExtracted,
		SectionIndex:     3,
	})

	if len(chunks) != 1 {
		t.Fatalf("chunks len = %d, want 1", len(chunks))
	}
	got := chunks[0]
	if got.TextSource != TextSourceEmbeddedText ||
		got.ExtractorKey != ExtractorKeyMarkdownText ||
		got.ExtractorVersion != "test_extractor_v1" ||
		got.ExtractionStatus != ExtractionStatusExtracted ||
		got.SectionIndex != 3 {
		t.Fatalf("chunk provenance = %#v", got)
	}
}
