package automation

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNormalizeTargetProfileRequiresCapability(t *testing.T) {
	if _, err := NormalizeTargetProfile(json.RawMessage(`{"capability_ref":"main@system.status.read"}`)); err != nil {
		t.Fatalf("NormalizeTargetProfile returned error: %v", err)
	}
	if _, err := NormalizeTargetProfile(json.RawMessage(`{}`)); err == nil {
		t.Fatal("NormalizeTargetProfile accepted empty capability_ref")
	}
}

func TestNormalizeProfilesApplyDefaults(t *testing.T) {
	misfire, err := NormalizeMisfireProfile(nil)
	if err != nil {
		t.Fatalf("NormalizeMisfireProfile returned error: %v", err)
	}
	if misfire.Policy != MisfireMarkMissed {
		t.Fatalf("misfire policy = %q, want %q", misfire.Policy, MisfireMarkMissed)
	}

	concurrency, err := NormalizeConcurrencyProfile(nil)
	if err != nil {
		t.Fatalf("NormalizeConcurrencyProfile returned error: %v", err)
	}
	if concurrency.Policy != ConcurrencyAllowParallel {
		t.Fatalf("concurrency policy = %q, want %q", concurrency.Policy, ConcurrencyAllowParallel)
	}

	timeout, err := NormalizeTimeoutProfile(nil)
	if err != nil {
		t.Fatalf("NormalizeTimeoutProfile returned error: %v", err)
	}
	if timeout.TimeoutSeconds != defaultTimeoutSeconds {
		t.Fatalf("timeout = %d, want %d", timeout.TimeoutSeconds, defaultTimeoutSeconds)
	}

	retry, err := NormalizeRetryProfile(nil)
	if err != nil {
		t.Fatalf("NormalizeRetryProfile returned error: %v", err)
	}
	if retry.MaxAttempts != defaultMaxAttempts {
		t.Fatalf("max attempts = %d, want %d", retry.MaxAttempts, defaultMaxAttempts)
	}
}

func TestParseScheduleExpressionOneShot(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	plan, err := ParseScheduleExpression(ScheduleKindOneShot, "now", now)
	if err != nil {
		t.Fatalf("ParseScheduleExpression returned error: %v", err)
	}
	if plan.NextFireAt == nil || !plan.NextFireAt.Equal(now) {
		t.Fatalf("next fire = %v, want %v", plan.NextFireAt, now)
	}
}

func TestParseScheduleExpressionInterval(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	plan, err := ParseScheduleExpression(ScheduleKindInterval, "15m", now)
	if err != nil {
		t.Fatalf("ParseScheduleExpression returned error: %v", err)
	}
	want := now.Add(15 * time.Minute)
	if plan.NextFireAt == nil || !plan.NextFireAt.Equal(want) {
		t.Fatalf("next fire = %v, want %v", plan.NextFireAt, want)
	}
}

func TestObjectHashIsStable(t *testing.T) {
	a, err := objectHash(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatalf("objectHash returned error: %v", err)
	}
	b, err := objectHash(json.RawMessage(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("objectHash returned error: %v", err)
	}
	if a != b {
		t.Fatalf("hashes should match for equivalent JSON objects: %s != %s", a, b)
	}
}
