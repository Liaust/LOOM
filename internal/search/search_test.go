package search

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestChunkTextUsesMarkdownHeadingAsStructuralPath(t *testing.T) {
	chunks := chunkText("# Runtime\n\nThe VM runs a headless node-agent.\n\nThe macOS client talks over SSH and rsync.")

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].StructuralPath != "Runtime" {
		t.Fatalf("expected structural path Runtime, got %q", chunks[0].StructuralPath)
	}
	if chunks[0].Index != 1 {
		t.Fatalf("expected chunk index 1, got %d", chunks[0].Index)
	}
	if !strings.HasPrefix(chunks[0].HashURI, "sha256:") {
		t.Fatalf("expected sha256 hash uri, got %q", chunks[0].HashURI)
	}
}

func TestChunkTextSplitsLongParagraph(t *testing.T) {
	text := strings.Repeat("capability routing ", 600)
	chunks := chunkText(text)

	if len(chunks) < 2 {
		t.Fatalf("expected long paragraph to split, got %d chunks", len(chunks))
	}
	for i, chunk := range chunks {
		if chunk.Index != i+1 {
			t.Fatalf("expected chunk index %d, got %d", i+1, chunk.Index)
		}
		if len(chunk.Text) > maxChunkCharacters {
			t.Fatalf("chunk %d exceeded max characters: %d", i+1, len(chunk.Text))
		}
	}
}

func TestSupportedTextTarget(t *testing.T) {
	if !supportedTextTarget(indexTarget{Extension: "md", MimeType: "text/markdown"}) {
		t.Fatal("markdown should be supported")
	}
	if supportedTextTarget(indexTarget{Extension: "png", MimeType: "image/png"}) {
		t.Fatal("png should not be supported")
	}
}

func TestRetryableIndexErrorClassification(t *testing.T) {
	if RetryableIndexError(sql.ErrNoRows) {
		t.Fatal("missing object version should be terminal")
	}
	if RetryableIndexError(fmt.Errorf("blob is not valid utf-8 text")) {
		t.Fatal("decode failure should be terminal")
	}
	if !RetryableIndexError(fmt.Errorf("read object-store blob: temporary timeout")) {
		t.Fatal("object-store read failure should be retryable")
	}
}

func TestRetryDelayCapsAtMax(t *testing.T) {
	got := retryDelay(10, 25, 4)
	if got != 25 {
		t.Fatalf("retry delay = %v, want capped max 25ns", got)
	}
}
