package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/restoreauthority"
)

var backupDatabaseSequence atomic.Uint64

func TestProvenanceRestoreDrillComparesCompleteLogicalLedgerPostgres(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not available")
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		t.Skip("pg_restore is not available")
	}
	ctx := context.Background()
	sourcePool, sourceDatabase, adminConfig := newBackupDisposablePostgres(t, "loom_provenance_source")
	if _, err := provenance.ApplyMigrations(ctx, sourcePool, sourceDatabase); err != nil {
		t.Fatal(err)
	}
	downgradeBackupTestLedgerToHeadSix(t, ctx, sourcePool)
	store, err := provenance.NewStore(sourcePool)
	if err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, 8, 29, 18, 0, 0, 0, time.UTC)
	candidateID := provenance.SemanticID("00000000-0000-4000-8000-00000000b501")
	sourceID := provenance.SemanticID("00000000-0000-4000-8000-00000000b502")
	sensitive := json.RawMessage(`{"credential":"semantic-payload-MUST-NOT-LEAK"}`)
	if err := store.AppendCandidate(ctx, provenance.Candidate{
		ID: candidateID, SchemaVersion: provenance.SchemaVersion, State: "pending", Domain: "test", Visibility: "private",
		RecordKind: "decision", Claim: "claim-body-MUST-NOT-LEAK", RecordContext: "context", AssertionPosture: "reported",
		ProducerID: "producer", RegisteredAt: registeredAt, Submitted: sensitive, Payload: sensitive,
	}); err != nil {
		t.Fatal(err)
	}
	locator := "git+file:///fixture"
	digest := "sha256:" + strings.Repeat("c", 64)
	excerpt := "source-excerpt-MUST-NOT-LEAK"
	if err := store.AppendSourceReference(ctx, provenance.SourceReference{
		ID: sourceID, CandidateID: &candidateID, SchemaVersion: provenance.SchemaVersion, SourceKind: "git", Status: "resolved",
		VerificationPosture: "content_verified", ResolverName: "git", ResolverVersion: "1.0", ResolutionAt: registeredAt,
		CanonicalLocator: &locator, ContentDigest: &digest, SourceContext: &excerpt, Submitted: sensitive, Payload: sensitive,
	}); err != nil {
		t.Fatal(err)
	}

	before, err := provenance.SnapshotLedgerAtHead(ctx, sourcePool, sourceDatabase, 6)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	if err := os.Chmod(backupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(backupDir, "loom_provenance.dump")
	runPostgresUtility(t, adminConfig, "pg_dump", "--format=custom", "--file", dumpPath, "--dbname", sourceDatabase)
	if err := os.Chmod(dumpPath, 0o600); err != nil {
		t.Fatal(err)
	}
	dumpHash, dumpSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest := maintenance.BackupManifest{
		Schema: maintenance.BackupManifestSchemaV010, BackupKind: "loom_main_and_provenance_state", CreatedAt: registeredAt.Add(time.Minute).Format(time.RFC3339Nano),
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: registeredAt.Format(time.RFC3339Nano), CompletedAt: registeredAt.Add(time.Minute).Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: filepath.Base(dumpPath), Format: "pg_dump_custom"},
			SchemaHead: before.SchemaHead, RequiredRelations: mustRecoveryRelationsForBackupTest(t, before.SchemaHead), LogicalCounts: before.RelationCounts,
			GraphDigest: before.GraphDigest, DumpSizeBytes: dumpSize, DumpSHA256: dumpHash,
		},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: filepath.Base(dumpPath), FileCount: backupInt64Ptr(1), SizeBytes: &dumpSize, SHA256: dumpHash}},
	}
	writeRestoreDrillManifest(t, filepath.Join(backupDir, "manifest.json"), manifest)
	manifestHash, _, err := maintenance.HashFile(filepath.Join(backupDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Mutable live state advances after package creation. Historical recovery
	// correctness must remain bound to the declared package snapshot, not this
	// later source state.
	secondCandidateID := provenance.SemanticID("00000000-0000-4000-8000-00000000b503")
	secondSourceID := provenance.SemanticID("00000000-0000-4000-8000-00000000b504")
	secondCanonicalLocator := "repo://loom/restore-drill/live-after-package"
	if err := store.AppendCandidate(ctx, provenance.Candidate{
		ID: secondCandidateID, SchemaVersion: provenance.SchemaVersion, State: "pending", Domain: "test", Visibility: "private",
		RecordKind: "decision", Claim: "later-live-claim", RecordContext: "later", AssertionPosture: "reported",
		ProducerID: "producer", RegisteredAt: registeredAt.Add(2 * time.Minute), Submitted: json.RawMessage(`{}`), Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendSourceReference(ctx, provenance.SourceReference{
		ID: secondSourceID, CandidateID: &secondCandidateID, SchemaVersion: provenance.SchemaVersion, SourceKind: "git", Status: "resolved",
		VerificationPosture: "locator_verified", ResolverName: "git", ResolverVersion: "1.0", ResolutionAt: registeredAt.Add(2 * time.Minute),
		CanonicalLocator: &secondCanonicalLocator, Submitted: json.RawMessage(`{}`), Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	liveAfterPackage, err := provenance.SnapshotLedgerAtHead(ctx, sourcePool, sourceDatabase, 6)
	if err != nil || liveAfterPackage.GraphDigest == before.GraphDigest {
		t.Fatalf("live source did not advance independently: %#v err=%v", liveAfterPackage, err)
	}

	targetPool, targetDatabase := newRestoredProvenanceTarget(t, adminConfig, dumpPath, "historical_success")

	result, err := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
		BackupDir: backupDir, ExpectedManifestSHA256: manifestHash,
		SourceDatabase: sourceDatabase, TargetDatabase: targetDatabase, SourcePool: sourcePool, RestoredPool: targetPool,
		Now: func() time.Time { return registeredAt.Add(2 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || result.GraphDigest != before.GraphDigest || result.LogicalCounts["source_references"] != 1 || result.RestoredSchemaHead != 6 || result.SchemaHead != 6 || result.FinalSchemaHead != provenance.SchemaHead {
		t.Fatalf("provenance restore result = %#v", result)
	}
	if status, err := provenance.InspectSchema(ctx, targetPool, targetDatabase); err != nil || !status.Ready || status.AppliedHead != provenance.SchemaHead {
		t.Fatalf("migrated target readiness = %#v err=%v", status, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"claim-body-MUST-NOT-LEAK", "source-excerpt-MUST-NOT-LEAK", "semantic-payload-MUST-NOT-LEAK", "postgresql://"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("restore result leaked %q: %s", forbidden, raw)
		}
	}

	tamperedPool, tamperedDatabase := newRestoredProvenanceTarget(t, adminConfig, dumpPath, "tampered_row")
	if _, err := tamperedPool.Exec(ctx, `ALTER TABLE provenance.source_references DISABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedPool.Exec(ctx, `DELETE FROM provenance.source_references WHERE id=$1`, string(sourceID)); err != nil {
		t.Fatal(err)
	}
	if _, err := tamperedPool.Exec(ctx, `ALTER TABLE provenance.source_references ENABLE TRIGGER reject_ledger_mutation`); err != nil {
		t.Fatal(err)
	}
	if _, err := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
		BackupDir: backupDir, ExpectedManifestSHA256: manifestHash,
		SourceDatabase: sourceDatabase, TargetDatabase: tamperedDatabase, SourcePool: sourcePool, RestoredPool: tamperedPool,
	}); err == nil || !strings.Contains(err.Error(), "missing candidate source relation") {
		t.Fatalf("partial restore error = %v", err)
	}

	missingPool, missingDatabase := newRestoredProvenanceTarget(t, adminConfig, dumpPath, "missing_relation")
	if _, err := missingPool.Exec(ctx, `DROP TABLE provenance.registration_replays`); err != nil {
		t.Fatal(err)
	}
	if _, err := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
		BackupDir: backupDir, ExpectedManifestSHA256: manifestHash,
		SourceDatabase: sourceDatabase, TargetDatabase: missingDatabase, SourcePool: sourcePool, RestoredPool: missingPool,
	}); err == nil || !strings.Contains(err.Error(), "schema objects do not match") {
		t.Fatalf("missing relation restore error = %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*maintenance.BackupManifest)
	}{
		{name: "forged_count", mutate: func(item *maintenance.BackupManifest) { item.Provenance.LogicalCounts["source_references"]++ }},
		{name: "forged_graph_digest", mutate: func(item *maintenance.BackupManifest) {
			item.Provenance.GraphDigest = "sha256:" + strings.Repeat("f", 64)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := manifest
			forged.Provenance = cloneBackupManifestProvenanceForTest(manifest.Provenance)
			test.mutate(&forged)
			writeRestoreDrillManifest(t, filepath.Join(backupDir, "manifest.json"), forged)
			forgedHash, _, err := maintenance.HashFile(filepath.Join(backupDir, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			forgedPool, forgedDatabase := newRestoredProvenanceTarget(t, adminConfig, dumpPath, test.name)
			if _, err := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
				BackupDir: backupDir, ExpectedManifestSHA256: forgedHash,
				SourceDatabase: sourceDatabase, TargetDatabase: forgedDatabase, RestoredPool: forgedPool,
			}); err == nil || !strings.Contains(err.Error(), "provenance restore drill failed") {
				t.Fatalf("forged recovery evidence error = %v", err)
			}
		})
	}
	writeRestoreDrillManifest(t, filepath.Join(backupDir, "manifest.json"), manifest)

	migrationFailurePool, migrationFailureDatabase := newRestoredProvenanceTarget(t, adminConfig, dumpPath, "migration_failure")
	if _, err := migrationFailurePool.Exec(ctx, `
		CREATE FUNCTION public.reject_head_seven_migration() RETURNS event_trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'forced disposable migration failure'; END;
		$$;
		CREATE EVENT TRIGGER reject_head_seven_migration ON ddl_command_start
		WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION public.reject_head_seven_migration();
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := RunProvenanceRestoreDrill(ctx, ProvenanceRestoreDrillInput{
		BackupDir: backupDir, ExpectedManifestSHA256: manifestHash,
		SourceDatabase: sourceDatabase, TargetDatabase: migrationFailureDatabase, RestoredPool: migrationFailurePool,
	}); err == nil || !strings.Contains(err.Error(), "migrate disposable provenance restore target") {
		t.Fatalf("failed migration error = %v", err)
	}
}

func TestRestoreAuthorityDisposableOperationalAndHistoricalProvenancePostgres(t *testing.T) {
	adminURL := strings.TrimSpace(os.Getenv("LOOM_PROVENANCE_TEST_DB_URL"))
	if adminURL == "" {
		t.Skip("LOOM_PROVENANCE_TEST_DB_URL is not set")
	}
	for _, utility := range []string{"createdb", "pg_dump", "pg_restore", "dropdb", "psql"} {
		if _, err := exec.LookPath(utility); err != nil {
			t.Skipf("%s is not available", utility)
		}
	}
	ctx := context.Background()
	adminConfig, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	installBackupRestoreAuthorityPeerRules(t, adminPool, adminConfig)
	for _, identity := range []string{"loom", provenance.DatabaseRole} {
		var exists bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=$1)`, identity).Scan(&exists); err != nil || !exists {
			t.Fatalf("required disposable PostgreSQL role %q is unavailable: exists=%t err=%v", identity, exists, err)
		}
	}
	for _, database := range []string{"loom_main", provenance.DatabaseName} {
		var exists bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, database).Scan(&exists); err != nil || !exists {
			t.Fatalf("required smoke-owned active database %q is unavailable: exists=%t err=%v", database, exists, err)
		}
	}

	authority, stopAuthority := startBackupPostgresRestoreAuthority(t, adminConfig)
	operational := createBackupPostgresOperationalPackage(t, adminConfig)
	t.Run("ordinary_comment_fidelity", func(t *testing.T) {
		sourceDatabase := fmt.Sprintf("loom_comment_source_%d_%d", os.Getpid(), backupDatabaseSequence.Add(1))
		if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{sourceDatabase}.Sanitize()+` OWNER loom`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{sourceDatabase}.Sanitize()+` WITH (FORCE)`)
		})
		sourceConfig := adminConfig.Copy()
		sourceConfig.ConnConfig.User = "loom"
		sourceConfig.ConnConfig.Database = sourceDatabase
		sourcePool, err := pgxpool.NewWithConfig(ctx, sourceConfig)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sourcePool.Exec(ctx, `
			CREATE TABLE public.authority_comment_probe (id bigint PRIMARY KEY);
			COMMENT ON TABLE public.authority_comment_probe IS 'ordinary-comment-preserved';
		`); err != nil {
			sourcePool.Close()
			t.Fatal(err)
		}
		dumpPath := filepath.Join(t.TempDir(), "comment-fidelity.dump")
		if _, err := runBackupPostgresCommand(ctx, sourceConfig, "loom", "pg_dump", []string{
			"--format=custom", "--file", dumpPath, "--dbname", sourceDatabase,
		}, nil); err != nil {
			sourcePool.Close()
			t.Fatal(err)
		}
		sourcePool.Close()
		dump, err := os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(dump)
		target := fmt.Sprintf("%scomment_fidelity_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
		result, err := authority.Restore(ctx, restoreauthority.RestoreRequest{
			Kind: restoreauthority.KindOperational, Database: target,
			DumpSize: int64(len(dump)), DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(dump),
		})
		if err != nil || result.Status != "succeeded" {
			t.Fatalf("owner-authenticated comment fidelity restore result=%#v err=%v", result, err)
		}
		targetConfig := adminConfig.Copy()
		targetConfig.ConnConfig.User = "loom"
		targetConfig.ConnConfig.Database = target
		targetPool, err := pgxpool.NewWithConfig(ctx, targetConfig)
		if err != nil {
			t.Fatal(err)
		}
		var comment, sessionUser string
		if err := targetPool.QueryRow(ctx, `SELECT obj_description('public.authority_comment_probe'::regclass), session_user`).Scan(&comment, &sessionUser); err != nil {
			targetPool.Close()
			t.Fatal(err)
		}
		targetPool.Close()
		if comment != "ordinary-comment-preserved" || sessionUser != "loom" {
			t.Fatalf("restored comment/session identity drift: comment=%q session_user=%q", comment, sessionUser)
		}
		if _, err := authority.Drop(ctx, restoreauthority.DropRequest{Kind: restoreauthority.KindOperational, Database: target}); err != nil {
			t.Fatal(err)
		}
		assertBackupPostgresDatabaseAbsent(t, adminPool, target)
	})

	t.Run("postgres_session_privilege_is_refused", func(t *testing.T) {
		sourceDatabase := fmt.Sprintf("loom_restore_privileged_source_%d_%d", os.Getpid(), backupDatabaseSequence.Add(1))
		if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{sourceDatabase}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{sourceDatabase}.Sanitize()+` WITH (FORCE)`)
		})
		sourceConfig := adminConfig.Copy()
		sourceConfig.ConnConfig.Database = sourceDatabase
		sourcePool, err := pgxpool.NewWithConfig(ctx, sourceConfig)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sourcePool.Exec(ctx, `
			CREATE TABLE public.authority_safe_marker (id bigint PRIMARY KEY);
			CREATE FUNCTION public.authority_superuser_probe() RETURNS event_trigger
			LANGUAGE plpgsql AS $$ BEGIN END $$;
			CREATE EVENT TRIGGER authority_superuser_probe ON ddl_command_end
			EXECUTE FUNCTION public.authority_superuser_probe();
		`); err != nil {
			sourcePool.Close()
			t.Fatal(err)
		}
		dumpPath := filepath.Join(t.TempDir(), "privileged.dump")
		if _, err := runBackupPostgresCommand(ctx, sourceConfig, "postgres", "pg_dump", []string{
			"--format=custom", "--file", dumpPath, "--dbname", sourceDatabase,
		}, nil); err != nil {
			sourcePool.Close()
			t.Fatal(err)
		}
		sourcePool.Close()
		listing, err := runBackupPostgresCommand(ctx, adminConfig, "postgres", "pg_restore", []string{"--list", dumpPath}, nil)
		if err != nil || !strings.Contains(string(listing), "EVENT TRIGGER - authority_superuser_probe") {
			t.Fatalf("crafted archive lacks privileged event trigger: err=%v listing=%s", err, listing)
		}

		controlTarget := fmt.Sprintf("%spostgres_control_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
		if _, err := runBackupPostgresCommand(ctx, adminConfig, "postgres", "createdb", []string{"--owner", "loom", "--", controlTarget}, nil); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = runBackupPostgresCommand(context.Background(), adminConfig, "postgres", "dropdb", []string{"--force", "--if-exists", "--", controlTarget}, nil)
		})
		if _, err := runBackupPostgresCommand(ctx, adminConfig, "postgres", "pg_restore", []string{
			"--no-owner", "--no-acl", "--dbname", controlTarget, dumpPath,
		}, nil); err != nil {
			t.Fatalf("postgres control restore did not prove privileged archive behavior: %v", err)
		}
		controlConfig := adminConfig.Copy()
		controlConfig.ConnConfig.Database = controlTarget
		controlPool, err := pgxpool.NewWithConfig(ctx, controlConfig)
		if err != nil {
			t.Fatal(err)
		}
		var privilegedObjectExists bool
		if err := controlPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_event_trigger WHERE evtname='authority_superuser_probe')`).Scan(&privilegedObjectExists); err != nil || !privilegedObjectExists {
			controlPool.Close()
			t.Fatalf("postgres control restore lacks privileged object: exists=%t err=%v", privilegedObjectExists, err)
		}
		controlPool.Close()
		if _, err := runBackupPostgresCommand(ctx, adminConfig, "postgres", "dropdb", []string{"--force", "--if-exists", "--", controlTarget}, nil); err != nil {
			t.Fatal(err)
		}

		dump, err := os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(dump)
		refusedTarget := fmt.Sprintf("%sprivilege_refusal_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
		result, err := authority.Restore(ctx, restoreauthority.RestoreRequest{
			Kind: restoreauthority.KindOperational, Database: refusedTarget,
			DumpSize: int64(len(dump)), DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(dump),
		})
		if err == nil || !result.CleanupAttempted || !result.CleanupSucceeded {
			t.Fatalf("owner-authenticated authority accepted privileged archive: result=%#v err=%v", result, err)
		}
		var authorityErr *restoreauthority.AuthorityError
		if !errors.As(err, &authorityErr) || authorityErr.Stage != restoreauthority.FailureStageDatabaseRestore || authorityErr.Code != restoreauthority.ErrorDatabaseRestoreFailed || result.FailureStage != authorityErr.Stage || result.ErrorCode != authorityErr.Code {
			t.Fatalf("owner-authenticated refusal lost typed stage/code: result=%#v err=%v", result, err)
		}
		assertBackupPostgresDatabaseAbsent(t, adminPool, refusedTarget)
	})

	operationalTarget := fmt.Sprintf("%sauthority_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
	verificationOwner := ""
	verificationVector := false
	runner := func(commandCtx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		if name != "psql" {
			return nil, fmt.Errorf("unexpected operational verification command %q", name)
		}
		if err := adminPool.QueryRow(commandCtx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=$1`, operationalTarget).Scan(&verificationOwner); err != nil {
			return nil, err
		}
		targetConfig := adminConfig.Copy()
		targetConfig.ConnConfig.Database = operationalTarget
		targetPool, err := pgxpool.NewWithConfig(commandCtx, targetConfig)
		if err != nil {
			return nil, err
		}
		err = targetPool.QueryRow(commandCtx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector')`).Scan(&verificationVector)
		targetPool.Close()
		if err != nil {
			return nil, err
		}
		return runBackupPostgresCommand(commandCtx, adminConfig, "loom", name, args, stdin)
	}
	operationalResult, err := RunOperationalRestoreDrill(ctx, OperationalRestoreDrillInput{
		PackageDir: operational.PackageDir, ExpectedManifestSHA256: operational.ManifestSHA256,
		ExpectedPackageID: operational.Verification.PackageID,
		TargetDatabase:    operationalTarget, ActiveDatabase: "loom_main", Owner: "loom",
		Authority: authority, Runner: runner,
	})
	if err != nil || operationalResult.Status != "succeeded" || verificationOwner != "loom" || !verificationVector {
		t.Fatalf("real operational authority restore result=%#v owner=%q vector=%t err=%v", operationalResult, verificationOwner, verificationVector, err)
	}
	assertBackupPostgresDatabaseAbsent(t, adminPool, operationalTarget)

	provenancePackage := createBackupHistoricalProvenancePackage(t, adminConfig)
	provenanceTarget := fmt.Sprintf("%sauthority_%d", ProvenanceRestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
	provenanceStep := NewProvenanceDirectArchiveRecoveryStep(ProvenanceDirectArchiveRecoveryConfig{
		SourceDatabase: provenance.DatabaseName,
		TargetDatabase: provenanceTarget,
		DatabaseURL:    backupPostgresDatabaseURL(adminConfig, provenance.DatabaseRole, provenance.DatabaseName),
		Owner:          provenance.DatabaseRole,
		Authority:      authority,
	})
	provenanceResult, err := provenanceStep(ctx, DirectArchiveRecoveryStepInput{
		PackageDir: provenancePackage.PackageDir, PackageID: provenancePackage.PackageID,
		ManifestSHA256: provenancePackage.ManifestSHA256, SchemaHead: 6,
	})
	if err != nil || provenanceResult.Status != "succeeded" || provenanceResult.SchemaHead != 6 || provenanceResult.Summary["final_schema_head"] != provenance.SchemaHead {
		t.Fatalf("real historical Provenance authority restore result=%#v err=%v", provenanceResult, err)
	}
	assertBackupPostgresDatabaseAbsent(t, adminPool, provenanceTarget)

	// Simulate the caller disappearing after a successful restore. Restarting
	// the authority and issuing only the typed exact drop must remove the sole
	// validated disposable survivor without touching either active database.
	interruptedTarget := fmt.Sprintf("%sinterrupted_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
	dumpPath := filepath.Join(operational.PackageDir, "postgres.dump")
	dump, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(dump)
	if _, err := authority.Restore(ctx, restoreauthority.RestoreRequest{
		Kind: restoreauthority.KindOperational, Database: interruptedTarget,
		DumpSize: int64(len(dump)), DumpSHA256: fmt.Sprintf("%x", digest[:]), Dump: bytes.NewReader(dump),
	}); err != nil {
		t.Fatal(err)
	}
	stopAuthority()
	restartedAuthority, _ := startBackupPostgresRestoreAuthority(t, adminConfig)
	if _, err := restartedAuthority.Drop(ctx, restoreauthority.DropRequest{Kind: restoreauthority.KindOperational, Database: interruptedTarget}); err != nil {
		t.Fatal(err)
	}
	assertBackupPostgresDatabaseAbsent(t, adminPool, interruptedTarget)
	for _, database := range []string{"loom_main", provenance.DatabaseName} {
		var exists bool
		if err := adminPool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, database).Scan(&exists); err != nil || !exists {
			t.Fatalf("active database %q changed during authority tests: exists=%t err=%v", database, exists, err)
		}
	}
}

type backupPostgresAuthorityExecutor struct {
	config *pgxpool.Config
}

func (executor backupPostgresAuthorityExecutor) Run(ctx context.Context, name string, args []string, stdin io.Reader) error {
	_, err := runBackupPostgresCommand(ctx, executor.config, "postgres", name, args, stdin)
	return err
}

func (executor backupPostgresAuthorityExecutor) InspectArchive(ctx context.Context, name string, args []string, stdin io.Reader, limit int64) ([]byte, error) {
	output, err := runBackupPostgresCommand(ctx, executor.config, "postgres", name, args, stdin)
	if err != nil {
		return nil, err
	}
	if int64(len(output)) > limit {
		return nil, fmt.Errorf("archive table of contents exceeds its bound")
	}
	return output, nil
}

func (executor backupPostgresAuthorityExecutor) RestoreArchive(ctx context.Context, name string, args []string, stdin io.Reader, restoreList *os.File) error {
	_, err := runBackupPostgresCommandWithFiles(ctx, executor.config, "postgres", name, args, stdin, []*os.File{restoreList})
	return err
}

func installBackupRestoreAuthorityPeerRules(t *testing.T, adminPool *pgxpool.Pool, adminConfig *pgxpool.Config) {
	t.Helper()
	current, err := osuser.Current()
	if err != nil {
		t.Fatal(err)
	}
	if current.Username == "" || strings.ContainsAny(current.Username, " \t\r\n\"\\") {
		t.Fatalf("disposable peer-auth test requires a simple local OS username, got %q", current.Username)
	}
	var hbaPath, identPath string
	if err := adminPool.QueryRow(context.Background(), `SHOW hba_file`).Scan(&hbaPath); err != nil {
		t.Fatal(err)
	}
	if err := adminPool.QueryRow(context.Background(), `SHOW ident_file`).Scan(&identPath); err != nil {
		t.Fatal(err)
	}
	hbaOriginal, err := os.ReadFile(hbaPath)
	if err != nil {
		t.Fatal(err)
	}
	identOriginal, err := os.ReadFile(identPath)
	if err != nil {
		t.Fatal(err)
	}
	hbaInfo, err := os.Stat(hbaPath)
	if err != nil {
		t.Fatal(err)
	}
	identInfo, err := os.Stat(identPath)
	if err != nil {
		t.Fatal(err)
	}
	const peerMap = "loom_restore_authority"
	rules := fmt.Sprintf(
		"local \"/^loom_restore_drill_[a-z0-9_]+$/\" \"loom\" peer map=%s\n"+
			"local \"/^loom_provenance_restore_drill_[a-z0-9_]+$/\" \"%s\" peer map=%s\n",
		peerMap, provenance.DatabaseRole, peerMap,
	)
	mappings := fmt.Sprintf("%s %s loom\n%s %s %s\n", peerMap, current.Username, peerMap, current.Username, provenance.DatabaseRole)
	if err := os.WriteFile(hbaPath, append([]byte(rules), hbaOriginal...), hbaInfo.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identPath, append([]byte(mappings), identOriginal...), identInfo.Mode().Perm()); err != nil {
		_ = os.WriteFile(hbaPath, hbaOriginal, hbaInfo.Mode().Perm())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(hbaPath, hbaOriginal, hbaInfo.Mode().Perm()); err != nil {
			t.Errorf("restore disposable pg_hba.conf: %v", err)
		}
		if err := os.WriteFile(identPath, identOriginal, identInfo.Mode().Perm()); err != nil {
			t.Errorf("restore disposable pg_ident.conf: %v", err)
		}
		cleanupPool, err := pgxpool.NewWithConfig(context.Background(), adminConfig.Copy())
		if err != nil {
			t.Errorf("connect to reload restored disposable PostgreSQL auth: %v", err)
			return
		}
		defer cleanupPool.Close()
		var reloaded bool
		if err := cleanupPool.QueryRow(context.Background(), `SELECT pg_reload_conf()`).Scan(&reloaded); err != nil || !reloaded {
			t.Errorf("reload restored disposable PostgreSQL auth: reloaded=%t err=%v", reloaded, err)
		}
	})
	var reloaded bool
	if err := adminPool.QueryRow(context.Background(), `SELECT pg_reload_conf()`).Scan(&reloaded); err != nil || !reloaded {
		t.Fatalf("reload disposable PostgreSQL peer rules: reloaded=%t err=%v", reloaded, err)
	}
	for _, rule := range []struct {
		database string
		role     string
	}{
		{database: `/^loom_restore_drill_[a-z0-9_]+$/`, role: "loom"},
		{database: `/^loom_provenance_restore_drill_[a-z0-9_]+$/`, role: provenance.DatabaseRole},
	} {
		var count int
		if err := adminPool.QueryRow(context.Background(), `
			SELECT count(*)
			FROM pg_hba_file_rules
			WHERE error IS NULL
			  AND auth_method = 'peer'
			  AND $1 = ANY(database)
			  AND $2 = ANY(user_name)
			  AND 'map=loom_restore_authority' = ANY(options)
		`, rule.database, rule.role).Scan(&count); err != nil || count != 1 {
			t.Fatalf("disposable PostgreSQL did not parse exact peer rule database=%q role=%q count=%d err=%v", rule.database, rule.role, count, err)
		}
	}
	var mappingCount int
	if err := adminPool.QueryRow(context.Background(), `
		SELECT count(*) FROM pg_ident_file_mappings
		WHERE error IS NULL AND map_name=$1 AND sys_name=$2 AND pg_username = ANY($3::text[])
	`, peerMap, current.Username, []string{"loom", provenance.DatabaseRole}).Scan(&mappingCount); err != nil || mappingCount != 2 {
		t.Fatalf("disposable PostgreSQL did not parse exact peer mappings: count=%d err=%v", mappingCount, err)
	}
}

func startBackupPostgresRestoreAuthority(t *testing.T, adminConfig *pgxpool.Config) (*restoreauthority.Client, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "loom-real-ra-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "authority.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(true)
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	createdbPath, _ := exec.LookPath("createdb")
	pgRestorePath, _ := exec.LookPath("pg_restore")
	dropdbPath, _ := exec.LookPath("dropdb")
	server, err := restoreauthority.NewServer(restoreauthority.ServerConfig{
		SocketPath: path, SocketUID: stat.Uid, SocketGID: stat.Gid, ClientUID: uint32(os.Getuid()),
		Operational:  restoreauthority.DatabasePolicy{ActiveDatabase: "loom_main", Owner: "loom"},
		Provenance:   restoreauthority.DatabasePolicy{ActiveDatabase: provenance.DatabaseName, Owner: provenance.DatabaseRole},
		CreatedbPath: createdbPath, PGRestorePath: pgRestorePath, DropdbPath: dropdbPath,
		PostgresSocketDirectory: adminConfig.ConnConfig.Host, PostgresPort: adminConfig.ConnConfig.Port,
		RestoreListPath: func() string {
			if runtime.GOOS == "darwin" {
				return "/dev/fd/3"
			}
			return "/proc/self/fd/3"
		}(),
		TempDir: dir, Executor: backupPostgresAuthorityExecutor{config: adminConfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := restoreauthority.NewClient(restoreauthority.ClientConfig{
		SocketPath: path, SocketUID: stat.Uid, SocketGID: stat.Gid, ServerUID: uint32(os.Getuid()),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(serverCtx, listener) }()
	readyTarget := fmt.Sprintf("%sready_%d", RestoreDrillDatabasePrefix, backupDatabaseSequence.Add(1))
	if _, err := client.Drop(context.Background(), restoreauthority.DropRequest{Kind: restoreauthority.KindOperational, Database: readyTarget}); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case serveErr := <-done:
				if serveErr != nil {
					t.Errorf("stop real restore authority: %v", serveErr)
				}
			case <-time.After(10 * time.Second):
				t.Error("real restore authority did not stop")
			}
			_ = os.RemoveAll(dir)
		})
	}
	t.Cleanup(stop)
	return client, stop
}

func createBackupPostgresOperationalPackage(t *testing.T, adminConfig *pgxpool.Config) backupstrategy.OperationalPackageCreateResult {
	t.Helper()
	root := t.TempDir()
	packages := filepath.Join(root, "packages")
	sources := filepath.Join(root, "sources")
	if err := os.Mkdir(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, raw string) string {
		path := filepath.Join(sources, name)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	migrationRaw, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: 61, LatestVersion: 61, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := backupstrategy.CreateOperationalPackage(context.Background(), backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packages, PackageID: "operational-authority-real", CreatedAt: time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), SchemaHead: 61,
		PostgresDump: func(ctx context.Context, destination string) error {
			_, err := runBackupPostgresCommand(ctx, adminConfig, "loom", "pg_dump", []string{"--format=custom", "--file", destination, "--dbname", "loom_main"}, nil)
			return err
		},
		ServiceConfig: write("service.env", "LOOM_ENV=test\n"), InstallConfig: write("install.json", `{"profile":"test"}`),
		ReleaseConfig: write("release.json", `{"release":"test"}`), MigrationState: write("migration.json", string(migrationRaw)),
		UpdateState: write("update.json", `{"status":"idle"}`), Health: write("health.json", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func createBackupHistoricalProvenancePackage(t *testing.T, adminConfig *pgxpool.Config) provenance.RecoveryPackageResult {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("loom_provenance_source_%d_%d", os.Getpid(), backupDatabaseSequence.Add(1))
	adminPool, err := pgxpool.NewWithConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize()+` OWNER `+pgx.Identifier{provenance.DatabaseRole}.Sanitize()); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	sourceConfig := adminConfig.Copy()
	sourceConfig.ConnConfig.User = provenance.DatabaseRole
	sourceConfig.ConnConfig.Database = name
	sourcePool, err := pgxpool.NewWithConfig(ctx, sourceConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		sourcePool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = adminPool.Exec(cleanupCtx, `DROP DATABASE IF EXISTS `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
		adminPool.Close()
	})
	if _, err := provenance.ApplyMigrations(ctx, sourcePool, name); err != nil {
		t.Fatal(err)
	}
	downgradeBackupTestLedgerToHeadSix(t, ctx, sourcePool)
	snapshot, err := provenance.SnapshotLedgerAtHead(ctx, sourcePool, name, 6)
	if err != nil {
		t.Fatal(err)
	}
	packageDir := t.TempDir()
	if err := os.Chmod(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dumpPath := filepath.Join(packageDir, provenance.RecoveryDumpFile)
	if _, err := runBackupPostgresCommand(ctx, sourceConfig, provenance.DatabaseRole, "pg_dump", []string{
		"--format=custom", "--file", dumpPath, "--dbname", name,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dumpPath, 0o600); err != nil {
		t.Fatal(err)
	}
	dumpSHA, dumpSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := provenance.RecoveryRelationsForSchemaHead(6)
	if err != nil {
		t.Fatal(err)
	}
	fileCount := int64(1)
	completedAt := time.Date(2026, 9, 2, 10, 5, 0, 0, time.UTC)
	manifest := maintenance.BackupManifest{
		Schema: provenance.RecoveryManifestSchema, BackupKind: provenance.RecoveryBackupKind,
		CreatedAt: completedAt.Format(time.RFC3339Nano), Source: maintenance.BackupManifestSource{NodeID: "loom-main"},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: provenance.RecoveryArtifactKind, Path: provenance.RecoveryDumpFile, FileCount: &fileCount, SizeBytes: &dumpSize, SHA256: dumpSHA}},
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: completedAt.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: provenance.RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: 6, RequiredRelations: relations, LogicalCounts: snapshot.RelationCounts,
			GraphDigest: snapshot.GraphDigest, DumpSizeBytes: dumpSize, DumpSHA256: dumpSHA,
		},
	}
	writeRestoreDrillManifest(t, filepath.Join(packageDir, provenance.RecoveryManifestFile), manifest)
	manifestSHA, _, err := maintenance.HashFile(filepath.Join(packageDir, provenance.RecoveryManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.VerifyProvenanceBackupPackage(ctx, packageDir, manifestSHA); err != nil {
		t.Fatal(err)
	}
	return provenance.RecoveryPackageResult{
		PackageDir: packageDir, PackageID: "provenance-authority-historical-head-6",
		ManifestPath: filepath.Join(packageDir, provenance.RecoveryManifestFile), ManifestSHA256: manifestSHA,
		DumpSizeBytes: dumpSize, Snapshot: snapshot, CreatedAt: completedAt,
	}
}

func runBackupPostgresCommand(ctx context.Context, config *pgxpool.Config, user, name string, args []string, stdin io.Reader) ([]byte, error) {
	return runBackupPostgresCommandWithFiles(ctx, config, user, name, args, stdin, nil)
}

func runBackupPostgresCommandWithFiles(ctx context.Context, config *pgxpool.Config, user, name string, args []string, stdin io.Reader, extraFiles []*os.File) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdin = stdin
	command.ExtraFiles = extraFiles
	command.Env = append(os.Environ(),
		"PGHOST="+config.ConnConfig.Host,
		"PGPORT="+strconv.FormatUint(uint64(config.ConnConfig.Port), 10),
		"PGUSER="+user,
		"PGPASSWORD="+config.ConnConfig.Password,
		"PGDATABASE="+config.ConnConfig.Database,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", filepath.Base(name), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func backupPostgresDatabaseURL(config *pgxpool.Config, user, database string) string {
	identity := url.User(user)
	if config.ConnConfig.Password != "" {
		identity = url.UserPassword(user, config.ConnConfig.Password)
	}
	databaseURL := &url.URL{Scheme: "postgresql", User: identity, Path: "/" + database}
	query := databaseURL.Query()
	query.Set("host", config.ConnConfig.Host)
	query.Set("port", strconv.FormatUint(uint64(config.ConnConfig.Port), 10))
	databaseURL.RawQuery = query.Encode()
	return databaseURL.String()
}

func assertBackupPostgresDatabaseAbsent(t *testing.T, adminPool *pgxpool.Pool, database string) {
	t.Helper()
	var exists bool
	if err := adminPool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, database).Scan(&exists); err != nil || exists {
		t.Fatalf("disposable database %q survived: exists=%t err=%v", database, exists, err)
	}
}

func backupInt64Ptr(value int64) *int64 {
	return &value
}

func mustRecoveryRelationsForBackupTest(t *testing.T, schemaHead int) []string {
	t.Helper()
	relations, err := provenance.RecoveryRelationsForSchemaHead(schemaHead)
	if err != nil {
		t.Fatal(err)
	}
	return relations
}

func cloneBackupManifestProvenanceForTest(input *maintenance.BackupManifestProvenance) *maintenance.BackupManifestProvenance {
	cloned := *input
	cloned.RequiredRelations = append([]string(nil), input.RequiredRelations...)
	cloned.LogicalCounts = make(map[string]int64, len(input.LogicalCounts))
	for relation, count := range input.LogicalCounts {
		cloned.LogicalCounts[relation] = count
	}
	return &cloned
}

func downgradeBackupTestLedgerToHeadSix(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		DROP TABLE provenance.repository_projection_snapshots;
		DROP TABLE provenance.project_projection_snapshots;
		DELETE FROM provenance.schema_migrations WHERE version = 7;
	`); err != nil {
		t.Fatal(err)
	}
}

func newRestoredProvenanceTarget(t *testing.T, adminConfig *pgxpool.Config, dumpPath, suffix string) (*pgxpool.Pool, string) {
	t.Helper()
	pool, database, _ := newBackupDisposablePostgres(t, ProvenanceRestoreDrillDatabasePrefix+suffix)
	runPostgresUtility(t, adminConfig, "pg_restore", "--no-owner", "--no-acl", "--dbname", database, dumpPath)
	return pool, database
}

func TestValidateProvenanceRestoreDrillDatabaseEnforcesIsolation(t *testing.T) {
	for _, test := range []struct {
		target string
		source string
	}{
		{"loom_provenance", "loom_provenance"},
		{"loom_main", "loom_provenance"},
		{"unsafe-target", "loom_provenance"},
		{ProvenanceRestoreDrillDatabasePrefix + "ok", "loom_main"},
	} {
		if err := ValidateProvenanceRestoreDrillDatabase(test.target, test.source); err == nil {
			t.Fatalf("ValidateProvenanceRestoreDrillDatabase(%q, %q) succeeded", test.target, test.source)
		}
	}
	if err := ValidateProvenanceRestoreDrillDatabase(ProvenanceRestoreDrillDatabasePrefix+"ok", "loom_provenance"); err != nil {
		t.Fatal(err)
	}
}

func newBackupDisposablePostgres(t *testing.T, prefix string) (*pgxpool.Pool, string, *pgxpool.Config) {
	t.Helper()
	adminURL := os.Getenv("LOOM_PROVENANCE_TEST_DB_URL")
	if adminURL == "" {
		t.Skip("LOOM_PROVENANCE_TEST_DB_URL is not set")
	}
	name := fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), backupDatabaseSequence.Add(1))
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
			t.Errorf("drop disposable database %s: %v", name, err)
		}
		adminPool.Close()
	})
	return pool, name, adminConfig
}

func runPostgresUtility(t *testing.T, config *pgxpool.Config, name string, args ...string) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Env = append(os.Environ(),
		"PGHOST="+config.ConnConfig.Host,
		"PGPORT="+strconv.FormatUint(uint64(config.ConnConfig.Port), 10),
		"PGUSER="+config.ConnConfig.User,
		"PGPASSWORD="+config.ConnConfig.Password,
		"PGDATABASE="+config.ConnConfig.Database,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v: %s", name, err, strings.TrimSpace(string(output)))
	}
}
