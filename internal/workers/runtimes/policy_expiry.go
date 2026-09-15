package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/workers"
)

type PolicyExpiryRuntime struct {
	Policy policy.Service
	Now    func() time.Time
}

func NewPolicyExpiryRuntime(policyService policy.Service) PolicyExpiryRuntime {
	return PolicyExpiryRuntime{Policy: policyService}
}

func (r PolicyExpiryRuntime) Kind() string {
	return workers.KindPolicyExpiry
}

func (r PolicyExpiryRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindPolicyExpiry,
		DisplayName:                  "Policy expiry",
		Description:                  "Expires stale approvals and grants.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.policy",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":10}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"policy_expiry.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"policy_expiry.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"policy_expiry.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r PolicyExpiryRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"policy_expiry.config.v0.2","batch_size":500,"max_runtime_ms":5000,"emit_operation_record":false}`)
}

func (r PolicyExpiryRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.policy_expiry",
			WorkerKind:         workers.KindPolicyExpiry,
			DisplayName:        "Policy expiry",
			Description:        "Expires stale approvals and grants.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":300,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":10}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r PolicyExpiryRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := workers.JSONObject(config, "config_json")
	return err
}

func (r PolicyExpiryRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Policy.DB == nil {
		return workers.RunResult{}, fmt.Errorf("policy expiry database is not configured")
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	expiration, err := r.Policy.ExpireStale(ctx, now)
	if err != nil {
		return workers.RunResult{}, err
	}

	counters := policyExpiryCounters(expiration)
	resultSummary, err := policyExpirySummary(expiration)
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpoint, err := policyExpiryCheckpoint(now, run.Run.WorkerRunID, expiration)
	if err != nil {
		return workers.RunResult{}, err
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	nextRunAfter := tickPolicy.NextAfter(now)

	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters:      counters,
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "policy_expiry.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: nextRunAfter,
		Retryable:    false,
	}, nil
}

func policyExpiryCounters(result policy.ExpirationResult) map[string]int64 {
	return map[string]int64{
		"approvals_expired": int64(result.ApprovalsExpired),
		"grants_expired":    int64(result.GrantsExpired),
		"total_expired":     int64(result.ApprovalsExpired + result.GrantsExpired),
	}
}

func policyExpirySummary(result policy.ExpirationResult) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":     "policy_expiry.result.v0.2",
		"status":             "ok",
		"approvals_expired":  result.ApprovalsExpired,
		"grants_expired":     result.GrantsExpired,
		"total_expired":      result.ApprovalsExpired + result.GrantsExpired,
		"operation_recorded": false,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal policy expiry summary: %w", err)
	}
	return workers.JSONObject(raw, "result_summary")
}

func policyExpiryCheckpoint(now time.Time, runID string, result policy.ExpirationResult) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":     "policy_expiry.checkpoint.v0.2",
		"last_policy_expiry": now.Format(time.RFC3339Nano),
		"last_run_id":        runID,
		"approvals_expired":  result.ApprovalsExpired,
		"grants_expired":     result.GrantsExpired,
		"total_expired":      result.ApprovalsExpired + result.GrantsExpired,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal policy expiry checkpoint: %w", err)
	}
	return workers.JSONObject(raw, "checkpoint")
}
