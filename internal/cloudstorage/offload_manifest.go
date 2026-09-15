package cloudstorage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const OffloadManifestSchema = "loom.cloud.offload.v0.6.3"

const (
	OffloadCustodyLocalPrimary      = "local_primary"
	OffloadCustodyReplicatedToCloud = "replicated_to_cloud"
	OffloadCustodyCloudOnly         = "cloud_only"

	OffloadLocalSourceRetained = "retain_until_manual_delete"
)

type OffloadManifest struct {
	SchemaVersion        string            `json:"schema_version"`
	OffloadID            string            `json:"offload_id"`
	SourceStorageRef     string            `json:"source_storage_ref"`
	SourceStorageEntryID string            `json:"source_storage_entry_id,omitempty"`
	SourceManifestID     string            `json:"source_manifest_id,omitempty"`
	SourcePath           string            `json:"source_path"`
	SourceIsDir          bool              `json:"source_is_dir"`
	SourceName           string            `json:"source_name"`
	NodeID               string            `json:"node_id"`
	RemoteURI            string            `json:"remote_uri"`
	RemotePrefix         string            `json:"remote_prefix"`
	PayloadRemoteURI     string            `json:"payload_remote_uri"`
	PayloadRemotePrefix  string            `json:"payload_remote_prefix"`
	ManifestRemoteURI    string            `json:"manifest_remote_uri"`
	ManifestRemotePath   string            `json:"manifest_remote_path"`
	CreatedAt            time.Time         `json:"created_at"`
	CompletedAt          time.Time         `json:"completed_at,omitempty"`
	Driver               string            `json:"driver"`
	Custody              string            `json:"custody"`
	LocalSourceAction    string            `json:"local_source_action"`
	FileCount            int64             `json:"file_count"`
	TotalBytes           int64             `json:"total_bytes"`
	Checks               map[string]string `json:"checks"`
}

func WriteOffloadManifest(path string, manifest OffloadManifest) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("offload manifest path is required")
	}
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		manifest.SchemaVersion = OffloadManifestSchema
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cloud-offload-*.json")
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

func ReadOffloadManifest(path string) (OffloadManifest, error) {
	payload, err := os.ReadFile(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return OffloadManifest{}, err
	}
	var manifest OffloadManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return OffloadManifest{}, err
	}
	if strings.TrimSpace(manifest.SchemaVersion) != OffloadManifestSchema {
		return OffloadManifest{}, fmt.Errorf("unsupported offload manifest schema %q", manifest.SchemaVersion)
	}
	return manifest, nil
}
