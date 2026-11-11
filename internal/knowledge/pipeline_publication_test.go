package knowledge

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/migrations"
)

func TestPipelinePublicationRejectsSupersededCapturedSourcePostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s, object := pipelineFixture(t, db, "markdown", "text/markdown", "original body", now)
	s.sourceStagingRoot = t.TempDir()
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	claim := claimSinglePipeline(t, s, PipelineExecutionCoordinator, "old-source", now)
	claim, err = s.capturePipelineSource(t.Context(), claim)
	if err != nil || len(claim.Run.SourceSnapshot) == 0 {
		t.Fatal("source capture", err)
	}
	defer cleanupSelectedPipelineInput(claim.Run)
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET source_hash=$2,source_revision='new-revision' WHERE knowledge_object_id=$1`, object.KnowledgeObjectID, textHashURI("new body")); err != nil {
		t.Fatal(err)
	}
	version, err := s.store.getKnowledgeObjectVersion(t.Context(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	body := "old captured text must not publish"
	err = s.publishConsolidatedLexical(t.Context(), claim, version, DerivedArtifact{TextContent: &body, GeneratorKey: "loom.consolidation", GeneratorVersion: StageContractVersionV1})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("late publication was not fenced: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_chunks WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late publication changed chunks: %d %v", count, err)
	}
}

func TestPipelinePublicationQuietTimingSkipsEquivalentMetadataPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s, object := pipelineFixture(t, db, "markdown", "text/markdown", "quiet metadata", now)
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	var plan CompiledPipelinePlan
	if err := json.Unmarshal(run.PlanSnapshot, &plan); err != nil {
		t.Fatal(err)
	}
	stageKey := plan.Stages[0].StageKey
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET attempt_count=2,error_json='{"code":"retained"}'::jsonb WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, run.KnowledgePipelineRunID, stageKey); err != nil {
		t.Fatal(err)
	}
	refresh := func() {
		t.Helper()
		snapshot, err := plan.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err = refreshQueuedPipelineTimingTx(t.Context(), tx, run, object, plan, snapshot, now); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	read := func() (string, bool) {
		t.Helper()
		var xmin, errorCode string
		var quiet bool
		var attempts int
		if err := db.QueryRow(`SELECT xmin::text,(metadata->>'quiet_window')::boolean,attempt_count,error_json->>'code' FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, run.KnowledgePipelineRunID, stageKey).Scan(&xmin, &quiet, &attempts, &errorCode); err != nil {
			t.Fatal(err)
		}
		if attempts != 2 || errorCode != "retained" {
			t.Fatal("quiet metadata update changed attempts/error receipt")
		}
		return xmin, quiet
	}
	refresh()
	first, quiet := read()
	refresh()
	second, again := read()
	if first != second || quiet != again {
		t.Fatal("equivalent stage metadata created a new tuple")
	}
	plan.Stages[0].QuietWindow = !quiet
	refresh()
	third, changed := read()
	if third == second || changed == quiet {
		t.Fatal("changed quiet metadata was not persisted")
	}
}

func TestPipelinePublicationReferenceImageCompletesNormallyPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	s, object := pipelineFixture(t, db, "image", "image/png", "reference bytes", time.Now().UTC())
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{WorkerRunID: "worker_reference", Limit: 20})
	if err != nil || result.Failed != 0 || result.Completed != 3 {
		var failures string
		_ = db.QueryRow(`SELECT coalesce(string_agg(stage_key||':'||error_json::text,','),'') FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND error_json<>'{}'::jsonb`, run.KnowledgePipelineRunID).Scan(&failures)
		t.Fatalf("%+v %v stages=%s", result, err, failures)
	}
	current, err := s.store.GetPipelineRun(t.Context(), run.KnowledgePipelineRunID)
	if err != nil || current.Status != FilePipelineStatusComplete || current.WarningCount != 0 {
		t.Fatalf("%+v %v", current, err)
	}
}

func TestPipelinePublicationEmptyBodyRetiresSemanticPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s, object := pipelineFixture(t, db, "markdown", "text/markdown", "earlier body", now)
	policy := PipelinePolicy{EmbeddingsEnabled: true}
	run, err := s.EnsurePipelineRun(t.Context(), object, policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	version, err := s.store.getKnowledgeObjectVersion(t.Context(), run.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageLexicalIndex, now)
	lexical := claimSinglePipeline(t, s, PipelineExecutionCoordinator, "lexical-old", now)
	body := "previous semantic body"
	if err = s.publishConsolidatedLexical(t.Context(), lexical, version, DerivedArtifact{TextContent: &body, GeneratorKey: "loom.consolidation", GeneratorVersion: StageContractVersionV1}); err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
	embedding := claimSinglePipeline(t, s, PipelineExecutionHeavy, "embedding-old", now)
	settings, err := s.store.GetEmbeddingSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := s.store.listKnowledgeChunksForVersion(t.Context(), version.KnowledgeObjectVersionID)
	if err != nil || len(chunks) != 1 {
		t.Fatalf("%+v %v", chunks, err)
	}
	vector := make([]float32, settings.Dimensions)
	vector[0] = 1
	if err = s.activateUnifiedChunkEmbedding(t.Context(), embedding, chunks[0], settings, vector, hashArtifactValue(chunks[0].ChunkText)); err != nil {
		t.Fatal(err)
	}
	if err = s.publishUnifiedEmbeddingObjectState(t.Context(), embedding, settings); err != nil {
		t.Fatal(err)
	}
	completeRunForTest(t, db, run.KnowledgePipelineRunID)
	next, err := s.EnsurePipelineRun(t.Context(), object, policy, true, 100)
	if err != nil {
		t.Fatal(err)
	}
	version, err = s.store.getKnowledgeObjectVersion(t.Context(), next.KnowledgeObjectVersionID)
	if err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, next.KnowledgePipelineRunID, FilePipelineStageLexicalIndex, now)
	lexical = claimSinglePipeline(t, s, PipelineExecutionCoordinator, "lexical-empty", now)
	if err = s.publishConsolidatedLexical(t.Context(), lexical, version, DerivedArtifact{}); err != nil {
		t.Fatal(err)
	}
	var retired bool
	if err = db.QueryRow(`SELECT semantic_version_id IS NULL AND semantic_published_at IS NULL AND semantic_runtime_key IS NULL AND semantic_model_key IS NULL AND semantic_dimensions IS NULL FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&retired); err != nil || !retired {
		t.Fatal("stale semantic descriptor", err)
	}
	var total, active int
	if err = db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE active) FROM knowledge.chunk_embeddings WHERE knowledge_object_id=$1`, object.KnowledgeObjectID).Scan(&total, &active); err != nil || total != 1 || active != 0 {
		t.Fatalf("lost history or retained active vectors: %d %d %v", total, active, err)
	}
}
