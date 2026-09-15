package loomcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/update"
)

func TestUpdateStatusJSONDoesNotRequireDaemon(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "update", "status", "--state-dir", filepath.Join(t.TempDir(), "update")})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("update status returned error: %v stderr=%s", err, stderr.String())
	}
	var payload struct {
		ActiveExists bool `json:"active_exists"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("status output was not JSON: %v output=%q", err, stdout.String())
	}
	if payload.ActiveExists {
		t.Fatal("fresh update status should not have active manifest")
	}
}

func TestUpdateMaintenanceStatusAllowsMissingActiveManifest(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "update")

	stdout, stderr, err := executeRootCommand("--json", "update", "maintenance", "status", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("update maintenance status returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	var payload struct {
		ActiveExists      bool             `json:"active_exists"`
		UpdateID          string           `json:"update_id"`
		ManifestPath      string           `json:"manifest_path"`
		MaintenanceWindow *json.RawMessage `json:"maintenance_window,omitempty"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("maintenance status output was not JSON: %v output=%q", err, stdout)
	}
	if payload.ActiveExists {
		t.Fatal("fresh update maintenance status should not have active manifest")
	}
	if payload.UpdateID != "" {
		t.Fatalf("update_id = %q, want empty", payload.UpdateID)
	}
	if payload.ManifestPath != update.ActiveManifestPath(stateDir) {
		t.Fatalf("manifest_path = %q, want %q", payload.ManifestPath, update.ActiveManifestPath(stateDir))
	}
	if payload.MaintenanceWindow != nil {
		t.Fatalf("maintenance_window = %v, want omitted", payload.MaintenanceWindow)
	}

	stdout, stderr, err = executeRootCommand("update", "maintenance", "status", "--state-dir", stateDir)
	if err != nil {
		t.Fatalf("text update maintenance status returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	if !strings.Contains(stdout, "Status: none") {
		t.Fatalf("text status missing none state:\n%s", stdout)
	}
}

func TestUpdatePlanJSONDoesNotRequireDaemon(t *testing.T) {
	releasePath := t.TempDir()
	migrationsDir := filepath.Join(releasePath, "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migrationsDir, "00036_current.sql"), []byte("-- +goose Up\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"update", "plan",
		"--release-path", releasePath,
		"--active-path", releasePath,
		"--active-migrations-dir", migrationsDir,
		"--current-migration", "36",
		"--state-dir", filepath.Join(t.TempDir(), "update"),
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("update plan returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var payload struct {
		SchemaVersion string `json:"schema_version"`
		Migrations    struct {
			Pending int64 `json:"pending"`
		} `json:"migrations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("plan output was not JSON: %v output=%q", err, stdout.String())
	}
	if payload.SchemaVersion == "" || payload.Migrations.Pending != 0 {
		t.Fatalf("unexpected plan payload: %#v", payload)
	}
}

func TestUpdateApplyWithoutYesDoesNotRequireMaintenanceCoordinator(t *testing.T) {
	t.Setenv("LOOM_CONFIG_FILE", "")
	t.Setenv("LOOM_DB_URL", "")

	root := t.TempDir()
	releasePath := filepath.Join(root, "release")
	migrationsDir := filepath.Join(releasePath, "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migrationsDir, "00036_current.sql"), []byte("-- +goose Up\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"update", "apply",
		"--release-path", releasePath,
		"--active-path", releasePath,
		"--active-migrations-dir", migrationsDir,
		"--current-migration", "36",
		"--state-dir", filepath.Join(root, "update"),
	})

	if err := cmd.Execute(); err == nil {
		t.Fatal("update apply without --yes returned nil error")
	}
	var payload update.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("apply output was not JSON: %v output=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !payload.Refused || payload.Refusal != "update apply requires --yes" {
		t.Fatalf("unexpected refusal payload: %#v stderr=%q", payload, stderr.String())
	}
	if strings.Contains(stdout.String(), "maintenance_coordinator_unavailable") || strings.Contains(stderr.String(), "maintenance_coordinator_unavailable") {
		t.Fatalf("unconfirmed update should not try maintenance coordinator: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestUpdateBackupVerifierUsesConfiguredLoomdClient(t *testing.T) {
	const (
		backupPath = "/var/lib/loom/backups/main/accepted-v09"
		backupRef  = "maintenance_operation_accepted"
		corrID     = "corr_update_backup_verifier"
	)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/maintenance/backup/verify" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-LOOM-Correlation-ID"); got != corrID {
			t.Fatalf("correlation header = %q, want %q", got, corrID)
		}
		var input maintenance.BackupVerifyInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatalf("decode input: %v", err)
		}
		if input.BackupRef != backupRef {
			t.Fatalf("backup_ref = %q, want %q", input.BackupRef, backupRef)
		}
		response.WriteJSON(w, http.StatusOK, response.Success(corrID, maintenance.BackupVerification{
			Status:            maintenance.VerificationSucceeded,
			BackupOperationID: backupRef,
			BackupDir:         backupPath,
		}))
	}))
	defer server.Close()
	client, err := localclient.NewHTTP(server.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}

	verification, err := updateBackupVerifier(client, corrID)(context.Background(), update.BackupVerificationInput{
		BackupPath: backupPath,
		BackupRef:  backupRef,
	})
	if err != nil {
		t.Fatalf("backup verifier: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if verification.BackupOperationID != backupRef || verification.BackupDir != backupPath || verification.Status != maintenance.VerificationSucceeded {
		t.Fatalf("verification = %#v", verification)
	}
}

func TestUpdateReleaseStageCommandWritesJSONResult(t *testing.T) {
	root, source := updateReleaseStageFixture(t)
	stage := filepath.Join(root, "stage")
	target := "/srv/loom/releases/v-test-cli"

	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"update", "release", "stage",
		"--source-path", source,
		"--stage-path", stage,
		"--target-path", target,
		"--release-id", "release_test_cli",
		"--version", "0.9.7-test",
		"--commit", "abc123",
		"--flake", ".#hardware-main",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("update release stage returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var payload update.ReleaseStageResult
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stage output was not JSON: %v output=%q", err, stdout.String())
	}
	if payload.Status != update.ReleaseStageStatusStaged || payload.TargetPath != target {
		t.Fatalf("unexpected stage payload: %#v", payload)
	}
	manifest, err := update.ReadReleaseManifest(filepath.Join(stage, update.DefaultReleaseManifestFileYAML))
	if err != nil {
		t.Fatalf("ReadReleaseManifest returned error: %v", err)
	}
	if manifest.SourcePath != target || manifest.MigrationsDir != filepath.Join(target, "migrations") {
		t.Fatalf("manifest did not use target paths: %#v", manifest)
	}
	if manifest.BackupScope != nil {
		t.Fatalf("absent --backup-scope must preserve fail-closed compatibility: %#v", manifest.BackupScope)
	}
}

func TestUpdateReleaseStageOperationalBackupScopeRoundTrips(t *testing.T) {
	root, source := updateReleaseStageFixture(t)
	stage := filepath.Join(root, "stage")
	target := "/srv/loom/releases/v-test-operational-scope"

	stdout, stderr, err := executeRootCommand(
		"--json", "update", "release", "stage",
		"--source-path", source,
		"--stage-path", stage,
		"--target-path", target,
		"--backup-scope", update.ReleaseBackupScopeOperationalOnly,
	)
	if err != nil {
		t.Fatalf("update release stage returned error: %v stderr=%s stdout=%s", err, stderr, stdout)
	}
	manifest, err := update.ReadReleaseManifest(filepath.Join(stage, update.DefaultReleaseManifestFileYAML))
	if err != nil {
		t.Fatalf("ReadReleaseManifest returned error: %v", err)
	}
	if manifest.BackupScope == nil || manifest.BackupScope.SchemaVersion != update.ReleaseBackupScopeSchemaVersion || manifest.BackupScope.Class != update.ReleaseBackupScopeOperationalOnly {
		t.Fatalf("operational backup scope did not round-trip: %#v", manifest.BackupScope)
	}
}

func TestUpdateReleaseStageInvalidBackupScopeWritesNoStage(t *testing.T) {
	root, source := updateReleaseStageFixture(t)
	stage := filepath.Join(root, "stage")

	stdout, stderr, err := executeRootCommand(
		"--json", "update", "release", "stage",
		"--source-path", source,
		"--stage-path", stage,
		"--target-path", "/srv/loom/releases/v-test-invalid-scope",
		"--backup-scope", "inferred_from_migrations",
	)
	if err == nil || !strings.Contains(err.Error(), "--backup-scope must be") {
		t.Fatalf("invalid scope error = %v, want exact scope refusal; stderr=%s stdout=%s", err, stderr, stdout)
	}
	if _, statErr := os.Lstat(stage); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid scope mutated stage path: %v", statErr)
	}
}

func TestReleaseBackupScopeFromFlagAcceptsOnlyDeclaredClasses(t *testing.T) {
	for _, class := range []string{update.ReleaseBackupScopeOperationalOnly, update.ReleaseBackupScopeCanonicalUserData} {
		scope, err := releaseBackupScopeFromFlag(class)
		if err != nil || scope == nil || scope.SchemaVersion != update.ReleaseBackupScopeSchemaVersion || scope.Class != class {
			t.Fatalf("scope %q = %#v, %v", class, scope, err)
		}
	}
	if scope, err := releaseBackupScopeFromFlag(""); err != nil || scope != nil {
		t.Fatalf("absent scope = %#v, %v", scope, err)
	}
}

func updateReleaseStageFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	migrationsDir := filepath.Join(source, "migrations")
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module example.local/release\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migrationsDir, "00001_init.sql"), []byte("-- +goose Up\nSELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	return root, source
}

func TestUpdateReleaseRetentionPlanAndApplyJSON(t *testing.T) {
	root := t.TempDir()
	releasesDir := filepath.Join(root, "releases")
	stateDir := filepath.Join(root, "update")
	activePath := filepath.Join(root, "current")
	old := time.Now().UTC().Add(-7 * 24 * time.Hour)
	makeRelease := func(name string) string {
		path := filepath.Join(releasesDir, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir release %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(path, "release.txt"), []byte(name), 0o644); err != nil {
			t.Fatalf("write release %s: %v", name, err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatalf("set release time %s: %v", name, err)
		}
		return path
	}
	current := makeRelease("current")
	rollback := makeRelease("rollback")
	candidate := makeRelease("candidate")
	if err := os.Symlink(current, activePath); err != nil {
		t.Fatalf("symlink current release: %v", err)
	}
	manifest := update.UpdateManifest{
		SchemaVersion: update.UpdateManifestSchemaVersion,
		Status:        update.UpdateStatusSucceeded,
		StartedAt:     time.Now().UTC().Add(-time.Hour),
		Active:        update.ReleaseState{Path: rollback},
		Target:        update.ReleaseState{Path: current},
		Rollback:      update.RollbackPlan{Class: update.RollbackClassServiceOnly},
	}
	if err := update.WriteUpdateManifest(update.ActiveManifestPath(stateDir), manifest); err != nil {
		t.Fatalf("write active update manifest: %v", err)
	}
	if err := update.WriteUpdateManifest(filepath.Join(update.HistoryDir(stateDir), "current.yaml"), manifest); err != nil {
		t.Fatalf("write history manifest: %v", err)
	}

	commonArgs := []string{
		"--releases-dir", releasesDir,
		"--active-path", activePath,
		"--state-dir", stateDir,
		"--keep-successful", "1",
		"--protect-younger-than", "1h",
	}
	planCommand := NewRootCommand()
	planOutput := &bytes.Buffer{}
	planCommand.SetOut(planOutput)
	planCommand.SetErr(&bytes.Buffer{})
	planCommand.SetArgs(append([]string{"--json", "update", "release", "retention", "plan"}, commonArgs...))
	if err := planCommand.Execute(); err != nil {
		t.Fatalf("retention plan returned error: %v output=%s", err, planOutput.String())
	}
	var plan update.ReleaseRetentionPlan
	if err := json.Unmarshal(planOutput.Bytes(), &plan); err != nil {
		t.Fatalf("retention plan was not JSON: %v output=%s", err, planOutput.String())
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		t.Fatalf("resolve candidate: %v", err)
	}
	if got := len(plan.Delete); got != 1 || plan.Delete[0].Path != resolvedCandidate {
		t.Fatalf("retention candidates = %#v", plan.Delete)
	}

	applyCommand := NewRootCommand()
	applyOutput := &bytes.Buffer{}
	applyCommand.SetOut(applyOutput)
	applyCommand.SetErr(&bytes.Buffer{})
	applyArgs := append([]string{"--json", "update", "release", "retention", "apply"}, commonArgs...)
	applyArgs = append(applyArgs, "--plan-hash", plan.PlanHash, "--yes")
	applyCommand.SetArgs(applyArgs)
	if err := applyCommand.Execute(); err != nil {
		t.Fatalf("retention apply returned error: %v output=%s", err, applyOutput.String())
	}
	var result update.ReleaseRetentionApplyResult
	if err := json.Unmarshal(applyOutput.Bytes(), &result); err != nil {
		t.Fatalf("retention apply was not JSON: %v output=%s", err, applyOutput.String())
	}
	if result.Status != update.UpdateStatusSucceeded || len(result.Deleted) != 1 {
		t.Fatalf("retention apply result = %#v", result)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("candidate still exists after apply: %v", err)
	}
	for _, protected := range []string{current, rollback} {
		if _, err := os.Stat(protected); err != nil {
			t.Fatalf("protected release missing after apply: %s: %v", protected, err)
		}
	}
}

func TestUpdateWorkspacePlanJSONDoesNotRequireDaemon(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	active := filepath.Join(root, "active")
	target := filepath.Join(root, "target")
	current := filepath.Join(home, ".local", "share", "loom", "current")
	for _, release := range []string{active, target} {
		for _, binary := range []string{"loom", "loom-node-agent"} {
			path := filepath.Join(release, "bin", binary)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir binary parent: %v", err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatalf("write binary: %v", err)
			}
		}
		if err := update.WriteReleaseManifest(filepath.Join(release, update.DefaultReleaseManifestFileYAML), update.ReleaseManifest{
			ReleaseID:  filepath.Base(release),
			Version:    "0.5.3-test",
			SourcePath: release,
		}); err != nil {
			t.Fatalf("WriteReleaseManifest: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		t.Fatalf("mkdir current parent: %v", err)
	}
	if err := os.Symlink(active, current); err != nil {
		t.Fatalf("symlink current: %v", err)
	}

	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"update", "workspace", "plan",
		"--release-path", target,
		"--home-dir", home,
		"--service-manager", "none",
		"--skip-service-restart",
		"--skip-health-check",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace update plan returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var payload update.WorkspaceUpdatePlan
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("workspace plan output was not JSON: %v output=%q", err, stdout.String())
	}
	if payload.SchemaVersion != update.WorkspaceUpdateSchemaVersion || payload.Backup.Required {
		t.Fatalf("unexpected workspace plan payload: %#v", payload)
	}
	for _, step := range payload.Steps {
		if step.ID == "nixos_rebuild" {
			t.Fatalf("workspace plan should not include nixos_rebuild: %#v", payload.Steps)
		}
	}
}

func TestUpdateMaintenancePeerFallbackDBURL(t *testing.T) {
	got, user, ok := updateMaintenancePeerFallbackDBURL("user=loom dbname=loom_main host=/run/postgresql sslmode=disable", "loomadmin")
	if !ok {
		t.Fatal("expected local peer DB URL fallback")
	}
	if user != "loomadmin" {
		t.Fatalf("fallback user = %q, want loomadmin", user)
	}
	want := "user=loomadmin dbname=loom_main host=/run/postgresql sslmode=disable"
	if got != want {
		t.Fatalf("fallback URL = %q, want %q", got, want)
	}
}

func TestUpdateMaintenancePeerFallbackDBURLRejectsPasswordURL(t *testing.T) {
	if got, _, ok := updateMaintenancePeerFallbackDBURL("user=loom password=secret dbname=loom_main host=/run/postgresql sslmode=disable", "loomadmin"); ok {
		t.Fatalf("password DB URL should not be rewritten, got %q", got)
	}
}

func TestUpdateMaintenancePeerFallbackDBURLRejectsTCPHost(t *testing.T) {
	if got, _, ok := updateMaintenancePeerFallbackDBURL("user=loom dbname=loom_main host=127.0.0.1 sslmode=disable", "loomadmin"); ok {
		t.Fatalf("TCP DB URL should not be rewritten, got %q", got)
	}
}

func TestIsPostgresPeerAuthFailure(t *testing.T) {
	if !isPostgresPeerAuthFailure(errors.New("failed to connect to `user=loom database=loom_main`: FATAL: Peer authentication failed for user \"loom\"")) {
		t.Fatal("expected peer auth failure to be detected")
	}
	if isPostgresPeerAuthFailure(errors.New("password authentication failed for user \"loom\"")) {
		t.Fatal("password auth failure should not be treated as peer auth")
	}
}
