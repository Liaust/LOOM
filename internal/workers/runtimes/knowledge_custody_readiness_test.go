package runtimes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

type custodyRevisionTest struct{ revision string }

func (r custodyRevisionTest) CompletionRevision(context.Context) (string, error) {
	return r.revision, nil
}
func (r custodyRevisionTest) ConsumeBatch(context.Context, string, int) (knowledge.NotesCustodyBatchResult, error) {
	return knowledge.NotesCustodyBatchResult{}, nil
}

func TestCustodyWakeUsesFinalRevisionWithoutBusyRetry(t *testing.T) {
	now := time.Now().UTC()
	saved := custodyScheduleCheckpoint{Revision: "same", RetryAt: now.Add(15 * time.Minute)}
	raw, _ := json.Marshal(saved)
	checkpoints := map[string]workers.WorkerCheckpoint{"custody_schedule": {CheckpointJSON: raw}}
	r := KnowledgeIndexerRuntime{Custody: custodyRevisionTest{"same"}}
	state, err := r.nextCustodySchedule(t.Context(), checkpoints)
	if err != nil || state.due(now) {
		t.Fatalf("unchanged failed evidence bypassed retry: %+v %v", state, err)
	}
	r.Custody = custodyRevisionTest{"new-final-event"}
	state, err = r.nextCustodySchedule(t.Context(), checkpoints)
	if err != nil || !state.due(now) || state.Revision != "new-final-event" {
		t.Fatalf("new final event stayed asleep: %+v %v", state, err)
	}
	state, err = r.nextCustodySchedule(t.Context(), nil)
	if err != nil || !state.due(now) {
		t.Fatalf("legacy checkpoint lost catch-up: %+v %v", state, err)
	}
}

func TestCustodyLateCompletionBehindCursorGetsOneWrap(t *testing.T) {
	now := time.Now().UTC()
	state := custodyScheduleCheckpoint{Revision: "late-final", Pending: true, WrapPending: true}
	state = state.afterBatch("later-workspace-event", true, now)
	if !state.due(now) {
		t.Fatal("late completion behind the old cursor was delayed until safety repair")
	}
	state = state.afterBatch("", false, now)
	if !state.due(now) || state.WrapPending {
		t.Fatal("fresh pass lost pending work or restarted repeatedly")
	}
	state = state.afterBatch("another-event", true, now)
	if state.due(now) {
		t.Fatal("wrapped failed historical rows caused a retry loop")
	}
}
