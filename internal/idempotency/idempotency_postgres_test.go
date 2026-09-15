package idempotency

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"loom.local/loom/internal/bootstrap"
	"loom.local/loom/internal/requestctx"
)

func TestCompletedSuccessDominatesConcurrentFailurePostgres(t *testing.T) {
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
	req, err := requestctx.ResolveBootstrap(ctx, db, "corr_idempotency_terminal_test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(db)
	key := fmt.Sprintf("terminal-success-%d", time.Now().UTC().UnixNano())
	request := map[string]any{"worker_ref": "main.test", "value": "same"}
	begin, err := service.Begin(ctx, BeginInput{
		ActorID: req.ActorID, NodeID: req.OriginNodeID, Key: key,
		Operation: "worker.policy.set", Request: request,
	})
	if err != nil || begin.Decision != DecisionNew {
		t.Fatalf("begin decision=%q err=%v", begin.Decision, err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM interface.idempotency_keys WHERE idempotency_id=$1`, begin.Record.IdempotencyID)
	})

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
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		started <- struct{}{}
		errs <- service.Complete(ctx, begin.Record.IdempotencyID, "worker_instance", "worker_instance_test", map[string]any{"outcome": "success"})
	}()
	go func() {
		defer wait.Done()
		started <- struct{}{}
		errs <- service.Fail(ctx, begin.Record.IdempotencyID, "transient.failure", map[string]any{"outcome": "failure"})
	}()
	<-started
	<-started
	if err := lockTx.Commit(); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	replay, err := service.Begin(ctx, BeginInput{
		ActorID: req.ActorID, NodeID: req.OriginNodeID, Key: key,
		Operation: "worker.policy.set", Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replay.Decision != DecisionReplayCompleted || replay.Record.Status != StatusCompleted || replay.Record.ErrorCode != "" {
		t.Fatalf("replay decision=%q record=%#v", replay.Decision, replay.Record)
	}
	var snapshot map[string]string
	if err := json.Unmarshal(replay.Record.ResponseSnapshot, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot["outcome"] != "success" {
		t.Fatalf("completed snapshot=%s", replay.Record.ResponseSnapshot)
	}

	if err := service.Fail(ctx, begin.Record.IdempotencyID, "late.failure", map[string]any{"outcome": "late failure"}); err != nil {
		t.Fatal(err)
	}
	afterLateFailure, err := service.Begin(ctx, BeginInput{
		ActorID: req.ActorID, NodeID: req.OriginNodeID, Key: key,
		Operation: "worker.policy.set", Request: request,
	})
	if err != nil || afterLateFailure.Decision != DecisionReplayCompleted || string(afterLateFailure.Record.ResponseSnapshot) != string(replay.Record.ResponseSnapshot) {
		t.Fatalf("late failure changed completed result: decision=%q snapshot=%s err=%v", afterLateFailure.Decision, afterLateFailure.Record.ResponseSnapshot, err)
	}
}
