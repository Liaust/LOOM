package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/setup"
)

func DefaultStateDir(dataDir string) string {
	dataDir = strings.TrimSpace(dataDir)
	if dataDir == "" {
		dataDir = "/var/lib/loom"
	}
	return filepath.Join(filepath.Clean(dataDir), "update")
}

func ActiveManifestPath(stateDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(stateDir)), DefaultActiveUpdateManifestFileYAML)
}

func HistoryDir(stateDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(stateDir)), "history")
}

func PendingDir(stateDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(stateDir)), "pending")
}

func RollbackDir(stateDir string) string {
	return filepath.Join(filepath.Clean(strings.TrimSpace(stateDir)), "rollback")
}

func LoadReleaseManifest(releasePath string) (ReleaseManifest, string, error) {
	releasePath = filepath.Clean(strings.TrimSpace(releasePath))
	if releasePath == "." || releasePath == "" {
		return ReleaseManifest{}, "", fmt.Errorf("release path is required")
	}
	for _, name := range []string{DefaultReleaseManifestFileYAML, DefaultReleaseManifestFileJSON} {
		path := filepath.Join(releasePath, name)
		manifest, err := readReleaseManifestFile(path)
		if err == nil {
			manifest.SourcePath = firstNonEmpty(manifest.SourcePath, releasePath)
			return normalizeReleaseManifest(manifest, releasePath), path, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		return ReleaseManifest{}, path, err
	}
	return ReleaseManifest{}, "", fs.ErrNotExist
}

func ReadReleaseManifest(path string) (ReleaseManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return ReleaseManifest{}, fmt.Errorf("release manifest path is required")
	}
	manifest, err := readReleaseManifestFile(path)
	if err != nil {
		return ReleaseManifest{}, err
	}
	if strings.TrimSpace(manifest.SourcePath) == "" {
		manifest.SourcePath = filepath.Dir(filepath.Clean(path))
	}
	return normalizeReleaseManifest(manifest, manifest.SourcePath), nil
}

func readReleaseManifestFile(path string) (ReleaseManifest, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return ReleaseManifest{}, err
	}
	var manifest ReleaseManifest
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return ReleaseManifest{}, fmt.Errorf("parse release manifest json: %w", err)
		}
	} else if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return ReleaseManifest{}, fmt.Errorf("parse release manifest yaml: %w", err)
	}
	return manifest, nil
}

func WriteReleaseManifest(path string, manifest ReleaseManifest) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("release manifest path is required")
	}
	manifest = normalizeReleaseManifest(manifest, filepath.Dir(filepath.Clean(path)))
	raw, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Clean(path)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Clean(path), raw, 0o644)
}

func normalizeReleaseManifest(manifest ReleaseManifest, releasePath string) ReleaseManifest {
	manifest.SchemaVersion = firstNonEmpty(manifest.SchemaVersion, ReleaseManifestSchemaVersion)
	manifest.SourcePath = filepath.Clean(firstNonEmpty(manifest.SourcePath, releasePath))
	manifest.FlakeOutput = firstNonEmpty(manifest.FlakeOutput, DefaultProductionFlakeOutput)
	if strings.TrimSpace(manifest.MigrationsDir) != "" && !filepath.IsAbs(manifest.MigrationsDir) {
		manifest.MigrationsDir = filepath.Clean(filepath.Join(manifest.SourcePath, manifest.MigrationsDir))
	}
	if manifest.Metadata != nil {
		manifest.Metadata = setup.RedactMap(manifest.Metadata)
	}
	return manifest
}

func ReadUpdateManifest(path string) (UpdateManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return UpdateManifest{}, fmt.Errorf("update manifest path is required")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return UpdateManifest{}, err
	}
	var manifest UpdateManifest
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return UpdateManifest{}, fmt.Errorf("parse update manifest json: %w", err)
		}
	} else if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return UpdateManifest{}, fmt.Errorf("parse update manifest yaml: %w", err)
	}
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		return UpdateManifest{}, fmt.Errorf("update manifest schema_version is required")
	}
	if manifest.SchemaVersion != UpdateManifestSchemaVersion {
		return UpdateManifest{}, fmt.Errorf("unsupported update manifest schema_version %q", manifest.SchemaVersion)
	}
	return RedactUpdateManifest(manifest), nil
}

func WriteUpdateManifest(path string, manifest UpdateManifest) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("update manifest path is required")
	}
	manifest = RedactUpdateManifest(manifest)
	if strings.TrimSpace(manifest.SchemaVersion) == "" {
		manifest.SchemaVersion = UpdateManifestSchemaVersion
	}
	if strings.TrimSpace(manifest.UpdateID) == "" {
		manifest.UpdateID = updateIDFromManifest(manifest)
	}
	if manifest.StartedAt.IsZero() {
		manifest.StartedAt = time.Now().UTC()
	}
	raw, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	dir := filepath.Dir(filepath.Clean(path))
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".update-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	alignUpdateManifestPermissions(tmpPath, dir)
	if _, err := tmp.Write(raw); err != nil {
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
	return os.Rename(tmpPath, filepath.Clean(path))
}

func alignUpdateManifestPermissions(path, dir string) {
	if info, err := os.Stat(dir); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			_ = os.Chown(path, -1, int(stat.Gid))
		}
	}
	_ = os.Chmod(path, 0o640)
}

func RedactUpdateManifest(manifest UpdateManifest) UpdateManifest {
	manifest.Metadata = setup.RedactMap(manifest.Metadata)
	return manifest
}

func updateIDFromManifest(manifest UpdateManifest) string {
	raw, _ := json.Marshal(manifest)
	hash := sha256.Sum256(raw)
	return "update_" + hex.EncodeToString(hash[:])[:16]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
