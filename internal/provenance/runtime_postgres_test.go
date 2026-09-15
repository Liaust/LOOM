package provenance

import (
	"context"
	"errors"
	"testing"
)

func TestRuntimeMigratesChecksAndClosesDedicatedPoolPostgres(t *testing.T) {
	ctx := context.Background()
	disposablePool, databaseName := newDisposablePostgres(t, "runtime")
	runtimeConfig := disposablePool.Config()
	runtimeConfig.ConnConfig.Database = databaseName

	expectedRole := runtimeConfig.ConnConfig.User
	if runtime, readiness, err := openRuntimeWithConfig(ctx, runtimeConfig, databaseName, expectedRole, false); runtime != nil || !errors.Is(err, ErrSchemaBehind) {
		t.Fatalf("unmigrated OpenRuntime() = runtime=%v readiness=%#v err=%v", runtime, readiness, err)
	} else if readiness.State != ReadinessNotReady || readiness.Code != ReadinessCodeSchemaBehind || readiness.PackagedHead != SchemaHead {
		t.Fatalf("unmigrated readiness = %#v", readiness)
	}

	runtime, readiness, err := openRuntimeWithConfig(ctx, runtimeConfig, databaseName, expectedRole, true)
	if err != nil {
		t.Fatal(err)
	}
	if readiness.State != ReadinessReady || readiness.Code != ReadinessCodeReady || readiness.Database != databaseName || readiness.Role != expectedRole || readiness.AppliedHead != SchemaHead || readiness.PackagedHead != SchemaHead {
		t.Fatalf("migrated readiness = %#v", readiness)
	}
	if got := runtime.Readiness(); got.State != ReadinessReady || got.AppliedHead != SchemaHead {
		t.Fatalf("Runtime.Readiness() = %#v", got)
	}

	runtime.Close()
	runtime.Close()
	if total := runtime.pool.Stat().TotalConns(); total != 0 {
		t.Fatalf("closed runtime retains %d connections", total)
	}
}

func TestRuntimeRejectsWrongDatabaseBeforeMigrationPostgres(t *testing.T) {
	ctx := context.Background()
	pool, databaseName := newDisposablePostgres(t, "runtime_wrong_database")
	runtimeConfig := pool.Config()
	runtimeConfig.ConnConfig.Database = databaseName
	runtime, readiness, err := openRuntimeWithConfig(ctx, runtimeConfig, databaseName+"_other", runtimeConfig.ConnConfig.User, true)
	if runtime != nil || !errors.Is(err, ErrWrongDatabase) {
		t.Fatalf("OpenRuntime() = runtime=%v readiness=%#v err=%v", runtime, readiness, err)
	}
	if readiness.Code != ReadinessCodeWrongDatabase || readiness.Database != databaseName {
		t.Fatalf("wrong-database readiness = %#v", readiness)
	}
	var schemaExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regnamespace('provenance') IS NOT NULL`).Scan(&schemaExists); err != nil {
		t.Fatal(err)
	}
	if schemaExists {
		t.Fatal("wrong-database runtime initialized the provenance schema")
	}
}
