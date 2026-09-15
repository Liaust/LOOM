package runtimes

import (
	"context"
	"encoding/json"
	"testing"

	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/workers"
)

func TestJobSweeperRuntimeDescriptor(t *testing.T) {
	runtime := NewJobSweeperRuntime(jobs.Service{})
	if runtime.Kind() != workers.KindJobSweeper {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindJobSweeper)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindJobSweeper {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindJobSweeper)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	if descriptor.MayTouchFilesystem {
		t.Fatal("job sweeper should not declare filesystem access")
	}
}

func TestJobSweeperRuntimeDefaultInstance(t *testing.T) {
	runtime := NewJobSweeperRuntime(jobs.Service{})
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instances = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.job_sweeper" {
		t.Fatalf("worker key = %q, want main.job_sweeper", instances[0].WorkerKey)
	}
	if instances[0].WorkerKind != workers.KindJobSweeper {
		t.Fatalf("worker kind = %q, want %q", instances[0].WorkerKind, workers.KindJobSweeper)
	}
}

func TestJobSweeperRuntimeValidatesDefaultConfig(t *testing.T) {
	runtime := NewJobSweeperRuntime(jobs.Service{})
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig(default) returned error: %v", err)
	}
}

func TestJobSweeperRuntimeRejectsUnsupportedRetryExhaustedBehavior(t *testing.T) {
	runtime := NewJobSweeperRuntime(jobs.Service{})
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"retry_exhausted_behavior":"auto_retry"}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
}
