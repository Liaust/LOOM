package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

type NotesProjectionRuntime struct {
	Dirty      notesprojection.DirtyStore
	Projection interface {
		ApplyDelta(context.Context, []string) (bool, error)
		Refresh(context.Context) (bool, error)
	}
	LoadCheckpoint func(context.Context, string) (json.RawMessage, error)
	Now            func() time.Time
}

func NewNotesProjectionRuntime(dirty notesprojection.DirtyStore, projection *notesprojection.Service) NotesProjectionRuntime {
	return NotesProjectionRuntime{Dirty: dirty, Projection: projection}
}
func (r NotesProjectionRuntime) Kind() string { return notesprojection.WorkerKind }
func (r NotesProjectionRuntime) DefaultConfig() json.RawMessage {
	return json.RawMessage(`{"schema_version":"notes_projection.config.v1","max_identities":64,"retry_seconds":30,"repair_interval_seconds":86400}`)
}
func (r NotesProjectionRuntime) Describe() workers.KindDescriptor {
	return workers.KindDescriptor{WorkerKind: r.Kind(), DisplayName: "Notes projection", Description: "Materializes committed generated-view deltas independently of source indexing.", RuntimeOwner: workers.RuntimeOwnerLoomd, RuntimePackage: "loom.core.notesprojection", Status: workers.KindStatusActive, SupportedLocalities: []string{workers.LocalityMainOwned}, MayTouchFilesystem: true,
		DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":5,"run_on_startup":true}`),
		DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
		DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
		DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":50}`),
		DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2","resource_class":"light"}`),
		ConfigSchemaJSON:             json.RawMessage(`{"type":"object"}`), CheckpointSchemaJSON: json.RawMessage(`{"type":"object"}`), ResultSchemaJSON: json.RawMessage(`{"type":"object"}`), Metadata: json.RawMessage(`{"builtin":true}`)}
}
func (r NotesProjectionRuntime) DefaultInstances() []workers.InstanceDescriptor {
	d := r.Describe()
	return []workers.InstanceDescriptor{{WorkerKey: "main.notes_projection", WorkerKind: r.Kind(), DisplayName: d.DisplayName, Description: d.Description, Locality: workers.LocalityMainOwned, ConfigJSON: r.DefaultConfig(), TickPolicyJSON: d.DefaultTickPolicyJSON, ConcurrencyJSON: d.DefaultConcurrencyPolicyJSON, RetryPolicyJSON: d.DefaultRetryPolicyJSON, TimeoutPolicyJSON: d.DefaultTimeoutPolicyJSON, ResourceLimitsJSON: d.DefaultResourceLimitsJSON, VisibilityJSON: json.RawMessage(`{}`), Metadata: json.RawMessage(`{"seeded_by":"workers.SeedBuiltins"}`)}}
}

type notesProjectionConfig struct {
	SchemaVersion         string `json:"schema_version"`
	MaxIdentities         int    `json:"max_identities"`
	RetrySeconds          int    `json:"retry_seconds"`
	RepairIntervalSeconds int    `json:"repair_interval_seconds"`
}

func parseNotesProjectionConfig(raw json.RawMessage) (notesProjectionConfig, error) {
	var c notesProjectionConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.SchemaVersion != "notes_projection.config.v1" || c.MaxIdentities < 1 || c.MaxIdentities > notesprojection.MaxDeltaIdentities || c.RetrySeconds < 1 || c.RepairIntervalSeconds < 3600 {
		return c, fmt.Errorf("invalid notes projection config")
	}
	return c, nil
}
func (r NotesProjectionRuntime) ValidateConfig(_ context.Context, raw json.RawMessage) error {
	_, err := parseNotesProjectionConfig(raw)
	return err
}

type notesProjectionCheckpoint struct {
	NextRepairAt time.Time              `json:"next_repair_at"`
	LastRepairAt time.Time              `json:"last_repair_at,omitempty"`
	FullFailure  *projectionFullFailure `json:"full_failure,omitempty"`
}

type projectionFullFailure struct {
	Sequence     int64                               `json:"sequence"`
	Attempts     int                                 `json:"attempts"`
	Dependencies []notesprojection.FailureDependency `json:"dependencies,omitempty"`
}

func readNotesProjectionCheckpoint(raw json.RawMessage) (notesProjectionCheckpoint, error) {
	var c notesProjectionCheckpoint
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("invalid notes projection checkpoint")
		}
	}
	return c, nil
}
func (r NotesProjectionRuntime) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r NotesProjectionRuntime) AutomaticReady(ctx context.Context, instance workers.WorkerInstance, now time.Time) (bool, *time.Time, error) {
	if r.Dirty == nil || r.LoadCheckpoint == nil {
		return false, nil, fmt.Errorf("notes projection readiness is not configured")
	}
	raw, err := r.LoadCheckpoint(ctx, instance.WorkerInstanceID)
	if err != nil {
		return false, nil, err
	}
	checkpoint, err := readNotesProjectionCheckpoint(raw)
	if err != nil {
		return false, nil, err
	}
	next, err := r.Dirty.NextProjectionDirtyAt(ctx)
	if err != nil {
		return false, nil, err
	}
	repair := checkpoint.NextRepairAt
	if repair.IsZero() {
		repair = now
	}
	if next == nil || repair.Before(*next) {
		next = &repair
	}
	return !next.After(now), next, nil
}

func (r NotesProjectionRuntime) RunOnce(ctx context.Context, run workers.RunContext) (workers.RunResult, error) {
	if r.Dirty == nil || r.Projection == nil {
		return workers.RunResult{}, fmt.Errorf("notes projection runtime is not configured")
	}
	config, err := parseNotesProjectionConfig(run.Instance.ConfigJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	policy, err := workers.ParseTickPolicy(run.Instance.TickPolicyJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	checkpoint, err := readNotesProjectionCheckpoint(run.Checkpoints["default"].CheckpointJSON)
	if err != nil {
		return workers.RunResult{}, err
	}
	now := r.now()
	priorRepairAt := checkpoint.NextRepairAt
	if checkpoint.NextRepairAt.IsZero() || !checkpoint.NextRepairAt.After(now) {
		if err := r.Dirty.RequireProjectionReconcile(ctx); err != nil {
			return workers.RunResult{}, err
		}
		// A pending full repair retains its own durable failure deadline.
		checkpoint.NextRepairAt = now.Add(time.Duration(config.RepairIntervalSeconds) * time.Second)
	}
	changes, err := r.Dirty.PendingProjectionDirty(ctx, now, config.MaxIdentities)
	if err != nil {
		return workers.RunResult{}, err
	}
	var ids []string
	full := false
	var fullSequence int64
	for _, change := range changes {
		if change.Key == "full" {
			full = true
			fullSequence = change.Sequence
		} else if change.ObjectID != "" {
			ids = append(ids, change.ObjectID)
		} else {
			return workers.RunResult{}, fmt.Errorf("invalid projection dirty identity")
		}
	}
	changed := false
	var applyErr error
	failures := map[string]error{}
	// Recursive evidence isolation must leave time to persist progress/backoff.
	// Using the whole lease here loses those receipts when the supervisor expires it.
	budget := 20 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		budget = min(budget, time.Until(deadline)/2)
	}
	workCtx, cancelWork := context.WithTimeout(ctx, budget)
	defer cancelWork()
	metadataHeld := false
	projectionStarted := time.Now()
	// An exact offending-object change may repair a held full inventory. Other
	// edits continue through their own delta without invalidating that failure.
	if !full && checkpoint.FullFailure != nil && projectionDependencySelected(checkpoint.FullFailure.Dependencies, ids) {
		if probe, ok := r.Projection.(interface {
			FailureChanged(context.Context, []notesprojection.FailureDependency) (bool, error)
		}); ok {
			probeCtx, cancel := context.WithTimeout(workCtx, 2*time.Second)
			changed, probeErr := probe.FailureChanged(probeCtx, checkpoint.FullFailure.Dependencies)
			cancel()
			if changed && probeErr == nil {
				if wake, ok := r.Dirty.(interface {
					WakeProjectionReconcile(context.Context, int64) error
				}); ok {
					if err := wake.WakeProjectionReconcile(ctx, checkpoint.FullFailure.Sequence); err != nil {
						return workers.RunResult{}, err
					}
				}
			}
		}
	}
	if full {
		// Complete inventory repair retains unchanged payloads. Explicit Rebuild
		// remains available separately for byte repair, not a daily recopy.
		prior := checkpoint.FullFailure
		if prior != nil && len(prior.Dependencies) > 0 {
			if probe, ok := r.Projection.(interface {
				FailureChanged(context.Context, []notesprojection.FailureDependency) (bool, error)
			}); ok {
				probeCtx, cancel := context.WithTimeout(workCtx, 2*time.Second)
				different, probeErr := probe.FailureChanged(probeCtx, prior.Dependencies)
				cancel()
				if !different || probeErr != nil {
					metadataHeld = true
					applyErr = &notesprojection.InvalidSourcePathsError{Dependencies: prior.Dependencies}
				}
			}
		}
		if !metadataHeld {
			changed, applyErr = r.Projection.Refresh(workCtx)
		}
		// A repair does not acknowledge object marks on the strength of a filtered
		// inventory. Each ordinary identity still gets its targeted evidence check.
		fullChanges := changes[:0]
		for _, change := range changes {
			if change.Key == "full" {
				fullChanges = append(fullChanges, change)
			}
		}
		changes = fullChanges
	} else if len(ids) > 0 {
		// Unavailable evidence holds only its exact identity. Bisect the bounded
		// batch so one unresolved source cannot block unrelated committed edits.
		var apply func([]notesprojection.DirtyChange)
		apply = func(batch []notesprojection.DirtyChange) {
			selected := make([]string, len(batch))
			for i, change := range batch {
				selected[i] = change.ObjectID
			}
			err := workCtx.Err()
			updated := false
			if err == nil {
				updated, err = r.Projection.ApplyDelta(workCtx, selected)
			}
			changed = changed || updated
			var unavailable *notesprojection.UncertainSourcesError
			if errors.As(err, &unavailable) && len(unavailable.ObjectIDs) > 0 {
				unknown := make(map[string]bool, len(unavailable.ObjectIDs))
				for _, id := range unavailable.ObjectIDs {
					unknown[id] = true
				}
				var remaining []notesprojection.DirtyChange
				for _, change := range batch {
					if !unknown[change.ObjectID] {
						remaining = append(remaining, change)
					}
				}
				// Validate the provider's exact subset before isolating any member.
				if len(unknown) == len(unavailable.ObjectIDs) && len(batch)-len(remaining) == len(unknown) {
					applyErr = err
					for _, change := range batch {
						if unknown[change.ObjectID] {
							failures[change.Key] = err
						}
					}
					if len(remaining) > 0 {
						apply(remaining)
					}
					return
				}
			}
			if errors.Is(err, notesprojection.ErrDeltaUncertain) && len(batch) > 1 {
				mid := len(batch) / 2
				apply(batch[:mid])
				apply(batch[mid:])
				return
			}
			if err != nil {
				applyErr = err
				for _, change := range batch {
					failures[change.Key] = err
				}
			}
		}
		apply(changes)
	}
	projectionElapsed := time.Since(projectionStarted).Milliseconds()
	if err := ctx.Err(); err != nil {
		return workers.RunResult{}, err
	} // replay rather than acknowledge cancellation
	finishStarted := time.Now()
	completions := make([]notesprojection.DirtyCompletion, 0, len(changes))
	completed, failed := int64(0), int64(0)
	if applyErr != nil {
		reconcileRequired := errors.Is(applyErr, notesprojection.ErrProjectionReconcileRequired)
		for _, failure := range failures {
			reconcileRequired = reconcileRequired || errors.Is(failure, notesprojection.ErrProjectionReconcileRequired)
		}
		if reconcileRequired {
			if err := r.Dirty.RequireProjectionReconcile(ctx); err != nil {
				return workers.RunResult{}, err
			}
		}
		finishedAt := r.now()
		retry := finishedAt.Add(time.Duration(config.RetrySeconds) * time.Second)
		if full {
			attempts := 1
			if prior := checkpoint.FullFailure; prior != nil && prior.Sequence == fullSequence {
				attempts = min(prior.Attempts+1, 16)
			}
			failure := &projectionFullFailure{Sequence: fullSequence, Attempts: attempts}
			delay := min(time.Duration(config.RetrySeconds)*time.Second*time.Duration(1<<min(attempts-1, 8)), 15*time.Minute)
			var invalid *notesprojection.InvalidSourcePathsError
			if errors.As(applyErr, &invalid) {
				failure.Dependencies = invalid.Dependencies
				delay = 6 * time.Hour
			}
			checkpoint.FullFailure = failure
			retry = finishedAt.Add(delay)
			if metadataHeld && priorRepairAt.After(finishedAt) {
				retry = priorRepairAt
			}
		}
		if full && (errors.Is(applyErr, notesprojection.ErrDeltaUncertain) || errors.Is(applyErr, context.DeadlineExceeded)) {
			// Full refreshes have no identity failure map. Both durable deadlines
			// must retain the same backoff or readiness starts another full pass.
			retry = finishedAt.Add(max(time.Duration(config.RetrySeconds)*time.Second, 15*time.Minute))
		}
		for _, change := range changes {
			var at *time.Time
			if full || failures[change.Key] != nil {
				deferred := retry
				if errors.Is(failures[change.Key], notesprojection.ErrDeltaUncertain) {
					deferred = finishedAt.Add(max(time.Duration(config.RetrySeconds)*time.Second, 15*time.Minute))
				}
				if errors.Is(failures[change.Key], notesprojection.ErrProjectionReconcileRequired) && checkpoint.FullFailure != nil && checkpoint.NextRepairAt.After(deferred) {
					deferred = checkpoint.NextRepairAt
				}
				at = &deferred
			}
			completions = append(completions, notesprojection.DirtyCompletion{Change: change, RetryAt: at})
			if at == nil {
				completed++
			} else {
				failed++
			}
		}
		if full {
			checkpoint.NextRepairAt = retry
		}
	} else {
		for _, change := range changes {
			completions = append(completions, notesprojection.DirtyCompletion{Change: change})
			completed++
		}
		if full {
			checkpoint.LastRepairAt = r.now()
			checkpoint.NextRepairAt = r.now().Add(time.Duration(config.RepairIntervalSeconds) * time.Second)
			checkpoint.FullFailure = nil
		}
	}
	if batch, ok := r.Dirty.(interface {
		FinishProjectionDirtyBatch(context.Context, []notesprojection.DirtyCompletion) error
	}); ok {
		if err := batch.FinishProjectionDirtyBatch(ctx, completions); err != nil {
			return workers.RunResult{}, err
		}
	} else {
		for _, item := range completions {
			if err := r.Dirty.FinishProjectionDirty(ctx, item.Change, item.RetryAt); err != nil {
				return workers.RunResult{}, err
			}
		}
	}
	finishElapsed := time.Since(finishStarted).Milliseconds()
	next, err := r.Dirty.NextProjectionDirtyAt(ctx)
	if err != nil {
		return workers.RunResult{}, err
	}
	if next == nil || checkpoint.NextRepairAt.Before(*next) {
		at := checkpoint.NextRepairAt
		next = &at
	}
	// Supervisor checks policy.Due before readiness. Keep the policy cadence so
	// a new committed mark can wake an idle worker; readiness returns the exact
	// durable retry/repair deadline and suppresses empty run transactions.
	nextWorkAt := next
	next = policy.NextAfter(r.now())
	counters := map[string]int64{"selected": int64(len(changes)), "completed": completed, "failed": failed}
	if changed {
		counters["changed"] = 1
	}
	if full {
		counters["full_reconcile"] = 1
	}
	summary := map[string]any{"selected": len(changes), "completed": completed, "failed": failed, "changed": changed, "full_reconcile": full}
	summary["elapsed_ms"] = map[string]int64{"projection": projectionElapsed, "dirty_finish": finishElapsed}
	if metadataHeld {
		summary["metadata_only_hold"] = true
	}
	if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
		summary["processing_budget_exhausted"] = true
	}
	summary["next_work_at"] = nextWorkAt
	if applyErr != nil {
		summary["reason"] = "projection_apply_failed"
		if errors.Is(applyErr, notesprojection.ErrDeltaUncertain) {
			summary["reason"] = "projection_evidence_unavailable"
		}
		if errors.Is(applyErr, notesprojection.ErrProjectionReconcileRequired) {
			summary["reason"] = "projection_scope_requires_reconcile"
		}
		var invalid *notesprojection.InvalidSourcePathsError
		if errors.As(applyErr, &invalid) {
			summary["reason"] = "projection_source_path_invalid"
			summary["dependency_count"] = len(invalid.Dependencies)
		}
	} // payload/private paths stay out of work receipts
	status := workers.RunStatusSucceeded
	if failed > 0 || checkpoint.FullFailure != nil {
		status = workers.RunStatusFailed
		summary["processing_incomplete"] = true
	}
	return workers.RunResult{Status: status, Retryable: status == workers.RunStatusFailed, ResultSummary: mustWorkerJSON(summary), Counters: counters, ResourceUsage: json.RawMessage(`{}`), NextRunAfter: next,
		CheckpointUpdates: []workers.CheckpointUpdate{{Key: "default", SchemaVersion: "notes_projection.checkpoint.v1", Value: mustWorkerJSON(checkpoint), Metadata: json.RawMessage(`{}`)}}}, nil
}

func projectionDependencySelected(dependencies []notesprojection.FailureDependency, ids []string) bool {
	for _, dependency := range dependencies {
		for _, id := range ids {
			if id == dependency.ObjectID {
				return true
			}
		}
	}
	return false
}
