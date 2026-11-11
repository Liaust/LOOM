package workers

import (
	"encoding/json"
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

func TestSupervisorSchedulerTickKeyUsesExactDurableDeadline(t *testing.T) {
	due := time.Unix(120, 123456789).UTC()
	worker := WorkerInstance{WorkerInstanceID: "scheduler", WorkerKind: KindAutomationScheduler, NextRunAfter: &due}
	policy := TickPolicy{Mode: TickModeInterval, IntervalSeconds: 60}
	first := supervisorTickIdempotencyKey(worker, policy, due)
	if replay := supervisorTickIdempotencyKey(worker, policy, due.Add(time.Hour)); replay != first {
		t.Fatalf("same scheduler deadline changed key: %q != %q", first, replay)
	}
	for _, delay := range []time.Duration{time.Nanosecond, 2 * time.Second, 20 * time.Second} {
		next := due.Add(delay)
		worker.NextRunAfter = &next
		if key := supervisorTickIdempotencyKey(worker, policy, next); key == first {
			t.Fatalf("distinct deadline within one interval bucket reused %q", first)
		}
	}
}

func TestSupervisorSchedulerWakeDeadline(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	idle := now.Add(time.Minute)
	base := WorkerInstance{
		WorkerKind: KindAutomationScheduler, Locality: LocalityMainOwned,
		LifecycleStatus: LifecycleActive, Enabled: true,
		TickPolicyJSON: json.RawMessage(`{"mode":"interval","interval_seconds":60}`),
		ConfigJSON:     json.RawMessage(`{}`), NextRunAfter: &idle,
	}
	for _, tc := range []struct {
		name   string
		change func(*WorkerInstance)
		wait   time.Duration
		skip   bool
	}{
		{"discover earlier schedule", func(*WorkerInstance) {}, 0, false},
		{"failure delay", func(w *WorkerInstance) { w.ConsecutiveFails = 1 }, 0, true},
		{"explicit backoff", func(w *WorkerInstance) { at := now.Add(10 * time.Second); w.BackoffUntil = &at }, 10 * time.Second, false},
		{"already earlier deadline", func(w *WorkerInstance) { w.NextRunAfter = &now }, 0, true},
		{"no initial deadline", func(w *WorkerInstance) { w.NextRunAfter = nil }, 0, false},
		{"manual", func(w *WorkerInstance) { w.TickPolicyJSON = json.RawMessage(`{"mode":"manual"}`) }, 0, true},
		{"daily", func(w *WorkerInstance) {
			w.TickPolicyJSON = json.RawMessage(`{"mode":"daily_local","local_time":"03:15","timezone":"UTC"}`)
		}, 0, true},
		{"disabled", func(w *WorkerInstance) { w.Enabled = false }, 0, true},
		{"paused", func(w *WorkerInstance) { w.Paused = true }, 0, true},
		{"retired", func(w *WorkerInstance) { w.LifecycleStatus = LifecycleRetired }, 0, true},
		{"active run no overlap", func(w *WorkerInstance) { id := "running"; w.CurrentRunID = &id }, 0, true},
		{"other kind", func(w *WorkerInstance) { w.WorkerKind = KindAutomationDispatcher }, 0, true},
		{"other locality", func(w *WorkerInstance) { w.Locality = "node_local" }, 0, true},
		{"invalid policy", func(w *WorkerInstance) { w.TickPolicyJSON = json.RawMessage(`{`) }, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := base
			tc.change(&worker)
			next := schedulerWakeDeadline(worker, now, &now)
			if tc.skip {
				if next != nil {
					t.Fatalf("wake changed a protected deadline: %v", next)
				}
				return
			}
			if next == nil || !next.Equal(now.Add(tc.wait)) {
				t.Fatalf("wake deadline = %v, want %v", next, now.Add(tc.wait))
			}
		})
	}
	worker := base
	worker.LastSuccessAt = &now
	future := now.Add(time.Nanosecond)
	if next := schedulerWakeDeadline(worker, now, &future); next == nil || !next.Equal(future) {
		t.Fatalf("future actual deadline was delayed: %v", next)
	}
	if next := schedulerWakeDeadline(worker, now, nil); next != nil {
		t.Fatalf("missing proposal changed deadline: %v", next)
	}
}

func TestSupervisorSchedulerWakeHintAndReconnect(t *testing.T) {
	wakes := NewQueueWakes()
	wakes.SignalLocal(KindAutomationScheduler)
	select {
	case <-wakes.channel("control"):
	default:
		t.Fatal("scheduler hint did not wake background lane")
	}
	if len(wakes.jobs) != 0 || len(wakes.notes) != 0 {
		t.Fatal("scheduler hint woke unrelated lanes")
	}
	if !wakes.take("control")[KindAutomationScheduler] {
		t.Fatal("scheduler kind was lost")
	}
	wakes.catchUp()
	if !wakes.take("control")["*"] {
		t.Fatal("reconnect did not request scheduler rediscovery")
	}
}

func TestQueueWakeEligibilityProtectsPoliciesAndFailures(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Minute)
	base := WorkerInstance{WorkerKind: KindJobRunner, Locality: LocalityMainOwned,
		LifecycleStatus: LifecycleActive, Enabled: true, NextRunAfter: &future,
		TickPolicyJSON: json.RawMessage(`{"mode":"interval","interval_seconds":60}`)}
	for _, kind := range []string{KindJobRunner, KindKnowledgeIndexer, KindKnowledgeHeavy, KindAutomationDispatcher} {
		instance := base
		instance.WorkerKind = kind
		if !queueWakeEligible(instance, now) {
			t.Fatalf("eligible %s retained idle deadline", kind)
		}
		first := supervisorTickIdempotencyKey(instance, TickPolicy{Mode: TickModeInterval}, now)
		next := future.Add(time.Microsecond)
		instance.NextRunAfter = &next
		if first == supervisorTickIdempotencyKey(instance, TickPolicy{Mode: TickModeInterval}, now) {
			t.Fatalf("%s reused a previous queue occurrence", kind)
		}
	}
	for _, change := range []func(*WorkerInstance){
		func(w *WorkerInstance) { w.Paused = true },
		func(w *WorkerInstance) { w.Enabled = false },
		func(w *WorkerInstance) { w.CurrentRunID = new(string) },
		func(w *WorkerInstance) { w.ConsecutiveFails = 1 },
		func(w *WorkerInstance) { w.LifecycleStatus = LifecycleRetired },
		func(w *WorkerInstance) { w.WorkerKind = KindMainBackup },
		func(w *WorkerInstance) { w.TickPolicyJSON = json.RawMessage(`{"mode":"manual"}`) },
		func(w *WorkerInstance) {
			w.TickPolicyJSON = json.RawMessage(`{"mode":"daily_local","local_time":"03:00","timezone":"UTC"}`)
		},
	} {
		instance := base
		change(&instance)
		if queueWakeEligible(instance, now) {
			t.Fatalf("wake bypassed protection: %+v", instance)
		}
	}
}
