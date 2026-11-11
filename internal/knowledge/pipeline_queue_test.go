package knowledge

import (
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/migrations"
)

func TestPipelineClaimHeavyAlternatesPromotesAndHonorsDeadlinesPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s, text := pipelineFixture(t, db, "markdown", "text/markdown", "text", now)
	_, asset := pipelineFixture(t, db, "image", "image/png", "asset", now)
	textRun, err := s.EnsurePipelineRun(t.Context(), text, PipelinePolicy{EmbeddingsEnabled: true}, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	assetRun, err := s.EnsurePipelineRun(t.Context(), asset, PipelinePolicy{ImageOCREnabled: true}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, textRun.KnowledgePipelineRunID, FilePipelineStageEmbedding, now)
	positionPipelineAtStage(t, db, assetRun.KnowledgePipelineRunID, FilePipelineStageImageOCR, now)
	first := claimSinglePipeline(t, s, PipelineExecutionHeavy, "first", now)
	if !IsOrdinaryPipelineWork(first) {
		t.Fatal("ordinary priority ignored")
	}
	if err = s.ReleasePipelineClaim(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := claimSinglePipeline(t, s, PipelineExecutionHeavy, "second", now.Add(time.Second))
	if second.Run.KnowledgePipelineRunID != assetRun.KnowledgePipelineRunID {
		t.Fatal("optional lane did not alternate")
	}
	if err = s.ReleasePipelineClaim(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET updated_at=$2 WHERE knowledge_pipeline_run_id=$1 AND stage_key='image_ocr'`, assetRun.KnowledgePipelineRunID, now.Add(-6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	aged := claimSinglePipeline(t, s, PipelineExecutionHeavy, "aged", now.Add(2*time.Second))
	if aged.Run.KnowledgePipelineRunID != assetRun.KnowledgePipelineRunID {
		t.Fatal("old optional work starved after service in its lane")
	}
	if err = s.ReleasePipelineClaim(t.Context(), aged); err != nil {
		t.Fatal(err)
	}
	future := now.Add(20 * time.Minute)
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_runs SET next_attempt_at=$2 WHERE knowledge_pipeline_run_id=$1 AND stage_key='image_ocr'`, assetRun.KnowledgePipelineRunID, future); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE knowledge.knowledge_objects SET source_revision='superseded' WHERE knowledge_object_id=$1`, text.KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ClaimPipelineRuns(t.Context(), PipelineExecutionHeavy, "not-due", PipelineClaimOptions{Limit: 1, Now: now.Add(3 * time.Second)})
	if err != nil || len(items) != 0 {
		t.Fatalf("claimed stale or retry-future work: %+v %v", items, err)
	}
	due, err := s.NextPipelineWorkAt(t.Context(), PipelineExecutionHeavy, now.Add(3*time.Second))
	if err != nil || due == nil || !due.Equal(future) {
		t.Fatalf("readiness does not share exact deadlines: %v %v", due, err)
	}
}

func TestPDFUnitYieldReleasesOnlyUnfinishedOwnedUnitsPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	s, object := pipelineFixture(t, db, "pdf", "application/pdf", "owned fixture", now)
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{PDFOCREnabled: true}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	positionPipelineAtStage(t, db, run.KnowledgePipelineRunID, FilePipelineStagePDFOCR, now)
	claim := claimSinglePipeline(t, s, PipelineExecutionHeavy, "yield", now)
	for page := 1; page <= 4; page++ {
		if err = s.store.upsertPDFOCRUnit(t.Context(), claim.Stage, PDFPage{Number: page}, object.SourceHash); err != nil {
			t.Fatal(err)
		}
	}
	units, err := s.store.listReadyPDFOCRUnits(t.Context(), claim, 4)
	if err != nil || len(units) != 4 {
		t.Fatalf("%+v %v", units, err)
	}
	if _, err = db.Exec(`UPDATE knowledge.pipeline_stage_units SET status='complete',claimed_by_worker_run_id='',claim_generation=0,completed_at=now() WHERE knowledge_pipeline_stage_unit_id=$1`, units[0].KnowledgePipelineStageUnitID); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleasePipelineClaim(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
	assertPDFUnitCounts(t, db, claim.Stage.KnowledgePipelineStageRunID, map[string]int{PipelineStageStatusComplete: 1, PipelineStageStatusReady: 3, PipelineStageStatusProcessing: 0})
	resumed := claimSinglePipeline(t, s, PipelineExecutionHeavy, "resume", now.Add(time.Second))
	units, err = s.store.listReadyPDFOCRUnits(t.Context(), resumed, 4)
	if err != nil || len(units) != 3 {
		t.Fatal("redid completed page", err)
	}
	for _, unit := range units {
		if unit.AttemptCount != 1 {
			t.Fatal("yield charged an unexecuted unit retry")
		}
	}
}
