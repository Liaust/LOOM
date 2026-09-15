package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/artifacts"
)

func (s Service) ListQueuedJobs(ctx context.Context, filter ListFilter) ([]Job, error) {
	filter.Status = StatusQueued
	return s.listJobsWithFilter(ctx, filter, true)
}

func (s Service) ListFailedJobs(ctx context.Context, filter ListFilter) ([]Job, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	attentionStatuses, err := jobAttentionStatusesFromFilter(filter)
	if err != nil {
		return nil, err
	}
	query := jobSelectSQL() + ` WHERE (j.status IN ('failed', 'timed_out') OR j.manual_action_required = true)`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	addIn := func(column string, values []string) {
		if len(values) == 0 {
			return
		}
		placeholders := make([]string, 0, len(values))
		for _, value := range values {
			args = append(args, value)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		query += fmt.Sprintf(" AND %s IN (%s)", column, strings.Join(placeholders, ", "))
	}
	addIn("j.failure_attention_status", attentionStatuses)
	if strings.TrimSpace(filter.ScriptRef) != "" {
		script, err := s.Scripts.GetScript(ctx, filter.ScriptRef)
		if err != nil {
			return nil, err
		}
		add("j.script_id =", script.Script.ScriptID)
	}
	if strings.TrimSpace(filter.WorkflowRef) != "" {
		workflow, err := s.Workflows.GetWorkflow(ctx, filter.WorkflowRef)
		if err != nil {
			return nil, err
		}
		add("j.workflow_id =", workflow.Workflow.WorkflowID)
	}
	if strings.TrimSpace(filter.JobType) != "" {
		add("j.job_type =", strings.TrimSpace(filter.JobType))
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("j.scope_id =", scopeID)
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY COALESCE(j.failed_at, j.updated_at) DESC, j.updated_at DESC LIMIT $%d", len(args))
	return s.queryJobs(ctx, query, args...)
}

func (s Service) JobQueueSummary(ctx context.Context) (QueueSummary, error) {
	now := time.Now().UTC()
	summary := QueueSummary{GeneratedAt: now}
	var oldestQueued sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'queued')::int,
			COUNT(*) FILTER (WHERE status = 'running')::int,
			COUNT(*) FILTER (WHERE status = 'failed' AND failure_attention_status = 'active')::int,
			COUNT(*) FILTER (WHERE status = 'timed_out' AND failure_attention_status = 'active')::int,
			COUNT(*) FILTER (WHERE status = 'cancelled')::int,
			COUNT(*) FILTER (WHERE manual_action_required = true AND failure_attention_status = 'active')::int,
			MIN(queued_at) FILTER (WHERE status = 'queued')
		FROM jobs.jobs
	`).Scan(
		&summary.QueuedCount,
		&summary.RunningCount,
		&summary.FailedCount,
		&summary.TimedOutCount,
		&summary.CancelledCount,
		&summary.ManualActionCount,
		&oldestQueued,
	)
	if err != nil {
		return QueueSummary{}, err
	}
	if oldestQueued.Valid {
		t := oldestQueued.Time
		summary.OldestQueuedAt = &t
		age := int64(now.Sub(t).Seconds())
		if age < 0 {
			age = 0
		}
		summary.OldestQueuedAgeSeconds = &age
	}
	err = s.DB.QueryRowContext(ctx, `
		SELECT
			COUNT(*)::int,
			COUNT(*) FILTER (WHERE status = 'idle')::int,
			COUNT(*) FILTER (WHERE status = 'running')::int,
			COUNT(*) FILTER (WHERE status = 'draining')::int,
			COUNT(*) FILTER (WHERE status = 'offline')::int,
			COUNT(*) FILTER (WHERE status = 'failed')::int,
			COUNT(*) FILTER (WHERE current_job_id IS NOT NULL)::int
		FROM jobs.runners
	`).Scan(
		&summary.RunnerCount,
		&summary.IdleRunnerCount,
		&summary.RunningRunnerCount,
		&summary.DrainingRunnerCount,
		&summary.OfflineRunnerCount,
		&summary.FailedRunnerCount,
		&summary.CurrentJobCount,
	)
	if err != nil {
		return QueueSummary{}, err
	}
	return summary, nil
}

func (s Service) ListRunners(ctx context.Context, filter RunnerFilter) ([]Runner, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := runnerSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("r.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.NodeID) != "" {
		add("r.node_id =", strings.TrimSpace(filter.NodeID))
	}
	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY r.updated_at DESC, r.runner_key LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	runners := []Runner{}
	for rows.Next() {
		runner, err := scanRunner(rows)
		if err != nil {
			return nil, err
		}
		runners = append(runners, runner)
	}
	return runners, rows.Err()
}

func (s Service) GetRunner(ctx context.Context, ref string) (Runner, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Runner{}, fmt.Errorf("runner ref is required")
	}
	row := s.DB.QueryRowContext(ctx, runnerSelectSQL()+`
		WHERE r.runner_id = $1 OR r.runner_key = $1
		LIMIT 1
	`, ref)
	return scanRunner(row)
}

func (s Service) ListJobOutputs(ctx context.Context, jobRef string) ([]JobOutput, error) {
	job, err := s.resolveJob(ctx, jobRef)
	if err != nil {
		return nil, err
	}
	return s.listOutputs(ctx, job.JobID)
}

func (s Service) ScriptRunResult(ctx context.Context, jobID, executionMode, waitResult string) (RunResult, error) {
	return s.RunResultForJob(ctx, jobID, executionMode, waitResult)
}

func (s Service) WorkflowRunResult(ctx context.Context, jobID, executionMode, waitResult string) (RunResult, error) {
	return s.RunResultForJob(ctx, jobID, executionMode, waitResult)
}

func (s Service) RunResultForJob(ctx context.Context, jobID, executionMode, waitResult string) (RunResult, error) {
	detail, err := s.GetJob(ctx, jobID)
	if err != nil {
		return RunResult{}, err
	}
	artifactDetails := []artifacts.ArtifactDetail{}
	for _, artifact := range detail.Artifacts {
		artifactDetail, err := s.Artifacts.GetArtifact(ctx, artifact.ArtifactID)
		if err != nil {
			return RunResult{}, err
		}
		artifactDetails = append(artifactDetails, artifactDetail)
	}
	return RunResult{
		Job:           detail,
		Artifacts:     artifactDetails,
		ExecutionMode: executionMode,
		WaitResult:    waitResult,
		TimedOut:      waitResult == WaitResultWaitTimeout,
	}, nil
}

func (s Service) WaitForJobStatus(ctx context.Context, jobID string, targets []string, opts WaitOptions) (JobDetail, string, error) {
	targetSet := map[string]struct{}{}
	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target != "" {
			targetSet[target] = struct{}{}
		}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	deadline := time.NewTimer(opts.Timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		detail, err := s.GetJob(ctx, jobID)
		if err != nil {
			return JobDetail{}, "", err
		}
		if _, ok := targetSet[detail.Job.Status]; ok {
			return detail, waitResultForStatus(detail.Job.Status, false), nil
		}
		if IsTerminalStatus(detail.Job.Status) {
			return detail, waitResultForStatus(detail.Job.Status, false), nil
		}
		select {
		case <-ctx.Done():
			return detail, WaitResultWaitTimeout, nil
		case <-deadline.C:
			return detail, WaitResultWaitTimeout, nil
		case <-ticker.C:
		}
	}
}

func (s Service) WaitForJobTerminal(ctx context.Context, jobID string, opts WaitOptions) (JobDetail, string, error) {
	return s.WaitForJobStatus(ctx, jobID, []string{
		StatusCompleted,
		StatusFailed,
		StatusCancelled,
		StatusTimedOut,
	}, opts)
}

func (s Service) listJobsWithFilter(ctx context.Context, filter ListFilter, queueOnly bool) ([]Job, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	query := jobSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}
	if strings.TrimSpace(filter.Status) != "" {
		add("j.status =", strings.TrimSpace(filter.Status))
	}
	if strings.TrimSpace(filter.JobType) != "" {
		add("j.job_type =", strings.TrimSpace(filter.JobType))
	}
	if strings.TrimSpace(filter.ScriptRef) != "" {
		script, err := s.Scripts.GetScript(ctx, filter.ScriptRef)
		if err != nil {
			return nil, err
		}
		add("j.script_id =", script.Script.ScriptID)
	}
	if strings.TrimSpace(filter.WorkflowRef) != "" {
		workflow, err := s.Workflows.GetWorkflow(ctx, filter.WorkflowRef)
		if err != nil {
			return nil, err
		}
		add("j.workflow_id =", workflow.Workflow.WorkflowID)
	}
	if strings.TrimSpace(filter.ProjectRef) != "" || strings.TrimSpace(filter.ScopeRef) != "" {
		scopeID, err := s.resolveOptionalScope(ctx, filter.ProjectRef, filter.ScopeRef)
		if err != nil {
			return nil, err
		}
		add("j.scope_id =", scopeID)
	}
	if filter.ManualOnly {
		query += " AND j.manual_action_required = true"
	}
	args = append(args, filter.Limit)
	if queueOnly {
		query += fmt.Sprintf(" ORDER BY j.priority ASC, COALESCE(j.next_attempt_at, j.queued_at, j.created_at) ASC, j.created_at ASC LIMIT $%d", len(args))
	} else {
		query += fmt.Sprintf(" ORDER BY j.created_at DESC LIMIT $%d", len(args))
	}
	return s.queryJobs(ctx, query, args...)
}

func (s Service) queryJobs(ctx context.Context, query string, args ...any) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func waitResultForStatus(status string, waitTimeout bool) string {
	if waitTimeout {
		return WaitResultWaitTimeout
	}
	switch status {
	case StatusQueued:
		return WaitResultQueued
	case StatusRunning:
		return WaitResultStarted
	case StatusCompleted:
		return WaitResultCompleted
	case StatusFailed:
		return WaitResultFailed
	case StatusCancelled:
		return WaitResultCancelled
	case StatusTimedOut:
		return WaitResultTimedOut
	default:
		return status
	}
}

func jobAttentionStatusesFromFilter(filter ListFilter) ([]string, error) {
	status := strings.ToLower(strings.TrimSpace(filter.AttentionStatus))
	if status == FailureAttentionStatusAll {
		return nil, nil
	}
	if status != "" {
		normalized := NormalizeFailureAttentionStatus(status)
		if normalized == "" {
			return nil, fmt.Errorf("invalid failure attention status %q", filter.AttentionStatus)
		}
		return []string{normalized}, nil
	}
	statuses := []string{FailureAttentionStatusActive}
	if filter.IncludeAcknowledged {
		statuses = append(statuses, FailureAttentionStatusAcknowledged)
	}
	if filter.IncludeArchived {
		statuses = append(statuses, FailureAttentionStatusArchived)
	}
	return statuses, nil
}
