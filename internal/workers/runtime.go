package workers

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"loom.local/loom/internal/requestctx"
)

type Runtime interface {
	Kind() string
	Describe() KindDescriptor
	DefaultConfig() json.RawMessage
	ValidateConfig(ctx context.Context, config json.RawMessage) error
	RunOnce(ctx context.Context, run RunContext) (RunResult, error)
}

type DefaultInstanceProvider interface {
	DefaultInstances() []InstanceDescriptor
}

type RunContext struct {
	Service        Service
	Instance       WorkerInstance
	Kind           WorkerKind
	Run            WorkerRun
	Lease          WorkerLease
	Checkpoints    map[string]WorkerCheckpoint
	Request        requestctx.Context
	Logger         *slog.Logger
	StartedAt      time.Time
	Deadline       time.Time
	CorrelationID  string
	IdempotencyKey string
}

type RunResult struct {
	Status            string             `json:"status"`
	ResultSummary     json.RawMessage    `json:"result_summary"`
	Counters          map[string]int64   `json:"counters"`
	ResourceUsage     json.RawMessage    `json:"resource_usage"`
	CheckpointUpdates []CheckpointUpdate `json:"checkpoint_updates"`
	NextRunAfter      *time.Time         `json:"next_run_after,omitempty"`
	Retryable         bool               `json:"retryable"`
}

type CheckpointUpdate struct {
	Key           string          `json:"key"`
	SchemaVersion string          `json:"schema_version"`
	Value         json.RawMessage `json:"value"`
	Metadata      json.RawMessage `json:"metadata"`
}

type WorkerCheckpoint struct {
	WorkerCheckpointID string          `json:"worker_checkpoint_id"`
	WorkerInstanceID   string          `json:"worker_instance_id"`
	CheckpointKey      string          `json:"checkpoint_key"`
	CheckpointJSON     json.RawMessage `json:"checkpoint_json"`
	SchemaVersion      string          `json:"schema_version"`
	UpdatedByRunID     *string         `json:"updated_by_run_id,omitempty"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Metadata           json.RawMessage `json:"metadata"`
}
