package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/workers"
)

type contextSourcesFixture struct {
	ids   []string
	after []string
}

func (f *contextSourcesFixture) ListProjectContextSources(_ context.Context, owner, after string, limit int) ([]string, error) {
	if owner != "main" || limit != 20 {
		return nil, errors.New("wrong scope")
	}
	f.after = append(f.after, after)
	result := []string{}
	for _, id := range f.ids {
		if id > after && len(result) < limit {
			result = append(result, id)
		}
	}
	return result, nil
}

type contextRefreshFixture struct {
	seen      []string
	failures  map[string]bool
	onRefresh func()
}

func (f *contextRefreshFixture) RefreshProject(_ context.Context, id string, only bool) (provenance.ProjectProjectionSyncReceipt, error) {
	if !only {
		return provenance.ProjectProjectionSyncReceipt{}, errors.New("full observation invoked")
	}
	f.seen = append(f.seen, id)
	if f.onRefresh != nil {
		f.onRefresh()
	}
	if f.failures[id] {
		return provenance.ProjectProjectionSyncReceipt{}, errors.New("unavailable local source")
	}
	return provenance.ProjectProjectionSyncReceipt{ProjectSnapshotID: "33333333-3333-4333-8333-333333333333"}, nil
}

type contextChangesFixture struct {
	ids        []string
	generation string
}

func (f *contextChangesFixture) Pending(_ context.Context, observed map[string]projects.ProjectContextObservation, _ time.Time, force bool) ([]projects.ProjectContextChange, *time.Time, error) {
	result := []projects.ProjectContextChange{}
	for _, id := range f.ids {
		if force || observed[id].Generation != f.generation {
			result = append(result, projects.ProjectContextChange{ProjectID: id, Generation: f.generation, Reason: "source_or_file_changed"})
		}
	}
	return result, nil, nil
}

func TestProjectContextRefreshReadinessFairPageAndIdle(t *testing.T) {
	sources := &contextSourcesFixture{}
	for i := 0; i < 23; i++ {
		sources.ids = append(sources.ids, fmt.Sprintf("project_01ARZ3NDEKTSV4RRFFQ69G5F%02d", i))
	}
	changes := &contextChangesFixture{ids: sources.ids, generation: "first"}
	refresh := &contextRefreshFixture{}
	var saved json.RawMessage
	r := ProjectContextRefreshRuntime{Sources: sources, Refresh: refresh, LocalNode: "main", Changes: changes,
		LoadCheckpoint: func(context.Context, string) (json.RawMessage, error) { return saved, nil }}
	run := workers.RunContext{Instance: workers.WorkerInstance{TickPolicyJSON: r.Describe().DefaultTickPolicyJSON}, Run: workers.WorkerRun{TriggerKind: workers.TriggerSupervisorTick}, Checkpoints: map[string]workers.WorkerCheckpoint{}}
	for _, want := range []int64{20, 3} {
		ready, _, err := r.AutomaticReady(t.Context(), run.Instance, time.Now())
		if err != nil || !ready {
			t.Fatal("pending page not ready", ready, err)
		}
		result, err := r.RunOnce(t.Context(), run)
		if err != nil || result.Counters["attempted"] != want {
			t.Fatal(result, err)
		}
		saved = result.CheckpointUpdates[0].Value
		run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: saved}
	}
	ready, _, err := r.AutomaticReady(t.Context(), run.Instance, time.Now())
	if err != nil || ready || len(refresh.seen) != 23 {
		t.Fatal("unchanged generation created another automatic refresh", ready, err, refresh.seen)
	}
	// Explicit runs still observe sources even when automatic readiness is idle.
	run.Run.TriggerKind = workers.TriggerManual
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["attempted"] != 20 {
		t.Fatal("manual refresh was suppressed", result, err)
	}
}

func TestProjectContextRefreshGenerationDuringReadAndCancellation(t *testing.T) {
	id := "project_01ARZ3NDEKTSV4RRFFQ69G5FAX"
	changes := &contextChangesFixture{ids: []string{id}, generation: "selected"}
	refresh := &contextRefreshFixture{onRefresh: func() { changes.generation = "newer" }}
	r := ProjectContextRefreshRuntime{Sources: &contextSourcesFixture{ids: []string{id}}, Refresh: refresh, LocalNode: "main", Changes: changes}
	run := workers.RunContext{Instance: workers.WorkerInstance{TickPolicyJSON: r.Describe().DefaultTickPolicyJSON}, Run: workers.WorkerRun{TriggerKind: workers.TriggerSupervisorTick}}
	result, err := r.RunOnce(t.Context(), run)
	if err != nil || result.Counters["dirty"] != 1 {
		t.Fatal("concurrent edit was acknowledged", result, err)
	}
	checkpoint, err := readProjectContextCheckpoint(result.CheckpointUpdates[0].Value)
	if err != nil || checkpoint.Observations[id].Generation != "selected" {
		t.Fatal("acknowledgement advanced beyond selected generation", checkpoint, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	refresh.onRefresh = cancel
	result, err = r.RunOnce(ctx, run)
	if !errors.Is(err, context.Canceled) || len(result.CheckpointUpdates) != 0 {
		t.Fatal("cancelled refresh published acknowledgement", result, err)
	}
	if _, err := readProjectContextCheckpoint(json.RawMessage(`{"after_project_id":""}`)); err != nil {
		t.Fatal("legacy checkpoint rejected", err)
	}
}
func TestProjectContextRefreshFairCursorAndNoAgent(t *testing.T) {
	sources := &contextSourcesFixture{}
	for i := 0; i < 23; i++ {
		sources.ids = append(sources.ids, fmt.Sprintf("project_01ARZ3NDEKTSV4RRFFQ69G5F%02d", i))
	}
	refresh := &contextRefreshFixture{failures: map[string]bool{sources.ids[0]: true}}
	r := ProjectContextRefreshRuntime{Sources: sources, Refresh: refresh, LocalNode: "main"}
	registry := workers.NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	d := r.Describe()
	run := workers.RunContext{Instance: workers.WorkerInstance{TickPolicyJSON: d.DefaultTickPolicyJSON}, Checkpoints: map[string]workers.WorkerCheckpoint{}}
	first, err := r.RunOnce(t.Context(), run)
	if err != nil || first.Counters["attempted"] != 20 || first.Counters["unavailable"] != 1 || len(refresh.seen) != 20 {
		t.Fatalf("first %+v %v", first, err)
	}
	if first.NextRunAfter == nil || first.Retryable {
		t.Fatal("missing normal deterministic cadence")
	}
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: first.CheckpointUpdates[0].Value}
	second, err := r.RunOnce(t.Context(), run)
	if err != nil || second.Counters["attempted"] != 3 || len(refresh.seen) != 23 {
		t.Fatalf("restart %+v %v", second, err)
	}
	run.Checkpoints["default"] = workers.WorkerCheckpoint{CheckpointJSON: second.CheckpointUpdates[0].Value}
	third, err := r.RunOnce(t.Context(), run)
	if err != nil || third.Counters["attempted"] != 20 || refresh.seen[23] != sources.ids[0] {
		t.Fatalf("wrap %+v %v", third, err)
	}
	var attempts struct {
		Attempts []projectContextAttempt `json:"attempts"`
	}
	if json.Unmarshal(first.ResultSummary, &attempts) != nil || attempts.Attempts[0].Status != "unavailable" {
		t.Fatal("failure evidence lost")
	}
	archivist := NewProvenanceArchivistRuntime(nil).Describe()
	policy, err := workers.ParseTickPolicy(archivist.DefaultTickPolicyJSON)
	if err != nil || policy.Mode != workers.TickModeManual {
		t.Fatal("archivist schedule changed")
	}
}
