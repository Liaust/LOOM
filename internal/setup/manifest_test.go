package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveManifestPathForServiceAndUser(t *testing.T) {
	t.Parallel()

	servicePath, serviceSource, err := ResolveManifestPath(ManifestPathInput{Spec: SetupSpec{InstallMode: InstallModeService}})
	if err != nil {
		t.Fatalf("ResolveManifestPath service returned error: %v", err)
	}
	if servicePath != "/etc/loom/install.yaml" || serviceSource != "service_default" {
		t.Fatalf("service path/source = %q/%q", servicePath, serviceSource)
	}

	home := filepath.Join(t.TempDir(), "home")
	userPath, userSource, err := ResolveManifestPath(ManifestPathInput{
		Spec: SetupSpec{InstallMode: InstallModeUser, HomeDir: home},
	})
	if err != nil {
		t.Fatalf("ResolveManifestPath user returned error: %v", err)
	}
	if userPath != filepath.Join(home, ".config", "loom", "install.yaml") || userSource != "home_default" {
		t.Fatalf("user path/source = %q/%q", userPath, userSource)
	}
}

func TestWriteReadAndInspectManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "install.yaml")
	now := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	manifest := InstallManifest{
		SchemaVersion:    ManifestSchemaVersion,
		InstallID:        "install_test",
		InstalledAt:      now,
		SetupVersion:     "test",
		NodeKey:          "macbook",
		DisplayName:      "MacBook",
		NodeKind:         "workspace",
		NodeRole:         "primary_workspace",
		RuntimeClass:     "workspace_full",
		AuthorityProfile: "primary_workspace_default",
		RuntimeProfile:   "workspace_full",
		InstallMode:      InstallModeUser,
		ServiceManager:   ServiceManagerNone,
		PackageMode:      PackageModeLocalBuild,
		Metadata: map[string]any{
			"credential_token": "secret-token",
			"nested": map[string]any{
				"api_key": "secret-key",
			},
		},
	}
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatalf("WriteManifest returned error: %v", err)
	}
	loaded, err := ReadManifest(path)
	if err != nil {
		t.Fatalf("ReadManifest returned error: %v", err)
	}
	if loaded.Metadata["credential_token"] != "[REDACTED]" {
		t.Fatalf("credential token was not redacted: %#v", loaded.Metadata)
	}
	nested := loaded.Metadata["nested"].(map[string]any)
	if nested["api_key"] != "[REDACTED]" {
		t.Fatalf("nested api key was not redacted: %#v", nested)
	}
	inspection := InspectManifest(path)
	if !inspection.Exists || inspection.Manifest == nil || inspection.Manifest.NodeKey != "macbook" {
		t.Fatalf("inspection unexpected: %#v", inspection)
	}
}

func TestInspectManifestMissingIsClear(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.yaml")
	inspection := InspectManifest(path)
	if inspection.Exists || inspection.Error != "" {
		t.Fatalf("inspection = %#v", inspection)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file to be created, stat err=%v", err)
	}
}

func TestManifestFromPlanDoesNotStoreSecrets(t *testing.T) {
	t.Parallel()

	plan, err := Plan(PlannerInput{
		Spec:  SetupSpec{NodeKind: "main", NodeKey: "main", CloudSnapshotBackend: "borg", CloudBorgRepository: "ssh://storage.example/./borg/loom-main"},
		Facts: testFacts(filepath.Join(t.TempDir(), "home")),
		Now:   fixedNow,
	})
	if err != nil {
		t.Fatalf("Plan returned error: %v", err)
	}
	manifest := ManifestFromPlan(plan)
	if manifest.Credential.Configured || manifest.Credential.CredentialHint != "" {
		t.Fatalf("planned manifest should not include credentials: %#v", manifest.Credential)
	}
	if manifest.PlanHash != plan.PlanHash || manifest.NodeKey != plan.Spec.NodeKey {
		t.Fatalf("manifest did not preserve plan identity: %#v", manifest)
	}
	if manifest.BootstrapMode != plan.Spec.BootstrapMode {
		t.Fatalf("bootstrap mode = %q, want %q", manifest.BootstrapMode, plan.Spec.BootstrapMode)
	}
	if manifest.CloudSnapshotBackend != "borg" || manifest.CloudBorgRepository != plan.Spec.CloudBorgRepository {
		t.Fatalf("manifest did not preserve Borg backend fields: %#v", manifest)
	}
	if manifest.CloudBorgPassphraseFile != plan.Paths.CloudBorgPassphraseFile || strings.Contains(manifest.CloudBorgPassphraseFile, "secret") {
		t.Fatalf("manifest Borg passphrase path unexpected: %#v", manifest)
	}
}
