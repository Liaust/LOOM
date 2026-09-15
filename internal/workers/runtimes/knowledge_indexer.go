package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/workers"
)

type KnowledgeIndexerRuntime struct {
	Knowledge  *knowledge.Service
	Now        func() time.Time
	Projection *notesprojection.Service
	Custody    notesCustodyConsumer
}

type notesCustodyConsumer interface {
	ConsumeBatch(context.Context, string, int) (knowledge.NotesCustodyBatchResult, error)
}

const notesCustodyCheckpointSchema = "notes_custody.cursor.v1"

type notesCustodyCheckpoint struct {
	SchemaVersion string `json:"schema_version"`
	EventID       string `json:"event_id"`
}

func (r KnowledgeIndexerRuntime) WithArchiveService(service *storagearchive.WorkspaceMoveService, nodeKey string) KnowledgeIndexerRuntime {
	if service == nil || r.Knowledge == nil {
		r.Custody = nil
		return r
	}
	r.Custody = knowledge.NotesArchiveProjector{DB: r.Knowledge.Store().DB(), Workspace: *service, NodeKey: nodeKey}
	return r
}

func (r KnowledgeIndexerRuntime) consumeCustody(ctx context.Context, checkpoints map[string]workers.WorkerCheckpoint, limit int) (*knowledge.NotesCustodyBatchResult, error) {
	if r.Custody == nil {
		return nil, nil
	}
	var cursor notesCustodyCheckpoint
	if saved, ok := checkpoints["custody"]; ok {
		if saved.SchemaVersion != notesCustodyCheckpointSchema || len(saved.CheckpointJSON) > 1024 {
			return nil, fmt.Errorf("invalid Notes custody checkpoint")
		}
		decoder := json.NewDecoder(bytes.NewReader(saved.CheckpointJSON))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cursor); err != nil || cursor.SchemaVersion != notesCustodyCheckpointSchema {
			return nil, fmt.Errorf("invalid Notes custody checkpoint")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("invalid Notes custody checkpoint")
		}
	}
	batch, err := r.Custody.ConsumeBatch(ctx, cursor.EventID, min(limit, 50))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The underlying database/evidence cause must not enter worker output.
		return nil, fmt.Errorf("Notes custody batch unavailable")
	}
	return &batch, nil
}

func (r KnowledgeIndexerRuntime) WithProjectionRoot(root string) KnowledgeIndexerRuntime {
	projection := notesprojection.NewService(r.Knowledge, root)
	r.Projection = &projection
	return r
}

type knowledgeIndexerConfig struct {
	SchemaVersion         string             `json:"schema_version"`
	BatchSize             int                `json:"batch_size"`
	MaxRuntimeSeconds     int                `json:"max_runtime_seconds"`
	MaxObjectsPerRun      int                `json:"max_objects_per_run"`
	MaxTextBytesPerObject int64              `json:"max_text_bytes_per_object"`
	MaxExtractedTextBytes int64              `json:"max_extracted_text_bytes"`
	MaxChunksPerObject    int                `json:"max_chunks_per_object"`
	LeaseDurationSeconds  int                `json:"lease_duration_seconds"`
	Retry                 indexerRetryConfig `json:"retry"`
}

func NewKnowledgeIndexerRuntime(service *knowledge.Service) KnowledgeIndexerRuntime {
	return KnowledgeIndexerRuntime{Knowledge: service}
}

func (r KnowledgeIndexerRuntime) Kind() string {
	return workers.KindKnowledgeIndexer
}

func (r KnowledgeIndexerRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindKnowledgeIndexer,
		DisplayName:                  "Knowledge pipeline coordinator",
		Description:                  "Coordinates one unified version-fenced pipeline run per Notes file.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.knowledge",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		MayTouchFilesystem:           true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":40}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_indexer.config_schema.v1","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"knowledge_indexer.checkpoint_schema.v1","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_indexer.result_schema.v1","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true,"role":"notes_pipeline_coordinator","compatibility_worker_key":"main.knowledge_indexer"}`),
	}
}

func (r KnowledgeIndexerRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"knowledge_indexer.config.v0.8.5","batch_size":50,"max_runtime_seconds":30,"max_objects_per_run":50,"max_text_bytes_per_object":5242880,"max_extracted_text_bytes":10485760,"max_chunks_per_object":1000,"lease_duration_seconds":120,"retry":{"max_attempts":3,"base_delay_seconds":60,"max_delay_seconds":3600}}`)
}

func (r KnowledgeIndexerRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.knowledge_indexer",
			WorkerKind:         workers.KindKnowledgeIndexer,
			DisplayName:        "Knowledge pipeline coordinator",
			Description:        "Advances lightweight stages in unified Notes file pipelines.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":40}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r KnowledgeIndexerRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := parseKnowledgeIndexerConfig(config)
	return err
}

func (r KnowledgeIndexerRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Knowledge == nil || r.Knowledge.Store().DB() == nil {
		return workers.RunResult{}, fmt.Errorf("knowledge indexer service is not configured")
	}
	config, err := parseKnowledgeIndexerConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	var cursor knowledge.AdmissionCursor
	if saved, ok := run.Checkpoints["admission"]; ok {
		if err := json.Unmarshal(saved.CheckpointJSON, &cursor); err != nil {
			return workers.RunResult{}, fmt.Errorf("invalid Notes admission checkpoint: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.MaxRuntimeSeconds)*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	limit := config.BatchSize
	if config.MaxObjectsPerRun > 0 && config.MaxObjectsPerRun < limit {
		limit = config.MaxObjectsPerRun
	}
	custody, err := r.consumeCustody(ctx, run.Checkpoints, limit)
	if err != nil {
		return workers.RunResult{}, err
	}
	admission, err := r.Knowledge.AdmitRegisteredNotesOnce(ctx, cursor, limit)
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("admit registered Notes: %w", err)
	}
	result, err := r.Knowledge.RunKnowledgeIndexerOnce(ctx, knowledge.KnowledgeIndexerRunInput{
		WorkerRunID:           run.Run.WorkerRunID,
		Limit:                 limit,
		LeaseDuration:         time.Duration(config.LeaseDurationSeconds) * time.Second,
		MaxTextBytesPerObject: config.MaxTextBytesPerObject,
		MaxExtractedTextBytes: config.MaxExtractedTextBytes,
		MaxChunksPerObject:    config.MaxChunksPerObject,
		RetryMaxAttempts:      config.Retry.MaxAttempts,
		RetryBaseDelay:        time.Duration(config.Retry.BaseDelaySeconds) * time.Second,
		RetryMaximumDelay:     time.Duration(config.Retry.MaxDelaySeconds) * time.Second,
		Now:                   now,
	})
	if err != nil {
		return workers.RunResult{}, err
	}
	projectionChanged := false
	if r.Projection != nil {
		projectionChanged, err = r.Projection.Refresh(ctx)
		if err != nil {
			return workers.RunResult{}, fmt.Errorf("refresh generated Notes projection: %w", err)
		}
	}
	result.MoreWork = result.MoreWork || admission.MoreWork
	if custody != nil {
		result.MoreWork = result.MoreWork || !custody.Wrapped
		for _, item := range custody.Items {
			result.MoreWork = result.MoreWork || item.Finding != ""
		}
	}
	checkpoint := mustWorkerJSON(map[string]any{
		"schema_version":           "knowledge_indexer.checkpoint.v0.8",
		"last_pipeline_status_id":  result.LastPipelineStatusID,
		"last_knowledge_object_id": result.LastKnowledgeObjectID,
		"claimed":                  result.Claimed,
		"processed":                result.Processed,
		"skipped_unsupported":      result.SkippedUnsupported,
		"extracted":                result.Extracted,
		"metadata_only":            result.MetadataOnly,
		"too_large":                result.TooLarge,
		"password_required":        result.PasswordRequired,
		"no_embedded_text":         result.NoEmbeddedText,
		"source_unavailable":       result.SourceUnavailable,
		"failed":                   result.FailedTerminal + result.RetryScheduled,
		"released_expired_claims":  result.ReleasedExpired,
		"text_bytes_read":          result.TextBytesRead,
		"chunks_created":           result.ChunksCreated,
		"more_work":                result.MoreWork,
		"updated_at":               time.Now().UTC().Format(time.RFC3339Nano),
		"admission":                admission,
		"projection_changed":       projectionChanged,
	})
	output := workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: mustWorkerJSON(result),
		Counters: map[string]int64{
			"catalog_observed":    int64(admission.CatalogObserved),
			"synced_observed":     int64(admission.SyncedObserved),
			"admission_applied":   int64(admission.Applied),
			"admission_skipped":   int64(admission.Skipped),
			"released_expired":    result.ReleasedExpired,
			"claimed":             result.Claimed,
			"processed":           result.Processed,
			"skipped_unsupported": result.SkippedUnsupported,
			"extracted":           result.Extracted,
			"metadata_only":       result.MetadataOnly,
			"too_large":           result.TooLarge,
			"password_required":   result.PasswordRequired,
			"no_embedded_text":    result.NoEmbeddedText,
			"source_unavailable":  result.SourceUnavailable,
			"retry_scheduled":     result.RetryScheduled,
			"failed_terminal":     result.FailedTerminal,
			"text_bytes_read":     result.TextBytesRead,
			"chunks_created":      result.ChunksCreated,
			"links_created":       result.LinksCreated,
			"statuses_written":    result.StatusesWritten,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{Key: "admission", SchemaVersion: "knowledge_admission.cursor.v1", Value: mustWorkerJSON(admission.Cursor), Metadata: json.RawMessage(`{}`)},
			{
				Key:           "default",
				SchemaVersion: "knowledge_indexer.checkpoint.v0.8",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(time.Now().UTC()),
		Retryable:    false,
	}
	if custody != nil {
		var findings, projected, replayed int64
		for _, item := range custody.Items {
			if item.Finding != "" {
				findings++
			}
			projected += int64(item.Projected)
			replayed += int64(item.Replayed)
		}
		output.Counters["custody_events_observed"] = int64(len(custody.Items))
		output.Counters["custody_findings"] = findings
		output.Counters["custody_objects_projected"] = projected
		output.Counters["custody_objects_replayed"] = replayed
		output.ResultSummary = mustWorkerJSON(struct {
			knowledge.KnowledgeIndexerRunResult
			Custody *knowledge.NotesCustodyBatchResult `json:"custody"`
		}{result, custody})
		output.CheckpointUpdates = append(output.CheckpointUpdates, workers.CheckpointUpdate{
			Key: "custody", SchemaVersion: notesCustodyCheckpointSchema,
			Value:    mustWorkerJSON(notesCustodyCheckpoint{SchemaVersion: notesCustodyCheckpointSchema, EventID: custody.NextCursor}),
			Metadata: json.RawMessage(`{}`),
		})
	}
	return output, nil
}

func parseKnowledgeIndexerConfig(raw json.RawMessage) (knowledgeIndexerConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return knowledgeIndexerConfig{}, err
	}
	config := knowledgeIndexerConfig{
		SchemaVersion:         "knowledge_indexer.config.v0.8",
		BatchSize:             50,
		MaxRuntimeSeconds:     30,
		MaxObjectsPerRun:      50,
		MaxTextBytesPerObject: 5 * 1024 * 1024,
		MaxExtractedTextBytes: 10485760,
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
			return knowledgeIndexerConfig{}, fmt.Errorf("%w: knowledge_indexer config_json is invalid JSON: %w", workers.ErrInvalid, err)
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
		config.MaxTextBytesPerObject = 5 * 1024 * 1024
	}
	if config.MaxExtractedTextBytes <= 0 {
		config.MaxExtractedTextBytes = 10 * 1024 * 1024
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
		config.SchemaVersion = "knowledge_indexer.config.v0.8.5"
	}
	return config, nil
}
