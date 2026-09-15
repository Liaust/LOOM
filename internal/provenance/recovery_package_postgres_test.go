package provenance_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
)

var recoveryPackageDatabaseSequence atomic.Uint64

func TestCreateRecoveryPackagePublishesExactVerifiedSnapshotPostgres(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not available")
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		t.Skip("pg_restore is not available")
	}
	ctx := context.Background()
	sourcePool, sourceDatabase, sourceConfig := newRecoveryPackagePostgres(t, "source")
	targetPool, targetDatabase, targetConfig := newRecoveryPackagePostgres(t, "target")
	if _, err := provenance.ApplyMigrations(ctx, sourcePool, sourceDatabase); err != nil {
		t.Fatal(err)
	}
	store, err := provenance.NewStore(sourcePool)
	if err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	if err := store.AppendCandidate(ctx, provenance.Candidate{
		ID: provenance.SemanticID("00000000-0000-4000-8000-00000000c501"), SchemaVersion: provenance.SchemaVersion,
		State: "pending", Domain: "test", Visibility: "private", RecordKind: "decision",
		Claim: "package-test-claim", RecordContext: "test", AssertionPosture: "reported",
		ProducerID: "package-test", RegisteredAt: registeredAt, Submitted: []byte(`{}`), Payload: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	packagesRoot := t.TempDir()
	verifyCalls := 0
	result, err := provenance.CreateRecoveryPackage(ctx, provenance.RecoveryPackageInput{
		Pool: sourcePool, ExpectedDatabase: sourceDatabase, PackagesRoot: packagesRoot, PackageID: "provenance-001",
		BackupOperationID: "op-001", WorkerRunID: "run-001", NodeID: "loom-main", CreatedAt: registeredAt,
		Dump: func(ctx context.Context, destination, database, exportedSnapshot string) error {
			if database != sourceDatabase || strings.TrimSpace(exportedSnapshot) == "" {
				return fmt.Errorf("unexpected dump binding database=%q snapshot=%q", database, exportedSnapshot)
			}
			if err := runRecoveryPostgresUtility(ctx, sourceConfig, "pg_dump", "--format=custom", "--snapshot="+exportedSnapshot, "--file", destination, "--dbname", database); err != nil {
				return err
			}
			return os.Chmod(destination, 0o640)
		},
		Verify: func(ctx context.Context, packageDir, manifestSHA256 string) error {
			verifyCalls++
			verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, manifestSHA256)
			if err != nil {
				return err
			}
			if verification.Status != maintenance.VerificationSucceeded {
				return fmt.Errorf("verification status %q", verification.Status)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if verifyCalls != 2 {
		t.Fatalf("independent verifier calls = %d, want staged and published verification", verifyCalls)
	}
	if result.PackageDir != filepath.Join(packagesRoot, "provenance-001") || result.ManifestSHA256 == "" || result.Snapshot.RelationCounts["candidates"] != 1 {
		t.Fatalf("recovery package result = %#v", result)
	}
	dumpInfo, err := os.Lstat(filepath.Join(result.PackageDir, provenance.RecoveryDumpFile))
	if err != nil {
		t.Fatal(err)
	}
	if !dumpInfo.Mode().IsRegular() || dumpInfo.Mode().Perm() != 0o600 {
		t.Fatalf("published producer dump mode = %v, want regular 0600", dumpInfo.Mode())
	}
	verification, err := maintenance.VerifyProvenanceBackupPackage(ctx, result.PackageDir, result.ManifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if verification.ManifestSHA256 != result.ManifestSHA256 || verification.GraphDigest != result.Snapshot.GraphDigest {
		t.Fatalf("verification = %#v, snapshot = %#v", verification, result.Snapshot)
	}
	assertDirectArchiveBinderAcceptsRecoveryPackage(t, ctx, result, verification)
	dumpSHA, _, err := maintenance.HashFile(filepath.Join(result.PackageDir, provenance.RecoveryDumpFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.VerifyProvenanceBackupPackage(ctx, result.PackageDir, dumpSHA); err == nil || !strings.Contains(err.Error(), "manifest identity mismatch") {
		t.Fatalf("dump hash was accepted as manifest identity: %v", err)
	}

	if err := runRecoveryPostgresUtility(ctx, targetConfig, "pg_restore", "--no-owner", "--no-acl", "--dbname", targetDatabase, filepath.Join(result.PackageDir, provenance.RecoveryDumpFile)); err != nil {
		t.Fatal(err)
	}
	restored, err := provenance.SnapshotLedger(ctx, targetPool, targetDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := provenance.CompareRecoverySnapshots(result.Snapshot, restored); err != nil {
		t.Fatalf("restored package does not match producer snapshot: %v", err)
	}
}

func assertDirectArchiveBinderAcceptsRecoveryPackage(t *testing.T, ctx context.Context, result provenance.RecoveryPackageResult, verification maintenance.ProvenanceBackupVerification) {
	t.Helper()
	packageRoot := t.TempDir()
	sourceRoot := t.TempDir()
	writeSource := func(name, content string) string {
		path := filepath.Join(sourceRoot, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	migrationRaw, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: 62, LatestVersion: 62, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	operational, err := backupstrategy.CreateOperationalPackage(ctx, backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packageRoot, PackageID: "operational-provenance-contract", CreatedAt: result.CreatedAt, SchemaHead: 62,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP-operational-contract"), 0o600)
		},
		ServiceConfig: writeSource("service.env", "LOOM_ENV=test\n"), InstallConfig: writeSource("install.json", `{"profile":"test"}`),
		ReleaseConfig: writeSource("release.json", `{"release":"test"}`), MigrationState: writeSource("migration.json", string(migrationRaw)),
		UpdateState: writeSource("update.json", `{"status":"idle"}`), Health: writeSource("health.json", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot := filepath.Join(t.TempDir(), "canonical")
	if err := os.Mkdir(canonicalRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalRoot, "payload"), []byte("canonical"), 0o600); err != nil {
		t.Fatal(err)
	}
	schemaHead := int64(62)
	input := backupstrategy.DirectArchiveManifestPrepareInput{
		Backend: backupstrategy.DirectArchiveBackendBorg, Repository: "/tmp/disposable-borg",
		ArchiveName: "__loom-direct-loom-main-history-provenance-contract",
		Request: backupstrategy.DirectArchiveRequest{
			Schema: backupstrategy.DirectArchiveRequestSchema, NodeID: "loom-main", ArchiveRef: "history-provenance-contract", CreatedAt: result.CreatedAt,
			Roots: []backupstrategy.DirectArchiveRoot{{Name: "canonical", Path: canonicalRoot}},
			OperationalPackage: backupstrategy.DirectArchiveOperationalPackage{
				Path: operational.PackageDir, ManifestSHA256: operational.ManifestSHA256, PackageID: operational.Verification.PackageID, ExpectedSchemaHead: &schemaHead,
			},
			ProvenancePackage: backupstrategy.DirectArchiveProvenancePackage{
				Path: result.PackageDir, ManifestSHA256: result.ManifestSHA256, PackageID: result.PackageID,
				Verify: func(ctx context.Context, packageDir, manifestSHA256 string) (backupstrategy.DirectArchiveProvenanceVerification, error) {
					verified, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, manifestSHA256)
					if err != nil {
						return backupstrategy.DirectArchiveProvenanceVerification{}, err
					}
					return backupstrategy.DirectArchiveProvenanceVerification{
						ManifestSHA256: verified.ManifestSHA256, SchemaHead: verified.SchemaHead, GraphDigest: verified.GraphDigest,
						CompletedAt: verified.CompletedAt, DumpSizeBytes: verified.DumpSizeBytes,
					}, nil
				},
			},
		},
	}
	prepared, err := backupstrategy.PrepareDirectArchiveManifest(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Manifest.ProvenancePackage.ManifestSHA256 != verification.ManifestSHA256 || prepared.Manifest.ProvenancePackage.DumpSizeBytes != verification.DumpSizeBytes {
		t.Fatalf("direct archive provenance evidence = %#v", prepared.Manifest.ProvenancePackage)
	}
}

func TestCreateRecoveryPackageFailsBeforePublishWithoutIndependentContractPostgres(t *testing.T) {
	ctx := context.Background()
	pool, database, _ := newRecoveryPackagePostgres(t, "failclosed")
	if _, err := provenance.ApplyMigrations(ctx, pool, database); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	base := provenance.RecoveryPackageInput{
		Pool: pool, ExpectedDatabase: database, PackagesRoot: root, PackageID: "provenance-failed",
		Dump: func(context.Context, string, string, string) error { return nil },
	}
	if _, err := provenance.CreateRecoveryPackage(ctx, base); err == nil || !strings.Contains(err.Error(), "independent verifier") {
		t.Fatalf("missing independent verifier error = %v", err)
	}
	base.Verify = func(context.Context, string, string) error { return nil }
	base.Dump = func(_ context.Context, destination, _, _ string) error {
		return os.WriteFile(destination, []byte("plain SQL"), 0o600)
	}
	if _, err := provenance.CreateRecoveryPackage(ctx, base); err == nil || !strings.Contains(err.Error(), "not PostgreSQL custom format") {
		t.Fatalf("non-custom dump error = %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed producer published evidence: %v", entries)
	}
}

func newRecoveryPackagePostgres(t *testing.T, purpose string) (*pgxpool.Pool, string, *pgxpool.Config) {
	t.Helper()
	adminURL := os.Getenv("LOOM_PROVENANCE_TEST_DB_URL")
	if adminURL == "" {
		t.Skip("LOOM_PROVENANCE_TEST_DB_URL is not set")
	}
	name := fmt.Sprintf("loom_recovery_package_%s_%d_%d", purpose, os.Getpid(), recoveryPackageDatabaseSequence.Add(1))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	databaseConfig := adminConfig.Copy()
	databaseConfig.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, databaseConfig)
	if err != nil {
		_, _ = adminPool.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
		adminPool.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`); err != nil {
			t.Errorf("drop disposable recovery package database %s: %v", name, err)
		}
		adminPool.Close()
	})
	return pool, name, databaseConfig
}

func runRecoveryPostgresUtility(ctx context.Context, config *pgxpool.Config, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(),
		"PGHOST="+config.ConnConfig.Host,
		"PGPORT="+strconv.FormatUint(uint64(config.ConnConfig.Port), 10),
		"PGUSER="+config.ConnConfig.User,
		"PGPASSWORD="+config.ConnConfig.Password,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}
