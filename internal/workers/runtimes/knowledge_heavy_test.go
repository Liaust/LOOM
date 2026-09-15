package runtimes

import (
	"context"
	"testing"

	"loom.local/loom/internal/workers"
)

func TestKnowledgeHeavyRuntimeContract(t *testing.T) {
	runtime := NewKnowledgeHeavyRuntime(nil)
	if runtime.Kind() != workers.KindKnowledgeHeavy {
		t.Fatalf("kind = %q", runtime.Kind())
	}
	descriptor := runtime.Describe()
	if descriptor.Status != workers.KindStatusActive {
		t.Fatalf("status = %q", descriptor.Status)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 || instances[0].WorkerKey != "main.knowledge_heavy" {
		t.Fatalf("instances = %#v", instances)
	}
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
}
