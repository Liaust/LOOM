package update

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspacePlanSkipsProductionMainGates(t *testing.T) {
	root := t.TempDir()
	home, active, target, current := prepareWorkspaceUpdateReleases(t, root)

	plan, err := PlanWorkspaceUpdate(context.Background(), WorkspaceUpdatePlanInput{
		Spec: WorkspaceUpdateSpec{
			ReleasePath:        target,
			HomeDir:            home,
			ActivePath:         current,
			ServiceManager:     ServiceManagerNone,
			SkipServiceRestart: true,
			SkipHealthCheck:    true,
		},
		Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("PlanWorkspaceUpdate returned error: %v", err)
	}
	if plan.Backup.Required {
		t.Fatalf("workspace plan should not require main-node backup: %#v", plan.Backup)
	}
	for _, step := range plan.Steps {
		if strings.Contains(step.ID, "nixos") {
			t.Fatalf("workspace plan should not contain nixos rebuild step: %#v", plan.Steps)
		}
	}
	if plan.Rollback.Class != RollbackClassServiceOnly || plan.Rollback.RestoreRequired {
		t.Fatalf("rollback = %#v, want service-only", plan.Rollback)
	}
	if plan.Active.Path != active || plan.Target.Path != target {
		t.Fatalf("active/target mismatch: active=%q target=%q", plan.Active.Path, plan.Target.Path)
	}
}

func TestWorkspaceApplySwitchesReleaseRestartsLaunchdAndWritesManifests(t *testing.T) {
	root := t.TempDir()
	home, _, target, current := prepareWorkspaceUpdateReleases(t, root)
	stateDir := filepath.Join(home, ".local", "state", "loom", "update")
	runner := &fakeWorkspaceUpdateRunner{}

	result, err := ApplyWorkspaceUpdate(context.Background(), WorkspaceApplyInput{
		Spec: WorkspaceUpdateSpec{
			ReleasePath:      target,
			HomeDir:          home,
			ActivePath:       current,
			StateDir:         stateDir,
			MainHost:         "loom-main",
			NodeKey:          "macbook",
			LaunchAgentPlist: filepath.Join(home, "Library", "LaunchAgents", "local.loom.node-agent.plist"),
		},
		Yes:    true,
		Now:    fixedNow,
		Runner: runner.Run,
	})
	if err != nil {
		t.Fatalf("ApplyWorkspaceUpdate returned error: %v", err)
	}
	if result.Status != UpdateStatusSucceeded {
		t.Fatalf("status = %q, result=%#v", result.Status, result)
	}
	assertSymlinkTarget(t, current, target)
	assertSymlinkTarget(t, filepath.Join(home, ".local", "bin", "loom"), filepath.Join(current, "bin", "loom"))
	assertSymlinkTarget(t, filepath.Join(home, ".local", "bin", "loom-node-agent"), filepath.Join(current, "bin", "loom-node-agent"))
	if !runner.saw("launchctl print") || !runner.saw("launchctl enable") || !runner.saw("launchctl kickstart") {
		t.Fatalf("launchd commands missing: %#v", runner.calls)
	}
	if runner.saw("launchctl bootout") || runner.saw("launchctl bootstrap") {
		t.Fatalf("loaded LaunchAgent update should avoid bootout/bootstrap: %#v", runner.calls)
	}
	if !runner.saw("loom-node-agent --json status") || !runner.saw("loom-node-agent --json heartbeat --once") || !runner.saw("ssh loom-main loom --json node health macbook") {
		t.Fatalf("health commands missing: %#v", runner.calls)
	}
	if _, err := os.Stat(ActiveManifestPath(stateDir)); err != nil {
		t.Fatalf("active manifest missing: %v", err)
	}
	if _, err := os.Stat(result.HistoryPath); err != nil {
		t.Fatalf("history manifest missing: %v", err)
	}
	manifest, err := ReadUpdateManifest(ActiveManifestPath(stateDir))
	if err != nil {
		t.Fatalf("ReadUpdateManifest: %v", err)
	}
	if metadataString(manifest.Metadata, "target_kind") != "workspace" || metadataString(manifest.Metadata, "nixos_rebuild") != "skipped" {
		t.Fatalf("workspace metadata missing: %#v", manifest.Metadata)
	}
}

func TestWorkspaceLaunchdStartKickstartsLoadedLaunchAgent(t *testing.T) {
	runner := &fakeLaunchdRecoveryRunner{}
	spec := WorkspaceUpdateSpec{
		ServiceManager:   DefaultWorkspaceService,
		LaunchAgentLabel: "local.loom.node-agent",
		LaunchAgentPlist: "/tmp/local.loom.node-agent.plist",
	}

	if err := restartWorkspaceService(context.Background(), runner.Run, spec, "start"); err != nil {
		t.Fatalf("restartWorkspaceService returned error: %v", err)
	}
	if runner.count("launchctl bootstrap") != 0 || runner.saw("launchctl bootout") {
		t.Fatalf("loaded LaunchAgent should not be booted out or bootstrapped: %#v", runner.calls)
	}
	if !orderedCalls(runner.calls, "launchctl print", "launchctl enable", "launchctl kickstart -k") {
		t.Fatalf("launchd restart sequence was not ordered as expected: %#v", runner.calls)
	}
}

func TestWorkspaceLaunchdStartBootstrapsWhenNotLoaded(t *testing.T) {
	runner := &fakeLaunchdRecoveryRunner{printFailures: 1}
	spec := WorkspaceUpdateSpec{
		ServiceManager:   DefaultWorkspaceService,
		LaunchAgentLabel: "local.loom.node-agent",
		LaunchAgentPlist: "/tmp/local.loom.node-agent.plist",
	}

	if err := restartWorkspaceService(context.Background(), runner.Run, spec, "start"); err != nil {
		t.Fatalf("restartWorkspaceService returned error: %v", err)
	}
	if runner.count("launchctl bootstrap") != 1 {
		t.Fatalf("bootstrap count = %d, calls=%#v", runner.count("launchctl bootstrap"), runner.calls)
	}
	if runner.saw("launchctl bootout") {
		t.Fatalf("workspace update should not bootout LaunchAgent during start repair: %#v", runner.calls)
	}
	if !orderedCalls(runner.calls, "launchctl print", "launchctl bootstrap", "launchctl enable", "launchctl kickstart -k") {
		t.Fatalf("launchd bootstrap sequence was not ordered as expected: %#v", runner.calls)
	}
}

func TestWorkspaceLaunchdStartRetriesRecoverableBootstrapFailure(t *testing.T) {
	previousDelay := workspaceLaunchdRepairDelay
	workspaceLaunchdRepairDelay = 0
	defer func() { workspaceLaunchdRepairDelay = previousDelay }()

	runner := &fakeLaunchdRecoveryRunner{
		printFailures:     1,
		bootstrapFailures: []string{"Bootstrap failed: 5: Input/output error"},
	}
	spec := WorkspaceUpdateSpec{
		ServiceManager:   DefaultWorkspaceService,
		LaunchAgentLabel: "local.loom.node-agent",
		LaunchAgentPlist: "/tmp/local.loom.node-agent.plist",
	}

	if err := restartWorkspaceService(context.Background(), runner.Run, spec, "start"); err != nil {
		t.Fatalf("restartWorkspaceService returned error: %v", err)
	}
	if runner.count("launchctl bootstrap") != 2 {
		t.Fatalf("bootstrap count = %d, calls=%#v", runner.count("launchctl bootstrap"), runner.calls)
	}
	for _, needle := range []string{"launchctl print", "launchctl enable", "launchctl kickstart -k"} {
		if !runner.saw(needle) {
			t.Fatalf("missing %q in calls %#v", needle, runner.calls)
		}
	}
	if runner.saw("launchctl bootout") {
		t.Fatalf("recoverable bootstrap retry should not bootout LaunchAgent: %#v", runner.calls)
	}
	if !orderedCalls(runner.calls, "launchctl print", "launchctl bootstrap", "launchctl bootstrap", "launchctl enable", "launchctl kickstart -k") {
		t.Fatalf("launchd retry sequence was not ordered as expected: %#v", runner.calls)
	}
}

func TestWorkspaceLaunchdStartDoesNotRepairNonRecoverableBootstrapFailure(t *testing.T) {
	previousDelay := workspaceLaunchdRepairDelay
	workspaceLaunchdRepairDelay = 0
	defer func() { workspaceLaunchdRepairDelay = previousDelay }()

	runner := &fakeLaunchdRecoveryRunner{
		printFailures:     1,
		bootstrapFailures: []string{"Bootstrap failed: 112: Service is disabled"},
	}
	spec := WorkspaceUpdateSpec{
		ServiceManager:   DefaultWorkspaceService,
		LaunchAgentLabel: "local.loom.node-agent",
		LaunchAgentPlist: "/tmp/local.loom.node-agent.plist",
	}

	if err := restartWorkspaceService(context.Background(), runner.Run, spec, "start"); err == nil {
		t.Fatalf("restartWorkspaceService returned nil error")
	}
	if runner.count("launchctl bootstrap") != 1 {
		t.Fatalf("bootstrap count = %d, calls=%#v", runner.count("launchctl bootstrap"), runner.calls)
	}
	if runner.saw("launchctl bootout") || runner.saw("launchctl enable") {
		t.Fatalf("non-recoverable bootstrap failure should not repair or continue: %#v", runner.calls)
	}
}

func TestWorkspaceRollbackSwitchesBackRestartsLaunchdAndChecksHealth(t *testing.T) {
	root := t.TempDir()
	home, active, target, current := prepareWorkspaceUpdateReleases(t, root)
	stateDir := filepath.Join(home, ".local", "state", "loom", "update")
	runner := &fakeWorkspaceUpdateRunner{}

	apply, err := ApplyWorkspaceUpdate(context.Background(), WorkspaceApplyInput{
		Spec: WorkspaceUpdateSpec{
			ReleasePath:      target,
			HomeDir:          home,
			ActivePath:       current,
			StateDir:         stateDir,
			LaunchAgentPlist: filepath.Join(home, "Library", "LaunchAgents", "local.loom.node-agent.plist"),
		},
		Yes:    true,
		Now:    fixedNow,
		Runner: runner.Run,
	})
	if err != nil {
		t.Fatalf("ApplyWorkspaceUpdate returned error: %v", err)
	}
	runner.calls = nil
	rollback, err := RollbackWorkspaceUpdate(context.Background(), WorkspaceRollbackInput{
		StateDir:         stateDir,
		ManifestPath:     apply.ManifestPath,
		HomeDir:          home,
		ActivePath:       current,
		LaunchAgentPlist: filepath.Join(home, "Library", "LaunchAgents", "local.loom.node-agent.plist"),
		Yes:              true,
		Now:              fixedNow,
		Runner:           runner.Run,
	})
	if err != nil {
		t.Fatalf("RollbackWorkspaceUpdate returned error: %v", err)
	}
	if rollback.Status != UpdateStatusRolledBack {
		t.Fatalf("status = %q, rollback=%#v", rollback.Status, rollback)
	}
	assertSymlinkTarget(t, current, active)
	if !runner.saw("launchctl print") || !runner.saw("launchctl kickstart") || !runner.saw("loom-node-agent --json status") {
		t.Fatalf("rollback commands missing: %#v", runner.calls)
	}
	if runner.saw("launchctl bootout") || runner.saw("launchctl bootstrap") {
		t.Fatalf("loaded LaunchAgent rollback should avoid bootout/bootstrap: %#v", runner.calls)
	}
}

func prepareWorkspaceUpdateReleases(t *testing.T, root string) (home string, active string, target string, current string) {
	t.Helper()
	home = filepath.Join(root, "home")
	active = filepath.Join(root, "releases", "active")
	target = filepath.Join(root, "releases", "target")
	current = filepath.Join(home, ".local", "share", "loom", "current")
	for _, release := range []string{active, target} {
		for _, binary := range []string{"loom", "loom-node-agent"} {
			path := filepath.Join(release, "bin", binary)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir bin: %v", err)
			}
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatalf("write binary: %v", err)
			}
		}
		if err := WriteReleaseManifest(filepath.Join(release, DefaultReleaseManifestFileYAML), ReleaseManifest{
			ReleaseID:  filepath.Base(release),
			Version:    "0.5.3-test",
			Commit:     filepath.Base(release) + "-commit",
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
	plist := filepath.Join(home, "Library", "LaunchAgents", "local.loom.node-agent.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatalf("mkdir plist parent: %v", err)
	}
	if err := os.WriteFile(plist, []byte("<plist></plist>\n"), 0o644); err != nil {
		t.Fatalf("write plist: %v", err)
	}
	return home, active, target, current
}

type fakeWorkspaceUpdateRunner struct {
	calls []string
}

func (r *fakeWorkspaceUpdateRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, call)
	switch {
	case strings.Contains(call, "loom-node-agent --json status"):
		return []byte(`{"ok":true,"data":{"credential_configured":true}}`), nil
	case strings.Contains(call, "loom-node-agent --json heartbeat --once"):
		return []byte(`{"ok":true,"data":{"presence_state":"online"}}`), nil
	case strings.Contains(call, "ssh loom-main loom --json node health macbook"):
		return []byte(`{"ok":true,"data":{"node":{"presence_state":"online"}}}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func (r *fakeWorkspaceUpdateRunner) saw(needle string) bool {
	for _, call := range r.calls {
		if strings.Contains(call, needle) {
			return true
		}
	}
	return false
}

type fakeLaunchdRecoveryRunner struct {
	calls             []string
	printFailures     int
	bootstrapFailures []string
}

func (r *fakeLaunchdRecoveryRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	r.calls = append(r.calls, call)
	if strings.Contains(call, "launchctl print") && r.printFailures > 0 {
		r.printFailures--
		return nil, errors.New("service not loaded")
	}
	if strings.Contains(call, "launchctl bootstrap") && len(r.bootstrapFailures) > 0 {
		message := r.bootstrapFailures[0]
		r.bootstrapFailures = r.bootstrapFailures[1:]
		return []byte(message), errors.New(message)
	}
	return []byte(`{}`), nil
}

func (r *fakeLaunchdRecoveryRunner) saw(needle string) bool {
	for _, call := range r.calls {
		if strings.Contains(call, needle) {
			return true
		}
	}
	return false
}

func (r *fakeLaunchdRecoveryRunner) count(needle string) int {
	count := 0
	for _, call := range r.calls {
		if strings.Contains(call, needle) {
			count++
		}
	}
	return count
}

func orderedCalls(calls []string, needles ...string) bool {
	offset := 0
	for _, needle := range needles {
		found := false
		for offset < len(calls) {
			if strings.Contains(calls[offset], needle) {
				found = true
				offset++
				break
			}
			offset++
		}
		if !found {
			return false
		}
	}
	return true
}
