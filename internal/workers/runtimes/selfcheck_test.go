package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/workers"
)

func TestSelfcheckDescriptorRegisters(t *testing.T) {
	registry := workers.NewRegistry()
	if err := registry.Register(NewSelfcheckRuntime(nil)); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if _, ok := registry.Get(workers.KindSelfcheck); !ok {
		t.Fatal("selfcheck runtime was not registered")
	}
}

func TestSelfcheckValidateConfig(t *testing.T) {
	runtime := NewSelfcheckRuntime(nil)
	if err := runtime.ValidateConfig(context.Background(), json.RawMessage(`{"schema_version":"worker_selfcheck.config.v0.2"}`)); err != nil {
		t.Fatalf("ValidateConfig returned error for object config: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want ErrInvalid", err)
	}
}

func TestSelfcheckRunOnceRequiresDatabase(t *testing.T) {
	runtime := NewSelfcheckRuntime(nil)
	_, err := runtime.RunOnce(context.Background(), workers.RunContext{
		Instance: workers.WorkerInstance{WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
		Run:      workers.WorkerRun{WorkerRunID: "worker_run_01ARZ3NDEKTSV4RRFFQ69G5FAV"},
	})
	if err == nil {
		t.Fatal("RunOnce returned nil error without database")
	}
}
