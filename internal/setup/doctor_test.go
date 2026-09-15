package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorReportsMissingManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "install.yaml")
	report, err := Doctor(DoctorInput{StatusInput: StatusInput{
		ManifestPath: path,
		Facts:        testFacts(filepath.Join(t.TempDir(), "home")),
		Now:          fixedNow,
	}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	assertFinding(t, report, "setup.manifest.missing")
	if len(report.Repairs) == 0 || report.Repairs[0].ID != RepairRewriteRedactedManifest {
		t.Fatalf("expected rewrite manifest repair, got %#v", report.Repairs)
	}
}

func TestDoctorReportsMissingBox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	if err := WriteManifest(path, testWorkspaceManifest(dir)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.path.box_root.missing")
	if finding.RepairID != RepairCreateMissingBoxRoot {
		t.Fatalf("repair = %q", finding.RepairID)
	}
}

func TestDoctorReportsMainBoxProfileMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := InstallManifest{
		SchemaVersion:    ManifestSchemaVersion,
		InstallID:        "install_main",
		InstalledAt:      fixedNow(),
		SetupVersion:     "test",
		NodeKey:          "main",
		DisplayName:      "Main",
		NodeKind:         "main",
		NodeRole:         "main",
		RuntimeClass:     "main_full",
		AuthorityProfile: "main_node_default",
		RuntimeProfile:   "main_full",
		InstallMode:      InstallModeService,
		ServiceManager:   ServiceManagerNone,
		PackageMode:      PackageModeLocalBuild,
		HomeDir:          filepath.Join(dir, "home"),
		ConfigDir:        filepath.Join(dir, "etc", "loom"),
		DataDir:          filepath.Join(dir, "var", "lib", "loom"),
		StateDir:         filepath.Join(dir, "var", "lib", "loom", "state"),
		LogDir:           filepath.Join(dir, "var", "log", "loom"),
		BoxPath:          filepath.Join(dir, "home", "LOOM Box"),
		BoxProfile:       "workspace",
		ObjectStorePath:  filepath.Join(dir, "var", "lib", "loom", "object-store"),
		SocketPath:       filepath.Join(dir, "run", "loom", "loomd.sock"),
	}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	assertFinding(t, report, "setup.box.main_profile_required")
}

func TestDoctorReportsBoxAccessDenied(t *testing.T) {
	spec, manifestPath := testMainApplySpec(t)
	plan, err := Plan(PlannerInput{Spec: spec, Facts: testFacts(spec.HomeDir), ManifestPath: manifestPath, Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(spec.BoxPath, ".loom"), 0o755); err != nil {
		t.Fatalf("mkdir box: %v", err)
	}
	loomDir := filepath.Join(spec.BoxPath, ".loom")
	if err := os.Chmod(loomDir, 0o555); err != nil {
		t.Fatalf("chmod .loom: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(loomDir, 0o755) })
	if err := WriteManifest(manifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: manifestPath, Facts: testFacts(spec.HomeDir), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.path.box_loom_dir.access_denied")
	if finding.RepairID != RepairMainBoxServiceACL {
		t.Fatalf("repair id = %q", finding.RepairID)
	}
}

func TestDoctorSystemdServiceSkipsCurrentActorWriteForProtectedRuntimePaths(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home", "loomadmin")
	spec := SetupSpec{
		NodeKey:                 "main",
		DisplayName:             "Main",
		NodeKind:                "main",
		NodeRole:                "main",
		RuntimeClass:            "main_full",
		InstallMode:             InstallModeService,
		ServiceManager:          ServiceManagerSystemd,
		PackageMode:             PackageModeLocalBuild,
		HomeDir:                 home,
		UserName:                "loomadmin",
		ConfigDir:               filepath.Join(root, "etc", "loom"),
		DataDir:                 filepath.Join(root, "var", "lib", "loom"),
		StateDir:                filepath.Join(root, "var", "lib", "loom", "state"),
		LogDir:                  filepath.Join(root, "var", "lib", "loom", "logs"),
		ObjectStorePath:         filepath.Join(root, "var", "lib", "loom", "object-store"),
		MainDocumentsPath:       filepath.Join(root, "var", "lib", "loom", "main-documents"),
		StorageExportRoot:       filepath.Join(root, "var", "lib", "loom", "storage-views", "main-export"),
		SocketPath:              filepath.Join(root, "run", "loom", "loomd.sock"),
		BoxPath:                 filepath.Join(home, "loom-box"),
		BoxProfile:              "main",
		EnableCloud:             true,
		CloudSnapshotBackend:    "borg",
		CloudConfigPath:         filepath.Join(root, "etc", "loom", "cloud", "config.json"),
		CloudStateDir:           filepath.Join(root, "var", "lib", "loom", "cloud"),
		CloudBorgRepository:     "ssh://storage.example/./borg/loom-main",
		CloudBorgPassphraseFile: filepath.Join(root, "etc", "loom", "cloud", "borg.passphrase"),
		CloudBorgCacheDir:       filepath.Join(root, "var", "lib", "loom", "cloud", "borg", "cache"),
		CloudBorgSecurityDir:    filepath.Join(root, "var", "lib", "loom", "cloud", "borg", "security"),
	}
	normalized, diagnostics, err := NormalizeSpec(spec, testFacts(home))
	if err != nil {
		t.Fatalf("NormalizeSpec returned error: %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("NormalizeSpec diagnostics = %#v", diagnostics)
	}
	plan, err := Plan(PlannerInput{Spec: normalized, Facts: testFacts(home), ManifestPath: filepath.Join(spec.ConfigDir, "install.yaml"), Now: fixedNow})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	for _, dir := range []string{
		plan.Paths.ConfigDir,
		plan.Paths.DataDir,
		plan.Paths.StateDir,
		plan.Paths.LogDir,
		plan.Paths.ObjectStorePath,
		plan.Paths.MainDocumentsPath,
		plan.Paths.StorageExportRoot,
		filepath.Dir(plan.Paths.SocketPath),
		plan.Paths.CloudConfigDir,
		plan.Paths.CloudStateDir,
		plan.Paths.CloudBorgCacheDir,
		plan.Paths.CloudBorgSecurityDir,
		plan.Paths.BoxPath,
		plan.Paths.BoxLoomDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(plan.Paths.CloudBorgPassphraseFile, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write borg passphrase: %v", err)
	}
	if err := WriteManifest(plan.Paths.ManifestPath, ManifestFromPlan(plan)); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}

	protected := []string{
		plan.Paths.DataDir,
		plan.Paths.StateDir,
		plan.Paths.LogDir,
		plan.Paths.ObjectStorePath,
		plan.Paths.MainDocumentsPath,
		plan.Paths.StorageExportRoot,
		filepath.Dir(plan.Paths.SocketPath),
		plan.Paths.CloudStateDir,
		plan.Paths.CloudBorgCacheDir,
		plan.Paths.CloudBorgSecurityDir,
	}
	for _, dir := range protected {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
		dir := dir
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	}
	if err := os.Chmod(plan.Paths.CloudBorgPassphraseFile, 0o000); err != nil {
		t.Fatalf("chmod borg passphrase: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(plan.Paths.CloudBorgPassphraseFile, 0o600) })

	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: plan.Paths.ManifestPath, Facts: testFacts(home), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	for _, finding := range report.Findings {
		if strings.HasPrefix(finding.Code, "setup.path.") && strings.HasSuffix(finding.Code, ".access_denied") {
			t.Fatalf("systemd service doctor should not require current-actor access for protected runtime path: %#v", finding)
		}
	}
}

func TestDoctorReportsRawSecretInManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "install.yaml")
	raw := []byte(`schema_version: loom.install.v0.5
install_id: install_secret
installed_at: 2026-05-31T12:00:00Z
updated_at: 2026-05-31T12:00:00Z
setup_version: test
node_key: macbook
display_name: MacBook
node_kind: workspace
node_role: primary_workspace
runtime_class: workspace_full
main_url: http://10.44.0.2:8080
authority_profile: primary_workspace_default
runtime_profile: workspace_full
install_mode: user
service_manager: none
package_mode: local-build
metadata:
  credential_token: not-redacted
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write raw manifest: %v", err)
	}
	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(t.TempDir(), "home")), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	finding := assertFinding(t, report, "setup.manifest.raw_secret")
	if finding.RepairID != RepairRewriteRedactedManifest {
		t.Fatalf("repair = %q", finding.RepairID)
	}
}

func TestDoctorReportsIncompatibleKindRuntime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	manifest.NodeKind = "main"
	manifest.NodeRole = "main"
	manifest.RuntimeClass = "hardware_agent"
	manifest.AuthorityProfile = "main_node_default"
	manifest.RuntimeProfile = "hardware_agent"
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	report, err := Doctor(DoctorInput{StatusInput: StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow}})
	if err != nil {
		t.Fatalf("Doctor returned error: %v", err)
	}
	assertFinding(t, report, "setup.profile.invalid")
}

func assertFinding(t *testing.T, report DoctorReport, code string) DoctorFinding {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.Code == code {
			return finding
		}
	}
	t.Fatalf("finding %q not found: %#v", code, report.Findings)
	return DoctorFinding{}
}
