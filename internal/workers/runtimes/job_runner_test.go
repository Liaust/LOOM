package runtimes

import (
	"context"
	"encoding/json"
	"testing"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/workers"
)

func TestJobRunnerRuntimeDescriptor(t *testing.T) {
	runtime := NewJobRunnerRuntime(jobs.Service{}, "test")
	if runtime.Kind() != workers.KindJobRunner {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindJobRunner)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindJobRunner {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindJobRunner)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	if !descriptor.MayTouchFilesystem {
		t.Fatal("job runner should declare filesystem access")
	}
}

func TestJobRunnerRuntimeDefaultInstance(t *testing.T) {
	runtime := NewJobRunnerRuntime(jobs.Service{}, "test")
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instances = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.job_runner" {
		t.Fatalf("worker key = %q, want main.job_runner", instances[0].WorkerKey)
	}
	if instances[0].WorkerKind != workers.KindJobRunner {
		t.Fatalf("worker kind = %q, want %q", instances[0].WorkerKind, workers.KindJobRunner)
	}
}

func TestJobRunnerRuntimeValidatesDefaultConfig(t *testing.T) {
	runtime := NewJobRunnerRuntime(jobs.Service{}, "test")
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig(default) returned error: %v", err)
	}
	config, err := parseJobRunnerConfig(runtime.DefaultConfig())
	if err != nil {
		t.Fatalf("parse default config: %v", err)
	}
	if !containsJobType(config.SupportedJobTypes, jobs.TypeWorkflowRun) {
		t.Fatalf("default config should support workflow jobs: %#v", config.SupportedJobTypes)
	}
}

func TestJobRunnerRuntimeUpgradesLegacyScriptOnlyConfig(t *testing.T) {
	config, err := parseJobRunnerConfig(json.RawMessage(`{"supported_job_types":["script_run"]}`))
	if err != nil {
		t.Fatalf("legacy script-only config should remain valid: %v", err)
	}
	if !containsJobType(config.SupportedJobTypes, jobs.TypeWorkflowRun) {
		t.Fatalf("legacy config should be augmented with workflow jobs: %#v", config.SupportedJobTypes)
	}
}

func TestJobRunnerRuntimeRejectsUnsupportedJobTypes(t *testing.T) {
	runtime := NewJobRunnerRuntime(jobs.Service{}, "test")
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"supported_job_types":["other"]}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}
