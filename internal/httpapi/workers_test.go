package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/idempotency"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/workers"
)

func TestWorkerPolicyRouteRejectsUnsafeShapesBeforeService(t *testing.T) {
	server := NewServer(Services{}, slog.Default()).Handler()
	for _, test := range []struct {
		name   string
		method string
		body   string
		status int
	}{
		{"unsupported method", http.MethodDelete, "", http.StatusMethodNotAllowed},
		{"malformed json", http.MethodPost, `{`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, `{"dry_run":true,"unknown":true}`, http.StatusBadRequest},
		{"multiple objects", http.MethodPost, `{"dry_run":true}{"dry_run":true}`, http.StatusBadRequest},
		{"oversized body", http.MethodPost, `{"reason":"` + strings.Repeat("x", (16<<10)+1) + `"}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(test.method, "/v1/workers/main.main_backup/policy", strings.NewReader(test.body))
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.status, rec.Body.String())
			}
		})
	}
}

func TestWorkerPolicyRouteDryRunApplyReplayAndConflictPostgres(t *testing.T) {
	db, workerKey, instanceID := httpWorkerPolicyPostgresFixture(t)
	server := NewServer(Services{
		DB:          db,
		Workers:     workers.NewService(db, nil, slog.Default()),
		Idempotency: idempotency.NewService(db),
	}, slog.Default()).Handler()

	inspectReq := httptest.NewRequest(http.MethodGet, "/v1/workers/"+workerKey+"/policy", nil)
	inspectRec := httptest.NewRecorder()
	server.ServeHTTP(inspectRec, inspectReq)
	if inspectRec.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", inspectRec.Code, inspectRec.Body.String())
	}
	var inspected response.Envelope[workers.WorkerPolicyState]
	if err := json.Unmarshal(inspectRec.Body.Bytes(), &inspected); err != nil {
		t.Fatal(err)
	}
	if inspected.Data.Policy.Mode != workers.TickModeManual || inspected.Data.PolicyFingerprint == "" {
		t.Fatalf("inspect = %#v", inspected.Data)
	}

	dryRunBody := workerPolicyRequestBody(t, workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{SchemaVersion: workers.TickPolicySchemaVersion, Mode: workers.TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: inspected.Data.PolicyFingerprint,
		DryRun:                    true,
	})
	dryRunReq := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(dryRunBody))
	dryRunRec := httptest.NewRecorder()
	server.ServeHTTP(dryRunRec, dryRunReq)
	if dryRunRec.Code != http.StatusOK {
		t.Fatalf("dry-run status=%d body=%s", dryRunRec.Code, dryRunRec.Body.String())
	}
	var dryRun response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(dryRunRec.Body.Bytes(), &dryRun); err != nil {
		t.Fatal(err)
	}
	if !dryRun.Data.DryRun || dryRun.Data.Applied || !dryRun.Data.Changed || dryRun.Data.New.Policy.Mode != workers.TickModeDailyLocal || dryRun.Data.New.Policy.LocalTime != "03:15" || dryRun.Data.New.Policy.Timezone != "Europe/Amsterdam" {
		t.Fatalf("dry-run = %#v", dryRun.Data)
	}

	applyInput := workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{SchemaVersion: workers.TickPolicySchemaVersion, Mode: workers.TickModeDailyLocal, LocalTime: "03:15", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: inspected.Data.PolicyFingerprint,
		Reason:                    "accelerated acceptance",
		Confirm:                   true,
	}
	applyBody := workerPolicyRequestBody(t, applyInput)
	apply := func(body []byte, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(body))
		req.Header.Set(idempotency.Header, key)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	first := apply(applyBody, "http-policy-replay-"+instanceID)
	if first.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", first.Code, first.Body.String())
	}
	var applied response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(first.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if !applied.Data.Applied || !applied.Data.Changed || applied.Meta.IdempotencyKey == "" || applied.Data.EventType == "" {
		t.Fatalf("apply = %#v meta=%#v", applied.Data, applied.Meta)
	}
	replay := apply(applyBody, "http-policy-replay-"+instanceID)
	var replayed response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(replay.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if replay.Code != http.StatusOK || replayed.Meta.IdempotencyKey != applied.Meta.IdempotencyKey || replayed.Data.New.PolicyFingerprint != applied.Data.New.PolicyFingerprint || replayed.Data.Old.PolicyFingerprint != applied.Data.Old.PolicyFingerprint {
		t.Fatalf("replay status=%d\nfirst=%#v\nreplay=%#v", replay.Code, applied, replayed)
	}

	different := apply(workerPolicyRequestBody(t, workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeDailyLocal, LocalTime: "03:30", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: inspected.Data.PolicyFingerprint,
		Reason:                    "different",
		Confirm:                   true,
	}), "http-policy-replay-"+instanceID)
	if different.Code != http.StatusConflict || !strings.Contains(different.Body.String(), "idempotency.conflict") {
		t.Fatalf("different replay status=%d body=%s", different.Code, different.Body.String())
	}
	stale := apply(workerPolicyRequestBody(t, workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeDailyLocal, LocalTime: "03:45", Timezone: "Europe/Amsterdam"},
		ExpectedPolicyFingerprint: inspected.Data.PolicyFingerprint,
		Reason:                    "stale",
		Confirm:                   true,
	}), "http-policy-stale-"+instanceID)
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), "worker.policy_set_failed") {
		t.Fatalf("stale status=%d body=%s", stale.Code, stale.Body.String())
	}

	var eventCount int
	if err := db.QueryRow(`SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='worker.instance.updated'`, instanceID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("worker update events=%d, want 1", eventCount)
	}
}

func TestWorkerPolicyRouteRecoversCommittedInProgressIdempotentApplyPostgres(t *testing.T) {
	db, workerKey, instanceID := httpWorkerPolicyPostgresFixture(t)
	workerService := workers.NewService(db, nil, slog.Default())
	idempotencyService := idempotency.NewService(db)
	server := NewServer(Services{
		DB:          db,
		Workers:     workerService,
		Idempotency: idempotencyService,
	}, slog.Default()).Handler()
	ctx := context.Background()
	reqCtx, err := requestctx.ResolveBootstrap(ctx, db, "corr_http_worker_policy_recovery")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workerService.InspectWorkerPolicy(ctx, workerKey)
	if err != nil {
		t.Fatal(err)
	}
	key := "http-policy-in-progress-" + instanceID
	input := workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30},
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "recover committed policy update",
		Confirm:                   true,
		IdempotencyKey:            key,
	}
	input, err = workers.NormalizeSetWorkerPolicyInput(input)
	if err != nil {
		t.Fatal(err)
	}
	begin, err := idempotencyService.Begin(ctx, idempotency.BeginInput{
		ActorID:   reqCtx.ActorID,
		NodeID:    reqCtx.OriginNodeID,
		Key:       key,
		Operation: "worker.policy.set",
		Request:   map[string]any{"worker_ref": workerKey, "input": input},
	})
	if err != nil || begin.Decision != idempotency.DecisionNew {
		t.Fatalf("begin decision=%q err=%v", begin.Decision, err)
	}
	if _, err := workerService.SetWorkerPolicy(ctx, reqCtx, workerKey, input); err != nil {
		t.Fatal(err)
	}

	retry := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, input)))
	retry.Header.Set(idempotency.Header, key)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, retry)
	if rec.Code != http.StatusOK {
		t.Fatalf("recovery status=%d body=%s", rec.Code, rec.Body.String())
	}
	var recovered response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(rec.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if !recovered.Data.IdempotentReplay || recovered.Data.New.PolicyFingerprint == initial.PolicyFingerprint || recovered.Meta.IdempotencyKey != key {
		t.Fatalf("recovered = %#v meta=%#v", recovered.Data, recovered.Meta)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM interface.idempotency_keys WHERE idempotency_id=$1`, begin.Record.IdempotencyID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != idempotency.StatusCompleted {
		t.Fatalf("idempotency status=%q, want completed", status)
	}
	var eventCount int
	if err := db.QueryRow(`SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='worker.instance.updated'`, instanceID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("worker update events=%d, want 1", eventCount)
	}
}

func TestWorkerPolicyConcurrentCompletionAndFailurePreserveSuccessPostgres(t *testing.T) {
	db, workerKey, instanceID := httpWorkerPolicyPostgresFixture(t)
	workerService := workers.NewService(db, nil, slog.Default())
	idempotencyService := idempotency.NewService(db)
	server := NewServer(Services{DB: db, Workers: workerService, Idempotency: idempotencyService}, slog.Default()).Handler()
	ctx := context.Background()
	reqCtx, err := requestctx.ResolveBootstrap(ctx, db, "corr_http_worker_policy_concurrent_terminal")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workerService.InspectWorkerPolicy(ctx, workerKey)
	if err != nil {
		t.Fatal(err)
	}
	key := "http-policy-concurrent-terminal-" + instanceID
	input, err := workers.NormalizeSetWorkerPolicyInput(workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30},
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "concurrent terminal outcome",
		Confirm:                   true,
		IdempotencyKey:            key,
	})
	if err != nil {
		t.Fatal(err)
	}
	beginInput := idempotency.BeginInput{
		ActorID: reqCtx.ActorID, NodeID: reqCtx.OriginNodeID, Key: key,
		Operation: "worker.policy.set", Request: map[string]any{"worker_ref": workerKey, "input": input},
	}
	begin, err := idempotencyService.Begin(ctx, beginInput)
	if err != nil || begin.Decision != idempotency.DecisionNew {
		t.Fatalf("begin decision=%q err=%v", begin.Decision, err)
	}
	inProgress, err := idempotencyService.Begin(ctx, beginInput)
	if err != nil || inProgress.Decision != idempotency.DecisionInProgress {
		t.Fatalf("matching concurrent decision=%q err=%v", inProgress.Decision, err)
	}
	var serviceResults [2]workers.SetWorkerPolicyResult
	var serviceErrs [2]error
	serviceStart := make(chan struct{})
	var serviceWait sync.WaitGroup
	for index := range serviceResults {
		serviceWait.Add(1)
		go func(index int) {
			defer serviceWait.Done()
			<-serviceStart
			serviceResults[index], serviceErrs[index] = workerService.SetWorkerPolicy(ctx, reqCtx, workerKey, input)
		}(index)
	}
	close(serviceStart)
	serviceWait.Wait()
	originals, replays := 0, 0
	var applied workers.SetWorkerPolicyResult
	for index, result := range serviceResults {
		if serviceErrs[index] != nil {
			t.Fatalf("concurrent policy service error[%d]=%v", index, serviceErrs[index])
		}
		if result.IdempotentReplay {
			replays++
		} else {
			originals++
			applied = result
		}
	}
	if originals != 1 || replays != 1 {
		t.Fatalf("concurrent policy results originals=%d replays=%d values=%#v", originals, replays, serviceResults)
	}

	lockTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var lockedID string
	if err := lockTx.QueryRowContext(ctx, `SELECT idempotency_id FROM interface.idempotency_keys WHERE idempotency_id=$1 FOR UPDATE`, begin.Record.IdempotencyID).Scan(&lockedID); err != nil {
		_ = lockTx.Rollback()
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	errs := make(chan error, 2)
	var terminalWait sync.WaitGroup
	terminalWait.Add(2)
	go func() {
		defer terminalWait.Done()
		started <- struct{}{}
		envelope := response.SuccessWithIdempotency(reqCtx.CorrelationID, key, applied)
		errs <- idempotencyService.Complete(ctx, begin.Record.IdempotencyID, "worker_instance", instanceID, envelope)
	}()
	go func() {
		defer terminalWait.Done()
		started <- struct{}{}
		errs <- idempotencyService.Fail(ctx, begin.Record.IdempotencyID, "transient.worker_failure", response.FailureWithIdempotency(reqCtx.CorrelationID, key, nil))
	}()
	<-started
	<-started
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	terminalWait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	retry := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, input)))
	retry.Header.Set(idempotency.Header, key)
	retryRec := httptest.NewRecorder()
	server.ServeHTTP(retryRec, retry)
	if retryRec.Code != http.StatusOK {
		t.Fatalf("completed replay status=%d body=%s", retryRec.Code, retryRec.Body.String())
	}
	var replayed response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(retryRec.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if !replayed.Data.Applied || !replayed.Data.Changed || replayed.Data.New.PolicyFingerprint != applied.New.PolicyFingerprint {
		t.Fatalf("completed replay=%#v", replayed.Data)
	}
	var status string
	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT status FROM interface.idempotency_keys WHERE idempotency_id=$1`, begin.Record.IdempotencyID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='worker.instance.updated'`, instanceID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if status != idempotency.StatusCompleted || eventCount != 1 {
		t.Fatalf("terminal status=%q events=%d", status, eventCount)
	}
}

func TestWorkerPolicyRouteRecoversCommittedNoOpAfterRunStartsPostgres(t *testing.T) {
	db, workerKey, instanceID := httpWorkerPolicyPostgresFixture(t)
	workerService := workers.NewService(db, nil, slog.Default())
	idempotencyService := idempotency.NewService(db)
	server := NewServer(Services{DB: db, Workers: workerService, Idempotency: idempotencyService}, slog.Default()).Handler()
	ctx := context.Background()
	reqCtx, err := requestctx.ResolveBootstrap(ctx, db, "corr_http_worker_policy_noop_recovery")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workerService.InspectWorkerPolicy(ctx, workerKey)
	if err != nil {
		t.Fatal(err)
	}
	key := "http-policy-noop-" + instanceID
	input, err := workers.NormalizeSetWorkerPolicyInput(workers.SetWorkerPolicyInput{
		Policy:                    initial.Policy,
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "confirm current manual policy",
		Confirm:                   true,
		IdempotencyKey:            key,
	})
	if err != nil {
		t.Fatal(err)
	}
	begin, err := idempotencyService.Begin(ctx, idempotency.BeginInput{
		ActorID: reqCtx.ActorID, NodeID: reqCtx.OriginNodeID, Key: key,
		Operation: "worker.policy.set", Request: map[string]any{"worker_ref": workerKey, "input": input},
	})
	if err != nil || begin.Decision != idempotency.DecisionNew {
		t.Fatalf("begin decision=%q err=%v", begin.Decision, err)
	}
	committed, err := workerService.SetWorkerPolicy(ctx, reqCtx, workerKey, input)
	if err != nil || committed.Changed || !committed.Applied || committed.EventType == "" {
		t.Fatalf("committed no-op=%#v err=%v", committed, err)
	}

	var workerKind string
	if err := db.QueryRowContext(ctx, `SELECT worker_kind FROM workers.worker_instances WHERE worker_instance_id=$1`, instanceID).Scan(&workerKind); err != nil {
		t.Fatal(err)
	}
	runID := ids.NewWorkerRunID()
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_runs (worker_run_id,worker_instance_id,worker_kind,run_status,trigger_kind,trigger_ref,correlation_id,idempotency_key) VALUES ($1,$2,$3,'running','manual','noop-recovery','corr_noop_recovery','noop-recovery-run')`, runID, instanceID, workerKind); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE workers.worker_instances SET current_run_id=$2 WHERE worker_instance_id=$1`, instanceID, runID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `UPDATE workers.worker_instances SET current_run_id=NULL WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM workers.worker_runs WHERE worker_run_id=$1`, runID)
	})

	retry := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, input)))
	retry.Header.Set(idempotency.Header, key)
	retryRec := httptest.NewRecorder()
	server.ServeHTTP(retryRec, retry)
	if retryRec.Code != http.StatusOK {
		t.Fatalf("no-op recovery status=%d body=%s", retryRec.Code, retryRec.Body.String())
	}
	var recovered response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(retryRec.Body.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if !recovered.Data.IdempotentReplay || recovered.Data.Changed || recovered.Data.Old.PolicyFingerprint != initial.PolicyFingerprint || recovered.Data.New.PolicyFingerprint != initial.PolicyFingerprint {
		t.Fatalf("recovered no-op=%#v", recovered.Data)
	}

	freshInput := workers.SetWorkerPolicyInput{
		Policy: initial.Policy, ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason: "fresh no-op while running", Confirm: true,
	}
	fresh := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, freshInput)))
	fresh.Header.Set(idempotency.Header, "http-policy-noop-fresh-"+instanceID)
	freshRec := httptest.NewRecorder()
	server.ServeHTTP(freshRec, fresh)
	if freshRec.Code != http.StatusConflict || !strings.Contains(freshRec.Body.String(), "worker.policy_set_failed") {
		t.Fatalf("fresh no-op status=%d body=%s", freshRec.Code, freshRec.Body.String())
	}

	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='worker.instance.updated'`, instanceID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("no-op update events=%d, want 1", eventCount)
	}
}

func TestWorkerPolicyRouteRecoversEarlierCommitAfterLaterPolicyUpdatePostgres(t *testing.T) {
	db, workerKey, instanceID := httpWorkerPolicyPostgresFixture(t)
	workerService := workers.NewService(db, nil, slog.Default())
	idempotencyService := idempotency.NewService(db)
	server := NewServer(Services{DB: db, Workers: workerService, Idempotency: idempotencyService}, slog.Default()).Handler()
	ctx := context.Background()
	reqCtx, err := requestctx.ResolveBootstrap(ctx, db, "corr_http_worker_policy_prior_recovery")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := workerService.InspectWorkerPolicy(ctx, workerKey)
	if err != nil {
		t.Fatal(err)
	}
	keyA := "http-policy-prior-a-" + instanceID
	inputA, err := workers.NormalizeSetWorkerPolicyInput(workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 30},
		ExpectedPolicyFingerprint: initial.PolicyFingerprint,
		Reason:                    "commit policy A",
		Confirm:                   true,
		IdempotencyKey:            keyA,
	})
	if err != nil {
		t.Fatal(err)
	}
	beginA, err := idempotencyService.Begin(ctx, idempotency.BeginInput{
		ActorID: reqCtx.ActorID, NodeID: reqCtx.OriginNodeID, Key: keyA,
		Operation: "worker.policy.set", Request: map[string]any{"worker_ref": workerKey, "input": inputA},
	})
	if err != nil || beginA.Decision != idempotency.DecisionNew {
		t.Fatalf("begin A decision=%q err=%v", beginA.Decision, err)
	}
	appliedA, err := workerService.SetWorkerPolicy(ctx, reqCtx, workerKey, inputA)
	if err != nil {
		t.Fatal(err)
	}

	inputB := workers.SetWorkerPolicyInput{
		Policy:                    workers.TickPolicy{Mode: workers.TickModeInterval, IntervalSeconds: 45},
		ExpectedPolicyFingerprint: appliedA.New.PolicyFingerprint,
		Reason:                    "commit later policy B",
		Confirm:                   true,
	}
	applyB := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, inputB)))
	applyB.Header.Set(idempotency.Header, "http-policy-prior-b-"+instanceID)
	applyBRec := httptest.NewRecorder()
	server.ServeHTTP(applyBRec, applyB)
	if applyBRec.Code != http.StatusOK {
		t.Fatalf("apply B status=%d body=%s", applyBRec.Code, applyBRec.Body.String())
	}
	var appliedB response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(applyBRec.Body.Bytes(), &appliedB); err != nil {
		t.Fatal(err)
	}

	retryA := httptest.NewRequest(http.MethodPost, "/v1/workers/"+workerKey+"/policy", bytes.NewReader(workerPolicyRequestBody(t, inputA)))
	retryA.Header.Set(idempotency.Header, keyA)
	retryARec := httptest.NewRecorder()
	server.ServeHTTP(retryARec, retryA)
	if retryARec.Code != http.StatusOK {
		t.Fatalf("retry A status=%d body=%s", retryARec.Code, retryARec.Body.String())
	}
	var recoveredA response.Envelope[workers.SetWorkerPolicyResult]
	if err := json.Unmarshal(retryARec.Body.Bytes(), &recoveredA); err != nil {
		t.Fatal(err)
	}
	if !recoveredA.Data.IdempotentReplay || recoveredA.Data.Old.PolicyFingerprint != initial.PolicyFingerprint || recoveredA.Data.New.PolicyFingerprint != appliedA.New.PolicyFingerprint {
		t.Fatalf("recovered A=%#v", recoveredA.Data)
	}
	current, err := workerService.InspectWorkerPolicy(ctx, workerKey)
	if err != nil {
		t.Fatal(err)
	}
	if current.PolicyFingerprint != appliedB.Data.New.PolicyFingerprint || current.Policy.IntervalSeconds != 45 {
		t.Fatalf("retry A changed current policy: current=%#v B=%#v", current, appliedB.Data.New)
	}
	var eventCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM events.events WHERE target_id=$1 AND event_type='worker.instance.updated'`, instanceID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 2 {
		t.Fatalf("policy update events=%d, want A and B only", eventCount)
	}
}

func workerPolicyRequestBody(t *testing.T, input workers.SetWorkerPolicyInput) []byte {
	t.Helper()
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func httpWorkerPolicyPostgresFixture(t *testing.T) (*sql.DB, string, string) {
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
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_http_worker_policy_test")
	if err != nil {
		t.Fatal(err)
	}
	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	kind := "http_policy_" + suffix
	workerKey := "main." + kind
	instanceID := ids.NewWorkerInstanceID()
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_kinds (worker_kind,display_name,runtime_owner,status,supported_localities,default_tick_policy_json) VALUES ($1,'HTTP policy test','loomd','active','{main_owned}'::text[],'{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}'::jsonb)`, kind); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workers.worker_instances (worker_instance_id,worker_key,worker_kind,display_name,owner_node_id,host_node_id,scope_id,locality,lifecycle_status,enabled,tick_policy_json) VALUES ($1,$2,$3,'HTTP policy test',$4,$4,$5,'main_owned','active',true,'{"schema_version":"worker_tick_policy.v0.2","mode":"manual"}'::jsonb)`, instanceID, workerKey, kind, req.OriginNodeID, req.ScopeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = db.ExecContext(cleanup, `DELETE FROM interface.idempotency_keys WHERE key LIKE $1`, "%"+instanceID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM events.events WHERE target_id=$1`, instanceID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM workers.worker_instances WHERE worker_instance_id=$1`, instanceID)
		_, _ = db.ExecContext(cleanup, `DELETE FROM workers.worker_kinds WHERE worker_kind=$1`, kind)
	})
	return db, workerKey, instanceID
}
