package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/storagearchive"
)

func notesEmptyWorkspace(t *testing.T, f *notesArchivePostgresFixture, slug string) storagearchive.WorkspaceArchivePlan {
	t.Helper()
	path := filepath.Join(f.p.Workspace.Roots.BoxRoot, "Topics", slug)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "unindexed.md"), []byte("not admitted into Notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := f.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{
		Kind: storagearchive.WorkspaceKindTopic, ObjectID: "topic_" + strings.ReplaceAll(slug, "-", "_"), Slug: slug, ActorID: f.actor, Reason: "disposable empty Notes membership"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.Workspace.ApplyArchive(t.Context(), plan, plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	return plan
}

func notesReceiptCount(t *testing.T, f *notesArchivePostgresFixture) int {
	t.Helper()
	var count int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_projection_receipts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNotesCustodyConsumerEmptyAndOrderingPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.s.store.db.SetMaxOpenConns(1)
	f.plan = notesEmptyWorkspace(t, f, "empty-topic")
	restore := f.restore(t)
	if _, err := f.p.ProjectOperation(t.Context(), restore.OperationID); notesCustodyFinding(err) != NotesCustodyArchivePending {
		t.Fatalf("empty restore bypassed archive predecessor: %v", err)
	}
	if notesReceiptCount(t, f) != 0 {
		t.Fatal("out-of-order restore published receipt")
	}
	for _, id := range []string{f.plan.OperationID, restore.OperationID, f.plan.OperationID, restore.OperationID} {
		result, err := f.p.ProjectOperation(t.Context(), id)
		if err != nil || result.Projected != 0 || result.Replayed != 0 {
			t.Fatalf("empty projection/replay: %+v %v", result, err)
		}
	}
	var linked int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_projection_receipts
	 WHERE object_count=0 AND previous_event_id IS NOT NULL`).Scan(&linked); err != nil || linked != 1 || notesReceiptCount(t, f) != 2 {
		t.Fatalf("empty receipt chain: %d %v", linked, err)
	}
	f.p.Workspace.ManifestKey = func(context.Context, string) ([]byte, error) {
		t.Fatal("completed receipt reloaded workspace manifest")
		return nil, errors.New("unreachable")
	}
	batch, err := f.p.ConsumeBatch(t.Context(), "", 1)
	if err != nil || len(batch.Items) != 0 || !batch.Wrapped {
		t.Fatalf("completed empty event reconsumed: %+v %v", batch, err)
	}
	if f.processingSnapshot(t) != f.before {
		t.Fatal("empty projection changed Notes processing")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.p.ConsumeBatch(canceled, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity lost: %v", err)
	}
	if _, err := f.p.ConsumeBatch(t.Context(), "invalid-cursor", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid cursor accepted: %v", err)
	}
	f.p.NodeKey = "not-the-local-main"
	if _, err := f.p.ConsumeBatch(t.Context(), "", 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong consumer node accepted: %v", err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong projector node accepted: %v", err)
	}
}

func TestNotesCustodyConsumerBoundedWrapPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	for i := range 3 {
		notesEmptyWorkspace(t, f, fmt.Sprintf("empty-topic-%d", i))
	}
	// One missing source observation refuses only its own atomic projection.
	if _, err := f.s.store.db.Exec(`UPDATE sync.replicas SET freshness_state='stale' WHERE replicated_id IN (
	 SELECT object_version_id FROM objects.object_versions WHERE source_path LIKE '%/topic-one/second.md')`); err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	cursor := ""
	for i := range 4 {
		batch, err := f.p.ConsumeBatch(t.Context(), cursor, 1)
		if err != nil || len(batch.Items) != 1 || batch.Wrapped != (i == 3) {
			t.Fatalf("bounded batch %d: %+v %v", i, batch, err)
		}
		item := batch.Items[0]
		seen[item.EventID]++
		if item.OperationID == f.plan.OperationID {
			if item.Finding != NotesCustodySourceConflict || item.Projected != 0 {
				t.Fatalf("untruthful conflict: %+v", item)
			}
		} else if item.Finding != "" {
			t.Fatalf("unrelated empty operation failed: %+v", item)
		}
		cursor = batch.NextCursor
	}
	if len(seen) != 4 || notesReceiptCount(t, f) != 3 || cursor != "" {
		t.Fatalf("starved or skipped work: %v cursor %q", seen, cursor)
	}
	for _, n := range seen {
		if n != 1 {
			t.Fatal("duplicate within bounded pass")
		}
	}
	if _, err := f.s.store.db.Exec(`UPDATE sync.replicas SET freshness_state='fresh' WHERE freshness_state='stale'`); err != nil {
		t.Fatal(err)
	}
	batch, err := f.p.ConsumeBatch(t.Context(), cursor, 1)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].Projected != 2 || batch.Items[0].Finding != "" || !batch.Wrapped {
		t.Fatalf("refused event lost on wrap: %+v %v", batch, err)
	}
	f.assertCustody(t, "archived", 2)
	if notesReceiptCount(t, f) != 4 {
		t.Fatal("missing successful receipts")
	}
}

func TestNotesCustodyConsumerLateCompletionPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	empty := notesEmptyWorkspace(t, f, "empty-topic")
	batch, err := f.p.ConsumeBatch(t.Context(), "", 1)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].OperationID != empty.OperationID {
		t.Fatalf("planned operation consumed: %+v %v", batch, err)
	}
	// Creating/archiving the sibling changed reviewed ancestor metadata.
	f.plan, err = f.p.Workspace.PlanArchive(t.Context(), storagearchive.WorkspaceArchivePlanInput{
		Kind: f.plan.Kind, ObjectID: f.plan.ObjectID, Slug: f.plan.Slug, ActorID: f.actor, Reason: f.plan.Reason})
	if err != nil {
		t.Fatal(err)
	}
	f.archive(t)
	// A restart with an older saved cursor cannot hide a late-completing event.
	batch, err = f.p.ConsumeBatch(t.Context(), "workspace_lifecycle_event_7ZZZZZZZZZZZZZZZZZZZZZZZZZ", 1)
	if err != nil || len(batch.Items) != 0 || !batch.Wrapped || batch.NextCursor != "" {
		t.Fatalf("cursor did not wrap: %+v %v", batch, err)
	}
	batch, err = f.p.ConsumeBatch(t.Context(), batch.NextCursor, 1)
	if err != nil || len(batch.Items) != 1 || batch.Items[0].OperationID != f.plan.OperationID || batch.Items[0].Projected != 2 {
		t.Fatalf("late completion skipped: %+v %v", batch, err)
	}
}

func TestNotesCustodyReceiptAtomicPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	if _, err := f.s.store.db.Exec(`CREATE FUNCTION knowledge.test_refuse_receipt() RETURNS trigger LANGUAGE plpgsql AS
	 $$ BEGIN RAISE EXCEPTION 'disposable receipt failure'; END $$;
	 CREATE TRIGGER test_refuse_receipt BEFORE INSERT ON knowledge.notes_custody_projection_receipts
	 FOR EACH ROW EXECUTE FUNCTION knowledge.test_refuse_receipt()`); err != nil {
		t.Fatal(err)
	}
	result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
	if err == nil || result.Projected != 0 || notesReceiptCount(t, f) != 0 {
		t.Fatalf("receipt failure committed: %+v %v", result, err)
	}
	var count int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipt failure left partial custody: %d %v", count, err)
	}
	if _, err := f.s.store.db.Exec(`DROP TRIGGER test_refuse_receipt ON knowledge.notes_custody_projection_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE knowledge.notes_custody_projection_receipts SET object_count=0`, `DELETE FROM knowledge.notes_custody_projection_receipts`} {
		if _, err := f.s.store.db.Exec(query); err == nil {
			t.Fatal("immutable receipt changed")
		}
	}
	f.assertCustody(t, "archived", 2)
}

func TestNotesCustodyReceiptConflictPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	evidence, err := f.p.Workspace.ReadLifecycleEvidence(t.Context(), f.plan.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.store.db.Exec(`INSERT INTO knowledge.notes_custody_projection_receipts
	 (node_id,workspace_lifecycle_event_id,manifest_digest,object_count,membership_digest)
	 VALUES ($1,$2,$3,2,$4)`, *f.root.NodeID, evidence.Event.EventID, evidence.ManifestDigest, hashArtifactValue("wrong membership")); err != nil {
		t.Fatal(err)
	}
	result, err := f.p.ProjectOperation(t.Context(), f.plan.OperationID)
	if notesCustodyFinding(err) != NotesCustodyReceiptConflict || result.Projected != 0 {
		t.Fatalf("conflicting receipt accepted: %+v %v", result, err)
	}
	var count int
	if err := f.s.store.db.QueryRow(`SELECT count(*) FROM knowledge.notes_custody_transitions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("receipt conflict mutated custody: %d %v", count, err)
	}
}

func TestNotesCustodyConsumerConcurrentPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	f.archive(t)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				batch, err := f.p.ConsumeBatch(t.Context(), "", 1)
				if err != nil {
					t.Error(err)
					return
				}
				if len(batch.Items) == 0 || batch.Items[0].Finding == "" {
					return
				}
				if batch.Items[0].Finding != NotesCustodyDatabaseRetryable {
					t.Errorf("unexpected concurrent finding: %+v", batch)
					return
				}
			}
			t.Error("concurrent consumer failed to converge")
		}()
	}
	wg.Wait()
	if notesReceiptCount(t, f) != 1 {
		t.Fatal("duplicate/missing concurrent receipt")
	}
	f.assertCustody(t, "archived", 2)
}

func TestNotesCustodyConsumerValidationAndRedaction(t *testing.T) {
	for _, err := range []error{errors.New("password=fixture-secret /private/fixture"),
		notesCustodyFailure(NotesCustodySourceConflict, errors.New("password=fixture-secret /private/fixture")),
		fmt.Errorf("wrapper: %w", &pgconn.PgError{Code: "40001", Message: "fixture-secret"})} {
		data, marshalErr := json.Marshal(NotesCustodyBatchItem{Finding: notesCustodyFinding(err)})
		if marshalErr != nil || strings.Contains(string(data), "fixture-secret") || strings.Contains(string(data), "/private") {
			t.Fatalf("finding leaks cause: %s %v", data, marshalErr)
		}
	}
	if notesCustodyFinding(fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"})) != NotesCustodyDatabaseRetryable {
		t.Fatal("retryable SQL state lost")
	}
	for _, limit := range []int{-1, 0, 51} {
		if _, err := (NotesArchiveProjector{}).ConsumeBatch(context.Background(), "", limit); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid batch accepted: %v", err)
		}
	}
}
