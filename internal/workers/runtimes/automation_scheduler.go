package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/workers"
)

type AutomationSchedulerRuntime struct {
	Automation automation.Service
	Now        func() time.Time
}

type automationSchedulerConfig struct {
	SchemaVersion             string `json:"schema_version"`
	BatchSize                 int    `json:"batch_size"`
	LookaheadSeconds          int    `json:"lookahead_seconds"`
	IdleTickIntervalSeconds   int    `json:"idle_tick_interval_seconds"`
	ActiveTickIntervalSeconds int    `json:"active_tick_interval_seconds"`
}

type automationSchedulerSummary struct {
	SchemaVersion      string   `json:"schema_version"`
	Status             string   `json:"status"`
	ScannedSchedules   int64    `json:"scanned_schedules"`
	CreatedFires       int64    `json:"created_fires"`
	CreatedInvocations int64    `json:"created_invocations"`
	MissedFires        int64    `json:"missed_fires"`
	SkippedFires       int64    `json:"skipped_fires"`
	FailedSchedules    int64    `json:"failed_schedules"`
	NextDueAt          string   `json:"next_due_at,omitempty"`
	NoWork             bool     `json:"no_work"`
	MoreWork           bool     `json:"more_work"`
	LastScheduleFireID string   `json:"last_schedule_fire_id,omitempty"`
	LastInvocationID   string   `json:"last_invocation_id,omitempty"`
	ScheduleFireIDs    []string `json:"schedule_fire_ids,omitempty"`
	InvocationIDs      []string `json:"invocation_ids,omitempty"`
}

func NewAutomationSchedulerRuntime(automationService automation.Service) AutomationSchedulerRuntime {
	return AutomationSchedulerRuntime{Automation: automationService}
}

func (r AutomationSchedulerRuntime) Kind() string {
	return workers.KindAutomationScheduler
}

func (r AutomationSchedulerRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindAutomationScheduler,
		DisplayName:                  "Automation scheduler",
		Description:                  "Creates durable schedule-fire and invocation records for due main-owned schedules.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.automation",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayCreateJobs:                false,
		MayCallCapabilities:          false,
		MayTouchFilesystem:           false,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"automation_scheduler.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"automation_scheduler.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"automation_scheduler.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r AutomationSchedulerRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"automation_scheduler.config.v0.2","batch_size":25,"lookahead_seconds":0,"idle_tick_interval_seconds":60,"active_tick_interval_seconds":2}`)
}

func (r AutomationSchedulerRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.automation_scheduler",
			WorkerKind:         workers.KindAutomationScheduler,
			DisplayName:        "Automation scheduler",
			Description:        "Creates one durable occurrence per due schedule per worker tick.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r AutomationSchedulerRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseAutomationSchedulerConfig(config)
	return err
}

func (r AutomationSchedulerRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Automation.DB == nil {
		return workers.RunResult{}, fmt.Errorf("automation scheduler service is not configured")
	}
	config, err := parseAutomationSchedulerConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	schedulerReq, err := requestctx.ResolveScheduler(ctx, r.Automation.DB, run.CorrelationID, requestctx.SchedulerResolveInput{
		Source: "worker:" + run.Instance.WorkerKey,
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	runResult, err := r.Automation.RunScheduler(ctx, schedulerReq, automation.SchedulerRunInput{
		Now:             now,
		BatchSize:       config.BatchSize,
		LookaheadWindow: time.Duration(config.LookaheadSeconds) * time.Second,
		WorkerRunID:     run.Run.WorkerRunID,
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	summary := automationSchedulerSummary{
		SchemaVersion:      "automation_scheduler.result.v0.2",
		Status:             "ok",
		ScannedSchedules:   runResult.ScannedSchedules,
		CreatedFires:       runResult.CreatedFires,
		CreatedInvocations: runResult.CreatedInvocations,
		MissedFires:        runResult.MissedFires,
		SkippedFires:       runResult.SkippedFires,
		FailedSchedules:    runResult.FailedSchedules,
		NoWork:             runResult.ScannedSchedules == 0,
		ScheduleFireIDs:    runResult.ScheduleFireIDs,
		InvocationIDs:      runResult.InvocationIDs,
	}
	if runResult.NextDueAt != nil {
		summary.NextDueAt = runResult.NextDueAt.Format(time.RFC3339Nano)
		summary.MoreWork = !runResult.NextDueAt.After(now)
	}
	if len(runResult.ScheduleFireIDs) > 0 {
		summary.LastScheduleFireID = runResult.ScheduleFireIDs[len(runResult.ScheduleFireIDs)-1]
	}
	if len(runResult.InvocationIDs) > 0 {
		summary.LastInvocationID = runResult.InvocationIDs[len(runResult.InvocationIDs)-1]
	}

	return automationSchedulerWorkerResult(summary, nextAutomationWorkerRunAfter(now, config.IdleTickIntervalSeconds, config.ActiveTickIntervalSeconds, summary.MoreWork)), nil
}

func parseAutomationSchedulerConfig(raw json.RawMessage) (automationSchedulerConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return automationSchedulerConfig{}, err
	}
	config := automationSchedulerConfig{
		SchemaVersion:             "automation_scheduler.config.v0.2",
		BatchSize:                 25,
		IdleTickIntervalSeconds:   60,
		ActiveTickIntervalSeconds: 2,
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return automationSchedulerConfig{}, fmt.Errorf("%w: automation_scheduler config is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = "automation_scheduler.config.v0.2"
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 25
	}
	if config.BatchSize > 200 {
		return automationSchedulerConfig{}, fmt.Errorf("%w: automation_scheduler batch_size must be <= 200", workers.ErrInvalid)
	}
	if config.LookaheadSeconds < 0 {
		return automationSchedulerConfig{}, fmt.Errorf("%w: automation_scheduler lookahead_seconds must be >= 0", workers.ErrInvalid)
	}
	if config.IdleTickIntervalSeconds <= 0 {
		config.IdleTickIntervalSeconds = 60
	}
	if config.ActiveTickIntervalSeconds <= 0 {
		config.ActiveTickIntervalSeconds = 2
	}
	return config, nil
}

func automationSchedulerWorkerResult(summary automationSchedulerSummary, nextRunAfter *time.Time) workers.RunResult {
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: mustWorkerJSON(summary),
		Counters: map[string]int64{
			"scanned_schedules":   summary.ScannedSchedules,
			"created_fires":       summary.CreatedFires,
			"created_invocations": summary.CreatedInvocations,
			"missed_fires":        summary.MissedFires,
			"skipped_fires":       summary.SkippedFires,
			"failed_schedules":    summary.FailedSchedules,
			"no_work":             boolCounter(summary.NoWork),
			"more_work":           boolCounter(summary.MoreWork),
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "automation_scheduler.checkpoint.v0.2",
				Value: mustWorkerJSON(map[string]any{
					"schema_version":        "automation_scheduler.checkpoint.v0.2",
					"last_schedule_fire_id": summary.LastScheduleFireID,
					"last_invocation_id":    summary.LastInvocationID,
					"scanned_schedules":     summary.ScannedSchedules,
					"created_fires":         summary.CreatedFires,
					"created_invocations":   summary.CreatedInvocations,
					"missed_fires":          summary.MissedFires,
					"skipped_fires":         summary.SkippedFires,
					"failed_schedules":      summary.FailedSchedules,
					"next_due_at":           summary.NextDueAt,
					"updated_at":            time.Now().UTC().Format(time.RFC3339Nano),
				}),
				Metadata: json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: nextRunAfter,
		Retryable:    false,
	}
}

func nextAutomationWorkerRunAfter(now time.Time, idleSeconds, activeSeconds int, moreWork bool) *time.Time {
	interval := idleSeconds
	if moreWork {
		interval = activeSeconds
	}
	if interval <= 0 {
		interval = 30
	}
	next := now.UTC().Add(time.Duration(interval) * time.Second)
	return &next
}
