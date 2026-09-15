package runtimes

import (
	"testing"

	"loom.local/loom/internal/workers"
)

func TestDirectEventIngestRuntimeDescriptor(t *testing.T) {
	runtime := NewDirectEventIngestRuntime(nilAutomationService())
	if runtime.Kind() != workers.KindDirectEventIngest {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindDirectEventIngest)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindDirectEventIngest {
		t.Fatalf("descriptor kind = %q", descriptor.WorkerKind)
	}
	if descriptor.MayCallCapabilities {
		t.Fatal("direct_event_ingest should not call capabilities")
	}
	if descriptor.MayCreateJobs {
		t.Fatal("direct_event_ingest should not create jobs")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("instances len = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.direct_event_ingest" {
		t.Fatalf("worker key = %q", instances[0].WorkerKey)
	}
}

func TestParseDirectEventIngestConfig(t *testing.T) {
	config, err := parseDirectEventIngestConfig(nil)
	if err != nil {
		t.Fatalf("parseDirectEventIngestConfig returned error: %v", err)
	}
	if config.BatchSize != 25 {
		t.Fatalf("batch = %d, want 25", config.BatchSize)
	}
	if _, err := parseDirectEventIngestConfig([]byte(`{"batch_size":201}`)); err == nil {
		t.Fatal("parseDirectEventIngestConfig accepted oversized batch")
	}
}
