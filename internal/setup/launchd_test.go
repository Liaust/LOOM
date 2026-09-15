package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanLaunchdWorkspaceUsesUserPathsAndLaunchAgent(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	plan, err := Plan(PlannerInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
		},
		Facts: testMacFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Paths.ConfigDir != filepath.Join(home, ".config", "loom") {
		t.Fatalf("config dir = %q", plan.Paths.ConfigDir)
	}
	if plan.Paths.ManifestPath != filepath.Join(home, ".config", "loom", "install.yaml") {
		t.Fatalf("manifest path = %q", plan.Paths.ManifestPath)
	}
	if plan.Paths.NodeAgentConfigPath != filepath.Join(home, ".config", "loom-node-agent", "config.json") {
		t.Fatalf("node-agent config = %q", plan.Paths.NodeAgentConfigPath)
	}
	if plan.Paths.NodeAgentStatePath != filepath.Join(home, ".local", "state", "loom-node-agent", "state.json") {
		t.Fatalf("node-agent state = %q", plan.Paths.NodeAgentStatePath)
	}
	if plan.Paths.NodeAgentDataDir != filepath.Join(home, ".local", "state", "loom-node-agent") {
		t.Fatalf("node-agent data dir = %q", plan.Paths.NodeAgentDataDir)
	}
	if plan.Paths.LaunchAgentPlistPath != filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist") {
		t.Fatalf("LaunchAgent path = %q", plan.Paths.LaunchAgentPlistPath)
	}
	step := setupStepByID(plan, "install_service")
	if step.ID == "" || step.Privileged {
		t.Fatalf("install service step should be user-level launchd: %#v", step)
	}
	if step.Metadata["label"] != LaunchAgentLabel || step.Metadata["path"] != plan.Paths.LaunchAgentPlistPath {
		t.Fatalf("install service metadata = %#v", step.Metadata)
	}
}

func TestApplyLaunchdWorkspaceWritesPlistLoadsAndRecordsService(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	runner := &fakeLaunchdRunner{
		printResults: []fakeLaunchdResult{
			{err: errors.New("not loaded")},
			{output: "pid = 123\n"},
		},
	}
	result, err := Apply(ApplyInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
			UserName:       "leonardo",
		},
		Facts:         testMacFacts(home),
		Yes:           true,
		Now:           fixedNow,
		LaunchdRunner: runner,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if result.Refused || len(result.Blocked) > 0 {
		t.Fatalf("apply refused/blocked: %#v", result)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", LaunchAgentLabel+".plist")
	payload, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatalf("read LaunchAgent plist: %v", err)
	}
	body := string(payload)
	for _, want := range []string{
		"<string>" + LaunchAgentLabel + "</string>",
		"<string>" + filepath.Join(home, ".local", "bin", "loom-node-agent") + "</string>",
		"<string>serve</string>",
		"<key>KeepAlive</key>",
		filepath.Join(home, ".local", "state", "loom", "logs", "loom-node-agent.err.log"),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("LaunchAgent plist missing %q:\n%s", want, body)
		}
	}
	if len(result.Manifest.Services) != 1 || result.Manifest.Services[0].Label != LaunchAgentLabel || result.Manifest.Services[0].Path != plistPath {
		t.Fatalf("manifest services unexpected: %#v", result.Manifest.Services)
	}
	if !runner.called("bootstrap") || !runner.called("enable") || !runner.called("kickstart") {
		t.Fatalf("missing launchctl calls: %#v", runner.calls)
	}
	if runner.called("bootout") {
		t.Fatalf("setup apply should not bootout LaunchAgent: %#v", runner.calls)
	}
	if len(result.Status.Services) != 1 || result.Status.Services[0].Status != "running" || result.Status.Services[0].PID != 123 {
		t.Fatalf("status services unexpected: %#v", result.Status.Services)
	}
}

func TestApplyLaunchdWorkspaceKeepsLoadedService(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	runner := &fakeLaunchdRunner{
		printResults: []fakeLaunchdResult{
			{output: "pid = 123\n"},
			{output: "pid = 123\n"},
		},
	}
	result, err := Apply(ApplyInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
			UserName:       "leonardo",
		},
		Facts:         testMacFacts(home),
		Yes:           true,
		Now:           fixedNow,
		LaunchdRunner: runner,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("apply should not block when service is already loaded: %#v", result.Blocked)
	}
	if runner.called("bootout") || runner.called("bootstrap") {
		t.Fatalf("loaded LaunchAgent should not be booted out or bootstrapped: %#v", runner.calls)
	}
	if !runner.called("print") || !runner.called("enable") || !runner.called("kickstart") {
		t.Fatalf("missing launchctl calls: %#v", runner.calls)
	}
}

func TestApplyLaunchdWorkspaceRetriesRecoverableBootstrapFailure(t *testing.T) {
	previousDelay := setupLaunchdRepairDelay
	setupLaunchdRepairDelay = 0
	defer func() { setupLaunchdRepairDelay = previousDelay }()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	runner := &fakeLaunchdRunner{
		printResults: []fakeLaunchdResult{
			{err: errors.New("not loaded")},
			{err: errors.New("not loaded")},
			{output: "pid = 123\n"},
		},
		bootstrapResults: []fakeLaunchdResult{
			{err: errors.New("Bootstrap failed: 5: Input/output error")},
			{},
		},
	}
	result, err := Apply(ApplyInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
			UserName:       "leonardo",
		},
		Facts:         testMacFacts(home),
		Yes:           true,
		Now:           fixedNow,
		LaunchdRunner: runner,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("apply should retry recoverable bootstrap failure: %#v", result.Blocked)
	}
	if runner.count("bootstrap") != 2 {
		t.Fatalf("bootstrap count = %d, calls=%#v", runner.count("bootstrap"), runner.calls)
	}
	if runner.called("bootout") {
		t.Fatalf("recoverable bootstrap retry should not bootout LaunchAgent: %#v", runner.calls)
	}
}

func TestApplyLaunchdWorkspaceAcceptsLoadedServiceAfterKickstartExit37(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	runner := &fakeLaunchdRunner{
		printResults: []fakeLaunchdResult{
			{output: "pid = 123\n"},
			{output: "pid = 123\n"},
			{output: "pid = 123\n"},
		},
		kickstartResults: []fakeLaunchdResult{
			{err: errors.New("launchctl kickstart -k gui/501/local.loom.node-agent: exit status 37")},
		},
	}
	result, err := Apply(ApplyInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
			UserName:       "leonardo",
		},
		Facts:         testMacFacts(home),
		Yes:           true,
		Now:           fixedNow,
		LaunchdRunner: runner,
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if len(result.Blocked) > 0 {
		t.Fatalf("apply should accept loaded service after transient kickstart failure: %#v", result.Blocked)
	}
	if runner.called("bootout") || runner.called("bootstrap") {
		t.Fatalf("loaded LaunchAgent should not be booted out or bootstrapped: %#v", runner.calls)
	}
	if runner.count("kickstart") != 1 {
		t.Fatalf("kickstart count = %d, calls=%#v", runner.count("kickstart"), runner.calls)
	}
}

func TestDoctorLaunchAgentMissingReportsRepairAndRepairDryRun(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	plan, err := Plan(PlannerInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
		},
		Facts: testMacFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := WriteManifest(plan.Paths.ManifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: plan.Paths.ManifestPath, Facts: testMacFacts(home), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.service.loom_node_agent.missing")
	if finding.RepairID != RepairInstallLaunchAgent {
		t.Fatalf("repair id = %q", finding.RepairID)
	}

	repair, err := Repair(RepairInput{
		StatusInput: StatusInput{ManifestPath: plan.Paths.ManifestPath, Facts: testMacFacts(home), Now: fixedNow},
		FixIDs:      []string{RepairInstallLaunchAgent},
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Repair returned error: %v", err)
	}
	if len(repair.Changed) == 0 || repair.Changed[0].RepairID != RepairInstallLaunchAgent {
		t.Fatalf("repair dry-run did not plan LaunchAgent changes: %#v", repair)
	}
}

func TestStatusLaunchAgentNotLoaded(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home", "leonardo")
	plan, err := Plan(PlannerInput{
		Spec: SetupSpec{
			NodeKind:       "workspace",
			NodeKey:        "macbook",
			DisplayName:    "MacBook",
			MainURL:        "http://10.44.0.2:8080",
			InstallMode:    InstallModeService,
			ServiceManager: ServiceManagerLaunchd,
			HomeDir:        home,
		},
		Facts: testMacFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	manifest := ManifestFromPlan(plan)
	if err := WriteManifest(plan.Paths.ManifestPath, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.Paths.LaunchAgentPlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.Paths.LaunchAgentPlistPath, renderLaunchAgentPlist(mustLaunchAgentSpec(t, plan)), 0o644); err != nil {
		t.Fatal(err)
	}

	status, err := Status(StatusInput{
		ManifestPath:  plan.Paths.ManifestPath,
		Facts:         testMacFacts(home),
		Now:           fixedNow,
		LaunchdRunner: &fakeLaunchdRunner{printErr: errors.New("not loaded")},
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if len(status.Services) != 1 || status.Services[0].Status != "not_loaded" {
		t.Fatalf("service status = %#v", status.Services)
	}
}

func mustLaunchAgentSpec(t *testing.T, plan SetupPlan) LaunchAgentSpec {
	t.Helper()
	spec, ok, err := launchAgentSpecFromPlan(plan)
	if err != nil || !ok {
		t.Fatalf("launchAgentSpecFromPlan = ok=%t err=%v", ok, err)
	}
	return spec
}

func testMacFacts(home string) TargetFacts {
	return TargetFacts{
		OS:         "darwin",
		Arch:       "arm64",
		Hostname:   "macbook",
		UserName:   "leonardo",
		HomeDir:    home,
		HasLaunchd: true,
		HasGit:     true,
		HasGo:      true,
	}
}

func setupStepByID(plan SetupPlan, id string) SetupStep {
	for _, step := range plan.Steps {
		if step.ID == id {
			return step
		}
	}
	return SetupStep{}
}

type fakeLaunchdRunner struct {
	calls            [][]string
	printOutput      string
	printErr         error
	bootstrapErr     error
	printResults     []fakeLaunchdResult
	bootstrapResults []fakeLaunchdResult
	kickstartResults []fakeLaunchdResult
}

type fakeLaunchdResult struct {
	output string
	err    error
}

func (r *fakeLaunchdRunner) Launchctl(ctx context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{}, args...))
	if len(args) == 0 {
		return "", nil
	}
	switch args[0] {
	case "bootstrap":
		if len(r.bootstrapResults) > 0 {
			return popFakeLaunchdResult(&r.bootstrapResults)
		}
		if r.bootstrapErr != nil {
			return "", r.bootstrapErr
		}
	case "kickstart":
		if len(r.kickstartResults) > 0 {
			return popFakeLaunchdResult(&r.kickstartResults)
		}
	case "print":
		if len(r.printResults) > 0 {
			return popFakeLaunchdResult(&r.printResults)
		}
		return r.printOutput, r.printErr
	}
	return "", nil
}

func popFakeLaunchdResult(results *[]fakeLaunchdResult) (string, error) {
	result := (*results)[0]
	*results = (*results)[1:]
	return result.output, result.err
}

func (r *fakeLaunchdRunner) called(name string) bool {
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == name {
			return true
		}
	}
	return false
}

func (r *fakeLaunchdRunner) count(name string) int {
	var total int
	for _, call := range r.calls {
		if len(call) > 0 && call[0] == name {
			total++
		}
	}
	return total
}
