package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/workers"
)

const (
	mainDocumentsImportConfigV06 = "main_documents_import.config.v0.6"
	mainDocumentsImportConfigV07 = "main_documents_import.config.v0.7"
)

type MainDocumentsImportRuntime struct {
	MainStorage mainstorage.Service
	BackingRoot string
	NodeKey     string
	Now         func() time.Time
}

type mainDocumentsImportConfig struct {
	SchemaVersion             string `json:"schema_version"`
	BackingRoot               string `json:"backing_root"`
	NodeKey                   string `json:"node_key,omitempty"`
	StableWindowSeconds       int    `json:"stable_window_seconds"`
	MaxFilesPerRun            int    `json:"max_files_per_run"`
	MaxBytesHashedPerRun      int64  `json:"max_bytes_hashed_per_run,omitempty"`
	MaxRuntimeSeconds         int    `json:"max_runtime_seconds,omitempty"`
	IdleTickIntervalSeconds   int    `json:"idle_tick_interval_seconds"`
	ActiveTickIntervalSeconds int    `json:"active_tick_interval_seconds"`
}

type mainDocumentsImportSummary struct {
	SchemaVersion          string              `json:"schema_version"`
	Status                 string              `json:"status"`
	BackingRoot            string              `json:"backing_root"`
	Discovered             int64               `json:"discovered"`
	Accepted               int64               `json:"accepted"`
	Delayed                int64               `json:"delayed"`
	Skipped                int64               `json:"skipped"`
	AlreadyCataloged       int64               `json:"already_cataloged"`
	Remaining              int64               `json:"remaining"`
	Failed                 int64               `json:"failed"`
	ByteBudgetExhausted    bool                `json:"byte_budget_exhausted,omitempty"`
	RuntimeBudgetExhausted bool                `json:"runtime_budget_exhausted,omitempty"`
	MissingCataloged       int64               `json:"missing_cataloged"`
	Tombstoned             int64               `json:"tombstoned"`
	BytesHashed            int64               `json:"bytes_hashed"`
	Metrics                mainstorage.Metrics `json:"metrics,omitempty"`
	MoreWork               bool                `json:"more_work"`
	LastStorageEntryID     string              `json:"last_storage_entry_id,omitempty"`
}

func NewMainDocumentsImportRuntime(mainStorage mainstorage.Service, backingRoot string) MainDocumentsImportRuntime {
	return MainDocumentsImportRuntime{
		MainStorage: mainStorage,
		BackingRoot: strings.TrimSpace(backingRoot),
		NodeKey:     mainstorage.DefaultNodeKey,
	}
}

func (r MainDocumentsImportRuntime) Kind() string {
	return workers.KindMainDocumentsImport
}

func (r MainDocumentsImportRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindMainDocumentsImport,
		DisplayName:                  "Main Documents importer",
		Description:                  "Scans canonical Box Documents, waits for stable files, catalogs accepted documents, and preserves retained payloads.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.storage",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":120}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"main_documents_import.config_schema.v0.7","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"main_documents_import.checkpoint_schema.v0.6","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"main_documents_import.result_schema.v0.7","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r MainDocumentsImportRuntime) DefaultConfig() json.RawMessage {
	payload, _ := json.Marshal(defaultMainDocumentsImportConfig(r.BackingRoot, r.NodeKey))
	return payload
}

func (r MainDocumentsImportRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.main_documents_import",
			WorkerKind:         workers.KindMainDocumentsImport,
			DisplayName:        "Main Documents importer",
			Description:        "Imports stable files copied into writable main/Documents storage.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":120}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r MainDocumentsImportRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := r.parseConfig(config)
	return err
}

func (r MainDocumentsImportRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	config, err := r.parseConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	result, err := r.MainStorage.RunOnce(ctx, mainstorage.Config{
		BackingRoot:          config.BackingRoot,
		NodeKey:              config.NodeKey,
		StableWindow:         time.Duration(config.StableWindowSeconds) * time.Second,
		MaxFilesPerRun:       config.MaxFilesPerRun,
		MaxBytesHashedPerRun: config.MaxBytesHashedPerRun,
		MaxRuntime:           time.Duration(config.MaxRuntimeSeconds) * time.Second,
		Now:                  r.Now,
	})
	if err != nil {
		return workers.RunResult{}, err
	}

	moreWork := result.Status.FilesDelayed > 0 || result.Status.FilesRemaining > 0
	summary := mainDocumentsImportSummary{
		SchemaVersion:          "main_documents_import.result.v0.7",
		Status:                 "ok",
		BackingRoot:            result.Status.BackingRoot,
		Discovered:             result.Status.FilesDiscovered,
		Accepted:               result.Status.FilesAccepted,
		Delayed:                result.Status.FilesDelayed,
		Skipped:                result.Status.FilesSkipped,
		AlreadyCataloged:       result.Status.FilesAlreadyCataloged,
		Remaining:              result.Status.FilesRemaining,
		Failed:                 result.Status.FilesFailed,
		ByteBudgetExhausted:    result.Status.ByteBudgetExhausted,
		RuntimeBudgetExhausted: result.Status.RuntimeBudgetExhausted,
		MissingCataloged:       result.Status.FilesMissingCataloged,
		Tombstoned:             result.Status.FilesTombstoned,
		BytesHashed:            result.Status.BytesHashed,
		Metrics:                result.Status.Metrics,
		MoreWork:               moreWork,
	}
	for i := len(result.Status.Imports) - 1; i >= 0; i-- {
		if result.Status.Imports[i].StorageEntryID != "" {
			summary.LastStorageEntryID = result.Status.Imports[i].StorageEntryID
			break
		}
	}
	summaryJSON, err := workers.JSONObject(mustMainDocumentsJSON(summary), "result_summary")
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpointJSON, err := workers.JSONObject(mustMainDocumentsJSON(map[string]any{
		"schema_version":     "main_documents_import.checkpoint.v0.6",
		"last_run_id":        run.Run.WorkerRunID,
		"backing_root":       result.Status.BackingRoot,
		"files_discovered":   result.Status.FilesDiscovered,
		"files_accepted":     result.Status.FilesAccepted,
		"files_tombstoned":   result.Status.FilesTombstoned,
		"already_cataloged":  result.Status.FilesAlreadyCataloged,
		"files_remaining":    result.Status.FilesRemaining,
		"latest_accepted_at": latestTimeString(result.Status.LatestAcceptedAt),
		"generated_at":       result.Status.GeneratedAt.Format(time.RFC3339Nano),
	}), "checkpoint")
	if err != nil {
		return workers.RunResult{}, err
	}
	nextRunAfter := time.Now().UTC().Add(time.Duration(config.IdleTickIntervalSeconds) * time.Second)
	if moreWork {
		nextRunAfter = time.Now().UTC().Add(time.Duration(config.ActiveTickIntervalSeconds) * time.Second)
	}
	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: summaryJSON,
		Counters: map[string]int64{
			"files_discovered":           result.Status.FilesDiscovered,
			"files_accepted":             result.Status.FilesAccepted,
			"files_delayed":              result.Status.FilesDelayed,
			"files_skipped":              result.Status.FilesSkipped,
			"already_cataloged":          result.Status.FilesAlreadyCataloged,
			"files_remaining":            result.Status.FilesRemaining,
			"files_failed":               result.Status.FilesFailed,
			"missing_cataloged":          result.Status.FilesMissingCataloged,
			"files_tombstoned":           result.Status.FilesTombstoned,
			"bytes_hashed":               result.Status.BytesHashed,
			"byte_budget_exhausted":      boolCounter(result.Status.ByteBudgetExhausted),
			"runtime_budget_exhausted":   boolCounter(result.Status.RuntimeBudgetExhausted),
			"duration_ms":                result.Status.Metrics.TotalDurationMS,
			"discover_duration_ms":       result.Status.Metrics.DiscoverDurationMS,
			"hash_duration_ms":           result.Status.Metrics.HashDurationMS,
			"retention_copy_duration_ms": result.Status.Metrics.RetentionCopyDurationMS,
			"register_duration_ms":       result.Status.Metrics.RegisterDurationMS,
			"reconcile_duration_ms":      result.Status.Metrics.ReconcileDurationMS,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "main_documents_import.checkpoint.v0.6",
				Value:         checkpointJSON,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: &nextRunAfter,
		Retryable:    false,
	}, nil
}

func (r MainDocumentsImportRuntime) parseConfig(raw json.RawMessage) (mainDocumentsImportConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return mainDocumentsImportConfig{}, err
	}
	config := defaultMainDocumentsImportConfig(r.BackingRoot, r.NodeKey)
	if len(normalized) > 0 && strings.TrimSpace(string(normalized)) != "{}" {
		if err := json.Unmarshal(normalized, &config); err != nil {
			return mainDocumentsImportConfig{}, fmt.Errorf("%w: main_documents_import config is invalid JSON: %w", workers.ErrInvalid, err)
		}
	}
	config.SchemaVersion = strings.TrimSpace(config.SchemaVersion)
	if config.SchemaVersion == "" {
		config.SchemaVersion = mainDocumentsImportConfigV07
	}
	switch config.SchemaVersion {
	case mainDocumentsImportConfigV06, mainDocumentsImportConfigV07:
		config.SchemaVersion = mainDocumentsImportConfigV07
	default:
		return mainDocumentsImportConfig{}, fmt.Errorf("%w: unsupported main_documents_import schema_version %q", workers.ErrInvalid, config.SchemaVersion)
	}
	config.BackingRoot = strings.TrimSpace(config.BackingRoot)
	if config.BackingRoot == "" {
		return mainDocumentsImportConfig{}, fmt.Errorf("%w: main_documents_import backing_root is required", workers.ErrInvalid)
	}
	config.NodeKey = strings.TrimSpace(config.NodeKey)
	if config.NodeKey == "" {
		config.NodeKey = mainstorage.DefaultNodeKey
	}
	if config.StableWindowSeconds <= 0 {
		config.StableWindowSeconds = int(mainstorage.DefaultStableWindow.Seconds())
	}
	if config.MaxFilesPerRun <= 0 {
		config.MaxFilesPerRun = mainstorage.DefaultMaxFilesPerRun
	}
	if config.MaxFilesPerRun > 10000 {
		return mainDocumentsImportConfig{}, fmt.Errorf("%w: main_documents_import max_files_per_run must be <= 10000", workers.ErrInvalid)
	}
	if config.MaxBytesHashedPerRun < 0 {
		return mainDocumentsImportConfig{}, fmt.Errorf("%w: main_documents_import max_bytes_hashed_per_run cannot be negative", workers.ErrInvalid)
	}
	if config.MaxRuntimeSeconds < 0 {
		return mainDocumentsImportConfig{}, fmt.Errorf("%w: main_documents_import max_runtime_seconds cannot be negative", workers.ErrInvalid)
	}
	if config.IdleTickIntervalSeconds <= 0 {
		config.IdleTickIntervalSeconds = 300
	}
	if config.ActiveTickIntervalSeconds <= 0 {
		config.ActiveTickIntervalSeconds = 5
	}
	return config, nil
}

func defaultMainDocumentsImportConfig(backingRoot, nodeKey string) mainDocumentsImportConfig {
	nodeKey = strings.TrimSpace(nodeKey)
	if nodeKey == "" {
		nodeKey = mainstorage.DefaultNodeKey
	}
	return mainDocumentsImportConfig{
		SchemaVersion:             mainDocumentsImportConfigV07,
		BackingRoot:               strings.TrimSpace(backingRoot),
		NodeKey:                   nodeKey,
		StableWindowSeconds:       int(mainstorage.DefaultStableWindow.Seconds()),
		MaxFilesPerRun:            mainstorage.DefaultMaxFilesPerRun,
		MaxBytesHashedPerRun:      0,
		MaxRuntimeSeconds:         0,
		IdleTickIntervalSeconds:   300,
		ActiveTickIntervalSeconds: 5,
	}
}

func mustMainDocumentsJSON(value any) json.RawMessage {
	payload, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func latestTimeString(value *time.Time) string {
	if value == nil || value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
