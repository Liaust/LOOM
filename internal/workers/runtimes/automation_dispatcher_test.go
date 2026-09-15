package runtimes

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/workers"
)

func TestAutomationDispatcherRuntimeDescriptor(t *testing.T) {
	runtime := NewAutomationDispatcherRuntime(nilAutomationService())
	if runtime.Kind() != workers.KindAutomationDispatcher {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindAutomationDispatcher)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindAutomationDispatcher {
		t.Fatalf("descriptor kind = %q", descriptor.WorkerKind)
	}
	if !descriptor.MayCreateJobs || !descriptor.MayCallCapabilities {
		t.Fatal("automation dispatcher should declare job creation and capability calls")
	}
	if descriptor.MayTouchFilesystem {
		t.Fatal("automation dispatcher should not declare filesystem access")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.automation_dispatcher" {
		t.Fatalf("worker key = %q", instances[0].WorkerKey)
	}
}

func TestParseAutomationDispatcherConfig(t *testing.T) {
	config, err := parseAutomationDispatcherConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("parseAutomationDispatcherConfig returned error: %v", err)
	}
	if config.BatchSize != 10 || config.LeaseDurationSeconds != 120 || config.IdleTickIntervalSeconds != 60 {
		t.Fatalf("defaults not applied: %+v", config)
	}
	if _, err := parseAutomationDispatcherConfig(json.RawMessage(`{"batch_size":101}`)); err == nil {
		t.Fatal("parseAutomationDispatcherConfig accepted too-large batch_size")
	}
}
