package httpapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/maintenance"
)

func TestOperationalBackupVerificationAdapterPreservesV09FallbackPostgres(t *testing.T) {
	db, _, workerInstanceID := httpWorkerPolicyPostgresFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	sources := filepath.Join(root, "sources")
	packages := filepath.Join(root, "packages")
	if err := os.MkdirAll(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, value string) string {
		path := filepath.Join(sources, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	schemaHead := int64(72)
	migrationRaw, _ := json.Marshal(backupstrategy.OperationalMigrationState{Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: schemaHead, LatestVersion: schemaHead, Status: "ok"})
	packageResult, err := backupstrategy.CreateOperationalPackage(ctx, backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packages, PackageID: "http-operational-package", CreatedAt: time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC), SchemaHead: schemaHead,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01http"), 0o600)
		},
		ServiceConfig: write("loom.env", "LOOM_DB_URL=postgres://secret\n"), InstallConfig: write("install.yaml", "profile: main\n"), ReleaseConfig: write("release.yaml", "release: current\n"),
		MigrationState: write("migration.json", string(migrationRaw)), UpdateState: write("update.json", `{"status":"idle"}`), Health: write("health.json", `{"status":"ok"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	service := maintenance.Service{DB: db}
	operation, err := service.CreateOperation(ctx, maintenance.CreateOperationInput{OperationKey: "http-operational-" + strings.ReplaceAll(workerInstanceID, "_", "-"), WorkerInstanceID: workerInstanceID, OperationKind: maintenance.OperationKindMainBackup, Status: maintenance.OperationRunning, SubjectKind: "node", SubjectID: "main", ConfigJSON: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM maintenance.maintenance_artifacts WHERE maintenance_operation_id=$1`, operation.MaintenanceOperationID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM maintenance.maintenance_operations WHERE maintenance_operation_id=$1`, operation.MaintenanceOperationID)
	})
	result := map[string]any{"schema_version": "main_backup.result.v1", "phase": "recovery_packages", "status": maintenance.OperationSucceeded, "committed": true, "package_id": packageResult.Verification.PackageID, "package_dir": packageResult.PackageDir, "manifest_sha256": packageResult.ManifestSHA256, "schema_head": schemaHead}
	if _, err := service.CompleteOperation(ctx, maintenance.CompleteOperationInput{OperationRef: operation.MaintenanceOperationID, Status: maintenance.OperationSucceeded, ResultJSON: mustJSONTest(t, result), ErrorJSON: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	server := Server{services: Services{Maintenance: service}}
	verification, operational, err := server.verifyOperationalMaintenanceBackup(ctx, operation.MaintenanceOperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !operational || verification.Status != maintenance.VerificationSucceeded || verification.BackupOperationID != operation.MaintenanceOperationID || verification.ManifestSchema != maintenance.OperationalBackupManifestSchema {
		t.Fatalf("verification = %#v operational=%t", verification, operational)
	}
	_, operational, err = server.verifyOperationalMaintenanceBackup(ctx, packageResult.PackageDir+string(os.PathSeparator)+".")
	if err != nil {
		t.Fatal(err)
	}
	if operational {
		t.Fatal("cleaned path alias must not select an exact operational package")
	}
	_, operational, err = server.verifyOperationalMaintenanceBackup(ctx, "historical-v0.9-ref")
	if err != nil {
		t.Fatal(err)
	}
	if operational {
		t.Fatal("unknown historical v0.9 ref must fall through to the existing reader")
	}
}

func mustJSONTest(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
