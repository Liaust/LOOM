package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/workers"
)

type RealtimeExpiryRuntime struct {
	Realtime realtime.Service
	Now      func() time.Time
}

func NewRealtimeExpiryRuntime(realtimeService realtime.Service) RealtimeExpiryRuntime {
	return RealtimeExpiryRuntime{Realtime: realtimeService}
}

func (r RealtimeExpiryRuntime) Kind() string {
	return workers.KindRealtimeExpiry
}

func (r RealtimeExpiryRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindRealtimeExpiry,
		DisplayName:                  "Realtime expiry",
		Description:                  "Expires stale realtime notifications, leases, subscriptions, presence, and progress feeds.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.realtime",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":10}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"realtime_expiry.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"realtime_expiry.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"realtime_expiry.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r RealtimeExpiryRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"realtime_expiry.config.v0.2","max_runtime_ms":5000}`)
}

func (r RealtimeExpiryRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.realtime_expiry",
			WorkerKind:         workers.KindRealtimeExpiry,
			DisplayName:        "Realtime expiry",
			Description:        "Expires stale realtime records.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":10}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r RealtimeExpiryRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := workers.JSONObject(config, "config_json")
	return err
}

func (r RealtimeExpiryRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Realtime.DB == nil {
		return workers.RunResult{}, fmt.Errorf("realtime expiry database is not configured")
	}

	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	expiration, err := realtime.NewExpiryWorker(r.Realtime, run.Logger).RunOnce(ctx)
	if err != nil {
		return workers.RunResult{}, err
	}

	counters := realtimeExpiryCounters(expiration)
	resultSummary, err := realtimeExpirySummary(expiration)
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpoint, err := realtimeExpiryCheckpoint(now, run.Run.WorkerRunID, expiration)
	if err != nil {
		return workers.RunResult{}, err
	}
	tickPolicy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}

	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters:      counters,
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "realtime_expiry.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		NextRunAfter: tickPolicy.NextAfter(now),
		Retryable:    false,
	}, nil
}

func realtimeExpiryCounters(result realtime.ExpiryResult) map[string]int64 {
	total := result.NotificationsExpired + result.LeasesExpired + result.SubscriptionsExpired + result.PresenceMarkedStale + result.ProgressFeedsClosed
	return map[string]int64{
		"notifications_expired": int64(result.NotificationsExpired),
		"leases_expired":        int64(result.LeasesExpired),
		"subscriptions_expired": int64(result.SubscriptionsExpired),
		"presence_marked_stale": int64(result.PresenceMarkedStale),
		"progress_feeds_closed": int64(result.ProgressFeedsClosed),
		"total_transitions":     int64(total),
	}
}

func realtimeExpirySummary(result realtime.ExpiryResult) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":        "realtime_expiry.result.v0.2",
		"status":                "ok",
		"notifications_expired": result.NotificationsExpired,
		"leases_expired":        result.LeasesExpired,
		"subscriptions_expired": result.SubscriptionsExpired,
		"presence_marked_stale": result.PresenceMarkedStale,
		"progress_feeds_closed": result.ProgressFeedsClosed,
		"total_transitions":     result.NotificationsExpired + result.LeasesExpired + result.SubscriptionsExpired + result.PresenceMarkedStale + result.ProgressFeedsClosed,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal realtime expiry summary: %w", err)
	}
	return workers.JSONObject(raw, "result_summary")
}

func realtimeExpiryCheckpoint(now time.Time, runID string, result realtime.ExpiryResult) (json.RawMessage, error) {
	raw, err := json.Marshal(map[string]any{
		"schema_version":        "realtime_expiry.checkpoint.v0.2",
		"last_realtime_expiry":  now.Format(time.RFC3339Nano),
		"last_run_id":           runID,
		"notifications_expired": result.NotificationsExpired,
		"leases_expired":        result.LeasesExpired,
		"subscriptions_expired": result.SubscriptionsExpired,
		"presence_marked_stale": result.PresenceMarkedStale,
		"progress_feeds_closed": result.ProgressFeedsClosed,
		"total_transitions":     result.NotificationsExpired + result.LeasesExpired + result.SubscriptionsExpired + result.PresenceMarkedStale + result.ProgressFeedsClosed,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal realtime expiry checkpoint: %w", err)
	}
	return workers.JSONObject(raw, "checkpoint")
}
