package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

type KnowledgeEmbedderRuntime struct {
	Knowledge *knowledge.Service
	Runtime   knowledge.EmbeddingRuntime
	Now       func() time.Time
}

type knowledgeEmbedderConfig struct {
	SchemaVersion        string             `json:"schema_version"`
	BatchSize            int                `json:"batch_size"`
	MaxObjectsPerRun     int                `json:"max_objects_per_run"`
	LeaseDurationSeconds int                `json:"lease_duration_seconds"`
	Retry                indexerRetryConfig `json:"retry"`
}

func NewKnowledgeEmbedderRuntime(service *knowledge.Service, runtime knowledge.EmbeddingRuntime) KnowledgeEmbedderRuntime {
	return KnowledgeEmbedderRuntime{Knowledge: service, Runtime: runtime}
}

func (r KnowledgeEmbedderRuntime) Kind() string {
	return workers.KindKnowledgeEmbedder
}

func (r KnowledgeEmbedderRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindKnowledgeEmbedder,
		DisplayName:                  "Knowledge embedder",
		Description:                  "Claims due notes embedding work and generates local semantic vectors.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.knowledge",
		Status:                       workers.KindStatusDeprecated,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           false,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":600}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"cpu_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_embedder.config_schema.v0.8.6","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"knowledge_embedder.checkpoint_schema.v0.8.6","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_embedder.result_schema.v0.8.6","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true,"replacement_worker_kind":"knowledge_heavy"}`),
	}
}

func (r KnowledgeEmbedderRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"knowledge_embedder.config.v0.8.6","batch_size":1,"max_objects_per_run":1,"lease_duration_seconds":600,"retry":{"max_attempts":3,"base_delay_seconds":60,"max_delay_seconds":3600}}`)
}

func (r KnowledgeEmbedderRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.knowledge_embedder",
			WorkerKind:         workers.KindKnowledgeEmbedder,
			DisplayName:        "Knowledge embedder",
			Description:        "Generates semantic embeddings for queued notes chunks.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":600}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"cpu_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r KnowledgeEmbedderRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseKnowledgeEmbedderConfig(config)
	return err
}

func (r KnowledgeEmbedderRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Knowledge == nil || r.Knowledge.Store().DB() == nil {
		return workers.RunResult{}, fmt.Errorf("knowledge embedder service is not configured")
	}
	config, err := parseKnowledgeEmbedderConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	_ = config
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: json.RawMessage(`{"schema_version":"knowledge_embedder.deprecation.v1","status":"deprecated","replacement_worker":"main.knowledge_heavy","claimed":0}`), Counters: map[string]int64{"claimed": 0}, ResourceUsage: json.RawMessage(`{"available":false}`), Retryable: false}, nil
	/* Legacy execution is retained below as rollback source, but is unreachable
	   after unified heavy-stage cutover. */
	/*
		now := time.Now().UTC()
		if r.Now != nil {
			now = r.Now().UTC()
		}
		limit := config.BatchSize
		if config.MaxObjectsPerRun > 0 && config.MaxObjectsPerRun < limit {
			limit = config.MaxObjectsPerRun
		}
		result, err := r.Knowledge.RunEmbeddingWorkerOnce(ctx, knowledge.EmbeddingWorkerRunInput{
			WorkerRunID:      run.Run.WorkerRunID,
			Limit:            limit,
			LeaseDuration:    time.Duration(config.LeaseDurationSeconds) * time.Second,
			Runtime:          r.Runtime,
			RetryMaxAttempts: config.Retry.MaxAttempts,
			RetryDelay:       time.Duration(config.Retry.BaseDelaySeconds) * time.Second,
			Now:              now,
		})
		if err != nil {
			return workers.RunResult{}, err
		}
		tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
		if err != nil {
			return workers.RunResult{}, err
		}
		checkpoint := mustWorkerJSON(map[string]any{
			"schema_version":              "knowledge_embedder.checkpoint.v0.8.6",
			"last_embedding_work_item_id": result.LastWorkItemID,
			"last_knowledge_object_id":    result.LastKnowledgeObject,
			"released_expired":            result.ReleasedExpired,
			"claimed":                     result.Claimed,
			"embedded":                    result.Embedded,
			"reused":                      result.Reused,
			"activated":                   result.Activated,
			"historical_written":          result.HistoricalWritten,
			"stale_outputs":               result.StaleOutputs,
			"failed_retryable":            result.FailedRetryable,
			"failed_terminal":             result.FailedTerminal,
			"retention_deleted":           result.RetentionDeleted,
			"more_work":                   result.MoreWork,
			"updated_at":                  time.Now().UTC().Format(time.RFC3339Nano),
		})
		return workers.RunResult{
			Status:        workers.RunStatusSucceeded,
			ResultSummary: mustWorkerJSON(result),
			Counters: map[string]int64{
				"released_expired":   result.ReleasedExpired,
				"claimed":            result.Claimed,
				"embedded":           result.Embedded,
				"reused":             result.Reused,
				"activated":          result.Activated,
				"historical_written": result.HistoricalWritten,
				"stale_outputs":      result.StaleOutputs,
				"failed_retryable":   result.FailedRetryable,
				"failed_terminal":    result.FailedTerminal,
				"retention_deleted":  result.RetentionDeleted,
			},
			ResourceUsage: json.RawMessage(`{}`),
			CheckpointUpdates: []workers.CheckpointUpdate{
				{
					Key:           "default",
					SchemaVersion: "knowledge_embedder.checkpoint.v0.8.6",
					Value:         checkpoint,
					Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
				},
			},
			NextRunAfter: tickPolicy.NextAfter(time.Now().UTC()),
			Retryable:    false,
		}, nil */
}

func parseKnowledgeEmbedderConfig(raw json.RawMessage) (knowledgeEmbedderConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return knowledgeEmbedderConfig{}, err
	}
	config := knowledgeEmbedderConfig{
		SchemaVersion:        "knowledge_embedder.config.v0.8.6",
		BatchSize:            1,
		MaxObjectsPerRun:     1,
		LeaseDurationSeconds: 600,
		Retry: indexerRetryConfig{
			MaxAttempts:      3,
			BaseDelaySeconds: 60,
			MaxDelaySeconds:  3600,
		},
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return knowledgeEmbedderConfig{}, fmt.Errorf("%w: knowledge_embedder config_json is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 1
	}
	if config.BatchSize > 1 {
		config.BatchSize = 1
	}
	if config.MaxObjectsPerRun <= 0 || config.MaxObjectsPerRun > 1 {
		config.MaxObjectsPerRun = 1
	}
	if config.LeaseDurationSeconds <= 0 {
		config.LeaseDurationSeconds = 600
	}
	if config.Retry.MaxAttempts <= 0 {
		config.Retry.MaxAttempts = 3
	}
	if config.Retry.BaseDelaySeconds <= 0 {
		config.Retry.BaseDelaySeconds = 60
	}
	if config.Retry.MaxDelaySeconds <= 0 {
		config.Retry.MaxDelaySeconds = 3600
	}
	if config.Retry.MaxDelaySeconds < config.Retry.BaseDelaySeconds {
		config.Retry.MaxDelaySeconds = config.Retry.BaseDelaySeconds
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = "knowledge_embedder.config.v0.8.6"
	}
	return config, nil
}
