package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"loom.local/loom/internal/restoreauthority"
)

// The parent supplies the tiny owned fixture dump described in recovery_handoff.md.
// This uses the installed Main authority, not a replacement service or cluster.
func TestPGSearchRestoreDrillInstalledAuthorityPostgres(t *testing.T) {
	path := os.Getenv("LOOM_TEST_PG_SEARCH_FIXTURE_DUMP")
	if path == "" {
		t.Skip("LOOM_TEST_PG_SEARCH_FIXTURE_DUMP is not set; installed-authority acceptance is parent-owned")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || !bytes.HasPrefix(raw, []byte("PGDMP")) {
		t.Fatalf("expected a tiny custom-format fixture dump: bytes=%d err=%v", len(raw), err)
	}
	ownerUID, err := restoreauthority.ResolveUserID("postgres")
	if err != nil {
		t.Fatal(err)
	}
	groupGID, err := restoreauthority.ResolveGroupID("loom")
	if err != nil {
		t.Fatal(err)
	}
	activatorUID := uint32(0)
	authority, err := restoreauthority.NewClient(restoreauthority.ClientConfig{
		SocketPath: restoreauthority.DefaultSocketPath, SocketUID: ownerUID,
		SocketGID: groupGID, SocketActivatorUID: &activatorUID,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("%spg_search_%d_%d", restoreauthority.OperationalPrefix, os.Getpid(), time.Now().UnixNano())
	cleanup := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := authority.Drop(ctx, restoreauthority.DropRequest{Kind: restoreauthority.KindOperational, Database: target})
		return err
	}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			if err := cleanup(); err != nil {
				t.Errorf("fixture cleanup failed: %v", err)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	digest := sha256.Sum256(raw)
	result, err := authority.Restore(ctx, restoreauthority.RestoreRequest{
		Kind: restoreauthority.KindOperational, Database: target,
		Dump: bytes.NewReader(raw), DumpSize: int64(len(raw)), DumpSHA256: fmt.Sprintf("%x", digest),
		Extensions: []restoreauthority.ExtensionRequirement{{Name: "pg_search", Version: restoreauthority.PGSearchVersion}},
	})
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("fixture restore result=%#v err=%v", result, err)
	}
	config, err := pgx.ParseConfig("host=/run/postgresql port=5432 user=loom sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.Database = target
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var pin, restricted, extensionOwner bool
	if err := conn.QueryRow(ctx, `SELECT
  EXISTS (SELECT FROM pg_extension WHERE extname='pg_search' AND extversion=$1 AND extnamespace='paradedb'::regnamespace),
  EXISTS (SELECT FROM pg_roles WHERE rolname=session_user AND rolname='loom' AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole),
  EXISTS (SELECT FROM pg_extension WHERE extname='pg_search' AND extowner=(SELECT oid FROM pg_roles WHERE rolname='postgres'))`, restoreauthority.PGSearchVersion).Scan(&pin, &restricted, &extensionOwner); err != nil || !pin || !restricted || !extensionOwner {
		t.Fatalf("extension/role contract: pin=%t restricted=%t extension_owner=%t err=%v", pin, restricted, extensionOwner, err)
	}
	var count, topID int
	var score float64
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM public.loom_pg_search_recovery_fixture`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("restored fixture count=%d err=%v", count, err)
	}
	if err := conn.QueryRow(ctx, `SELECT id, paradedb.score(id) FROM public.loom_pg_search_recovery_fixture
WHERE body @@@ 'unruh' AND category='physics' ORDER BY paradedb.score(id) DESC LIMIT 1`).Scan(&topID, &score); err != nil || topID != 1 || score <= 0 {
		t.Fatalf("restored BM25 top=%d score=%f err=%v", topID, score, err)
	}
	var tableComment, columnComment, indexComment string
	if err := conn.QueryRow(ctx, `SELECT
obj_description('public.loom_pg_search_recovery_fixture'::regclass),
col_description('public.loom_pg_search_recovery_fixture'::regclass, 2),
obj_description('public.loom_pg_search_recovery_fixture_bm25'::regclass)`).Scan(&tableComment, &columnComment, &indexComment); err != nil || tableComment != "ordinary table comment" || columnComment != "ordinary column comment" || indexComment != "ordinary index comment" {
		t.Fatalf("restored comments=%q/%q/%q err=%v", tableComment, columnComment, indexComment, err)
	}
	var schemaComment string
	if err := conn.QueryRow(ctx, `SELECT obj_description('paradedb'::regnamespace,'pg_namespace')`).Scan(&schemaComment); err != nil || schemaComment != "ordinary schema comment" {
		t.Fatalf("ordinary schema comment lost: %q %v", schemaComment, err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err != nil {
		t.Fatal(err)
	}
	cleaned = true
	unexpected, err := pgx.ConnectConfig(ctx, config)
	if unexpected != nil {
		_ = unexpected.Close(ctx)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "3D000" {
		t.Fatalf("expected cleaned target to be absent, got %v", err)
	}
}
