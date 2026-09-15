package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/storagecatalog"
)

const defaultPipelineClaimLease = 2 * time.Minute

type PipelineClaimOptions struct {
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type PipelineWorkItem struct {
	Run    PipelineRun      `json:"run"`
	Stage  PipelineStageRun `json:"stage"`
	Object KnowledgeObject  `json:"object"`
}

// lockPipelineClaimTx locks and verifies both authoritative claim rows. Every
// worker-side mutation must pass this fence before changing pipeline state.
func lockPipelineClaimTx(ctx context.Context, tx *sql.Tx, item PipelineWorkItem) error {
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return err
	}
	var runGeneration, runClaimGeneration, stageClaimGeneration int64
	var runWorker, runStatus, currentStage, stageWorker, stageStatus string
	var objectID, runSourceHash, runSourceRevision string
	err := tx.QueryRowContext(ctx, `SELECT r.generation,r.claim_generation,r.claimed_by_worker_run_id,r.status,
		r.current_stage_key,s.claim_generation,s.claimed_by_worker_run_id,s.status,
		r.knowledge_object_id,r.source_hash,r.source_revision
		FROM knowledge.pipeline_runs r
		JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
		WHERE r.knowledge_pipeline_run_id=$1 AND s.knowledge_pipeline_stage_run_id=$2
		FOR UPDATE OF r,s`, item.Run.KnowledgePipelineRunID, item.Stage.KnowledgePipelineStageRunID).Scan(
		&runGeneration, &runClaimGeneration, &runWorker, &runStatus, &currentStage,
		&stageClaimGeneration, &stageWorker, &stageStatus,
		&objectID, &runSourceHash, &runSourceRevision)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: pipeline claim rows no longer exist", ErrConflict)
		}
		return err
	}
	worker := item.Run.ClaimedByWorkerRunID
	if worker == "" || item.Stage.ClaimedByWorkerRunID != worker ||
		item.Stage.ClaimGeneration != item.Run.ClaimGeneration {
		return fmt.Errorf("%w: pipeline work item claim ownership is stale", ErrConflict)
	}
	if runGeneration != item.Run.Generation || runClaimGeneration != item.Run.ClaimGeneration ||
		stageClaimGeneration != item.Stage.ClaimGeneration || runWorker != worker || stageWorker != worker ||
		runStatus != FilePipelineStatusProcessing || stageStatus != PipelineStageStatusProcessing ||
		currentStage != item.Stage.StageKey || objectID != item.Run.KnowledgeObjectID ||
		item.Object.KnowledgeObjectID != objectID || item.Run.SourceHash != runSourceHash ||
		item.Run.SourceRevision != runSourceRevision {
		return fmt.Errorf("%w: pipeline claim fence is stale", ErrConflict)
	}
	// Object identity is fenced after the authoritative run and stage rows. This
	// closes the interval where ingestion has advanced the source but trajectory
	// reconciliation has not yet superseded the claimed run.
	var currentSourceHash, currentSourceRevision string
	err = tx.QueryRowContext(ctx, `SELECT source_hash,source_revision
		FROM knowledge.knowledge_objects
		WHERE knowledge_object_id=$1 AND deleted_at IS NULL FOR UPDATE`, objectID).Scan(&currentSourceHash, &currentSourceRevision)
	if err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: pipeline source object no longer exists", ErrConflict)
		}
		return err
	}
	if currentSourceHash != runSourceHash || currentSourceRevision != runSourceRevision ||
		item.Object.SourceHash != runSourceHash || item.Object.SourceRevision != runSourceRevision {
		return fmt.Errorf("%w: pipeline source identity is stale", ErrConflict)
	}
	var visible, writable bool
	if err := tx.QueryRowContext(ctx, `SELECT (`+visibleNotesKnowledgeObjectSQL("o")+`),`+notesCustodyWriteAllowedSQL("o")+` FROM knowledge.knowledge_objects o WHERE o.knowledge_object_id=$1`, objectID).Scan(&visible, &writable); err != nil {
		return err
	}
	if !writable {
		return ErrNotesCustodyPaused
	}
	if !visible {
		return fmt.Errorf("%w: pipeline source admission is stale", ErrConflict)
	}
	return nil
}

func requireOneRow(result sql.Result, message string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s", ErrConflict, message)
	}
	return nil
}

func (s *Service) ReleaseExpiredPipelineClaims(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = s.currentTime()
	}
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Keep expiry reclamation on the canonical pipeline mutation order:
	// run -> stage/unit -> object -> version/artifact. In particular, never lock
	// units before their run because policy supersession owns the run first.
	lockQueries := []string{
		`SELECT knowledge_pipeline_run_id FROM knowledge.pipeline_runs
		 WHERE status='processing' AND claimed_by_worker_run_id<>'' AND claim_expires_at<=$1
		 ORDER BY knowledge_pipeline_run_id FOR UPDATE`,
		`SELECT s.knowledge_pipeline_stage_run_id FROM knowledge.pipeline_stage_runs s
		 JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
		 WHERE r.status='processing' AND r.claimed_by_worker_run_id<>'' AND r.claim_expires_at<=$1
		 ORDER BY s.knowledge_pipeline_stage_run_id FOR UPDATE OF s`,
		`SELECT u.knowledge_pipeline_stage_unit_id FROM knowledge.pipeline_stage_units u
		 JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_stage_run_id=u.knowledge_pipeline_stage_run_id
		 JOIN knowledge.pipeline_runs r ON r.knowledge_pipeline_run_id=s.knowledge_pipeline_run_id
		 WHERE r.status='processing' AND r.claimed_by_worker_run_id<>'' AND r.claim_expires_at<=$1
		 ORDER BY u.knowledge_pipeline_stage_unit_id FOR UPDATE OF u`,
	}
	for _, query := range lockQueries {
		rows, lockErr := tx.QueryContext(ctx, query, now)
		if lockErr != nil {
			return 0, lockErr
		}
		for rows.Next() {
			var ignored string
			if lockErr = rows.Scan(&ignored); lockErr != nil {
				rows.Close()
				return 0, lockErr
			}
		}
		if lockErr = rows.Err(); lockErr != nil {
			rows.Close()
			return 0, lockErr
		}
		if lockErr = rows.Close(); lockErr != nil {
			return 0, lockErr
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_units u
		SET status='ready',claimed_by_worker_run_id='',claim_generation=0,updated_at=$1
		FROM knowledge.pipeline_stage_runs s,knowledge.pipeline_runs r
		WHERE u.knowledge_pipeline_stage_run_id=s.knowledge_pipeline_stage_run_id
		  AND s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
		  AND u.status='processing' AND r.status='processing'
		  AND r.claimed_by_worker_run_id<>'' AND r.claim_expires_at<=$1
		  AND u.claimed_by_worker_run_id=r.claimed_by_worker_run_id
		  AND u.claim_generation=r.claim_generation`, now); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs
		SET status=CASE WHEN current_execution_class='heavy' THEN 'waiting_heavy' ELSE 'waiting_coordinator' END,
		    claimed_by_worker_run_id='', claim_expires_at=NULL, updated_at=$1
		WHERE status='processing' AND claimed_by_worker_run_id<>'' AND claim_expires_at<=$1`, now)
	if err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs s
		SET status='ready', claimed_by_worker_run_id='', updated_at=$1
		FROM knowledge.pipeline_runs r
		WHERE s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id
		  AND s.stage_key=r.current_stage_key AND s.status='processing'
		  AND r.claimed_by_worker_run_id=''`, now); err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Service) ClaimPipelineRuns(ctx context.Context, executionClass, workerRunID string, options PipelineClaimOptions) ([]PipelineWorkItem, error) {
	if s == nil || s.store.db == nil {
		return nil, fmt.Errorf("knowledge store is not configured")
	}
	if !isPipelineExecutionClass(executionClass) {
		return nil, fmt.Errorf("%w: execution class %q is invalid", ErrInvalid, executionClass)
	}
	workerRunID = strings.TrimSpace(workerRunID)
	if workerRunID == "" {
		return nil, fmt.Errorf("%w: worker_run_id is required", ErrInvalid)
	}
	if options.Limit <= 0 || options.Limit > maxKnowledgeClaimLimit {
		options.Limit = defaultKnowledgeClaimLimit
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = defaultPipelineClaimLease
	}
	now := options.Now
	if now.IsZero() {
		now = s.currentTime()
	}
	expires := now.Add(options.LeaseDuration)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := storagecatalog.LockWorkspaceCustodyReaderTx(ctx, tx); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH candidates AS (
		SELECT r.knowledge_pipeline_run_id
		FROM knowledge.pipeline_runs r
		JOIN knowledge.pipeline_stage_runs s ON s.knowledge_pipeline_run_id=r.knowledge_pipeline_run_id AND s.stage_key=r.current_stage_key
		JOIN knowledge.knowledge_objects o ON o.knowledge_object_id=r.knowledge_object_id
		WHERE r.current_execution_class=$1
		  AND `+notesCustodyWriteAllowedSQL("o")+`
		  AND r.status IN ('queued','waiting_coordinator','waiting_heavy','waiting_quiet_window')
		  AND s.status IN ('ready','failed_retryable')
		  AND (s.next_attempt_at IS NULL OR s.next_attempt_at<=$2)
		  AND (r.status<>'waiting_quiet_window' OR r.quiet_window_eligible_at IS NULL OR r.quiet_window_eligible_at<=$2)
		  AND (r.claimed_by_worker_run_id='' OR r.claim_expires_at IS NULL OR r.claim_expires_at<=$2)
		ORDER BY r.priority, COALESCE(s.next_attempt_at,r.quiet_window_eligible_at,r.created_at), r.created_at
		LIMIT $3 FOR UPDATE OF r SKIP LOCKED
	) UPDATE knowledge.pipeline_runs r
	SET status='processing', claimed_by_worker_run_id=$4, claim_generation=r.claim_generation+1,
	    claim_expires_at=$5, started_at=COALESCE(started_at,$2), updated_at=$2
	FROM candidates WHERE r.knowledge_pipeline_run_id=candidates.knowledge_pipeline_run_id
	RETURNING `+pipelineRunColumnsQualified("r"), executionClass, now, options.Limit, workerRunID, expires)
	if err != nil {
		return nil, err
	}
	var runs []PipelineRun
	for rows.Next() {
		run, err := scanPipelineRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, run := range runs {
		result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status='processing',
			claimed_by_worker_run_id=$3, claim_generation=$4, attempt_count=attempt_count+1,
			started_at=COALESCE(started_at,$2), updated_at=$2
			WHERE knowledge_pipeline_run_id=$1 AND stage_key=$5 AND status IN ('ready','failed_retryable')`,
			run.KnowledgePipelineRunID, now, workerRunID, run.ClaimGeneration, run.CurrentStageKey)
		if err != nil {
			return nil, err
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			if affectedErr != nil {
				return nil, affectedErr
			}
			return nil, fmt.Errorf("%w: pipeline stage was not claimable", ErrConflict)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	items := make([]PipelineWorkItem, 0, len(runs))
	for _, run := range runs {
		stage, err := s.store.getCurrentPipelineStage(ctx, run.KnowledgePipelineRunID, run.CurrentStageKey)
		if err != nil {
			return nil, err
		}
		object, err := s.store.GetKnowledgeObject(ctx, run.KnowledgeObjectID)
		if err != nil {
			return nil, err
		}
		items = append(items, PipelineWorkItem{Run: run, Stage: stage, Object: object})
	}
	return items, nil
}

func (s Store) getCurrentPipelineStage(ctx context.Context, runID, stageKey string) (PipelineStageRun, error) {
	return scanPipelineStageRun(s.db.QueryRowContext(ctx, `SELECT `+pipelineStageRunColumns()+`
		FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2`, runID, stageKey))
}

func (s *Service) CompletePipelineStage(ctx context.Context, item PipelineWorkItem, warnings []string) (PipelineRun, error) {
	now := s.currentTime()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return PipelineRun{}, err
	}
	defer tx.Rollback()
	if err = lockPipelineClaimTx(ctx, tx, item); err != nil {
		return PipelineRun{}, err
	}
	stageStatus := PipelineStageStatusComplete
	if len(warnings) > 0 {
		stageStatus = PipelineStageStatusCompleteWithWarning
	}
	warningJSON := jsonArray(warnings)
	result, err := tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs
		SET status=$3, warning_json=$4, claimed_by_worker_run_id='', completed_at=$5, updated_at=$5
		WHERE knowledge_pipeline_run_id=$1 AND stage_key=$2 AND claim_generation=$6
		  AND claimed_by_worker_run_id=$7 AND status='processing'`, item.Run.KnowledgePipelineRunID,
		item.Stage.StageKey, stageStatus, warningJSON, now, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
	if err != nil {
		return PipelineRun{}, err
	}
	if err = requireOneRow(result, "pipeline stage completion fence is stale"); err != nil {
		return PipelineRun{}, err
	}
	next, found, err := nextPendingPipelineStageTx(ctx, tx, item.Run.KnowledgePipelineRunID, item.Stage.Ordinal)
	if err != nil {
		return PipelineRun{}, err
	}
	if !found {
		totalWarnings := item.Run.WarningCount + len(warnings)
		result, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET status=$2, current_stage_key='',
			current_execution_class='', claimed_by_worker_run_id='', claim_expires_at=NULL,
			warning_count=warning_count+$3, completed_at=$4, updated_at=$4 WHERE knowledge_pipeline_run_id=$1
			  AND generation=$5 AND claim_generation=$6 AND claimed_by_worker_run_id=$7 AND status='processing'`,
			item.Run.KnowledgePipelineRunID, finalPipelineStatus(totalWarnings), len(warnings), now,
			item.Run.Generation, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
		if err != nil {
			return PipelineRun{}, err
		}
		if err = requireOneRow(result, "pipeline run completion fence is stale"); err != nil {
			return PipelineRun{}, err
		}
	} else {
		if next.Status == PipelineStageStatusWaitingDependency {
			result, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_stage_runs SET status='ready', updated_at=$3
				WHERE knowledge_pipeline_stage_run_id=$1 AND status=$2`, next.KnowledgePipelineStageRunID, next.Status, now)
			if err != nil {
				return PipelineRun{}, err
			}
			if err = requireOneRow(result, "next pipeline stage is not waiting on its dependency"); err != nil {
				return PipelineRun{}, err
			}
			next.Status = PipelineStageStatusReady
		}
		quiet := pipelineStageRequiresQuietWindow(next.Metadata)
		status := waitingPipelineStatus(next.ExecutionClass, quiet && item.Run.QuietWindowEligibleAt != nil && item.Run.QuietWindowEligibleAt.After(now))
		result, err = tx.ExecContext(ctx, `UPDATE knowledge.pipeline_runs SET status=$2, current_stage_key=$3,
			current_execution_class=$4, claimed_by_worker_run_id='', claim_expires_at=NULL,
			warning_count=warning_count+$5, updated_at=$6 WHERE knowledge_pipeline_run_id=$1
			  AND generation=$7 AND claim_generation=$8 AND claimed_by_worker_run_id=$9 AND status='processing'`,
			item.Run.KnowledgePipelineRunID, status, next.StageKey, next.ExecutionClass, len(warnings), now,
			item.Run.Generation, item.Run.ClaimGeneration, item.Run.ClaimedByWorkerRunID)
		if err != nil {
			return PipelineRun{}, err
		}
		if err = requireOneRow(result, "pipeline run completion fence is stale"); err != nil {
			return PipelineRun{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return PipelineRun{}, err
	}
	return s.store.GetPipelineRun(ctx, item.Run.KnowledgePipelineRunID)
}

func nextPendingPipelineStageTx(ctx context.Context, tx *sql.Tx, runID string, ordinal int) (PipelineStageRun, bool, error) {
	for {
		stage, err := scanPipelineStageRun(tx.QueryRowContext(ctx, `SELECT `+pipelineStageRunColumns()+`
			FROM knowledge.pipeline_stage_runs WHERE knowledge_pipeline_run_id=$1 AND ordinal>$2
			  AND status NOT IN ('complete','complete_with_warnings','skipped_not_applicable','skipped_by_policy','stale','cancelled')
			ORDER BY ordinal LIMIT 1`, runID, ordinal))
		if err == sql.ErrNoRows {
			return PipelineStageRun{}, false, nil
		}
		if err != nil {
			return PipelineStageRun{}, false, err
		}
		return stage, true, nil
	}
}

func finalPipelineStatus(warnings int) string {
	if warnings > 0 {
		return FilePipelineStatusCompleteWithWarning
	}
	return FilePipelineStatusComplete
}

func pipelineStageRequiresQuietWindow(raw []byte) bool {
	var value struct {
		QuietWindow bool `json:"quiet_window"`
	}
	_ = json.Unmarshal(raw, &value)
	return value.QuietWindow
}

func jsonArray(values []string) []byte {
	if values == nil {
		return []byte(`[]`)
	}
	payload, _ := json.Marshal(values)
	if len(payload) == 0 {
		return []byte(`[]`)
	}
	return payload
}
