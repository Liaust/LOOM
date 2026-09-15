package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"loom.local/loom/internal/realtime"
	"loom.local/loom/internal/workers"
)

func TestRealtimeExpiryRuntimeDescriptorAndInstance(t *testing.T) {
	runtime := NewRealtimeExpiryRuntime(realtime.Service{})
	if runtime.Kind() != workers.KindRealtimeExpiry {
		t.Fatalf("kind = %q, want %q", runtime.Kind(), workers.KindRealtimeExpiry)
	}
	descriptor := runtime.Describe()
	if descriptor.WorkerKind != workers.KindRealtimeExpiry {
		t.Fatalf("descriptor kind = %q, want %q", descriptor.WorkerKind, workers.KindRealtimeExpiry)
	}
	instances := runtime.DefaultInstances()
	if len(instances) != 1 {
		t.Fatalf("default instance count = %d, want 1", len(instances))
	}
	if instances[0].WorkerKey != "main.realtime_expiry" {
		t.Fatalf("worker key = %q, want main.realtime_expiry", instances[0].WorkerKey)
	}
}

func TestRealtimeExpiryRuntimeValidateConfig(t *testing.T) {
	runtime := NewRealtimeExpiryRuntime(realtime.Service{})
	if err := runtime.ValidateConfig(context.Background(), runtime.DefaultConfig()); err != nil {
		t.Fatalf("ValidateConfig returned error: %v", err)
	}
	err := runtime.ValidateConfig(context.Background(), json.RawMessage(`[]`))
	if !errors.Is(err, workers.ErrInvalid) {
		t.Fatalf("ValidateConfig error = %v, want workers.ErrInvalid", err)
	}
}

func TestRealtimeExpiryCounters(t *testing.T) {
	counters := realtimeExpiryCounters(realtime.ExpiryResult{
		NotificationsExpired: 2,
		LeasesExpired:        3,
		SubscriptionsExpired: 5,
		PresenceMarkedStale:  7,
		ProgressFeedsClosed:  11,
	})
	if counters["total_transitions"] != 28 {
		t.Fatalf("total_transitions = %d, want 28", counters["total_transitions"])
	}
}

func TestRealtimeExpirySummaryIsObject(t *testing.T) {
	summary, err := realtimeExpirySummary(realtime.ExpiryResult{NotificationsExpired: 1})
	if err != nil {
		t.Fatalf("realtimeExpirySummary returned error: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(summary, &value); err != nil {
		t.Fatalf("summary is invalid JSON: %v", err)
	}
	if value["status"] != "ok" {
		t.Fatalf("status = %v, want ok", value["status"])
	}
}
