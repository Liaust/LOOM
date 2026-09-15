package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteReadUpdateManifestRedactsMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.yaml")
	manifest := UpdateManifest{
		SchemaVersion: UpdateManifestSchemaVersion,
		UpdateID:      "update_test",
		Status:        "planned",
		StartedAt:     time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC),
		Active:        ReleaseState{Path: "/srv/loom/current"},
		Target:        ReleaseState{Path: "/srv/loom/releases/test"},
		Rollback:      RollbackPlan{Class: RollbackClassServiceOnly},
		Metadata: map[string]any{
			"operator": "agent",
			"token":    "secret-token",
			"nested": map[string]any{
				"password": "secret-password",
			},
		},
	}
	if err := WriteUpdateManifest(path, manifest); err != nil {
		t.Fatalf("WriteUpdateManifest returned error: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written manifest: %v", err)
	}
	if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "secret-password") {
		t.Fatalf("written update manifest leaked secret metadata:\n%s", string(raw))
	}
	loaded, err := ReadUpdateManifest(path)
	if err != nil {
		t.Fatalf("ReadUpdateManifest returned error: %v", err)
	}
	if loaded.Metadata["token"] != "[REDACTED]" {
		t.Fatalf("token metadata was not redacted: %#v", loaded.Metadata)
	}
}

func TestWriteUpdateManifestUsesGroupReadableMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.yaml")
	manifest := UpdateManifest{
		SchemaVersion: UpdateManifestSchemaVersion,
		UpdateID:      "update_permissions",
		Status:        "planned",
		StartedAt:     time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC),
		Active:        ReleaseState{Path: "/srv/loom/current"},
		Target:        ReleaseState{Path: "/srv/loom/releases/test"},
		Rollback:      RollbackPlan{Class: RollbackClassServiceOnly},
	}
	if err := WriteUpdateManifest(path, manifest); err != nil {
		t.Fatalf("WriteUpdateManifest returned error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("manifest mode = %o, want 640", got)
	}
}

func TestWriteReadReleaseManifestRedactsMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultReleaseManifestFileYAML)
	if err := WriteReleaseManifest(path, ReleaseManifest{
		ReleaseID: "release_test",
		Version:   "0.5.1-test",
		Metadata:  map[string]any{"api_key": "secret-key"},
		BackupScope: &ReleaseBackupScope{
			SchemaVersion: ReleaseBackupScopeSchemaVersion,
			Class:         ReleaseBackupScopeOperationalOnly,
		},
	}); err != nil {
		t.Fatalf("WriteReleaseManifest returned error: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release manifest: %v", err)
	}
	if strings.Contains(string(raw), "secret-key") {
		t.Fatalf("release manifest leaked secret metadata:\n%s", string(raw))
	}
	loaded, err := ReadReleaseManifest(path)
	if err != nil {
		t.Fatalf("ReadReleaseManifest returned error: %v", err)
	}
	if loaded.Metadata["api_key"] != "[REDACTED]" {
		t.Fatalf("api key metadata was not redacted: %#v", loaded.Metadata)
	}
	if loaded.BackupScope == nil || loaded.BackupScope.SchemaVersion != ReleaseBackupScopeSchemaVersion || loaded.BackupScope.Class != ReleaseBackupScopeOperationalOnly {
		t.Fatalf("backup scope did not round-trip: %#v", loaded.BackupScope)
	}
}
