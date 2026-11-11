package runtimes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/projects"
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
	Changes   interface {
		Pending(context.Context, map[string]projects.ProjectContextObservation, time.Time, bool) ([]projects.ProjectContextChange, *time.Time, error)
	}
	LoadCheckpoint func(context.Context, string) (json.RawMessage, error)
	Now            func() time.Time
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
	After        string                                        `json:"after_project_id"`
	Observations map[string]projects.ProjectContextObservation `json:"observations,omitempty"`
}
type projectContextAttempt struct {
	ProjectID  string                `json:"project_id"`
	Status     string                `json:"status"`
	SnapshotID provenance.SemanticID `json:"snapshot_id,omitempty"`
	Reason     string                `json:"reason,omitempty"`
}

func (r ProjectContextRefreshRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func readProjectContextCheckpoint(raw json.RawMessage) (projectContextCheckpoint, error) {
	cursor := projectContextCheckpoint{Observations: map[string]projects.ProjectContextObservation{}}
	if len(raw) != 0 && (json.Unmarshal(raw, &cursor) != nil || (cursor.After != "" && ids.Validate(ids.ProjectPrefix, cursor.After) != nil)) {
		return cursor, fmt.Errorf("invalid project context checkpoint")
	}
	if cursor.Observations == nil {
		cursor.Observations = map[string]projects.ProjectContextObservation{}
	}
	for id, observed := range cursor.Observations {
		if ids.Validate(ids.ProjectPrefix, id) != nil || observed.Generation == "" || observed.AttemptedAt.IsZero() ||
			(observed.Status != "refreshed" && observed.Status != "unavailable") {
			return cursor, fmt.Errorf("invalid project context observation checkpoint")
		}
	}
	return cursor, nil
}

func (r ProjectContextRefreshRuntime) AutomaticReady(ctx context.Context, instance workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	if r.Changes == nil || r.LoadCheckpoint == nil {
		return true, nil, nil // Preserve runtimes without the change owner.
	}
	raw, err := r.LoadCheckpoint(ctx, instance.WorkerInstanceID)
	if err != nil {
		return false, nil, fmt.Errorf("project context checkpoint unavailable")
	}
	cursor, err := readProjectContextCheckpoint(raw)
	if err != nil {
		return false, nil, err
	}
	pending, next, err := r.Changes.Pending(ctx, cursor.Observations, now, false)
	return len(pending) != 0, next, err
}

func (r ProjectContextRefreshRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Sources == nil || r.Refresh == nil || r.LocalNode == "" {
		return workers.RunResult{}, fmt.Errorf("project context refresh is not configured")
	}
	var savedJSON json.RawMessage
	if saved, ok := run.Checkpoints["default"]; ok {
		savedJSON = saved.CheckpointJSON
	}
	cursor, err := readProjectContextCheckpoint(savedJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	if r.Changes != nil {
		return r.runChanges(ctx, run, cursor)
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

func (r ProjectContextRefreshRuntime) runChanges(ctx context.Context, run workers.RunContext, cursor projectContextCheckpoint) (workers.RunResult, error) {
	force := run.Run.TriggerKind != workers.TriggerSupervisorTick
	pending, _, err := r.Changes.Pending(ctx, cursor.Observations, r.now(), force)
	if err != nil {
		return workers.RunResult{}, err
	}
	// Rotate the bounded page so repeatedly changing projects cannot starve peers.
	start := 0
	for start < len(pending) && pending[start].ProjectID <= cursor.After {
		start++
	}
	ordered := append(append([]projects.ProjectContextChange{}, pending[start:]...), pending[:start]...)
	if len(ordered) > 20 {
		ordered = ordered[:20]
	}
	attempts := []projectContextAttempt{}
	var failed int64
	for _, change := range ordered {
		if err := ctx.Err(); err != nil {
			return workers.RunResult{}, err // No acknowledgement of a cancelled page.
		}
		projectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		receipt, refreshErr := r.Refresh.RefreshProject(projectCtx, change.ProjectID, true)
		cancel()
		attempt := projectContextAttempt{ProjectID: change.ProjectID, Status: "refreshed", SnapshotID: receipt.ProjectSnapshotID, Reason: change.Reason}
		observed := cursor.Observations[change.ProjectID]
		observed.Generation, observed.AttemptedAt, observed.Status = change.Generation, r.now(), "refreshed"
		if refreshErr != nil {
			attempt.Status, attempt.SnapshotID = "unavailable", ""
			observed.Status = "unavailable"
			failed++
		} else {
			observed.RefreshedAt = observed.AttemptedAt
		}
		// A new hint/source generation arriving during refresh remains pending;
		// only this exact selected generation is durably acknowledged.
		cursor.Observations[change.ProjectID] = observed
		cursor.After = change.ProjectID
		attempts = append(attempts, attempt)
	}
	if err := ctx.Err(); err != nil {
		return workers.RunResult{}, err
	}
	remaining, deadline, err := r.Changes.Pending(ctx, cursor.Observations, r.now(), false)
	if err != nil {
		return workers.RunResult{}, err
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	// Keep policy spacing: notifications do not activate a disabled/daily worker
	// or bypass its interval. Empty automatic probes create no run/checkpoint.
	next := policy.NextAfter(r.now())
	checkpoint, _ := json.Marshal(cursor)
	watchStatus := "unavailable"
	if status, ok := r.Changes.(interface{ WatchStatus() string }); ok {
		watchStatus = status.WatchStatus()
	}
	summary, _ := json.Marshal(struct {
		Attempts              []projectContextAttempt `json:"attempts"`
		Failed                int64                   `json:"failed"`
		Dirty                 int                     `json:"dirty_count"`
		SafetyIntervalSeconds int                     `json:"safety_interval_seconds"`
		NextSafety            *time.Time              `json:"next_safety_after,omitempty"`
		WatchStatus           string                  `json:"watch_status"`
	}{attempts, failed, len(remaining), int(projects.ProjectContextSafetyInterval.Seconds()), deadline, watchStatus})
	return workers.RunResult{Status: workers.RunStatusSucceeded, ResultSummary: summary,
		Counters:      map[string]int64{"attempted": int64(len(attempts)), "unavailable": failed, "dirty": int64(len(remaining))},
		ResourceUsage: json.RawMessage(`{}`), NextRunAfter: next,
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: "project_context_refresh.checkpoint.v1", Value: checkpoint, Metadata: json.RawMessage(`{}`)}},
	}, nil
}
