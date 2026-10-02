package knowledge

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type batchEmbeddingRuntime struct {
	calls     int
	malformed bool
}

func (r *batchEmbeddingRuntime) Health(context.Context) EmbeddingRuntimeHealth {
	return EmbeddingRuntimeHealth{}
}
func (r *batchEmbeddingRuntime) Embed(_ context.Context, request EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	r.calls++
	if request.Truncate {
		return EmbeddingRuntimeResponse{}, fmt.Errorf("truncation enabled")
	}
	response := EmbeddingRuntimeResponse{}
	for i := range request.Inputs {
		response.Embeddings = append(response.Embeddings, []float32{float32(i + 1), 1})
	}
	if r.malformed {
		response.Embeddings = response.Embeddings[:len(response.Embeddings)-1]
	}
	return response, nil
}

func TestEmbeddingMicrobatchMappingAndCardinality(t *testing.T) {
	chunks := []KnowledgeChunk{{KnowledgeChunkID: "a", ChunkHash: "ha", ChunkText: "first"}, {KnowledgeChunkID: "b", ChunkHash: "hb", ChunkText: "second"}}
	runtime := &batchEmbeddingRuntime{}
	result, err := embedChunkBatch(t.Context(), runtime, "fixture", 2, chunks)
	if err != nil || runtime.calls != 1 || len(result) != 2 {
		t.Fatalf("batch: %+v %v", result, err)
	}
	if result[0].Vector[0] == result[1].Vector[0] || result[0].InputHash == result[1].InputHash {
		t.Fatal("chunk mapping collapsed")
	}
	for _, dimensions := range []int{1, 2} {
		runtime.malformed = dimensions == 2
		if _, err := embedChunkBatch(t.Context(), runtime, "fixture", dimensions, chunks); err == nil {
			t.Fatal("malformed response accepted")
		}
	}
}

func TestEmbeddingMicrobatchContextFallback(t *testing.T) {
	chunks := []KnowledgeChunk{{KnowledgeChunkID: "a", ChunkHash: "ha", ChunkText: strings.Repeat("dense ", 500)}, {KnowledgeChunkID: "b", ChunkHash: "hb", ChunkText: "small"}}
	runtime := &passageLimitRuntime{limit: 450}
	result, err := embedChunkBatch(t.Context(), runtime, "fixture", 2, chunks)
	if err != nil || len(result) != 2 || runtime.calls < 3 {
		t.Fatalf("fallback: %+v %v calls=%d", result, err, runtime.calls)
	}
	for i, chunk := range chunks {
		_, passages, err := embedChunkPassages(t.Context(), &passageLimitRuntime{limit: 450}, "fixture", chunk)
		if err != nil || result[i].InputHash != hashEmbeddingInput(passages) {
			t.Fatal("fallback lost passage identity")
		}
	}
}
