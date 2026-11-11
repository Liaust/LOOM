package runtimes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type projectionRefreshCheckpoint struct {
	LastSuccess time.Time `json:"last_success"`
}

type knowledgeAdmissionSweepCheckpoint struct {
	SourceGeneration  string    `json:"source_generation"`
	SweepStarted      bool      `json:"sweep_started"`
	Complete          bool      `json:"complete"`
	NextInventoryAt   time.Time `json:"next_inventory_at,omitempty"`
	AdmissionRevision string    `json:"admission_revision,omitempty"`
}

func (r KnowledgeIndexerRuntime) AutomaticReady(ctx context.Context, instance workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	if r.Knowledge == nil {
		return false, nil, fmt.Errorf("Notes indexer unavailable")
	}
	pendingAt, err := r.Knowledge.PendingSourceAdmissionAt(ctx)
	if err != nil {
		return false, nil, err
	}
	if pendingAt != nil && !pendingAt.After(now) {
		return true, pendingAt, nil
	}
	custodyReady, custodyNext, err := r.custodyReady(ctx, instance, now)
	if err != nil || custodyReady {
		return custodyReady, custodyNext, err
	}
	var raw []byte
	err = r.Knowledge.Store().DB().QueryRowContext(ctx, `SELECT checkpoint_json FROM workers.worker_checkpoints WHERE worker_instance_id=$1 AND checkpoint_key='admission_schedule'`, instance.WorkerInstanceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	var sweep knowledgeAdmissionSweepCheckpoint
	if err = json.Unmarshal(raw, &sweep); err != nil {
		return false, nil, err
	}
	if !sweep.Complete || !sweep.NextInventoryAt.After(now) {
		return true, nil, nil
	}
	next, err := r.Knowledge.NextPipelineWorkAt(ctx, knowledge.PipelineExecutionCoordinator, now)
	if err != nil {
		return false, nil, err
	}
	if next != nil && !next.After(now) {
		return true, next, nil
	}
	changed, err := r.Knowledge.NotesAdmissionChanged(ctx, sweep.SourceGeneration, sweep.AdmissionRevision)
	if err != nil || changed {
		return changed, nil, err
	}
	if next == nil || sweep.NextInventoryAt.Before(*next) {
		next = &sweep.NextInventoryAt
	}
	if custodyNext != nil && (next == nil || custodyNext.Before(*next)) {
		next = custodyNext
	}
	if pendingAt != nil && (next == nil || pendingAt.Before(*next)) {
		next = pendingAt
	}
	return false, next, nil
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
	return json.RawMessage(`{"schema_version":"knowledge_indexer.config.v0.8.5","batch_size":200,"max_runtime_seconds":30,"max_objects_per_run":200,"max_text_bytes_per_object":5242880,"max_extracted_text_bytes":10485760,"max_chunks_per_object":2048,"lease_duration_seconds":120,"retry":{"max_attempts":3,"base_delay_seconds":60,"max_delay_seconds":3600}}`)
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
	savedAdmission, existingInventory := run.Checkpoints["admission"]
	if existingInventory {
		saved := savedAdmission
		if err := json.Unmarshal(saved.CheckpointJSON, &cursor); err != nil {
			return workers.RunResult{}, fmt.Errorf("invalid Notes admission checkpoint: %w", err)
		}
	}
	var sweep knowledgeAdmissionSweepCheckpoint
	if saved, ok := run.Checkpoints["admission_schedule"]; ok {
		if err := json.Unmarshal(saved.CheckpointJSON, &sweep); err != nil {
			return workers.RunResult{}, fmt.Errorf("invalid Notes admission schedule checkpoint")
		}
	}
	// Finish a legacy in-flight cursor before adopting scoped checkpoints.
	if cursor.Incremental || cursor.StorageAfter == "" && cursor.SyncedAfter == [3]string{} && !cursor.StorageDone && !cursor.SyncedDone {
		cursor.Incremental = true
		if cursor.RootCursors == nil && sweep.Complete {
			cursor.BaselineGeneration = sweep.SourceGeneration
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(config.MaxRuntimeSeconds)*time.Second)
	defer cancel()
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	limit := config.BatchSize
	admissionRevision, err := r.Knowledge.NotesAdmissionRevision(ctx)
	if err != nil {
		return workers.RunResult{}, err
	}
	if config.MaxObjectsPerRun > 0 && config.MaxObjectsPerRun < limit {
		limit = config.MaxObjectsPerRun
	}
	admissionLimit, err := knowledgeAdmissionPageLimit(limit, run.Checkpoints)
	if err != nil {
		return workers.RunResult{}, err
	}
	custodySchedule, err := r.nextCustodySchedule(ctx, run.Checkpoints)
	if err != nil {
		return workers.RunResult{}, err
	}
	var custody *knowledge.NotesCustodyBatchResult
	if custodySchedule.due(now) {
		custody, err = r.consumeCustody(ctx, run.Checkpoints, limit)
	}
	if err != nil {
		return workers.RunResult{}, err
	}
	admissionStarted := time.Now()
	admission, admissionTimedOut, err := admitKnowledgeNotes(ctx, cursor, admissionLimit,
		time.Duration(config.MaxRuntimeSeconds)*time.Second/3, r.Knowledge.AdmitRegisteredNotesInteractiveOnce)
	admissionElapsed := time.Since(admissionStarted)
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("admit registered Notes: %w", err)
	}
	if admissionTimedOut && run.Logger != nil {
		if warning := admission.Warning; warning != nil {
			run.Logger.Warn("Notes admission timed out; retaining cursor and processing existing work",
				"phase", warning.Phase, "reason", warning.Reason, "elapsed_ms", warning.ElapsedMS,
				"budget_ms", warning.BudgetMS, "retryable", warning.Retryable,
				"worker_run_id", run.Run.WorkerRunID, "correlation_id", run.CorrelationID)
		} else {
			run.Logger.Warn("Notes admission timed out; phase unavailable; retaining cursor and processing existing work",
				"worker_run_id", run.Run.WorkerRunID, "correlation_id", run.CorrelationID)
		}
	}
	// Reserve time to publish the generated view and checkpoints after a drain.
	coordinatorCtx := ctx
	if deadline, ok := ctx.Deadline(); ok && r.Projection != nil {
		reserve := min(8*time.Second, time.Duration(config.MaxRuntimeSeconds)*time.Second/3)
		var stopCoordinator context.CancelFunc
		coordinatorCtx, stopCoordinator = context.WithDeadline(ctx, deadline.Add(-reserve))
		defer stopCoordinator()
	}
	result, err := r.Knowledge.RunKnowledgeIndexerOnce(coordinatorCtx, knowledge.KnowledgeIndexerRunInput{
		PreferredRunID:        admission.PreferredPipelineRunID,
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
	var projectionCheckpoint projectionRefreshCheckpoint
	if saved, ok := run.Checkpoints["projection_refresh"]; ok {
		if err := json.Unmarshal(saved.CheckpointJSON, &projectionCheckpoint); err != nil {
			return workers.RunResult{}, fmt.Errorf("invalid Notes projection refresh checkpoint: %w", err)
		}
	}
	if r.Projection != nil {
		var refreshed bool
		projectionChanged, refreshed, err = r.Projection.RefreshCoalesced(ctx, projectionCheckpoint.LastSuccess)
		if err != nil {
			return workers.RunResult{}, fmt.Errorf("refresh generated Notes projection: %w", err)
		}
		if refreshed {
			projectionCheckpoint.LastSuccess = time.Now().UTC()
		}
	}
	pipelineMore := result.MoreWork
	sweep = knowledgeAdmissionUpgradeBaseline(sweep, admission, existingInventory, admissionTimedOut)
	sweep, admissionFast, continuationReason := knowledgeAdmissionContinuation(cursor, sweep, admission, admissionTimedOut)
	if !admissionTimedOut && !admission.InventoryDeferred {
		sweep.AdmissionRevision = admissionRevision
		sweep.NextInventoryAt = admission.Cursor.NextRepairAt
		if sweep.NextInventoryAt.IsZero() {
			sweep.NextInventoryAt = now.Add(6 * time.Hour)
		}
		if admissionFast || admission.PriorityMoreWork || custody != nil && !custody.Wrapped {
			sweep.NextInventoryAt = now
		} else if admission.InventoryMoreWork {
			interval := tickPolicy.IntervalSeconds
			if interval <= 0 {
				interval = 60
			}
			sweep.NextInventoryAt = now.Add(time.Duration(interval) * time.Second)
		}
	}
	if pipelineMore {
		continuationReason = "pipeline_backlog"
	}
	result.MoreWork = result.MoreWork || admission.MoreWork
	if custody != nil {
		result.MoreWork = result.MoreWork || !custody.Wrapped
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
		"admission_timed_out":      admissionTimedOut,
		"admission_warning":        admission.Warning,
		"admission_page_limit":     admissionLimit,
		"admission_elapsed_ms":     admissionElapsed.Milliseconds(),
		"admission_budget_ms":      int64(config.MaxRuntimeSeconds) * 1000 / 3,
		"admission_success_streak": knowledgeAdmissionSuccessStreak(admissionLimit, admissionTimedOut, run.Checkpoints),
		"projection_changed":       projectionChanged,
		"continuation_reason":      continuationReason,
	})
	output := workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: mustWorkerJSON(result),
		Counters: map[string]int64{
			"admission_deferred_for_pipeline": boolCounter(admission.InventoryDeferred),
			"catalog_observed":                int64(admission.CatalogObserved),
			"synced_observed":                 int64(admission.SyncedObserved),
			"priority_synced_observed":        int64(admission.PrioritySyncedObserved),
			"priority_admission_applied":      int64(admission.PriorityApplied),
			"admission_applied":               int64(admission.Applied),
			"admission_skipped":               int64(admission.Skipped),
			"released_expired":                result.ReleasedExpired,
			"claimed":                         result.Claimed,
			"processed":                       result.Processed,
			"skipped_unsupported":             result.SkippedUnsupported,
			"extracted":                       result.Extracted,
			"metadata_only":                   result.MetadataOnly,
			"too_large":                       result.TooLarge,
			"password_required":               result.PasswordRequired,
			"no_embedded_text":                result.NoEmbeddedText,
			"source_unavailable":              result.SourceUnavailable,
			"retry_scheduled":                 result.RetryScheduled,
			"failed_terminal":                 result.FailedTerminal,
			"text_bytes_read":                 result.TextBytesRead,
			"chunks_created":                  result.ChunksCreated,
			"links_created":                   result.LinksCreated,
			"statuses_written":                result.StatusesWritten,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: append(knowledgeAdmissionCheckpoints(admission.Cursor, sweep, admissionTimedOut),
			workers.CheckpointUpdate{Key: "projection_refresh", SchemaVersion: "notes_projection.refresh.v1", Value: mustWorkerJSON(projectionCheckpoint), Metadata: json.RawMessage(`{}`)},
			workers.CheckpointUpdate{
				Key:           "default",
				SchemaVersion: "knowledge_indexer.checkpoint.v0.8",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		),
		NextRunAfter: knowledgeIndexerNextRun(tickPolicy, pipelineMore, admissionFast || custody != nil && !custody.Wrapped, time.Now().UTC()),
		Retryable:    false,
	}
	if admissionTimedOut {
		output.Counters["admission_timeouts"] = 1
		// Processing succeeded, but admission is incomplete and must be retried.
		output.Retryable = true
	}
	output.Counters["admission_page_limit"] = int64(admissionLimit)
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
		output.CheckpointUpdates = append(output.CheckpointUpdates, workers.CheckpointUpdate{
			Key: "custody", SchemaVersion: notesCustodyCheckpointSchema,
			Value:    mustWorkerJSON(notesCustodyCheckpoint{SchemaVersion: notesCustodyCheckpointSchema, EventID: custody.NextCursor}),
			Metadata: json.RawMessage(`{}`),
		})
		var start notesCustodyCheckpoint
		if saved, ok := run.Checkpoints["custody"]; ok {
			if err := json.Unmarshal(saved.CheckpointJSON, &start); err != nil {
				return workers.RunResult{}, err
			}
		}
		custodySchedule = custodySchedule.afterBatch(start.EventID, custody.Wrapped, now)
		output.CheckpointUpdates = append(output.CheckpointUpdates, workers.CheckpointUpdate{
			Key: "custody_schedule", SchemaVersion: "notes_custody.schedule.v1",
			Value: mustWorkerJSON(custodySchedule), Metadata: json.RawMessage(`{}`),
		})
		if custodySchedule.Pending {
			next := now
			output.NextRunAfter = &next
		}
	}
	output.ResultSummary = knowledgeIndexerResultSummary(result, admissionTimedOut, admissionLimit, admission, continuationReason, custody)
	return output, nil
}

func knowledgeIndexerResultSummary(result knowledge.KnowledgeIndexerRunResult, admissionTimedOut bool, admissionLimit int,
	admission knowledge.AdmissionResult, continuationReason string, custody *knowledge.NotesCustodyBatchResult,
) json.RawMessage {
	return mustWorkerJSON(struct {
		knowledge.KnowledgeIndexerRunResult
		AdmissionTimedOut  bool                               `json:"admission_timed_out"`
		AdmissionWarning   *knowledge.AdmissionPhaseWarning   `json:"admission_warning,omitempty"`
		AdmissionPageLimit int                                `json:"admission_page_limit"`
		Admission          knowledge.AdmissionResult          `json:"admission"`
		ContinuationReason string                             `json:"continuation_reason"`
		Custody            *knowledge.NotesCustodyBatchResult `json:"custody,omitempty"`
		Warnings           []*knowledge.AdmissionPhaseWarning `json:"warnings,omitempty"`
	}{result, admissionTimedOut, admission.Warning, admissionLimit, admission, continuationReason, custody, knowledgeAdmissionWarnings(admission)})
}

func knowledgeAdmissionWarnings(admission knowledge.AdmissionResult) []*knowledge.AdmissionPhaseWarning {
	if admission.Warning == nil {
		return nil
	}
	return []*knowledge.AdmissionPhaseWarning{admission.Warning}
}

func knowledgeAdmissionPageLimit(limit int, checkpoints map[string]workers.WorkerCheckpoint) (int, error) {
	if saved, ok := checkpoints["default"]; ok {
		var previous struct {
			Limit     int   `json:"admission_page_limit"`
			TimedOut  bool  `json:"admission_timed_out"`
			Successes int   `json:"admission_success_streak"`
			ElapsedMS int64 `json:"admission_elapsed_ms"`
			BudgetMS  int64 `json:"admission_budget_ms"`
		}
		if err := json.Unmarshal(saved.CheckpointJSON, &previous); err != nil {
			return 0, fmt.Errorf("invalid Notes indexer checkpoint")
		}
		if previous.Limit > 0 {
			if previous.TimedOut {
				limit = max(1, min(limit, previous.Limit)/2)
			} else if previous.Successes >= 3 && previous.BudgetMS > 0 && previous.ElapsedMS*2 < previous.BudgetMS {
				limit = min(limit, max(previous.Limit+1, previous.Limit*2))
			} else {
				limit = min(limit, previous.Limit)
			}
		} else if previous.TimedOut {
			limit = max(1, limit/2)
		}
	}
	return limit, nil
}

func knowledgeAdmissionSuccessStreak(limit int, timedOut bool, checkpoints map[string]workers.WorkerCheckpoint) int {
	if timedOut {
		return 0
	}
	var previous struct {
		Limit     int `json:"admission_page_limit"`
		Successes int `json:"admission_success_streak"`
	}
	if saved, ok := checkpoints["default"]; ok {
		_ = json.Unmarshal(saved.CheckpointJSON, &previous)
	}
	if previous.Limit < limit {
		return 1
	}
	return min(3, previous.Successes+1)
}

func admitKnowledgeNotes(ctx context.Context, cursor knowledge.AdmissionCursor, limit int, budget time.Duration,
	admit func(context.Context, knowledge.AdmissionCursor, int) (knowledge.AdmissionResult, error),
) (knowledge.AdmissionResult, bool, error) {
	admissionCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	result, err := admit(admissionCtx, cursor, limit)
	if ctx.Err() != nil {
		if errors.Is(err, ctx.Err()) {
			return knowledge.AdmissionResult{}, false, err
		}
		return knowledge.AdmissionResult{}, false, ctx.Err()
	}
	if err != nil && errors.Is(admissionCtx.Err(), context.DeadlineExceeded) {
		// Authority reconciliation is committed before this stamp is returned.
		// Inventory/priority positions may still be tentative and must replay.
		if result.Cursor.RootConfigRevision != "" {
			cursor.RootConfigRevision = result.Cursor.RootConfigRevision
			cursor.NextRootReconcileAt = result.Cursor.NextRootReconcileAt
		}
		result.Cursor, result.MoreWork = cursor, true
		var phaseErr *knowledge.AdmissionPhaseError
		if errors.As(err, &phaseErr) {
			result.Warning = phaseErr.Warning()
		}
		return result, true, nil
	}
	return result, false, err
}

func knowledgeAdmissionCheckpoints(cursor knowledge.AdmissionCursor, sweep knowledgeAdmissionSweepCheckpoint, timedOut bool) []workers.CheckpointUpdate {
	updates := []workers.CheckpointUpdate{
		{Key: "admission", SchemaVersion: "knowledge_admission.cursor.v1", Value: mustWorkerJSON(cursor), Metadata: json.RawMessage(`{}`)},
	}
	if !timedOut {
		updates = append(updates, workers.CheckpointUpdate{
			Key: "admission_schedule", SchemaVersion: "knowledge_admission.schedule.v1", Value: mustWorkerJSON(sweep), Metadata: json.RawMessage(`{}`),
		})
	}
	return updates
}

func knowledgeAdmissionContinuation(cursor knowledge.AdmissionCursor, previous knowledgeAdmissionSweepCheckpoint,
	admission knowledge.AdmissionResult, timedOut bool,
) (knowledgeAdmissionSweepCheckpoint, bool, string) {
	if timedOut {
		return previous, false, "admission_timeout"
	}
	if admission.InventoryDeferred {
		return previous, true, "pipeline_priority"
	}
	if admission.Cursor.Incremental {
		initial := !previous.Complete || previous.SourceGeneration != admission.SourceGeneration
		previous.SourceGeneration, previous.SweepStarted = admission.SourceGeneration, true
		if initial {
			previous.Complete = !admission.InventoryMoreWork
		}
		if admission.PriorityMoreWork && admission.PriorityApplied > 0 {
			return previous, true, "priority_delivery"
		}
		if admission.InventoryMoreWork {
			if initial {
				return previous, true, "scoped_inventory"
			}
			return previous, false, "inventory_reconcile"
		}
		return previous, false, "idle"
	}
	if previous.SourceGeneration != admission.SourceGeneration {
		previous = knowledgeAdmissionSweepCheckpoint{SourceGeneration: admission.SourceGeneration}
	}
	initialSweep := !previous.Complete
	if initialSweep {
		// A generation discovered mid-sweep must also see the beginning of the
		// inventory. Finish the committed cursor, then take one full fast pass.
		if !cursor.StorageDone && !cursor.SyncedDone && cursor.StorageAfter == "" && cursor.SyncedAfter == [3]string{} {
			previous.SweepStarted = true
		}
		if !admission.InventoryMoreWork && previous.SweepStarted {
			previous.Complete = true
		}
	}
	if admission.PriorityMoreWork && admission.PriorityApplied > 0 {
		return previous, true, "priority_delivery"
	}
	if initialSweep && admission.InventoryMoreWork {
		return previous, true, "initial_inventory"
	}
	if admission.InventoryMoreWork {
		return previous, false, "inventory_reconcile"
	}
	return previous, false, "idle"
}

func knowledgeAdmissionUpgradeBaseline(previous knowledgeAdmissionSweepCheckpoint, admission knowledge.AdmissionResult,
	existingInventory, timedOut bool,
) knowledgeAdmissionSweepCheckpoint {
	// Existing cursor owners already perform continuous reconciliation. Installing
	// the new cadence must not manufacture another initial import of that inventory.
	// This checkpoint controls cadence, not source acceptance or coverage truth.
	if previous.SourceGeneration == "" && existingInventory && !timedOut && !admission.InventoryDeferred {
		return knowledgeAdmissionSweepCheckpoint{SourceGeneration: admission.SourceGeneration, SweepStarted: true, Complete: true}
	}
	return previous
}

func knowledgeIndexerNextRun(policy workers.TickPolicy, pipelineMore, admissionFast bool, now time.Time) *time.Time {
	if (pipelineMore || admissionFast) && policy.Mode == workers.TickModeInterval && policy.IntervalSeconds > workers.MinimumIntervalSeconds {
		next := now.Add(time.Duration(workers.MinimumIntervalSeconds) * time.Second)
		return &next
	}
	return policy.NextAfter(now)
}

func parseKnowledgeIndexerConfig(raw json.RawMessage) (knowledgeIndexerConfig, error) {
	normalized, err := workers.JSONObject(raw, "config_json")
	if err != nil {
		return knowledgeIndexerConfig{}, err
	}
	config := knowledgeIndexerConfig{
		SchemaVersion:         "knowledge_indexer.config.v0.8",
		BatchSize:             200,
		MaxRuntimeSeconds:     30,
		MaxObjectsPerRun:      200,
		MaxTextBytesPerObject: 5 * 1024 * 1024,
		MaxExtractedTextBytes: 10485760,
		MaxChunksPerObject:    2048,
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
		config.BatchSize = 200
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
		config.MaxChunksPerObject = 2048
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
