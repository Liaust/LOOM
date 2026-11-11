package knowledge

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/migrations"
	"loom.local/loom/internal/storagecatalog"
)

func TestSmallNoteSelectedSourcePostgres(t *testing.T) {
	s, object, _, _ := syncedReaderFixture(t)
	ctx := t.Context()
	policy, err := s.GetPipelinePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.EnsurePipelineRun(ctx, object, policy.Policy, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ClaimPipelineRuns(ctx, PipelineExecutionCoordinator, "selected-source-worker", PipelineClaimOptions{PreferredRunID: run.KnowledgePipelineRunID, Limit: 1})
	if err != nil || len(items) != 1 || items[0].Run.KnowledgePipelineRunID != run.KnowledgePipelineRunID {
		t.Fatalf("exact initial claim: %+v %v", items, err)
	}
	item, err := s.capturePipelineSource(ctx, items[0])
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.selectedPipelineBinding(ctx, item.Object); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ name, change, restore string }{
		{"private", `UPDATE files.file_metadata SET index_policy='private_no_index'`, `UPDATE files.file_metadata SET index_policy='text_later'`},
		{"root-disabled", `UPDATE knowledge.notes_source_roots SET status='blocked'`, `UPDATE knowledge.notes_source_roots SET status='active'`},
		{"source-changed", `UPDATE knowledge.knowledge_objects SET source_revision=source_revision||'changed'`, `UPDATE knowledge.knowledge_objects SET source_revision=replace(source_revision,'changed','')`},
		{"run-cancelled", `UPDATE knowledge.pipeline_runs SET status='cancelled'`, `UPDATE knowledge.pipeline_runs SET status='processing' WHERE claimed_by_worker_run_id='selected-source-worker'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := s.store.db.ExecContext(ctx, test.change); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := s.store.db.ExecContext(ctx, test.restore); err != nil {
					t.Fatal(err)
				}
			}()
			if _, err := s.selectedPipelineBinding(ctx, item.Object); !errors.Is(err, ErrConflict) {
				t.Fatalf("cached read accepted changed admission: %v", err)
			}
		})
	}
	item.Object.pipelineSource.Input.Hash = hashArtifactValue("substituted")
	if _, err := s.selectedPipelineBinding(ctx, item.Object); !errors.Is(err, ErrConflict) {
		t.Fatalf("substituted selected input accepted: %v", err)
	}
}

func TestSmallNoteSQLCostPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	query := `EXPLAIN (ANALYZE,FORMAT JSON) SELECT ` + knowledgeObjectColumns() + `,
	 (` + visibleNotesKnowledgeObjectSQL("o") + ` AND ` + notesPublicationCurrentSQL("o", "o") + ` AND ` + notesCanonicalBodyOwnerSQL("o") + `),` + notesCustodyWriteAllowedSQL("o") + `
	 FROM knowledge.knowledge_objects o WHERE o.knowledge_object_id=$1 AND o.deleted_at IS NULL FOR UPDATE OF o`
	for _, jit := range []string{"on", "off"} {
		tx, err := s.store.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(t.Context(), "SET LOCAL jit="+jit); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := tx.QueryRowContext(t.Context(), query, objects[0].KnowledgeObjectID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plan []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &plan); err != nil {
			t.Fatal(err)
		}
		t.Logf("jit=%s planning=%s execution=%s JIT=%s", jit, plan[0]["Planning Time"], plan[0]["Execution Time"], plan[0]["JIT"])
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}
	query = `SELECT ` + knowledgeObjectColumns() + `,
	 (` + visibleNotesKnowledgeObjectSQL("o") + ` AND ` + notesPublicationCurrentSQL("o", "o") + ` AND ` + notesCanonicalBodyOwnerSQL("o") + `),` + notesCustodyWriteAllowedSQL("o") + `
	 FROM knowledge.knowledge_objects o WHERE o.knowledge_object_id=$1 AND o.deleted_at IS NULL FOR UPDATE OF o`
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		var samples []int64
		for range 4 {
			tx, err := s.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "SET LOCAL plan_cache_mode="+mode); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			var visible, writable bool
			if _, err := scanKnowledgeObject(pipelineClaimObjectRow{row: tx.QueryRowContext(t.Context(), query, objects[0].KnowledgeObjectID), visible: &visible, writable: &writable}); err != nil {
				t.Fatal(err)
			}
			samples = append(samples, time.Since(started).Microseconds())
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s query microseconds=%v", mode, samples)
	}
	// A cached plan must still evaluate current privacy, not reuse a successful
	// permission decision from the preceding executions with the same object ID.
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE files.file_metadata SET index_policy='private_no_index'`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := preparePipelineSingletonTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	var visible, writable bool
	if _, err := scanKnowledgeObject(pipelineClaimObjectRow{row: tx.QueryRowContext(t.Context(), query, objects[0].KnowledgeObjectID), visible: &visible, writable: &writable}); err != nil || visible {
		t.Fatalf("cached plan bypassed live privacy: %t %v", visible, err)
	}
}

func TestSmallNotePipelinePostgres(t *testing.T) {
	db, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	service, object := pipelineFixture(t, db, storagecatalog.FileClassMarkdown, "text/markdown", "small-note-drain-marker", time.Now().UTC())
	service.now = func() time.Time { return time.Now().UTC() }
	run, err := service.EnsurePipelineRun(t.Context(), object, PipelinePolicy{}, false, 100)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{PreferredRunID: run.KnowledgePipelineRunID, WorkerRunID: "small-note-worker", Limit: 20, Now: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err != nil || result.Completed != 6 || result.Failed != 0 {
		t.Fatalf("drain: %+v %v", result, err)
	}
	var startedBeforeCreation, distinctStarts int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE s.started_at<r.created_at),count(DISTINCT s.started_at)
  FROM knowledge.pipeline_stage_runs s JOIN knowledge.pipeline_runs r USING(knowledge_pipeline_run_id)
  WHERE r.knowledge_pipeline_run_id=$1 AND s.started_at IS NOT NULL`, run.KnowledgePipelineRunID).Scan(&startedBeforeCreation, &distinctStarts); err != nil || startedBeforeCreation != 0 || distinctStarts != 6 {
		t.Fatalf("backdated stage clocks: %d %d %v", startedBeforeCreation, distinctStarts, err)
	}
	current, err := service.store.GetPipelineRun(t.Context(), run.KnowledgePipelineRunID)
	if err != nil || current.Status != FilePipelineStatusComplete || len(current.SourceSnapshot) == 0 {
		t.Fatalf("completion lost selected input: %+v %v", current, err)
	}
	if _, ok := result.ElapsedMS["claim.metadata"]; !ok {
		t.Fatal("missing per-stage claim timing")
	}
	if result, err := service.RunPipelineCoordinatorOnce(t.Context(), PipelineCoordinatorRunInput{PreferredRunID: run.KnowledgePipelineRunID, WorkerRunID: "small-note-replay"}); err != nil || result.Claimed != 0 {
		t.Fatalf("completed hint did not fall back to empty durable queue: %+v %v", result, err)
	}
}

func TestSmallNotePipelineFencesPostgres(t *testing.T) {
	_, url := boxSourcesDatabase(t)
	if _, err := migrations.Up(t.Context(), url, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOOM_TEST_DB_URL", url)
	t.Run("expired-claim", TestPipelineClaimLeaseReclamationFencesStaleWorkerPostgres)
	t.Run("supersession", TestPipelineSupersessionClearsClaimsAndFencesOldWorkersPostgres)
	t.Run("source-identity", TestSourceIdentityFencesAllWorkerPublicationPostgres)
	t.Run("heavy-quiet-window", TestQuietWindowGatesOnlyCurrentHeavyStagePostgres)
}
