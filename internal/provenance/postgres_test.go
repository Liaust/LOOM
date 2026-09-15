package provenance

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const provenanceTestDatabaseURLEnv = "LOOM_PROVENANCE_TEST_DB_URL"

var databaseSequence atomic.Uint64

func newDisposablePostgres(t *testing.T, purpose string) (*pgxpool.Pool, string) {
	t.Helper()
	adminURL := os.Getenv(provenanceTestDatabaseURLEnv)
	if adminURL == "" {
		t.Skip(provenanceTestDatabaseURLEnv + " is not set")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(purpose) {
		t.Fatalf("invalid disposable database purpose %q", purpose)
	}
	name := fmt.Sprintf("loom_%s_%d_%d", purpose, os.Getpid(), databaseSequence.Add(1))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("parse disposable PostgreSQL URL: %v", err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("open disposable PostgreSQL admin pool: %v", err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		adminPool.Close()
		t.Fatalf("create disposable PostgreSQL database: %v", err)
	}

	databaseConfig := adminConfig.Copy()
	databaseConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, databaseConfig)
	if err != nil {
		_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		adminPool.Close()
		t.Fatalf("open disposable PostgreSQL database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Errorf("drop disposable PostgreSQL database %s: %v", name, err)
		}
		adminPool.Close()
	})
	return pool, name
}

func migratedStore(t *testing.T, purpose string) (*pgxpool.Pool, *Store, string) {
	t.Helper()
	pool, name := newDisposablePostgres(t, purpose)
	if _, err := ApplyMigrations(context.Background(), pool, name); err != nil {
		t.Fatalf("apply provenance migrations: %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return pool, store, name
}
