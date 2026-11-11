package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

// Periodic source repair must not occupy the serial note-publication lane.
type KnowledgeRootsRuntime struct {
	Reconcile func(context.Context, knowledge.SourceRootReconcileInput) (knowledge.SourceRootReconcileResult, error)
}

func NewKnowledgeRootsRuntime(service *knowledge.Service) KnowledgeRootsRuntime {
	r := KnowledgeRootsRuntime{}
	if service != nil {
		r.Reconcile = service.ReconcileSourceRoots
	}
	return r
}

func (r KnowledgeRootsRuntime) Kind() string { return workers.KindKnowledgeRoots }

func (r KnowledgeRootsRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"knowledge_roots.config.v1"}`)
}

func (r KnowledgeRootsRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{WorkerKind: r.Kind(), DisplayName: "Notes source-root repair",
		Description:  "Repairs source-root metadata independently of fresh Notes publication.",
		RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.knowledge", Status: workers.KindStatusActive,
		SupportedLocalities: []string{workers.LocalityMainOwned}, MayTouchFilesystem: false,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":900,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_roots.config_schema.v1","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"knowledge_roots.checkpoint_schema.v1","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"knowledge_roots.result_schema.v1","type":"object"}`),
		Metadata:                     json.RawMessage(`{"builtin":true,"role":"notes_periodic_root_repair"}`)}
}

func (r KnowledgeRootsRuntime) DefaultInstances() []workers.InstanceDescriptor {
	d := r.Describe()
	return []workers.InstanceDescriptor{{WorkerKey: "main.knowledge_root_reconciler", WorkerKind: r.Kind(),
		DisplayName: d.DisplayName, Description: d.Description, Locality: workers.LocalityMainOwned,
		ConfigJSON: r.DefaultConfig(), TickPolicyJSON: d.DefaultTickPolicyJSON, ConcurrencyJSON: d.DefaultConcurrencyPolicyJSON,
		RetryPolicyJSON: d.DefaultRetryPolicyJSON, TimeoutPolicyJSON: d.DefaultTimeoutPolicyJSON,
		ResourceLimitsJSON: d.DefaultResourceLimitsJSON, VisibilityJSON: json.RawMessage(`{}`),
		Metadata: json.RawMessage(`{"seeded_by":"workers.SeedBuiltins"}`)}}
}

func (r KnowledgeRootsRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	var config struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(raw, &config); err != nil || config.SchemaVersion != "knowledge_roots.config.v1" {
		return fmt.Errorf("invalid Notes root-repair config")
	}
	return nil
}

func (r KnowledgeRootsRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Reconcile == nil {
		return workers.RunResult{}, fmt.Errorf("Notes root repair is not configured")
	}
	if err := r.ValidateConfig(ctx, run.Instance.ConfigJSON); err != nil {
		return workers.RunResult{}, err
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	started := time.Now()
	result, err := r.Reconcile(ctx, knowledge.SourceRootReconcileInput{DiscoverFromStore: true})
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("reconcile Notes roots: %w", err)
	}
	finished := time.Now().UTC()
	summary := mustWorkerJSON(map[string]any{"schema_version": "knowledge_roots.result.v1", "applied": result.Applied,
		"skipped": len(result.Skipped), "elapsed_ms": finished.Sub(started).Milliseconds()})
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: summary,
		Counters:      map[string]int64{"roots_applied": int64(result.Applied), "roots_skipped": int64(len(result.Skipped))},
		ResourceUsage: json.RawMessage(`{}`), NextRunAfter: policy.NextAfter(finished),
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: "knowledge_roots.checkpoint.v1",
			Value:    mustWorkerJSON(map[string]any{"schema_version": "knowledge_roots.checkpoint.v1", "last_success_at": finished, "run_id": run.Run.WorkerRunID}),
			Metadata: json.RawMessage(`{}`)}}}, nil
}
