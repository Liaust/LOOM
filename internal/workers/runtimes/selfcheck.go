package runtimes

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/workers"
)

type SelfcheckRuntime struct {
	DB *sql.DB
}

func NewSelfcheckRuntime(db *sql.DB) SelfcheckRuntime {
	return SelfcheckRuntime{DB: db}
}

func (r SelfcheckRuntime) Kind() string {
	return workers.KindSelfcheck
}

func (r SelfcheckRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind:                   workers.KindSelfcheck,
		DisplayName:                  "Worker selfcheck",
		Description:                  "Verifies that the worker core can read and write durable worker state.",
		RuntimeOwner:                 workers.RuntimeOwnerLoomd,
		RuntimePackage:               "loom.core.workers",
		Status:                       workers.KindStatusActive,
		SupportedLocalities:          []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"worker_selfcheck.config_schema.v0.2","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"worker_selfcheck.checkpoint_schema.v0.2","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"worker_selfcheck.result_schema.v0.2","type":"object"}`),
		Metadata:                     json.RawMessage(`{"schema_version":"worker_kind.metadata.v0.2","builtin":true}`),
	}
}

func (r SelfcheckRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"worker_selfcheck.config.v0.2"}`)
}

func (r SelfcheckRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return []workers.InstanceDescriptor{
		{
			WorkerKey:          "main.worker_selfcheck",
			WorkerKind:         workers.KindSelfcheck,
			DisplayName:        "Worker selfcheck",
			Description:        "Verifies the worker core can read state and write durable status.",
			Locality:           workers.LocalityMainOwned,
			ConfigJSON:         r.DefaultConfig(),
			TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
			ConcurrencyJSON:    json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			RetryPolicyJSON:    json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			TimeoutPolicyJSON:  json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
			ResourceLimitsJSON: json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2"}`),
			VisibilityJSON:     json.RawMessage(`{}`),
			Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"workers.SeedBuiltins"}`),
		},
	}
}

func (r SelfcheckRuntime) ValidateConfig(ctx context.Context, config json.RawMessage) error {
	_, err := workers.JSONObject(config, "config_json")
	return err
}

func (r SelfcheckRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.DB == nil {
		return workers.RunResult{}, fmt.Errorf("selfcheck database is not configured")
	}

	var one int
	if err := r.DB.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		return workers.RunResult{}, fmt.Errorf("selfcheck database query failed: %w", err)
	}
	if one != 1 {
		return workers.RunResult{}, fmt.Errorf("selfcheck database query returned %d", one)
	}

	var found bool
	if err := r.DB.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM workers.worker_instances
			WHERE worker_instance_id = $1
		)
	`, run.Instance.WorkerInstanceID).Scan(&found); err != nil {
		return workers.RunResult{}, fmt.Errorf("selfcheck worker instance query failed: %w", err)
	}
	if !found {
		return workers.RunResult{}, fmt.Errorf("selfcheck worker instance %s was not found", run.Instance.WorkerInstanceID)
	}

	now := time.Now().UTC()
	resultSummary, err := workers.JSONObject(json.RawMessage(`{"schema_version":"worker_selfcheck.result.v0.2","status":"ok"}`), "result_summary")
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpoint, err := json.Marshal(map[string]any{
		"schema_version":     "worker_selfcheck.checkpoint.v0.2",
		"last_selfcheck_at":  now.Format(time.RFC3339Nano),
		"last_run_id":        run.Run.WorkerRunID,
		"worker_instance_id": run.Instance.WorkerInstanceID,
	})
	if err != nil {
		return workers.RunResult{}, fmt.Errorf("marshal selfcheck checkpoint: %w", err)
	}

	return workers.RunResult{
		Status:        workers.RunStatusSucceeded,
		ResultSummary: resultSummary,
		Counters: map[string]int64{
			"db_checks":       1,
			"instance_checks": 1,
		},
		ResourceUsage: json.RawMessage(`{}`),
		CheckpointUpdates: []workers.CheckpointUpdate{
			{
				Key:           "default",
				SchemaVersion: "worker_selfcheck.checkpoint.v0.2",
				Value:         checkpoint,
				Metadata:      json.RawMessage(`{"schema_version":"worker_checkpoint.metadata.v0.2"}`),
			},
		},
		Retryable: false,
	}, nil
}
