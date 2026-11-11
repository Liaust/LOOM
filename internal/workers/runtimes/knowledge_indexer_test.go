package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/workers"
)

func TestKnowledgeIndexerBacklogCadence(t *testing.T) {
	now := time.Now().UTC()
	policy := workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 60}
	if next := knowledgeIndexerNextRun(policy, false, false, now); next == nil || next.Sub(now) != time.Minute {
		t.Fatal("idle inventory must retain configured cadence", next)
	}
	if next := knowledgeIndexerNextRun(policy, true, false, now); next == nil || next.Sub(now) != time.Duration(workers.MinimumIntervalSeconds)*time.Second {
		t.Fatal("pipeline backlog must get bounded continuation", next)
	}
	if next := knowledgeIndexerNextRun(policy, false, true, now); next == nil || next.Sub(now) != time.Duration(workers.MinimumIntervalSeconds)*time.Second {
		t.Fatal("useful admission must get bounded continuation", next)
	}
	policy.Mode = workers.TickModeManual
	if next := knowledgeIndexerNextRun(policy, true, true, now); next != nil {
		t.Fatal("backlog must not activate manual scheduling", next)
	}
}

func TestKnowledgeIndexerAdmissionContinuation(t *testing.T) {
	complete := knowledgeAdmissionSweepCheckpoint{SourceGeneration: "generation", SweepStarted: true, Complete: true}
	for _, tc := range []struct {
		name       string
		admission  knowledge.AdmissionResult
		wantFast   bool
		wantReason string
	}{
		{"idle", knowledge.AdmissionResult{}, false, "idle"},
		{"unchanged partial inventory", knowledge.AdmissionResult{MoreWork: true, InventoryMoreWork: true, Applied: 200}, false, "inventory_reconcile"},
		{"mixed inventory and deliveries", knowledge.AdmissionResult{MoreWork: true, InventoryMoreWork: true, PriorityMoreWork: true, PriorityApplied: 1}, true, "priority_delivery"},
		{"delivery backlog only", knowledge.AdmissionResult{MoreWork: true, PriorityMoreWork: true, PriorityApplied: 200}, true, "priority_delivery"},
		{"excluded priority page", knowledge.AdmissionResult{MoreWork: true, PriorityMoreWork: true, PrioritySyncedObserved: 200, Skipped: 200}, false, "idle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.admission.SourceGeneration = "generation"
			state, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, complete, tc.admission, false)
			if state != complete || fast != tc.wantFast || reason != tc.wantReason {
				t.Fatalf("continuation: %+v fast=%t reason=%s", state, fast, reason)
			}
			now := time.Now().UTC()
			next := knowledgeIndexerNextRun(workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 60}, false, fast, now)
			want := time.Minute
			if tc.wantFast {
				want = time.Duration(workers.MinimumIntervalSeconds) * time.Second
			}
			if next == nil || next.Sub(now) != want {
				t.Fatalf("next run: %v want %v", next, want)
			}
		})
	}
}

func TestKnowledgeIndexerInitialInventoryAndGeneration(t *testing.T) {
	var state knowledgeAdmissionSweepCheckpoint
	page := knowledge.AdmissionResult{SourceGeneration: "first", MoreWork: true, InventoryMoreWork: true}
	state, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, state, page, false)
	if !state.SweepStarted || state.Complete || !fast || reason != "initial_inventory" {
		t.Fatal(state, fast, reason)
	}
	cursor := knowledge.AdmissionCursor{StorageAfter: "next", SyncedDone: true}
	state, fast, _ = knowledgeAdmissionContinuation(cursor, state, page, false)
	if state.Complete || !fast {
		t.Fatal("initial import slowed", state, fast)
	}
	page.MoreWork, page.InventoryMoreWork = false, false
	state, fast, _ = knowledgeAdmissionContinuation(cursor, state, page, false)
	if !state.Complete || fast {
		t.Fatal("initial sweep did not finish", state, fast)
	}

	// A new configuration discovered halfway through a sweep cannot mark its
	// initial import complete until the next pass has seen the beginning too.
	page.SourceGeneration, page.MoreWork, page.InventoryMoreWork = "second", true, true
	state, fast, _ = knowledgeAdmissionContinuation(cursor, state, page, false)
	if state.SweepStarted || state.Complete || !fast {
		t.Fatal(state, fast)
	}
	page.MoreWork, page.InventoryMoreWork = false, false
	state, _, _ = knowledgeAdmissionContinuation(cursor, state, page, false)
	if state.Complete {
		t.Fatal("new root before cursor was lost")
	}
	state, fast, _ = knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, state, page, false)
	if !state.Complete || fast {
		t.Fatal("full generation sweep did not finish", state, fast)
	}
}

func TestKnowledgeIndexerAdmissionTimeoutRetainsSweep(t *testing.T) {
	previous := knowledgeAdmissionSweepCheckpoint{SourceGeneration: "old", SweepStarted: true}
	state, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, previous,
		knowledge.AdmissionResult{SourceGeneration: "new", MoreWork: true}, true)
	if state != previous || fast || reason != "admission_timeout" {
		t.Fatal(state, fast, reason)
	}
	now := time.Now().UTC()
	if next := knowledgeIndexerNextRun(workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 60}, true, fast, now); next == nil || next.Sub(now) != time.Duration(workers.MinimumIntervalSeconds)*time.Second {
		t.Fatal("admission timeout must not stall ready pipeline work", next)
	}
}

func TestKnowledgeIndexerUpgradePreservesOrdinaryInventoryCadence(t *testing.T) {
	page := knowledge.AdmissionResult{SourceGeneration: "existing", InventoryMoreWork: true, MoreWork: true}
	for _, tc := range []struct{ existing, timeout, complete bool }{
		{true, false, true}, {false, false, false}, {true, true, false},
	} {
		state := knowledgeAdmissionUpgradeBaseline(knowledgeAdmissionSweepCheckpoint{}, page, tc.existing, tc.timeout)
		if state.Complete != tc.complete {
			t.Fatal("incorrect upgrade baseline", state, tc)
		}
		if tc.complete {
			_, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{StorageAfter: "committed"}, state, page, false)
			if fast || reason != "inventory_reconcile" {
				t.Fatal("upgrade retriggered initial import", fast, reason)
			}
			page.SourceGeneration = "changed"
			_, fast, reason = knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, state, page, false)
			if !fast || reason != "initial_inventory" {
				t.Fatal("new configuration lost fast import", fast, reason)
			}
			page.SourceGeneration = "existing"
		}
	}
}

func TestKnowledgeIndexerRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	if runtime.Kind() != workers.KindKnowledgeIndexer {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindKnowledgeIndexer)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindKnowledgeIndexer {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindKnowledgeIndexer)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	if !descriptor.MayTouchFilesystem {
		t.Fatal("knowledge indexer should declare filesystem access")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.knowledge_indexer" {
		t.Fatalf("worker key = %q, want main.knowledge_indexer", instances[0].WorkerKey)
	}
}

type custodyConsumerFunc func(context.Context, string, int) (knowledge.NotesCustodyBatchResult, error)

func (f custodyConsumerFunc) ConsumeBatch(ctx context.Context, cursor string, limit int) (knowledge.NotesCustodyBatchResult, error) {
	return f(ctx, cursor, limit)
}

func TestKnowledgeCustodyCheckpointBoundary(t *testing.T) {
	called := false
	runtime := NewKnowledgeIndexerRuntime(nil)
	runtime.Custody = custodyConsumerFunc(func(_ context.Context, cursor string, limit int) (knowledge.NotesCustodyBatchResult, error) {
		called = true
		if cursor != "" || limit != 50 {
			t.Fatalf("consumer cursor/limit: %q/%d", cursor, limit)
		}
		return knowledge.NotesCustodyBatchResult{Wrapped: true}, nil
	})
	batch, err := runtime.consumeCustody(t.Context(), nil, 200)
	if err != nil || batch == nil || !batch.Wrapped || !called {
		t.Fatalf("bounded consumer: %+v %v", batch, err)
	}
	for _, raw := range []string{`null`, `[]`, `{}`, `{"schema_version":"old"}`,
		`{"schema_version":"notes_custody.cursor.v1","event_id":"","unexpected":1}`,
		`{"schema_version":"notes_custody.cursor.v1","event_id":""} {}`, strings.Repeat("x", 1025)} {
		called = false
		_, err := runtime.consumeCustody(t.Context(), map[string]workers.WorkerCheckpoint{"custody": {
			SchemaVersion: notesCustodyCheckpointSchema, CheckpointJSON: json.RawMessage(raw),
		}}, 1)
		if err == nil || called || strings.Contains(err.Error(), raw) {
			t.Fatalf("malformed checkpoint passed or leaked: %v, called=%v", err, called)
		}
	}
	runtime.Custody = nil
	batch, err = runtime.consumeCustody(t.Context(), map[string]workers.WorkerCheckpoint{"custody": {CheckpointJSON: json.RawMessage("stale")}}, 1)
	if err != nil || batch != nil {
		t.Fatal("disabled consumer changed existing coordinator behavior", err)
	}
}

func TestKnowledgeCustodyErrorsStayBounded(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	runtime.Custody = custodyConsumerFunc(func(context.Context, string, int) (knowledge.NotesCustodyBatchResult, error) {
		return knowledge.NotesCustodyBatchResult{}, errors.New("private database/path/cause")
	})
	_, err := runtime.consumeCustody(t.Context(), nil, 1)
	if err == nil || err.Error() != "Notes custody batch unavailable" {
		t.Fatalf("unbounded consumer error: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = runtime.consumeCustody(ctx, nil, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestKnowledgeIndexerRuntimeValidateConfig(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestParseKnowledgeIndexerConfigDefaultsAndBounds(t *testing.T) {
	config, err := parseKnowledgeIndexerConfig(json.RawMessage(`{"batch_size":500,"retry":{"max_attempts":0}}`))
	if err != nil {
		t.Fatalf("parseKnowledgeIndexerConfig returned error: %v", err)
	}
	if config.BatchSize != 200 {
		t.Fatalf("batch size = %d, want clamp to 200", config.BatchSize)
	}
	if config.MaxObjectsPerRun != 200 {
		t.Fatalf("max objects = %d, want default 200", config.MaxObjectsPerRun)
	}
	if config.MaxTextBytesPerObject != 5*1024*1024 {
		t.Fatalf("max text bytes = %d, want 5 MiB", config.MaxTextBytesPerObject)
	}
	if config.MaxExtractedTextBytes != 10*1024*1024 {
		t.Fatalf("max extracted bytes = %d, want 10 MiB", config.MaxExtractedTextBytes)
	}
	if config.Retry.MaxAttempts != 3 {
		t.Fatalf("retry attempts = %d, want default 3", config.Retry.MaxAttempts)
	}
	if config.LeaseDurationSeconds != 120 {
		t.Fatalf("lease seconds = %d, want 120", config.LeaseDurationSeconds)
	}
}

func TestKnowledgeIndexerRetainsExplicitSmallBatch(t *testing.T) {
	config, err := parseKnowledgeIndexerConfig(json.RawMessage(`{"batch_size":12,"max_objects_per_run":7}`))
	if err != nil || config.BatchSize != 12 || config.MaxObjectsPerRun != 7 {
		t.Fatalf("explicit bounds changed: %+v %v", config, err)
	}
}

func TestKnowledgeIndexerHeadingDenseNotesBudget(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	for _, raw := range []json.RawMessage{runtime.DefaultConfig(), json.RawMessage(`{}`)} {
		config, err := parseKnowledgeIndexerConfig(raw)
		if err != nil || config.MaxChunksPerObject != 2048 {
			t.Fatalf("default chunk budget: %+v %v", config, err)
		}
		service := &knowledge.Service{}
		object := knowledge.KnowledgeObject{FileClass: storagecatalog.FileClassMarkdown}
		extraction, err := service.BuildTextPipelineExtraction(object,
			strings.Repeat("# Section\n\nSmall paragraph.\n\n", 1434), knowledge.ChunkerOptions{})
		if err != nil || len(extraction.Chunks) != 1434 || len(extraction.Chunks) > config.MaxChunksPerObject {
			t.Fatalf("heading-dense note not admitted: chunks=%d err=%v", len(extraction.Chunks), err)
		}
	}
	config, err := parseKnowledgeIndexerConfig(json.RawMessage(`{"max_chunks_per_object":1000}`))
	if err != nil || config.MaxChunksPerObject != 1000 {
		t.Fatalf("explicit chunk budget changed: %+v %v", config, err)
	}
}

func TestProjectionRefreshCheckpointUsesWorkerJSONObject(t *testing.T) {
	raw := mustWorkerJSON(projectionRefreshCheckpoint{})
	if _, err := workers.JSONObject(raw, "checkpoint_json"); err != nil {
		t.Fatal(err)
	}
	var decoded projectionRefreshCheckpoint
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
}

func TestKnowledgeIndexerRunOnceRequiresService(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	_, err := runtime.RunOnce(context.Background(), workers.RunContext{})
	if err == nil {
		t.Fatal("RunOnce returned nil error without knowledge service")
	}
}

func TestKnowledgeAdmissionBudgetRetainsCursorAndProcessingContext(t *testing.T) {
	cursor := knowledge.AdmissionCursor{StorageAfter: "catalog", SyncedAfter: [3]string{"root", "version", "replica"}, StorageDone: true}
	result, timedOut, err := admitKnowledgeNotes(t.Context(), cursor, 200, time.Millisecond,
		func(ctx context.Context, saved knowledge.AdmissionCursor, limit int) (knowledge.AdmissionResult, error) {
			if !reflect.DeepEqual(saved, cursor) || limit != 200 {
				t.Fatal("admission input changed")
			}
			<-ctx.Done()
			return knowledge.AdmissionResult{Cursor: knowledge.AdmissionCursor{SyncedDone: true}, Applied: 3}, ctx.Err()
		})
	if err != nil || !timedOut || !reflect.DeepEqual(result.Cursor, cursor) || !result.MoreWork || result.Applied != 3 || t.Context().Err() != nil {
		t.Fatalf("timeout consumed processing context or advanced cursor: %+v %t %v", result, timedOut, err)
	}
}

func TestKnowledgeAdmissionTimeoutPreservesOnlyCommittedConfig(t *testing.T) {
	now := time.Now().UTC()
	cursor := knowledge.AdmissionCursor{
		Incremental: true, StorageAfter: "saved-storage", StorageDone: true,
		SyncedAfter:        [3]string{"root", "saved-version", "saved-replica"},
		PriorityAfter:      [3]string{"root", "saved-priority", "saved-replica"},
		RootConfigRevision: "saved-config", NextRootReconcileAt: now,
		RootCursors: map[string]knowledge.AdmissionRootCursor{
			"root": {Generation: "saved-generation", StorageAfter: "saved-root-storage"},
		},
	}
	for _, revision := range []string{"committed-config", "saved-config", ""} {
		t.Run(revision, func(t *testing.T) {
			tentative := knowledge.AdmissionCursor{
				StorageDone: true, SyncedDone: true,
				PriorityAfter:      [3]string{"root", "uncommitted-priority", "replica"},
				RootConfigRevision: revision, NextRootReconcileAt: now.Add(15 * time.Minute),
				RootCursors: map[string]knowledge.AdmissionRootCursor{
					"root": {Generation: "uncommitted-generation", StorageDone: true, SyncedDone: true},
				},
			}
			result, timedOut, err := admitKnowledgeNotes(t.Context(), cursor, 25, time.Millisecond,
				func(ctx context.Context, _ knowledge.AdmissionCursor, _ int) (knowledge.AdmissionResult, error) {
					<-ctx.Done()
					return knowledge.AdmissionResult{Cursor: tentative, Applied: 3}, ctx.Err()
				})
			want := cursor
			if revision != "" {
				want.RootConfigRevision, want.NextRootReconcileAt = revision, tentative.NextRootReconcileAt
			}
			if err != nil || !timedOut || !result.MoreWork || result.Applied != 3 || !reflect.DeepEqual(result.Cursor, want) {
				t.Fatalf("timeout lost authority or published tentative positions: %+v timeout=%t err=%v", result, timedOut, err)
			}
			if cursor.RootConfigRevision != "saved-config" || cursor.RootCursors["root"].StorageDone {
				t.Fatal("timeout changed its input checkpoint", cursor)
			}
		})
	}
}

func TestKnowledgeAdmissionCheckpointsRetainConfigWithoutTimeoutAcknowledgement(t *testing.T) {
	cursor := knowledge.AdmissionCursor{Incremental: true, RootConfigRevision: "committed-config", NextRootReconcileAt: time.Now().UTC()}
	sweep := knowledgeAdmissionSweepCheckpoint{SourceGeneration: "saved-generation", AdmissionRevision: "saved-revision"}
	for _, timedOut := range []bool{false, true} {
		updates := knowledgeAdmissionCheckpoints(cursor, sweep, timedOut)
		wantCount := 2
		if timedOut {
			wantCount = 1
		}
		if len(updates) != wantCount || updates[0].Key != "admission" || updates[0].SchemaVersion != "knowledge_admission.cursor.v1" {
			t.Fatalf("incorrect checkpoint selection: %+v", updates)
		}
		var saved knowledge.AdmissionCursor
		if err := json.Unmarshal(updates[0].Value, &saved); err != nil || !reflect.DeepEqual(saved, cursor) {
			t.Fatalf("lost committed config checkpoint: %+v %v", saved, err)
		}
		if !timedOut {
			var savedSweep knowledgeAdmissionSweepCheckpoint
			if updates[1].Key != "admission_schedule" || updates[1].SchemaVersion != "knowledge_admission.schedule.v1" {
				t.Fatal("successful admission lost schedule acknowledgement", updates)
			}
			if err := json.Unmarshal(updates[1].Value, &savedSweep); err != nil || savedSweep != sweep {
				t.Fatalf("changed schedule checkpoint: %+v %v", savedSweep, err)
			}
		}
	}
}

func TestKnowledgeAdmissionPipelineYieldPreservesInventoryAcknowledgement(t *testing.T) {
	before := knowledgeAdmissionSweepCheckpoint{SourceGeneration: "old", Complete: true, AdmissionRevision: "unacknowledged-arrivals", NextInventoryAt: time.Now().Add(time.Minute)}
	after, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, before, knowledge.AdmissionResult{SourceGeneration: "new", InventoryDeferred: true}, false)
	if !reflect.DeepEqual(before, after) || !fast || reason != "pipeline_priority" {
		t.Fatalf("pipeline yield consumed source/inventory acknowledgement: %+v %t %s", after, fast, reason)
	}
}

func TestKnowledgeScopedRepairDoesNotBecomeAnotherFastImport(t *testing.T) {
	before := knowledgeAdmissionSweepCheckpoint{SourceGeneration: "same", Complete: true}
	page := knowledge.AdmissionResult{Cursor: knowledge.AdmissionCursor{Incremental: true}, SourceGeneration: "same", InventoryMoreWork: true}
	after, fast, reason := knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, before, page, false)
	if !after.Complete || fast || reason != "inventory_reconcile" {
		t.Fatalf("unchanged repair was promoted to an import: %+v %t %s", after, fast, reason)
	}
	page.SourceGeneration = "changed-root"
	after, fast, _ = knowledgeAdmissionContinuation(knowledge.AdmissionCursor{}, before, page, false)
	if after.Complete || !fast {
		t.Fatal("changed root lost prompt bounded reconciliation")
	}
}

func TestKnowledgeAdmissionBudgetPreservesSuccessAndFailures(t *testing.T) {
	want := knowledge.AdmissionResult{Cursor: knowledge.AdmissionCursor{StorageAfter: "next"}, Applied: 2}
	failure := errors.New("database failure")
	for _, admissionErr := range []error{nil, failure, context.DeadlineExceeded} {
		result, timedOut, err := admitKnowledgeNotes(t.Context(), knowledge.AdmissionCursor{}, 2, time.Second,
			func(context.Context, knowledge.AdmissionCursor, int) (knowledge.AdmissionResult, error) {
				return want, admissionErr
			})
		if !reflect.DeepEqual(result, want) || timedOut || !errors.Is(err, admissionErr) {
			t.Fatalf("success or unrelated failure changed: %+v %t %v", result, timedOut, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, timedOut, err := admitKnowledgeNotes(ctx, knowledge.AdmissionCursor{}, 2, time.Second,
		func(ctx context.Context, _ knowledge.AdmissionCursor, _ int) (knowledge.AdmissionResult, error) {
			return want, ctx.Err()
		})
	if timedOut || !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation was swallowed: %t %v", timedOut, err)
	}
}

func TestKnowledgeAdmissionPhaseWarningPreservesCommittedWork(t *testing.T) {
	cursor := knowledge.AdmissionCursor{StorageAfter: "saved", InventoryRootAfter: "committed-root", PriorityAfter: [3]string{"root", "version", "replica"}}
	result, timedOut, err := admitKnowledgeNotes(t.Context(), cursor, 20, time.Millisecond,
		func(ctx context.Context, _ knowledge.AdmissionCursor, _ int) (knowledge.AdmissionResult, error) {
			<-ctx.Done()
			return knowledge.AdmissionResult{Applied: 3, PriorityApplied: 2, Cursor: knowledge.AdmissionCursor{StorageDone: true, InventoryRootAfter: "tentative-root"}},
				&knowledge.AdmissionPhaseError{Phase: "catalog_hydration", Elapsed: time.Millisecond, Budget: time.Millisecond, Retryable: true, Cause: ctx.Err()}
		})
	if err != nil || !timedOut || !reflect.DeepEqual(result.Cursor, cursor) || result.Applied != 3 || result.PriorityApplied != 2 || !result.MoreWork || result.Warning == nil {
		t.Fatalf("lost partial receipts/cursor/qualification: %+v timeout=%t err=%v", result, timedOut, err)
	}
	if result.Warning.Phase != "catalog_hydration" || result.Warning.Reason != "deadline_exceeded" || !result.Warning.Retryable || len(knowledgeAdmissionWarnings(result)) != 1 {
		t.Fatal("missing phase warning", result.Warning)
	}
	if knowledgeAdmissionWarnings(knowledge.AdmissionResult{}) != nil {
		t.Fatal("healthy no-work emits warnings")
	}
}

func TestKnowledgeAdmissionParentCancellationPreservesPhase(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, timedOut, err := admitKnowledgeNotes(ctx, knowledge.AdmissionCursor{}, 2, time.Second,
		func(ctx context.Context, _ knowledge.AdmissionCursor, _ int) (knowledge.AdmissionResult, error) {
			return knowledge.AdmissionResult{}, &knowledge.AdmissionPhaseError{Phase: "inventory_selection", Cause: ctx.Err()}
		})
	var phaseErr *knowledge.AdmissionPhaseError
	if timedOut || !errors.Is(err, context.Canceled) || !errors.As(err, &phaseErr) || phaseErr.Phase != "inventory_selection" {
		t.Fatalf("parent cancellation lost phase/identity: timeout=%t err=%v", timedOut, err)
	}
}

func TestKnowledgeAdmissionWarningResultContract(t *testing.T) {
	warning := &knowledge.AdmissionPhaseWarning{Phase: "catalog_selection", Reason: "deadline_exceeded", ElapsedMS: 10, BudgetMS: 9, Retryable: true}
	for _, partial := range []bool{false, true} {
		admission := knowledge.AdmissionResult{Applied: 2}
		if partial {
			admission.Warning = warning
		}
		raw := knowledgeIndexerResultSummary(knowledge.KnowledgeIndexerRunResult{Processed: 3}, partial, 100, admission, "pipeline_backlog", nil)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for _, legacy := range []string{"processed", "admission_timed_out", "admission_page_limit", "admission", "continuation_reason"} {
			if _, ok := fields[legacy]; !ok {
				t.Fatalf("lost legacy %s: %s", legacy, raw)
			}
		}
		if partial {
			var got knowledge.AdmissionPhaseWarning
			if err := json.Unmarshal(fields["admission_warning"], &got); err != nil || got != *warning {
				t.Fatalf("admission_warning contract=%+v err=%v", got, err)
			}
		} else if _, ok := fields["admission_warning"]; ok {
			t.Fatal("complete run fabricated a diagnostic", string(raw))
		}
	}
}

func TestKnowledgeAdmissionPageLimitBackoffAndReplay(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		limit int
		want  int
	}{
		{`{}`, 200, 200},
		{`{"admission_timed_out":true}`, 200, 100},
		{`{"admission_timed_out":true,"admission_page_limit":100}`, 200, 50},
		{`{"admission_timed_out":false,"admission_page_limit":50}`, 200, 50},
		{`{"admission_timed_out":false,"admission_page_limit":1}`, 200, 1},
		{`{"admission_timed_out":false,"admission_page_limit":100,"admission_success_streak":3,"admission_elapsed_ms":8000,"admission_budget_ms":10000}`, 200, 100},
		{`{"admission_timed_out":false,"admission_page_limit":100,"admission_success_streak":3,"admission_elapsed_ms":2000,"admission_budget_ms":10000}`, 200, 200},
		{`{"admission_timed_out":true,"admission_page_limit":1}`, 200, 1},
		{`{"admission_timed_out":false,"admission_page_limit":50}`, 12, 12},
	} {
		checkpoints := map[string]workers.WorkerCheckpoint{"default": {CheckpointJSON: json.RawMessage(tc.raw)}}
		for range 2 {
			got, err := knowledgeAdmissionPageLimit(tc.limit, checkpoints)
			if err != nil || got != tc.want {
				t.Fatalf("page backoff/replay %s: %d %v", tc.raw, got, err)
			}
		}
	}
	if got, err := knowledgeAdmissionPageLimit(200, nil); err != nil || got != 200 {
		t.Fatal("new instance changed configured ceiling", got, err)
	}
	if _, err := knowledgeAdmissionPageLimit(200, map[string]workers.WorkerCheckpoint{"default": {CheckpointJSON: json.RawMessage("invalid")}}); err == nil {
		t.Fatal("invalid checkpoint accepted")
	}
}
