package workers

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestSeedSchedulerInitialOccurrencePostgres(t *testing.T) {
	db, _, instance, _ := workerPolicyPostgresFixture(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Only the fixture row is changed, and every change is rolled back.
	if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances SET worker_key='main.automation_scheduler',enabled=true,paused=false,lifecycle_status='active',next_run_after=NULL,tick_policy_json='{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":60,"run_on_startup":false}' WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if err := initializeSchedulerOccurrence(ctx, tx, instance.WorkerInstanceID, now); err != nil {
		t.Fatal(err)
	}
	read := func() sql.NullTime {
		t.Helper()
		var at sql.NullTime
		if err := tx.QueryRowContext(ctx, `SELECT next_run_after FROM workers.worker_instances WHERE worker_instance_id=$1`, instance.WorkerInstanceID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	first := read()
	if !first.Valid || !first.Time.Equal(now.Add(time.Minute)) {
		t.Fatal(first)
	}
	if err := initializeSchedulerOccurrence(ctx, tx, instance.WorkerInstanceID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if next := read(); next != first {
		t.Fatal("seed postponed existing occurrence", next)
	}
	for _, condition := range []string{
		`paused=true`, `enabled=false`, `worker_key='main.other'`,
		`tick_policy_json='{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}'`,
	} {
		if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=NULL,`+condition+` WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
			t.Fatal(err)
		}
		if err := initializeSchedulerOccurrence(ctx, tx, instance.WorkerInstanceID, now); err != nil {
			t.Fatal(err)
		}
		if read().Valid {
			t.Fatal("seed activated stopped/unrelated worker", condition)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances SET paused=false,enabled=true,worker_key='main.automation_scheduler' WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSchedulerWakeDeadlineRefreshPostgres(t *testing.T) {
	db, _, instance, _ := workerPolicyPostgresFixture(t)
	ctx := t.Context()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Reuse the existing owned fixture and roll back every change, including
	// the scheduler kind if the disposable database has not seeded it yet.
	if _, err := tx.ExecContext(ctx, `INSERT INTO workers.worker_kinds
		SELECT (jsonb_populate_record(NULL::workers.worker_kinds,
			to_jsonb(kind) || jsonb_build_object('worker_kind',$2::text))).*
		FROM workers.worker_kinds kind WHERE worker_kind=$1
		ON CONFLICT (worker_kind) DO NOTHING
	`, instance.WorkerKind, KindAutomationScheduler); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	idle := now.Add(time.Minute)
	runtime := &schedulerWakeTestRuntime{testRuntime: newTestRuntime(KindAutomationScheduler), next: &now}
	registry := NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances
		SET worker_kind=$2,next_run_after=$3,config_json='{}',
		    tick_policy_json='{"mode":"interval","interval_seconds":60}'
		WHERE worker_instance_id=$1
	`, instance.WorkerInstanceID, KindAutomationScheduler, idle); err != nil {
		t.Fatal(err)
	}
	readDeadline := func() time.Time {
		t.Helper()
		var at time.Time
		if err := tx.QueryRowContext(ctx, `SELECT next_run_after FROM workers.worker_instances
			WHERE worker_instance_id=$1`, instance.WorkerInstanceID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	if err := refreshSchedulerWakeDeadlineTx(ctx, tx, registry, now); err != nil {
		t.Fatal(err)
	}
	if at := readDeadline(); !at.Equal(now) {
		t.Fatalf("wake kept idle deadline: %v", at)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances
		SET next_run_after=$2,last_success_at=$3 WHERE worker_instance_id=$1
	`, instance.WorkerInstanceID, idle, now); err != nil {
		t.Fatal(err)
	}
	floor := now.Add(2 * time.Second)
	runtime.next = &floor
	if err := refreshSchedulerWakeDeadlineTx(ctx, tx, registry, now); err != nil {
		t.Fatal(err)
	}
	if at := readDeadline(); !at.Equal(now.Add(2 * time.Second)) {
		t.Fatalf("wake bypassed active floor: %v", at)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE workers.worker_instances
		SET next_run_after=$2,consecutive_failures=1 WHERE worker_instance_id=$1
	`, instance.WorkerInstanceID, idle); err != nil {
		t.Fatal(err)
	}
	if err := refreshSchedulerWakeDeadlineTx(ctx, tx, registry, now); err != nil {
		t.Fatal(err)
	}
	if at := readDeadline(); !at.Equal(idle) {
		t.Fatalf("wake bypassed failure backoff: %v", at)
	}
}

type schedulerWakeTestRuntime struct {
	testRuntime
	next *time.Time
}

func (r *schedulerWakeTestRuntime) NextSchedulerWakeDeadline(context.Context, *sql.Tx, WorkerInstance, time.Time) (*time.Time, error) {
	return r.next, nil
}
