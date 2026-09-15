package cloudstorage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"loom.local/loom/internal/maintenance"
)

const (
	SnapshotUploadManifestSchemaV063 = "loom.cloud.snapshot_upload.v0.6.3"
	SnapshotUploadManifestSchema     = "loom.cloud.snapshot_upload.v0.6.4"
)

type SnapshotUploadManifest struct {
	SchemaVersion              string                          `json:"schema_version"`
	UploadID                   string                          `json:"upload_id"`
	Backend                    string                          `json:"backend,omitempty"`
	Provider                   string                          `json:"provider,omitempty"`
	BackupOperationID          string                          `json:"backup_operation_id,omitempty"`
	SourceBackupDir            string                          `json:"source_backup_dir"`
	SourceBackupManifestPath   string                          `json:"source_backup_manifest_path,omitempty"`
	SourceBackupManifestSHA256 string                          `json:"source_backup_manifest_sha256,omitempty"`
	SourceBackupPaths          maintenance.BackupManifestPaths `json:"source_backup_paths,omitempty"`
	RemoteURI                  string                          `json:"remote_uri"`
	RemotePrefix               string                          `json:"remote_prefix"`
	SnapshotRef                string                          `json:"snapshot_ref,omitempty"`
	Repository                 string                          `json:"repository,omitempty"`
	Archive                    string                          `json:"archive,omitempty"`
	CreatedAt                  time.Time                       `json:"created_at"`
	CompletedAt                time.Time                       `json:"completed_at"`
	Driver                     string                          `json:"driver"`
	VerifyBeforeUpload         string                          `json:"verify_before_upload"`
	VerifyAfterUpload          string                          `json:"verify_after_upload"`
	FileCount                  int64                           `json:"file_count"`
	TotalBytes                 int64                           `json:"total_bytes"`
	PackedBytes                int64                           `json:"packed_bytes,omitempty"`
	DeduplicatedBytes          int64                           `json:"deduplicated_bytes,omitempty"`
	Compression                string                          `json:"compression,omitempty"`
	Encryption                 string                          `json:"encryption,omitempty"`
	Checks                     map[string]string               `json:"checks"`
	BackendMetadata            map[string]any                  `json:"backend_metadata,omitempty"`
}

func WriteSnapshotUploadManifest(path string, manifest SnapshotUploadManifest) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("snapshot upload manifest path is required")
	}
	if manifest.SchemaVersion == "" {
		manifest.SchemaVersion = SnapshotUploadManifestSchema
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloud-upload-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func ReadSnapshotUploadManifest(path string) (SnapshotUploadManifest, error) {
	payload, err := os.ReadFile(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return SnapshotUploadManifest{}, err
	}
	var manifest SnapshotUploadManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return SnapshotUploadManifest{}, err
	}
	switch strings.TrimSpace(manifest.SchemaVersion) {
	case SnapshotUploadManifestSchema, SnapshotUploadManifestSchemaV063:
	default:
		return SnapshotUploadManifest{}, fmt.Errorf("unsupported snapshot upload manifest schema %q", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.Backend) == "" {
		manifest.Backend = SnapshotBackendLegacyTree
	}
	return manifest, nil
}
