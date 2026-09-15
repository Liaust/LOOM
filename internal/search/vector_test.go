package search

import "testing"

func TestVectorDistanceScore(t *testing.T) {
	if score := VectorDistanceScore(0); score != 1 {
		t.Fatalf("score = %v, want 1", score)
	}
	if score := VectorDistanceScore(3); score != 0.25 {
		t.Fatalf("score = %v, want 0.25", score)
	}
	if score := VectorDistanceScore(-1); score != 1 {
		t.Fatalf("negative score = %v, want 1", score)
	}
}
