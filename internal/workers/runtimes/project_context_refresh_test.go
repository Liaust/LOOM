package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

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
	seen     []string
	failures map[string]bool
}

func (f *contextRefreshFixture) RefreshProject(_ context.Context, id string, only bool) (provenance.ProjectProjectionSyncReceipt, error) {
	if !only {
		return provenance.ProjectProjectionSyncReceipt{}, errors.New("full observation invoked")
	}
	f.seen = append(f.seen, id)
	if f.failures[id] {
		return provenance.ProjectProjectionSyncReceipt{}, errors.New("unavailable local source")
	}
	return provenance.ProjectProjectionSyncReceipt{ProjectSnapshotID: "33333333-3333-4333-8333-333333333333"}, nil
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
