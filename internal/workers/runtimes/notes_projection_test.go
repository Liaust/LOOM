package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

type projectionDirtyFixture struct {
	changes          map[string]notesprojection.DirtyChange
	retry            map[string]time.Time
	now              time.Time
	acknowledgements []notesprojection.DirtyChange
	ensured          int
	batches          int
}

func (f *projectionDirtyFixture) FinishProjectionDirtyBatch(ctx context.Context, items []notesprojection.DirtyCompletion) error {
	f.batches++
	for _, item := range items {
		if err := f.FinishProjectionDirty(ctx, item.Change, item.RetryAt); err != nil {
			return err
		}
	}
	return nil
}

func TestNotesProjectionFinishesRefusedBatchOnce(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	for i := range 64 {
		dirty.mark(fmt.Sprintf("held-%02d", i))
	}
	projection.applyIDs = func(ids []string) error {
		return &notesprojection.UncertainSourcesError{ObjectIDs: ids}
	}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || dirty.batches != 1 || result.Counters["failed"] != 64 || len(dirty.changes) != 64 {
		t.Fatalf("refusal split commits or lost held work: batches=%d result=%+v err=%v", dirty.batches, result, err)
	}
}

func (f *projectionDirtyFixture) mark(id string) {
	key := "object:" + id
	if id == "" {
		key = "full"
	}
	change := f.changes[key]
	change.Key = key
	change.ObjectID = id
	change.Sequence++
	f.changes[key] = change
	f.retry[key] = f.now
}
func (f *projectionDirtyFixture) PendingProjectionDirty(_ context.Context, now time.Time, limit int) ([]notesprojection.DirtyChange, error) {
	var keys []string
	for key := range f.changes {
		if !f.retry[key].After(now) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	// A repair has priority over ordinary identities.
	if _, ok := f.changes["full"]; ok && !f.retry["full"].After(now) {
		sort.SliceStable(keys, func(i, j int) bool { return keys[i] == "full" && keys[j] != "full" })
	}
	if len(keys) > limit {
		keys = keys[:limit]
	}
	var result []notesprojection.DirtyChange
	for _, key := range keys {
		result = append(result, f.changes[key])
	}
	return result, nil
}
func (f *projectionDirtyFixture) NextProjectionDirtyAt(context.Context) (*time.Time, error) {
	var next *time.Time
	for key := range f.changes {
		at := f.retry[key]
		if next == nil || at.Before(*next) {
			next = &at
		}
	}
	return next, nil
}
func (f *projectionDirtyFixture) FinishProjectionDirty(_ context.Context, c notesprojection.DirtyChange, retry *time.Time) error {
	f.acknowledgements = append(f.acknowledgements, c)
	if current, ok := f.changes[c.Key]; ok && current.Sequence == c.Sequence {
		if retry == nil {
			delete(f.changes, c.Key)
			delete(f.retry, c.Key)
		} else {
			f.retry[c.Key] = *retry
		}
	}
	return nil
}
func (f *projectionDirtyFixture) RequireProjectionReconcile(context.Context) error {
	f.ensured++
	if _, ok := f.changes["full"]; !ok {
		f.mark("")
	}
	return nil
}

type projectionApplyFixture struct {
	deltaCalls   [][]string
	fullCalls    int
	apply        func() error
	applyIDs     func([]string) error
	applyContext func(context.Context, []string) error
	rebuildError error
	probeChanged bool
	probeError   error
	probes       int
}

func (f *projectionApplyFixture) FailureChanged(context.Context, []notesprojection.FailureDependency) (bool, error) {
	f.probes++
	return f.probeChanged, f.probeError
}

func (f *projectionDirtyFixture) WakeProjectionReconcile(_ context.Context, sequence int64) error {
	if c, ok := f.changes["full"]; ok && c.Sequence == sequence {
		f.retry["full"] = f.now
	}
	return nil
}

func (f *projectionApplyFixture) ApplyDelta(ctx context.Context, ids []string) (bool, error) {
	f.deltaCalls = append(f.deltaCalls, append([]string(nil), ids...))
	if f.applyContext != nil {
		return false, f.applyContext(ctx, ids)
	}
	if f.applyIDs != nil {
		return false, f.applyIDs(ids)
	}
	if f.apply != nil {
		return false, f.apply()
	}
	return true, nil
}
func (f *projectionApplyFixture) Refresh(context.Context) (bool, error) {
	f.fullCalls++
	return false, f.rebuildError
}
func projectionRuntimeFixture() (NotesProjectionRuntime, workers.RunContext, *projectionDirtyFixture, *projectionApplyFixture, *json.RawMessage) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	dirty := &projectionDirtyFixture{changes: map[string]notesprojection.DirtyChange{}, retry: map[string]time.Time{}, now: now}
	projection := &projectionApplyFixture{}
	checkpoint := mustWorkerJSON(notesProjectionCheckpoint{LastRepairAt: now, NextRepairAt: now.Add(24 * time.Hour)})
	r := NotesProjectionRuntime{Dirty: dirty, Projection: projection, Now: func() time.Time { return dirty.now }, LoadCheckpoint: func(context.Context, string) (json.RawMessage, error) { return checkpoint, nil }}
	run := workers.RunContext{Instance: workers.WorkerInstance{ConfigJSON: r.DefaultConfig(), TickPolicyJSON: r.Describe().DefaultTickPolicyJSON}, Run: workers.WorkerRun{TriggerKind: workers.TriggerSupervisorTick}, Checkpoints: map[string]workers.WorkerCheckpoint{"default": {CheckpointJSON: checkpoint}}}
	return r, run, dirty, projection, &checkpoint
}

func TestNotesProjectionRuntimeIdleReadinessAndNewerMark(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	ready, next, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now)
	if err != nil || ready || next == nil || !next.Equal(dirty.now.Add(24*time.Hour)) || dirty.ensured != 0 || len(dirty.acknowledgements) > 0 {
		t.Fatalf("idle readiness mutated work: %t %v %v", ready, next, err)
	}
	dirty.mark("a")
	projection.apply = func() error { dirty.mark("a"); return nil }
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["completed"] != 1 || projection.fullCalls != 0 {
		t.Fatalf("run: %+v %v", result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	if dirty.changes["object:a"].Sequence != 2 {
		t.Fatal("new mark cleared while applying")
	}
	if result.NextRunAfter == nil || !result.NextRunAfter.Before(dirty.now.Add(time.Minute)) {
		t.Fatal("persisted distant repair deadline blocked committed wakes")
	}
	projection.apply = nil
	if _, err := r.RunOnce(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	if ready, _, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || ready {
		t.Fatal("unchanged consumer stayed active", err)
	}
}

func TestNotesProjectionRuntimeRetryAndRestart(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	dirty.mark("a")
	projection.apply = func() error { return notesprojection.ErrDeltaUncertain }
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["failed"] != 1 || dirty.ensured != 0 {
		t.Fatalf("retry: %+v %v", result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	// Missing evidence cannot be repaired by repeatedly rebuilding a filtered
	// view. Keep the exact identity pending without a full-repair feedback loop.
	ready, next, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now)
	if err != nil || ready || next == nil || !next.Equal(dirty.now.Add(15*time.Minute)) {
		t.Fatalf("retry readiness: %t %v %v", ready, next, err)
	}
	// A fresh runtime relies only on durable selection/checkpoint, never a wake.
	restarted := NewNotesProjectionRuntime(dirty, nil)
	restarted.Projection = projection
	restarted.LoadCheckpoint = r.LoadCheckpoint
	restarted.Now = r.Now
	dirty.now = dirty.now.Add(15 * time.Minute)
	projection.apply = nil
	if ready, _, err := restarted.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || !ready {
		t.Fatal("restart lost pending work")
	}
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	if result, err := restarted.RunOnce(t.Context(), run); err != nil || result.Counters["completed"] != 1 {
		t.Fatal(result, err)
	}
}

func TestNotesProjectionBudgetPersistsPartialProgress(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	for _, id := range []string{"a", "b", "c", "d"} {
		dirty.mark(id)
	}
	projection.applyContext = func(ctx context.Context, ids []string) error {
		if len(ids) > 1 {
			return notesprojection.ErrDeltaUncertain
		}
		if ids[0] == "a" {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	result, err := r.RunOnce(ctx, run)
	if err != nil || ctx.Err() != nil || result.Counters["completed"] != 1 || result.Counters["failed"] != 3 {
		t.Fatalf("lost partial progress or exceeded lease: %+v %v (%v)", result, err, ctx.Err())
	}
	if len(dirty.changes) != 3 || len(dirty.acknowledgements) != 4 {
		t.Fatalf("unattempted work discarded: %+v", dirty.changes)
	}
	if ready, next, err := r.AutomaticReady(ctx, run.Instance, dirty.now); err != nil || ready || next == nil || !next.After(dirty.now) {
		t.Fatalf("deadline immediately retried: %t %v %v", ready, next, err)
	}
}

func TestNotesProjectionIsolatesReportedUnavailableIdentities(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	for _, id := range []string{"a", "b", "c", "d"} {
		dirty.mark(id)
	}
	projection.applyIDs = func(ids []string) error {
		for _, id := range ids {
			if id == "b" || id == "d" {
				return &notesprojection.UncertainSourcesError{ObjectIDs: []string{"b", "d"}}
			}
		}
		return nil
	}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || len(projection.deltaCalls) != 2 || result.Counters["completed"] != 2 || result.Counters["failed"] != 2 {
		t.Fatalf("repeated unavailable batch reads: %+v %+v %v", projection.deltaCalls, result, err)
	}
	if len(dirty.changes) != 2 || !dirty.retry["object:b"].Equal(dirty.now.Add(15*time.Minute)) {
		t.Fatalf("unavailable identities lost their backoff: %+v", dirty)
	}
}

func TestNotesProjectionParentCancellationDoesNotAcknowledge(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	dirty.mark("a")
	ctx, cancel := context.WithCancel(t.Context())
	projection.applyContext = func(context.Context, []string) error {
		cancel()
		return nil
	}
	if _, err := r.RunOnce(ctx, run); !errors.Is(err, context.Canceled) || len(dirty.acknowledgements) != 0 || len(dirty.changes) != 1 {
		t.Fatalf("cancelled work acknowledged: %v %+v", err, dirty)
	}
}

func TestNotesProjectionFullUncertaintyRetainsAlignedRetry(t *testing.T) {
	for _, retrySeconds := range []int{30, 1800} {
		t.Run((time.Duration(retrySeconds) * time.Second).String(), func(t *testing.T) {
			r, run, dirty, projection, saved := projectionRuntimeFixture()
			config, err := parseNotesProjectionConfig(run.Instance.ConfigJSON)
			if err != nil {
				t.Fatal(err)
			}
			config.RetrySeconds = retrySeconds
			run.Instance.ConfigJSON = mustWorkerJSON(config)
			dirty.mark("")
			projection.rebuildError = notesprojection.ErrDeltaUncertain
			result, err := r.RunOnce(t.Context(), run)
			if err != nil || result.Counters["failed"] != 1 || result.Counters["completed"] != 0 || projection.fullCalls != 1 {
				t.Fatal(result, err)
			}
			*saved = result.CheckpointUpdates[0].Value
			run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
			checkpoint, err := readNotesProjectionCheckpoint(*saved)
			deadline := dirty.now.Add(max(time.Duration(retrySeconds)*time.Second, 15*time.Minute))
			if err != nil || !dirty.retry["full"].Equal(deadline) || !checkpoint.NextRepairAt.Equal(deadline) || len(dirty.changes) != 1 {
				t.Fatalf("full retry lost/aligned incorrectly: %v %+v %v", dirty.retry, checkpoint, err)
			}
			restarted := NewNotesProjectionRuntime(dirty, nil)
			restarted.Projection = projection
			restarted.LoadCheckpoint = r.LoadCheckpoint
			restarted.Now = r.Now
			dirty.now = dirty.now.Add(time.Minute)
			ready, next, err := restarted.AutomaticReady(t.Context(), run.Instance, dirty.now)
			if err != nil || ready || next == nil || !next.Equal(deadline) {
				t.Fatalf("restart bypassed full retry: %t %v %v", ready, next, err)
			}
			dirty.mark("new-edit")
			if ready, _, err := restarted.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || !ready {
				t.Fatalf("full retry blocked new delta: %v", err)
			}
			result, err = restarted.RunOnce(t.Context(), run)
			if err != nil || result.Counters["completed"] != 1 || projection.fullCalls != 1 || len(projection.deltaCalls) != 1 || projection.deltaCalls[0][0] != "new-edit" {
				t.Fatal(result, err)
			}
			*saved = result.CheckpointUpdates[0].Value
			run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
			if !dirty.retry["full"].Equal(deadline) || len(dirty.changes) != 1 {
				t.Fatal("unrelated delta cleared the pending full repair")
			}
			dirty.now = deadline
			projection.rebuildError = nil
			if ready, _, err := restarted.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || !ready {
				t.Fatalf("full retry was lost: %v", err)
			}
			result, err = restarted.RunOnce(t.Context(), run)
			if err != nil || result.Counters["completed"] != 1 || projection.fullCalls != 2 || len(dirty.changes) != 0 {
				t.Fatal(result, err)
			}
		})
	}
}

func TestNotesProjectionUnavailableIdentityDoesNotBlockPeer(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	dirty.mark("missing")
	dirty.mark("ready")
	projection.applyIDs = func(ids []string) error {
		for _, id := range ids {
			if id == "missing" {
				return notesprojection.ErrDeltaUncertain
			}
		}
		return nil
	}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["completed"] != 1 || result.Counters["failed"] != 1 || dirty.ensured != 0 {
		t.Fatal(result, err)
	}
	if _, ok := dirty.changes["object:ready"]; ok {
		t.Fatal("ready peer was not acknowledged")
	}
	if !dirty.retry["object:missing"].Equal(dirty.now.Add(15 * time.Minute)) {
		t.Fatal("missing identity lacks bounded backoff")
	}
	dirty.mark("missing")
	if ready, _, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || !ready {
		t.Fatal("new evidence did not bypass old retry", err)
	}
}

func TestNotesProjectionRuntimeUncertainScopeRequiresRepair(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	dirty.mark("a")
	projection.apply = func() error { return notesprojection.ErrProjectionReconcileRequired }
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || dirty.ensured != 1 || dirty.changes["full"].Sequence != 1 {
		t.Fatal(result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	result, err = r.RunOnce(t.Context(), run)
	if err != nil || projection.fullCalls != 1 || result.Counters["full_reconcile"] != 1 {
		t.Fatal(result, err)
	}
	if _, ok := dirty.changes["full"]; ok {
		t.Fatal("successful complete repair not acknowledged")
	}
	if _, ok := dirty.changes["object:a"]; !ok {
		t.Fatal("future identity retry was falsely acknowledged by repair")
	}
}

func TestNotesProjectionMixedFailuresRetainRequiredRepair(t *testing.T) {
	r, run, dirty, projection, _ := projectionRuntimeFixture()
	dirty.mark("a")
	dirty.mark("b")
	projection.applyIDs = func(ids []string) error {
		if len(ids) > 1 || ids[0] == "b" {
			return notesprojection.ErrDeltaUncertain
		}
		return notesprojection.ErrProjectionReconcileRequired
	}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || dirty.ensured != 1 || result.Counters["failed"] != 2 {
		t.Fatal(result, err)
	}
}

func TestNotesProjectionRuntimeFirstRepairFailureAndCancellation(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	*saved = nil
	run.Checkpoints = map[string]workers.WorkerCheckpoint{}
	projection.rebuildError = errors.New("incomplete inventory")
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["failed"] != 1 || len(dirty.changes) != 1 {
		t.Fatal(result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	if ready, next, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now); err != nil || ready || !next.Equal(dirty.now.Add(30*time.Second)) {
		t.Fatal("failed first repair spun", err)
	}
	dirty.now = dirty.now.Add(30 * time.Second)
	projection.rebuildError = nil
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.RunOnce(ctx, run); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation acknowledged: %v", err)
	}
	if len(dirty.changes) != 1 {
		t.Fatal("cancelled full repair lost dirty work")
	}
}

func TestNotesProjectionStructuralFailureIsMetadataOnlyAcrossRestartAndExpiry(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	dirty.mark("")
	projection.rebuildError = &notesprojection.InvalidSourcePathsError{Dependencies: []notesprojection.FailureDependency{{ObjectID: "bad", Revision: "old"}}}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Status != workers.RunStatusFailed || result.Counters["failed"] != 1 || projection.fullCalls != 1 {
		t.Fatalf("failure was not retained: %+v %v", result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	if ready, next, err := r.AutomaticReady(t.Context(), run.Instance, dirty.now); ready || err != nil || !next.Equal(dirty.now.Add(6*time.Hour)) {
		t.Fatalf("structural failure runnable: %t %v %v", ready, next, err)
	}
	dirty.now = dirty.now.Add(6 * time.Hour)
	// A new process has only the saved checkpoint and the original dirty work.
	restarted := NewNotesProjectionRuntime(dirty, nil)
	restarted.Projection, restarted.LoadCheckpoint, restarted.Now = projection, r.LoadCheckpoint, r.Now
	result, err = restarted.RunOnce(t.Context(), run)
	var summary map[string]any
	_ = json.Unmarshal(result.ResultSummary, &summary)
	if err != nil || projection.fullCalls != 1 || projection.probes != 1 || summary["metadata_only_hold"] != true || len(dirty.changes) != 1 {
		t.Fatalf("expiry reread corpus or lost held work: %+v %v", result, err)
	}
	if !dirty.retry["full"].Equal(dirty.now.Add(6 * time.Hour)) {
		t.Fatal("expired failure inherited the unrelated daily repair deadline")
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	dirty.mark("unrelated")
	if result, err := restarted.RunOnce(t.Context(), run); err != nil || result.Counters["completed"] != 1 || projection.probes != 1 || projection.fullCalls != 1 {
		t.Fatalf("unrelated edit invalidated failure: %+v %v", result, err)
	}
	// A real change to the offending source wakes the exact full mark.
	projection.probeChanged = true
	dirty.mark("bad")
	projection.apply = func() error { return notesprojection.ErrProjectionReconcileRequired }
	result, err = restarted.RunOnce(t.Context(), run)
	if err != nil || dirty.retry["full"].After(dirty.now) {
		t.Fatalf("repaired dependency did not wake full mark: %+v %v", result, err)
	}
	*saved = result.CheckpointUpdates[0].Value
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
	projection.rebuildError = nil
	result, err = restarted.RunOnce(t.Context(), run)
	if err != nil || projection.fullCalls != 2 || result.Counters["completed"] != 1 {
		t.Fatalf("real repair did not resume: %+v %v", result, err)
	}
	if _, ok := dirty.changes["object:bad"]; !ok {
		t.Fatal("full refresh falsely acknowledged dependent edit")
	}
}

func TestNotesProjectionRepeatedUnknownFailureBacksOff(t *testing.T) {
	r, run, dirty, projection, saved := projectionRuntimeFixture()
	dirty.mark("")
	projection.rebuildError = errors.New("private path must not be exposed")
	for _, delay := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute} {
		result, err := r.RunOnce(t.Context(), run)
		if err != nil || result.Status != workers.RunStatusFailed || !dirty.retry["full"].Equal(dirty.now.Add(delay)) {
			t.Fatalf("unknown failure did not back off: %+v %v", result, err)
		}
		*saved = result.CheckpointUpdates[0].Value
		run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: *saved}
		dirty.now = dirty.now.Add(delay)
	}
}
