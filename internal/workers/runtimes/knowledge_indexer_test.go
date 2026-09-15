package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/workers"
)

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
	if config.MaxObjectsPerRun != 50 {
		t.Fatalf("max objects = %d, want default 50", config.MaxObjectsPerRun)
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

func TestKnowledgeIndexerRunOnceRequiresService(t *testing.T) {
	runtime := NewKnowledgeIndexerRuntime(nil)
	_, err := runtime.RunOnce(context.Background(), workers.RunContext{})
	if err == nil {
		t.Fatal("RunOnce returned nil error without knowledge service")
	}
}
