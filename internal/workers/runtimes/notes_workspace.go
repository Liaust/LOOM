package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"loom.local/loom/internal/notesworkspacesync"
	"loom.local/loom/internal/workers"
)

const notesWorkspaceKind = "notes_workspace_sync"

type NotesWorkspaceRuntime struct{ Runtime *notesworkspacesync.Runtime }

func (r NotesWorkspaceRuntime) Kind() string { return notesWorkspaceKind }
func (r NotesWorkspaceRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"notes_workspace.config.v1"}`)
}
func (r NotesWorkspaceRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{
		WorkerKind: notesWorkspaceKind, DisplayName: "Notes workspace sync",
		Description:  "Synchronizes explicitly selected canonical Notes through native client intent.",
		RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.notes", Status: workers.KindStatusActive,
		SupportedLocalities: []string{workers.LocalityMainOwned}, MayTouchFilesystem: true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":120}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"io_medium"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"notes_workspace.config_schema.v1","type":"object"}`),
		CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"notes_workspace.checkpoint_schema.v1","type":"object"}`),
		ResultSchemaJSON:             json.RawMessage(`{"schema_version":"notes_workspace.result_schema.v1","type":"object"}`),
	}
}
func (r NotesWorkspaceRuntime) DefaultInstances() []workers.InstanceDescriptor {
	d := r.Describe()
	return []workers.InstanceDescriptor{{WorkerKey: "main.notes_workspace_sync", WorkerKind: notesWorkspaceKind,
		DisplayName: d.DisplayName, Description: d.Description, Locality: workers.LocalityMainOwned,
		ConfigJSON: r.DefaultConfig(), TickPolicyJSON: d.DefaultTickPolicyJSON,
		ConcurrencyJSON: d.DefaultConcurrencyPolicyJSON, RetryPolicyJSON: d.DefaultRetryPolicyJSON,
		TimeoutPolicyJSON: d.DefaultTimeoutPolicyJSON, ResourceLimitsJSON: d.DefaultResourceLimitsJSON,
		VisibilityJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"seeded_by":"workers.SeedBuiltins"}`)}}
}
func (r NotesWorkspaceRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	var cfg struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := decodeProvenanceArchivistJSON(raw, &cfg); err != nil || cfg.SchemaVersion != "notes_workspace.config.v1" {
		return errors.New("invalid Notes workspace worker configuration")
	}
	return nil
}
func (r NotesWorkspaceRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Runtime == nil {
		return workers.RunResult{}, errors.New("Notes workspace is not configured; select collections and native settings first")
	}
	if err := r.ValidateConfig(ctx, run.Instance.ConfigJSON); err != nil {
		return workers.RunResult{}, err
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	var cursor notesworkspacesync.RuntimeCursor
	if saved, ok := run.Checkpoints["sources"]; ok {
		if saved.SchemaVersion != "notes_workspace.cursor.v1" || json.Unmarshal(saved.CheckpointJSON, &cursor) != nil {
			return workers.RunResult{}, errors.New("invalid Notes workspace checkpoint")
		}
	}
	result, err := r.Runtime.Run(ctx, cursor)
	summary, _ := json.Marshal(result)
	if err != nil {
		return workers.RunResult{ResultSummary: summary}, err
	}
	checkpoint, _ := json.Marshal(result.Cursor)
	next := notesWorkspaceNextRun(policy, r.Runtime.Config.WatchSeconds, result.Replication, run.StartedAt, time.Now().UTC())
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: summary,
		NextRunAfter:      next,
		Counters:          map[string]int64{"imported": int64(result.Imported), "processed": int64(result.Processed), "held": int64(result.Held), "exported": int64(result.Exported), "skipped": int64(result.Skipped)},
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "sources", SchemaVersion: "notes_workspace.cursor.v1", Value: checkpoint}},
	}, nil
}

func notesWorkspaceNextRun(policy workers.TickPolicy, watch int, replication string, started, now time.Time) *time.Time {
	// Residency already includes the interval. Don't add another quiet period
	// after a healthy live window. Manual/daily and recovery pauses stay unchanged.
	if watch > 0 && replication == "completed" && policy.Mode == workers.TickModeInterval && !started.IsZero() {
		return policy.NextAfter(started)
	}
	return policy.NextAfter(now)
}
