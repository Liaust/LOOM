package runtimes

import (
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/workers"
)

func TestNotesWorkspaceManualUnconfigured(t *testing.T) {
	r := NotesWorkspaceRuntime{}
	if err := workers.NewRegistry().Register(r); err != nil {
		t.Fatal(err)
	}
	if err := r.ValidateConfig(t.Context(), r.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	policy, err := workers.ParseTickPolicy(r.Describe().DefaultTickPolicyJSON)
	if err != nil || policy.Mode != "manual" {
		t.Fatalf("default policy: %+v %v", policy, err)
	}
	if _, err := r.RunOnce(t.Context(), workers.RunContext{}); err == nil {
		t.Fatal("unconfigured runtime succeeded")
	}
	if err := r.ValidateConfig(t.Context(), json.RawMessage(`{"schema_version":"notes_workspace.config.v1","command":"arbitrary"}`)); err == nil {
		t.Fatal("worker config expanded runtime authority")
	}
}

func TestNotesWorkspaceLiveWindowCadence(t *testing.T) {
	p, err := workers.ParseTickPolicy(json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	now := start.Add(30 * time.Second)
	if next := notesWorkspaceNextRun(p, 30, "completed", start, now); next == nil || !next.Equal(start.Add(5*time.Second)) {
		t.Fatal("live window added a trailing delay")
	}
	if next := notesWorkspaceNextRun(p, 30, "recovery_paused", start, now); next == nil || !next.Equal(now.Add(5*time.Second)) {
		t.Fatal("recovery pause busy-looped")
	}
	p.Mode = workers.TickModeManual
	if notesWorkspaceNextRun(p, 30, "completed", start, now) != nil {
		t.Fatal("manual schedule activated")
	}
}
