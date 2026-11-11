package knowledge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNotesSourceConcurrentOwnerReportPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	db, service := f.db, f.service
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(ctx, `SELECT report.watched_root_id FROM watched_roots.roots report JOIN projects.project_watched_root_registrations r USING(watched_root_id) WHERE r.project_watched_root_registration_id=$1 FOR UPDATE OF report`, root.ProjectWatchedRootRegistrationID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- service.WithNotesWorkspaceRoot(ctx, root.NotesSourceRootID, root.NodeKey, func(SourceRoot, string) error { return nil })
	}()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%SELECT report.watched_root_id%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("admission did not wait for the owner report: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := writer.ExecContext(ctx, `SET LOCAL lock_timeout='250ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(ctx, `UPDATE projects.project_watched_root_registrations SET last_reported_at=now() WHERE project_watched_root_registration_id=$1`, root.ProjectWatchedRootRegistrationID); err != nil {
		t.Fatalf("registration/report lock inversion: %v", err)
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestPartialSourceScanKeepsExactAdmissionFencesPostgres(t *testing.T) {
	f := declarationEnrollmentDB(t)
	f.report(f.analysis.Report.WatchedRoots[0])
	f.correlate(t)
	root := f.reconcile(t, SourceRootStatusActive)
	var generation string
	if err := f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, root.NodeKey, func(_ SourceRoot, g string) error { generation = g; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := f.db.Exec(`UPDATE watched_roots.roots SET status='degraded',
 summary_json='{"summary":{"root_reachable":true},"scan_counts":{"errors":0,"budget_exhausted":1,"missing_deferred":0,"hash_deferred":12}}',
 metadata=metadata||'{"root_reachable":true}'::jsonb
 WHERE watched_root_id=(SELECT watched_root_id FROM projects.project_watched_root_registrations WHERE project_watched_root_registration_id=$1)`, root.ProjectWatchedRootRegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"", "ordinary.md"} {
		called := false
		callback := func(actual SourceRoot, g string) error {
			called = true
			if actual.SourceIntakeStage != "scanning" || g != generation {
				t.Fatalf("progress changed authority: %q %q", actual.SourceIntakeStage, g)
			}
			return nil
		}
		if relative == "" {
			err = f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, root.NodeKey, callback)
		} else {
			err = f.service.WithWritableNotesSource(t.Context(), root.NotesSourceRootID, root.NodeKey, relative, callback)
		}
		if err != nil || !called {
			t.Fatalf("partial scan rejected: %v", err)
		}
	}
	for _, relative := range []string{"../outside.md", ".private.md", "credentials/secret.md"} {
		called := false
		err := f.service.WithWritableNotesSource(t.Context(), root.NotesSourceRootID, root.NodeKey, relative, func(SourceRoot, string) error { called = true; return nil })
		if err == nil || called {
			t.Fatalf("partial scan bypassed path policy: %s %v", relative, err)
		}
	}
	input := f.input
	input.ActivationStatus = "disabled"
	f.upsert(t, input)
	if err := f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, root.NodeKey, func(SourceRoot, string) error { t.Fatal("withdrawn partial source admitted"); return nil }); err == nil {
		t.Fatal("withdrawal was ignored")
	}
}

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
	for _, directories := range [][]string{nil, {"empty"}, {"empty", "renamed"}} {
		called = false
		if err := f.service.WithWritableNotesDirectoryPaths(t.Context(), root.NotesSourceRootID, "main", directories, func(_ SourceRoot, generation string) error {
			called = true
			if generation != expectedGeneration {
				t.Fatal("directory admission changed source generation")
			}
			return nil
		}); err != nil || !called {
			t.Fatalf("unindexed directory admission %v: %v", directories, err)
		}
	}
	for _, directories := range [][]string{{"."}, {"../outside"}, {".private"}, {"private_no_index"}, {"credentials"}} {
		called = false
		if err := f.service.WithWritableNotesDirectoryPaths(t.Context(), root.NotesSourceRootID, "main", directories, func(SourceRoot, string) error { called = true; return nil }); err == nil || called {
			t.Fatalf("invalid directory admitted %v: %v", directories, err)
		}
	}
	if err := f.service.WithNotesWorkspaceRoot(t.Context(), root.NotesSourceRootID, "main", func(_ SourceRoot, generation string) error {
		if generation != expectedGeneration {
			t.Fatal("root and file admission generations differ")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Even directory root-only scope admission must not grant application-data
	// write permission. Restore the fixture metadata before the remaining checks.
	if _, err := f.db.Exec(`UPDATE knowledge.notes_source_roots SET metadata='{"registration_metadata":{"knowledge_source":{"application_data":{}}}}'::jsonb WHERE notes_source_root_id=$1`, root.NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	for _, directories := range [][]string{nil, {"empty"}} {
		called = false
		err := f.service.WithWritableNotesDirectoryPaths(t.Context(), root.NotesSourceRootID, "main", directories, func(SourceRoot, string) error { called = true; return nil })
		if err == nil || called || !strings.Contains(err.Error(), "application-data") {
			t.Fatalf("application-data directory write admitted: %v %v", directories, err)
		}
	}
	if _, err := f.db.Exec(`UPDATE knowledge.notes_source_roots SET metadata=$2 WHERE notes_source_root_id=$1`, root.NotesSourceRootID, root.Metadata); err != nil {
		t.Fatal(err)
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
