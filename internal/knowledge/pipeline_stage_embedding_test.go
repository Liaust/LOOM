package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"loom.local/loom/internal/migrations"
	"path/filepath"
	"testing"
	"time"
)

func TestUnifiedEmbeddingItemUsesPipelineGeneration(t *testing.T) {
	chunkID, versionID := "knowledge_chunk_01ARZ3NDEKTSV4RRFFQ69G5FAV", "knowledge_object_version_01ARZ3NDEKTSV4RRFFQ69G5FAV"
	item := unifiedEmbeddingItem(PipelineWorkItem{Run: PipelineRun{Generation: 9}}, KnowledgeChunk{KnowledgeChunkID: chunkID, KnowledgeObjectID: "knowledge_object_01ARZ3NDEKTSV4RRFFQ69G5FAV", KnowledgeObjectVersionID: &versionID}, EmbeddingSettings{RuntimeKey: "ollama", ModelKey: "model", Dimensions: 3})
	if item.Generation != 9 || item.KnowledgeChunkID == nil || *item.KnowledgeChunkID != chunkID {
		t.Fatalf("item=%#v", item)
	}
}

func TestBoundedEmbeddingChunks(t *testing.T) {
	chunks := make([]KnowledgeChunk, 969)
	for i := range chunks {
		chunks[i] = KnowledgeChunk{ChunkIndex: i, ChunkText: "four"}
	}
	policy := DefaultHeavyResourcePolicy()
	remaining := chunks
	for turn, want := range []int{200, 200, 200, 200, 169} {
		selected, more, err := boundedEmbeddingChunks(remaining, policy)
		if err != nil || len(selected) != want || more != (turn < 4) {
			t.Fatalf("turn %d: %d chunks, more=%v, err=%v", turn, len(selected), more, err)
		}
		if selected[0].ChunkIndex != turn*200 {
			t.Fatal("chunk identity/order changed")
		}
		remaining = remaining[len(selected):]
	}
	policy.MaxInputBytes = 7
	selected, more, err := boundedEmbeddingChunks(chunks[:2], policy)
	if err != nil || len(selected) != 1 || !more {
		t.Fatalf("byte-limited turn: %v %v %v", selected, more, err)
	}
	policy.MaxInputBytes = 3
	if _, _, err = boundedEmbeddingChunks(chunks[:1], policy); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized single chunk must refuse: %v", err)
	}
	policy.MaxInputBytes = 4
	selected, more, err = boundedEmbeddingChunks(chunks[:1], policy)
	if err != nil || len(selected) != 1 || more {
		t.Fatalf("exact boundary: %v %v %v", selected, more, err)
	}
	selected, more, err = boundedEmbeddingChunks(nil, policy)
	if err != nil || len(selected) != 0 || more {
		t.Fatalf("empty: %v %v %v", selected, more, err)
	}
}

// Uses the existing disposable PostgreSQL fixture. No model server is needed.
func TestEmbeddingBoundedContinuationPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name                                 string
		count, budget, failCall, timeoutCall int
	}{
		{"969 chunks", 969, 200, 0, 0},
		{"late partial retry", 9, 2, 8, 0},
		{"late partial timeout", 9, 2, 0, 8},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			now := time.Now().UTC()
			policy := PipelinePolicy{EmbeddingsEnabled: true}
			setPipelinePolicyForTest(t, db, policy)
			s, object := pipelineFixture(t, db, "markdown", "text/markdown", scenario.name, now)
			s.sourceStagingRoot = t.TempDir()
			run, err := s.EnsurePipelineRun(t.Context(), object, policy, false, 100)
			if err != nil {
				t.Fatal(err)
			}
			version, err := s.store.getKnowledgeObjectVersion(t.Context(), run.KnowledgeObjectVersionID)
			if err != nil {
				t.Fatal(err)
			}
			inputs := make([]TextChunkInput, scenario.count)
			for i := range inputs {
				text := fmt.Sprintf("%s unique chunk %d %s", scenario.name, i, object.KnowledgeObjectID)
				inputs[i] = TextChunkInput{Index: i + 1, Text: text, ChunkHash: hashArtifactValue(text), EndOffset: len(text)}
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			chunks, err := s.replaceKnowledgeChunksTx(t.Context(), tx, object, version, inputs)
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
			settings, err := s.store.GetEmbeddingSettings(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			runtime := &continuationEmbeddingRuntime{t: t, failCall: scenario.failCall, timeoutCall: scenario.timeoutCall, settings: settings}
			budget := DefaultHeavyResourcePolicy()
			budget.MaxUnits = scenario.budget
			if scenario.timeoutCall != 0 {
				budget.TimeoutSeconds = 1
			}
			completed, failures := false, 0
			for turn := 0; turn < 10; turn++ {
				// Recreate the service on every turn: only persisted vectors carry progress.
				s = NewService(db, WithClock(func() time.Time { return now }), WithSourceStagingRoot(s.sourceStagingRoot))
				claim := claimSinglePipeline(t, s, PipelineExecutionHeavy, "bounded-embedding", now)
				if claim.Stage.AttemptCount != failures+1 {
					t.Fatalf("successful continuations consumed retries: attempts=%d failures=%d", claim.Stage.AttemptCount, failures)
				}
				if err = s.publishUnifiedEmbeddingObjectState(t.Context(), claim, settings); !errors.Is(err, ErrConflict) {
					t.Fatalf("published incomplete version: %v", err)
				}
				before := runtime.calls
				result, err := s.ExecuteClaimedHeavyStage(t.Context(), claim, budget, map[string]HeavyStageHandler{
					FilePipelineStageEmbedding: EmbeddingStageHandler{Service: s, Runtime: runtime},
				})
				if err != nil {
					t.Fatal(err)
				}
				if runtime.calls-before > scenario.budget {
					t.Fatal("turn exceeded unit budget")
				}
				var semantic sql.NullString
				if err = db.QueryRow(`SELECT semantic_version_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&semantic); err != nil {
					t.Fatal(err)
				}
				if result.Completed == 1 {
					if !semantic.Valid || semantic.String != run.KnowledgeObjectVersionID {
						t.Fatal("complete version not published")
					}
					completed = true
					break
				}
				if semantic.Valid {
					t.Fatal("partial version was published")
				}
				if result.Failed == 1 {
					failures++
					now = now.Add(time.Hour) // Normal persisted backoff, no sleep.
				}
			}
			wantCalls := scenario.count
			if scenario.failCall != 0 || scenario.timeoutCall != 0 {
				wantCalls++
			}
			if !completed || runtime.calls != wantCalls {
				t.Fatalf("completion=%v model calls=%d want=%d", completed, runtime.calls, wantCalls)
			}
			var active int
			if err = db.QueryRow(`SELECT count(*) FROM knowledge.chunk_embeddings WHERE knowledge_object_version_id=$1 AND active`, run.KnowledgeObjectVersionID).Scan(&active); err != nil {
				t.Fatal(err)
			}
			if active != len(chunks) {
				t.Fatalf("active=%d chunks=%d", active, len(chunks))
			}
			pending, err := s.store.listPendingUnifiedEmbeddingChunks(t.Context(), run.KnowledgeObjectVersionID, settings, 201)
			if err != nil || len(pending) != 0 {
				t.Fatalf("replay has unfinished work: %d %v", len(pending), err)
			}
			// A different model must not treat the persisted vectors as completed.
			settings.ModelKey = "different-model"
			pending, err = s.store.listPendingUnifiedEmbeddingChunks(t.Context(), run.KnowledgeObjectVersionID, settings, 201)
			if err != nil || len(pending) != min(201, scenario.count) {
				t.Fatalf("model identity lost: %d %v", len(pending), err)
			}
		})
	}
}

type continuationEmbeddingRuntime struct {
	t                            *testing.T
	settings                     EmbeddingSettings
	calls, failCall, timeoutCall int
}

func (r *continuationEmbeddingRuntime) Health(context.Context) EmbeddingRuntimeHealth {
	return EmbeddingRuntimeHealth{Available: true}
}

func (r *continuationEmbeddingRuntime) Embed(ctx context.Context, request EmbeddingRuntimeRequest) (EmbeddingRuntimeResponse, error) {
	r.calls++
	if request.Model != r.settings.ModelKey || request.Truncate {
		r.t.Fatal("model/context contract changed")
	}
	if r.calls == r.timeoutCall {
		<-ctx.Done()
		return EmbeddingRuntimeResponse{}, ctx.Err()
	}
	if r.calls == r.failCall {
		return EmbeddingRuntimeResponse{}, &EmbeddingRuntimeError{Kind: EmbeddingRuntimeErrorUnavailable, Message: "fixture interruption"}
	}
	vectors := make([][]float32, len(request.Inputs))
	for i := range vectors {
		vectors[i] = make([]float32, r.settings.Dimensions)
		vectors[i][0] = 1
	}
	return EmbeddingRuntimeResponse{Model: request.Model, Embeddings: vectors, Dimensions: r.settings.Dimensions}, nil
}
