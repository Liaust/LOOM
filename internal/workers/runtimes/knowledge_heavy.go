package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

type KnowledgeHeavyRuntime struct {
	Knowledge *knowledge.Service
	Vision    knowledge.VisionRuntime
	Embedding knowledge.EmbeddingRuntime
	Now       func() time.Time
}

type knowledgeHeavyConfig struct {
	SchemaVersion        string                        `json:"schema_version"`
	LeaseDurationSeconds int                           `json:"lease_duration_seconds"`
	Resource             knowledge.HeavyResourcePolicy `json:"resource"`
}

func NewKnowledgeHeavyRuntime(service *knowledge.Service, vision ...knowledge.VisionRuntime) KnowledgeHeavyRuntime {
	runtime := KnowledgeHeavyRuntime{Knowledge: service}
	if len(vision) > 0 {
		runtime.Vision = vision[0]
	}
	return runtime
}

func NewKnowledgeHeavyRuntimeWithRuntimes(service *knowledge.Service, vision knowledge.VisionRuntime, embedding knowledge.EmbeddingRuntime) KnowledgeHeavyRuntime {
	return KnowledgeHeavyRuntime{Knowledge: service, Vision: vision, Embedding: embedding}
}
func (r KnowledgeHeavyRuntime) Kind() string { return workers.KindKnowledgeHeavy }
func (r KnowledgeHeavyRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{WorkerKind: workers.KindKnowledgeHeavy, DisplayName: "Knowledge heavy executor", Description: "Runs OCR, image-description, and embedding stages under one global resource lease.", RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.knowledge", Status: workers.KindStatusActive, SupportedLocalities: []string{workers.LocalityMainOwned}, MayTouchFilesystem: true, DefaultTickPolicyJSON: json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":30,"run_on_startup":true}`), DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`), DefaultRetryPolicyJSON: json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`), DefaultTimeoutPolicyJSON: json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":660}`), DefaultResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v1","resource_key":"knowledge_heavy","capacity":1}`), ConfigSchemaJSON: json.RawMessage(`{"schema_version":"knowledge_heavy.config_schema.v1","type":"object"}`), CheckpointSchemaJSON: json.RawMessage(`{"schema_version":"knowledge_heavy.checkpoint_schema.v1","type":"object"}`), ResultSchemaJSON: json.RawMessage(`{"schema_version":"knowledge_heavy.result_schema.v1","type":"object"}`), Metadata: json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`)}
}
func (r KnowledgeHeavyRuntime) DefaultConfig() json.RawMessage {
	payload, _ := json.Marshal(knowledgeHeavyConfig{SchemaVersion: "knowledge_heavy.config.v1", LeaseDurationSeconds: 600, Resource: knowledge.DefaultHeavyResourcePolicy()})
	return payload
}
func (r KnowledgeHeavyRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{{WorkerKey: "main.knowledge_heavy", WorkerKind: workers.KindKnowledgeHeavy, DisplayName: "Knowledge heavy executor", Description: "Serializes all heavy Notes AI stages.", Locality: workers.LocalityMainOwned, ConfigJSON: r.DefaultConfig(), TickPolicyJSON: json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":30,"run_on_startup":true}`), ConcurrencyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`), RetryPolicyJSON: json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`), TimeoutPolicyJSON: json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":660}`), ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v1","resource_key":"knowledge_heavy","capacity":1}`), VisibilityJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`)}}
}
func (r KnowledgeHeavyRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	_, err := parseKnowledgeHeavyConfig(raw)
	return err
}
func (r KnowledgeHeavyRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Knowledge == nil || r.Knowledge.Store().DB() == nil {
		return workers.RunResult{}, fmt.Errorf("knowledge heavy service is not configured")
	}
	config, err := parseKnowledgeHeavyConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	runID, instanceID := run.Run.WorkerRunID, run.Instance.WorkerInstanceID
	items, err := r.Knowledge.ClaimPipelineRuns(ctx, knowledge.PipelineExecutionHeavy, runID, knowledge.PipelineClaimOptions{Limit: 1, LeaseDuration: time.Duration(config.LeaseDurationSeconds) * time.Second, Now: now})
	if err != nil {
		return workers.RunResult{}, err
	}
	if len(items) == 0 {
		return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: json.RawMessage(`{"claimed":0}`), Counters: map[string]int64{"claimed": 0}, ResourceUsage: json.RawMessage(`{"available":false}`)}, nil
	}
	item := items[0]
	pipelineRunID, stageRunID := item.Run.KnowledgePipelineRunID, item.Stage.KnowledgePipelineStageRunID
	lease, err := run.Service.AcquireResourceLease(ctx, workers.ResourceLeaseRequest{ResourceKey: workers.ResourceKnowledgeHeavy, HolderID: runID, WorkerInstanceID: &instanceID, WorkerRunID: &runID, KnowledgePipelineRunID: &pipelineRunID, KnowledgePipelineStageRunID: &stageRunID, TTL: time.Duration(config.LeaseDurationSeconds) * time.Second, Now: now})
	if err != nil {
		_ = r.Knowledge.ReleasePipelineClaim(ctx, item)
		return workers.RunResult{}, err
	}
	defer run.Service.ReleaseResourceLease(context.Background(), lease)
	handlers := map[string]knowledge.HeavyStageHandler{}
	if r.Vision != nil {
		handlers[knowledge.FilePipelineStageImageDescription] = knowledge.ImageDescriptionStageHandler{Service: r.Knowledge, Runtime: r.Vision}
	}
	if r.Embedding != nil {
		handlers[knowledge.FilePipelineStageEmbedding] = knowledge.EmbeddingStageHandler{Service: r.Knowledge, Runtime: r.Embedding}
	}
	result, err := r.Knowledge.ExecuteClaimedHeavyStage(ctx, item, config.Resource, handlers)
	if err != nil {
		return workers.RunResult{}, err
	}
	usage, _ := json.Marshal(result.Observation)
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: mustWorkerJSON(result), Counters: map[string]int64{"claimed": result.Claimed, "completed": result.Completed, "failed": result.Failed}, ResourceUsage: usage, Retryable: false}, nil
}

func parseKnowledgeHeavyConfig(raw json.RawMessage) (knowledgeHeavyConfig, error) {
	var config knowledgeHeavyConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, fmt.Errorf("invalid knowledge heavy config: %w", err)
	}
	if config.SchemaVersion == "" || config.LeaseDurationSeconds <= 0 {
		return config, fmt.Errorf("knowledge heavy schema_version and positive lease duration are required")
	}
	policy, err := knowledge.ParseHeavyResourcePolicy(mustWorkerJSON(config.Resource))
	if err != nil {
		return config, err
	}
	config.Resource = policy
	return config, nil
}
