package knowledge

import (
	"strings"
	"testing"
)

func TestPrepareEmbeddingPassagesPreservesChunkMappingAndRawText(t *testing.T) {
	start := 10
	end := 42
	versionID := "knowledge_object_version_01HZZZZZZZZZZZZZZZZZZZZZZZ"
	chunk := KnowledgeChunk{
		KnowledgeChunkID:         "knowledge_chunk_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		KnowledgeObjectID:        "knowledge_object_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		KnowledgeObjectVersionID: &versionID,
		ChunkIndex:               3,
		ChunkText:                "  plain passage text  ",
		ChunkHash:                "sha256:" + strings.Repeat("a", 64),
		StructuralPath:           "Runtime / Network",
		StartOffset:              &start,
		EndOffset:                &end,
	}

	passages, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{})
	if err != nil {
		t.Fatalf("PrepareEmbeddingPassages returned error: %v", err)
	}
	if len(passages) != 1 {
		t.Fatalf("passages len = %d, want 1", len(passages))
	}
	passage := passages[0]
	if passage.Text != "plain passage text" {
		t.Fatalf("Text = %q, want raw trimmed passage text", passage.Text)
	}
	if passage.StructuralPath != "Runtime / Network" {
		t.Fatalf("StructuralPath = %q, want Runtime / Network", passage.StructuralPath)
	}
	if passage.KnowledgeChunkID != chunk.KnowledgeChunkID || passage.KnowledgeObjectID != chunk.KnowledgeObjectID {
		t.Fatalf("passage mapping = %#v, want source chunk/object mapping", passage)
	}
	wantStart := start + 2
	if passage.StartOffset == nil || *passage.StartOffset != wantStart {
		t.Fatalf("StartOffset = %v, want %d", passage.StartOffset, wantStart)
	}
	if passage.EndOffset == nil || *passage.EndOffset != wantStart+len("plain passage text") {
		t.Fatalf("EndOffset = %v, want trimmed passage end", passage.EndOffset)
	}
	if !strings.HasPrefix(passage.PassageHash, "sha256:") {
		t.Fatalf("PassageHash = %q, want sha256 hash", passage.PassageHash)
	}
}

func TestPrepareEmbeddingPassagesSplitsOversizeInputDeterministically(t *testing.T) {
	text := strings.Repeat("alpha ", 40) + strings.Repeat("beta ", 40)
	chunk := KnowledgeChunk{
		KnowledgeChunkID:  "knowledge_chunk_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		KnowledgeObjectID: "knowledge_object_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		ChunkIndex:        1,
		ChunkText:         text,
		ChunkHash:         "sha256:" + strings.Repeat("b", 64),
	}

	first, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{
		TargetCharacters: 80,
		MaxCharacters:    100,
	})
	if err != nil {
		t.Fatalf("PrepareEmbeddingPassages returned error: %v", err)
	}
	second, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{
		TargetCharacters: 80,
		MaxCharacters:    100,
	})
	if err != nil {
		t.Fatalf("PrepareEmbeddingPassages second run returned error: %v", err)
	}
	if len(first) < 2 {
		t.Fatalf("passages len = %d, want oversize text split", len(first))
	}
	if len(first) != len(second) {
		t.Fatalf("second split len = %d, want %d", len(second), len(first))
	}
	for i := range first {
		if len(first[i].Text) > 100 {
			t.Fatalf("passage %d len = %d, want <= 100", i, len(first[i].Text))
		}
		if first[i].PassageHash != second[i].PassageHash {
			t.Fatalf("passage %d hash changed between runs: %q vs %q", i, first[i].PassageHash, second[i].PassageHash)
		}
		if first[i].PassageIndex != i+1 {
			t.Fatalf("passage %d index = %d, want %d", i, first[i].PassageIndex, i+1)
		}
	}
}

func TestPrepareEmbeddingPassagesSplitsSingleLongTokenInsteadOfTruncating(t *testing.T) {
	chunk := KnowledgeChunk{
		KnowledgeChunkID:  "knowledge_chunk_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		KnowledgeObjectID: "knowledge_object_01HZZZZZZZZZZZZZZZZZZZZZZZ",
		ChunkIndex:        1,
		ChunkText:         strings.Repeat("x", 250),
		ChunkHash:         "sha256:" + strings.Repeat("c", 64),
	}

	passages, err := PrepareEmbeddingPassages([]KnowledgeChunk{chunk}, EmbeddingPassageOptions{
		TargetCharacters: 100,
		MaxCharacters:    100,
	})
	if err != nil {
		t.Fatalf("PrepareEmbeddingPassages returned error: %v", err)
	}
	combined := strings.Builder{}
	for _, passage := range passages {
		if len(passage.Text) > 100 {
			t.Fatalf("passage len = %d, want <= 100", len(passage.Text))
		}
		combined.WriteString(passage.Text)
	}
	if combined.String() != chunk.ChunkText {
		t.Fatalf("combined passages len/text mismatch; oversize input was not fully preserved")
	}
}

func TestEmbeddingRuntimeInputsFromPassagesRejectsEmptyText(t *testing.T) {
	if _, err := EmbeddingRuntimeInputsFromPassages([]EmbeddingPassage{{Text: " "}}); err == nil {
		t.Fatal("EmbeddingRuntimeInputsFromPassages accepted empty passage text")
	}
}
