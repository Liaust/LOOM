package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/events"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/requestctx"
)

func (s Service) CancelJob(ctx context.Context, req requestctx.Context, input CancelJobInput) (Job, error) {
	jobRef := strings.TrimSpace(input.JobRef)
	if jobRef == "" {
		return Job{}, fmt.Errorf("job_ref is required")
	}
	resolved, err := s.resolveJob(ctx, jobRef)
	if err != nil {
		return Job{}, err
	}
	if resolved.Status != StatusQueued {
		return Job{}, fmt.Errorf("only queued jobs can be cancelled; job %s is %s", resolved.JobID, resolved.Status)
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      stringValue(resolved.ScopeID),
		ResourceKind: "job_cancel",
		ResourceRef:  resolved.JobID,
	}); err != nil {
		return Job{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	job, err := scanJob(tx.QueryRowContext(ctx, `
		UPDATE jobs.jobs
		SET status = 'cancelled',
		    lease_owner = NULL,
		    lease_expires_at = NULL,
		    last_heartbeat_at = NULL,
		    cancel_requested_at = now(),
		    cancel_requested_by_actor_id = nullif($2, ''),
		    cancelled_at = now(),
		    failure_attention_status = 'archived',
		    failure_attention_updated_at = now(),
		    failure_attention_note = CASE
		        WHEN failure_attention_status = 'active' THEN 'Cancelled before execution.'
		        ELSE failure_attention_note
		    END,
		    updated_at = now()
		WHERE job_id = $1
		  AND status = 'queued'
		RETURNING `+jobReturningColumns()+`
	`, resolved.JobID, req.ActorID))
	if err != nil {
		if err == sql.ErrNoRows {
			return Job{}, fmt.Errorf("job %s is no longer queued", resolved.JobID)
		}
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobCancelled, StatusCancelled, "ok", map[string]any{
		"job_id":       job.JobID,
		"cancelled_by": req.ActorID,
	})); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusCancelled, "cancelled", "Job was cancelled before execution.", realtime.ProgressSeverityWarning, map[string]any{
		"job_id": job.JobID,
	})
	return job, nil
}

func (s Service) RetryJob(ctx context.Context, req requestctx.Context, input RetryJobInput) (Job, error) {
	jobRef := strings.TrimSpace(input.JobRef)
	if jobRef == "" {
		return Job{}, fmt.Errorf("job_ref is required")
	}
	resolved, err := s.resolveJob(ctx, jobRef)
	if err != nil {
		return Job{}, err
	}
	if resolved.Status != StatusFailed && resolved.Status != StatusTimedOut {
		return Job{}, fmt.Errorf("only failed or timed-out jobs can be retried; job %s is %s", resolved.JobID, resolved.Status)
	}
	if !input.Force && resolved.AttemptCount >= resolved.MaxAttempts {
		return Job{}, fmt.Errorf("job %s exhausted attempts (%d/%d); pass force to requeue", resolved.JobID, resolved.AttemptCount, resolved.MaxAttempts)
	}
	if err := projects.EnsureRuntimeActive(ctx, s.DB, projects.RuntimeRef{
		ScopeID:      stringValue(resolved.ScopeID),
		ResourceKind: "job_retry",
		ResourceRef:  resolved.JobID,
	}); err != nil {
		return Job{}, err
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	job, err := scanJob(tx.QueryRowContext(ctx, `
		UPDATE jobs.jobs
		SET status = 'queued',
		    output_json = '{}'::jsonb,
		    progress_json = '{}'::jsonb,
		    artifact_refs_json = '[]'::jsonb,
		    lease_owner = NULL,
		    lease_expires_at = NULL,
		    next_attempt_at = NULL,
		    manual_action_required = false,
		    last_worker_run_id = NULL,
		    last_heartbeat_at = NULL,
		    cancel_requested_at = NULL,
		    cancel_requested_by_actor_id = NULL,
		    exit_code = NULL,
		    failure_code = NULL,
		    failure_message = NULL,
		    failure_attention_status = 'active',
		    failure_attention_updated_at = NULL,
		    failure_attention_updated_by_actor_id = NULL,
		    failure_attention_note = '',
		    queued_at = now(),
		    started_at = NULL,
		    completed_at = NULL,
		    failed_at = NULL,
		    cancelled_at = NULL,
		    updated_at = now()
		WHERE job_id = $1
		  AND status IN ('failed', 'timed_out')
		RETURNING `+jobReturningColumns()+`
	`, resolved.JobID))
	if err != nil {
		if err == sql.ErrNoRows {
			return Job{}, fmt.Errorf("job %s is no longer failed or timed out", resolved.JobID)
		}
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobQueued, StatusQueued, "ok", map[string]any{
		"job_id":           job.JobID,
		"retried":          true,
		"force":            input.Force,
		"previous_status":  resolved.Status,
		"previous_attempt": resolved.AttemptCount,
	})); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	s.recordJobProgress(ctx, req, job, realtime.ProgressStatusPending, "queued", "Failed job was requeued for explicit retry.", realtime.ProgressSeverityNormal, map[string]any{
		"job_id":          job.JobID,
		"previous_status": resolved.Status,
		"force":           input.Force,
	})
	return job, nil
}

func (s Service) AcknowledgeJobAttention(ctx context.Context, req requestctx.Context, input JobAttentionInput) (Job, error) {
	return s.updateJobFailureAttention(ctx, req, input, FailureAttentionStatusAcknowledged, events.TypeJobAttentionAcknowledged)
}

func (s Service) ArchiveJobAttention(ctx context.Context, req requestctx.Context, input JobAttentionInput) (Job, error) {
	return s.updateJobFailureAttention(ctx, req, input, FailureAttentionStatusArchived, events.TypeJobAttentionArchived)
}

func (s Service) updateJobFailureAttention(ctx context.Context, req requestctx.Context, input JobAttentionInput, status string, eventType string) (Job, error) {
	jobRef := strings.TrimSpace(input.JobRef)
	if jobRef == "" {
		return Job{}, fmt.Errorf("job_ref is required")
	}
	status = NormalizeFailureAttentionStatus(status)
	if status == "" {
		return Job{}, fmt.Errorf("failure attention status is invalid")
	}
	note := strings.TrimSpace(input.Note)
	if len(note) > 1000 {
		return Job{}, fmt.Errorf("failure attention note is too long")
	}
	resolved, err := s.resolveJob(ctx, jobRef)
	if err != nil {
		return Job{}, err
	}
	if !JobNeedsFailureAttention(resolved) {
		return Job{}, fmt.Errorf("only failed, timed-out, or manual-action jobs can have failure attention updated; job %s is %s", resolved.JobID, resolved.Status)
	}

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()

	job, err := scanJob(tx.QueryRowContext(ctx, `
		UPDATE jobs.jobs
		SET failure_attention_status = $2,
		    failure_attention_updated_at = now(),
		    failure_attention_updated_by_actor_id = nullif($3, ''),
		    failure_attention_note = $4
		WHERE job_id = $1
		  AND (status IN ('failed', 'timed_out') OR manual_action_required = true)
		RETURNING `+jobReturningColumns()+`
	`, resolved.JobID, status, req.ActorID, note))
	if err != nil {
		if err == sql.ErrNoRows {
			return Job{}, fmt.Errorf("job %s no longer requires failure attention", resolved.JobID)
		}
		return Job{}, err
	}
	if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, eventType, job.Status, "ok", map[string]any{
		"job_id":                   job.JobID,
		"failure_attention_status": status,
		"failure_attention_note":   note,
		"previous_status":          resolved.Status,
	})); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

func (s Service) SweepJobs(ctx context.Context, req requestctx.Context, input SweepInput) (SweepResult, error) {
	input = normalizeSweepInput(input)
	result := SweepResult{
		SchemaVersion: "job_sweeper.result.v0.2",
		ScannedAt:     input.Now,
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SweepResult{}, err
	}
	defer tx.Rollback()

	released, err := tx.ExecContext(ctx, `
		UPDATE jobs.jobs
		SET lease_owner = NULL,
		    lease_expires_at = NULL,
		    last_heartbeat_at = NULL,
		    updated_at = now()
		WHERE job_id IN (
			SELECT job_id
			FROM jobs.jobs
			WHERE status = 'queued'
			  AND lease_expires_at IS NOT NULL
			  AND lease_expires_at < $1
			ORDER BY lease_expires_at ASC
			LIMIT $2
		)
	`, input.Now.Add(-input.QueuedLeaseReleaseAfter), input.BatchSize)
	if err != nil {
		return SweepResult{}, err
	}
	result.QueuedLeasesReleased, _ = released.RowsAffected()

	offline, err := tx.ExecContext(ctx, `
		UPDATE jobs.runners
		SET status = 'offline',
		    current_job_id = NULL,
		    updated_at = now()
		WHERE status <> 'offline'
		  AND last_heartbeat_at IS NOT NULL
		  AND last_heartbeat_at < $1
		  AND (status <> 'running' OR current_job_id IS NULL)
	`, input.Now.Add(-input.RunnerOfflineAfter))
	if err != nil {
		return SweepResult{}, err
	}
	result.RunnersMarkedOffline, _ = offline.RowsAffected()

	timedOut, err := sweepTimedOutJobs(ctx, tx, input)
	if err != nil {
		return SweepResult{}, err
	}
	result.JobsTimedOut = int64(len(timedOut))
	for _, job := range timedOut {
		result.TimedOutJobIDs = append(result.TimedOutJobIDs, job.JobID)
		if _, err := updateRunningAttemptTx(ctx, tx, job.JobID, StatusTimedOut); err != nil {
			return SweepResult{}, err
		}
		if _, err := updateRunnerForTimedOutJobTx(ctx, tx, job.JobID); err != nil {
			return SweepResult{}, err
		}
		if _, err := events.AppendTx(ctx, tx, jobEventInput(req, job, events.TypeJobTimedOut, StatusTimedOut, "error", map[string]any{
			"job_id":       job.JobID,
			"failure_code": "runner_lease_expired",
			"swept":        true,
		})); err != nil {
			return SweepResult{}, err
		}
		runPayload := map[string]any{
			"job_id":       job.JobID,
			"failure_code": "runner_lease_expired",
			"swept":        true,
		}
		runEvent := scriptRunEventInput(req, job, events.TypeScriptRunFailed, StatusTimedOut, "error", runPayload)
		if job.JobType == TypeWorkflowRun {
			runPayload["workflow_id"] = stringValue(job.WorkflowID)
			runPayload["workflow_version_id"] = stringValue(job.WorkflowVersionID)
			runEvent = workflowRunEventInput(req, job, events.TypeWorkflowRunFailed, StatusTimedOut, "error", runPayload)
		} else {
			runPayload["script_id"] = stringValue(job.ScriptID)
			runPayload["script_version_id"] = stringValue(job.ScriptVersionID)
		}
		if _, err := events.AppendTx(ctx, tx, runEvent); err != nil {
			return SweepResult{}, err
		}
	}

	manualJobs, err := sweepAmbiguousJobs(ctx, tx, input)
	if err != nil {
		return SweepResult{}, err
	}
	result.Ambiguous = int64(len(manualJobs))
	result.ManualAction = int64(len(manualJobs))
	for _, job := range manualJobs {
		result.ManualActionJobIDs = append(result.ManualActionJobIDs, job.JobID)
	}

	if err := tx.Commit(); err != nil {
		return SweepResult{}, err
	}
	for _, job := range timedOut {
		s.recordJobProgress(ctx, req, job, realtime.ProgressStatusFailed, "timed_out", "Job was marked timed out by the job sweeper after its runner lease expired.", realtime.ProgressSeverityError, map[string]any{
			"job_id":       job.JobID,
			"failure_code": "runner_lease_expired",
		})
	}
	return result, nil
}

func normalizeSweepInput(input SweepInput) SweepInput {
	if input.RunnerOfflineAfter <= 0 {
		input.RunnerOfflineAfter = 120 * time.Second
	}
	if input.StaleRunningLeaseAfter <= 0 {
		input.StaleRunningLeaseAfter = 300 * time.Second
	}
	if input.QueuedLeaseReleaseAfter <= 0 {
		input.QueuedLeaseReleaseAfter = 120 * time.Second
	}
	if input.BatchSize <= 0 || input.BatchSize > 1000 {
		input.BatchSize = 100
	}
	if input.Now.IsZero() {
		input.Now = time.Now().UTC()
	} else {
		input.Now = input.Now.UTC()
	}
	return input
}

func sweepTimedOutJobs(ctx context.Context, tx *sql.Tx, input SweepInput) ([]Job, error) {
	rows, err := tx.QueryContext(ctx, jobSelectSQL()+`
		LEFT JOIN jobs.runners r ON r.runner_id = j.lease_owner
		WHERE j.job_id IN (
			SELECT j2.job_id
			FROM jobs.jobs j2
			LEFT JOIN jobs.runners r2 ON r2.runner_id = j2.lease_owner
			WHERE j2.status = 'running'
			  AND j2.lease_expires_at IS NOT NULL
			  AND j2.lease_expires_at < $1
			  AND (
			    r2.runner_id IS NULL
			    OR r2.status = 'offline'
			    OR r2.last_heartbeat_at IS NULL
			    OR r2.last_heartbeat_at < $2
			  )
			ORDER BY j2.lease_expires_at ASC
			LIMIT $3
		)
		FOR UPDATE OF j SKIP LOCKED
	`, input.Now.Add(-input.StaleRunningLeaseAfter), input.Now.Add(-input.RunnerOfflineAfter), input.BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selected := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		selected = append(selected, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	updated := []Job{}
	for _, job := range selected {
		out, err := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs.jobs
			SET status = 'timed_out',
			    lease_owner = NULL,
			    lease_expires_at = NULL,
			    last_heartbeat_at = NULL,
			    failure_code = COALESCE(failure_code, 'runner_lease_expired'),
			    failure_message = COALESCE(failure_message, 'Job runner lease expired and the runner is stale or offline.'),
			    failed_at = COALESCE(failed_at, now()),
			    updated_at = now()
			WHERE job_id = $1
			  AND status = 'running'
			RETURNING `+jobReturningColumns()+`
		`, job.JobID))
		if err != nil {
			return nil, err
		}
		updated = append(updated, out)
	}
	return updated, nil
}

func sweepAmbiguousJobs(ctx context.Context, tx *sql.Tx, input SweepInput) ([]Job, error) {
	rows, err := tx.QueryContext(ctx, jobSelectSQL()+`
		LEFT JOIN jobs.runners r ON r.runner_id = j.lease_owner
		WHERE j.job_id IN (
			SELECT j2.job_id
			FROM jobs.jobs j2
			LEFT JOIN jobs.runners r2 ON r2.runner_id = j2.lease_owner
			WHERE j2.status = 'running'
			  AND j2.lease_expires_at IS NOT NULL
			  AND j2.lease_expires_at < $1
			  AND j2.manual_action_required = false
			  AND r2.runner_id IS NOT NULL
			  AND r2.status <> 'offline'
			  AND r2.last_heartbeat_at IS NOT NULL
			  AND r2.last_heartbeat_at >= $2
			ORDER BY j2.lease_expires_at ASC
			LIMIT $3
		)
		FOR UPDATE OF j SKIP LOCKED
	`, input.Now, input.Now.Add(-input.RunnerOfflineAfter), input.BatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selected := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		selected = append(selected, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	updated := []Job{}
	for _, job := range selected {
		out, err := scanJob(tx.QueryRowContext(ctx, `
			UPDATE jobs.jobs
			SET manual_action_required = true,
			    failure_code = COALESCE(failure_code, 'ambiguous_expired_lease'),
			    failure_message = COALESCE(failure_message, 'Job lease expired but runner state was not stale enough for automatic timeout.'),
			    updated_at = now()
			WHERE job_id = $1
			  AND status = 'running'
			  AND manual_action_required = false
			RETURNING `+jobReturningColumns()+`
		`, job.JobID))
		if err != nil {
			return nil, err
		}
		updated = append(updated, out)
	}
	return updated, nil
}

func updateRunningAttemptTx(ctx context.Context, tx *sql.Tx, jobID, status string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE jobs.job_attempts
		SET status = $2,
		    failed_at = CASE WHEN $2 IN ('failed', 'timed_out', 'cancelled') THEN COALESCE(failed_at, now()) ELSE failed_at END,
		    completed_at = CASE WHEN $2 = 'completed' THEN COALESCE(completed_at, now()) ELSE completed_at END
		WHERE job_id = $1
		  AND status = 'running'
	`, jobID, status)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func updateRunnerForTimedOutJobTx(ctx context.Context, tx *sql.Tx, jobID string) (int64, error) {
	result, err := tx.ExecContext(ctx, `
		UPDATE jobs.runners
		SET status = CASE WHEN status = 'running' THEN 'offline' ELSE status END,
		    current_job_id = NULL,
		    updated_at = now()
		WHERE current_job_id = $1
	`, jobID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
