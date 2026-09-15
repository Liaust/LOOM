package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/provenance"
	"loom.local/loom/internal/restoreauthority"
	"loom.local/loom/internal/storagearchive"
)

type restoreAuthorityStub struct {
	restore func(context.Context, restoreauthority.RestoreRequest) (restoreauthority.Result, error)
	drop    func(context.Context, restoreauthority.DropRequest) (restoreauthority.Result, error)
}

func (stub restoreAuthorityStub) Restore(ctx context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
	if stub.restore != nil {
		return stub.restore(ctx, request)
	}
	if _, err := io.ReadAll(request.Dump); err != nil {
		return restoreauthority.Result{}, err
	}
	return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, nil
}

func (stub restoreAuthorityStub) Drop(ctx context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
	if stub.drop != nil {
		return stub.drop(ctx, request)
	}
	return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
}

func TestOperationalRestoreDrillVerifiesExactPackageAndUsesDisposableDatabase(t *testing.T) {
	packageRoot := filepath.Join(t.TempDir(), "packages")
	sourceRoot := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSource := func(name, content string) string {
		path := filepath.Join(sourceRoot, name)
		writeFile(t, path, []byte(content))
		return path
	}
	migrationRaw, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: 61, LatestVersion: 61, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := backupstrategy.CreateOperationalPackage(context.Background(), backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packageRoot, PackageID: "operational-restore", CreatedAt: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), SchemaHead: 61,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01disposable"), 0o600)
		},
		ServiceConfig: writeSource("service.env", "LOOM_ENV=test\n"), InstallConfig: writeSource("install.json", `{"profile":"test"}`),
		ReleaseConfig: writeSource("release.json", `{"release":"test"}`), MigrationState: writeSource("migration.json", string(migrationRaw)),
		UpdateState: writeSource("update.json", `{"status":"idle"}`), Health: writeSource("health.json", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			commands = append(commands, "authority restore "+string(request.Kind)+" "+request.Database)
			raw, readErr := io.ReadAll(request.Dump)
			if readErr != nil || !strings.HasPrefix(string(raw), "PGDMP") {
				t.Fatalf("restore dump = %q err=%v", raw, readErr)
			}
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, nil
		},
		drop: func(_ context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
			commands = append(commands, "authority drop "+string(request.Kind)+" "+request.Database)
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
		},
	}
	runner := func(_ context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "psql" {
			return []byte(`{"nodes":1}`), nil
		}
		return nil, nil
	}
	result, err := RunOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: created.ManifestSHA256, ExpectedPackageID: "operational-restore",
		TargetDatabase: "loom_restore_drill_operational", ActiveDatabase: "loom_main", Runner: runner, Authority: authority,
		Now: func() time.Time { return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil || result.Status != "succeeded" || result.ManifestSHA256 != created.ManifestSHA256 || len(commands) != 3 || commands[2] != "authority drop operational loom_restore_drill_operational" {
		t.Fatalf("operational restore result=%#v commands=%#v err=%v", result, commands, err)
	}
	if _, err := PlanOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: strings.Repeat("f", 64), ExpectedPackageID: "operational-restore",
	}); err == nil {
		t.Fatal("operational restore plan accepted wrong manifest identity")
	}
}

func TestOperationalRestoreDrillPreservesTypedAuthorityFailureThroughWrappingAndCleanup(t *testing.T) {
	created, _, _ := createOperationalRestoreFixture(t)
	target := "loom_restore_drill_typed_failure"
	authorityFailure := &restoreauthority.AuthorityError{
		Stage: restoreauthority.FailureStageDatabaseCreate, Code: restoreauthority.ErrorDatabaseCreateFailed,
		Message: "Disposable database creation failed.",
		Result: restoreauthority.Result{
			Status: "failed", Kind: restoreauthority.KindOperational, Database: target,
			FailureStage: restoreauthority.FailureStageDatabaseCreate, ErrorCode: restoreauthority.ErrorDatabaseCreateFailed,
			CleanupAttempted: true, CleanupSucceeded: true,
		},
	}
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			_, _ = io.Copy(io.Discard, request.Dump)
			return authorityFailure.Result, authorityFailure
		},
	}
	_, err := RunOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: created.ManifestSHA256,
		ExpectedPackageID: created.Verification.PackageID, TargetDatabase: target, Authority: authority,
	})
	var preserved *restoreauthority.AuthorityError
	if err == nil || !errors.As(err, &preserved) || preserved != authorityFailure || preserved.Stage != restoreauthority.FailureStageDatabaseCreate || preserved.Code != restoreauthority.ErrorDatabaseCreateFailed {
		t.Fatalf("typed authority failure was lost after backup wrapping/cleanup: %v", err)
	}
	var databaseFailure *RestoreDatabaseFailure
	if !errors.As(err, &databaseFailure) || databaseFailure.Kind != restoreauthority.KindOperational || databaseFailure.Database != target || !databaseFailure.CleanupAttempted || !databaseFailure.CleanupSucceeded {
		t.Fatalf("final backup cleanup truth was lost: %#v err=%v", databaseFailure, err)
	}
}

func TestOperationalRestoreDrillRejectsSymlinkReplacementBeforePGRestore(t *testing.T) {
	created, dumpPath, trusted := createOperationalRestoreFixture(t)
	attacker := append([]byte(nil), trusted...)
	attacker[len(attacker)-1] ^= 0x1
	commands := []string{}
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			commands = append(commands, "restore")
			held := dumpPath + ".held"
			if err := os.Rename(dumpPath, held); err != nil {
				t.Fatal(err)
			}
			attackerPath := dumpPath + ".attacker"
			writeFile(t, attackerPath, attacker)
			if err := os.Symlink(attackerPath, dumpPath); err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, request.Dump)
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, nil
		},
		drop: func(_ context.Context, _ restoreauthority.DropRequest) (restoreauthority.Result, error) {
			commands = append(commands, "drop")
			return restoreauthority.Result{}, errors.New("injected cleanup failure")
		},
	}
	_, err := RunOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: created.ManifestSHA256, ExpectedPackageID: created.Verification.PackageID,
		TargetDatabase: "loom_restore_drill_symlink", Authority: authority,
	})
	if err == nil || !strings.Contains(err.Error(), "changed during pg_restore") || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("symlink replacement error = %v", err)
	}
	if len(commands) != 2 || commands[0] != "restore" || commands[1] != "drop" {
		t.Fatalf("symlink replacement commands = %#v", commands)
	}
}

func TestOperationalRestoreDrillRejectsSameSizeReplacementBeforePGRestore(t *testing.T) {
	created, dumpPath, trusted := createOperationalRestoreFixture(t)
	attacker := append([]byte(nil), trusted...)
	attacker[len(attacker)-2] ^= 0x1
	commands := []string{}
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			commands = append(commands, "restore")
			replacement := dumpPath + ".replacement"
			writeFile(t, replacement, attacker)
			if err := os.Rename(replacement, dumpPath); err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, request.Dump)
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, nil
		},
		drop: func(_ context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
			commands = append(commands, "drop")
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
		},
	}
	_, err := RunOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: created.ManifestSHA256, ExpectedPackageID: created.Verification.PackageID,
		TargetDatabase: "loom_restore_drill_same_size", Authority: authority,
	})
	if err == nil || !strings.Contains(err.Error(), "changed during pg_restore") {
		t.Fatalf("same-size replacement error = %v", err)
	}
	if len(commands) != 2 || commands[1] != "drop" {
		t.Fatalf("same-size replacement cleanup commands = %#v", commands)
	}
}

func TestOperationalRestoreDrillUsesAuthenticatedBytesAndDetectsMutationDuringPGRestore(t *testing.T) {
	created, dumpPath, trusted := createOperationalRestoreFixture(t)
	attacker := append([]byte(nil), trusted...)
	attacker[len(attacker)-3] ^= 0x1
	commands := []string{}
	var consumed []byte
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			commands = append(commands, "restore")
			if err := os.WriteFile(dumpPath, attacker, 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			consumed, err = io.ReadAll(request.Dump)
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database}, err
		},
		drop: func(_ context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
			commands = append(commands, "drop")
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
		},
	}
	_, err := RunOperationalRestoreDrill(context.Background(), OperationalRestoreDrillInput{
		PackageDir: created.PackageDir, ExpectedManifestSHA256: created.ManifestSHA256, ExpectedPackageID: created.Verification.PackageID,
		TargetDatabase: "loom_restore_drill_during_read", Authority: authority,
	})
	if err == nil || !strings.Contains(err.Error(), "changed during pg_restore") {
		t.Fatalf("during-read mutation error = %v", err)
	}
	if string(consumed) != string(trusted) || string(consumed) == string(attacker) {
		t.Fatalf("pg_restore consumed unauthenticated bytes: got=%q trusted=%q attacker=%q", consumed, trusted, attacker)
	}
	if len(commands) != 2 || commands[1] != "drop" {
		t.Fatalf("during-read cleanup commands = %#v", commands)
	}
}

func createOperationalRestoreFixture(t *testing.T) (backupstrategy.OperationalPackageCreateResult, string, []byte) {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "packages")
	sourceRoot := filepath.Join(t.TempDir(), "sources")
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	writeSource := func(name, content string) string {
		path := filepath.Join(sourceRoot, name)
		writeFile(t, path, []byte(content))
		return path
	}
	migrationRaw, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: 61, LatestVersion: 61, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	trusted := []byte("PGDMP\x01authenticated-operational-dump")
	created, err := backupstrategy.CreateOperationalPackage(context.Background(), backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packageRoot, PackageID: "operational-toctou", CreatedAt: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), SchemaHead: 61,
		PostgresDump:  func(_ context.Context, destination string) error { return os.WriteFile(destination, trusted, 0o600) },
		ServiceConfig: writeSource("service.env", "LOOM_ENV=test\n"), InstallConfig: writeSource("install.json", `{"profile":"test"}`),
		ReleaseConfig: writeSource("release.json", `{"release":"test"}`), MigrationState: writeSource("migration.json", string(migrationRaw)),
		UpdateState: writeSource("update.json", `{"status":"idle"}`), Health: writeSource("health.json", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return created, filepath.Join(created.PackageDir, "postgres.dump"), trusted
}

func TestValidateRestoreDrillDatabaseRefusesUnsafeTargets(t *testing.T) {
	for _, target := range []string{
		"",
		"loom_main",
		"loom_restore",
		"loom_restore_drill_bad-name",
	} {
		if err := ValidateRestoreDrillDatabase(target, "loom_main"); err == nil {
			t.Fatalf("ValidateRestoreDrillDatabase(%q) succeeded, want error", target)
		}
	}
	if err := ValidateRestoreDrillDatabase("loom_restore_drill_safe_001", "loom_main"); err != nil {
		t.Fatalf("safe drill database rejected: %v", err)
	}
}

func TestMetadataOnlyDirectArchiveCheckCannotBecomeStrictRestoreEvidence(t *testing.T) {
	result := DirectArchiveRestoreDrillResult{
		Status:     "succeeded",
		FinishedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Plan: DirectArchiveRestoreDrillPlan{
			ManifestSHA256: strings.Repeat("a", sha256.Size*2),
			Extraction: backupstrategy.DirectArchiveExtractionVerification{
				Status: "succeeded", Checks: map[string]string{"manifest_identity": "succeeded", "declared_payload_roots": "succeeded"},
			},
		},
	}
	if _, err := MigrationEvidenceFromDirectArchiveRestore("repository", result, time.Date(2026, 8, 30, 13, 0, 0, 0, time.UTC)); err == nil || !strings.Contains(err.Error(), "complete strict direct-archive restore") {
		t.Fatalf("metadata-only result became strict restore evidence: %v", err)
	}
}

func TestDirectArchiveV2SymlinkEvidenceClonePreservesNilEmptyAndPrivacy(t *testing.T) {
	if cloneDirectArchiveV2UserSymlinkTargets(nil) != nil {
		t.Fatal("nil v2 symlink evidence became authenticated empty evidence")
	}
	empty := map[string]string{}
	emptyClone := cloneDirectArchiveV2UserSymlinkTargets(empty)
	if emptyClone == nil || len(emptyClone) != 0 {
		t.Fatalf("authenticated empty v2 symlink evidence clone = %#v", emptyClone)
	}
	source := map[string]string{"root/link": "/outside/private-target"}
	cloned := cloneDirectArchiveV2UserSymlinkTargets(source)
	source["root/link"] = "/caller/mutation"
	if cloned["root/link"] != "/outside/private-target" {
		t.Fatalf("owned v2 symlink evidence aliases caller map: %#v", cloned)
	}
	prepared := backupstrategy.PreparedDirectArchiveManifest{V2UserSymlinkTargets: cloned}
	raw, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/outside/private-target") {
		t.Fatalf("prepared manifest JSON exposed transient v2 symlink evidence: %s", raw)
	}
}

func TestManagedProvenanceRecoveryUsesDisposableTargetAndJoinsCleanupFailureWithoutCredentialLeak(t *testing.T) {
	packageDir, manifestSHA := createProvenanceRestorePackageFixture(t, 6)
	commands := []string{}
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, request restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			commands = append(commands, "restore "+string(request.Kind)+" "+request.Database)
			if raw, err := io.ReadAll(request.Dump); err != nil || !strings.HasPrefix(string(raw), "PGDMP") {
				t.Fatalf("provenance restore input = %q err=%v", raw, err)
			}
			return restoreauthority.Result{}, errors.New("injected primary restore failure")
		},
		drop: func(_ context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
			commands = append(commands, "drop "+string(request.Kind)+" "+request.Database)
			return restoreauthority.Result{}, errors.New("injected cleanup failure")
		},
	}
	step := NewProvenanceDirectArchiveRecoveryStep(ProvenanceDirectArchiveRecoveryConfig{
		SourceDatabase: provenance.DatabaseName,
		DatabaseURL:    "postgresql://loom_provenance:credential-MUST-NOT-LEAK@localhost/loom_provenance",
		Owner:          provenance.DatabaseRole,
		Authority:      authority,
		Now:            func() time.Time { return time.Date(2026, 9, 1, 4, 5, 6, 0, time.UTC) },
	})
	_, err := step(context.Background(), DirectArchiveRecoveryStepInput{
		PackageDir: packageDir, PackageID: "provenance-historical-head-6",
		ManifestSHA256: manifestSHA, SchemaHead: 6,
	})
	if err == nil || !strings.Contains(err.Error(), "injected primary restore failure") || !strings.Contains(err.Error(), "injected cleanup failure") {
		t.Fatalf("managed provenance recovery error = %v", err)
	}
	for _, forbidden := range []string{"credential-MUST-NOT-LEAK", "postgresql://"} {
		if strings.Contains(err.Error(), forbidden) || strings.Contains(strings.Join(commands, "\n"), forbidden) {
			t.Fatalf("managed provenance recovery leaked %q: err=%v commands=%v", forbidden, err, commands)
		}
	}
	if len(commands) != 2 || commands[0] != "restore provenance loom_provenance_restore_drill_20260901040506" || commands[1] != "drop provenance loom_provenance_restore_drill_20260901040506" {
		t.Fatalf("managed provenance commands = %#v", commands)
	}
	for _, command := range commands {
		if strings.Contains(command, " loom_main") || strings.HasSuffix(command, " "+provenance.DatabaseName) {
			t.Fatalf("managed provenance recovery targeted an active database: %s", command)
		}
	}
}

func TestManagedProvenanceRecoveryAttemptsBoundedCleanupAfterRequestCancellation(t *testing.T) {
	packageDir, manifestSHA := createProvenanceRestorePackageFixture(t, 6)
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cleanupUsedLiveContext := false
	authority := restoreAuthorityStub{
		restore: func(_ context.Context, _ restoreauthority.RestoreRequest) (restoreauthority.Result, error) {
			cancelRequest()
			return restoreauthority.Result{}, context.Canceled
		},
		drop: func(commandCtx context.Context, request restoreauthority.DropRequest) (restoreauthority.Result, error) {
			if err := commandCtx.Err(); err != nil {
				t.Fatalf("cleanup reused cancelled request context: %v", err)
			}
			cleanupUsedLiveContext = true
			return restoreauthority.Result{Status: "succeeded", Kind: request.Kind, Database: request.Database, CleanupAttempted: true, CleanupSucceeded: true}, nil
		},
	}
	step := NewProvenanceDirectArchiveRecoveryStep(ProvenanceDirectArchiveRecoveryConfig{
		SourceDatabase: provenance.DatabaseName,
		DatabaseURL:    "postgresql://loom_provenance@localhost/loom_provenance",
		Owner:          provenance.DatabaseRole,
		Authority:      authority,
	})
	_, err := step(requestCtx, DirectArchiveRecoveryStepInput{
		PackageDir: packageDir, PackageID: "provenance-historical-head-6",
		ManifestSHA256: manifestSHA, SchemaHead: 6,
	})
	if !errors.Is(err, context.Canceled) || !cleanupUsedLiveContext {
		t.Fatalf("cancelled managed recovery error=%v cleanup_live=%t", err, cleanupUsedLiveContext)
	}
}

func createProvenanceRestorePackageFixture(t *testing.T, schemaHead int) (string, string) {
	t.Helper()
	packageDir := t.TempDir()
	if err := os.Chmod(packageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dump := []byte("PGDMP\x01authenticated-provenance-dump")
	dumpPath := filepath.Join(packageDir, provenance.RecoveryDumpFile)
	if err := os.WriteFile(dumpPath, dump, 0o600); err != nil {
		t.Fatal(err)
	}
	dumpSHA, dumpSize, err := maintenance.HashFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	relations, err := provenance.RecoveryRelationsForSchemaHead(schemaHead)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int64, len(relations))
	for _, relation := range relations {
		counts[relation] = 0
	}
	fileCount := int64(1)
	completedAt := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	manifest := maintenance.BackupManifest{
		Schema: provenance.RecoveryManifestSchema, BackupKind: provenance.RecoveryBackupKind,
		CreatedAt: completedAt.Format(time.RFC3339Nano),
		Provenance: &maintenance.BackupManifestProvenance{
			State: provenance.BackupStateComplete, StartedAt: completedAt.Add(-time.Minute).Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano),
			Database:   maintenance.BackupManifestDatabase{Name: provenance.DatabaseName, DumpFile: provenance.RecoveryDumpFile, Format: "pg_dump_custom"},
			SchemaHead: schemaHead, RequiredRelations: relations, LogicalCounts: counts,
			GraphDigest: "sha256:" + strings.Repeat("a", 64), DumpSizeBytes: dumpSize, DumpSHA256: dumpSHA,
		},
		Artifacts: []maintenance.BackupManifestArtifact{{Kind: maintenance.ArtifactKindProvenanceDump, Path: provenance.RecoveryDumpFile, FileCount: &fileCount, SizeBytes: &dumpSize, SHA256: dumpSHA}},
	}
	writeRestoreDrillManifest(t, filepath.Join(packageDir, provenance.RecoveryManifestFile), manifest)
	manifestSHA, _, err := maintenance.HashFile(filepath.Join(packageDir, provenance.RecoveryManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	return packageDir, manifestSHA
}

func TestPlanRestoreDrillVerifiesBackupWithoutRunningCommands(t *testing.T) {
	dir := validBackupDir(t)

	plan, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{
		BackupDir: dir,
		Now: func() time.Time {
			return time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("PlanRestoreDrill returned error: %v", err)
	}
	if plan.TargetDatabase != "loom_restore_drill_20260602100000" {
		t.Fatalf("target database = %q", plan.TargetDatabase)
	}
	if plan.Verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("verification status = %q", plan.Verification.Status)
	}
}

func TestPlanRestoreDrillRequiresDeclaredMainStorageRoots(t *testing.T) {
	dir := validBackupDir(t)
	manifest := readRestoreDrillManifest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.UserBackups = "user-backups"
	manifest.Paths.MainDocuments = "main-documents"
	manifest.Paths.StorageArchive = "storage-archive"
	writeRestoreDrillManifest(t, filepath.Join(dir, "manifest.json"), manifest)
	if err := os.Mkdir(filepath.Join(dir, "user-backups"), 0o755); err != nil {
		t.Fatalf("mkdir user-backups: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "main-documents"), 0o755); err != nil {
		t.Fatalf("mkdir main-documents: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "storage-archive", "incomplete-key", "objects"), 0o755); err != nil {
		t.Fatalf("mkdir incomplete archive key: %v", err)
	}

	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err == nil {
		t.Fatal("PlanRestoreDrill succeeded without archive-key manifest")
	}

	writeValidRestoreArchiveCustody(t, filepath.Join(dir, "storage-archive"), "incomplete-key", []byte("archive payload"))
	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err != nil {
		t.Fatalf("PlanRestoreDrill returned error after required roots were present: %v", err)
	}
}

func TestPlanRestoreDrillVerifiesBothV09UserBackupCustodyPaths(t *testing.T) {
	dir := validBackupDir(t)
	importsRoot := filepath.Join(dir, "imports")
	if err := os.Mkdir(importsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(importsRoot, "payload.bin"), []byte("imports payload"))
	if _, err := maintenance.WriteImportsBackupEvidence(context.Background(), importsRoot, filepath.Join(dir, "imports-evidence.json"), maintenance.ImportsBackupPolicyLegacy, maintenance.ImportsSnapshotSharedStore, 1); err != nil {
		t.Fatal(err)
	}
	userBackups := filepath.Join(dir, "user-backups")
	if err := os.Mkdir(userBackups, 0o755); err != nil {
		t.Fatal(err)
	}
	privateBackups := filepath.Join(dir, "private-backups")
	operation := filepath.Join(privateBackups, "node_main", "private_backup_test")
	if err := os.MkdirAll(operation, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("legacy payload")
	writeFile(t, filepath.Join(operation, "payload.tar"), payload)
	userSize := int64(0)
	privateSize := int64(len(payload))
	userFiles := int64(0)
	privateFiles := int64(1)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := readRestoreDrillManifest(t, manifestPath)
	manifest.Schema = maintenance.BackupManifestSchemaV09
	manifest.Paths.Imports = "imports"
	manifest.Paths.ImportsEvidence = "imports-evidence.json"
	manifest.Paths.UserBackups = "user-backups"
	manifest.Paths.PrivateBackups = "private-backups"
	manifest.Paths.PrivateBackupsEvidence = maintenance.PrivateBackupsEvidencePath
	manifest.Policies.MainBox = "selected_canonical_roots_copied_once"
	manifest.Policies.NotesSourceRoots = "watched_roots"
	manifest.Policies.NotesProjection = "excluded"
	manifest.Policies.KnowledgeIndex = "postgres"
	manifest.Policies.Imports = maintenance.ImportsBackupPolicyLegacy
	manifest.Policies.ImportsSnapshot = maintenance.ImportsSnapshotSharedStore
	if _, err := maintenance.WritePrivateBackupsEvidence(context.Background(), privateBackups, manifest.Paths.PrivateBackups, filepath.Join(dir, maintenance.PrivateBackupsEvidencePath)); err != nil {
		t.Fatal(err)
	}
	evidenceHash, evidenceSize, err := maintenance.HashFile(filepath.Join(dir, maintenance.PrivateBackupsEvidencePath))
	if err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts = []maintenance.BackupManifestArtifact{
		{Kind: maintenance.ArtifactKindPrivateBackupsSnapshot, Path: "user-backups", FileCount: &userFiles, SizeBytes: &userSize},
		{Kind: maintenance.ArtifactKindPrivateBackupsSnapshot, Path: "private-backups", FileCount: &privateFiles, SizeBytes: &privateSize},
		{Kind: maintenance.ArtifactKindPrivateBackupsEvidence, Path: maintenance.PrivateBackupsEvidencePath, SizeBytes: &evidenceSize, SHA256: evidenceHash},
	}
	writeRestoreDrillManifest(t, manifestPath, manifest)

	plan, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir})
	if err != nil {
		t.Fatalf("PlanRestoreDrill rejected valid dual custody: %v", err)
	}
	if plan.Verification.Checks["user_backups_dual_path"] != maintenance.VerificationSucceeded {
		t.Fatalf("dual custody verification = %#v", plan.Verification)
	}
	if err := os.WriteFile(filepath.Join(operation, "payload.tar"), []byte("legacy payloaD"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err == nil {
		t.Fatal("PlanRestoreDrill accepted same-size legacy payload tamper")
	}
	if err := os.WriteFile(filepath.Join(operation, "payload.tar"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(operation, "payload.tar")); err != nil {
		t.Fatal(err)
	}
	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err == nil {
		t.Fatal("PlanRestoreDrill accepted mixed custody with missing legacy payload")
	}
}

func TestPlanRestoreDrillValidatesCanonicalArchiveKeysBesideLegacyLayout(t *testing.T) {
	dir := validBackupDir(t)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := readRestoreDrillManifest(t, manifestPath)
	manifest.Paths.StorageArchive = "storage-archive"
	writeRestoreDrillManifest(t, manifestPath, manifest)
	for _, child := range []string{"objects", "manifests", filepath.Join("incomplete-key", "objects")} {
		if err := os.MkdirAll(filepath.Join(dir, "storage-archive", child), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir, TargetDatabase: "loom_restore_drill_test"}); err == nil {
		t.Fatal("PlanRestoreDrill accepted an invalid canonical key beside the legacy archive layout")
	}
}

func TestPlanRestoreDrillRequiresDeclaredNotesProjection(t *testing.T) {
	dir := validBackupDir(t)
	manifest := readRestoreDrillManifest(t, filepath.Join(dir, "manifest.json"))
	manifest.Paths.NotesProjection = "loom-notes"
	writeRestoreDrillManifest(t, filepath.Join(dir, "manifest.json"), manifest)

	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err == nil {
		t.Fatal("PlanRestoreDrill succeeded without declared notes projection directory")
	}

	if err := os.Mkdir(filepath.Join(dir, "loom-notes"), 0o755); err != nil {
		t.Fatalf("mkdir loom-notes: %v", err)
	}
	if _, err := PlanRestoreDrill(context.Background(), RestoreDrillInput{BackupDir: dir}); err != nil {
		t.Fatalf("PlanRestoreDrill returned error after notes projection was present: %v", err)
	}
}

func TestRunRestoreDrillUsesTemporaryDatabaseAndCleansUp(t *testing.T) {
	dir := validBackupDir(t)
	var commands []string
	runner := func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		commands = append(commands, name+" "+joinArgs(args))
		if name == "psql" {
			return []byte(`{"nodes":1,"worker_instances":1}`), nil
		}
		return []byte{}, nil
	}

	result, err := RunRestoreDrill(context.Background(), RestoreDrillInput{
		BackupDir:      dir,
		TargetDatabase: "loom_restore_drill_test",
		Runner:         runner,
		Now: func() time.Time {
			return time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("RunRestoreDrill returned error: %v", err)
	}
	if result.Status != "succeeded" {
		t.Fatalf("status = %q", result.Status)
	}
	want := []string{
		"createdb --owner loom -- loom_restore_drill_test",
		"pg_restore --no-acl --dbname loom_restore_drill_test",
		"psql --no-psqlrc --dbname loom_restore_drill_test --set ON_ERROR_STOP=1 --quiet --tuples-only --no-align",
		"dropdb --if-exists -- loom_restore_drill_test",
	}
	if len(commands) != len(want) {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("commands[%d] = %q, want %q", i, commands[i], want[i])
		}
	}
}

func readRestoreDrillManifest(t *testing.T, path string) maintenance.BackupManifest {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest maintenance.BackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	return manifest
}

func writeRestoreDrillManifest(t *testing.T, path string, manifest maintenance.BackupManifest) {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, path, raw)
}

func validBackupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "health.json"), []byte(`{"ok":true}`))
	writeFile(t, filepath.Join(dir, "loom_main.dump"), []byte("dump\n"))
	if err := os.Mkdir(filepath.Join(dir, "object-store"), 0o755); err != nil {
		t.Fatalf("mkdir object-store: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "private-backups"), 0o755); err != nil {
		t.Fatalf("mkdir private-backups: %v", err)
	}
	current := int64(36)
	latest := int64(36)
	manifest := maintenance.BackupManifest{
		Schema:     maintenance.BackupManifestSchemaV063,
		BackupKind: "loom_main_state",
		CreatedAt:  "2026-06-02T10:00:00Z",
		Source: maintenance.BackupManifestSource{
			Hostname:    "loom-main",
			NodeID:      "main",
			Environment: "production",
		},
		Loom: maintenance.BackupManifestLoom{
			Version:          "0.5.1-test",
			CurrentMigration: &current,
			LatestMigration:  &latest,
		},
		Database: maintenance.BackupManifestDatabase{
			Name:     "loom_main",
			DumpFile: "loom_main.dump",
			Format:   "pg_dump_custom",
		},
		Paths: maintenance.BackupManifestPaths{
			ObjectStore:    "object-store",
			PrivateBackups: "private-backups",
		},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	writeFile(t, filepath.Join(dir, "manifest.json"), raw)
	return dir
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, arg := range args {
		if i > 0 {
			out += " "
		}
		out += arg
	}
	return out
}

func writeValidRestoreArchiveCustody(t *testing.T, root, archiveKey string, payload []byte) {
	t.Helper()
	sum := sha256.Sum256(payload)
	size := int64(len(payload))
	checksum := hex.EncodeToString(sum[:])
	objectRelative := filepath.ToSlash(filepath.Join("objects", "sha256", checksum[:2], checksum))
	objectPath := filepath.Join(root, archiveKey, filepath.FromSlash(objectRelative))
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, objectPath, payload)
	manifest := storagearchive.ManifestDocument{
		SchemaVersion: storagearchive.ManifestSchemaVersion, ArchiveManifestID: ids.NewStorageArchiveManifestID(),
		ArchiveKey: archiveKey, ArchiveKind: "document_archive", SourceRef: "source/file.txt",
		TargetPath: "main/Archive/Documents/file.txt", OwnerNodeKey: "main",
		ExpectedEntryCount: 1, ArchivedEntryCount: 1, Complete: true, CreatedAt: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		Entries: []storagearchive.ManifestItem{{
			SourceStorageEntryID: ids.NewStorageEntryID(), ArchiveStorageEntryID: ids.NewStorageEntryID(),
			ArchivePhysicalRefID: ids.NewStoragePhysicalRefID(), SourceViewPath: "source/file.txt",
			SourceLogicalPath: "file.txt", SourceAvailabilityState: "available",
			ArchiveViewPath: "main/Archive/Documents/file.txt", ArchiveLogicalPath: "Documents/file.txt",
			ChecksumAlgorithm: "sha256", ChecksumHex: hex.EncodeToString(sum[:]), SizeBytes: &size,
			FileClass: "text", ContentKey: "sha256:" + hex.EncodeToString(sum[:]), ArchiveObjectPath: objectRelative,
		}},
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, archiveKey, "manifest.json"), raw)
}
