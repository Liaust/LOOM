package runtimes

import (
	"encoding/json"
	"testing"
	"time"

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

func TestAutomationSchedulerDeadline(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 123456789, time.UTC)
	config, err := parseAutomationSchedulerConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		due  *time.Time
		wait time.Duration
	}{
		{"no schedules fallback", nil, time.Minute},
		{"far deadline fallback", automationDeadlineTime(now.Add(time.Hour)), time.Minute},
		{"earlier actual deadline", automationDeadlineTime(now.Add(20 * time.Second)), 20 * time.Second},
		{"subsecond deadline", automationDeadlineTime(now.Add(100 * time.Millisecond)), 100 * time.Millisecond},
		{"nanosecond deadline", automationDeadlineTime(now.Add(time.Nanosecond)), time.Nanosecond},
		{"due now floor", &now, 2 * time.Second},
		{"overdue batch backoff", automationDeadlineTime(now.Add(-time.Hour)), 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := nextAutomationSchedulerRunAfter(now, config, tc.due)
			if next == nil || !next.Equal(now.Add(tc.wait)) {
				t.Fatalf("next = %v, want %v", next, now.Add(tc.wait))
			}
			if tc.due != nil && tc.due.After(now) && tc.due.Before(now.Add(time.Minute)) && next.Before(*tc.due) {
				t.Fatal("future deadline scheduled early")
			}
		})
	}
}

func TestAutomationSchedulerDeadlineUsesConfiguredBackoff(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	config, err := parseAutomationSchedulerConfig(json.RawMessage(`{"idle_tick_interval_seconds":30,"active_tick_interval_seconds":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if next := nextAutomationSchedulerRunAfter(now, config, &now); !next.Equal(now.Add(5 * time.Second)) {
		t.Fatalf("overdue backoff = %v", next)
	}
	if next := nextAutomationSchedulerRunAfter(now, config, nil); !next.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("idle fallback = %v", next)
	}
}

func automationDeadlineTime(at time.Time) *time.Time { return &at }

func TestAutomationSchedulerWakeDeadline(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	config, err := parseAutomationSchedulerConfig(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		due  *time.Time
		last *time.Time
		wait time.Duration
	}{
		{"future exact deadline after recent run", automationDeadlineTime(now.Add(time.Nanosecond)), &now, time.Nanosecond},
		{"overdue recent run floor", &now, &now, 2 * time.Second},
		{"overdue remaining floor", &now, automationDeadlineTime(now.Add(-time.Second)), time.Second},
		{"overdue old run", &now, automationDeadlineTime(now.Add(-time.Hour)), 0},
		{"overdue no prior run", &now, nil, 0},
		{"no schedules bounded fallback", nil, &now, time.Minute},
		{"far schedule bounded fallback", automationDeadlineTime(now.Add(time.Hour)), &now, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := nextAutomationSchedulerWakeAfter(now, config, tc.due, tc.last)
			if next == nil || !next.Equal(now.Add(tc.wait)) {
				t.Fatalf("wake deadline = %v, want %v", next, now.Add(tc.wait))
			}
		})
	}
}
