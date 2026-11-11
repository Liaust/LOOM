package workers

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loom.local/loom/internal/migrations"
)

func TestQueueWakesCoalesceAndKeepLanesIndependent(t *testing.T) {
	w := NewQueueWakes()
	for i := 0; i < 100; i++ {
		w.signal(KindJobRunner)
	}
	if len(w.jobs) != 1 || len(w.notes) != 0 || len(w.background) != 0 {
		t.Fatal("job hints leaked or did not coalesce")
	}
	w.catchUp()
	if len(w.jobs) != 1 || len(w.notes) != 1 || len(w.background) != 1 {
		t.Fatal("catch-up did not wake every lane")
	}
	if err := NotifyQueueTx(t.Context(), nil, "secret/path\n"); err == nil {
		t.Fatal("invalid notification payload accepted")
	}
}

func TestQueueWakesInvalidateOnlyTheChangedKind(t *testing.T) {
	w := NewQueueWakes()
	w.signal("notes_projection")
	w.signal("notes_projection")
	<-w.background
	probes := map[string]time.Time{"projection": time.Now(), "indexer": time.Now()}
	kinds := map[string]string{"projection": "notes_projection", "indexer": KindKnowledgeIndexer}
	invalidateWorkerProbes(probes, kinds, w.take("background"))
	if len(probes) != 1 || probes["indexer"].IsZero() {
		t.Fatal("unrelated readiness suppression was discarded")
	}
	w.catchUp()
	invalidateWorkerProbes(probes, kinds, w.take("background"))
	if len(probes) != 0 {
		t.Fatal("reconnect did not invalidate all probes")
	}
}

func wakePostgresDB(t *testing.T) *sql.DB {
	t.Helper()
	u := os.Getenv("LOOM_TEST_DB_URL")
	if u == "" {
		t.Skip("LOOM_TEST_DB_URL is unset")
	}
	if _, err := migrations.Up(t.Context(), u, filepath.Join("..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", u)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestQueueWakeCommitRollbackAndReconnectPostgres(t *testing.T) {
	db := wakePostgresDB(t)
	w := NewQueueWakes()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Listen(ctx, os.Getenv("LOOM_TEST_DB_URL"), nil)
	}()
	t.Cleanup(func() { cancel(); <-done })
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("queue wake not delivered")
		}
	}
	wait(w.jobs)
	wait(w.notes)
	wait(w.background)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := NotifyQueueTx(t.Context(), tx, KindJobRunner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.jobs:
		t.Fatal("rollback emitted a wake")
	case <-time.After(100 * time.Millisecond):
	}
	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := NotifyQueueTx(t.Context(), tx, KindJobRunner); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wait(w.jobs)
	// Only this disposable database's dedicated listener is interrupted.
	var stopped bool
	if err := db.QueryRowContext(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		WHERE datname=current_database() AND application_name='loom-worker-wake' AND pid<>pg_backend_pid() LIMIT 1`).Scan(&stopped); err != nil || !stopped {
		t.Fatalf("listener interruption: %v %v", stopped, err)
	}
	wait(w.background) // Disconnect catch-up.
	wait(w.jobs)
	wait(w.notes)
	wait(w.background) // LISTEN-before-catch-up after reconnect.
	wait(w.jobs)
	wait(w.notes)
	if _, err := db.ExecContext(t.Context(), `SELECT pg_notify('loom_worker_wake','job_runner')`); err != nil {
		t.Fatal(err)
	}
	wait(w.jobs)
}

type readinessTestRuntime struct {
	testRuntime
	ready  bool
	next   *time.Time
	checks int
}

func (r *readinessTestRuntime) AutomaticReady(context.Context, WorkerInstance, time.Time) (bool, *time.Time, error) {
	r.checks++
	return r.ready, r.next, nil
}

func TestSupervisorReadinessSkipsIdleWithoutWritesPostgres(t *testing.T) {
	_ = wakePostgresDB(t)
	db, req, instance, _ := workerPolicyPostgresFixture(t)
	now := time.Now().UTC()
	retry := now.Add(20 * time.Second)
	r := &readinessTestRuntime{testRuntime: newTestRuntime(instance.WorkerKind), next: &retry}
	registry := NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(NewService(db, registry, nil), nil)
	if _, err := db.Exec(`UPDATE workers.worker_instances SET tick_policy_json='{"mode":"interval","interval_seconds":30}',next_run_after=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	probes := map[string]time.Time{"retired": now.Add(time.Hour)}
	for i := 0; i < 2; i++ {
		result, err := s.runDueWithReadiness(t.Context(), req, now, "background", probes, time.Minute)
		if err != nil || result.Started != 0 {
			t.Fatalf("idle probe result=%+v error=%v", result, err)
		}
	}
	if r.checks != 1 {
		t.Fatalf("probe backoff lost: checks=%d", r.checks)
	}
	deadline, err := s.Service.nextSupervisorDeadline(t.Context(), "background", now, time.Minute, probes)
	if err != nil || !deadline.Equal(retry) {
		t.Fatalf("retry deadline=%v error=%v", deadline, err)
	}
	if _, ok := probes["retired"]; ok {
		t.Fatal("retired probe was not discarded")
	}
	var writes int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM workers.worker_runs WHERE worker_instance_id=$1)
		+(SELECT count(*) FROM workers.worker_controls WHERE worker_instance_id=$1)
		+(SELECT count(*) FROM workers.worker_leases WHERE worker_instance_id=$1)`, instance.WorkerInstanceID).Scan(&writes); err != nil || writes != 0 {
		t.Fatalf("idle probe wrote execution records: %d %v", writes, err)
	}
	// A committed wake invalidates the read cache, never the locked policy.
	clear(probes)
	r.ready = true
	result, err := s.runDueWithReadiness(t.Context(), req, now, "background", probes, time.Minute)
	if err != nil || result.Started != 1 {
		t.Fatalf("ready result=%+v error=%v", result, err)
	}
	if _, err := db.Exec(`UPDATE workers.worker_instances SET tick_policy_json='{"mode":"manual"}',next_run_after=NULL WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	clear(probes)
	result, err = s.runDueWithReadiness(t.Context(), req, now, "background", probes, time.Minute)
	if err != nil || result.Started != 0 || r.checks != 2 {
		t.Fatalf("wake bypassed manual policy: %+v checks=%d err=%v", result, r.checks, err)
	}
}

func TestQueueWakeAdvancesOnlyEligibleIdleDeadlinePostgres(t *testing.T) {
	_ = wakePostgresDB(t)
	db, req, instance, _ := workerPolicyPostgresFixture(t, KindJobRunner)
	now := time.Now().UTC().Truncate(time.Microsecond)
	idle := now.Add(time.Minute)
	if _, err := db.Exec(`UPDATE workers.worker_instances SET tick_policy_json='{"mode":"interval","interval_seconds":60}',next_run_after=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, idle); err != nil {
		t.Fatal(err)
	}
	r := &readinessTestRuntime{testRuntime: newTestRuntime(KindJobRunner)}
	registry := NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	s := NewSupervisor(NewService(db, registry, nil), nil)
	wake := func() {
		t.Helper()
		if err := s.Service.refreshQueueWakeDeadlines(t.Context(), map[string]bool{KindJobRunner: true}, now, "jobs"); err != nil {
			t.Fatal(err)
		}
	}
	wake()
	current, err := resolveWorkerInstance(t.Context(), db, instance.WorkerInstanceID)
	if err != nil || current.NextRunAfter == nil || !current.NextRunAfter.Equal(idle) {
		t.Fatalf("empty queue changed deadline: %+v %v", current, err)
	}
	retry := now.Add(time.Second)
	r.next = &retry
	wake()
	current, err = resolveWorkerInstance(t.Context(), db, instance.WorkerInstanceID)
	if err != nil || current.NextRunAfter == nil || !current.NextRunAfter.Equal(retry) {
		t.Fatalf("eligible retry deadline ignored: %+v %v", current, err)
	}
	r.ready = true
	wake()
	result, err := s.runDueOnce(t.Context(), req, now, "jobs")
	if err != nil || result.Started != 1 {
		t.Fatalf("committed ready work waited for idle interval: %+v %v", result, err)
	}
	if _, err := db.Exec(`UPDATE workers.worker_instances SET next_run_after=$2,consecutive_failures=1 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, idle); err != nil {
		t.Fatal(err)
	}
	wake()
	current, err = resolveWorkerInstance(t.Context(), db, instance.WorkerInstanceID)
	if err != nil || current.NextRunAfter == nil || !current.NextRunAfter.Equal(idle) {
		t.Fatalf("wake bypassed failure cooldown: %+v %v", current, err)
	}
}
