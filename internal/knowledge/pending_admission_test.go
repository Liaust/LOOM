package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/notesprojection"
	"loom.local/loom/internal/workers"
)

func TestPendingAdmissionDoesNotWaitForPeriodicRootsPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	ctx := t.Context()
	// The synced fixture has manually prepared roots; add a real unrelated
	// registration so DiscoverFromStore actually attempts the locked root.
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO box.watch_root_registrations
	 (box_watch_root_registration_id,box_id,box_root_path,box_contract_path,node_id,owner_node_key,
	 area_key,local_root_key,backend_root_key,root_relative_path,activation_status,source_kinds_json,metadata)
	 VALUES($1,$6,$2,$3,$4,'sync-owner','library','library','loom_box__library','library','reported','["box_library"]',$5::jsonb->'registration_metadata')`,
		ids.NewBoxWatchRootRegistrationID(), filepath.Dir(roots[2].SourcePath), filepath.Join(roots[2].SourcePath, ".loom", "box.yaml"), roots[2].NodeID, roots[2].Metadata, ids.NewBoxID()); err != nil {
		t.Fatal(err)
	}
	revision, err := s.NotesAdmissionRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(revision), &parts); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(parts[3:])
	expired := time.Now().Add(-time.Hour)
	cursor := AdmissionCursor{RootConfigRevision: string(config), NextRootReconcileAt: expired}
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture: %+v %v", entries, err)
	}
	enqueue := func() {
		t.Helper()
		if _, err := s.store.db.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1)`, entries[0].ObjectVersionID); err != nil {
			t.Fatal(err)
		}
	}
	enqueue()
	locked, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback()
	if _, err := locked.ExecContext(ctx, `SELECT notes_source_root_id FROM knowledge.notes_source_roots WHERE notes_source_root_id=$1 FOR UPDATE`, roots[2].NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	// Maintenance waits on an unrelated root. Admission to the target root must
	// proceed without waiting for that global repair (or its unrelated row lock).
	wait, cancel := context.WithTimeout(ctx, 80*time.Millisecond)
	_, err = s.ReconcileSourceRoots(wait, SourceRootReconcileInput{DiscoverFromStore: true})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fixture did not block root repair: %v", err)
	}
	wait, cancel = context.WithTimeout(ctx, 2*time.Second)
	result, err := s.AdmitRegisteredNotesInteractiveOnce(wait, cursor, 8)
	cancel()
	if err != nil || result.PriorityApplied != 1 || result.PreferredPipelineRunID == "" || !result.Cursor.NextRootReconcileAt.Equal(expired) {
		t.Fatalf("expired repair blocked fresh admission or was falsely acknowledged: %+v %v", result, err)
	}
	// Changed authority is deliberately different from periodic maintenance.
	// It must reconcile before consuming the new delivery.
	enqueue()
	cursor.RootConfigRevision = "changed"
	wait, cancel = context.WithTimeout(ctx, 80*time.Millisecond)
	_, err = s.AdmitRegisteredNotesInteractiveOnce(wait, cursor, 8)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("configuration-change reconciliation was bypassed: %v", err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at == nil {
		t.Fatalf("blocked changed authority acknowledged a delivery: %v %v", at, err)
	}
}

func TestSourceRootChangesWakeAdmissionAndWorkspaceButUnchangedRepairDoesNotPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	ctx := t.Context()
	listener, err := s.store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := listener.ExecContext(ctx, "LISTEN loom_worker_wake"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.UpsertSourceRoot(ctx, roots[0]); err != nil {
		t.Fatal(err)
	}
	if err := listener.Raw(func(driver any) error {
		wait, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
		defer cancel()
		if notice, err := driver.(*stdlib.Conn).Conn().WaitForNotification(wait); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unchanged root woke work: %+v %v", notice, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A metadata-only authority refresh has no filesystem event to wake Notes.
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(roots[0].Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["audit_revision"] = json.RawMessage(`"refreshed"`)
	roots[0].Metadata = mustJSON(t, metadata)
	if _, err := s.store.UpsertSourceRoot(ctx, roots[0]); err != nil {
		t.Fatal(err)
	}
	if err := listener.Raw(func(driver any) error {
		wait, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		wanted := map[string]bool{workers.KindKnowledgeIndexer: true, "notes_workspace_sync": true}
		for len(wanted) > 0 {
			notice, err := driver.(*stdlib.Conn).Conn().WaitForNotification(wait)
			if err != nil {
				return err
			}
			if wanted[notice.Payload] {
				delete(wanted, notice.Payload)
				continue
			}
			if notice.Payload != notesprojection.WorkerKind {
				t.Fatalf("root change woke an unrelated owner: %+v", notice)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.UpsertSourceRoot(ctx, roots[0]); err != nil {
		t.Fatal(err)
	}
	if err := listener.Raw(func(driver any) error {
		wait, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
		defer cancel()
		if notice, err := driver.(*stdlib.Conn).Conn().WaitForNotification(wait); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unchanged refreshed root woke work: %+v %v", notice, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPendingSourceAdmissionPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	ctx := t.Context()
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[0].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture: %+v %v", entries, err)
	}
	version := entries[0].ObjectVersionID
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1)`, version); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at != nil {
		t.Fatalf("rollback escaped: %v %v", at, err)
	}
	for range 2 {
		if _, err := s.store.db.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1) ON CONFLICT DO NOTHING`, version); err != nil {
			t.Fatal(err)
		}
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at == nil || at.After(time.Now()) {
		t.Fatalf("lost durable readiness: %v %v", at, err)
	}
	result := AdmissionResult{ElapsedMS: map[string]int64{}}
	if observed, err := s.admitPendingSources(ctx, roots, 8, &result); err != nil || !observed || result.Applied != 1 || result.PreferredPipelineRunID == "" {
		t.Fatalf("exact admission: %+v %v", result, err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at != nil {
		t.Fatalf("not acknowledged: %v %v", at, err)
	}
	// Re-enqueue after the object/pipeline committed, as if the process died
	// before acknowledging. It must not create a second run or lose work.
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1)`, version); err != nil {
		t.Fatal(err)
	}
	replay := AdmissionResult{ElapsedMS: map[string]int64{}}
	if _, err := s.admitPendingSources(ctx, roots, 8, &replay); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := s.store.db.QueryRowContext(ctx, `SELECT count(*) FROM knowledge.pipeline_runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("replay duplicated pipeline: %d %v", runs, err)
	}
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1)`, version); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := s.store.db.QueryRowContext(ctx, `SELECT revision FROM knowledge.pending_source_admissions WHERE object_version_id=$1`, version).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.ExecContext(ctx, `UPDATE knowledge.pending_source_admissions SET revision=nextval('knowledge.source_admission_revision') WHERE object_version_id=$1`, version); err != nil {
		t.Fatal(err)
	}
	if err := s.acknowledgePendingSources(ctx, []string{version}, []int64{seen}, false); err != nil {
		t.Fatal(err)
	}
	if at, err := s.PendingSourceAdmissionAt(ctx); err != nil || at == nil {
		t.Fatalf("newer same-version metadata lost: %v %v", at, err)
	}
}

func TestPendingAdmissionPreservesChangedRootDiscoveryPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	ctx := t.Context()
	generation, err := admissionSourceGeneration(roots)
	if err != nil {
		t.Fatal(err)
	}
	cursor, _, err := prepareAdmissionScopes(roots, AdmissionCursor{Incremental: true, BaselineGeneration: generation}, generation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.AdmitRegisteredNotesInteractiveOnce(ctx, cursor, 8)
	if err != nil {
		t.Fatal(err)
	}
	cursor = first.Cursor
	// Existing content in this root must be revisited even when a different
	// root's fresh delivery takes the early-return path in this same turn.
	roots[0].Metadata = mustJSON(t, map[string]any{"box_id": "sync-fixture", "registration_metadata": map[string]any{"knowledge_source": map[string]any{"include": []string{"**/*.md"}, "exclude": []string{"new-exclusion/**"}}}})
	if _, err := s.store.UpsertSourceRoot(ctx, roots[0]); err != nil {
		t.Fatal(err)
	}
	entries, err := s.store.ListSyncedObjectEntriesForNotesRoots(ctx, roots[1].NotesSourceRootID)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture: %+v %v", entries, err)
	}
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO knowledge.pending_source_admissions(object_version_id) VALUES($1)`, entries[0].ObjectVersionID); err != nil {
		t.Fatal(err)
	}
	deferred, err := s.AdmitRegisteredNotesInteractiveOnce(ctx, cursor, 8)
	if err != nil || !deferred.InventoryDeferred {
		t.Fatalf("pending admission: %+v %v", deferred, err)
	}
	state := deferred.Cursor.RootCursors[roots[0].NotesSourceRootID]
	if state.StorageDone || state.SyncedDone {
		t.Fatal("configuration-change discovery lost across fast return")
	}
	if !deferred.Cursor.RootCursors[roots[2].NotesSourceRootID].StorageDone {
		t.Fatal("unrelated completed root reset")
	}
}
