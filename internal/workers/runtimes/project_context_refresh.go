package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/workers"
)

type ProjectContextRefreshRuntime struct {
	Sources interface {
		ListProjectContextSources(context.Context, string, string, int) ([]string, error)
	}
	Refresh interface {
		RefreshProject(context.Context, string, bool) (provenance.ProjectProjectionSyncReceipt, error)
	}
	LocalNode string
}

func (r ProjectContextRefreshRuntime) Kind() string { return workers.KindProjectContextRefresh }
func (r ProjectContextRefreshRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"project_context_refresh.config.v1"}`)
}
func (r ProjectContextRefreshRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{WorkerKind: r.Kind(), DisplayName: "Project context refresh", Description: "Refresh registered project development metadata without executing agents or applying resources.", RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.provenance.project_context", Status: workers.KindStatusActive, SupportedLocalities: []string{workers.LocalityMainOwned},
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":50}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{}`), CheckpointSchemaJSON: json.RawMessage(`{}`), ResultSchemaJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"builtin":true}`)}
}
func (r ProjectContextRefreshRuntime) DefaultInstances() []workers.InstanceDescriptor {
	d := r.Describe()
	return []workers.InstanceDescriptor{{WorkerKey: "main.project_context_refresh", WorkerKind: r.Kind(), DisplayName: d.DisplayName, Description: d.Description, Locality: workers.LocalityMainOwned, ConfigJSON: r.DefaultConfig(), TickPolicyJSON: d.DefaultTickPolicyJSON, ConcurrencyJSON: d.DefaultConcurrencyPolicyJSON, RetryPolicyJSON: d.DefaultRetryPolicyJSON, TimeoutPolicyJSON: d.DefaultTimeoutPolicyJSON, ResourceLimitsJSON: d.DefaultResourceLimitsJSON, VisibilityJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"seeded_by":"workers.SeedBuiltins"}`)}}
}
func (r ProjectContextRefreshRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	_, err := workers.JSONObject(raw, "config_json")
	return err
}

type projectContextCheckpoint struct {
	After string `json:"after_project_id"`
}
type projectContextAttempt struct {
	ProjectID  string                `json:"project_id"`
	Status     string                `json:"status"`
	SnapshotID provenance.SemanticID `json:"snapshot_id,omitempty"`
}

func (r ProjectContextRefreshRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Sources == nil || r.Refresh == nil || r.LocalNode == "" {
		return workers.RunResult{}, fmt.Errorf("project context refresh is not configured")
	}
	cursor := projectContextCheckpoint{}
	if saved, ok := run.Checkpoints["default"]; ok {
		if json.Unmarshal(saved.CheckpointJSON, &cursor) != nil || (cursor.After != "" && ids.Validate(ids.ProjectPrefix, cursor.After) != nil) {
			return workers.RunResult{}, fmt.Errorf("invalid project context checkpoint")
		}
	}
	page, err := r.Sources.ListProjectContextSources(ctx, r.LocalNode, cursor.After, 20)
	if err != nil {
		return workers.RunResult{}, err
	}
	if len(page) == 0 && cursor.After != "" {
		cursor.After = ""
		page, err = r.Sources.ListProjectContextSources(ctx, r.LocalNode, "", 20)
		if err != nil {
			return workers.RunResult{}, err
		}
	}
	if len(page) > 20 {
		return workers.RunResult{}, fmt.Errorf("project context source page exceeds bound")
	}
	attempts := []projectContextAttempt{}
	var failed int64
	previous := cursor.After
	for _, id := range page {
		if ids.Validate(ids.ProjectPrefix, id) != nil || id <= previous {
			return workers.RunResult{}, fmt.Errorf("invalid project context source order")
		}
		previous = id
	}
	for _, id := range page {
		if ctx.Err() != nil {
			break
		}
		projectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		receipt, err := r.Refresh.RefreshProject(projectCtx, id, true)
		cancel()
		attempt := projectContextAttempt{ProjectID: id, Status: "refreshed", SnapshotID: receipt.ProjectSnapshotID}
		if err != nil {
			attempt.Status = "unavailable"
			attempt.SnapshotID = ""
			failed++
		}
		attempts = append(attempts, attempt)
		cursor.After = id // Failed sources cannot starve the next registered project.
	}
	checkpoint, _ := json.Marshal(cursor)
	summary, _ := json.Marshal(struct {
		Attempts []projectContextAttempt `json:"attempts"`
		Failed   int64                   `json:"failed"`
	}{attempts, failed})
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: summary, Counters: map[string]int64{"attempted": int64(len(attempts)), "unavailable": failed}, ResourceUsage: json.RawMessage(`{}`), CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: "project_context_refresh.checkpoint.v1", Value: checkpoint, Metadata: json.RawMessage(`{}`)}}, NextRunAfter: policy.NextAfter(time.Now().UTC())}, nil
}
