package loomcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/nodeprofiles"
	"loom.local/loom/internal/setup"
)

func TestSetupPlanJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"setup", "plan",
		"--kind", "workspace",
		"--role", "primary_workspace",
		"--runtime-class", "workspace_full",
		"--node-key", "macbook",
		"--display-name", "MacBook",
		"--main-url", "http://10.44.0.2:8080",
		"--home-dir", filepath.Join(t.TempDir(), "home"),
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup plan returned error: %v stderr=%s", err, stderr.String())
	}
	var plan setup.SetupPlan
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("setup plan output was not JSON: %v output=%s", err, stdout.String())
	}
	if plan.Profile.AuthorityProfileKey != nodeprofiles.AuthorityPrimaryWorkspaceDefault {
		t.Fatalf("authority = %q", plan.Profile.AuthorityProfileKey)
	}
	if plan.PlanHash == "" || len(plan.Steps) == 0 {
		t.Fatalf("plan missing hash or steps: %#v", plan)
	}
}

func TestSetupPlanHumanOutputContainsProfilesAndPaths(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	home := filepath.Join(t.TempDir(), "home")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"setup", "plan",
		"--kind", "workspace",
		"--node-key", "macbook",
		"--main-url", "http://10.44.0.2:8080",
		"--home-dir", home,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup plan returned error: %v stderr=%s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"LOOM setup plan",
		"authority: primary_workspace_default",
		filepath.Join(home, box.DefaultRootDirName),
		filepath.Join(home, ".config", "loom", "install.yaml"),
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestSetupCleanupPlanReadsSavedMainInventoryWithoutMutation(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "config", "install.yaml")
	planPath := filepath.Join(root, "main-plan.json")

	planCommand := NewRootCommand()
	planStdout := &bytes.Buffer{}
	planStderr := &bytes.Buffer{}
	planCommand.SetOut(planStdout)
	planCommand.SetErr(planStderr)
	args := append([]string{"--json", "setup", "plan"}, testSetupApplyMainArgs(root, manifest)...)
	planCommand.SetArgs(args)
	if err := planCommand.Execute(); err != nil {
		t.Fatalf("setup plan returned error: %v stderr=%s", err, planStderr.String())
	}
	var saved setup.SetupPlan
	if err := json.Unmarshal(planStdout.Bytes(), &saved); err != nil {
		t.Fatalf("decode setup plan: %v", err)
	}
	if saved.RetiredIntakeCleanup == nil || saved.RetiredIntakeCleanup.PlanDigest == "" {
		t.Fatalf("main setup plan omitted retired intake inventory: %#v", saved.RetiredIntakeCleanup)
	}
	if err := os.WriteFile(planPath, planStdout.Bytes(), 0o600); err != nil {
		t.Fatalf("write saved setup plan fixture: %v", err)
	}

	cleanupCommand := NewRootCommand()
	cleanupStdout := &bytes.Buffer{}
	cleanupStderr := &bytes.Buffer{}
	cleanupCommand.SetOut(cleanupStdout)
	cleanupCommand.SetErr(cleanupStderr)
	cleanupCommand.SetArgs([]string{"--json", "setup", "cleanup", "plan", "--plan", planPath})
	if err := cleanupCommand.Execute(); err != nil {
		t.Fatalf("setup cleanup plan returned error: %v stderr=%s", err, cleanupStderr.String())
	}
	var inventory setup.RetiredIntakeCleanupPlan
	if err := json.Unmarshal(cleanupStdout.Bytes(), &inventory); err != nil {
		t.Fatalf("decode cleanup inventory: %v output=%s", err, cleanupStdout.String())
	}
	if inventory.PlanDigest != saved.RetiredIntakeCleanup.PlanDigest || inventory.Summary.Total == 0 {
		t.Fatalf("cleanup plan did not preserve saved inventory: %#v", inventory)
	}
	if _, err := os.Stat(saved.Paths.BoxPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup plan mutated the missing Box root: %v", err)
	}
}

func TestSetupManifestPathCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	home := filepath.Join(t.TempDir(), "home")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"setup", "manifest", "path", "--install-mode", "user", "--home-dir", home})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("manifest path returned error: %v stderr=%s", err, stderr.String())
	}
	want := filepath.Join(home, ".config", "loom", "install.yaml")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("path = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestSetupManifestPathCommandLaunchdServiceUsesHomeManifest(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	home := filepath.Join(t.TempDir(), "home")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"setup", "manifest", "path",
		"--install-mode", "service",
		"--service-manager", "launchd",
		"--home-dir", home,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("manifest path returned error: %v stderr=%s", err, stderr.String())
	}
	want := filepath.Join(home, ".config", "loom", "install.yaml")
	if strings.TrimSpace(stdout.String()) != want {
		t.Fatalf("path = %q, want %q", strings.TrimSpace(stdout.String()), want)
	}
}

func TestSetupManifestInspectMissing(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"setup", "manifest", "inspect", "--manifest", missing})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("manifest inspect returned error: %v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No setup manifest found at "+missing) {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
}

func TestSetupStatusJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "setup", "status", "--manifest", missing})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup status returned error: %v stderr=%s", err, stderr.String())
	}
	var status setup.SetupStatus
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("status output was not JSON: %v output=%s", err, stdout.String())
	}
	if status.Summary.Status != setup.SummaryNotInstalled {
		t.Fatalf("summary = %q", status.Summary.Status)
	}
}

func TestSetupDoctorJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "setup", "doctor", "--manifest", missing})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup doctor returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var report setup.DoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("doctor output was not JSON: %v output=%s", err, stdout.String())
	}
	if len(report.Findings) == 0 || report.Findings[0].Code != "setup.manifest.missing" {
		t.Fatalf("findings = %#v", report.Findings)
	}
}

func TestSetupRepairDryRunCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"setup", "repair", "--dry-run", "--fix", "all-safe", "--manifest", missing})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup repair dry-run returned error: %v stderr=%s", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "LOOM setup repair") || !strings.Contains(output, "would_write") {
		t.Fatalf("unexpected output:\n%s", output)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write manifest, stat err=%v", err)
	}
}

func TestSetupRepairRefusesMutationWithoutYes(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"setup", "repair", "--fix", "rewrite-redacted-manifest", "--manifest", missing})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected repair mutation without --yes to fail")
	}
	if !strings.Contains(stdout.String(), "refused") {
		t.Fatalf("expected refusal output, got:\n%s", stdout.String())
	}
}

func TestSetupApplyJSONErrorRendersMessage(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	missing := filepath.Join(t.TempDir(), "missing-plan.json")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--json", "setup", "apply", "--plan", missing, "--dry-run"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected setup apply with a missing plan to fail")
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Summary string `json:"summary"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("setup apply error output was not JSON: %v output=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if envelope.OK || envelope.Error.Code != "setup.apply.failed" || !strings.Contains(envelope.Error.Summary, "read setup plan") {
		t.Fatalf("unexpected error envelope: %#v output=%s", envelope, stdout.String())
	}
}

func TestSetupApplyDryRunJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	manifest := filepath.Join(root, "config", "install.yaml")
	args := append([]string{"--json", "setup", "apply"}, testSetupApplyMainArgs(root, manifest)...)
	args = append(args, "--dry-run")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup apply dry-run returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("setup apply output was not JSON: %v output=%s", err, stdout.String())
	}
	if !result.DryRun || len(result.Changed) == 0 {
		t.Fatalf("dry-run result missing preview changes: %#v", result)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write manifest, stat err=%v", err)
	}
}

func TestSetupApplyMainJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	manifest := filepath.Join(root, "config", "install.yaml")
	args := append([]string{"--json", "setup", "apply"}, testSetupApplyMainArgs(root, manifest)...)
	args = append(args, "--yes")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs(args)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("setup apply returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("setup apply output was not JSON: %v output=%s", err, stdout.String())
	}
	if result.Manifest.NodeKind != "main" || result.Manifest.BoxProfile != "main" {
		t.Fatalf("manifest summary unexpected: %#v", result.Manifest)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Fatalf("manifest was not written: %v", err)
	}
	env, err := os.ReadFile(filepath.Join(root, "config", "loom.env"))
	if err != nil {
		t.Fatalf("env file was not written: %v", err)
	}
	if !strings.Contains(string(env), "LOOM_BOX_PATH="+filepath.Join(root, "home", "loomadmin", "LOOM Box")) {
		t.Fatalf("env missing Box path:\n%s", string(env))
	}
}

func TestSetupApplyWorkspaceJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	home := filepath.Join(root, "home", "loomadmin")
	manifest := filepath.Join(root, "config", "install.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"setup", "apply",
		"--kind", "workspace",
		"--role", "primary_workspace",
		"--runtime-class", "workspace_full",
		"--node-key", "macbook",
		"--display-name", "MacBook",
		"--main-url", "http://10.44.0.2:8080",
		"--install-mode", "user",
		"--service-manager", "none",
		"--home-dir", home,
		"--user-name", "loomadmin",
		"--config-dir", filepath.Join(root, "config"),
		"--data-dir", filepath.Join(root, "data"),
		"--service-root", filepath.Join(root, "service"),
		"--storage-root", filepath.Join(root, "service", "storage"),
		"--imports-root", filepath.Join(root, "service", "storage", "imports"),
		"--user-backups-root", filepath.Join(root, "service", "storage", "backups"),
		"--archive-root", filepath.Join(root, "service", "storage", "archive"),
		"--generated-root", filepath.Join(root, "data", "generated"),
		"--box-state-root", filepath.Join(root, "data", "box-state"),
		"--state-dir", filepath.Join(root, "state"),
		"--log-dir", filepath.Join(root, "logs"),
		"--box-path", filepath.Join(home, "LOOM Box"),
		"--box-profile", "workspace",
		"--manifest", manifest,
		"--yes",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace setup apply returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("workspace setup apply output was not JSON: %v output=%s", err, stdout.String())
	}
	if result.Manifest.NodeKind != "workspace" || result.Manifest.Enrollment.Status != "skipped" {
		t.Fatalf("workspace manifest unexpected: %#v", result.Manifest)
	}
	if _, err := os.Stat(result.Manifest.NodeAgentConfigPath); err != nil {
		t.Fatalf("node-agent config was not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "data", "box-state")); err != nil {
		t.Fatalf("workspace node-owned Box runtime root was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "LOOM Box", ".loom", "state")); !os.IsNotExist(err) {
		t.Fatalf("workspace setup created visible Box runtime state: %v", err)
	}
}

func TestSetupApplyWorkspaceEnrollDryRunJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	home := filepath.Join(root, "home", "loomadmin")
	manifest := filepath.Join(root, "config", "install.yaml")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"setup", "apply",
		"--kind", "workspace",
		"--node-key", "macbook",
		"--display-name", "MacBook",
		"--main-url", "http://10.44.0.2:8080",
		"--install-mode", "user",
		"--service-manager", "none",
		"--home-dir", home,
		"--config-dir", filepath.Join(root, "config"),
		"--data-dir", filepath.Join(root, "data"),
		"--service-root", filepath.Join(root, "service"),
		"--storage-root", filepath.Join(root, "service", "storage"),
		"--imports-root", filepath.Join(root, "service", "storage", "imports"),
		"--user-backups-root", filepath.Join(root, "service", "storage", "backups"),
		"--archive-root", filepath.Join(root, "service", "storage", "archive"),
		"--generated-root", filepath.Join(root, "data", "generated"),
		"--box-state-root", filepath.Join(root, "data", "box-state"),
		"--state-dir", filepath.Join(root, "state"),
		"--log-dir", filepath.Join(root, "logs"),
		"--box-path", filepath.Join(home, "LOOM Box"),
		"--manifest", manifest,
		"--enroll",
		"--dry-run",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace setup apply enroll dry-run returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("workspace setup apply enroll dry-run output was not JSON: %v output=%s", err, stdout.String())
	}
	if !result.DryRun || result.Manifest.Enrollment.Status != "not_started" {
		t.Fatalf("dry-run enrollment manifest unexpected: %#v", result)
	}
	found := false
	for _, change := range result.Changed {
		if change.ID == "run_enrollment_flow" && change.Status == setup.ApplyStatusWouldChange {
			found = true
		}
	}
	if !found {
		t.Fatalf("dry-run did not preview enrollment flow: %#v", result.Changed)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not write manifest, stat err=%v", err)
	}
}

func TestSetupWorkspaceInstallDryRunJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	home := filepath.Join(root, "home", "leonardo")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"setup", "workspace", "install",
		"--node-key", "macbook",
		"--display-name", "MacBook Primary Workspace",
		"--main-url", "http://10.44.0.2:8080",
		"--home-dir", home,
		"--source-path", root,
		"--release-id", "release-test",
		"--dry-run",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace install dry-run returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.WorkspaceInstallResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("workspace install output was not JSON: %v output=%s", err, stdout.String())
	}
	if result.Status != "dry_run" || !result.DryRun {
		t.Fatalf("dry-run status unexpected: %#v", result)
	}
	if result.Plan.Release.ReleasePath != filepath.Join(home, ".local", "share", "loom", "releases", "release-test") {
		t.Fatalf("release path = %q", result.Plan.Release.ReleasePath)
	}
	if result.Plan.Setup.Spec.ServiceManager != setup.ServiceManagerLaunchd || result.Plan.Setup.Spec.RunEnrollment != true {
		t.Fatalf("setup spec unexpected: %#v", result.Plan.Setup.Spec)
	}
	foundReleaseBuild := false
	for _, change := range result.Release.Changed {
		if change.ID == "build_loom-node-agent" && change.Status == setup.ApplyStatusWouldChange {
			foundReleaseBuild = true
		}
	}
	if !foundReleaseBuild {
		t.Fatalf("release dry-run did not preview node-agent build: %#v", result.Release.Changed)
	}
	if _, err := os.Stat(filepath.Join(home, ".local")); !os.IsNotExist(err) {
		t.Fatalf("dry-run should not create local install dirs, stat err=%v", err)
	}
}

func TestSetupApplyHardwareJSONCommand(t *testing.T) {
	cmd := NewRootCommand()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	root := t.TempDir()
	home := filepath.Join(root, "home", "sensor")
	manifest := filepath.Join(root, "config", "install.yaml")
	safeRoot := filepath.Join(root, "sensor-data")
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{
		"--json",
		"setup", "apply",
		"--kind", "hardware",
		"--role", "capability_node",
		"--runtime-class", "hardware_agent",
		"--node-key", "sensor",
		"--display-name", "Sensor",
		"--main-url", "http://10.44.0.2:8080",
		"--install-mode", "user",
		"--service-manager", "none",
		"--home-dir", home,
		"--user-name", "sensor",
		"--config-dir", filepath.Join(root, "config"),
		"--data-dir", filepath.Join(root, "data"),
		"--state-dir", filepath.Join(root, "state"),
		"--log-dir", filepath.Join(root, "logs"),
		"--safe-root", "sensor=" + safeRoot + ",mode=read_write",
		"--manifest", manifest,
		"--yes",
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("hardware setup apply returned error: %v stderr=%s stdout=%s", err, stderr.String(), stdout.String())
	}
	var result setup.ApplyResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("hardware setup apply output was not JSON: %v output=%s", err, stdout.String())
	}
	if result.Manifest.NodeKind != "hardware" || result.Manifest.BoxPath != "" || len(result.Manifest.SafeRoots) != 1 {
		t.Fatalf("hardware manifest unexpected: %#v", result.Manifest)
	}
	if _, err := os.Stat(result.Manifest.NodeAgentConfigPath); err != nil {
		t.Fatalf("hardware node-agent config was not written: %v", err)
	}
}

func testSetupApplyMainArgs(root string, manifest string) []string {
	home := filepath.Join(root, "home", "loomadmin")
	return []string{
		"--kind", "main",
		"--role", "main",
		"--runtime-class", "main_full",
		"--node-key", "main",
		"--display-name", "Main",
		"--install-mode", "user",
		"--service-manager", "none",
		"--home-dir", home,
		"--user-name", "loomadmin",
		"--config-dir", filepath.Join(root, "config"),
		"--data-dir", filepath.Join(root, "data"),
		"--service-root", filepath.Join(root, "service"),
		"--storage-root", filepath.Join(root, "service", "storage"),
		"--imports-root", filepath.Join(root, "service", "storage", "imports"),
		"--user-backups-root", filepath.Join(root, "service", "storage", "backups"),
		"--archive-root", filepath.Join(root, "service", "storage", "archive"),
		"--generated-root", filepath.Join(root, "data", "generated"),
		"--box-state-root", filepath.Join(root, "data", "box-state"),
		"--state-dir", filepath.Join(root, "state"),
		"--log-dir", filepath.Join(root, "logs"),
		"--object-store", filepath.Join(root, "object-store"),
		"--socket-path", filepath.Join(root, "run", "loomd.sock"),
		"--box-path", filepath.Join(home, "LOOM Box"),
		"--box-profile", "main",
		"--manifest", manifest,
	}
}
