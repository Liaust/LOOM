package status

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/config"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/version"
)

func TestCheckReportsHealthAndDatabaseUnavailableSummaries(t *testing.T) {
	dir := t.TempDir()
	serviceRoot := filepath.Join(dir, "service")
	storageRoot := filepath.Join(serviceRoot, "storage")
	dataRoot := filepath.Join(dir, "runtime")
	objectStore := filepath.Join(dataRoot, "object-store")
	if err := os.MkdirAll(objectStore, 0o700); err != nil {
		t.Fatalf("create object store: %v", err)
	}
	cfg := config.Config{
		Env:              "test",
		NodeID:           "test-main",
		NodeKind:         "main",
		NodeRole:         "main",
		RuntimeClass:     "main_full",
		DataDir:          dataRoot,
		ObjectStore:      objectStore,
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

	report := NewService(nil, health.NewService(cfg, version.Info{Version: "test"})).Check(context.Background())
	if report.Health.Node.ID != "test-main" {
		t.Fatalf("node id = %q, want test-main", report.Health.Node.ID)
	}
	if report.Status != report.Health.Status {
		t.Fatalf("status = %q, want health status %q", report.Status, report.Health.Status)
	}
	if report.Jobs.Error == "" {
		t.Fatal("expected jobs database unavailable summary")
	}
	if report.Runner.Status != "unknown" {
		t.Fatalf("runner status = %q, want unknown", report.Runner.Status)
	}
}
