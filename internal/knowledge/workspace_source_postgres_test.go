package knowledge

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWritableNotesSourceBeforeIndexingAndWithdrawalPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM knowledge.knowledge_objects`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fixture unexpectedly indexed: %d %v", count, err)
	}
	called := false
	var expectedGeneration string
	invoke := func(node, relative string) error {
		return f.service.WithWritableNotesSource(t.Context(), root.NotesSourceRootID, node, relative, func(actual SourceRoot, generation string) error {
			called = true
			expectedGeneration = generation
			if actual.SourcePath != root.SourcePath || generation == "" {
				t.Fatal("unbound source")
			}
			return nil
		})
	}
	if err := invoke("main", "ordinary.md"); err != nil || !called {
		t.Fatalf("unindexed write source: %v", err)
	}
	// Both paths share one transaction/connection and the same source generation.
	f.db.SetMaxOpenConns(1)
	pathsCtx, cancelPaths := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelPaths()
	called = false
	if err := f.service.WithWritableNotesSourcePaths(pathsCtx, root.NotesSourceRootID, "main", []string{"ordinary.md", "renamed.md"}, func(_ SourceRoot, generation string) error {
		called = true
		if generation != expectedGeneration {
			t.Fatal("rename changed source generation")
		}
		return nil
	}); err != nil || !called {
		t.Fatalf("two-path admission: %v", err)
	}
	for _, destination := range []string{".private.md", "reference.pdf", "../outside.md"} {
		called = false
		err := f.service.WithWritableNotesSourcePaths(pathsCtx, root.NotesSourceRootID, "main", []string{"ordinary.md", destination}, func(SourceRoot, string) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("denied rename destination accepted: %s %v", destination, err)
		}
	}
	for _, input := range []struct{ node, path string }{{"other-node", "ordinary.md"}, {"main", "../outside.md"}, {"main", ".private.md"}, {"main", "reference.pdf"}} {
		called = false
		if err := invoke(input.node, input.path); err == nil || called {
			t.Fatalf("invalid source accepted: %+v %v", input, err)
		}
	}
	if _, err := f.db.Exec(`UPDATE projects.projects SET status='archived' WHERE project_id=$1`, root.ProjectID); err != nil {
		t.Fatal(err)
	}
	called = false
	if err := invoke("main", "ordinary.md"); err == nil || called {
		t.Fatal("archived project accepted")
	}
	if _, err := f.db.Exec(`UPDATE projects.projects SET status='active' WHERE project_id=$1`, root.ProjectID); err != nil {
		t.Fatal(err)
	}
	// Root reconciliation deliberately lags this withdrawal. Current writer wins.
	input := f.input
	input.ActivationStatus = "disabled"
	f.upsert(t, input)
	called = false
	if err := invoke("main", "ordinary.md"); err == nil || called {
		t.Fatal("withdrawn registration accepted")
	}
}

func TestWritableNotesSourcePoolWaitCancellationPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	f.db.SetMaxOpenConns(1)
	blocker, err := f.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	before := f.db.Stats().WaitCount
	result := make(chan error, 1)
	called := false
	go func() {
		result <- f.service.WithWritableNotesSource(ctx, root.NotesSourceRootID, "main", "ordinary.md", func(SourceRoot, string) error {
			called = true
			return nil
		})
	}()
	// Observe a real pool wait before cancellation, not just an already-canceled call.
	wait, stop := context.WithTimeout(t.Context(), 2*time.Second)
	defer stop()
	for f.db.Stats().WaitCount == before {
		select {
		case err := <-result:
			t.Fatalf("returned before pool wait: %v", err)
		case <-wait.Done():
			t.Fatal("source did not wait for pooled connection")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || called {
			t.Fatalf("canceled admission: called=%v err=%v", called, err)
		}
	case <-wait.Done():
		t.Fatal("canceled source admission remained blocked on pool")
	}
	// Release only after cancellation returned. Reuse a one-connection pool to
	// check that lookup and transaction share it and all exit paths release it.
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}
	admission, finish := context.WithTimeout(t.Context(), 2*time.Second)
	defer finish()
	failure := errors.New("callback rejected")
	for _, callbackErr := range []error{failure, nil} {
		called = false
		err := f.service.WithWritableNotesSource(admission, root.NotesSourceRootID, "main", "ordinary.md", func(SourceRoot, string) error {
			called = true
			return callbackErr
		})
		if !errors.Is(err, callbackErr) || !called {
			t.Fatalf("admission: called=%v err=%v", called, err)
		}
		if inUse := f.db.Stats().InUse; inUse != 0 {
			t.Fatalf("connection retained after callback: %d", inUse)
		}
	}
}
