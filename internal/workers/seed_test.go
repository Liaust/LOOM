package workers

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"loom.local/loom/internal/requestctx"
)

func TestSeedBuiltinsRequiresDatabase(t *testing.T) {
	service := NewService(nil, NewRegistry(), nil)
	_, err := service.SeedBuiltins(context.Background(), testRequestContext(), "main")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("SeedBuiltins error = %v, want ErrInvalid", err)
	}
}

func TestSeedBuiltinsRequiresRegistry(t *testing.T) {
	service := NewService(new(sql.DB), nil, nil)
	_, err := service.SeedBuiltins(context.Background(), testRequestContext(), "main")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("SeedBuiltins error = %v, want ErrInvalid", err)
	}
}

func TestRegistryCollectsDefaultInstances(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(testRuntimeWithInstances{testRuntime: newTestRuntime("other_worker")}); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	instances := registry.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.other_worker" {
		t.Fatalf("worker key = %q, want main.other_worker", instances[0].WorkerKey)
	}
}

func TestNormalizeInstanceDescriptorDefaultsFromKind(t *testing.T) {
	kind := newTestRuntime("other_worker").Describe()
	instance, err := normalizeInstanceDescriptor(InstanceDescriptor{
		WorkerKey:  "main.other_worker",
		WorkerKind: "other_worker",
		ConfigJSON: []byte(`{"schema_version":"test.config"}`),
	}, kind)
	if err != nil {
		t.Fatalf("normalizeInstanceDescriptor returned error: %v", err)
	}
	if instance.DisplayName != kind.DisplayName {
		t.Fatalf("display name = %q, want %q", instance.DisplayName, kind.DisplayName)
	}
	if instance.Locality != LocalityMainOwned {
		t.Fatalf("locality = %q, want %q", instance.Locality, LocalityMainOwned)
	}
	if string(instance.TickPolicyJSON) != string(kind.DefaultTickPolicyJSON) {
		t.Fatalf("tick policy = %s, want %s", instance.TickPolicyJSON, kind.DefaultTickPolicyJSON)
	}
}

func TestTextArrayLiteralQuotesValues(t *testing.T) {
	got := textArrayLiteral([]string{"main_owned", `node"agent`, `with\slash`})
	want := `{"main_owned","node\"agent","with\\slash"}`
	if got != want {
		t.Fatalf("textArrayLiteral = %q, want %q", got, want)
	}
}

func testRequestContext() requestctx.Context {
	return requestctx.Context{
		ActorID:       "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ActorKey:      "owner",
		OriginNodeID:  "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeKey: "main",
		ScopeID:       "scope_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		ScopeKey:      "system",
		CorrelationID: "corr_test_workers_seed",
	}
}

type testRuntimeWithInstances struct {
	testRuntime
}

func (r testRuntimeWithInstances) DefaultInstances() []InstanceDescriptor {
	return []InstanceDescriptor{
		{
			WorkerKey:  "main." + r.kind,
			WorkerKind: r.kind,
			ConfigJSON: r.DefaultConfig(),
		},
	}
}
