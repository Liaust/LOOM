package setup

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loom.local/loom/internal/nodeprofiles"
)

func TestPlanMainDefaults(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "main", NodeKey: "main", DisplayName: "Main"},
		Facts: testFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Spec.InstallMode != InstallModeService {
		t.Fatalf("install mode = %q, want service", plan.Spec.InstallMode)
	}
	if plan.Profile.AuthorityProfileKey != nodeprofiles.AuthorityMainNodeDefault || plan.Profile.RuntimeProfileKey != nodeprofiles.RuntimeMainFull {
		t.Fatalf("profiles = %#v", plan.Profile)
	}
	if !plan.Spec.EnableLoomd || plan.Spec.EnableNodeAgent {
		t.Fatalf("main feature flags unexpected: %#v", plan.Spec)
	}
	if !plan.Spec.EnableCloud {
		t.Fatalf("main setup should enable cloud substrate")
	}
	if plan.Spec.BootstrapMode != "production" || !plan.Spec.ProductionBootstrap {
		t.Fatalf("main bootstrap mode unexpected: %s/%t", plan.Spec.BootstrapMode, plan.Spec.ProductionBootstrap)
	}
	if plan.Paths.ManifestPath != "/etc/loom/install.yaml" {
		t.Fatalf("manifest path = %q", plan.Paths.ManifestPath)
	}
	if plan.Paths.BoxPath != "/srv/loom/box" {
		t.Fatalf("box path = %q", plan.Paths.BoxPath)
	}
	if plan.Paths.BoxStateRoot != "/var/lib/loom/box-state" || plan.Paths.StorageRoot != "/srv/loom/storage" {
		t.Fatalf("canonical main roots unexpected: %#v", plan.Paths)
	}
	if !containsString(plan.Paths.ServiceReadWritePaths, plan.Paths.BoxLoomDir) {
		t.Fatalf("service read/write paths do not include Box .loom: %#v", plan.Paths.ServiceReadWritePaths)
	}
	if !containsString(plan.Paths.ServiceReadWritePaths, plan.Paths.ImportsRoot) {
		t.Fatalf("service read/write paths do not include imports root: %#v", plan.Paths.ServiceReadWritePaths)
	}
	if !containsString(plan.Paths.ServiceReadWritePaths, plan.Paths.BoxStateRoot) {
		t.Fatalf("service read/write paths do not include external Box state root: %#v", plan.Paths.ServiceReadWritePaths)
	}
	if !containsString(plan.Paths.ServiceReadWritePaths, plan.Paths.CloudStateDir) {
		t.Fatalf("service read/write paths do not include cloud state dir: %#v", plan.Paths.ServiceReadWritePaths)
	}
	if plan.Paths.CloudConfigPath != "/etc/loom/cloud/config.json" || plan.Paths.CloudRcloneConfigPath != "/etc/loom/cloud/rclone.conf" {
		t.Fatalf("cloud paths unexpected: %#v", plan.Paths)
	}
	if plan.Spec.CloudRemoteName != "loom-cloud" || plan.Spec.CloudRemoteRoot != "loom" {
		t.Fatalf("cloud remote defaults unexpected: %#v", plan.Spec)
	}
	if plan.Spec.CloudSnapshotBackend != "legacy_tree" {
		t.Fatalf("cloud snapshot backend = %q, want legacy_tree", plan.Spec.CloudSnapshotBackend)
	}
	if plan.Paths.CloudBorgPassphraseFile != "/etc/loom/cloud/borg.passphrase" {
		t.Fatalf("borg passphrase file = %q", plan.Paths.CloudBorgPassphraseFile)
	}
	for _, path := range []string{plan.Paths.CloudBorgCacheDir, plan.Paths.CloudBorgSecurityDir} {
		if !strings.HasPrefix(path, filepath.Join(plan.Paths.CloudStateDir, "borg")) {
			t.Fatalf("borg path %q is not under cloud borg state dir %q", path, filepath.Join(plan.Paths.CloudStateDir, "borg"))
		}
		if !containsString(plan.Paths.ServiceReadWritePaths, path) {
			t.Fatalf("service read/write paths do not include Borg path %s: %#v", path, plan.Paths.ServiceReadWritePaths)
		}
	}
	if stepByID(plan, "ensure_cloud_borg_cache_dir").ID == "" || stepByID(plan, "cloud_borg_credentials_operator_managed").ID == "" {
		t.Fatalf("plan is missing Borg setup steps: %#v", plan.Steps)
	}
	if containsString(plan.Paths.ManagedTmpfilesPaths, filepath.Join(plan.Paths.BoxLoomDir, "state")) {
		t.Fatalf("managed tmpfiles paths must not include visible Box state: %#v", plan.Paths.ManagedTmpfilesPaths)
	}
}

func TestPlanBorgBackendReportsCredentialPrerequisites(t *testing.T) {
	t.Parallel()

	plan, err := Plan(PlannerInput{
		Spec: SetupSpec{
			NodeKind:             "main",
			NodeKey:              "main",
			CloudSnapshotBackend: "borg",
		},
		Facts: testFacts(filepath.Join(t.TempDir(), "home")),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	step := stepByID(plan, "cloud_borg_credentials_operator_managed")
	if !step.Required {
		t.Fatalf("Borg credential step should be required when backend=borg: %#v", step)
	}
	if step.Metadata["repository_configured"] != false {
		t.Fatalf("Borg credential metadata should report missing repository: %#v", step.Metadata)
	}
	if !hasDiagnostic(plan.Diagnostics, "setup.cloud_borg_repository_missing") {
		t.Fatalf("expected missing Borg repository diagnostic: %#v", plan.Diagnostics)
	}
}

func TestPlanPrimaryWorkspaceDefaults(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "workspace", NodeKey: "macbook", MainURL: "http://10.44.0.2:8080"},
		Facts: testFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Spec.NodeRole != "primary_workspace" || plan.Spec.RuntimeClass != nodeprofiles.RuntimeWorkspaceFull {
		t.Fatalf("role/runtime = %s/%s", plan.Spec.NodeRole, plan.Spec.RuntimeClass)
	}
	if plan.Profile.AuthorityProfileKey != nodeprofiles.AuthorityPrimaryWorkspaceDefault {
		t.Fatalf("authority = %q", plan.Profile.AuthorityProfileKey)
	}
	if plan.Spec.BootstrapMode != "none" || plan.Spec.ProductionBootstrap {
		t.Fatalf("workspace bootstrap mode unexpected: %s/%t", plan.Spec.BootstrapMode, plan.Spec.ProductionBootstrap)
	}
	if !plan.Spec.EnableNodeAgent || !plan.Spec.EnableBox || plan.Spec.EnableDropzone || !plan.Spec.EnableWatchedRoots {
		t.Fatalf("workspace feature flags unexpected: %#v", plan.Spec)
	}
	if plan.Paths.NodeAgentConfigPath != filepath.Join(home, ".config", "loom-node-agent", "config.json") {
		t.Fatalf("node-agent config = %q", plan.Paths.NodeAgentConfigPath)
	}
	if len(plan.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %#v", plan.Diagnostics)
	}
}

func TestPlanHardwareAgentDefaults(t *testing.T) {
	t.Parallel()

	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "hardware", NodeKey: "sensor", MainURL: "http://10.44.0.2:8080"},
		Facts: testFacts(filepath.Join(t.TempDir(), "home")),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Spec.NodeRole != "capability_node" || plan.Spec.RuntimeClass != nodeprofiles.RuntimeHardwareAgent {
		t.Fatalf("role/runtime = %s/%s", plan.Spec.NodeRole, plan.Spec.RuntimeClass)
	}
	if plan.Profile.AuthorityProfileKey != nodeprofiles.AuthorityHardwareCapabilityDefault {
		t.Fatalf("authority = %q", plan.Profile.AuthorityProfileKey)
	}
	if plan.Spec.EnableBox || plan.Spec.EnableDropzone || plan.Paths.BoxPath != "" {
		t.Fatalf("hardware should not create Box by default: spec=%#v paths=%#v", plan.Spec, plan.Paths)
	}
	if len(plan.Paths.ServiceReadWritePaths) != 0 || len(plan.Paths.ManagedTmpfilesPaths) != 0 {
		t.Fatalf("hardware should not plan loomd service Box paths: %#v", plan.Paths)
	}
}

func TestPlanRejectsInvalidRuntimeCombination(t *testing.T) {
	t.Parallel()

	_, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "main", NodeKey: "main", RuntimeClass: nodeprofiles.RuntimeHardwareAgent},
		Facts: testFacts(filepath.Join(t.TempDir(), "home")),
		Now:   fixedNow,
	})
	if err == nil {
		t.Fatal("Plan returned nil error")
	}
	if !strings.Contains(err.Error(), "not compatible") {
		t.Fatalf("error = %q", err)
	}
}

func TestPlanRejectsInvalidBootstrapMode(t *testing.T) {
	t.Parallel()

	_, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "main", NodeKey: "main", BootstrapMode: "sometimes"},
		Facts: testFacts(filepath.Join(t.TempDir(), "home")),
		Now:   fixedNow,
	})
	if err == nil {
		t.Fatal("Plan returned nil error")
	}
	if !strings.Contains(err.Error(), "unsupported bootstrap_mode") {
		t.Fatalf("error = %q", err)
	}
}

func TestPlanExplicitBoxPathOverride(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	boxPath := filepath.Join(t.TempDir(), "custom-box")
	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "workspace", NodeKey: "macbook", MainURL: "http://10.44.0.2:8080", BoxPath: boxPath},
		Facts: testFacts(home),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if plan.Paths.BoxPath != boxPath || plan.Paths.BoxLoomDir != filepath.Join(boxPath, ".loom") {
		t.Fatalf("box paths = %#v", plan.Paths)
	}
}

func TestPlanHashStableAcrossCreatedAt(t *testing.T) {
	t.Parallel()

	facts := testFacts(filepath.Join(t.TempDir(), "home"))
	spec := SetupSpec{NodeKind: "workspace", NodeKey: "macbook", MainURL: "http://10.44.0.2:8080"}
	first, err := Plan(PlannerInput{Spec: spec, Facts: facts, Now: fixedNow})
	if err != nil {
		t.Fatalf("first Plan returned error: %v", err)
	}
	second, err := Plan(PlannerInput{Spec: spec, Facts: facts, Now: func() time.Time { return fixedNow().Add(time.Hour) }})
	if err != nil {
		t.Fatalf("second Plan returned error: %v", err)
	}
	if first.PlanHash != second.PlanHash {
		t.Fatalf("plan hashes differ: %s vs %s", first.PlanHash, second.PlanHash)
	}
}

func testFacts(home string) TargetFacts {
	return TargetFacts{
		OS:         "linux",
		Arch:       "amd64",
		Hostname:   "test-host",
		UserName:   "loomadmin",
		HomeDir:    home,
		HasSystemd: true,
		HasGit:     true,
		HasGo:      true,
	}
}

func fixedNow() time.Time {
	return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stepByID(plan SetupPlan, id string) SetupStep {
	for _, step := range plan.Steps {
		if step.ID == id {
			return step
		}
	}
	return SetupStep{}
}

func hasDiagnostic(diagnostics []Diagnostic, code string) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}
