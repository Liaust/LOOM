package workers

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDailyWorkerPolicyApplicationDueSelectionAndRacePostgres(t *testing.T) {
	db, req, instance, registry := workerPolicyPostgresFixture(t)
	service := NewService(db, registry, nil)
	ctx := context.Background()

	initial, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	daily := TickPolicy{Mode: TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"}
	dryRun, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy: daily, ExpectedPolicyFingerprint: initial.PolicyFingerprint, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.Applied || !dryRun.Changed || dryRun.New.NextRunAfter == nil {
		t.Fatalf("daily dry-run = %#v", dryRun)
	}
	unchanged, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || unchanged.PolicyFingerprint != initial.PolicyFingerprint || unchanged.NextRunAfter != nil {
		t.Fatalf("daily dry-run mutated policy: %#v err=%v", unchanged, err)
	}

	applyInput := SetWorkerPolicyInput{
		Policy: daily, ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason: "schedule daily cloud acceptance fixture", Confirm: true,
		IdempotencyKey: "daily-policy-apply-" + instance.WorkerInstanceID,
	}
	applied, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.Changed || applied.New.NextRunAfter == nil || applied.New.Policy.Mode != TickModeDailyLocal {
		t.Fatalf("daily apply = %#v", applied)
	}
	location, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	localNext := applied.New.NextRunAfter.In(location)
	if localNext.Hour() != 3 || localNext.Minute() != 15 || localNext.Second() != 0 || !applied.New.NextRunAfter.After(applied.NextRunEvidence.DerivedAt) || applied.New.NextRunAfter.Sub(applied.NextRunEvidence.DerivedAt) > 26*time.Hour {
		t.Fatalf("daily next occurrence = %v local=%v evidence=%#v", applied.New.NextRunAfter, localNext, applied.NextRunEvidence)
	}
	if applied.NextRunEvidence.DerivationBasis != "daily_local policy schedules the next 03:15 occurrence in Europe/Amsterdam" {
		t.Fatalf("daily derivation basis = %q", applied.NextRunEvidence.DerivationBasis)
	}

	// A daemon restart reads the already-derived UTC instant instead of deriving
	// a new 24-hour interval from reboot time.
	afterRestart, err := NewService(db, registry, nil).InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || afterRestart.NextRunAfter == nil || !afterRestart.NextRunAfter.Equal(*applied.New.NextRunAfter) {
		t.Fatalf("durable daily next_run_after = %#v err=%v", afterRestart, err)
	}

	identical, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy: daily, ExpectedPolicyFingerprint: applied.New.PolicyFingerprint,
		Reason: "confirm unchanged daily policy", Confirm: true,
		IdempotencyKey: "daily-policy-identical-" + instance.WorkerInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if identical.Changed || identical.New.NextRunAfter == nil || !identical.New.NextRunAfter.Equal(*applied.New.NextRunAfter) {
		t.Fatalf("identical daily policy reset durable schedule: %#v", identical)
	}

	// daily_local never adopts interval run-on-startup semantics when durable
	// scheduling state is absent.
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=NULL WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	withoutDurableSchedule, err := service.ListDueWorkers(ctx, time.Now().UTC().Add(48*time.Hour), 200)
	if err != nil {
		t.Fatal(err)
	}
	if workerInstanceInSet(withoutDurableSchedule, instance.WorkerInstanceID) {
		t.Fatalf("daily worker without durable next_run_after appeared due: %#v", withoutDurableSchedule)
	}

	past := time.Now().UTC().Add(-time.Second)
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, past); err != nil {
		t.Fatal(err)
	}
	dueWorkers, err := service.ListDueWorkers(ctx, time.Now().UTC(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if !workerInstanceInSet(dueWorkers, instance.WorkerInstanceID) {
		t.Fatalf("daily worker missing from due set: %#v", dueWorkers)
	}

	var selected WorkerInstance
	for _, candidate := range dueWorkers {
		if candidate.WorkerInstanceID == instance.WorkerInstanceID {
			selected = candidate
			break
		}
	}
	advanced, err := service.advanceDailyScheduleAfterFailedAttempt(ctx, selected, daily, time.Now().UTC())
	if err != nil || !advanced {
		t.Fatalf("advance failed daily occurrence = %t err=%v", advanced, err)
	}
	afterFailure, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || afterFailure.NextRunAfter == nil || !afterFailure.NextRunAfter.After(time.Now().UTC()) {
		t.Fatalf("failed daily occurrence next_run_after = %#v err=%v", afterFailure, err)
	}
	if local := afterFailure.NextRunAfter.In(location); local.Hour() != 3 || local.Minute() != 15 {
		t.Fatalf("failed daily occurrence advanced to %v", local)
	}
	advanced, err = service.advanceDailyScheduleAfterFailedAttempt(ctx, selected, daily, time.Now().UTC())
	if err != nil || advanced {
		t.Fatalf("stale daily occurrence advanced twice = %t err=%v", advanced, err)
	}

	// The selected row can race with an operator policy update, but RunOnce
	// re-locks the worker and rejects the stale fingerprint before runtime work.
	changed, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy:                    TickPolicy{Mode: TickModeDailyLocal, LocalTime: "03:16", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: identical.New.PolicyFingerprint,
		Reason:                    "advance daily policy after supervisor selection",
		Confirm:                   true,
		IdempotencyKey:            "daily-policy-race-" + instance.WorkerInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed.New.PolicyFingerprint == identical.New.PolicyFingerprint {
		t.Fatal("daily policy race fixture did not change fingerprint")
	}
	_, err = service.RunOnce(ctx, req, instance.WorkerKey, RunOnceInput{
		Reason:                    "supervisor tick",
		TriggerKind:               TriggerSupervisorTick,
		TriggerRef:                instance.WorkerKey,
		IdempotencyKey:            "daily-supervisor-stale-" + instance.WorkerInstanceID,
		ExpectedPolicyFingerprint: identical.New.PolicyFingerprint,
		ScheduleEvidenceAt:        time.Now().UTC(),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale daily supervisor selection error = %v, want ErrConflict", err)
	}
}

func workerInstanceInSet(instances []WorkerInstance, instanceID string) bool {
	for _, instance := range instances {
		if instance.WorkerInstanceID == instanceID {
			return true
		}
	}
	return false
}
