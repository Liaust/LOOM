package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/workers"
)

type JobRunnerRuntime struct {
	Jobs    jobs.Service
	Version string
	Now     func() time.Time
}

type jobRunnerConfig struct {
	SchemaVersion               string   `json:"schema_version"`
	RunnerKey                   string   `json:"runner_key"`
	RunnerType                  string   `json:"runner_type"`
	SupportedJobTypes           []string `json:"supported_job_types"`
	MaxJobsPerRun               int      `json:"max_jobs_per_run"`
	LeaseDurationSeconds        int      `json:"lease_duration_seconds"`
	LeaseRenewalIntervalSeconds int      `json:"lease_renewal_interval_seconds"`
	MaxRuntimeSeconds           int      `json:"max_runtime_seconds"`
	IdleTickIntervalSeconds     int      `json:"idle_tick_interval_seconds"`
	ActiveTickIntervalSeconds   int      `json:"active_tick_interval_seconds"`
	WorkingDirectoryRoot        string   `json:"working_directory_root"`
}

type jobRunnerSummary struct {
	SchemaVersion      string `json:"schema_version"`
	Status             string `json:"status"`
	RunnerID           string `json:"runner_id,omitempty"`
	RunnerKey          string `json:"runner_key,omitempty"`
	Claimed            int64  `json:"claimed"`
	Completed          int64  `json:"completed"`
	Failed             int64  `json:"failed"`
	TimedOut           int64  `json:"timed_out"`
	Cancelled          int64  `json:"cancelled"`
	ArtifactsCreated   int64  `json:"artifacts_created"`
	OutputsCreated     int64  `json:"outputs_created"`
	QueueDepthBefore   int    `json:"queue_depth_before"`
	QueueDepthAfter    int    `json:"queue_depth_after"`
	NoWork             bool   `json:"no_work"`
	MoreWork           bool   `json:"more_work"`
	LastJobID          string `json:"last_job_id,omitempty"`
	LastAttemptID      string `json:"last_attempt_id,omitempty"`
	LastJobStatus      string `json:"last_job_status,omitempty"`
	LastFailureCode    string `json:"last_failure_code,omitempty"`
	LastFailureMessage string `json:"last_failure_message,omitempty"`
	LastWaitResult     string `json:"last_wait_result,omitempty"`
}

func NewJobRunnerRuntime(jobService jobs.Service, loomVersion string) JobRunnerRuntime {
	return JobRunnerRuntime{
		Jobs:    jobService,
		Version: strings.TrimSpace(loomVersion),
	}
}

func (r JobRunnerRuntime) Kind() string {
	return workers.KindJobRunner
}

func (r JobRunnerRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindJobRunner,
		DisplayName:                  "Job runner",
		Description:                  "Claims queued script and workflow jobs and executes them through the durable jobs queue.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.jobs",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":43230}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"process_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"job_runner.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"job_runner.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"job_runner.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r JobRunnerRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"job_runner.config.v0.2","runner_key":"main-local-runner","runner_type":"local","supported_job_types":["script_run","workflow_run"],"max_jobs_per_run":1,"lease_duration_seconds":120,"lease_renewal_interval_seconds":30,"max_runtime_seconds":43200,"idle_tick_interval_seconds":60,"active_tick_interval_seconds":2,"working_directory_root":"jobs"}`)
}

func (r JobRunnerRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.job_runner",
			WorkerKind:         workers.KindJobRunner,
			DisplayName:        "Job runner",
			Description:        "Executes one queued script or workflow job per worker tick.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":43230}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"process_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r JobRunnerRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseJobRunnerConfig(config)
	return err
}

func (r JobRunnerRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Jobs.DB == nil {
		return workers.RunResult{}, fmt.Errorf("job runner jobs service is not configured")
	}
	config, err := parseJobRunnerConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	summary := jobRunnerSummary{
		SchemaVersion: "job_runner.result.v0.2",
		Status:        "ok",
	}
	before, err := r.Jobs.JobQueueSummary(ctx)
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("summarize queue before claim: %w", err)
	}
	summary.QueueDepthBefore = before.QueuedCount

	runner, err := r.Jobs.EnsureLocalRunner(ctx, run.Request, defaultJobRunnerVersion(r.Version))
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("ensure local job runner: %w", err)
	}
	summary.RunnerID = runner.RunnerID
	summary.RunnerKey = runner.RunnerKey

	claim, err := r.Jobs.ClaimNextWithOptions(ctx, run.Request, runner.RunnerID, jobs.ClaimOptions{
		LeaseDuration: time.Duration(config.LeaseDurationSeconds) * time.Second,
		WorkerRunID:   run.Run.WorkerRunID,
	})
	if errors.Is(err, jobs.ErrNoQueuedJob) {
		summary.NoWork = true
		after, summaryErr := r.Jobs.JobQueueSummary(ctx)
		if summaryErr != nil {
			return workers.RunResult{}, fmt.Errorf("summarize queue after idle claim: %w", summaryErr)
		}
		summary.QueueDepthAfter = after.QueuedCount
		return jobRunnerWorkerResult(summary, nextJobRunnerRunAfter(now, config, summary.MoreWork)), nil
	}
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("claim queued job: %w", err)
	}
	summary.Claimed = 1
	summary.LastJobID = claim.Job.JobID
	summary.LastAttemptID = claim.Attempt.JobAttemptID

	runResult, runErr := r.Jobs.RunClaimWithOptions(ctx, run.Request, claim, jobs.RunOptions{
		LeaseDuration:        time.Duration(config.LeaseDurationSeconds) * time.Second,
		LeaseRenewalInterval: time.Duration(config.LeaseRenewalIntervalSeconds) * time.Second,
		WorkerRunID:          run.Run.WorkerRunID,
	})
	if runResult.Job.Job.JobID == "" && runErr != nil {
		return workers.RunResult{}, fmt.Errorf("run claimed job: %w", runErr)
	}
	job := runResult.Job.Job
	summary.LastJobStatus = job.Status
	summary.ArtifactsCreated = int64(len(runResult.Artifacts))
	summary.OutputsCreated = int64(len(runResult.Job.Outputs))
	if job.FailureCode != nil {
		summary.LastFailureCode = *job.FailureCode
	}
	if job.FailureMessage != nil {
		summary.LastFailureMessage = *job.FailureMessage
	}
	switch job.Status {
	case jobs.StatusCompleted:
		summary.Completed = 1
	case jobs.StatusTimedOut:
		summary.TimedOut = 1
	case jobs.StatusCancelled:
		summary.Cancelled = 1
	case jobs.StatusFailed:
		summary.Failed = 1
	default:
		if runErr != nil {
			summary.Failed = 1
		}
	}

	after, err := r.Jobs.JobQueueSummary(ctx)
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("summarize queue after run: %w", err)
	}
	summary.QueueDepthAfter = after.QueuedCount
	summary.MoreWork = after.QueuedCount > 0
	return jobRunnerWorkerResult(summary, nextJobRunnerRunAfter(time.Now().UTC(), config, summary.MoreWork)), nil
}

func parseJobRunnerConfig(raw json.RawMessage) (jobRunnerConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return jobRunnerConfig{}, err
	}
	config := jobRunnerConfig{
		SchemaVersion:               "job_runner.config.v0.2",
		RunnerKey:                   jobs.DefaultRunnerKey,
		RunnerType:                  jobs.DefaultRunnerType,
		SupportedJobTypes:           []string{jobs.TypeScriptRun, jobs.TypeWorkflowRun},
		MaxJobsPerRun:               1,
		LeaseDurationSeconds:        120,
		LeaseRenewalIntervalSeconds: 30,
		MaxRuntimeSeconds:           43200,
		IdleTickIntervalSeconds:     60,
		ActiveTickIntervalSeconds:   2,
		WorkingDirectoryRoot:        "jobs",
	}
	if err := json.Unmarshal(normalized, &config); err != nil {
		return jobRunnerConfig{}, fmt.Errorf("%w: job_runner config is invalid JSON: %w", workers.ErrInvalid, err)
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = "job_runner.config.v0.2"
	}
	config.RunnerKey = strings.TrimSpace(config.RunnerKey)
	if config.RunnerKey == "" {
		config.RunnerKey = jobs.DefaultRunnerKey
	}
	config.RunnerType = strings.TrimSpace(config.RunnerType)
	if config.RunnerType == "" {
		config.RunnerType = jobs.DefaultRunnerType
	}
	if len(config.SupportedJobTypes) == 0 {
		config.SupportedJobTypes = []string{jobs.TypeScriptRun, jobs.TypeWorkflowRun}
	}
	if !containsJobType(config.SupportedJobTypes, jobs.TypeScriptRun) {
		return jobRunnerConfig{}, fmt.Errorf("%w: job_runner must support %s jobs", workers.ErrInvalid, jobs.TypeScriptRun)
	}
	if !containsJobType(config.SupportedJobTypes, jobs.TypeWorkflowRun) {
		config.SupportedJobTypes = append(config.SupportedJobTypes, jobs.TypeWorkflowRun)
	}
	if config.MaxJobsPerRun <= 0 {
		config.MaxJobsPerRun = 1
	}
	if config.MaxJobsPerRun != 1 {
		return jobRunnerConfig{}, fmt.Errorf("%w: job_runner currently supports max_jobs_per_run=1", workers.ErrInvalid)
	}
	if config.LeaseDurationSeconds <= 0 {
		config.LeaseDurationSeconds = 120
	}
	if config.LeaseRenewalIntervalSeconds <= 0 {
		config.LeaseRenewalIntervalSeconds = config.LeaseDurationSeconds / 2
	}
	if config.LeaseRenewalIntervalSeconds >= config.LeaseDurationSeconds {
		config.LeaseRenewalIntervalSeconds = config.LeaseDurationSeconds / 2
	}
	if config.LeaseRenewalIntervalSeconds <= 0 {
		config.LeaseRenewalIntervalSeconds = 1
	}
	if config.MaxRuntimeSeconds <= 0 {
		config.MaxRuntimeSeconds = 43200
	}
	if config.IdleTickIntervalSeconds <= 0 {
		config.IdleTickIntervalSeconds = 60
	}
	if config.ActiveTickIntervalSeconds <= 0 {
		config.ActiveTickIntervalSeconds = 2
	}
	config.WorkingDirectoryRoot = strings.TrimSpace(config.WorkingDirectoryRoot)
	if config.WorkingDirectoryRoot == "" {
		config.WorkingDirectoryRoot = "jobs"
	}
	return config, nil
}

func jobRunnerWorkerResult(summary jobRunnerSummary, nextRunAfter *time.Time) workers.RunResult {
	resultSummary := mustWorkerJSON(summary)
	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":    "job_runner.checkpoint.v0.2",
		"runner_id":         summary.RunnerID,
		"last_job_id":       summary.LastJobID,
		"last_attempt_id":   summary.LastAttemptID,
		"last_job_status":   summary.LastJobStatus,
		"queue_depth_after": summary.QueueDepthAfter,
		"claimed":           summary.Claimed,
		"completed":         summary.Completed,
		"failed":            summary.Failed,
		"timed_out":         summary.TimedOut,
		"cancelled":         summary.Cancelled,
		"no_work":           summary.NoWork,
		"more_work":         summary.MoreWork,
		"updated_at":        time.Now().UTC().Format(time.RFC3339Nano),
	})
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters: map[string]int64{
			"claimed":           summary.Claimed,
			"completed":         summary.Completed,
			"failed":            summary.Failed,
			"timed_out":         summary.TimedOut,
			"cancelled":         summary.Cancelled,
			"artifacts_created": summary.ArtifactsCreated,
			"outputs_created":   summary.OutputsCreated,
			"no_work":           boolCounter(summary.NoWork),
			"more_work":         boolCounter(summary.MoreWork),
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "job_runner.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: nextRunAfter,
		Retryable:    false,
	}
}

func nextJobRunnerRunAfter(now time.Time, config jobRunnerConfig, moreWork bool) *time.Time {
	interval := config.IdleTickIntervalSeconds
	if moreWork {
		interval = config.ActiveTickIntervalSeconds
	}
	next := now.UTC().Add(time.Duration(interval) * time.Second)
	return &next
}

func containsJobType(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func boolCounter(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func defaultJobRunnerVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "0.0.0-dev"
	}
	return value
}
