package knowledge

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"loom.local/loom/internal/workers"
)

func notifyPipelineWorkTx(ctx context.Context, tx *sql.Tx) error {
	if err := workers.NotifyQueueTx(ctx, tx, workers.KindKnowledgeIndexer); err != nil {
		return err
	}
	return workers.NotifyQueueTx(ctx, tx, "knowledge_heavy")
}

// This cheap read has the same waiting-stage/quiet/claim eligibility as the
// queue owner. It acquires no claim and does not advance processing state.
func (s *Service) NextPipelineWorkAt(ctx context.Context, executionClass string, now time.Time) (*time.Time, error) {
	return s.nextPipelineWorkAt(ctx, executionClass, now, false)
}

func (s *Service) NextOrdinaryPipelineWorkAt(ctx context.Context, now time.Time) (*time.Time, error) {
	return s.nextPipelineWorkAt(ctx, PipelineExecutionHeavy, now, true)
}

func (s *Service) nextPipelineWorkAt(ctx context.Context, executionClass string, now time.Time, ordinaryOnly bool) (*time.Time, error) {
	if s == nil || s.store.db == nil || !isPipelineExecutionClass(executionClass) {
		return nil, fmt.Errorf("%w: pipeline readiness is not configured", ErrInvalid)
	}
	var due sql.NullTime
	err := s.store.db.QueryRowContext(ctx, `SELECT min(GREATEST(
		COALESCE(stage.next_attempt_at,$2),
		CASE WHEN run.status='waiting_quiet_window' THEN COALESCE(run.quiet_window_eligible_at,$2) ELSE $2 END,
		CASE WHEN run.claimed_by_worker_run_id<>'' THEN COALESCE(run.claim_expires_at,$2) ELSE $2 END))
		FROM knowledge.pipeline_runs run
		JOIN knowledge.pipeline_stage_runs stage ON stage.knowledge_pipeline_run_id=run.knowledge_pipeline_run_id AND stage.stage_key=run.current_stage_key
		JOIN knowledge.knowledge_objects object ON object.knowledge_object_id=run.knowledge_object_id
		WHERE run.current_execution_class=$1 AND (NOT $3::boolean OR (stage.stage_key='embedding' AND run.plan_snapshot->>'file_family' NOT IN ('pdf','image','metadata_only') AND COALESCE(run.plan_snapshot->'enrichment','null'::jsonb)='null'::jsonb)) AND run.status IN ('queued','waiting_coordinator','waiting_heavy','waiting_quiet_window')
		AND stage.status IN ('ready','failed_retryable') AND `+pipelineClaimSourceSQL("run", "object")+` AND `+notesCustodyWriteAllowedSQL("object"), executionClass, now.UTC(), ordinaryOnly).Scan(&due)
	if err != nil || !due.Valid {
		return nil, err
	}
	return &due.Time, nil
}
