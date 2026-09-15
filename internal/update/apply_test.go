package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/backupstrategy"
	"loom.local/loom/internal/maintenance"
)

func TestApplyAcceptsExactOperationalPackageForKnownNonUserRootPlan(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, true)
	declareOperationalOnlyRelease(t, target)
	packageResult, secret := validOperationalPackageForUpdate(t, root, 36)
	backupRef := "maintenance_operational_test"

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath: target, ActivePath: current, ActiveMigrationsDir: filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "state"),
		},
		Runtime: productionMainRuntime(), Yes: true, DryRun: true,
		BackupPath: packageResult.PackageDir, BackupRef: backupRef,
		BackupVerifier: NewOperationalPackageBackupVerifier(backupRef, packageResult.ManifestSHA256, 36, []string{secret}),
		Now:            fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Refused || result.Status != UpdateStatusDryRun {
		t.Fatalf("result = %#v, want dry-run acceptance", result)
	}
	if !result.Plan.Migrations.WillApply || result.Plan.Rollback.Class != RollbackClassRestoreRequired {
		t.Fatalf("plan = %#v, want database migration protected by custom dump", result.Plan)
	}
	if len(result.Changed) == 0 || result.Changed[0].Step != "backup" || result.Changed[0].Status != "verified" {
		t.Fatalf("changed = %#v, want verified operational backup first", result.Changed)
	}
}

func TestApplyOperationalPackageFailsClosedOnSchemaHeadAndRef(t *testing.T) {
	t.Run("schema head", func(t *testing.T) {
		root := t.TempDir()
		active, target, current := prepareUpdateReleases(t, root, false)
		declareOperationalOnlyRelease(t, target)
		packageResult, secret := validOperationalPackageForUpdate(t, root, 35)
		result, err := Apply(context.Background(), ApplyInput{
			Spec:    UpdateSpec{ReleasePath: target, ActivePath: current, ActiveMigrationsDir: filepath.Join(active, "migrations"), CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "state")},
			Runtime: devRuntime(), Yes: true, DryRun: true, AllowNonProduction: true,
			BackupPath: packageResult.PackageDir, BackupRef: "operation-test",
			BackupVerifier: NewOperationalPackageBackupVerifier("operation-test", packageResult.ManifestSHA256, 35, []string{secret}),
			Now:            fixedNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Refused || !strings.Contains(result.Refusal, "schema head") {
			t.Fatalf("result = %#v, want schema-head refusal", result)
		}
		assertApplyDidNotMutate(t, current, active, filepath.Join(root, "state"))
	})

	t.Run("registered ref", func(t *testing.T) {
		root := t.TempDir()
		active, target, current := prepareUpdateReleases(t, root, false)
		declareOperationalOnlyRelease(t, target)
		packageResult, secret := validOperationalPackageForUpdate(t, root, 36)
		result, err := Apply(context.Background(), ApplyInput{
			Spec:    UpdateSpec{ReleasePath: target, ActivePath: current, ActiveMigrationsDir: filepath.Join(active, "migrations"), CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "state")},
			Runtime: devRuntime(), Yes: true, DryRun: true, AllowNonProduction: true,
			BackupPath: packageResult.PackageDir, BackupRef: "different-operation",
			BackupVerifier: NewOperationalPackageBackupVerifier("expected-operation", packageResult.ManifestSHA256, 36, []string{secret}),
			Now:            fixedNow,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !result.Refused || !strings.Contains(result.Refusal, "backup verification failed") {
			t.Fatalf("result = %#v, want exact ref refusal", result)
		}
		assertApplyDidNotMutate(t, current, active, filepath.Join(root, "state"))
	})
}

func TestOperationalPackageCompatibilityUsesOnlyTrustedBackupScope(t *testing.T) {
	valid := UpdateBackupScope{
		DeclarationStatus:         BackupScopeDeclarationValid,
		DeclaredSchema:            ReleaseBackupScopeSchemaVersion,
		DeclaredClass:             ReleaseBackupScopeOperationalOnly,
		RequiredBackup:            UpdateBackupRequirementOperational,
		OperationalPackageAllowed: true,
	}
	tests := []struct {
		name  string
		scope UpdateBackupScope
		want  bool
	}{
		{name: "absent"},
		{name: "malformed", scope: UpdateBackupScope{DeclarationStatus: BackupScopeDeclarationMalformed, RequiredBackup: UpdateBackupRequirementComplete}},
		{name: "unknown schema", scope: UpdateBackupScope{DeclarationStatus: BackupScopeDeclarationUnknownSchema, DeclaredSchema: "loom.release.backup_scope.v2", DeclaredClass: ReleaseBackupScopeOperationalOnly, RequiredBackup: UpdateBackupRequirementComplete}},
		{name: "unknown class", scope: UpdateBackupScope{DeclarationStatus: BackupScopeDeclarationUnknownClass, DeclaredSchema: ReleaseBackupScopeSchemaVersion, DeclaredClass: "future_scope", RequiredBackup: UpdateBackupRequirementComplete}},
		{name: "canonical user data", scope: UpdateBackupScope{DeclarationStatus: BackupScopeDeclarationValid, DeclaredSchema: ReleaseBackupScopeSchemaVersion, DeclaredClass: ReleaseBackupScopeCanonicalUserData, RequiredBackup: UpdateBackupRequirementComplete}},
		{name: "operational only", scope: valid, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := UpdatePlan{
				BackupScope: test.scope,
				Steps: []UpdateStep{
					{ID: "nixos_rebuild", Mutating: true},
					{ID: "migrations", Mutating: true},
				},
			}
			if got := operationalPackageCompatibleWithPlan(plan); got != test.want {
				t.Fatalf("compatible = %t, want %t for %#v", got, test.want, test.scope)
			}
		})
	}
}

func TestApplyOperationalPackageRequiresOperationalOnlyReleaseDeclaration(t *testing.T) {
	tests := []struct {
		name        string
		declaration *ReleaseBackupScope
	}{
		{name: "absent"},
		{name: "canonical user data", declaration: &ReleaseBackupScope{SchemaVersion: ReleaseBackupScopeSchemaVersion, Class: ReleaseBackupScopeCanonicalUserData}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			active, target, current := prepareUpdateReleases(t, root, false)
			if test.declaration != nil {
				if err := WriteReleaseManifest(filepath.Join(target, DefaultReleaseManifestFileYAML), ReleaseManifest{
					SourcePath: target, BackupScope: test.declaration,
				}); err != nil {
					t.Fatal(err)
				}
			}
			packageResult, secret := validOperationalPackageForUpdate(t, root, 36)
			result, err := Apply(context.Background(), ApplyInput{
				Spec:    UpdateSpec{ReleasePath: target, ActivePath: current, ActiveMigrationsDir: filepath.Join(active, "migrations"), CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "state")},
				Runtime: devRuntime(), Yes: true, DryRun: true, AllowNonProduction: true,
				BackupPath: packageResult.PackageDir, BackupRef: "operation-test",
				BackupVerifier: NewOperationalPackageBackupVerifier("operation-test", packageResult.ManifestSHA256, 36, []string{secret}),
				Now:            fixedNow,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Refused || !strings.Contains(result.Refusal, "operational-only release backup-scope") {
				t.Fatalf("result = %#v, want complete-backup refusal", result)
			}
			assertApplyDidNotMutate(t, current, active, filepath.Join(root, "state"))
		})
	}
}

func TestApplyRefusesWithoutYes(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                filepath.Join(root, "state"),
		},
		Runtime:            devRuntime(),
		AllowNonProduction: true,
		SkipBackup:         true,
		SkipRebuild:        true,
		SkipHealthCheck:    true,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "--yes") {
		t.Fatalf("result = %#v, want --yes refusal", result)
	}
}

func TestApplyProductionRequiresVerifiedBackup(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                filepath.Join(root, "state"),
		},
		Runtime:         productionMainRuntime(),
		Yes:             true,
		SkipRebuild:     false,
		SkipHealthCheck: false,
		Now:             fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "--backup-path") {
		t.Fatalf("result = %#v, want backup refusal", result)
	}
}

func TestApplyProductionRequiresServiceIdentityBackupVerifier(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	backupPath := filepath.Join(root, "backup")

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:    productionMainRuntime(),
		Yes:        true,
		BackupPath: backupPath,
		BackupRef:  "maintenance_operation_test",
		Now:        fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Refused || !strings.Contains(result.Refusal, "service-identity") {
		t.Fatalf("result = %#v, want service-identity verifier refusal", result)
	}
	assertApplyDidNotMutate(t, current, active, stateDir)
}

func TestApplyProductionVerifierFailsClosedBeforeMutation(t *testing.T) {
	tests := []struct {
		name         string
		path         func(string) string
		ref          string
		verification func(string, string) (maintenance.BackupVerification, error)
		wantError    bool
		wantRefusal  string
	}{
		{
			name: "verifier error",
			ref:  "maintenance_operation_test",
			verification: func(_, _ string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{}, errors.New("loomd unavailable")
			},
			wantError: true,
		},
		{
			name: "failed verification",
			ref:  "maintenance_operation_test",
			verification: func(path, ref string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: "failed", BackupDir: path, BackupOperationID: ref}, nil
			},
			wantRefusal: "backup verification failed",
		},
		{
			name: "missing operation identity",
			ref:  "maintenance_operation_test",
			verification: func(path, _ string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: path}, nil
			},
			wantRefusal: "different maintenance operation identity",
		},
		{
			name: "different operation identity",
			ref:  "maintenance_operation_test",
			verification: func(path, _ string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: path, BackupOperationID: "maintenance_operation_other"}, nil
			},
			wantRefusal: "different maintenance operation identity",
		},
		{
			name: "different directory",
			ref:  "maintenance_operation_test",
			verification: func(_, ref string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: "/var/lib/loom/backups/main/other", BackupOperationID: ref}, nil
			},
			wantRefusal: "different backup directory",
		},
		{
			name: "missing backup ref",
			ref:  "",
			verification: func(path, ref string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: path, BackupOperationID: ref}, nil
			},
			wantRefusal: "requires --backup-ref",
		},
		{
			name: "path alias",
			path: func(root string) string { return root + string(os.PathSeparator) + "nested/../backup" },
			ref:  "maintenance_operation_test",
			verification: func(path, ref string) (maintenance.BackupVerification, error) {
				return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: path, BackupOperationID: ref}, nil
			},
			wantRefusal: "exact clean absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			active, target, current := prepareUpdateReleases(t, root, false)
			stateDir := filepath.Join(root, "state")
			backupPath := filepath.Join(root, "backup")
			if tt.path != nil {
				backupPath = tt.path(root)
			}
			calls := 0
			result, err := Apply(context.Background(), ApplyInput{
				Spec: UpdateSpec{
					ReleasePath:             target,
					ActivePath:              current,
					ActiveMigrationsDir:     filepath.Join(active, "migrations"),
					CurrentMigrationVersion: int64Ptr(36),
					StateDir:                stateDir,
				},
				Runtime:    productionMainRuntime(),
				Yes:        true,
				BackupPath: backupPath,
				BackupRef:  tt.ref,
				BackupVerifier: func(_ context.Context, input BackupVerificationInput) (maintenance.BackupVerification, error) {
					calls++
					if input.BackupPath != backupPath || input.BackupRef != tt.ref {
						t.Fatalf("verifier args = (%q, %q), want (%q, %q)", input.BackupPath, input.BackupRef, backupPath, tt.ref)
					}
					return tt.verification(input.BackupPath, input.BackupRef)
				},
				Now: fixedNow,
			})
			if tt.wantError && err == nil {
				t.Fatal("Apply returned nil error, want verifier error")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("Apply returned error: %v", err)
			}
			if tt.wantRefusal != "" && (!result.Refused || !strings.Contains(result.Refusal, tt.wantRefusal)) {
				t.Fatalf("result = %#v, want refusal containing %q", result, tt.wantRefusal)
			}
			wantCalls := 1
			if tt.ref == "" || tt.path != nil {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("verifier calls = %d, want %d", calls, wantCalls)
			}
			assertApplyDidNotMutate(t, current, active, stateDir)
		})
	}
}

func TestApplyProductionVerifierSuccessProceedsOnce(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	backupPath := filepath.Join(root, "backup")
	backupRef := "maintenance_operation_test"
	verifierCalls := 0
	runnerCalls := 0

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:    productionMainRuntime(),
		Yes:        true,
		BackupPath: backupPath,
		BackupRef:  backupRef,
		BackupVerifier: func(_ context.Context, input BackupVerificationInput) (maintenance.BackupVerification, error) {
			verifierCalls++
			if input.BackupPath != backupPath || input.BackupRef != backupRef {
				t.Fatalf("verifier args = (%q, %q), want (%q, %q)", input.BackupPath, input.BackupRef, backupPath, backupRef)
			}
			return maintenance.BackupVerification{
				Status:            maintenance.VerificationSucceeded,
				BackupDir:         backupPath,
				BackupOperationID: backupRef,
			}, nil
		},
		Runner: func(_ context.Context, name string, args ...string) ([]byte, error) {
			runnerCalls++
			if name == "loom" {
				return []byte(`{"ok":true,"data":{"status":"ok"}}`), nil
			}
			return []byte("ok"), nil
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Status != UpdateStatusSucceeded || result.Refused {
		t.Fatalf("result = %#v, want succeeded", result)
	}
	if verifierCalls != 1 {
		t.Fatalf("verifier calls = %d, want 1", verifierCalls)
	}
	if runnerCalls != 2 {
		t.Fatalf("runner calls = %d, want rebuild and health", runnerCalls)
	}
	if result.Manifest.BackupPath != backupPath || result.Manifest.BackupRef != backupRef {
		t.Fatalf("manifest backup binding = (%q, %q)", result.Manifest.BackupPath, result.Manifest.BackupRef)
	}
	assertSymlinkTarget(t, current, target)
}

func TestApplyMode0700TargetFailsBeforeAnyEffect(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("chmod target: %v", err)
	}
	verifierCalls := 0
	runnerCalls := 0
	coordinator := &fakeMaintenanceCoordinator{}

	_, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:    productionMainRuntime(),
		Yes:        true,
		BackupPath: filepath.Join(root, "backup"),
		BackupRef:  "maintenance_operation_test",
		BackupVerifier: func(context.Context, BackupVerificationInput) (maintenance.BackupVerification, error) {
			verifierCalls++
			return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded}, nil
		},
		MaintenancePolicy: MaintenancePausePolicy{PauseSchedules: true},
		Maintenance:       coordinator,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			runnerCalls++
			return []byte("unexpected"), nil
		},
		Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "mode is 0700, want 0755") {
		t.Fatalf("expected exact-mode refusal, got %v", err)
	}
	if verifierCalls != 0 || coordinator.openCalls != 0 || coordinator.resumeCalls != 0 || runnerCalls != 0 {
		t.Fatalf("effects occurred: verifier=%d maintenance=%d/%d runner=%d", verifierCalls, coordinator.openCalls, coordinator.resumeCalls, runnerCalls)
	}
	assertApplyDidNotMutate(t, current, active, stateDir)
}

func TestApplyNonProductionCanUseExplicitVerifier(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	backupPath := filepath.Join(root, "backup")
	calls := 0
	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                filepath.Join(root, "state"),
		},
		Runtime:            devRuntime(),
		Yes:                true,
		AllowNonProduction: true,
		SkipRebuild:        true,
		SkipHealthCheck:    true,
		BackupPath:         backupPath,
		BackupVerifier: func(_ context.Context, input BackupVerificationInput) (maintenance.BackupVerification, error) {
			calls++
			return maintenance.BackupVerification{Status: maintenance.VerificationSucceeded, BackupDir: input.BackupPath}, nil
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Status != UpdateStatusSucceeded || calls != 1 {
		t.Fatalf("result=%#v verifier calls=%d, want success and one call", result, calls)
	}
}

func TestApplyDevDrillSwitchesReleaseAndWritesManifests(t *testing.T) {
	root := t.TempDir()
	_, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(current, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:            devRuntime(),
		Yes:                true,
		AllowNonProduction: true,
		SkipBackup:         true,
		SkipRebuild:        true,
		SkipHealthCheck:    true,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Status != UpdateStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	assertSymlinkTarget(t, current, target)
	if _, err := os.Stat(ActiveManifestPath(stateDir)); err != nil {
		t.Fatalf("active manifest missing: %v", err)
	}
	if _, err := os.Stat(result.HistoryPath); err != nil {
		t.Fatalf("history manifest missing: %v", err)
	}
}

func TestApplyOpensAndResumesMaintenanceWindow(t *testing.T) {
	root := t.TempDir()
	_, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	coordinator := &fakeMaintenanceCoordinator{}

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(current, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:            devRuntime(),
		Yes:                true,
		AllowNonProduction: true,
		SkipBackup:         true,
		SkipRebuild:        true,
		SkipHealthCheck:    true,
		MaintenancePolicy:  MaintenancePausePolicy{PauseSchedules: true},
		Maintenance:        coordinator,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if coordinator.openCalls != 1 || coordinator.resumeCalls != 1 {
		t.Fatalf("coordinator calls open=%d resume=%d, want 1/1", coordinator.openCalls, coordinator.resumeCalls)
	}
	if result.Manifest.Maintenance == nil || result.Manifest.Maintenance.Status != MaintenanceWindowStatusResumed {
		t.Fatalf("maintenance window = %#v, want resumed", result.Manifest.Maintenance)
	}
	if result.Manifest.Maintenance.Schedules[0].ResumeStatus != MaintenanceItemStatusResumed {
		t.Fatalf("schedule item = %#v, want resumed", result.Manifest.Maintenance.Schedules[0])
	}
	manifest, err := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if err != nil {
		t.Fatalf("ReadUpdateManifest: %v", err)
	}
	if manifest.Maintenance == nil || manifest.Maintenance.Status != MaintenanceWindowStatusResumed {
		t.Fatalf("persisted maintenance window = %#v, want resumed", manifest.Maintenance)
	}
}

func TestApplyFailedHealthCheckWritesFailedManifest(t *testing.T) {
	root := t.TempDir()
	_, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(current, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:            devRuntime(),
		Yes:                true,
		AllowNonProduction: true,
		SkipBackup:         true,
		SkipRebuild:        true,
		Now:                fixedNow,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"ok":false,"data":{"status":"unhealthy"}}`), nil
		},
	})
	if err == nil {
		t.Fatalf("Apply returned nil error, want failed health check")
	}
	if result.Status != UpdateStatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	manifest, readErr := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if readErr != nil {
		t.Fatalf("ReadUpdateManifest: %v", readErr)
	}
	if manifest.Status != UpdateStatusFailed || metadataString(manifest.Metadata, "failed_step") != "health_check" {
		t.Fatalf("manifest = %#v, want failed health_check", manifest)
	}
}

func TestApplyFailureAfterMaintenancePauseMarksResumeRequired(t *testing.T) {
	root := t.TempDir()
	_, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	coordinator := &fakeMaintenanceCoordinator{}

	result, err := Apply(context.Background(), ApplyInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              current,
			ActiveMigrationsDir:     filepath.Join(current, "migrations"),
			CurrentMigrationVersion: int64Ptr(36),
			StateDir:                stateDir,
		},
		Runtime:            devRuntime(),
		Yes:                true,
		AllowNonProduction: true,
		SkipBackup:         true,
		SkipRebuild:        true,
		MaintenancePolicy:  MaintenancePausePolicy{PauseSchedules: true},
		Maintenance:        coordinator,
		Now:                fixedNow,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(`{"ok":false,"data":{"status":"unhealthy"}}`), nil
		},
	})
	if err == nil {
		t.Fatalf("Apply returned nil error, want failed health check")
	}
	if result.Status != UpdateStatusFailed {
		t.Fatalf("status = %q, want failed", result.Status)
	}
	if coordinator.resumeCalls != 0 {
		t.Fatalf("resume calls = %d, want 0 after health failure", coordinator.resumeCalls)
	}
	manifest, readErr := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if readErr != nil {
		t.Fatalf("ReadUpdateManifest: %v", readErr)
	}
	if manifest.Maintenance == nil || manifest.Maintenance.Status != MaintenanceWindowStatusResumeRequired {
		t.Fatalf("maintenance window = %#v, want resume_required", manifest.Maintenance)
	}
}

func TestResumeMaintenanceUsesCoordinatorAndWritesManifest(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	manifest := updateManifestForRollback("update_resume", filepath.Join(root, "current"), filepath.Join(root, "active"), filepath.Join(root, "target"), false)
	manifest.Maintenance = fakePausedMaintenanceWindow("update_resume")
	if err := WriteUpdateManifest(ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}
	coordinator := &fakeMaintenanceCoordinator{}

	result, err := ResumeMaintenance(context.Background(), ResumeMaintenanceInput{
		StateDir:           stateDir,
		Yes:                true,
		AllowNonProduction: true,
		Runtime:            devRuntime(),
		Maintenance:        coordinator,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("ResumeMaintenance returned error: %v", err)
	}
	if coordinator.resumeCalls != 1 {
		t.Fatalf("resume calls = %d, want 1", coordinator.resumeCalls)
	}
	if result.Status != MaintenanceWindowStatusResumed {
		t.Fatalf("status = %q, want resumed", result.Status)
	}
	persisted, err := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if err != nil {
		t.Fatalf("ReadUpdateManifest: %v", err)
	}
	if persisted.Maintenance == nil || persisted.Maintenance.Schedules[0].ResumeStatus != MaintenanceItemStatusResumed {
		t.Fatalf("persisted maintenance = %#v, want resumed schedule", persisted.Maintenance)
	}
}

func TestResumeMaintenanceDoesNotResumePreviouslyPausedItems(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	manifest := updateManifestForRollback("update_resume_selective", filepath.Join(root, "current"), filepath.Join(root, "active"), filepath.Join(root, "target"), false)
	window := fakePausedMaintenanceWindow("update_resume_selective")
	window.Schedules = append(window.Schedules, MaintenancePausedItem{
		Kind:           MaintenanceItemKindSchedule,
		Ref:            "schedule_already_paused",
		DisplayName:    "Schedule Already Paused",
		PreviousStatus: "paused",
		PauseStatus:    MaintenanceItemStatusAlreadyPaused,
	})
	manifest.Maintenance = window
	if err := WriteUpdateManifest(ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}
	coordinator := &fakeMaintenanceCoordinator{}

	result, err := ResumeMaintenance(context.Background(), ResumeMaintenanceInput{
		StateDir:           stateDir,
		Yes:                true,
		AllowNonProduction: true,
		Runtime:            devRuntime(),
		Maintenance:        coordinator,
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("ResumeMaintenance returned error: %v", err)
	}
	if result.Status != MaintenanceWindowStatusResumed {
		t.Fatalf("status = %q, want resumed", result.Status)
	}
	if len(coordinator.resumedRefs) != 1 || coordinator.resumedRefs[0] != "schedule_test" {
		t.Fatalf("resumed refs = %#v, want only schedule_test", coordinator.resumedRefs)
	}
	persisted, err := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if err != nil {
		t.Fatalf("ReadUpdateManifest: %v", err)
	}
	if got := persisted.Maintenance.Schedules[1].ResumeStatus; got != "" {
		t.Fatalf("already-paused schedule resume status = %q, want empty", got)
	}
}

func TestResumeMaintenanceNoopCoordinatorCannotResumeRecordedResources(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	manifest := updateManifestForRollback("update_resume_noop", filepath.Join(root, "current"), filepath.Join(root, "active"), filepath.Join(root, "target"), false)
	manifest.Maintenance = fakePausedMaintenanceWindow("update_resume_noop")
	if err := WriteUpdateManifest(ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}

	result, err := ResumeMaintenance(context.Background(), ResumeMaintenanceInput{
		StateDir:           stateDir,
		Yes:                true,
		AllowNonProduction: true,
		Runtime:            devRuntime(),
		Maintenance:        NoopMaintenanceCoordinator{Reason: "no database configured"},
		Now:                fixedNow,
	})
	if err == nil {
		t.Fatalf("ResumeMaintenance returned nil error, want unavailable coordinator error")
	}
	if result.Status != MaintenanceWindowStatusResumeRequired {
		t.Fatalf("status = %q, want resume_required", result.Status)
	}
	persisted, readErr := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if readErr != nil {
		t.Fatalf("ReadUpdateManifest: %v", readErr)
	}
	if persisted.Maintenance == nil || persisted.Maintenance.Status != MaintenanceWindowStatusResumeRequired {
		t.Fatalf("persisted maintenance = %#v, want resume_required", persisted.Maintenance)
	}
}

func TestRollbackServiceOnlySwitchesBack(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, false)
	stateDir := filepath.Join(root, "state")
	manifest := updateManifestForRollback("update_test", current, active, target, false)
	if err := WriteUpdateManifest(ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}

	result, err := Rollback(context.Background(), RollbackInput{
		StateDir:           stateDir,
		Yes:                true,
		AllowNonProduction: true,
		ServiceOnly:        true,
		SkipRebuild:        true,
		SkipHealthCheck:    true,
		Runtime:            devRuntime(),
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}
	if result.Status != UpdateStatusRolledBack {
		t.Fatalf("status = %q, want rolled_back", result.Status)
	}
	assertSymlinkTarget(t, current, active)
}

func TestRollbackRestoreRequiredReturnsRunbook(t *testing.T) {
	root := t.TempDir()
	active, target, current := prepareUpdateReleases(t, root, true)
	stateDir := filepath.Join(root, "state")
	manifest := updateManifestForRollback("update_migrations", current, active, target, true)
	manifest.BackupPath = filepath.Join(root, "backup")
	if err := WriteUpdateManifest(ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("WriteUpdateManifest: %v", err)
	}

	result, err := Rollback(context.Background(), RollbackInput{
		StateDir:           stateDir,
		Yes:                true,
		AllowNonProduction: true,
		RestoreRequired:    true,
		Runtime:            devRuntime(),
		Now:                fixedNow,
	})
	if err != nil {
		t.Fatalf("Rollback returned error: %v", err)
	}
	if result.Status != UpdateStatusDatabaseRestoreRequired {
		t.Fatalf("status = %q, want database_restore_required", result.Status)
	}
	if len(result.Runbook) == 0 || !strings.Contains(strings.Join(result.Runbook, "\n"), "loom backup verify") {
		t.Fatalf("runbook = %#v, want backup restore instructions", result.Runbook)
	}
	assertSymlinkTarget(t, current, active)
}

func prepareUpdateReleases(t *testing.T, root string, targetAhead bool) (string, string, string) {
	t.Helper()
	active := filepath.Join(root, "release-active")
	target := filepath.Join(root, "release-target")
	current := filepath.Join(root, "current")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	if targetAhead {
		writeMigration(t, target, "00037_next.sql")
	}
	if err := os.Symlink(active, current); err != nil {
		t.Fatalf("symlink current: %v", err)
	}
	return active, target, current
}

func assertApplyDidNotMutate(t *testing.T, current, active, stateDir string) {
	t.Helper()
	assertSymlinkTarget(t, current, active)
	if _, err := os.Lstat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("update state path exists after refusal: err=%v", err)
	}
}

func updateManifestForRollback(updateID, current, active, target string, restoreRequired bool) UpdateManifest {
	return UpdateManifest{
		SchemaVersion: UpdateManifestSchemaVersion,
		UpdateID:      updateID,
		Status:        UpdateStatusSucceeded,
		PlanHash:      "sha256:test",
		StartedAt:     fixedNow(),
		Active: ReleaseState{
			ReleaseID: "release_active",
			Path:      active,
			Version:   "test",
			Source:    "active_path",
		},
		Target: ReleaseState{
			ReleaseID: "release_target",
			Path:      target,
			Version:   "test",
			Source:    "target_path",
		},
		Migrations: MigrationPlan{
			Status:         "ok",
			CurrentVersion: 36,
			TargetLatestVersion: func() int64 {
				if restoreRequired {
					return 37
				}
				return 36
			}(),
			Pending:   0,
			WillApply: restoreRequired,
		},
		Rollback: RollbackPlan{
			Class: func() string {
				if restoreRequired {
					return RollbackClassRestoreRequired
				}
				return RollbackClassServiceOnly
			}(),
			ServiceOnlyPossible: !restoreRequired,
			RestoreRequired:     restoreRequired,
		},
		Metadata: map[string]any{
			"active_path": current,
		},
	}
}

func devRuntime() RuntimeIdentity {
	return RuntimeIdentity{Environment: "dev", NodeID: "dev-main", NodeRole: "main"}
}

func productionMainRuntime() RuntimeIdentity {
	return RuntimeIdentity{Environment: "production", NodeID: "main", NodeRole: "main"}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func assertSymlinkTarget(t *testing.T, linkPath, want string) {
	t.Helper()
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("Readlink(%s): %v", linkPath, err)
	}
	if got != want {
		t.Fatalf("symlink target = %s, want %s", got, want)
	}
}

func validOperationalPackageForUpdate(t *testing.T, root string, schemaHead int64) (backupstrategy.OperationalPackageCreateResult, string) {
	t.Helper()
	packages := filepath.Join(root, "operational-packages")
	sources := filepath.Join(root, "operational-sources")
	if err := os.Mkdir(packages, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(sources, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := "update-operational-secret"
	service := filepath.Join(sources, "service.env")
	install := filepath.Join(sources, "install.yaml")
	release := filepath.Join(sources, "release.yaml")
	migration := filepath.Join(sources, "migration.json")
	updateState := filepath.Join(sources, "update.json")
	health := filepath.Join(sources, "health.json")
	write := func(path string, payload []byte) {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(service, []byte("LOOM_DB_URL="+secret+"\n"))
	write(install, []byte("credential: "+secret+"\n"))
	write(release, []byte("token: "+secret+"\n"))
	migrationRaw, err := json.Marshal(backupstrategy.OperationalMigrationState{
		Schema: backupstrategy.OperationalMigrationStateSchema, CurrentVersion: schemaHead, LatestVersion: schemaHead, Status: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	write(migration, migrationRaw)
	write(updateState, []byte(`{"schema":"loom.update.state.v1","status":"idle"}`))
	write(health, []byte(`{"status":"ok"}`))
	result, err := backupstrategy.CreateOperationalPackage(context.Background(), backupstrategy.OperationalPackageCreateInput{
		PackagesRoot: packages, PackageID: "operational-update", CreatedAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC), SchemaHead: schemaHead,
		PostgresDump: func(_ context.Context, destination string) error {
			return os.WriteFile(destination, []byte("PGDMP\x01update"), 0o600)
		},
		ServiceConfig: service, InstallConfig: install, ReleaseConfig: release,
		MigrationState: migration, UpdateState: updateState, Health: health,
		RedactConfig: func(_ string, source []byte) ([]byte, error) {
			return bytes.ReplaceAll(source, []byte(secret), []byte("[REDACTED]")), nil
		},
		ForbiddenValues: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	return result, secret
}

func declareOperationalOnlyRelease(t *testing.T, releasePath string) {
	t.Helper()
	if err := WriteReleaseManifest(filepath.Join(releasePath, DefaultReleaseManifestFileYAML), ReleaseManifest{
		SourcePath:    releasePath,
		MigrationsDir: filepath.Join(releasePath, "migrations"),
		BackupScope: &ReleaseBackupScope{
			SchemaVersion: ReleaseBackupScopeSchemaVersion,
			Class:         ReleaseBackupScopeOperationalOnly,
		},
	}); err != nil {
		t.Fatalf("declare operational-only release: %v", err)
	}
}

type fakeMaintenanceCoordinator struct {
	openCalls   int
	resumeCalls int
	resumedRefs []string
}

func (f *fakeMaintenanceCoordinator) Open(_ context.Context, input MaintenanceOpenInput) (MaintenanceWindow, error) {
	f.openCalls++
	window := fakePausedMaintenanceWindow(input.UpdateID)
	window.PausePolicy = input.Policy
	return *window, nil
}

func (f *fakeMaintenanceCoordinator) Resume(_ context.Context, input MaintenanceResumeInput) (MaintenanceWindow, error) {
	f.resumeCalls++
	window := input.Window
	window.Status = MaintenanceWindowStatusResumed
	resumedAt := fixedNow()
	for i := range window.Schedules {
		if window.Schedules[i].PauseStatus == MaintenanceItemStatusPaused {
			f.resumedRefs = append(f.resumedRefs, window.Schedules[i].Ref)
			window.Schedules[i].ResumeStatus = MaintenanceItemStatusResumed
			window.Schedules[i].ResumedAt = &resumedAt
		}
	}
	return window, nil
}

func fakePausedMaintenanceWindow(updateID string) *MaintenanceWindow {
	pausedAt := fixedNow()
	return &MaintenanceWindow{
		SchemaVersion: MaintenanceWindowSchemaVersion,
		WindowID:      "window_test",
		UpdateID:      updateID,
		Status:        MaintenanceWindowStatusPaused,
		StartedAt:     fixedNow(),
		PausePolicy:   MaintenancePausePolicy{PauseSchedules: true},
		Schedules: []MaintenancePausedItem{{
			Kind:           MaintenanceItemKindSchedule,
			Ref:            "schedule_test",
			DisplayName:    "Schedule Test",
			PreviousStatus: "active",
			PausedStatus:   "paused",
			PauseStatus:    MaintenanceItemStatusPaused,
			PausedAt:       &pausedAt,
		}},
	}
}

func TestFlakeArgument(t *testing.T) {
	release := "/srv/loom/releases/test"
	cases := []struct {
		output string
		want   string
	}{
		{"", release + "#loom-main"},
		{".#loom-main", release + "#loom-main"},
		{"#loom-main", release + "#loom-main"},
		{"github:owner/repo#loom-main", "github:owner/repo#loom-main"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("output=%q", tc.output), func(t *testing.T) {
			if got := flakeArgument(release, tc.output); got != tc.want {
				t.Fatalf("flakeArgument = %q, want %q", got, tc.want)
			}
		})
	}
}
