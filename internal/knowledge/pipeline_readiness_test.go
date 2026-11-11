package knowledge

import (
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/storagecatalog"
)

func TestPipelineReadinessQuietRetryAndTerminalPostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	s, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "readiness", now)
	run, err := s.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	check := func(want *time.Time) {
		t.Helper()
		got, err := s.NextPipelineWorkAt(t.Context(), PipelineExecutionCoordinator, now)
		if err != nil || (want == nil && got != nil) || (want != nil && (got == nil || !got.Equal(*want))) {
			t.Fatalf("readiness got=%v want=%v error=%v", got, want, err)
		}
	}
	check(&now)
	quiet := now.Add(20 * time.Second)
	if _, err := db.Exec(`UPDATE knowledge.pipeline_runs SET status='waiting_quiet_window',quiet_window_eligible_at=$2 WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID, quiet); err != nil {
		t.Fatal(err)
	}
	check(&quiet)
	retry := now.Add(40 * time.Second)
	if _, err := db.Exec(`UPDATE knowledge.pipeline_stage_runs SET status='failed_retryable',next_attempt_at=$2 WHERE knowledge_pipeline_run_id=$1 AND stage_key=$3`, run.KnowledgePipelineRunID, retry, run.CurrentStageKey); err != nil {
		t.Fatal(err)
	}
	check(&retry)
	if _, err := db.Exec(`UPDATE knowledge.pipeline_runs SET status='blocked_manual_action' WHERE knowledge_pipeline_run_id=$1`, run.KnowledgePipelineRunID); err != nil {
		t.Fatal(err)
	}
	check(nil)
}
