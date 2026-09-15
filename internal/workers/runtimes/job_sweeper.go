package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/workers"
)

type JobSweeperRuntime struct {
	Jobs jobs.Service
	Now  func() time.Time
}

type jobSweeperConfig struct {
	SchemaVersion                  string `json:"schema_version"`
	RunnerOfflineAfterSeconds      int    `json:"runner_offline_after_seconds"`
	StaleRunningLeaseAfterSeconds  int    `json:"stale_running_lease_after_seconds"`
	QueuedLeaseReleaseAfterSeconds int    `json:"queued_lease_release_after_seconds"`
	BatchSize                      int    `json:"batch_size"`
	MaxRuntimeSeconds              int    `json:"max_runtime_seconds"`
	RetryExhaustedBehavior         string `json:"retry_exhausted_behavior"`
}

func NewJobSweeperRuntime(jobService jobs.Service) JobSweeperRuntime {
	return JobSweeperRuntime{Jobs: jobService}
}

func (r JobSweeperRuntime) Kind() string {
	return workers.KindJobSweeper
}

func (r JobSweeperRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindJobSweeper,
		DisplayName:                  "Job sweeper",
		Description:                  "Repairs stale job runner state and marks clearly abandoned running jobs for operator visibility.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.jobs",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           false,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":25}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"db_light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"job_sweeper.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"job_sweeper.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"job_sweeper.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r JobSweeperRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"job_sweeper.config.v0.2","runner_offline_after_seconds":120,"stale_running_lease_after_seconds":300,"queued_lease_release_after_seconds":120,"batch_size":100,"max_runtime_seconds":20,"retry_exhausted_behavior":"manual_action"}`)
}

func (r JobSweeperRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.job_sweeper",
			WorkerKind:         workers.KindJobSweeper,
			DisplayName:        "Job sweeper",
			Description:        "Conservatively repairs stale runner and job queue state.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":25}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"db_light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r JobSweeperRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseJobSweeperConfig(config)
	return err
}

func (r JobSweeperRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Jobs.DB == nil {
		return workers.RunResult{}, fmt.Errorf("job sweeper jobs service is not configured")
	}
	config, err := parseJobSweeperConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	result, err := r.Jobs.SweepJobs(ctx, run.Request, jobs.SweepInput{
		RunnerOfflineAfter:      time.Duration(config.RunnerOfflineAfterSeconds) * time.Second,
		StaleRunningLeaseAfter:  time.Duration(config.StaleRunningLeaseAfterSeconds) * time.Second,
		QueuedLeaseReleaseAfter: time.Duration(config.QueuedLeaseReleaseAfterSeconds) * time.Second,
		BatchSize:               config.BatchSize,
		Now:                     now,
	})
	if err != nil {
		return workers.RunResult{}, err
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	summary := mustWorkerJSON(result)
	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":         "job_sweeper.checkpoint.v0.2",
		"last_scan_at":           result.ScannedAt.Format(time.RFC3339Nano),
		"runners_marked_offline": result.RunnersMarkedOffline,
		"queued_leases_released": result.QueuedLeasesReleased,
		"jobs_timed_out":         result.JobsTimedOut,
		"manual_action":          result.ManualAction,
		"ambiguous":              result.Ambiguous,
	})
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: summary,
		Counters: map[string]int64{
			"runners_marked_offline": result.RunnersMarkedOffline,
			"queued_leases_released": result.QueuedLeasesReleased,
			"jobs_timed_out":         result.JobsTimedOut,
			"manual_action":          result.ManualAction,
			"ambiguous":              result.Ambiguous,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "job_sweeper.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(time.Now().UTC()),
		Retryable:    false,
	}, nil
}

func parseJobSweeperConfig(raw json.RawMessage) (jobSweeperConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return jobSweeperConfig{}, err
	}
	config := jobSweeperConfig{
		SchemaVersion:                  "job_sweeper.config.v0.2",
		RunnerOfflineAfterSeconds:      120,
		StaleRunningLeaseAfterSeconds:  300,
		QueuedLeaseReleaseAfterSeconds: 120,
		BatchSize:                      100,
		MaxRuntimeSeconds:              20,
		RetryExhaustedBehavior:         "manual_action",
	}
	if err := json.Unmarshal(normalized, &config); err != nil {
		return jobSweeperConfig{}, fmt.Errorf("%w: job_sweeper config is invalid JSON: %w", workers.ErrInvalid, err)
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = "job_sweeper.config.v0.2"
	}
	if config.RunnerOfflineAfterSeconds <= 0 {
		config.RunnerOfflineAfterSeconds = 120
	}
	if config.StaleRunningLeaseAfterSeconds <= 0 {
		config.StaleRunningLeaseAfterSeconds = 300
	}
	if config.QueuedLeaseReleaseAfterSeconds <= 0 {
		config.QueuedLeaseReleaseAfterSeconds = 120
	}
	if config.BatchSize <= 0 || config.BatchSize > 1000 {
		config.BatchSize = 100
	}
	if config.MaxRuntimeSeconds <= 0 {
		config.MaxRuntimeSeconds = 20
	}
	config.RetryExhaustedBehavior = strings.TrimSpace(config.RetryExhaustedBehavior)
	if config.RetryExhaustedBehavior == "" {
		config.RetryExhaustedBehavior = "manual_action"
	}
	if config.RetryExhaustedBehavior != "manual_action" {
		return jobSweeperConfig{}, fmt.Errorf("%w: retry_exhausted_behavior must be manual_action", workers.ErrInvalid)
	}
	return config, nil
}
