package knowledge

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"loom.local/loom/internal/notesprojection"
)

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
