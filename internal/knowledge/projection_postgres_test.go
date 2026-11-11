package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/notesprojection"
)

func TestProjectionTargetedSQLCostPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	args := append(projectionSourceArgs(), []string{objects[0].KnowledgeObjectID})
	query := `EXPLAIN (ANALYZE,FORMAT JSON) ` + projectionSourcesSQL("AND o.knowledge_object_id=ANY($9::text[])")
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		for _, jit := range []string{"on", "off"} {
			tx, err := s.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "SET LOCAL plan_cache_mode="+mode+"; SET LOCAL jit="+jit); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			var raw []byte
			if err := tx.QueryRowContext(t.Context(), query, args...).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var plan []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &plan); err != nil {
				t.Fatal(err)
			}
			t.Logf("mode=%s jit=%s total=%s planning=%s execution=%s JIT=%s", mode, jit, time.Since(started), plan[0]["Planning Time"], plan[0]["Execution Time"], plan[0]["JIT"])
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
	}
	query = projectionSourcesSQL("AND o.knowledge_object_id=ANY($9::text[])")
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		var samples []int64
		for range 4 {
			tx, err := s.store.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.ExecContext(t.Context(), "SET LOCAL plan_cache_mode="+mode); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			rows, err := tx.QueryContext(t.Context(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				if _, err := scanProjectionSource(rows); err != nil {
					t.Fatal(err)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				t.Fatal(err)
			}
			samples = append(samples, time.Since(started).Microseconds())
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s query microseconds=%v", mode, samples)
	}
}

func TestProjectionCachedPlanKeepsLiveAdmissionPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	objects := reconcileBoxSyncedFixture(t, s, roots)
	id := objects[0].KnowledgeObjectID
	for range 4 {
		if delta, err := s.ReadProjectionSources(t.Context(), []string{id}); err != nil || len(delta.Sources) != 1 {
			t.Fatalf("keyed source read: %+v %v", delta, err)
		}
	}
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE files.file_metadata SET index_policy='private_no_index'`); err != nil {
		t.Fatal(err)
	}
	if delta, err := s.ReadProjectionSources(t.Context(), []string{id}); !errors.Is(err, notesprojection.ErrDeltaUncertain) || len(delta.Sources) != 0 || len(delta.Withdrawn) != 0 {
		t.Fatalf("cached plan reused old admission: %+v %v", delta, err)
	}
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE files.file_metadata SET index_policy='text_later'`); err != nil {
		t.Fatal(err)
	}
	if delta, err := s.ReadProjectionSources(t.Context(), []string{id}); err != nil || len(delta.Sources) != 1 {
		t.Fatalf("restored evidence did not resume: %+v %v", delta, err)
	}
}

func TestProjectionTargetedWithdrawalEvidencePostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	before, err := s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(before) == 0 {
		t.Fatalf("inventory: %v", err)
	}
	id := before[0].KnowledgeObjectID
	delta, err := s.ReadProjectionSources(t.Context(), []string{id})
	if err != nil || len(delta.Sources) != 1 || !reflect.DeepEqual(delta.Sources[0], before[0]) {
		t.Fatalf("targeted differs: %+v %v", delta, err)
	}
	if _, err := s.ReadProjectionSources(t.Context(), []string{"missing"}); !errors.Is(err, notesprojection.ErrDeltaUncertain) {
		t.Fatalf("missing row authorized removal: %v", err)
	}
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE knowledge.knowledge_objects SET deleted_at=now() WHERE knowledge_object_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	delta, err = s.ReadProjectionSources(t.Context(), []string{id})
	if err != nil || len(delta.Sources) != 0 || delta.Withdrawn[id] != "source_withdrawn" {
		t.Fatalf("withdrawal: %+v %v", delta, err)
	}
}

func TestProjectionObservedMissingEvidencePostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	before, err := s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(before) == 0 {
		t.Fatalf("inventory: %v", err)
	}
	id := before[0].KnowledgeObjectID
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.store.db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var fileID string
	if err := s.store.db.QueryRowContext(t.Context(), `SELECT canonical_file_id FROM knowledge.knowledge_objects WHERE knowledge_object_id=$1`, id).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	reset := func() {
		exec(`INSERT INTO watched_roots.roots(watched_root_id,node_id,root_key,config_hash,metadata)
	 SELECT 'watched_root_projection_missing',r.node_id,r.backend_root_key,'current','{"root_reachable":true}'
	 FROM knowledge.notes_source_roots r JOIN knowledge.knowledge_objects o USING(notes_source_root_id)
	 WHERE o.knowledge_object_id=$1
	 ON CONFLICT(node_id,root_key) DO UPDATE SET config_hash='current',metadata='{"root_reachable":true}'`, id)
		exec(`UPDATE files.file_metadata f SET source_root_key=r.backend_root_key,
	 source_relative_path=o.relative_path,managed_source_path=o.source_path,
	 source_generation='current',source_sequence=1,source_observed_at=now(),source_presence='missing'
	 FROM knowledge.knowledge_objects o JOIN knowledge.notes_source_roots r USING(notes_source_root_id)
	 WHERE o.knowledge_object_id=$1 AND f.object_id=$2`, id, fileID)
	}
	reset()
	delta, err := s.ReadProjectionSources(t.Context(), []string{id})
	if err != nil || len(delta.Sources) != 0 || delta.Withdrawn[id] != "source_missing" {
		t.Fatalf("authenticated missing observation not recognized: %+v %v", delta, err)
	}
	for _, tc := range []struct{ name, query string }{
		{"unavailable", `UPDATE files.file_metadata SET source_presence='unavailable' WHERE object_id=$1`},
		{"stale_generation", `UPDATE files.file_metadata SET source_generation='old' WHERE object_id=$1`},
		{"no_producer_sequence", `UPDATE files.file_metadata SET source_sequence=0 WHERE object_id=$1`},
		{"no_observation_time", `UPDATE files.file_metadata SET source_observed_at=NULL WHERE object_id=$1`},
		{"wrong_root", `UPDATE files.file_metadata SET source_root_key='other' WHERE object_id=$1`},
		{"wrong_relative_path", `UPDATE files.file_metadata SET source_relative_path='other.md' WHERE object_id=$1`},
		{"wrong_managed_path", `UPDATE files.file_metadata SET managed_source_path='/other/source.md' WHERE object_id=$1`},
		{"unreachable_root", `UPDATE watched_roots.roots SET metadata='{"root_reachable":false}' WHERE watched_root_id='watched_root_projection_missing' AND $1::text<>''`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reset()
			exec(tc.query, fileID)
			if delta, err := s.ReadProjectionSources(t.Context(), []string{id}); !errors.Is(err, notesprojection.ErrDeltaUncertain) || delta.Withdrawn[id] != "" {
				t.Fatalf("uncertain observation became withdrawal: %+v %v", delta, err)
			}
		})
	}
	reset()
	// Withdrawal describes only generated-view membership; retained files and
	// content versions are not mutated by source selection.
	var retained bool
	if err := s.store.db.QueryRowContext(t.Context(), `SELECT o.deleted_at IS NULL AND f.latest_version_id IS NOT NULL
	 FROM knowledge.knowledge_objects o JOIN files.file_metadata f ON f.object_id=o.canonical_file_id
	 WHERE o.knowledge_object_id=$1`, id).Scan(&retained); err != nil || !retained {
		t.Fatalf("retained source/version altered: %t %v", retained, err)
	}
}

func TestProjectionCurrentDeclarationExclusionPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	before, err := s.ListProjectionSources(t.Context(), notesprojection.SourceListInput{})
	if err != nil || len(before) == 0 {
		t.Fatalf("inventory: %v", err)
	}
	id := before[0].KnowledgeObjectID
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,
	 '{registration_metadata,knowledge_source,exclude}','["source.md"]') WHERE notes_source_root_id=$1`, before[0].NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	delta, err := s.ReadProjectionSources(t.Context(), []string{id})
	if err != nil || len(delta.Sources) != 0 || delta.Withdrawn[id] != "source_excluded" {
		t.Fatalf("current exclusion not recognized: %+v %v", delta, err)
	}
	if _, err := s.store.db.ExecContext(t.Context(), `UPDATE knowledge.notes_source_roots SET metadata=jsonb_set(metadata,
	 '{registration_metadata,knowledge_source,exclude}','[17]') WHERE notes_source_root_id=$1`, before[0].NotesSourceRootID); err != nil {
		t.Fatal(err)
	}
	if delta, err := s.ReadProjectionSources(t.Context(), []string{id}); !errors.Is(err, notesprojection.ErrDeltaUncertain) || delta.Withdrawn[id] != "" {
		t.Fatalf("malformed declaration authorized withdrawal: %+v %v", delta, err)
	}
}

func TestNotesRecoveryJournalVisibilityPostgres(t *testing.T) {
	s, _ := boxSyncedFixture(t)
	journal := ".loom-notes-" + strings.Repeat("a", 64)
	for _, p := range []string{journal, journal + "/original", "nested/" + journal + "/displaced", `nested\` + journal + `\intent`, ".loom-notes-guide.md", "notes.md"} {
		var visible bool
		if err := s.store.db.QueryRowContext(t.Context(), "SELECT "+visibleNotesKnowledgeRelativePathSQL("$1::text"), p).Scan(&visible); err != nil {
			t.Fatal(err)
		}
		if visible == ignoredNotesKnowledgeRelativePath(p) {
			t.Fatalf("Go/SQL journal visibility differs for %q", p)
		}
	}
}

// Reuse the existing local disposable-DB fixture. No runtime Main access.
func TestProjectionCursorSnapshotPostgres(t *testing.T) {
	s, roots := boxSyncedFixture(t)
	reconcileBoxSyncedFixture(t, s, roots)
	ctx, db := t.Context(), s.store.db
	before, err := s.ListProjectionSources(ctx, notesprojection.SourceListInput{})
	if err != nil || len(before) != 3 {
		t.Fatalf("initial inventory: %d %v", len(before), err)
	}
	if limited, err := s.ListProjectionSources(ctx, notesprojection.SourceListInput{Limit: 2}); err == nil || limited != nil {
		t.Fatalf("silent explicit truncation: %d %v", len(limited), err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := declareProjectionSources(ctx, tx); err != nil {
		t.Fatal(err)
	}
	// Fetch one row to leave the other fixture sources on a later page.
	rows, err := tx.QueryContext(ctx, "FETCH FORWARD 1 FROM notes_projection_sources")
	if err != nil {
		t.Fatal(err)
	}
	if !rows.Next() {
		rows.Close()
		t.Fatal("empty first page")
	}
	first, err := scanProjectionSource(rows)
	if closeErr := rows.Close(); err != nil || closeErr != nil {
		t.Fatalf("first page: %v %v", err, closeErr)
	}
	// Commit ordering and membership changes on another connection. Neither may
	// remove as-yet-unfetched rows from this inventory or change their paths.
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.knowledge_objects SET relative_path='renamed.md' WHERE knowledge_object_id=$1`, before[2].KnowledgeObjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE knowledge.notes_source_roots SET status='disabled'`); err != nil {
		t.Fatal(err)
	}
	rest, err := collectProjectionSources(ctx, 0, func(ctx context.Context) ([]notesprojection.SourceObject, error) {
		return fetchProjectionSources(ctx, tx)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := append([]notesprojection.SourceObject{first}, rest...); !reflect.DeepEqual(got, before) {
		t.Fatalf("cursor inventory changed across fetches: got %+v, want %+v", got, before)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListProjectionSources(ctx, notesprojection.SourceListInput{})
	if err != nil || len(after) != 0 {
		t.Fatalf("next inventory did not observe withdrawal: %d %v", len(after), err)
	}
}
