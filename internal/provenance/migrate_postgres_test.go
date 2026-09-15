package provenance

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestMigrationsReplayAtExactHeadAndDoNotTouchMainDatabasePostgres(t *testing.T) {
	ctx := context.Background()
	mainPool, mainName := newDisposablePostgres(t, "main_guard")
	provenancePool, provenanceName := newDisposablePostgres(t, "provenance")

	status, err := ApplyMigrations(ctx, provenancePool, provenanceName)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.AppliedHead != SchemaHead || len(status.PendingVersions) != 0 {
		t.Fatalf("first migration status = %#v", status)
	}
	replayed, err := ApplyMigrations(ctx, provenancePool, provenanceName)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Ready || len(replayed.AppliedVersions) != SchemaHead {
		t.Fatalf("replayed migration status = %#v", replayed)
	}
	inspected, err := InspectSchema(ctx, provenancePool, provenanceName)
	if err != nil || !inspected.Ready {
		t.Fatalf("schema inspection = %#v, err=%v", inspected, err)
	}

	if _, err := ApplyMigrations(ctx, mainPool, provenanceName); !errors.Is(err, ErrWrongDatabase) {
		t.Fatalf("wrong-database migration error = %v, want %v", err, ErrWrongDatabase)
	}
	var mainHasProvenance bool
	if err := mainPool.QueryRow(ctx, `SELECT to_regnamespace('provenance') IS NOT NULL`).Scan(&mainHasProvenance); err != nil {
		t.Fatal(err)
	}
	if mainHasProvenance {
		t.Fatalf("provenance schema entered disposable loom_main surrogate %q", mainName)
	}

	rows, err := provenancePool.Query(ctx, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'provenance'
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	wantTables := []string{
		"candidate_evidence_links", "candidate_events", "candidate_lineage", "candidates",
		"case_events", "case_members", "evidence_registrations", "operation_history",
		"processing_runs", "project_projection_snapshots", "record_events", "record_producers", "record_sources", "records",
		"registration_replays", "relationship_events", "relationships", "resolution_cases",
		"repository_projection_snapshots", "schema_migrations", "source_references",
	}
	sort.Strings(wantTables)
	if strings.Join(tables, ",") != strings.Join(wantTables, ",") {
		t.Fatalf("provenance tables = %v, want %v", tables, wantTables)
	}
	for _, table := range tables {
		lower := strings.ToLower(table)
		if strings.Contains(lower, "archivist") || strings.Contains(lower, "clarification") ||
			strings.Contains(lower, "fts") {
			t.Fatalf("deferred table was ported: %s", table)
		}
	}
}

func TestSchemaInspectionFailsClosedForTamperedBehindAndAheadHistoryPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("tampered", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "tampered")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE provenance.schema_migrations SET sha256 = repeat('0', 64) WHERE version = 3`); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectSchema(ctx, pool, name); !errors.Is(err, ErrHistoryMismatch) {
			t.Fatalf("tampered history error = %v", err)
		}
	})
	t.Run("missing_table", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "missing_table")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DROP TABLE provenance.registration_replays`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("missing-table status = %#v, err=%v", status, err)
		}
	})
	t.Run("missing_append_trigger", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "missing_trigger")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_ledger_mutation ON provenance.records`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("missing-trigger status = %#v, err=%v", status, err)
		}
	})
	t.Run("disabled_append_trigger", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "disabled_trigger")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `ALTER TABLE provenance.records DISABLE TRIGGER reject_ledger_mutation`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("disabled-trigger status = %#v, err=%v", status, err)
		}
	})
	t.Run("same_name_weakened_event_trigger", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "weakened_trigger")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			DROP TRIGGER reject_ledger_mutation ON provenance.records;
			CREATE TRIGGER reject_ledger_mutation
			BEFORE DELETE ON provenance.records
			FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation()
		`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("weakened-trigger status = %#v, err=%v", status, err)
		}
	})
	t.Run("column_scoped_update_trigger", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "column_trigger")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			DROP TRIGGER reject_ledger_mutation ON provenance.records;
			CREATE TRIGGER reject_ledger_mutation
			BEFORE UPDATE OF claim OR DELETE ON provenance.records
			FOR EACH ROW EXECUTE FUNCTION provenance.reject_ledger_mutation()
		`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("column-scoped-trigger status = %#v, err=%v", status, err)
		}
	})
	t.Run("repointed_append_trigger", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "repointed_trigger")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			CREATE FUNCTION provenance.allow_ledger_mutation()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    IF TG_OP = 'DELETE' THEN
			        RETURN OLD;
			    END IF;
			    RETURN NEW;
			END;
			$$;
			DROP TRIGGER reject_ledger_mutation ON provenance.records;
			CREATE TRIGGER reject_ledger_mutation
			BEFORE UPDATE OR DELETE ON provenance.records
			FOR EACH ROW EXECUTE FUNCTION provenance.allow_ledger_mutation()
		`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("repointed-trigger status = %#v, err=%v", status, err)
		}
	})
	t.Run("weakened_reject_function", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "weakened_function")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			CREATE OR REPLACE FUNCTION provenance.reject_ledger_mutation()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
			    IF TG_OP = 'DELETE' THEN
			        RETURN OLD;
			    END IF;
			    RETURN NEW;
			END;
			$$
		`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaTampered) || status.Ready {
			t.Fatalf("weakened-function status = %#v, err=%v", status, err)
		}
	})
	t.Run("behind", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "behind")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM provenance.schema_migrations WHERE version = 7`); err != nil {
			t.Fatal(err)
		}
		status, err := InspectSchema(ctx, pool, name)
		if !errors.Is(err, ErrSchemaBehind) || status.AppliedHead != 6 {
			t.Fatalf("behind status = %#v, err=%v", status, err)
		}
	})
	t.Run("ahead", func(t *testing.T) {
		pool, name := newDisposablePostgres(t, "ahead")
		if _, err := ApplyMigrations(ctx, pool, name); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO provenance.schema_migrations(version, name, sha256, applied_at)
			VALUES (8, '008_deferred_upstream_migration.sql', repeat('a', 64), now())
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := InspectSchema(ctx, pool, name); !errors.Is(err, ErrSchemaAhead) {
			t.Fatalf("ahead history error = %v", err)
		}
	})
}

func TestConcurrentMigrationRunnersConvergeOnOneHistoryPostgres(t *testing.T) {
	ctx := context.Background()
	pool, name := newDisposablePostgres(t, "migration_race")
	const workers = 8
	var wait sync.WaitGroup
	errorsFound := make(chan error, workers)
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			_, err := ApplyMigrations(ctx, pool, name)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	status, err := InspectSchema(ctx, pool, name)
	if err != nil || !status.Ready || len(status.AppliedVersions) != SchemaHead {
		t.Fatalf("concurrent migration status = %#v, err=%v", status, err)
	}
}

func TestHistoricalSchemaHeadSixInspectionThenDisposableMigrationToCurrentPostgres(t *testing.T) {
	ctx := context.Background()
	pool, database := newDisposablePostgres(t, "historical_head_6")
	if _, err := ApplyMigrations(ctx, pool, database); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		DROP TABLE provenance.repository_projection_snapshots;
		DROP TABLE provenance.project_projection_snapshots;
		DELETE FROM provenance.schema_migrations WHERE version = 7;
	`); err != nil {
		t.Fatal(err)
	}

	historical, err := InspectSchemaAtHead(ctx, pool, database, 6)
	if err != nil || !historical.Ready || historical.AppliedHead != 6 || historical.PackagedHead != SchemaHead {
		t.Fatalf("historical schema status = %#v err=%v", historical, err)
	}
	if _, err := SnapshotLedgerAtHead(ctx, pool, database, 6); err != nil {
		t.Fatalf("head-6 snapshot: %v", err)
	}
	if current, err := InspectSchema(ctx, pool, database); !errors.Is(err, ErrSchemaBehind) || current.AppliedHead != 6 {
		t.Fatalf("current inspection before migration = %#v err=%v", current, err)
	}

	migrated, err := ApplyMigrations(ctx, pool, database)
	if err != nil || !migrated.Ready || migrated.AppliedHead != SchemaHead {
		t.Fatalf("migrated schema status = %#v err=%v", migrated, err)
	}
	if _, err := SnapshotLedger(ctx, pool, database); err != nil {
		t.Fatalf("current snapshot after migration: %v", err)
	}
	if _, err := InspectSchemaAtHead(ctx, pool, database, 6); !errors.Is(err, ErrSchemaAhead) {
		t.Fatalf("current database accepted as historical head 6: %v", err)
	}
}
