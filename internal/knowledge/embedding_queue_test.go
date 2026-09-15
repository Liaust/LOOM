package knowledge

import (
	"strings"
	"testing"
	"time"
)

func TestBuildEmbeddingTrajectoryPlanOffPreventsQueueing(t *testing.T) {
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	plan, err := buildEmbeddingTrajectoryPlan(embeddingTrajectoryPlanInput{
		Settings:           defaultEmbeddingSettings(now),
		Object:             testEmbeddingObject(),
		KnowledgeVersionID: "knowledge_object_version_test",
		Chunks:             []KnowledgeChunk{testEmbeddingChunk("knowledge_chunk_a")},
		PreviousGeneration: 0,
		ChangedAt:          now,
	})
	if err != nil {
		t.Fatalf("buildEmbeddingTrajectoryPlan returned error: %v", err)
	}
	if plan.State.Status != EmbeddingObjectStatusDisabledByPolicy {
		t.Fatalf("state status = %q, want disabled_by_policy", plan.State.Status)
	}
	if len(plan.WorkItems) != 0 {
		t.Fatalf("work items len = %d, want 0 while embeddings are OFF", len(plan.WorkItems))
	}
	if plan.State.Generation != 1 {
		t.Fatalf("generation = %d, want 1", plan.State.Generation)
	}
}

func TestBuildEmbeddingTrajectoryPlanOnQueuesChunksAfterQuietWindow(t *testing.T) {
	now := time.Date(2026, 7, 5, 9, 10, 0, 0, time.UTC)
	settings := defaultEmbeddingSettings(now)
	settings.Enabled = true
	plan, err := buildEmbeddingTrajectoryPlan(embeddingTrajectoryPlanInput{
		Settings:           settings,
		Object:             testEmbeddingObject(),
		KnowledgeVersionID: "knowledge_object_version_test",
		Chunks: []KnowledgeChunk{
			testEmbeddingChunk("knowledge_chunk_a"),
			testEmbeddingChunk("knowledge_chunk_b"),
		},
		PreviousGeneration: 4,
		ChangedAt:          now,
	})
	if err != nil {
		t.Fatalf("buildEmbeddingTrajectoryPlan returned error: %v", err)
	}
	if plan.State.Status != EmbeddingObjectStatusQueued {
		t.Fatalf("state status = %q, want queued", plan.State.Status)
	}
	if plan.State.Generation != 5 {
		t.Fatalf("generation = %d, want 5", plan.State.Generation)
	}
	if plan.State.EligibleAt == nil || !plan.State.EligibleAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("eligible_at = %v, want changed_at + 10 minutes", plan.State.EligibleAt)
	}
	if len(plan.WorkItems) != 2 {
		t.Fatalf("work items len = %d, want 2", len(plan.WorkItems))
	}
	for _, item := range plan.WorkItems {
		if item.Status != EmbeddingWorkStatusQueued {
			t.Fatalf("work status = %q, want queued", item.Status)
		}
		if !item.EligibleAt.Equal(now.Add(10 * time.Minute)) {
			t.Fatalf("work eligible_at = %s, want quiet-window eligible time", item.EligibleAt)
		}
		if item.Generation != 5 {
			t.Fatalf("work generation = %d, want 5", item.Generation)
		}
	}
}

func TestLimitReadyEmbeddingWorkItemsReturnsOneWhenLimitIsOne(t *testing.T) {
	now := time.Date(2026, 7, 5, 9, 30, 0, 0, time.UTC)
	items := make([]EmbeddingWorkItem, 0, 5)
	for i := 0; i < 5; i++ {
		items = append(items, EmbeddingWorkItem{
			KnowledgeEmbeddingWorkItemID: "knowledge_embedding_work_item_test",
			Status:                       EmbeddingWorkStatusQueued,
			EligibleAt:                   now.Add(-time.Minute),
			QueuedAt:                     now.Add(-2 * time.Minute),
		})
	}
	claimed := limitReadyEmbeddingWorkItems(items, 1, now)
	if len(claimed) != 1 {
		t.Fatalf("claimed len = %d, want 1", len(claimed))
	}
}

func TestTrajectoryResetRefreshesWaitingFileToNewGeneration(t *testing.T) {
	firstChange := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	secondChange := firstChange.Add(2 * time.Minute)
	settings := defaultEmbeddingSettings(firstChange)
	settings.Enabled = true
	first, err := buildEmbeddingTrajectoryPlan(embeddingTrajectoryPlanInput{
		Settings:           settings,
		Object:             testEmbeddingObject(),
		KnowledgeVersionID: "knowledge_object_version_test",
		Chunks:             []KnowledgeChunk{testEmbeddingChunk("knowledge_chunk_a")},
		PreviousGeneration: 0,
		ChangedAt:          firstChange,
	})
	if err != nil {
		t.Fatalf("first plan error: %v", err)
	}
	second, err := buildEmbeddingTrajectoryPlan(embeddingTrajectoryPlanInput{
		Settings:           settings,
		Object:             testEmbeddingObject(),
		KnowledgeVersionID: "knowledge_object_version_test2",
		Chunks:             []KnowledgeChunk{testEmbeddingChunk("knowledge_chunk_a")},
		PreviousGeneration: first.State.Generation,
		ChangedAt:          secondChange,
	})
	if err != nil {
		t.Fatalf("second plan error: %v", err)
	}
	if second.State.Generation != first.State.Generation+1 {
		t.Fatalf("second generation = %d, want first + 1", second.State.Generation)
	}
	if !second.WorkItems[0].EligibleAt.Equal(secondChange.Add(10 * time.Minute)) {
		t.Fatalf("second eligible_at = %s, want reset quiet window", second.WorkItems[0].EligibleAt)
	}
	if !first.WorkItems[0].EligibleAt.Before(second.WorkItems[0].EligibleAt) {
		t.Fatalf("first eligible_at = %s, second = %s, want reset later", first.WorkItems[0].EligibleAt, second.WorkItems[0].EligibleAt)
	}
}

func TestNormalizeEmbeddingWorkClaimOptionsDefaultsToSingleLane(t *testing.T) {
	opts := normalizeEmbeddingWorkClaimOptions(EmbeddingWorkClaimOptions{})
	if opts.Limit != 1 {
		t.Fatalf("limit = %d, want 1", opts.Limit)
	}
	if opts.LeaseDuration != defaultEmbeddingLeaseDuration {
		t.Fatalf("lease duration = %s, want %s", opts.LeaseDuration, defaultEmbeddingLeaseDuration)
	}
	opts = normalizeEmbeddingWorkClaimOptions(EmbeddingWorkClaimOptions{Limit: 999})
	if opts.Limit != maxEmbeddingClaimLimit {
		t.Fatalf("limit = %d, want capped max %d", opts.Limit, maxEmbeddingClaimLimit)
	}
}

func testEmbeddingObject() KnowledgeObject {
	return KnowledgeObject{
		KnowledgeObjectID: "knowledge_object_test",
		FileClass:         "markdown",
		ProcessingState:   ProcessingStateChunked,
	}
}

func testEmbeddingChunk(id string) KnowledgeChunk {
	versionID := "knowledge_object_version_test"
	return KnowledgeChunk{
		KnowledgeChunkID:         id,
		KnowledgeObjectID:        "knowledge_object_test",
		KnowledgeObjectVersionID: &versionID,
		ChunkIndex:               1,
		ChunkText:                "embedding queue test",
		ChunkHash:                "sha256:" + strings.Repeat("a", 64),
		ChunkerVersion:           KnowledgeMarkdownTextChunkerVersion,
		Status:                   ChunkStatusCreated,
	}
}
