package notesworkspacesync

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnrollmentScanRetainsCompletionAcrossIdleTurns(t *testing.T) {
	now := time.Now()
	cursor := RuntimeCursor{}
	scopes := []Scope{{Collection: "c", Generation: "g", Root: "Notes"}}
	if !enrollmentScanDue(&cursor, scopes, now) {
		t.Fatal("initial coverage omitted")
	}
	cursor.EnrollmentAfter = "page-one"
	if !enrollmentScanDue(&cursor, scopes, now) || cursor.EnrollmentAfter != "page-one" {
		t.Fatal("continuation reset")
	}
	cursor.EnrollmentAfter, cursor.EnrollmentComplete = "", true
	cursor.EnrollmentRetryAt = now.Add(15 * time.Minute)
	if enrollmentScanDue(&cursor, scopes, now.Add(time.Minute)) {
		t.Fatal("completed scan restarted on idle retry")
	}
	if !enrollmentScanDue(&cursor, scopes, now.Add(16*time.Minute)) {
		t.Fatal("sparse repair lost")
	}
	cursor.EnrollmentComplete, cursor.EnrollmentRetryAt = true, now.Add(time.Hour)
	scopes[0].Generation = "new"
	if !enrollmentScanDue(&cursor, scopes, now) || cursor.EnrollmentComplete {
		t.Fatal("changed source did not invalidate coverage")
	}
}

func TestRetainedReconciliationIgnoresSourceHintsButKeepsRecoveryGates(t *testing.T) {
	now := time.Now()
	r := &Runtime{}
	if !r.retainedReconciliationDue(true, "", now) {
		t.Fatal("missing audit coverage must reconcile retained state")
	}
	r.nextRetained = now.Add(24 * time.Hour)
	if r.retainedReconciliationDue(true, "", now) {
		t.Fatal("native restart ignored durable audit coverage")
	}
	r.session = &nativeSession{}
	r.sourceRevision.Add(1)
	if r.retainedReconciliationDue(true, "", now) {
		t.Fatal("a source hint restarted the whole retained journal")
	}
	if !r.retainedReconciliationDue(true, "continuation", now) ||
		!r.retainedReconciliationDue(true, "", now.Add(24*time.Hour)) ||
		!r.retainedReconciliationDue(false, "", now) {
		t.Fatal("continuation, periodic repair or one-shot reconciliation was lost")
	}
}

func TestNativeSessionRetainsLifetimeAndQuiescesForRecovery(t *testing.T) {
	root := t.TempDir()
	replica := filepath.Join(root, "replica")
	if err := os.Mkdir(replica, 0700); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "native")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nprintf . >&3\nwhile IFS= read -r line; do printf '%s\\n' '{\"id\":1,\"result\":{\"status\":\"ready\"}}'; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var hints atomic.Int64
	r := &Runtime{Config: RuntimeConfig{Command: command, ReplicaDir: replica, Settings: filepath.Join(root, "settings")}, Lifetime: ctx, Wake: func() { hints.Add(1) }}
	if err := r.WatchRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	lock, acquired, err := lockReplica(replica)
	if err != nil || !acquired {
		t.Fatal(err)
	}
	session, err := startNativeSession(ctx, r.Config, r.nativeHint)
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	session.lock = lock
	r.session = session
	defer r.CloseSourceWatches()
	var status NativeStatus
	workerCtx, stopWorker := context.WithCancel(ctx)
	if err := session.client.Call(workerCtx, map[string]any{"method": "status"}, &status); err != nil || status.Status != "ready" {
		t.Fatal(status, err)
	}
	stopWorker()
	select {
	case <-session.done:
		t.Fatal("worker completion stopped daemon child")
	default:
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !check() {
			if time.Now().After(deadline) {
				t.Fatal("lifecycle event not delivered")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool { return hints.Load() > 0 })
	if another, acquired, err := lockReplica(replica); err != nil || acquired {
		if another != nil {
			another.Close()
		}
		t.Fatal("live replica was not fenced", err)
	}
	request := filepath.Join(root, "recovery.request")
	if err := os.WriteFile(request, nil, 0600); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.session == nil })
	capture, acquired, err := lockReplica(replica)
	if err != nil || !acquired {
		t.Fatal("capture still blocked after native stop", err)
	}
	if !recoveryRequested(replica) {
		t.Fatal("pause signal missing")
	}
	capture.Close()
	before := hints.Load()
	if err := os.Remove(request); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return hints.Load() > before })
}
