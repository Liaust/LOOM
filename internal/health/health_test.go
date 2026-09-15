package health

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/version"
)

func TestHealthReportsStorageAndMissingDatabase(t *testing.T) {
	dir := t.TempDir()
	serviceRoot := filepath.Join(dir, "service")
	storageRoot := filepath.Join(serviceRoot, "storage")
	dataRoot := filepath.Join(dir, "runtime")
	cfg := config.Config{
		Env:              "test",
		NodeID:           "test-main",
		NodeKind:         "main",
		NodeRole:         "main",
		RuntimeClass:     "main_full",
		DataDir:          dataRoot,
		ObjectStore:      filepath.Join(dataRoot, "object-store"),
		ServiceRoot:      serviceRoot,
		StorageRoot:      storageRoot,
		ImportsRoot:      filepath.Join(storageRoot, "imports"),
		UserBackupsRoot:  filepath.Join(storageRoot, "backups"),
		ArchiveRoot:      filepath.Join(storageRoot, "archive"),
		GeneratedRoot:    filepath.Join(dataRoot, "generated"),
		BoxStateRoot:     filepath.Join(dataRoot, "box-state"),
		BoxPath:          filepath.Join(serviceRoot, "box"),
		StorageExport:    filepath.Join(dataRoot, "storage-export"),
		StorageRetention: filepath.Join(dataRoot, "storage-retention"),
		MainDocuments:    filepath.Join(dataRoot, "main-documents"),
		SocketPath:       filepath.Join(dataRoot, "loomd.sock"),
		LogLevel:         "debug",
		MigrationsDir:    filepath.Join(dataRoot, "migrations"),
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	if err := ensureDir(cfg.ObjectStore); err != nil {
		t.Fatalf("create object store: %v", err)
	}

	report := NewService(cfg, version.Info{Version: "test"}).Check(context.Background())
	if report.Checks.Storage.Status != "ok" {
		t.Fatalf("storage status = %q, want ok", report.Checks.Storage.Status)
	}
	if report.Checks.Database.Status != "unhealthy" {
		t.Fatalf("database status = %q, want unhealthy", report.Checks.Database.Status)
	}
	if report.Status != "unhealthy" {
		t.Fatalf("report status = %q, want unhealthy", report.Status)
	}
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
