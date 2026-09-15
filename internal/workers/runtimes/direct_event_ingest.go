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

type DirectEventIngestRuntime struct {
	Automation automation.Service
	Now        func() time.Time
}

type directEventIngestConfig struct {
	SchemaVersion             string `json:"schema_version"`
	BatchSize                 int    `json:"batch_size"`
	IdleTickIntervalSeconds   int    `json:"idle_tick_interval_seconds"`
	ActiveTickIntervalSeconds int    `json:"active_tick_interval_seconds"`
}

type directEventIngestSummary struct {
	SchemaVersion      string   `json:"schema_version"`
	Status             string   `json:"status"`
	Claimed            int64    `json:"claimed"`
	Mapped             int64    `json:"mapped"`
	CreatedInvocations int64    `json:"created_invocations"`
	Duplicates         int64    `json:"duplicates"`
	MappingFailed      int64    `json:"mapping_failed"`
	Failed             int64    `json:"failed"`
	NoWork             bool     `json:"no_work"`
	MoreWork           bool     `json:"more_work"`
	LastDirectEventID  string   `json:"last_direct_event_id,omitempty"`
	LastInvocationID   string   `json:"last_invocation_id,omitempty"`
	DirectEventIDs     []string `json:"direct_event_ids,omitempty"`
	InvocationIDs      []string `json:"invocation_ids,omitempty"`
}

func NewDirectEventIngestRuntime(automationService automation.Service) DirectEventIngestRuntime {
	return DirectEventIngestRuntime{Automation: automationService}
}

func (r DirectEventIngestRuntime) Kind() string {
	return workers.KindDirectEventIngest
}

func (r DirectEventIngestRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindDirectEventIngest,
		DisplayName:                  "Direct event ingest",
		Description:                  "Maps accepted direct-event occurrences into pending automation invocations.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.automation",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayCreateJobs:                false,
		MayCallCapabilities:          false,
		MayTouchFilesystem:           false,
		MayStoreRawPayloads:          true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"direct_event_ingest.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"direct_event_ingest.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"direct_event_ingest.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r DirectEventIngestRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"direct_event_ingest.config.v0.2","batch_size":25,"idle_tick_interval_seconds":60,"active_tick_interval_seconds":1}`)
}

func (r DirectEventIngestRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.direct_event_ingest",
			WorkerKind:         workers.KindDirectEventIngest,
			DisplayName:        "Direct event ingest",
			Description:        "Turns accepted direct-event occurrences into pending invocations.",
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

func (r DirectEventIngestRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseDirectEventIngestConfig(config)
	return err
}

func (r DirectEventIngestRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Automation.DB == nil {
		return workers.RunResult{}, fmt.Errorf("direct event ingest service is not configured")
	}
	config, err := parseDirectEventIngestConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	workerReq, err := requestctx.ResolveScheduler(ctx, r.Automation.DB, run.CorrelationID, requestctx.SchedulerResolveInput{
		Source: "worker:" + run.Instance.WorkerKey,
	})
	if err != nil {
		return workers.RunResult{}, err
	}
	runResult, err := r.Automation.RunDirectEventIngest(ctx, workerReq, automation.DirectEventIngestRunInput{
		Now:         now,
		BatchSize:   config.BatchSize,
		WorkerRunID: run.Run.WorkerRunID,
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	summary := directEventIngestSummary{
		SchemaVersion:      "direct_event_ingest.result.v0.2",
		Status:             "ok",
		Claimed:            runResult.Claimed,
		Mapped:             runResult.Mapped,
		CreatedInvocations: runResult.CreatedInvocations,
		Duplicates:         runResult.Duplicates,
		MappingFailed:      runResult.MappingFailed,
		Failed:             runResult.Failed,
		NoWork:             runResult.NoWork,
		MoreWork:           runResult.MoreWork,
		DirectEventIDs:     runResult.DirectEventIDs,
		InvocationIDs:      runResult.InvocationIDs,
	}
	if len(runResult.DirectEventIDs) > 0 {
		summary.LastDirectEventID = runResult.DirectEventIDs[len(runResult.DirectEventIDs)-1]
	}
	if len(runResult.InvocationIDs) > 0 {
		summary.LastInvocationID = runResult.InvocationIDs[len(runResult.InvocationIDs)-1]
	}
	return directEventIngestWorkerResult(summary, nextAutomationWorkerRunAfter(now, config.IdleTickIntervalSeconds, config.ActiveTickIntervalSeconds, summary.MoreWork)), nil
}

func parseDirectEventIngestConfig(raw json.RawMessage) (directEventIngestConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return directEventIngestConfig{}, err
	}
	config := directEventIngestConfig{
		SchemaVersion:             "direct_event_ingest.config.v0.2",
		BatchSize:                 25,
		IdleTickIntervalSeconds:   60,
		ActiveTickIntervalSeconds: 1,
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return directEventIngestConfig{}, fmt.Errorf("%w: direct_event_ingest config is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	if strings.TrimSpace(config.SchemaVersion) == "" {
		config.SchemaVersion = "direct_event_ingest.config.v0.2"
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 25
	}
	if config.BatchSize > 200 {
		return directEventIngestConfig{}, fmt.Errorf("%w: direct_event_ingest batch_size must be <= 200", workers.ErrInvalid)
	}
	if config.IdleTickIntervalSeconds <= 0 {
		config.IdleTickIntervalSeconds = 60
	}
	if config.ActiveTickIntervalSeconds <= 0 {
		config.ActiveTickIntervalSeconds = 1
	}
	return config, nil
}

func directEventIngestWorkerResult(summary directEventIngestSummary, nextRunAfter *time.Time) workers.RunResult {
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: mustWorkerJSON(summary),
		Counters: map[string]int64{
			"claimed":             summary.Claimed,
			"mapped":              summary.Mapped,
			"created_invocations": summary.CreatedInvocations,
			"duplicates":          summary.Duplicates,
			"mapping_failed":      summary.MappingFailed,
			"failed":              summary.Failed,
			"no_work":             boolCounter(summary.NoWork),
			"more_work":           boolCounter(summary.MoreWork),
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "direct_event_ingest.checkpoint.v0.2",
				Value: mustWorkerJSON(map[string]any{
					"schema_version":       "direct_event_ingest.checkpoint.v0.2",
					"last_direct_event_id": summary.LastDirectEventID,
					"last_invocation_id":   summary.LastInvocationID,
					"claimed":              summary.Claimed,
					"mapped":               summary.Mapped,
					"created_invocations":  summary.CreatedInvocations,
					"duplicates":           summary.Duplicates,
					"mapping_failed":       summary.MappingFailed,
					"failed":               summary.Failed,
					"updated_at":           time.Now().UTC().Format(time.RFC3339Nano),
				}),
				Metadata: json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: nextRunAfter,
		Retryable:    false,
	}
}
