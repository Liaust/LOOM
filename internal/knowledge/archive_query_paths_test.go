package knowledge

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestNotesCustodyQueriesUseStoredPlanPaths(t *testing.T) {
	for name, query := range map[string]string{
		"read_lag":     notesCustodyReadCaughtUpSQL("candidate"),
		"write_fence":  notesCustodyWriteAllowedSQL("candidate"),
		"project_stop": notesProjectArchiveStopSQL("root", "candidate", "'operation'"),
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(query, "plan_json") || !strings.Contains(query, "source_absolute_path") {
				t.Fatal("custody predicate must use generated paths, not unpack the full inventory")
			}
		})
	}
	if !strings.Contains(notesCustodyReadCaughtUpSQL("candidate"), "lag_op.destination_absolute_path") {
		t.Fatal("restore lag must use the destination path")
	}
}

func TestNotesArchiveGeneratedQueryPathsPostgres(t *testing.T) {
	f := notesArchiveTestFixture(t)
	if _, err := f.p.Workspace.ApplyArchive(t.Context(), f.plan, f.plan.PlanDigest); err != nil {
		t.Fatal(err)
	}
	// Exercise backfill of an existing complete operation, not only new inserts.
	dir := filepath.Join("..", "..", "migrations")
	if err := goose.DownToContext(t.Context(), f.s.store.db, dir, 79); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(t.Context(), f.s.store.db, dir); err != nil {
		t.Fatal(err)
	}
	var source, destination string
	var exact bool
	err := f.s.store.db.QueryRowContext(t.Context(), `SELECT source_absolute_path, destination_absolute_path,
	 source_absolute_path IS NOT DISTINCT FROM plan_json#>>'{source,path,absolute_path}'
	 AND destination_absolute_path IS NOT DISTINCT FROM plan_json#>>'{destination,path,absolute_path}'
	 FROM storage.workspace_archive_operations WHERE workspace_archive_operation_id=$1`, f.plan.OperationID).Scan(&source, &destination, &exact)
	if err != nil || !exact || source != f.plan.Source.Path.AbsolutePath || destination != f.plan.Destination.Path.AbsolutePath {
		t.Fatalf("generated paths: %q %q exact=%v err=%v", source, destination, exact, err)
	}
	if _, err := f.s.store.db.ExecContext(t.Context(), `UPDATE storage.workspace_archive_operations SET source_absolute_path='/substituted' WHERE workspace_archive_operation_id=$1`, f.plan.OperationID); err == nil {
		t.Fatal("generated source path accepted caller substitution")
	}
	if _, err := f.s.store.db.ExecContext(t.Context(), `UPDATE storage.workspace_archive_operations SET destination_absolute_path='/substituted' WHERE workspace_archive_operation_id=$1`, f.plan.OperationID); err == nil {
		t.Fatal("generated destination path accepted caller substitution")
	}
}
