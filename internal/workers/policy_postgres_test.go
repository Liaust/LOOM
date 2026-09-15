package workers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

func TestWorkerPolicyLifecycleAndSupervisorPostgres(t *testing.T) {
	db, req, instance, registry := workerPolicyPostgresFixture(t)
	service := NewService(db, registry, nil)
	ctx := context.Background()

	initial, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	if initial.Policy.Mode != TickModeManual || initial.PolicyFingerprint == "" {
		t.Fatalf("initial policy = %#v", initial)
	}
	// Seed refreshes still adopt descriptor defaults until an operator policy
	// marker exists.
	seedPolicy := func(raw json.RawMessage) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, created, err := upsertWorkerInstance(ctx, tx, policyTestSeedDescriptor(instance, raw), req.OriginNodeID); err != nil || created {
			_ = tx.Rollback()
			t.Fatalf("seed policy refresh created=%t err=%v", created, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seedPolicy(json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"interval","interval_seconds":75}`))
	seededDefault, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || seededDefault.Policy.Mode != TickModeInterval || seededDefault.Policy.IntervalSeconds != 75 {
		t.Fatalf("uncontrolled seed did not adopt descriptor policy: %#v err=%v", seededDefault, err)
	}
	seedPolicy(json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`))
	initial, err = service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || initial.Policy.Mode != TickModeManual {
		t.Fatalf("restored seed default = %#v err=%v", initial, err)
	}
	staleManualNextRun := time.Now().UTC().Add(time.Hour)
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, staleManualNextRun); err != nil {
		t.Fatal(err)
	}
	manualWithStaleSchedule, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || manualWithStaleSchedule.NextRunAfter == nil {
		t.Fatalf("manual stale schedule fixture = %#v err=%v", manualWithStaleSchedule, err)
	}
	clearedManual, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy:                    initial.Policy,
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "clear inconsistent manual schedule",
		Confirm:                   true,
		IdempotencyKey:            "policy-clear-manual-next-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !clearedManual.Changed || clearedManual.New.NextRunAfter != nil || clearedManual.New.PolicyFingerprint != initial.PolicyFingerprint {
		t.Fatalf("manual schedule correction = %#v", clearedManual)
	}
	initial = clearedManual.New

	dryRunInput := SetWorkerPolicyInput{
		Policy:                    TickPolicy{Mode: TickModeInterval, IntervalSeconds: 30, RunOnStartup: true},
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		DryRun:                    true,
	}
	dryRun, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, dryRunInput)
	if err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.Applied || !dryRun.Changed || dryRun.New.NextRunAfter == nil {
		t.Fatalf("dry-run = %#v", dryRun)
	}
	unchanged, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || unchanged.PolicyFingerprint != initial.PolicyFingerprint || unchanged.NextRunAfter != nil {
		t.Fatalf("dry-run mutated policy: %#v err=%v", unchanged, err)
	}

	applyInput := dryRunInput
	applyInput.DryRun = false
	applyInput.Confirm = true
	applyInput.Reason = "automatic acceptance cycle"
	applyInput.IdempotencyKey = "policy-lifecycle-apply"
	applied, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || !applied.Changed || applied.IdempotentReplay || applied.EventType == "" || applied.New.PolicyFingerprint == initial.PolicyFingerprint || applied.New.NextRunAfter == nil {
		t.Fatalf("applied = %#v", applied)
	}
	var eventActor, eventReason, eventOldMode, eventNewMode, eventCorrelation string
	var eventHasIdempotencyKey bool
	if err := db.QueryRowContext(ctx, `
		SELECT actor_id,payload->>'reason',payload->'old_policy'->>'mode',payload->'new_policy'->>'mode',payload->>'correlation_id',payload ? 'idempotency_key'
		FROM events.events
		WHERE target_id=$1 AND event_type='worker.instance.updated'
		ORDER BY created_at DESC LIMIT 1
	`, instance.WorkerInstanceID).Scan(&eventActor, &eventReason, &eventOldMode, &eventNewMode, &eventCorrelation, &eventHasIdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if eventActor != req.ActorID || eventReason != applyInput.Reason || eventOldMode != TickModeManual || eventNewMode != TickModeInterval || eventCorrelation != req.CorrelationID || eventHasIdempotencyKey {
		t.Fatalf("worker policy event actor=%q reason=%q old=%q new=%q correlation=%q has_idempotency=%t", eventActor, eventReason, eventOldMode, eventNewMode, eventCorrelation, eventHasIdempotencyKey)
	}
	var storedIdempotencyHash string
	var metadataHasRawIdempotencyKey bool
	if err := db.QueryRowContext(ctx, `
		SELECT metadata->'policy_control'->>'idempotency_key_hash', metadata->'policy_control' ? 'idempotency_key'
		FROM workers.worker_instances
		WHERE worker_instance_id=$1
	`, instance.WorkerInstanceID).Scan(&storedIdempotencyHash, &metadataHasRawIdempotencyKey); err != nil {
		t.Fatal(err)
	}
	if storedIdempotencyHash != workerPolicyIdempotencyKeyHash(applyInput.IdempotencyKey) || metadataHasRawIdempotencyKey {
		t.Fatalf("policy metadata idempotency_hash=%q has_raw_key=%t", storedIdempotencyHash, metadataHasRawIdempotencyKey)
	}
	retryRunID := ids.NewWorkerRunID()
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_runs (worker_run_id,worker_instance_id,worker_kind,run_status,trigger_kind,trigger_ref,correlation_id,idempotency_key) VALUES ($1,$2,$3,'running','manual','retry-window','corr_test','policy-retry-window')`, retryRunID, instance.WorkerInstanceID, instance.WorkerKind); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET current_run_id=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, retryRunID); err != nil {
		t.Fatal(err)
	}
	replayDuringRun, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, applyInput)
	if err != nil || !replayDuringRun.IdempotentReplay {
		t.Fatalf("identical retry during active run = %#v err=%v", replayDuringRun, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET current_run_id=NULL WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM workers.worker_runs WHERE worker_run_id=$1`, retryRunID); err != nil {
		t.Fatal(err)
	}
	samePolicy, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy:                    applied.New.Policy,
		ExpectedPolicyFingerprint: applied.New.PolicyFingerprint,
		Reason:                    "repeat identical policy",
		Confirm:                   true,
		IdempotencyKey:            "policy-lifecycle-identical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if samePolicy.Changed || !equalOptionalTimes(samePolicy.New.NextRunAfter, applied.New.NextRunAfter) {
		t.Fatalf("identical policy reset scheduling state: %#v", samePolicy)
	}
	if _, err := service.RunOnce(ctx, req, instance.WorkerKey, RunOnceInput{
		TriggerKind:               TriggerSupervisorTick,
		TriggerRef:                instance.WorkerKey,
		IdempotencyKey:            "stale-supervisor-selection",
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		ScheduleEvidenceAt:        time.Now().UTC(),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale supervisor selection error = %v, want ErrConflict", err)
	}

	replayed, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, applyInput)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.IdempotentReplay || replayed.Old.PolicyFingerprint != initial.PolicyFingerprint || replayed.New.PolicyFingerprint != applied.New.PolicyFingerprint {
		t.Fatalf("replay = %#v", replayed)
	}
	stale := applyInput
	stale.IdempotencyKey = "policy-lifecycle-stale"
	stale.Policy.IntervalSeconds = 45
	if _, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}

	// A daemon seed refresh must retain the explicit policy marker and policy.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := InstanceDescriptor{
		WorkerKey:          instance.WorkerKey,
		WorkerKind:         instance.WorkerKind,
		DisplayName:        "Seed refresh",
		Description:        "Seed refresh must preserve operator policy.",
		Locality:           LocalityMainOwned,
		ConfigJSON:         json.RawMessage(`{"schema_version":"worker_selfcheck.config.v0.2"}`),
		TickPolicyJSON:     json.RawMessage(`{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}`),
		ConcurrencyJSON:    json.RawMessage(`{}`),
		RetryPolicyJSON:    json.RawMessage(`{}`),
		TimeoutPolicyJSON:  json.RawMessage(`{}`),
		ResourceLimitsJSON: json.RawMessage(`{}`),
		VisibilityJSON:     json.RawMessage(`{}`),
		Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"test"}`),
	}
	if _, created, err := upsertWorkerInstance(ctx, tx, descriptor, req.OriginNodeID); err != nil || created {
		_ = tx.Rollback()
		t.Fatalf("seed refresh created=%t err=%v", created, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	afterSeed, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || afterSeed.PolicyFingerprint != applied.New.PolicyFingerprint {
		t.Fatalf("seed overwrote operator policy: %#v err=%v", afterSeed, err)
	}

	past := time.Now().UTC().Add(-time.Second)
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET next_run_after=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, past); err != nil {
		t.Fatal(err)
	}
	evidenceAt := time.Now().UTC()
	dueWorkers, err := service.ListDueWorkers(ctx, evidenceAt, 200)
	if err != nil {
		t.Fatal(err)
	}
	foundDue := false
	for _, candidate := range dueWorkers {
		if candidate.WorkerInstanceID == instance.WorkerInstanceID {
			foundDue = true
		}
	}
	if !foundDue {
		t.Fatalf("interval worker missing from due set: %#v", dueWorkers)
	}
	supervisorRun, err := service.RunOnce(ctx, req, instance.WorkerKey, RunOnceInput{
		Reason:                    "supervisor tick",
		TriggerKind:               TriggerSupervisorTick,
		TriggerRef:                instance.WorkerKey,
		IdempotencyKey:            "policy-supervisor-run",
		ExpectedPolicyFingerprint: afterSeed.PolicyFingerprint,
		ScheduleEvidenceAt:        evidenceAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if supervisorRun.Run.TriggerKind != TriggerSupervisorTick || supervisorRun.Run.RunStatus != RunStatusSucceeded {
		t.Fatalf("supervisor run = %#v", supervisorRun.Run)
	}

	current, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, SetWorkerPolicyInput{
		Policy:                    initial.Policy,
		ExpectedPolicyFingerprint: current.PolicyFingerprint,
		Reason:                    "restore captured prior policy",
		Confirm:                   true,
		IdempotencyKey:            "policy-lifecycle-restore",
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.New.PolicyFingerprint != initial.PolicyFingerprint || restored.New.NextRunAfter != nil {
		t.Fatalf("restore = %#v", restored)
	}
	notDue, err := service.ListDueWorkers(ctx, time.Now().UTC().Add(24*time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range notDue {
		if candidate.WorkerInstanceID == instance.WorkerInstanceID {
			t.Fatalf("restored manual worker remained due: %#v", candidate)
		}
	}
}

func TestWorkerPolicyConflictsAndConcurrencyPostgres(t *testing.T) {
	db, req, instance, registry := workerPolicyPostgresFixture(t)
	service := NewService(db, registry, nil)
	ctx := context.Background()
	initial, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil {
		t.Fatal(err)
	}
	badEventReq := req
	badEventReq.ActorID = "actor_missing_policy_test"
	if _, err := service.SetWorkerPolicy(ctx, badEventReq, instance.WorkerKey, SetWorkerPolicyInput{
		Policy:                    TickPolicy{Mode: TickModeInterval, IntervalSeconds: 30},
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "event transaction rollback",
		Confirm:                   true,
		IdempotencyKey:            "policy-event-rollback",
	}); err == nil {
		t.Fatal("policy update unexpectedly committed without its audit event")
	}
	afterEventFailure, err := service.InspectWorkerPolicy(ctx, instance.WorkerKey)
	if err != nil || afterEventFailure.PolicyFingerprint != initial.PolicyFingerprint || afterEventFailure.NextRunAfter != nil {
		t.Fatalf("event failure left partial policy state: %#v err=%v", afterEventFailure, err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET enabled=false WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	input := SetWorkerPolicyInput{Policy: TickPolicy{Mode: TickModeInterval, IntervalSeconds: 30}, ExpectedPolicyFingerprint: initial.PolicyFingerprint, Reason: "test", Confirm: true, IdempotencyKey: "disabled"}
	if _, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET enabled=true, locality=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, LocalityNodeAgentOwned); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("remote locality error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET locality=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, LocalityMainOwned); err != nil {
		t.Fatal(err)
	}
	runID := ids.NewWorkerRunID()
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_runs (worker_run_id,worker_instance_id,worker_kind,run_status,trigger_kind,trigger_ref,correlation_id,idempotency_key) VALUES ($1,$2,$3,'running','manual','test','corr_test','policy-active-run')`, runID, instance.WorkerInstanceID, instance.WorkerKind); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET current_run_id=$2 WHERE worker_instance_id=$1`, instance.WorkerInstanceID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("active run error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET current_run_id=NULL WHERE worker_instance_id=$1`, instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM workers.worker_runs WHERE worker_run_id=$1`, runID); err != nil {
		t.Fatal(err)
	}

	leaseID := ids.NewWorkerLeaseID()
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_leases (worker_lease_id,worker_instance_id,lease_key,lease_status,generation,holder_id,holder_kind,acquired_at,renewed_at,expires_at) VALUES ($1,$2,$3,'active',1,'test','loomd',now(),now(),now()+interval '1 minute')`, leaseID, instance.WorkerInstanceID, "worker:"+instance.WorkerInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetWorkerPolicy(ctx, req, instance.WorkerKey, input); !errors.Is(err, ErrConflict) {
		t.Fatalf("active lease error = %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM workers.worker_leases WHERE worker_lease_id=$1`, leaseID); err != nil {
		t.Fatal(err)
	}

	inputs := []SetWorkerPolicyInput{
		{Policy: TickPolicy{Mode: TickModeInterval, IntervalSeconds: 30}, ExpectedPolicyFingerprint: initial.PolicyFingerprint, Reason: "first", Confirm: true, IdempotencyKey: "concurrent-first"},
		{Policy: TickPolicy{Mode: TickModeInterval, IntervalSeconds: 45}, ExpectedPolicyFingerprint: initial.PolicyFingerprint, Reason: "second", Confirm: true, IdempotencyKey: "concurrent-second"},
	}
	errs := make([]error, len(inputs))
	var wait sync.WaitGroup
	for index := range inputs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errs[index] = service.SetWorkerPolicy(ctx, req, instance.WorkerKey, inputs[index])
		}(index)
	}
	wait.Wait()
	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent update error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent results successes=%d conflicts=%d errors=%v", successes, conflicts, errs)
	}
}

func policyTestSeedDescriptor(instance WorkerInstance, tickPolicy json.RawMessage) InstanceDescriptor {
	return InstanceDescriptor{
		WorkerKey:          instance.WorkerKey,
		WorkerKind:         instance.WorkerKind,
		DisplayName:        "Seed refresh",
		Description:        "Seed refresh policy behavior.",
		Locality:           LocalityMainOwned,
		ConfigJSON:         json.RawMessage(`{"schema_version":"worker_selfcheck.config.v0.2"}`),
		TickPolicyJSON:     tickPolicy,
		ConcurrencyJSON:    json.RawMessage(`{}`),
		RetryPolicyJSON:    json.RawMessage(`{}`),
		TimeoutPolicyJSON:  json.RawMessage(`{}`),
		ResourceLimitsJSON: json.RawMessage(`{}`),
		VisibilityJSON:     json.RawMessage(`{}`),
		Metadata:           json.RawMessage(`{"schema_version":"worker_instance.metadata.v0.2","seeded_by":"test"}`),
	}
}

func workerPolicyPostgresFixture(t *testing.T) (*sql.DB, requestctx.Context, WorkerInstance, *Registry) {
	t.Helper()
	dbURL := os.Getenv("LOOM_TEST_DB_URL")
	if dbURL == "" {
		t.Skip("LOOM_TEST_DB_URL is not set")
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := bootstrap.NewService(db).EnsureDevBootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_worker_policy_test")
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	kind := "policy_test_" + suffix
	workerKey := "main." + kind
	instanceID := ids.NewWorkerInstanceID()
	runtime := newTestRuntime(kind)
	registry := NewRegistry()
	if err := registry.Register(runtime); err != nil {
		t.Fatal(err)
	}
	descriptor := runtime.Describe()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers.worker_kinds (
			worker_kind,display_name,description,runtime_owner,runtime_package,status,
			supported_localities,default_tick_policy_json,default_concurrency_policy_json,
			default_retry_policy_json,default_timeout_policy_json,default_resource_limits_json,
			config_schema_json,checkpoint_schema_json,result_schema_json,metadata
		) VALUES ($1,$2,$3,$4,'test','active',$5::text[],$6::jsonb,$7::jsonb,$8::jsonb,$9::jsonb,$10::jsonb,$11::jsonb,$12::jsonb,$13::jsonb,$14::jsonb)
	`, kind, descriptor.DisplayName, descriptor.Description, descriptor.RuntimeOwner, `{main_owned}`,
		descriptor.DefaultTickPolicyJSON, descriptor.DefaultConcurrencyPolicyJSON, descriptor.DefaultRetryPolicyJSON,
		descriptor.DefaultTimeoutPolicyJSON, descriptor.DefaultResourceLimitsJSON, descriptor.ConfigSchemaJSON,
		descriptor.CheckpointSchemaJSON, descriptor.ResultSchemaJSON, descriptor.Metadata); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workers.worker_instances (
			worker_instance_id,worker_key,worker_kind,display_name,description,owner_node_id,host_node_id,
			scope_id,locality,lifecycle_status,enabled,paused,config_json,tick_policy_json,
			concurrency_policy_json,retry_policy_json,timeout_policy_json,resource_limits_json,visibility_json,metadata
		) VALUES ($1,$2,$3,'Policy test','Policy test',$4,$4,$5,'main_owned','active',true,false,$6::jsonb,$7::jsonb,'{}'::jsonb,'{}'::jsonb,$8::jsonb,'{}'::jsonb,'{}'::jsonb,'{}'::jsonb)
	`, instanceID, workerKey, kind, req.OriginNodeID, req.ScopeID, runtime.DefaultConfig(), descriptor.DefaultTickPolicyJSON, descriptor.DefaultTimeoutPolicyJSON); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM events.events WHERE target_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `UPDATE workers.worker_instances SET current_run_id=NULL,last_run_id=NULL WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_health WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_controls WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_leases WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_runs WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_instances WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_kinds WHERE worker_kind=$1`, kind)
	})
	return db, req, WorkerInstance{WorkerInstanceID: instanceID, WorkerKey: workerKey, WorkerKind: kind}, registry
}
