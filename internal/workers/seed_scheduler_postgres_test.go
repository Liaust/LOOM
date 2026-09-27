package workers

import (
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
