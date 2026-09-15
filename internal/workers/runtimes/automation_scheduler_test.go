package runtimes

import (
	"encoding/json"
	"testing"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/workers"
)

func TestAutomationSchedulerRuntimeDescriptor(t *testing.T) {
	runtime := NewAutomationSchedulerRuntime(nilAutomationService())
	if runtime.Kind() != workers.KindAutomationScheduler {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindAutomationScheduler)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindAutomationScheduler {
		t.Fatalf("descriptor kind = %q", descriptor.WorkerKind)
	}
	if descriptor.MayCreateJobs || descriptor.MayCallCapabilities || descriptor.MayTouchFilesystem {
		t.Fatal("automation scheduler should not declare jobs, capability calls, or filesystem access")
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("instances = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.automation_scheduler" {
		t.Fatalf("worker key = %q", instances[0].WorkerKey)
	}
}

func TestParseAutomationSchedulerConfig(t *testing.T) {
	config, err := parseAutomationSchedulerConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("parseAutomationSchedulerConfig returned error: %v", err)
	}
	if config.BatchSize != 25 || config.IdleTickIntervalSeconds != 60 || config.ActiveTickIntervalSeconds != 2 {
		t.Fatalf("defaults not applied: %+v", config)
	}
	if _, err := parseAutomationSchedulerConfig(json.RawMessage(`{"batch_size":201}`)); err == nil {
		t.Fatal("parseAutomationSchedulerConfig accepted too-large batch_size")
	}
	if _, err := parseAutomationSchedulerConfig(json.RawMessage(`{"lookahead_seconds":-1}`)); err == nil {
		t.Fatal("parseAutomationSchedulerConfig accepted negative lookahead")
	}
}

func nilAutomationService() automation.Service {
	return automation.Service{}
}
