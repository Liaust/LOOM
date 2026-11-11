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
	Pressure  func(time.Time) workers.ResourcePressureSnapshot
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

func (r KnowledgeHeavyRuntime) AutomaticReady(ctx context.Context, _ workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	next, err := r.Knowledge.NextPipelineWorkAt(ctx, knowledge.PipelineExecutionHeavy, now)
	if err != nil || next == nil || next.After(now) {
		return false, next, err
	}
	state, err := (workers.Service{DB: r.Knowledge.Store().DB()}).ResourcePressureDeadline(ctx, workers.ResourceKnowledgeHeavy)
	if err != nil {
		return false, nil, err
	}
	if state.BlockedUntil != nil && state.BlockedUntil.After(now) {
		return false, state.BlockedUntil, nil
	}
	if state.OptionalNotBefore != nil && state.OptionalNotBefore.After(now) {
		ordinary, err := r.Knowledge.NextOrdinaryPipelineWorkAt(ctx, now)
		if err != nil {
			return false, nil, err
		}
		if ordinary == nil || ordinary.After(now) {
			at := *state.OptionalNotBefore
			if ordinary != nil && ordinary.Before(at) {
				at = *ordinary
			}
			return false, &at, nil
		}
	}
	return true, next, nil
}
func (r KnowledgeHeavyRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Knowledge == nil || r.Knowledge.Store().DB() == nil {
		return workers.RunResult{}, fmt.Errorf("knowledge heavy service is not configured")
	}
	config, err := parseKnowledgeHeavyConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	return drainKnowledgeHeavy(ctx, config.Resource.Timeout(), policy, r.currentTime, func() (knowledge.HeavyExecutorRunResult, error) {
		return r.runStage(ctx, run, config)
	})
}

func (r KnowledgeHeavyRuntime) currentTime() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Each stage retains its full execution budget. The short drain window bounds
// admission of additional stages, not the execution time of a claimed stage.
func drainKnowledgeHeavy(ctx context.Context, stageBudget time.Duration, policy workers.TickPolicy, now func() time.Time, stage func() (knowledge.HeavyExecutorRunResult, error)) (workers.RunResult, error) {
	started := now()
	var total knowledge.HeavyExecutorRunResult
	stages := make([]knowledge.HeavyExecutorRunResult, 0, 8)
	mayHaveMore := false
	var pressureNext *time.Time
	var completedUnits, yielded, pressureSamples, pressureDeferred int64
	for len(stages) < 8 {
		if err := ctx.Err(); err != nil {
			return workers.RunResult{}, err
		}
		at := now()
		if len(stages) > 0 && at.Sub(started) >= 30*time.Second {
			break
		}
		if deadline, ok := ctx.Deadline(); ok && deadline.Sub(at) < stageBudget+5*time.Second {
			break
		}
		result, err := stage()
		if err != nil {
			return workers.RunResult{}, err
		}
		if a := result.Observation.Admission; a != nil {
			pressureSamples++
			if !a.Admitted || (!a.AllowOptional && result.Claimed == 0) {
				pressureDeferred++
				pressureNext = a.NextEligibleAt
			}
		}
		if result.Claimed == 0 {
			mayHaveMore = pressureNext != nil
			if result.Observation.Admission != nil {
				stages = append(stages, result)
			}
			break
		}
		var units struct {
			Units int64 `json:"units"`
		}
		_ = json.Unmarshal(result.Observation.Observed, &units)
		completedUnits += units.Units
		if result.Completed == 0 && result.Failed == 0 {
			yielded++
		}
		stages = append(stages, result)
		total.Claimed += result.Claimed
		total.Completed += result.Completed
		total.Failed += result.Failed
		mayHaveMore = result.Failed == 0
		if result.Failed > 0 {
			break
		}
	}
	summary := struct {
		knowledge.HeavyExecutorRunResult
		Stages   []knowledge.HeavyExecutorRunResult `json:"stages"`
		MoreWork bool                               `json:"more_work"`
	}{total, stages, mayHaveMore}
	next := policy.NextAfter(now())
	if pressureNext != nil && policy.Mode == workers.TickModeInterval {
		next = pressureNext
	} else if mayHaveMore && policy.Mode == workers.TickModeInterval && policy.IntervalSeconds > workers.MinimumIntervalSeconds {
		at := now().Add(time.Duration(workers.MinimumIntervalSeconds) * time.Second)
		next = &at
	}
	return workers.RunResult{
		Status: workers.RunStatusSucceeded, ResultSummary: mustWorkerJSON(summary),
		Counters:      map[string]int64{"claimed": total.Claimed, "completed": total.Completed, "failed": total.Failed, "completed_units": completedUnits, "yielded_turns": yielded, "pressure_samples": pressureSamples, "pressure_deferred": pressureDeferred},
		ResourceUsage: mustWorkerJSON(map[string]any{"stages": stages}),
		NextRunAfter:  next,
	}, nil
}

func (r KnowledgeHeavyRuntime) runStage(ctx context.Context, run workers.RunContext, config knowledgeHeavyConfig) (knowledge.HeavyExecutorRunResult, error) {
	now := r.currentTime()
	// Empty/future queues never read pressure, acquire a lease or call a model.
	ready, err := r.Knowledge.NextPipelineWorkAt(ctx, knowledge.PipelineExecutionHeavy, now)
	if err != nil || ready == nil || ready.After(now) {
		return knowledge.HeavyExecutorRunResult{}, err
	}
	sample := r.Pressure
	if sample == nil {
		sample = workers.SampleResourcePressure
	}
	admission, err := run.Service.AdmitResourceTurn(ctx, workers.ResourceKnowledgeHeavy, sample(now), config.Resource.MaxUnits, config.Resource.MaxTempBytes)
	if err != nil {
		return knowledge.HeavyExecutorRunResult{}, err
	}
	deferred := knowledge.HeavyExecutorRunResult{Observation: knowledge.HeavyStageObservation{Admission: &admission}}
	if !admission.Admitted {
		return deferred, nil
	}
	runID, instanceID := run.Run.WorkerRunID, run.Instance.WorkerInstanceID
	items, err := r.Knowledge.ClaimPipelineRuns(ctx, knowledge.PipelineExecutionHeavy, runID, knowledge.PipelineClaimOptions{OrdinaryOnly: !admission.AllowOptional, Limit: 1, LeaseDuration: time.Duration(config.LeaseDurationSeconds) * time.Second, Now: now})
	if err != nil {
		return knowledge.HeavyExecutorRunResult{}, err
	}
	if len(items) == 0 {
		return deferred, nil
	}
	item := items[0]
	pipelineRunID, stageRunID := item.Run.KnowledgePipelineRunID, item.Stage.KnowledgePipelineStageRunID
	lease, err := run.Service.AcquireResourceLease(ctx, workers.ResourceLeaseRequest{ResourceKey: workers.ResourceKnowledgeHeavy, HolderID: runID, WorkerInstanceID: &instanceID, WorkerRunID: &runID, KnowledgePipelineRunID: &pipelineRunID, KnowledgePipelineStageRunID: &stageRunID, TTL: time.Duration(config.LeaseDurationSeconds) * time.Second, Now: now})
	if err != nil {
		_ = r.Knowledge.ReleasePipelineClaim(ctx, item)
		return knowledge.HeavyExecutorRunResult{}, err
	}
	defer run.Service.ReleaseResourceLease(context.Background(), lease)
	handlers := map[string]knowledge.HeavyStageHandler{}
	if r.Vision != nil {
		handlers[knowledge.FilePipelineStageImageDescription] = knowledge.ImageDescriptionStageHandler{Service: r.Knowledge, Runtime: r.Vision}
	}
	if r.Embedding != nil {
		handlers[knowledge.FilePipelineStageEmbedding] = knowledge.EmbeddingStageHandler{Service: r.Knowledge, Runtime: r.Embedding}
	}
	resource := config.Resource
	resource.MaxUnits = admission.BudgetMaxUnits
	if !knowledge.IsOrdinaryPipelineWork(item) && admission.Reason == "sustained_pressure" {
		resource.MaxUnits = 1
		admission.BudgetMaxUnits = 1
		if err = run.Service.RecordOptionalResourceTurn(ctx, workers.ResourceKnowledgeHeavy, now); err != nil {
			_ = r.Knowledge.ReleasePipelineClaim(ctx, item)
			return knowledge.HeavyExecutorRunResult{}, err
		}
	}
	result, err := r.Knowledge.ExecuteClaimedHeavyStage(ctx, item, resource, handlers, &admission)
	result.Observation.Admission = &admission
	return result, err
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
