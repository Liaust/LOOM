package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/policy"
	"loom.local/loom/internal/workers"
)

func TestPolicyExpiryRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewPolicyExpiryRuntime(policy.Service{})
	if runtime.Kind() != workers.KindPolicyExpiry {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindPolicyExpiry)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindPolicyExpiry {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindPolicyExpiry)
	}
	if descriptor.RuntimeOwner != workers.RuntimeOwnerLoomd {
		t.Fatalf("runtime owner = %q, want %q", descriptor.RuntimeOwner, workers.RuntimeOwnerLoomd)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.policy_expiry" {
		t.Fatalf("worker key = %q, want main.policy_expiry", instances[0].WorkerKey)
	}
	if instances[0].WorkerKind != workers.KindPolicyExpiry {
		t.Fatalf("worker kind = %q, want %q", instances[0].WorkerKind, workers.KindPolicyExpiry)
	}
}

func TestPolicyExpiryRuntimeValidateConfig(t *testing.T) {
	runtime := NewPolicyExpiryRuntime(policy.Service{})
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestPolicyExpiryCounters(t *testing.T) {
	counters := policyExpiryCounters(policy.ExpirationResult{
		ApprovalsExpired: 2,
		GrantsExpired:    3,
	})
	if counters["approvals_expired"] != 2 {
		t.Fatalf("approvals_expired = %d, want 2", counters["approvals_expired"])
	}
	if counters["grants_expired"] != 3 {
		t.Fatalf("grants_expired = %d, want 3", counters["grants_expired"])
	}
	if counters["total_expired"] != 5 {
		t.Fatalf("total_expired = %d, want 5", counters["total_expired"])
	}
}

func TestPolicyExpirySummaryIsObject(t *testing.T) {
	summary, err := policyExpirySummary(policy.ExpirationResult{ApprovalsExpired: 1})
	if err != nil {
		t.Fatalf("policyExpirySummary returned error: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(summary, &value); err != nil {
		t.Fatalf("summary is invalid JSON: %v", err)
	}
	if value["status"] != "ok" {
		t.Fatalf("status = %v, want ok", value["status"])
	}
}
