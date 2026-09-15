package loomdapp

import (
	"path/filepath"
	"strings"
	"testing"

	"loom.local/loom/internal/config"
)

func TestRuntimeStorageRootsPreserveConfiguredCustody(t *testing.T) {
	root := t.TempDir()
	configuredBackups := filepath.Join(root, "custom-storage", "protected-copies")
	configuredArchive := filepath.Join(root, "custom-storage", "archive-custody")
	configuredStorage := filepath.Join(root, "custom-storage")
	roots := runtimeStorageRoots(config.Config{
		DataDir:                  filepath.Join(root, "runtime"),
		StorageRoot:              configuredStorage,
		CanonicalUserBackupsRoot: configuredBackups,
		UserBackupsRoot:          configuredBackups,
		ArchiveRoot:              configuredArchive,
	})
	if roots.Storage != configuredStorage || roots.CanonicalUserBackups != configuredBackups {
		t.Fatalf("canonical roots = %#v", roots)
	}
	if roots.UserBackups != configuredBackups {
		t.Fatalf("UserBackups = %q, want %q", roots.UserBackups, configuredBackups)
	}
	if roots.Archive != configuredArchive {
		t.Fatalf("Archive = %q, want %q", roots.Archive, configuredArchive)
	}
	if roots.ArchiveRuntimeManifests != filepath.Join(configuredArchive, "runtime-manifests") {
		t.Fatalf("ArchiveRuntimeManifests = %q", roots.ArchiveRuntimeManifests)
	}
	if roots.UserBackups == filepath.Join(root, "runtime", "private-backups") || roots.Archive == filepath.Join(root, "runtime", "storage-archive") || strings.HasPrefix(roots.ArchiveRuntimeManifests, filepath.Join(root, "runtime")+string(filepath.Separator)) {
		t.Fatalf("runtime roots leaked into physical custody: %#v", roots)
	}
}

func TestRuntimeStorageRootsPreserveProductionPreCutoverCanonicalIdentity(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "var-lib-loom")
	legacyExport := filepath.Join(dataRoot, "storage-views", "main-export")
	storageRoot := filepath.Join(t.TempDir(), "srv-loom", "storage")
	roots := runtimeStorageRoots(config.Config{
		DataDir:                  dataRoot,
		StorageRoot:              storageRoot,
		CanonicalUserBackupsRoot: filepath.Join(storageRoot, "backups"),
		UserBackupsRoot:          filepath.Join(dataRoot, "private-backups"),
		ArchiveRoot:              filepath.Join(dataRoot, "storage-archive"),
		StorageExport:            legacyExport,
	})
	if roots.Storage != storageRoot || roots.CanonicalUserBackups != filepath.Join(storageRoot, "backups") || roots.UserBackups != filepath.Join(dataRoot, "private-backups") || roots.Archive != filepath.Join(dataRoot, "storage-archive") || roots.ArchiveRuntimeManifests != filepath.Join(dataRoot, "storage-archive", "runtime-manifests") {
		t.Fatalf("pre-cutover runtime roots = %#v", roots)
	}
	if roots.Storage == legacyExport || roots.CanonicalUserBackups == legacyExport {
		t.Fatal("loomd conflated runtime custody ownership with the legacy SMB inspection export")
	}
}
