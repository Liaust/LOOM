package knowledge

import "testing"

func TestRetainedEmbeddingIDsKeepsCurrentAndRecentHistorical(t *testing.T) {
	candidates := []embeddingRetentionCandidate{
		{ID: "active", Lineage: "a", Active: true},
		{ID: "hist-4", Lineage: "a"},
		{ID: "hist-3", Lineage: "a"},
		{ID: "hist-2", Lineage: "a"},
		{ID: "hist-1", Lineage: "a"},
		{ID: "hist-0", Lineage: "a"},
		{ID: "other-1", Lineage: "b"},
	}
	retained := retainedEmbeddingIDs(candidates, 5)
	for _, id := range []string{"active", "hist-4", "hist-3", "hist-2", "hist-1", "other-1"} {
		if _, ok := retained[id]; !ok {
			t.Fatalf("expected %s to be retained", id)
		}
	}
	if _, ok := retained["hist-0"]; ok {
		t.Fatal("oldest inactive embedding should not be retained")
	}
}

func TestRetainedEmbeddingIDsDefaultsHistoryWhenNegative(t *testing.T) {
	candidates := []embeddingRetentionCandidate{
		{ID: "active", Lineage: "a", Active: true},
		{ID: "hist-4", Lineage: "a"},
		{ID: "hist-3", Lineage: "a"},
		{ID: "hist-2", Lineage: "a"},
		{ID: "hist-1", Lineage: "a"},
		{ID: "hist-0", Lineage: "a"},
	}
	retained := retainedEmbeddingIDs(candidates, -1)
	if _, ok := retained["hist-1"]; !ok {
		t.Fatal("default history should retain four inactive embeddings")
	}
	if _, ok := retained["hist-0"]; ok {
		t.Fatal("default history should drop the fifth inactive embedding")
	}
}
