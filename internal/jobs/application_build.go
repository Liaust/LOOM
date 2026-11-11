package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/workers"
)

const TypeApplicationBuild = "application_build"
const applicationRunnerKey = "main-application-builder"

func (s Service) ApplicationBuild(ctx context.Context, key string) (Job, error) {
	args := []any{key}
	predicate := jobBoxPredicate(ctx, &args)
	return scanJob(s.DB.QueryRowContext(ctx, jobSelectSQL()+` WHERE j.job_type='application_build' AND j.target_id=$1 AND `+predicate, args...))
}

func (s Service) EnqueueApplicationBuild(ctx context.Context, req requestctx.Context, node, project, key string, input json.RawMessage) (Job, error) {
	if node == "" || project == "" || key == "" || len(input) > 2*1024*1024 || !json.Valid(input) {
		return Job{}, fmt.Errorf("invalid application build input")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	// The project operation holds the project lock; this key also survives reconnects.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
		return Job{}, err
	}
	existing, err := scanJob(tx.QueryRowContext(ctx, jobSelectSQL()+` WHERE j.job_type='application_build' AND j.target_id=$1`, key))
	if err == nil {
		args := []any{existing.JobID}
		var allowed bool
		if err := tx.QueryRowContext(ctx, `SELECT `+jobBoxPredicate(ctx, &args)+` FROM jobs.jobs j WHERE j.job_id=$1`, args...).Scan(&allowed); err != nil {
			return Job{}, err
		}
		if !allowed {
			return Job{}, sql.ErrNoRows
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, err
	}
	var scope string
	if err := tx.QueryRowContext(ctx, `SELECT project_scope_id FROM projects.projects WHERE project_id=$1 AND status<>'archived'`, project).Scan(&scope); err != nil {
		return Job{}, err
	}
	job, err := insertJobTx(ctx, tx, ids.NewJobID(), req, TypeApplicationBuild, StatusCreated, scope, "application_build", key, "", "", "", "", "", "", 1800, input, "", "", nil)
	if err != nil {
		return Job{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs.jobs SET execution_node_id=$2 WHERE job_id=$1`, job.JobID, node); err != nil {
		return Job{}, err
	}
	job, err = updateJobStatusTx(ctx, tx, job.JobID, StatusQueued, nil, "", "")
	if err != nil {
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobQueued, StatusQueued, "ok", map[string]any{"job_id": job.JobID, "job_type": TypeApplicationBuild})); err != nil {
		return Job{}, err
	}
	if err := workers.NotifyQueueTx(ctx, tx, workers.KindApplicationBuilder); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s Service) NextApplicationBuildAt(ctx context.Context, node string, now time.Time) (*time.Time, error) {
	// A live lease wins over queued work. An ambiguous compiler blocks admission
	// until reviewed; do not generate no-work runs behind it every second.
	var active sql.NullTime
	var held bool
	err := s.DB.QueryRowContext(ctx, `SELECT min(COALESCE(lease_expires_at,$2)),COALESCE(bool_or(manual_action_required),false)
	 FROM jobs.jobs WHERE job_type='application_build' AND execution_node_id=$1
	 AND (status='running' OR (lease_owner IS NOT NULL AND lease_expires_at>$2))`, node, now.UTC()).Scan(&active, &held)
	if err != nil || held {
		return nil, err
	}
	if active.Valid {
		return &active.Time, nil
	}
	var due sql.NullTime
	err = s.DB.QueryRowContext(ctx, `SELECT min(GREATEST($2,COALESCE(next_attempt_at,$2),COALESCE(lease_expires_at,$2)))
	 FROM jobs.jobs WHERE job_type='application_build' AND execution_node_id=$1 AND NOT manual_action_required
	 AND (status='queued' OR (status IN ('completed','failed','cancelled') AND COALESCE(progress_json->>'parent_done','false')<>'true'))`, node, now.UTC()).Scan(&due)
	if err != nil || !due.Valid {
		return nil, err
	}
	return &due.Time, nil
}

// The fixed typed runner uses existing leases/attempts, never the script slot.
func (s Service) ClaimApplicationBuild(ctx context.Context, req requestctx.Context, workerRunID string) (JobClaim, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return JobClaim{}, err
	}
	defer tx.Rollback()
	// A lost compiler owner is ambiguous: the Nix daemon may still be building.
	if _, err := tx.ExecContext(ctx, `UPDATE jobs.jobs SET manual_action_required=true,failure_code='application_build_owner_lost',failure_attention_status='active',updated_at=now()
	 WHERE job_type='application_build' AND execution_node_id=$1 AND status='running' AND COALESCE(lease_expires_at,'epoch'::timestamptz)<now() AND NOT manual_action_required`, req.OriginNodeID); err != nil {
		return JobClaim{}, err
	}
	runner, err := scanRunner(tx.QueryRowContext(ctx, `INSERT INTO jobs.runners(runner_id,runner_key,node_id,runner_type,supported_job_types,supported_runtimes,status,last_heartbeat_at)
	 VALUES($1,$2,$3,'local','["application_build"]','["native"]','idle',now()) ON CONFLICT(runner_key) DO UPDATE SET last_heartbeat_at=now()
	 RETURNING `+runnerReturningColumns(), ids.NewRunnerID(), applicationRunnerKey, req.OriginNodeID))
	if err != nil {
		return JobClaim{}, err
	}
	job, err := scanJob(tx.QueryRowContext(ctx, jobSelectSQL()+` WHERE j.job_type='application_build' AND j.execution_node_id=$1 AND NOT j.manual_action_required
	 AND NOT EXISTS (SELECT 1 FROM jobs.jobs active WHERE active.job_type='application_build' AND active.execution_node_id=$1 AND (active.status='running' OR (active.lease_owner IS NOT NULL AND active.lease_expires_at>now())))
	 AND (j.status IN ('failed','cancelled','completed') OR j.cancel_requested_at IS NULL) AND (j.lease_expires_at IS NULL OR j.lease_expires_at<now()) AND (j.next_attempt_at IS NULL OR j.next_attempt_at<=now())
	 AND (j.status='queued' OR (j.status IN ('completed','failed','cancelled') AND COALESCE(j.progress_json->>'parent_done','false')<>'true'))
	 ORDER BY j.created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, req.OriginNodeID))
	if errors.Is(err, sql.ErrNoRows) {
		if e := tx.Commit(); e != nil {
			return JobClaim{}, e
		}
		return JobClaim{}, ErrNoQueuedJob
	}
	if err != nil {
		return JobClaim{}, err
	}
	claim := JobClaim{Runner: runner, Job: job}
	if job.Status == StatusQueued {
		attempt := job.AttemptCount + 1
		dir := s.attemptWorkdir(job.JobID, attempt)
		claim.Job, err = claimJobWithOptionsTx(ctx, tx, job.JobID, runner.RunnerID, dir, attempt, 2*time.Minute, workerRunID)
		if err != nil {
			return JobClaim{}, err
		}
		claim.Attempt, err = insertAttemptTx(ctx, tx, ids.NewJobAttemptID(), job.JobID, attempt, runner.RunnerID, dir)
		if err != nil {
			return JobClaim{}, err
		}
		if _, err := events.AppendTx(ctx, tx, jobEventInput(req, claim.Job, events.TypeJobStarted, StatusRunning, "ok", map[string]any{"job_id": job.JobID})); err != nil {
			return JobClaim{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE jobs.jobs SET lease_owner=$2,lease_expires_at=now()+interval '2 minutes',last_worker_run_id=$3 WHERE job_id=$1`, job.JobID, runner.RunnerID, workerRunID); err != nil {
			return JobClaim{}, err
		}
	}
	if _, err := updateRunnerHeartbeatTx(ctx, tx, runner.RunnerID, RunnerStatusRunning, "", job.JobID); err != nil {
		return JobClaim{}, err
	}
	if err := tx.Commit(); err != nil {
		return JobClaim{}, err
	}
	return claim, nil
}

func (s Service) FinishApplicationBuild(ctx context.Context, req requestctx.Context, claim JobClaim, failure error, buildStarted bool) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var status string
	var manual bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(lease_owner,''),status,manual_action_required FROM jobs.jobs WHERE job_id=$1 FOR UPDATE`, claim.Job.JobID).Scan(&owner, &status, &manual); err != nil {
		return err
	}
	if owner != claim.Runner.RunnerID || status != StatusRunning || manual {
		return fmt.Errorf("application build lease lost")
	}
	newStatus, code, message := StatusCompleted, "", ""
	event := events.TypeJobCompleted
	if failure != nil {
		newStatus, code, message, event = StatusFailed, "application_build_failed", failure.Error(), events.TypeJobFailed
		if !buildStarted {
			code = "application_build_prerequisite_failed"
		}
		if len(message) > 2048 {
			message = message[:2048]
		}
	}
	job, err := updateJobStatusTx(ctx, tx, claim.Job.JobID, newStatus, nil, code, message)
	if err != nil {
		return err
	}
	phase := "prerequisites"
	if buildStarted {
		phase = "build"
	}
	output, _ := json.Marshal(map[string]any{"schema_version": "application_build.result.v1", "phase": phase, "build_phase_started": buildStarted, "status": newStatus})
	if _, err := tx.ExecContext(ctx, `UPDATE jobs.jobs SET output_json=$2 WHERE job_id=$1`, claim.Job.JobID, output); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE jobs.job_attempts SET status=$2,completed_at=CASE WHEN $2='completed' THEN now() ELSE NULL END,failed_at=CASE WHEN $2='failed' THEN now() ELSE NULL END WHERE job_attempt_id=$1`, claim.Attempt.JobAttemptID, newStatus); err != nil {
		return err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, event, newStatus, "ok", map[string]any{"job_id": job.JobID})); err != nil {
		return err
	}
	if err := workers.NotifyQueueTx(ctx, tx, workers.KindApplicationBuilder); err != nil {
		return err
	}
	return tx.Commit()
}

func (s Service) HoldApplicationBuild(ctx context.Context, claim JobClaim) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE jobs.jobs SET manual_action_required=true,failure_code='application_build_outcome_uncertain',failure_attention_status='active',updated_at=now()
	 WHERE job_id=$1 AND lease_owner=$2 AND status='running'`, claim.Job.JobID, claim.Runner.RunnerID)
	return err
}

func (s Service) FinishApplicationParent(ctx context.Context, claim JobClaim, done bool, failure error) error {
	progress := map[string]any{"parent_done": done}
	if failure != nil {
		progress["parent_error"] = failure.Error()
		done = true
		progress["parent_done"] = true
	}
	raw, _ := json.Marshal(progress)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE jobs.jobs SET progress_json=$3,manual_action_required=$4,lease_owner=NULL,lease_expires_at=NULL,
	 next_attempt_at=CASE WHEN $5 THEN NULL ELSE now()+interval '1 second' END,updated_at=now() WHERE job_id=$1 AND lease_owner=$2`, claim.Job.JobID, claim.Runner.RunnerID, raw, failure != nil, done)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("application parent lease lost")
	}
	if _, err := updateRunnerHeartbeatTx(ctx, tx, claim.Runner.RunnerID, RunnerStatusIdle, "", ""); err != nil {
		return err
	}
	if err := workers.NotifyQueueTx(ctx, tx, workers.KindApplicationBuilder); err != nil {
		return err
	}
	return tx.Commit()
}
