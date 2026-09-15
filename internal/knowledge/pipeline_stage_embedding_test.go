package knowledge

import "testing"

func TestUnifiedEmbeddingItemUsesPipelineGeneration(t *testing.T) {
	chunkID, versionID := "knowledge_chunk_01ARZ3NDEKTSV4RRFFQ69G5FAV", "knowledge_object_version_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	item := unifiedEmbeddingItem(PipelineWorkItem{Run: PipelineRun{Generation: 9}}, KnowledgeChunk{KnowledgeChunkID: chunkID, KnowledgeObjectID: "knowledge_object_01ARZ3NDEKTSV4RRFFQ69G5FAV", KnowledgeObjectVersionID: &versionID}, EmbeddingSettings{RuntimeKey: "ollama", ModelKey: "model", Dimensions: 3})
	if item.Generation != 9 || item.KnowledgeChunkID == nil || *item.KnowledgeChunkID != chunkID {
		t.Fatalf("item=%#v", item)
	}
}
