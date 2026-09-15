package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loom.local/loom/internal/backupcoverage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/response"
)

func TestBackupCoverageEndpointUsesDaemonRuntimeConfig(t *testing.T) {
	dataDir := t.TempDir()
	for _, dir := range []string{
		"object-store",
		"private-backups",
		"main-documents",
		"storage-retention",
		filepath.Join("storage-archive", "objects"),
		filepath.Join("storage-archive", "manifests"),
		"loom-notes",
	} {
		if err := os.MkdirAll(filepath.Join(dataDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	server := NewServer(Services{
		RuntimeConfig: config.Config{
			DataDir:       dataDir,
			ObjectStore:   filepath.Join(dataDir, "object-store"),
			MainDocuments: filepath.Join(dataDir, "main-documents"),
			StorageExport: filepath.Join(dataDir, "storage-views", "main-export"),
			BoxPath:       filepath.Join(dataDir, "box"),
		},
	}, nil).Handler()

	req := httptest.NewRequest(http.MethodGet, "/v1/backup/coverage", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope response.Envelope[backupcoverage.Report]
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v body=%s", err, rec.Body.String())
	}
	if envelope.Data.Mode != backupcoverage.ModeMainBacked {
		t.Fatalf("mode = %q, want %q", envelope.Data.Mode, backupcoverage.ModeMainBacked)
	}
	if envelope.Data.BackupRoot != filepath.Join(dataDir, "backups", "main") {
		t.Fatalf("backup root = %q", envelope.Data.BackupRoot)
	}
}
