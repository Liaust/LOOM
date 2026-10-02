package automation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/workers"
	"loom.local/loom/internal/workers/runtimes"
)

type cancelledJobAdapter struct {
	db     *sql.DB
	cancel context.CancelFunc
}

type blockingSupervisorRuntime struct {
	workers.Runtime
	instances []workers.InstanceDescriptor
	entered   chan struct{}
}

func (r blockingSupervisorRuntime) DefaultInstances() []workers.InstanceDescriptor {
	return r.instances
}
func (r blockingSupervisorRuntime) RunOnce(ctx context.Context, _ workers.RunContext) (workers.RunResult, error) {
	close(r.entered)
	<-ctx.Done()
	return workers.RunResult{}, ctx.Err()
}

func TestSupervisorPostgresJobDoesNotBlockDispatcher(t *testing.T) {
	db, _, req := calendarDatabase(t)
	job := runtimes.NewJobRunnerRuntime(jobs.Service{}, "test")
	dispatch := runtimes.NewAutomationDispatcherRuntime(automation.Service{})
	jobEntered, dispatchEntered := make(chan struct{}), make(chan struct{})
	registry := workers.NewRegistry()
	for _, r := range []blockingSupervisorRuntime{
		{job, job.DefaultInstances(), jobEntered}, {dispatch, dispatch.DefaultInstances(), dispatchEntered},
	} {
		if err := registry.Register(r); err != nil {
			t.Fatal(err)
		}
	}
	svc := workers.NewService(db, registry, nil)
	if _, err := svc.SeedBuiltins(t.Context(), req, "main"); err != nil {
		t.Fatal(err)
	}
	// Exercise two due workers, without waiting for the dispatcher's startup delay.
	if _, err := db.ExecContext(t.Context(), `UPDATE workers.worker_instances SET next_run_after=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("supervisor did not stop")
		}
	})
	supervisor := workers.NewSupervisor(svc, nil)
	supervisor.PollInterval = 10 * time.Millisecond
	go func() { defer close(done); supervisor.Run(ctx, req) }()
	for name, ch := range map[string]chan struct{}{"job": jobEntered, "dispatcher": dispatchEntered} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s blocked behind another worker", name)
		}
	}
}

func (a cancelledJobAdapter) Execute(ctx context.Context, ec routing.ExecutionContext, _ json.RawMessage) (routing.ExecutionResult, error) {
	_, err := a.db.ExecContext(ctx, `INSERT INTO jobs.jobs(job_id,job_type,status,origin_actor_id,origin_node_id,execution_node_id,metadata)
		VALUES('job_cancelled_dispatch','script_run','queued',$1,$2,$2,$3::jsonb)`, ec.ActorID, ec.OriginNodeID,
		fmt.Sprintf(`{"routing":{"capability_call_id":%q}}`, ec.CapabilityCallID))
	if err != nil {
		return routing.ExecutionResult{}, err
	}
	a.cancel()
	return routing.ExecutionResult{JobID: "job_cancelled_dispatch"}, ctx.Err()
}

func TestDispatcherPostgresCancellationAndExpiredRecovery(t *testing.T) {
	db, _, req := calendarDatabase(t)
	p := seedCalendarTarget(t, db, req)
	if _, err := db.Exec(`INSERT INTO capabilities.provider_health(provider_id, health_status, availability_status)
		SELECT provider_id,'ok','available' FROM capabilities.providers WHERE compact_address='main@calendar'
		ON CONFLICT(provider_id) DO UPDATE SET health_status='ok',availability_status='available'`); err != nil {
		t.Fatal(err)
	}
	router := routing.NewService(db)
	svc := automation.NewService(db, router)
	d, err := svc.CreateSchedule(t.Context(), req, automation.CreateScheduleInput{
		ScheduleKey: "dispatcher_recovery", ScheduleKind: "interval", ScheduleExpr: "1h", Status: "paused",
		TargetCapability: "main@calendar.read", ProjectRef: p.ProjectID, ScopeRef: p.ProjectScopeID, RunAsActorRef: "owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	fire, err := svc.FireScheduleNow(t.Context(), req, d.Schedule.ScheduleID, automation.FireScheduleInput{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := router.Registry.Register("main@calendar", cancelledJobAdapter{db: db, cancel: cancel}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.DispatchInvocationNow(ctx, req, fire.Invocation.InvocationID, automation.DispatcherRunInput{})
	if err != nil {
		t.Fatal(err)
	}
	i, err := svc.GetInvocation(t.Context(), fire.Invocation.InvocationID)
	if err != nil {
		t.Fatal(err)
	}
	if i.Status != automation.InvocationStatusRequiresManualAction || i.JobID == nil || *i.JobID != "job_cancelled_dispatch" || i.LeaseExpiresAt != nil {
		raw, _ := json.Marshal(i)
		t.Fatalf("cancelled receipt lost: %s", raw)
	}
	var status, job string
	if err := db.QueryRow(`SELECT status,job_id FROM routing.capability_calls WHERE capability_call_id=$1`, i.CapabilityCallID).Scan(&status, &job); err != nil || status != "failed" || job != "job_cancelled_dispatch" {
		t.Fatalf("routing receipt status=%s job=%s err=%v", status, job, err)
	}
	// Recreate the historical crash window in this owned fixture: no receipt
	// IDs reached the invocation, but routing metadata still identifies its job.
	for _, state := range []string{"created", "queued", "running", "completed", "failed", "cancelled", "timed_out"} {
		if _, err := db.Exec(`UPDATE jobs.jobs SET status=$1 WHERE job_id='job_cancelled_dispatch'`, state); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE automation.invocations SET status='calling',job_id=NULL,capability_call_id=NULL,lease_expires_at=$2 WHERE invocation_id=$1`, i.InvocationID, time.Now().Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.DispatchInvocationNow(t.Context(), req, i.InvocationID, automation.DispatcherRunInput{}); err != nil {
			t.Fatal(err)
		}
		recovered, err := svc.GetInvocation(t.Context(), i.InvocationID)
		if err != nil {
			t.Fatal(err)
		}
		want := automation.InvocationStatusFailed
		if state == "created" || state == "queued" || state == "running" {
			want = automation.InvocationStatusCalling
		}
		if state == "completed" {
			want = automation.InvocationStatusSucceeded
		}
		if recovered.Status != want {
			t.Fatalf("%s -> %s want %s", state, recovered.Status, want)
		}
		if want != automation.InvocationStatusCalling && (recovered.JobID == nil || *recovered.JobID != job || recovered.LeaseExpiresAt != nil) {
			t.Fatalf("recovery lost job/lease: %+v", recovered)
		}
	}
	if _, err := svc.DispatchInvocationNow(t.Context(), req, i.InvocationID, automation.DispatcherRunInput{}); err != nil {
		t.Fatal(err)
	}
	var calls, jobs int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM routing.capability_calls), (SELECT count(*) FROM jobs.jobs)`).Scan(&calls, &jobs); err != nil || calls != 1 || jobs != 1 {
		t.Fatalf("recovery replayed work: %d calls %d jobs %v", calls, jobs, err)
	}
}
