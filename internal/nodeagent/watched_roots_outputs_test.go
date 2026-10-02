package nodeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/response"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
)

func TestWatchedRootEmptyOutputPlanDoesNotOpenQueueTransaction(t *testing.T) {
	store := Store{DataDir: t.TempDir()}
	plan := watchedroots.OutputPlan{RootKey: "notes"}
	plan.Counts.AlreadyCurrent = 42
	got := applyWatchedRootOutputPlan(context.Background(), store, Config{}, State{}, watchedroots.NewStore(store.DataDir), watchedroots.ValidatedRoot{}, plan)
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("empty plan changed: %#v", got)
	}
	entries, err := os.ReadDir(store.DataDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty plan touched queue state: entries=%v err=%v", entries, err)
	}
}

func TestWatchedRootOutputPlanBoundedAndResumable(t *testing.T) {
	store, config, state, _ := localSyncObjectTestStore(t)
	ws := watchedroots.NewStore(store.DataDir)
	root := watchedroots.ValidatedRoot{Config: watchedroots.RootConfig{
		RootKey: "notes", Include: []string{"**/*"},
		BackupPolicy: watchedroots.BackupPolicy{Mode: watchedroots.BackupModeMetadataOnly},
	}}
	const total = watchedRootOutputBatchSize*2 + 3
	for i := 0; i < total; i++ {
		if err := ws.SavePathState(watchedroots.PathState{
			RootKey: "notes", RelativePath: fmt.Sprintf("dir-%03d", i),
			Kind:           watchedroots.PathKindDirectory,
			Classification: watchedroots.Classification{ReasonCode: watchedroots.ReasonExcludedDirectoryMetadata},
		}); err != nil {
			t.Fatal(err)
		}
	}
	for done := 0; done < total; {
		plan, err := watchedroots.PlanOutputs(ws, root, "test")
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Actions) != total-done {
			t.Fatalf("remaining actions = %d, want %d", len(plan.Actions), total-done)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		stopped := applyWatchedRootOutputPlan(cancelled, store, config, state, ws, root, plan)
		if len(stopped.Actions) != 0 || stopped.Counts.Deferred != total-done {
			t.Fatalf("cancelled plan mutated actions: %#v", stopped)
		}
		plan = applyWatchedRootOutputPlan(context.Background(), store, config, state, ws, root, plan)
		if len(plan.Actions) == 0 || len(plan.Actions) > watchedRootOutputBatchSize || plan.Counts.Failed != 0 {
			t.Fatalf("unbounded or failed plan: %#v", plan)
		}
		done += len(plan.Actions)
		if plan.Counts.Deferred != total-done {
			t.Fatalf("deferred = %d, want %d", plan.Counts.Deferred, total-done)
		}
		queued, err := store.LoadWatchedRootBackupOutbox()
		if err != nil || len(queued) != done {
			t.Fatalf("queue lost or duplicated actions: count=%d err=%v", len(queued), err)
		}
	}
	requests := 0
	if _, _, err := store.QueueWatchedRootBackupAction(config, state, watchedroots.OutputAction{
		ActionKind: watchedroots.OutputActionBackupMetadata,
		RootKey:    "other", RelativePath: "unrelated",
		BackupMode:     watchedroots.BackupModeMetadataOnly,
		BackupItemKind: mainwatchedroots.BackupItemKindDirectory,
	}, ""); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input mainwatchedroots.BackupBatchInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if input.RootKey != "notes" {
			t.Errorf("unrelated root delivered: %s", input.RootKey)
		}
		requests++
		_ = json.NewEncoder(w).Encode(response.Success("test", mainwatchedroots.BackupBatchResult{
			Batch: mainwatchedroots.BackupBatch{WatchedRootBackupBatchID: fmt.Sprintf("batch-%d", requests), Status: mainwatchedroots.BackupBatchStatusAccepted},
			Items: []mainwatchedroots.BackupItem{{LocalItemRef: input.Items[0].LocalItemRef, Status: mainwatchedroots.BackupItemStatusAccepted}},
		}))
	}))
	defer server.Close()
	config.MainURL = server.URL
	if flush := flushWatchedRootBackupsForRoot(context.Background(), store, config, state, "test", "empty"); flush.SubmittedItems != 0 || flush.PendingBefore != 0 || requests != 0 {
		t.Fatalf("empty root flushed unrelated work: %#v", flush)
	}
	for done := 0; done < total; {
		flush := flushWatchedRootBackupsForRoot(context.Background(), store, config, state, "test", "notes")
		if flush.SubmittedItems == 0 || flush.SubmittedItems > watchedRootOutputBatchSize {
			t.Fatalf("unbounded or stalled flush: %#v", flush)
		}
		done += flush.SubmittedItems
		if flush.PendingAfter != total-done || flush.AcceptedAfter != done {
			t.Fatalf("incorrect remaining queue: %#v", flush)
		}
		want := watchedroots.OutputStatusQueued
		if done == total {
			want = watchedroots.OutputStatusRecorded
		}
		if flush.Status != want {
			t.Fatalf("normal partial progress classified as %s, want %s: %#v", flush.Status, want, flush)
		}
	}
	other, err := store.LocalWatchedRootBackupStatus(config, state, "other")
	if err != nil || other.Counts.Pending != 1 || other.Counts.Accepted != 0 {
		t.Fatalf("unrelated queue changed: %#v, err=%v", other, err)
	}
}
