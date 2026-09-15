package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/search"
	"loom.local/loom/internal/workers"
)

func TestIndexerTextRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewIndexerTextRuntime(search.Service{})
	if runtime.Kind() != workers.KindIndexerText {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindIndexerText)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindIndexerText {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindIndexerText)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	if !descriptor.MayTouchFilesystem {
		t.Fatal("text indexer should declare filesystem access")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.indexer_text" {
		t.Fatalf("worker key = %q, want main.indexer_text", instances[0].WorkerKey)
	}
	if instances[0].WorkerKind != workers.KindIndexerText {
		t.Fatalf("worker kind = %q, want %q", instances[0].WorkerKind, workers.KindIndexerText)
	}
}

func TestIndexerTextRuntimeValidateConfig(t *testing.T) {
	runtime := NewIndexerTextRuntime(search.Service{})
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestParseIndexerTextConfigDefaultsAndBounds(t *testing.T) {
	config, err := parseIndexerTextConfig(json.RawMessage(`{"batch_size":500,"retry":{"max_attempts":0}}`))
	if err != nil {
		t.Fatalf("parseIndexerTextConfig returned error: %v", err)
	}
	if config.BatchSize != 200 {
		t.Fatalf("batch size = %d, want clamp to 200", config.BatchSize)
	}
	if config.MaxObjectsPerRun != 50 {
		t.Fatalf("max objects = %d, want default 50", config.MaxObjectsPerRun)
	}
	if config.Retry.MaxAttempts != 3 {
		t.Fatalf("retry attempts = %d, want default 3", config.Retry.MaxAttempts)
	}
	if config.LeaseDurationSeconds != 120 {
		t.Fatalf("lease seconds = %d, want 120", config.LeaseDurationSeconds)
	}
}
