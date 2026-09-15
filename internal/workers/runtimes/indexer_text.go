package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workers"
)

type IndexerTextRuntime struct {
	Search search.Service
	Now    func() time.Time
}

type indexerTextConfig struct {
	SchemaVersion         string             `json:"schema_version"`
	BatchSize             int                `json:"batch_size"`
	MaxRuntimeSeconds     int                `json:"max_runtime_seconds"`
	MaxObjectsPerRun      int                `json:"max_objects_per_run"`
	MaxTextBytesPerObject int64              `json:"max_text_bytes_per_object"`
	MaxTextBytesPerRun    int64              `json:"max_text_bytes_per_run"`
	MaxChunksPerObject    int                `json:"max_chunks_per_object"`
	LeaseDurationSeconds  int                `json:"lease_duration_seconds"`
	Retry                 indexerRetryConfig `json:"retry"`
}

type indexerRetryConfig struct {
	MaxAttempts      int `json:"max_attempts"`
	BaseDelaySeconds int `json:"base_delay_seconds"`
	MaxDelaySeconds  int `json:"max_delay_seconds"`
}

type indexerTextSummary struct {
	SchemaVersion       string `json:"schema_version"`
	Status              string `json:"status"`
	Claimed             int64  `json:"claimed"`
	Indexed             int64  `json:"indexed"`
	DisabledByPolicy    int64  `json:"disabled_by_policy"`
	SkippedUnsupported  int64  `json:"skipped_unsupported"`
	RetryScheduled      int64  `json:"retry_scheduled"`
	FailedTerminal      int64  `json:"failed_terminal"`
	TextBytesRead       int64  `json:"text_bytes_read"`
	ChunksCreated       int64  `json:"chunks_created"`
	DocumentsCreated    int64  `json:"documents_created"`
	ReleasedExpired     int64  `json:"released_expired"`
	MoreWork            bool   `json:"more_work"`
	LastClaimedStatusID string `json:"last_claimed_index_status_id,omitempty"`
	LastObjectID        string `json:"last_object_id,omitempty"`
}

func NewIndexerTextRuntime(searchService search.Service) IndexerTextRuntime {
	return IndexerTextRuntime{Search: searchService}
}

func (r IndexerTextRuntime) Kind() string {
	return workers.KindIndexerText
}

func (r IndexerTextRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindIndexerText,
		DisplayName:                  "Text indexer",
		Description:                  "Claims queued text indexing work and writes extracted text, chunks, and search documents.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.search",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":35}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"indexer_text.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"indexer_text.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"indexer_text.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r IndexerTextRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"indexer_text.config.v0.2","batch_size":50,"max_runtime_seconds":30,"max_objects_per_run":50,"max_text_bytes_per_object":1048576,"max_text_bytes_per_run":10485760,"max_chunks_per_object":1000,"lease_duration_seconds":120,"retry":{"max_attempts":3,"base_delay_seconds":60,"max_delay_seconds":3600}}`)
}

func (r IndexerTextRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.indexer_text",
			WorkerKind:         workers.KindIndexerText,
			DisplayName:        "Text indexer",
			Description:        "Processes queued full-text indexing work for object versions.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":35}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r IndexerTextRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseIndexerTextConfig(config)
	return err
}

func (r IndexerTextRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Search.DB == nil {
		return workers.RunResult{}, fmt.Errorf("text indexer search service is not configured")
	}
	config, err := parseIndexerTextConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	released, err := r.Search.ReleaseExpiredTextClaims(ctx, now)
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("release expired text claims: %w", err)
	}

	limit := config.BatchSize
	if config.MaxObjectsPerRun > 0 && config.MaxObjectsPerRun < limit {
		limit = config.MaxObjectsPerRun
	}
	items, err := r.Search.ClaimTextWork(ctx, run.Run.WorkerRunID, search.TextWorkClaimOptions{
		Limit:         limit,
		LeaseDuration: time.Duration(config.LeaseDurationSeconds) * time.Second,
		Now:           now,
	})
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("claim text index work: %w", err)
	}

	summary := indexerTextSummary{
		SchemaVersion:   "indexer_text.result.v0.2",
		Status:          "ok",
		Claimed:         int64(len(items)),
		ReleasedExpired: int64(released),
	}
	deadline := now.Add(time.Duration(config.MaxRuntimeSeconds) * time.Second)
	if !run.Deadline.IsZero() && run.Deadline.Before(deadline) {
		deadline = run.Deadline
	}

	for _, item := range items {
		if time.Now().UTC().After(deadline) {
			summary.MoreWork = true
			break
		}
		if config.MaxTextBytesPerRun > 0 && summary.TextBytesRead >= config.MaxTextBytesPerRun {
			summary.MoreWork = true
			break
		}
		summary.LastClaimedStatusID = item.IndexStatus.IndexStatusID
		summary.LastObjectID = item.ObjectID
		processResult, processErr := r.Search.ProcessTextWork(ctx, run.Request, item, search.TextWorkProcessOptions{
			MaxTextBytesPerObject: config.MaxTextBytesPerObject,
			MaxChunksPerObject:    config.MaxChunksPerObject,
		})
		summary.TextBytesRead += processResult.TextBytesRead
		summary.ChunksCreated += processResult.ChunksCreated
		summary.DocumentsCreated += processResult.DocumentsCreated

		if processErr != nil {
			failedStatus, failErr := r.Search.FailTextWork(ctx, item, search.TextWorkFailure{
				ErrorCode:    processResult.LastErrorCode,
				ErrorMessage: processResult.LastErrorMessage,
				Retryable:    processResult.Retryable,
				MaxAttempts:  config.Retry.MaxAttempts,
				BaseDelay:    time.Duration(config.Retry.BaseDelaySeconds) * time.Second,
				MaxDelay:     time.Duration(config.Retry.MaxDelaySeconds) * time.Second,
				WorkerRunID:  run.Run.WorkerRunID,
				Now:          time.Now().UTC(),
			})
			if failErr != nil {
				return workers.RunResult{}, fmt.Errorf("record text index failure: %w", failErr)
			}
			if failedStatus.ManualAction {
				summary.FailedTerminal++
			} else {
				summary.RetryScheduled++
			}
			continue
		}

		finalStatus, err := r.Search.CompleteTextWork(ctx, item, processResult, run.Run.WorkerRunID)
		if err != nil {
			return workers.RunResult{}, fmt.Errorf("complete text index work: %w", err)
		}
		switch finalStatus.Status {
		case "indexed":
			summary.Indexed++
		case "disabled_by_policy":
			summary.DisabledByPolicy++
		case "skipped_unsupported":
			summary.SkippedUnsupported++
		}
	}
	if len(items) == limit && limit > 0 {
		summary.MoreWork = true
	}

	resultSummary := mustWorkerJSON(summary)
	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":               "indexer_text.checkpoint.v0.2",
		"last_claimed_index_status_id": summary.LastClaimedStatusID,
		"last_object_id":               summary.LastObjectID,
		"claimed":                      summary.Claimed,
		"indexed":                      summary.Indexed,
		"skipped_unsupported":          summary.SkippedUnsupported,
		"failed":                       summary.FailedTerminal + summary.RetryScheduled,
		"text_bytes":                   summary.TextBytesRead,
		"chunks_created":               summary.ChunksCreated,
		"released_expired_text_claims": summary.ReleasedExpired,
		"more_work":                    summary.MoreWork,
		"updated_at":                   time.Now().UTC().Format(time.RFC3339Nano),
	})
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}

	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters: map[string]int64{
			"claimed":             summary.Claimed,
			"indexed":             summary.Indexed,
			"disabled_by_policy":  summary.DisabledByPolicy,
			"skipped_unsupported": summary.SkippedUnsupported,
			"retry_scheduled":     summary.RetryScheduled,
			"failed_terminal":     summary.FailedTerminal,
			"text_bytes_read":     summary.TextBytesRead,
			"chunks_created":      summary.ChunksCreated,
			"documents_created":   summary.DocumentsCreated,
			"released_expired":    summary.ReleasedExpired,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "indexer_text.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(time.Now().UTC()),
		Retryable:    false,
	}, nil
}

func parseIndexerTextConfig(raw json.RawMessage) (indexerTextConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return indexerTextConfig{}, err
	}
	config := indexerTextConfig{
		SchemaVersion:         "indexer_text.config.v0.2",
		BatchSize:             50,
		MaxRuntimeSeconds:     30,
		MaxObjectsPerRun:      50,
		MaxTextBytesPerObject: 1048576,
		MaxTextBytesPerRun:    10485760,
		MaxChunksPerObject:    1000,
		LeaseDurationSeconds:  120,
		Retry: indexerRetryConfig{
			MaxAttempts:      3,
			BaseDelaySeconds: 60,
			MaxDelaySeconds:  3600,
		},
	}
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return indexerTextConfig{}, fmt.Errorf("%w: indexer_text config_json is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 50
	}
	if config.BatchSize > 200 {
		config.BatchSize = 200
	}
	if config.MaxObjectsPerRun <= 0 {
		config.MaxObjectsPerRun = config.BatchSize
	}
	if config.MaxRuntimeSeconds <= 0 {
		config.MaxRuntimeSeconds = 30
	}
	if config.MaxTextBytesPerObject <= 0 {
		config.MaxTextBytesPerObject = 1048576
	}
	if config.MaxTextBytesPerRun <= 0 {
		config.MaxTextBytesPerRun = 10485760
	}
	if config.MaxChunksPerObject <= 0 {
		config.MaxChunksPerObject = 1000
	}
	if config.LeaseDurationSeconds <= 0 {
		config.LeaseDurationSeconds = 120
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
		config.SchemaVersion = "indexer_text.config.v0.2"
	}
	return config, nil
}
