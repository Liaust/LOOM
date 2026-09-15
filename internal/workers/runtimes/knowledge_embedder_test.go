package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/workers"
)

func TestKnowledgeEmbedderRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewKnowledgeEmbedderRuntime(nil, nil)
	if runtime.Kind() != workers.KindKnowledgeEmbedder {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindKnowledgeEmbedder)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindKnowledgeEmbedder {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindKnowledgeEmbedder)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	if descriptor.Status != workers.KindStatusDeprecated {
		t.Fatalf("status = %q", descriptor.Status)
	}
	if descriptor.MayTouchFilesystem {
		t.Fatal("knowledge embedder should not declare direct filesystem access")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.knowledge_embedder" {
		t.Fatalf("worker key = %q, want main.knowledge_embedder", instances[0].WorkerKey)
	}
	if instances[0].WorkerKind != workers.KindKnowledgeEmbedder {
		t.Fatalf("worker kind = %q", instances[0].WorkerKind)
	}
	if string(instances[0].TickPolicyJSON) != `{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}` {
		t.Fatalf("tick policy = %s", instances[0].TickPolicyJSON)
	}
}

func TestKnowledgeEmbedderRuntimeValidateConfig(t *testing.T) {
	runtime := NewKnowledgeEmbedderRuntime(nil, nil)
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestParseKnowledgeEmbedderConfigForcesOneLane(t *testing.T) {
	config, err := parseKnowledgeEmbedderConfig(json.RawMessage(`{"batch_size":20,"max_objects_per_run":10,"retry":{"max_attempts":0}}`))
	if err != nil {
		t.Fatalf("parseKnowledgeEmbedderConfig returned error: %v", err)
	}
	if config.BatchSize != 1 {
		t.Fatalf("batch size = %d, want 1", config.BatchSize)
	}
	if config.MaxObjectsPerRun != 1 {
		t.Fatalf("max objects = %d, want 1", config.MaxObjectsPerRun)
	}
	if config.Retry.MaxAttempts != 3 {
		t.Fatalf("retry attempts = %d, want 3", config.Retry.MaxAttempts)
	}
	if config.LeaseDurationSeconds != 600 {
		t.Fatalf("lease seconds = %d, want 600", config.LeaseDurationSeconds)
	}
}

func TestKnowledgeEmbedderRunOnceRequiresService(t *testing.T) {
	runtime := NewKnowledgeEmbedderRuntime(nil, nil)
	_, err := runtime.RunOnce(context.Background(), workers.RunContext{})
	if err == nil {
		t.Fatal("RunOnce returned nil error without knowledge service")
	}
}
