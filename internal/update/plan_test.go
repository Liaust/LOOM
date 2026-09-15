package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPlanRefusesMode0700ReleaseRoot(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatalf("chmod target: %v", err)
	}

	_, err := Plan(context.Background(), PlannerInput{Spec: UpdateSpec{
		ReleasePath: target, ActivePath: active,
		ActiveMigrationsDir:     filepath.Join(active, "migrations"),
		CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "update"),
	}, Now: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "mode is 0700, want 0755") {
		t.Fatalf("expected exact-mode refusal, got %v", err)
	}
}

func TestPlanRefusesSymlinkReleaseRoot(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	realTarget := filepath.Join(root, "real-target")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, realTarget, "00036_current.sql")
	if err := os.Symlink(realTarget, target); err != nil {
		t.Fatalf("symlink target: %v", err)
	}

	_, err := Plan(context.Background(), PlannerInput{Spec: UpdateSpec{
		ReleasePath: target, ActivePath: active,
		ActiveMigrationsDir:     filepath.Join(active, "migrations"),
		CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "update"),
	}, Now: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
}

func TestPlanRefusesReleaseManifestTargetMismatch(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	otherTarget := filepath.Join(root, "other-target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	writeMigration(t, otherTarget, "00036_current.sql")
	if err := WriteReleaseManifest(filepath.Join(target, DefaultReleaseManifestFileYAML), ReleaseManifest{
		SourcePath: otherTarget,
	}); err != nil {
		t.Fatalf("WriteReleaseManifest: %v", err)
	}

	_, err := Plan(context.Background(), PlannerInput{Spec: UpdateSpec{
		ReleasePath: target, ActivePath: active,
		ActiveMigrationsDir:     filepath.Join(active, "migrations"),
		CurrentMigrationVersion: int64Ptr(36), StateDir: filepath.Join(root, "update"),
	}, Now: fixedNow})
	if err == nil || !strings.Contains(err.Error(), "does not match release root") {
		t.Fatalf("expected manifest target mismatch, got %v", err)
	}
}

func TestPlanMarksRestoreRequiredWhenTargetMigrationsAhead(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	writeMigration(t, target, "00037_next.sql")
	current := int64(36)

	plan, err := Plan(context.Background(), PlannerInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              active,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: &current,
			StateDir:                filepath.Join(root, "update"),
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if !plan.Migrations.WillApply || plan.Migrations.Pending != 1 {
		t.Fatalf("migration plan = %#v, want one pending migration", plan.Migrations)
	}
	if plan.Rollback.Class != RollbackClassRestoreRequired || !plan.Rollback.RestoreRequired {
		t.Fatalf("rollback = %#v, want restore required", plan.Rollback)
	}
	if plan.Status != PlanStatusReady && plan.Status != PlanStatusDegraded {
		t.Fatalf("plan status = %q", plan.Status)
	}
}

func TestPlanMarksServiceOnlyRollbackWhenNoMigrationsAhead(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	current := int64(36)

	plan, err := Plan(context.Background(), PlannerInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              active,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: &current,
			StateDir:                filepath.Join(root, "update"),
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Migrations.WillApply || plan.Migrations.Pending != 0 {
		t.Fatalf("migration plan = %#v, want no pending migrations", plan.Migrations)
	}
	if plan.Rollback.Class != RollbackClassServiceOnly || !plan.Rollback.ServiceOnlyPossible {
		t.Fatalf("rollback = %#v, want service-only rollback", plan.Rollback)
	}
}

func TestPlanUsesReleaseManifestMetadata(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	if err := WriteReleaseManifest(filepath.Join(target, DefaultReleaseManifestFileYAML), ReleaseManifest{
		ReleaseID:   "release_manual",
		Version:     "0.5.1-test",
		Commit:      "commit_test",
		FlakeOutput: ".#custom-main",
		BackupScope: &ReleaseBackupScope{
			SchemaVersion: ReleaseBackupScopeSchemaVersion,
			Class:         ReleaseBackupScopeOperationalOnly,
		},
		Metadata: map[string]any{
			"token": "secret",
		},
	}); err != nil {
		t.Fatalf("WriteReleaseManifest: %v", err)
	}
	current := int64(36)

	plan, err := Plan(context.Background(), PlannerInput{
		Spec: UpdateSpec{
			ReleasePath:             target,
			ActivePath:              active,
			ActiveMigrationsDir:     filepath.Join(active, "migrations"),
			CurrentMigrationVersion: &current,
			StateDir:                filepath.Join(root, "update"),
			FlakeOutput:             "",
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Target.ReleaseID != "release_manual" {
		t.Fatalf("target release id = %q", plan.Target.ReleaseID)
	}
	if plan.Nix.TargetFlakeOutput != ".#custom-main" {
		t.Fatalf("flake output = %q", plan.Nix.TargetFlakeOutput)
	}
	if !plan.BackupScope.OperationalPackageAllowed || plan.BackupScope.RequiredBackup != UpdateBackupRequirementOperational {
		t.Fatalf("backup scope = %#v, want verified operational-package compatibility", plan.BackupScope)
	}
}

func TestPlanBackupScopeDeclarationFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		declaration *ReleaseBackupScope
		wantStatus  string
		wantBackup  string
		wantAllowed bool
	}{
		{name: "absent", wantStatus: BackupScopeDeclarationAbsent, wantBackup: UpdateBackupRequirementComplete},
		{name: "malformed", declaration: &ReleaseBackupScope{SchemaVersion: ReleaseBackupScopeSchemaVersion}, wantStatus: BackupScopeDeclarationMalformed, wantBackup: UpdateBackupRequirementComplete},
		{name: "unknown schema", declaration: &ReleaseBackupScope{SchemaVersion: "loom.release.backup_scope.v2", Class: ReleaseBackupScopeOperationalOnly}, wantStatus: BackupScopeDeclarationUnknownSchema, wantBackup: UpdateBackupRequirementComplete},
		{name: "unknown class", declaration: &ReleaseBackupScope{SchemaVersion: ReleaseBackupScopeSchemaVersion, Class: "future_scope"}, wantStatus: BackupScopeDeclarationUnknownClass, wantBackup: UpdateBackupRequirementComplete},
		{name: "operational only", declaration: &ReleaseBackupScope{SchemaVersion: ReleaseBackupScopeSchemaVersion, Class: ReleaseBackupScopeOperationalOnly}, wantStatus: BackupScopeDeclarationValid, wantBackup: UpdateBackupRequirementOperational, wantAllowed: true},
		{name: "canonical user data", declaration: &ReleaseBackupScope{SchemaVersion: ReleaseBackupScopeSchemaVersion, Class: ReleaseBackupScopeCanonicalUserData}, wantStatus: BackupScopeDeclarationValid, wantBackup: UpdateBackupRequirementComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := planFixtureWithBackupScope(t, test.declaration, false)
			if plan.BackupScope.DeclarationStatus != test.wantStatus || plan.BackupScope.RequiredBackup != test.wantBackup || plan.BackupScope.OperationalPackageAllowed != test.wantAllowed {
				t.Fatalf("backup scope = %#v", plan.BackupScope)
			}
			if got := operationalPackageCompatibleWithPlan(plan); got != test.wantAllowed {
				t.Fatalf("compatible = %t, want %t", got, test.wantAllowed)
			}
		})
	}
}

func TestPlanBackupScopeIsIndependentOfMigrationAndRebuildSteps(t *testing.T) {
	for _, pendingMigration := range []bool{false, true} {
		plan := planFixtureWithBackupScope(t, &ReleaseBackupScope{
			SchemaVersion: ReleaseBackupScopeSchemaVersion,
			Class:         ReleaseBackupScopeOperationalOnly,
		}, pendingMigration)
		if !plan.Nix.RebuildExpected || plan.Migrations.WillApply != pendingMigration {
			t.Fatalf("plan combination = nix:%t migration:%t, want nix:true migration:%t", plan.Nix.RebuildExpected, plan.Migrations.WillApply, pendingMigration)
		}
		if !operationalPackageCompatibleWithPlan(plan) {
			t.Fatalf("valid operational declaration rejected for migration=%t", pendingMigration)
		}
	}
	canonical := planFixtureWithBackupScope(t, &ReleaseBackupScope{
		SchemaVersion: ReleaseBackupScopeSchemaVersion,
		Class:         ReleaseBackupScopeCanonicalUserData,
	}, true)
	if operationalPackageCompatibleWithPlan(canonical) {
		t.Fatal("migration/rebuild steps overrode canonical-user-data declaration")
	}
}

func TestPlanIdentityStableAcrossPlannerClocks(t *testing.T) {
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	current := int64(36)
	spec := UpdateSpec{
		ReleasePath:             target,
		ActivePath:              active,
		ActiveMigrationsDir:     filepath.Join(active, "migrations"),
		CurrentMigrationVersion: &current,
		StateDir:                filepath.Join(root, "update"),
	}

	firstClock := time.Now()
	secondClock := firstClock.Add(37 * time.Minute).In(time.FixedZone("review-clock", 9*60*60+30*60))
	first, err := Plan(context.Background(), PlannerInput{Spec: spec, Now: func() time.Time { return firstClock }})
	if err != nil {
		t.Fatalf("first Plan returned error: %v", err)
	}
	second, err := Plan(context.Background(), PlannerInput{Spec: spec, Now: func() time.Time { return secondClock }})
	if err != nil {
		t.Fatalf("second Plan returned error: %v", err)
	}

	if !first.CreatedAt.Equal(firstClock) || first.CreatedAt.Location() != time.UTC {
		t.Fatalf("first created_at = %v, want truthful UTC %v", first.CreatedAt, firstClock.UTC())
	}
	if !second.CreatedAt.Equal(secondClock) || second.CreatedAt.Location() != time.UTC {
		t.Fatalf("second created_at = %v, want truthful UTC %v", second.CreatedAt, secondClock.UTC())
	}
	if first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("created_at values unexpectedly equal: %v", first.CreatedAt)
	}
	if first.PlanHash != second.PlanHash {
		t.Fatalf("plan hash changed across planner clocks: %s != %s", first.PlanHash, second.PlanHash)
	}
	if first.PlanID != second.PlanID {
		t.Fatalf("plan ID changed across planner clocks: %s != %s", first.PlanID, second.PlanID)
	}
}

func TestHashPlanIgnoresOnlyReceiptMetadata(t *testing.T) {
	base := planFixtureWithBackupScope(t, &ReleaseBackupScope{
		SchemaVersion: ReleaseBackupScopeSchemaVersion,
		Class:         ReleaseBackupScopeOperationalOnly,
	}, true)
	want := HashPlan(base)

	tests := []struct {
		name   string
		mutate func(*UpdatePlan)
	}{
		{name: "created at", mutate: func(plan *UpdatePlan) { plan.CreatedAt = time.Now().Add(24 * time.Hour) }},
		{name: "plan ID", mutate: func(plan *UpdatePlan) { plan.PlanID = "update_plan_review_copy" }},
		{name: "plan hash", mutate: func(plan *UpdatePlan) { plan.PlanHash = "sha256:review-copy" }},
		{name: "all receipt metadata", mutate: func(plan *UpdatePlan) {
			plan.CreatedAt = time.Now().In(time.FixedZone("receipt-copy", -7*60*60))
			plan.PlanID = "update_plan_receipt_copy"
			plan.PlanHash = "sha256:receipt-copy"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneUpdatePlan(t, base)
			test.mutate(&mutated)
			if got := HashPlan(mutated); got != want {
				t.Fatalf("HashPlan = %s, want %s", got, want)
			}
		})
	}
}

func TestHashPlanChangesForEverySemanticSection(t *testing.T) {
	base := planFixtureWithBackupScope(t, &ReleaseBackupScope{
		SchemaVersion: ReleaseBackupScopeSchemaVersion,
		Class:         ReleaseBackupScopeOperationalOnly,
	}, true)
	want := HashPlan(base)

	tests := []struct {
		name   string
		mutate func(*UpdatePlan)
	}{
		{name: "schema version", mutate: func(plan *UpdatePlan) { plan.SchemaVersion += ".changed" }},
		{name: "status", mutate: func(plan *UpdatePlan) { plan.Status = PlanStatusBlocked }},
		{name: "spec path", mutate: func(plan *UpdatePlan) { plan.Spec.StateDir += "-changed" }},
		{name: "active release", mutate: func(plan *UpdatePlan) { plan.Active.Commit = "active-commit-changed" }},
		{name: "target release", mutate: func(plan *UpdatePlan) { plan.Target.ReleaseID = "release_changed" }},
		{name: "target commit", mutate: func(plan *UpdatePlan) { plan.Target.Commit = "target-commit-changed" }},
		{name: "current migration", mutate: func(plan *UpdatePlan) { plan.Migrations.CurrentVersion++ }},
		{name: "migration plan", mutate: func(plan *UpdatePlan) { plan.Migrations.Pending++ }},
		{name: "nix plan", mutate: func(plan *UpdatePlan) { plan.Nix.TargetFlakeOutput = ".#changed" }},
		{name: "backup requirement", mutate: func(plan *UpdatePlan) { plan.Backup.MinimumCommand += " --changed" }},
		{name: "backup scope", mutate: func(plan *UpdatePlan) { plan.BackupScope.RequiredBackup = UpdateBackupRequirementComplete }},
		{name: "service impact", mutate: func(plan *UpdatePlan) {
			plan.ServiceImpact.SchedulerPauseRecommended = !plan.ServiceImpact.SchedulerPauseRecommended
		}},
		{name: "rollback", mutate: func(plan *UpdatePlan) { plan.Rollback.Class = RollbackClassManualOnly }},
		{name: "steps", mutate: func(plan *UpdatePlan) { plan.Steps[0].Title += " changed" }},
		{name: "diagnostics", mutate: func(plan *UpdatePlan) { plan.Diagnostics[0].Code += ".changed" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := cloneUpdatePlan(t, base)
			test.mutate(&mutated)
			if got := HashPlan(mutated); got == want {
				t.Fatalf("HashPlan did not change from %s", want)
			}
		})
	}
}

func TestHashPlanIdentityProjectionIncludesEverySemanticField(t *testing.T) {
	planType := reflect.TypeOf(UpdatePlan{})
	identityType := reflect.TypeOf(updatePlanIdentity{})
	excluded := map[string]bool{"CreatedAt": true, "PlanID": true, "PlanHash": true}

	for index := 0; index < planType.NumField(); index++ {
		planField := planType.Field(index)
		identityField, present := identityType.FieldByName(planField.Name)
		if excluded[planField.Name] {
			if present {
				t.Fatalf("receipt metadata %s is present in updatePlanIdentity", planField.Name)
			}
			continue
		}
		if !present {
			t.Fatalf("semantic UpdatePlan field %s is missing from updatePlanIdentity", planField.Name)
		}
		if identityField.Type != planField.Type || identityField.Tag.Get("json") != planField.Tag.Get("json") {
			t.Fatalf("identity field %s = (%s, %q), want (%s, %q)", planField.Name, identityField.Type, identityField.Tag.Get("json"), planField.Type, planField.Tag.Get("json"))
		}
	}
	if want := planType.NumField() - len(excluded); identityType.NumField() != want {
		t.Fatalf("updatePlanIdentity fields = %d, want %d", identityType.NumField(), want)
	}
}

func TestHashPlanStableConcurrent(t *testing.T) {
	plan := planFixtureWithBackupScope(t, &ReleaseBackupScope{
		SchemaVersion: ReleaseBackupScopeSchemaVersion,
		Class:         ReleaseBackupScopeOperationalOnly,
	}, true)
	want := HashPlan(plan)

	const workers = 64
	errors := make(chan string, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			copy := plan
			copy.CreatedAt = time.Now().Add(time.Duration(index) * time.Second)
			copy.PlanID = "receipt-copy"
			copy.PlanHash = "sha256:receipt-copy"
			if got := HashPlan(copy); got != want {
				errors <- got
			}
		}(index)
	}
	wait.Wait()
	close(errors)
	for got := range errors {
		t.Fatalf("concurrent HashPlan = %s, want %s", got, want)
	}
}

func TestPlanIdentityReceiptMetadataRemainsInJSON(t *testing.T) {
	plan := planFixtureWithBackupScope(t, &ReleaseBackupScope{
		SchemaVersion: ReleaseBackupScopeSchemaVersion,
		Class:         ReleaseBackupScopeOperationalOnly,
	}, false)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"created_at", "plan_id", "plan_hash"} {
		if len(payload[key]) == 0 {
			t.Fatalf("plan JSON is missing %q: %s", key, raw)
		}
	}
	var createdAt time.Time
	if err := json.Unmarshal(payload["created_at"], &createdAt); err != nil {
		t.Fatalf("decode created_at: %v", err)
	}
	if !createdAt.Equal(plan.CreatedAt) || string(payload["plan_id"]) != `"`+plan.PlanID+`"` || string(payload["plan_hash"]) != `"`+plan.PlanHash+`"` {
		t.Fatalf("receipt metadata changed in JSON: %s", raw)
	}
}

func cloneUpdatePlan(t *testing.T, plan UpdatePlan) UpdatePlan {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var cloned UpdatePlan
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func planFixtureWithBackupScope(t *testing.T, declaration *ReleaseBackupScope, pendingMigration bool) UpdatePlan {
	t.Helper()
	root := t.TempDir()
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	writeMigration(t, active, "00036_current.sql")
	writeMigration(t, target, "00036_current.sql")
	if pendingMigration {
		writeMigration(t, target, "00037_next.sql")
	}
	if declaration != nil {
		if err := WriteReleaseManifest(filepath.Join(target, DefaultReleaseManifestFileYAML), ReleaseManifest{
			SourcePath:  target,
			BackupScope: declaration,
		}); err != nil {
			t.Fatal(err)
		}
	}
	current := int64(36)
	plan, err := Plan(context.Background(), PlannerInput{Spec: UpdateSpec{
		ReleasePath: target, ActivePath: active,
		ActiveMigrationsDir:     filepath.Join(active, "migrations"),
		CurrentMigrationVersion: &current, StateDir: filepath.Join(root, "update"),
	}, Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestStatusReportsMissingActiveManifest(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "update")
	status := Status(StatusInput{StateDir: stateDir, Now: fixedNow})
	if status.ActiveExists || status.Active != nil {
		t.Fatalf("active manifest should be missing: %#v", status)
	}
	if len(status.Diagnostics) == 0 || status.Diagnostics[0].Code != "update.active_manifest_missing" {
		t.Fatalf("diagnostics = %#v", status.Diagnostics)
	}
}

func writeMigration(t *testing.T, releasePath, name string) {
	t.Helper()
	dir := filepath.Join(releasePath, "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	content := "-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
}
