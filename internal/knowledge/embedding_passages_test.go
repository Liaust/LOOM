package knowledge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

type passageLimitRuntime struct {
	limit int
	calls int
	err   error
}

func (r *passageLimitRuntime) Health(context.Context) EmbeddingRuntimeHealth {
	return EmbeddingRuntimeHealth{Available: true}
}

func (r *passageLimitRuntime) Embed(_ context.Context, request EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	r.calls++
	if request.Truncate || request.Model != "fixture" {
		return EmbeddingRuntimeResponse{}, errors.New("request changed model or enabled truncation")
	}
	if r.err != nil {
		return EmbeddingRuntimeResponse{}, r.err
	}
	response := EmbeddingRuntimeResponse{Model: request.Model, Dimensions: 2}
	for _, input := range request.Inputs {
		if !utf8.ValidString(input) {
			return EmbeddingRuntimeResponse{}, errors.New("invalid UTF-8")
		}
		if len(input) > r.limit {
			return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorContextLength}
		}
		response.Embeddings = append(response.Embeddings, []float32{1, 2})
	}
	return response, nil
}

func TestEmbedChunkPassagesAdaptsWithoutLosingDenseUnicodeText(t *testing.T) {
	text := strings.Repeat("\u03b1\u2264\u03b2\u4e2d", 700)
	start := 0
	chunk := KnowledgeChunk{KnowledgeChunkID: "chunk", ChunkHash: hashArtifactValue(text), ChunkText: text, StartOffset: &start}
	runtime := &passageLimitRuntime{limit: 450}
	response, passages, err := embedChunkPassages(t.Context(), runtime, "fixture", chunk)
	if err != nil || len(response.Embeddings) != len(passages) || runtime.calls < 2 || runtime.calls > 7 {
		t.Fatalf("adaptive result: %d passages, %d calls, %v", len(passages), runtime.calls, err)
	}
	var joined strings.Builder
	for _, passage := range passages {
		if !utf8.ValidString(passage.Text) || passage.Text != text[*passage.StartOffset:*passage.EndOffset] {
			t.Fatal("passage changed source text or offsets")
		}
		joined.WriteString(passage.Text)
	}
	if joined.String() != text {
		t.Fatal("adaptive input was truncated")
	}
	_, repeated, err := embedChunkPassages(t.Context(), &passageLimitRuntime{limit: 450}, "fixture", chunk)
	if err != nil || !reflect.DeepEqual(passages, repeated) {
		t.Fatal("adaptive splitting is not deterministic")
	}
}

func TestEmbedChunkPassagesBoundsRefusalsAndDoesNotRetryOtherErrors(t *testing.T) {
	chunk := KnowledgeChunk{KnowledgeChunkID: "chunk", ChunkHash: "hash", ChunkText: strings.Repeat("dense ", 1000)}
	for _, kind := range []EmbeddingRuntimeErrorKind{EmbeddingRuntimeErrorUnavailable, EmbeddingRuntimeErrorBadStatus, EmbeddingRuntimeErrorContextLength} {
		runtime := &passageLimitRuntime{err: &EmbeddingRuntimeError{Kind: kind}}
		_, _, err := embedChunkPassages(t.Context(), runtime, "fixture", chunk)
		if err == nil || runtime.calls > 7 || (kind != EmbeddingRuntimeErrorContextLength && runtime.calls != 1) {
			t.Fatalf("%s: calls=%d error=%v", kind, runtime.calls, err)
		}
		if kind == EmbeddingRuntimeErrorContextLength && (RetryableKnowledgeError(err) || knowledgeProcessingErrorCode(err) != "embedding_context_length_exceeded") {
			t.Fatal("exhausted context refusal must be explicit and terminal")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runtime := &passageLimitRuntime{}
	if _, _, err := embedChunkPassages(ctx, runtime, "fixture", chunk); !errors.Is(err, context.Canceled) || runtime.calls != 0 {
		t.Fatal("cancelled work reached runtime")
	}
}

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
