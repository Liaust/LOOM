package runtimes

import (
	"context"
	"encoding/json"
	"errors"
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
		t.Fatal("admission pages must get bounded continuation", next)
	}
	policy.Mode = workers.TickModeManual
	if next := knowledgeIndexerNextRun(policy, true, true, now); next != nil {
		t.Fatal("backlog must not activate manual scheduling", next)
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
			if saved != cursor || limit != 200 {
				t.Fatal("admission input changed")
			}
			<-ctx.Done()
			return knowledge.AdmissionResult{Cursor: knowledge.AdmissionCursor{SyncedDone: true}, Applied: 3}, ctx.Err()
		})
	if err != nil || !timedOut || result.Cursor != cursor || !result.MoreWork || result.Applied != 0 || t.Context().Err() != nil {
		t.Fatalf("timeout consumed processing context or advanced cursor: %+v %t %v", result, timedOut, err)
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
		if result != want || timedOut || !errors.Is(err, admissionErr) {
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
