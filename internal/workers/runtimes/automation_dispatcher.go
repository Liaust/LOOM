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

type AutomationDispatcherRuntime struct {
	Automation automation.Service
	Now        func() time.Time
}

type automationDispatcherConfig struct {
	SchemaVersion             string `json:"schema_version"`
	BatchSize                 int    `json:"batch_size"`
	LeaseDurationSeconds      int    `json:"lease_duration_seconds"`
	IdleTickIntervalSeconds   int    `json:"idle_tick_interval_seconds"`
	ActiveTickIntervalSeconds int    `json:"active_tick_interval_seconds"`
}

type automationDispatcherSummary struct {
	SchemaVersion      string `json:"schema_version"`
	Status             string `json:"status"`
	Claimed            int64  `json:"claimed"`
	Succeeded          int64  `json:"succeeded"`
	Failed             int64  `json:"failed"`
	WaitingApproval    int64  `json:"waiting_approval"`
	Retrying           int64  `json:"retrying"`
	ManualAction       int64  `json:"manual_action"`
	NoWork             bool   `json:"no_work"`
	MoreWork           bool   `json:"more_work"`
	LastInvocationID   string `json:"last_invocation_id,omitempty"`
	LastCapabilityCall string `json:"last_capability_call,omitempty"`
	LastRouteID        string `json:"last_route_id,omitempty"`
	LastJobID          string `json:"last_job_id,omitempty"`
}

func NewAutomationDispatcherRuntime(automationService automation.Service) AutomationDispatcherRuntime {
	return AutomationDispatcherRuntime{Automation: automationService}
}

func (r AutomationDispatcherRuntime) Kind() string {
	return workers.KindAutomationDispatcher
}

func (r AutomationDispatcherRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindAutomationDispatcher,
		DisplayName:                  "Automation dispatcher",
		Description:                  "Claims pending automation invocations and calls their target capabilities through the routing layer.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.automation",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayCreateJobs:                true,
		MayCallCapabilities:          true,
		MayTouchFilesystem:           false,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":60}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"automation_dispatcher.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"automation_dispatcher.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"automation_dispatcher.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r AutomationDispatcherRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"automation_dispatcher.config.v0.2","batch_size":10,"lease_duration_seconds":120,"idle_tick_interval_seconds":60,"active_tick_interval_seconds":1}`)
}

func (r AutomationDispatcherRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.automation_dispatcher",
			WorkerKind:         workers.KindAutomationDispatcher,
			DisplayName:        "Automation dispatcher",
			Description:        "Dispatches pending automation invocations through routing.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":60}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r AutomationDispatcherRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseAutomationDispatcherConfig(config)
	return err
}

func (r AutomationDispatcherRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Automation.DB == nil {
		return workers.RunResult{}, fmt.Errorf("automation dispatcher service is not configured")
	}
	if r.Automation.Routing.DB == nil {
		return workers.RunResult{}, fmt.Errorf("automation dispatcher routing service is not configured")
	}
	config, err := parseAutomationDispatcherConfig(run.Instance.ConfigJSON)
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

	runResult, err := r.Automation.RunDispatcher(ctx, schedulerReq, automation.DispatcherRunInput{
		Now:           now,
		BatchSize:     config.BatchSize,
		LeaseDuration: time.Duration(config.LeaseDurationSeconds) * time.Second,
		WorkerRunID:   run.Run.WorkerRunID,
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	summary := automationDispatcherSummary{
		SchemaVersion:      "automation_dispatcher.result.v0.2",
		Status:             "ok",
		Claimed:            runResult.Claimed,
		Succeeded:          runResult.Succeeded,
		Failed:             runResult.Failed,
		WaitingApproval:    runResult.WaitingApproval,
		Retrying:           runResult.Retrying,
		ManualAction:       runResult.ManualAction,
		NoWork:             runResult.Claimed == 0,
		MoreWork:           runResult.Claimed >= int64(config.BatchSize),
		LastInvocationID:   runResult.LastInvocationID,
		LastCapabilityCall: runResult.LastCapabilityCall,
		LastRouteID:        runResult.LastRouteID,
		LastJobID:          runResult.LastJobID,
	}

	return automationDispatcherWorkerResult(summary, nextAutomationWorkerRunAfter(now, config.IdleTickIntervalSeconds, config.ActiveTickIntervalSeconds, summary.MoreWork)), nil
}

func parseAutomationDispatcherConfig(raw json.RawMessage) (automationDispatcherConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return automationDispatcherConfig{}, err
	}
	config := automationDispatcherConfig{
		SchemaVersion:             "automation_dispatcher.config.v0.2",
		BatchSize:                 10,
		LeaseDurationSeconds:      120,
		IdleTickIntervalSeconds:   60,
		ActiveTickIntervalSeconds: 1,
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return automationDispatcherConfig{}, fmt.Errorf("%w: automation_dispatcher config is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = "automation_dispatcher.config.v0.2"
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 10
	}
	if config.BatchSize > 100 {
		return automationDispatcherConfig{}, fmt.Errorf("%w: automation_dispatcher batch_size must be <= 100", workers.ErrInvalid)
	}
	if config.LeaseDurationSeconds <= 0 {
		config.LeaseDurationSeconds = 120
	}
	if config.IdleTickIntervalSeconds <= 0 {
		config.IdleTickIntervalSeconds = 60
	}
	if config.ActiveTickIntervalSeconds <= 0 {
		config.ActiveTickIntervalSeconds = 1
	}
	return config, nil
}

func automationDispatcherWorkerResult(summary automationDispatcherSummary, nextRunAfter *time.Time) workers.RunResult {
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: mustWorkerJSON(summary),
		Counters: map[string]int64{
			"claimed":          summary.Claimed,
			"succeeded":        summary.Succeeded,
			"failed":           summary.Failed,
			"waiting_approval": summary.WaitingApproval,
			"retrying":         summary.Retrying,
			"manual_action":    summary.ManualAction,
			"no_work":          boolCounter(summary.NoWork),
			"more_work":        boolCounter(summary.MoreWork),
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "automation_dispatcher.checkpoint.v0.2",
				Value: mustWorkerJSON(map[string]any{
					"schema_version":       "automation_dispatcher.checkpoint.v0.2",
					"last_invocation_id":   summary.LastInvocationID,
					"last_capability_call": summary.LastCapabilityCall,
					"last_route_id":        summary.LastRouteID,
					"last_job_id":          summary.LastJobID,
					"claimed":              summary.Claimed,
					"succeeded":            summary.Succeeded,
					"failed":               summary.Failed,
					"waiting_approval":     summary.WaitingApproval,
					"retrying":             summary.Retrying,
					"manual_action":        summary.ManualAction,
					"updated_at":           time.Now().UTC().Format(time.RFC3339Nano),
				}),
				Metadata: json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: nextRunAfter,
		Retryable:    false,
	}
}
