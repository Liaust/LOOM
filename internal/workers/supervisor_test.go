package workers

import (
	"testing"
	"time"
)

func TestSupervisorTickIdempotencyKeyUsesIntervalBucket(t *testing.T) {
	worker := WorkerInstance{WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	policy := TickPolicy{Mode: TickModeInterval, IntervalSeconds: 60}
	first := supervisorTickIdempotencyKey(worker, policy, time.Unix(120, 0).UTC())
	second := supervisorTickIdempotencyKey(worker, policy, time.Unix(179, 0).UTC())
	third := supervisorTickIdempotencyKey(worker, policy, time.Unix(180, 0).UTC())

	if first != second {
		t.Fatalf("same bucket keys differ: %q != %q", first, second)
	}
	if first == third {
		t.Fatalf("next bucket key should differ from %q", first)
	}
}

func TestSupervisorTickIdempotencyKeyUsesDurableDailyOccurrence(t *testing.T) {
	dueAt := time.Date(2026, 10, 25, 2, 15, 0, 0, time.UTC)
	worker := WorkerInstance{WorkerInstanceID: "worker_instance_01ARZ3NDEKTSV4RRFFQ69G5FAV", NextRunAfter: &dueAt}
	policy := TickPolicy{Mode: TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"}
	first := supervisorTickIdempotencyKey(worker, policy, dueAt)
	late := supervisorTickIdempotencyKey(worker, policy, dueAt.Add(6*time.Hour))
	if first != late {
		t.Fatalf("same durable occurrence keys differ: %q != %q", first, late)
	}
	nextDue := time.Date(2026, 10, 26, 2, 15, 0, 0, time.UTC)
	worker.NextRunAfter = &nextDue
	if next := supervisorTickIdempotencyKey(worker, policy, nextDue); next == first {
		t.Fatalf("next daily occurrence reused key %q", first)
	}
}

func TestNewSupervisorDefaultsPollInterval(t *testing.T) {
	supervisor := NewSupervisor(Service{}, nil)
	if supervisor.PollInterval != defaultSupervisorPollInterval {
		t.Fatalf("poll interval = %s, want %s", supervisor.PollInterval, defaultSupervisorPollInterval)
	}
}
