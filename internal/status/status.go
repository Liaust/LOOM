package status

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"loom.local/loom/internal/health"
)

const recentInterval = "24 hours"

type JobSummary struct {
	Running         int    `json:"running"`
	FailedRecent    int    `json:"failed_recent"`
	CompletedRecent int    `json:"completed_recent"`
	Error           string `json:"error,omitempty"`
}

type EventSummary struct {
	Recent int    `json:"recent"`
	Error  string `json:"error,omitempty"`
}

type SearchSummary struct {
	FailedRecent int    `json:"failed_recent"`
	Error        string `json:"error,omitempty"`
}

type RunnerSummary struct {
	RunnerID        string     `json:"runner_id,omitempty"`
	RunnerKey       string     `json:"runner_key,omitempty"`
	Status          string     `json:"status"`
	CurrentJobID    string     `json:"current_job_id,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type Report struct {
	Status         string        `json:"status"`
	Health         health.Report `json:"health"`
	Jobs           JobSummary    `json:"jobs"`
	Events         EventSummary  `json:"events"`
	Search         SearchSummary `json:"search"`
	Runner         RunnerSummary `json:"runner"`
	NextInspection []string      `json:"next_inspection"`
}

type Service struct {
	DB     *sql.DB
	Health health.Service
}

func NewService(db *sql.DB, healthService health.Service) Service {
	return Service{DB: db, Health: healthService}
}

func (s Service) Check(ctx context.Context) Report {
	healthReport := s.Health.Check(ctx)
	report := Report{
		Status: healthReport.Status,
		Health: healthReport,
		Runner: RunnerSummary{
			Status: "unknown",
		},
		NextInspection: []string{
			"loom jobs list --status failed",
			"loom index status --failed",
			"loom events list --limit 20",
			"loom storage doctor --include-fidelity",
		},
	}

	if s.DB == nil {
		report.Jobs.Error = "database unavailable"
		report.Events.Error = "database unavailable"
		report.Search.Error = "database unavailable"
		report.Runner.Error = "database unavailable"
		return report
	}

	report.Jobs = s.jobSummary(ctx)
	report.Events = s.eventSummary(ctx)
	report.Search = s.searchSummary(ctx)
	report.Runner = s.runnerSummary(ctx)
	return report
}

func (s Service) jobSummary(ctx context.Context) JobSummary {
	var summary JobSummary
	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM jobs.jobs
		WHERE status = 'running'
	`).Scan(&summary.Running); err != nil {
		summary.Error = err.Error()
		return summary
	}
	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM jobs.jobs
		WHERE status IN ('failed', 'timed_out', 'cancelled')
		  AND updated_at >= now() - $1::interval
	`, recentInterval).Scan(&summary.FailedRecent); err != nil {
		summary.Error = err.Error()
		return summary
	}
	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM jobs.jobs
		WHERE status = 'completed'
		  AND updated_at >= now() - $1::interval
	`, recentInterval).Scan(&summary.CompletedRecent); err != nil {
		summary.Error = err.Error()
	}
	return summary
}

func (s Service) eventSummary(ctx context.Context) EventSummary {
	var summary EventSummary
	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM events.events
		WHERE created_at >= now() - $1::interval
	`, recentInterval).Scan(&summary.Recent); err != nil {
		summary.Error = err.Error()
	}
	return summary
}

func (s Service) searchSummary(ctx context.Context) SearchSummary {
	var summary SearchSummary
	if err := s.DB.QueryRowContext(ctx, `
		SELECT count(*)
		FROM search.index_status
		WHERE status = 'failed'
		  AND updated_at >= now() - $1::interval
	`, recentInterval).Scan(&summary.FailedRecent); err != nil {
		summary.Error = err.Error()
	}
	return summary
}

func (s Service) runnerSummary(ctx context.Context) RunnerSummary {
	var summary RunnerSummary
	var currentJob sql.NullString
	var lastHeartbeat sql.NullTime
	err := s.DB.QueryRowContext(ctx, `
		SELECT runner_id, runner_key, status, current_job_id, last_heartbeat_at
		FROM jobs.runners
		ORDER BY updated_at DESC, created_at DESC
		LIMIT 1
	`).Scan(&summary.RunnerID, &summary.RunnerKey, &summary.Status, &currentJob, &lastHeartbeat)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerSummary{Status: "not_registered"}
	}
	if err != nil {
		return RunnerSummary{Status: "unknown", Error: err.Error()}
	}
	if currentJob.Valid {
		summary.CurrentJobID = currentJob.String
	}
	if lastHeartbeat.Valid {
		value := lastHeartbeat.Time.UTC()
		summary.LastHeartbeatAt = &value
	}
	return summary
}
