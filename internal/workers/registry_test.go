package workers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type testRuntime struct {
	kind       string
	descriptor KindDescriptor
	config     json.RawMessage
}

func (r testRuntime) Kind() string {
	return r.kind
}

func (r testRuntime) Describe() KindDescriptor {
	return r.descriptor
}

func (r testRuntime) DefaultConfig() json.RawMessage {
	if len(r.config) == 0 {
		return json.RawMessage(`{}`)
	}
	return r.config
}

func (r testRuntime) ValidateConfig(context.Context, json.RawMessage) error {
	return nil
}

func (r testRuntime) RunOnce(context.Context, RunContext) (RunResult, error) {
	return RunResult{}, nil
}

func TestRegistryAcceptsSelfcheckRuntime(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime(KindSelfcheck)

	if err := registry.Register(runtime); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	if _, ok := registry.Get(KindSelfcheck); !ok {
		t.Fatal("registered runtime was not found")
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 1 {
		t.Fatalf("descriptor count = %d, want 1", len(descriptors))
	}
	if descriptors[0].WorkerKind != KindSelfcheck {
		t.Fatalf("descriptor kind = %q, want %q", descriptors[0].WorkerKind, KindSelfcheck)
	}
}

func TestRegistryRejectsInvalidKind(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime("Worker Selfcheck")
	err := registry.Register(runtime)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Register error = %v, want ErrInvalid", err)
	}
}

func TestRegistryRejectsDuplicateRuntime(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime(KindSelfcheck)
	if err := registry.Register(runtime); err != nil {
		t.Fatalf("first Register returned error: %v", err)
	}
	err := registry.Register(runtime)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("second Register error = %v, want ErrConflict", err)
	}
}

func TestRegistryRejectsDescriptorKindMismatch(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime(KindSelfcheck)
	runtime.descriptor.WorkerKind = "other_worker"
	err := registry.Register(runtime)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Register error = %v, want ErrInvalid", err)
	}
}

func TestRegistryRejectsNonObjectJSONDefaults(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime(KindSelfcheck)
	runtime.descriptor.DefaultTickPolicyJSON = json.RawMessage(`[]`)
	err := registry.Register(runtime)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Register error = %v, want ErrInvalid", err)
	}
}

func TestRegistryRejectsNonObjectDefaultConfig(t *testing.T) {
	registry := NewRegistry()
	runtime := newTestRuntime(KindSelfcheck)
	runtime.config = json.RawMessage(`[]`)
	err := registry.Register(runtime)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Register error = %v, want ErrInvalid", err)
	}
}

func newTestRuntime(kind string) testRuntime {
	return testRuntime{
		kind: kind,
		descriptor: KindDescriptor{
			WorkerKind:                   kind,
			DisplayName:                  "Worker Selfcheck",
			Description:                  "Test selfcheck runtime.",
			RuntimeOwner:                 RuntimeOwnerLoomd,
			Status:                       KindStatusActive,
			SupportedLocalities:          []string{LocalityMainOwned},
			DefaultTickPolicyJSON:        json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
			DefaultConcurrencyPolicyJSON: json.RawMessage(`{"schema_version":"worker_concurrency_policy.v0.2","mode":"single"}`),
			DefaultRetryPolicyJSON:       json.RawMessage(`{"schema_version":"worker_retry_policy.v0.2","mode":"none"}`),
			DefaultTimeoutPolicyJSON:     json.RawMessage(`{"schema_version":"worker_timeout_policy.v0.2","run_timeout_seconds":30}`),
			DefaultResourceLimitsJSON:    json.RawMessage(`{"schema_version":"worker_resource_limits.v0.2"}`),
			ConfigSchemaJSON:             json.RawMessage(`{"schema_version":"worker_selfcheck.config_schema.v0.2"}`),
			CheckpointSchemaJSON:         json.RawMessage(`{"schema_version":"worker_selfcheck.checkpoint_schema.v0.2"}`),
			ResultSchemaJSON:             json.RawMessage(`{"schema_version":"worker_selfcheck.result_schema.v0.2"}`),
			Metadata:                     json.RawMessage(`{"test":true}`),
		},
		config: json.RawMessage(`{"schema_version":"worker_selfcheck.config.v0.2"}`),
	}
}
