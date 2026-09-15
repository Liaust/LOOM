package setup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusMissingManifestIsNotInstalled(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "install.yaml")
	status, err := Status(StatusInput{
		ManifestPath: path,
		Facts:        testFacts(filepath.Join(t.TempDir(), "home")),
		Now:          fixedNow,
	})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.Summary.Status != SummaryNotInstalled {
		t.Fatalf("summary = %q, want %q; diagnostics=%#v", status.Summary.Status, SummaryNotInstalled, status.Diagnostics)
	}
	if status.Manifest.Exists {
		t.Fatalf("manifest should be missing: %#v", status.Manifest)
	}
}

func TestStatusPartialManifestReportsMissingPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	status, err := Status(StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.Summary.Status != SummaryPartial {
		t.Fatalf("summary = %q, want partial", status.Summary.Status)
	}
	assertPathStatus(t, status, "box_root", "missing")
	assertPathStatus(t, status, "node_agent_config", "missing")
	if status.NodeAgent.Status != "not_initialized" {
		t.Fatalf("node-agent status = %q", status.NodeAgent.Status)
	}
}

func TestStatusReportsPresentBox(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	boxPath := filepath.Join(dir, "LOOM Box")
	if err := mkdirAll(filepath.Join(boxPath, ".loom")); err != nil {
		t.Fatalf("mkdir box: %v", err)
	}
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	manifest.BoxPath = boxPath
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	status, err := Status(StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.Box.Exists || !status.Box.LoomDirExists || status.Box.Status != "configured" {
		t.Fatalf("box status = %#v", status.Box)
	}
}

func TestStatusMainURLMissingForNonMainDiagnostic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.yaml")
	manifest := testWorkspaceManifest(dir)
	manifest.MainURL = ""
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	status, err := Status(StatusInput{ManifestPath: path, Facts: testFacts(filepath.Join(dir, "home")), Now: fixedNow})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if status.MainConnectivity.Status != "missing_url" {
		t.Fatalf("main connectivity = %#v", status.MainConnectivity)
	}
	found := false
	for _, diagnostic := range status.Diagnostics {
		if diagnostic.Code == "setup.main_url_required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing main_url diagnostic: %#v", status.Diagnostics)
	}
}

func assertPathStatus(t *testing.T, status SetupStatus, key string, want string) {
	t.Helper()
	for _, path := range status.Paths {
		if path.Key == key {
			if path.Status != want {
				t.Fatalf("%s status = %q, want %q", key, path.Status, want)
			}
			return
		}
	}
	t.Fatalf("path status %q not found: %#v", key, status.Paths)
}

func testWorkspaceManifest(dir string) InstallManifest {
	home := filepath.Join(dir, "home")
	return InstallManifest{
		SchemaVersion:    ManifestSchemaVersion,
		InstallID:        "install_test",
		InstalledAt:      fixedNow(),
		SetupVersion:     "test",
		NodeKey:          "macbook",
		DisplayName:      "MacBook",
		NodeKind:         "workspace",
		NodeRole:         "primary_workspace",
		RuntimeClass:     "workspace_full",
		MainURL:          "http://10.44.0.2:8080",
		AuthorityProfile: "primary_workspace_default",
		RuntimeProfile:   "workspace_full",
		InstallMode:      InstallModeUser,
		ServiceManager:   ServiceManagerNone,
		PackageMode:      PackageModeLocalBuild,
		HomeDir:          home,
		ConfigDir:        filepath.Join(home, ".config", "loom"),
		DataDir:          filepath.Join(home, ".local", "share", "loom"),
		StateDir:         filepath.Join(home, ".local", "state", "loom"),
		LogDir:           filepath.Join(home, ".local", "state", "loom", "logs"),
		BoxPath:          filepath.Join(home, "LOOM Box"),
		BoxProfile:       "workspace",
	}
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o700)
}
