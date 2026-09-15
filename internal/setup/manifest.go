package setup

import (
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

	"loom.local/loom/internal/version"
)

func LoadSpecFile(path string) (SetupSpec, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return SetupSpec{}, fmt.Errorf("spec path is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return SetupSpec{}, fmt.Errorf("read setup spec: %w", err)
	}
	var spec SetupSpec
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(payload, &spec); err != nil {
			return SetupSpec{}, fmt.Errorf("parse setup spec json: %w", err)
		}
		return spec, nil
	}
	if err := yaml.Unmarshal(payload, &spec); err != nil {
		return SetupSpec{}, fmt.Errorf("parse setup spec yaml: %w", err)
	}
	return spec, nil
}

func LoadPlanFile(path string) (SetupPlan, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return SetupPlan{}, fmt.Errorf("plan path is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return SetupPlan{}, fmt.Errorf("read setup plan: %w", err)
	}
	var plan SetupPlan
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(payload, &plan); err != nil {
			return SetupPlan{}, fmt.Errorf("parse setup plan json: %w", err)
		}
	} else if err := yaml.Unmarshal(payload, &plan); err != nil {
		return SetupPlan{}, fmt.Errorf("parse setup plan yaml: %w", err)
	}
	if strings.TrimSpace(plan.SchemaVersion) == "" {
		return SetupPlan{}, fmt.Errorf("setup plan schema_version is required")
	}
	if plan.SchemaVersion != SchemaVersion {
		return SetupPlan{}, fmt.Errorf("unsupported setup plan schema_version %q", plan.SchemaVersion)
	}
	if strings.TrimSpace(plan.PlanHash) == "" {
		plan.PlanHash = HashPlan(plan)
	}
	if strings.TrimSpace(plan.PlanID) == "" && len(strings.TrimPrefix(plan.PlanHash, "sha256:")) >= 16 {
		plan.PlanID = "setup_plan_" + strings.TrimPrefix(plan.PlanHash, "sha256:")[:16]
	}
	return plan, nil
}

func ReadManifest(path string) (InstallManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return InstallManifest{}, fmt.Errorf("manifest path is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return InstallManifest{}, err
	}
	var manifest InstallManifest
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		if err := json.Unmarshal(payload, &manifest); err != nil {
			return InstallManifest{}, fmt.Errorf("parse install manifest json: %w", err)
		}
	} else if err := yaml.Unmarshal(payload, &manifest); err != nil {
		return InstallManifest{}, fmt.Errorf("parse install manifest yaml: %w", err)
	}
	if manifest.SchemaVersion == "" {
		return InstallManifest{}, fmt.Errorf("install manifest schema_version is required")
	}
	return RedactManifest(manifest), nil
}

func WriteManifest(path string, manifest InstallManifest) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("manifest path is required")
	}
	manifest = RedactManifest(manifest)
	if manifest.SchemaVersion == "" {
		manifest.SchemaVersion = ManifestSchemaVersion
	}
	now := time.Now().UTC()
	if manifest.InstalledAt.IsZero() {
		manifest.InstalledAt = now
	}
	manifest.UpdatedAt = now
	payload, err := yaml.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal install manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".install-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(payload); err != nil {
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
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return normalizeManifestFileAccess(path)
}

func normalizeManifestFileAccess(path string) error {
	if filepath.Clean(path) != "/etc/loom/install.yaml" {
		return nil
	}
	if os.Geteuid() == 0 {
		parentInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			return err
		}
		if stat, ok := parentInfo.Sys().(*syscall.Stat_t); ok {
			if err := os.Chown(path, -1, int(stat.Gid)); err != nil {
				return err
			}
		}
	}
	return os.Chmod(path, 0o640)
}

func InspectManifest(path string) ManifestInspection {
	manifest, err := ReadManifest(path)
	if err == nil {
		return ManifestInspection{Path: path, Exists: true, Manifest: &manifest}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ManifestInspection{Path: path}
	}
	return ManifestInspection{Path: path, Error: err.Error()}
}

func ManifestFromPlan(plan SetupPlan) InstallManifest {
	now := plan.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return RedactManifest(InstallManifest{
		SchemaVersion:           ManifestSchemaVersion,
		InstallID:               installIDFromPlan(plan),
		InstalledAt:             now,
		UpdatedAt:               now,
		SetupVersion:            version.Current().Version,
		SourcePath:              plan.Spec.SourcePath,
		SourceCommit:            plan.Spec.SourceCommit,
		MigrationsDir:           plan.Spec.MigrationsDir,
		PlanHash:                plan.PlanHash,
		NodeKey:                 plan.Spec.NodeKey,
		DisplayName:             plan.Spec.DisplayName,
		NodeKind:                plan.Spec.NodeKind,
		NodeRole:                plan.Spec.NodeRole,
		RuntimeClass:            plan.Spec.RuntimeClass,
		MainURL:                 plan.Spec.MainURL,
		AuthorityProfile:        plan.Profile.AuthorityProfileKey,
		RuntimeProfile:          plan.Profile.RuntimeProfileKey,
		InstallMode:             plan.Spec.InstallMode,
		ServiceManager:          plan.Spec.ServiceManager,
		PackageMode:             plan.Spec.PackageMode,
		UserName:                plan.Spec.UserName,
		HomeDir:                 plan.Spec.HomeDir,
		ConfigDir:               plan.Paths.ConfigDir,
		DataDir:                 plan.Paths.DataDir,
		StateDir:                plan.Paths.StateDir,
		LogDir:                  plan.Paths.LogDir,
		ServiceRoot:             plan.Paths.ServiceRoot,
		StorageRoot:             plan.Paths.StorageRoot,
		ImportsRoot:             plan.Paths.ImportsRoot,
		UserBackupsRoot:         plan.Paths.UserBackupsRoot,
		ArchiveRoot:             plan.Paths.ArchiveRoot,
		GeneratedRoot:           plan.Paths.GeneratedRoot,
		BoxStateRoot:            plan.Paths.BoxStateRoot,
		BoxPath:                 plan.Paths.BoxPath,
		BoxProfile:              plan.Spec.BoxProfile,
		NodeAgentConfigPath:     plan.Paths.NodeAgentConfigPath,
		NodeAgentStatePath:      plan.Paths.NodeAgentStatePath,
		NodeAgentDataDir:        plan.Paths.NodeAgentDataDir,
		ObjectStorePath:         plan.Paths.ObjectStorePath,
		MainDocumentsPath:       plan.Paths.MainDocumentsPath,
		StorageExportRoot:       plan.Paths.StorageExportRoot,
		SocketPath:              plan.Paths.SocketPath,
		HTTPListenAddr:          plan.Spec.HTTPListenAddr,
		EnableCloud:             plan.Spec.EnableCloud,
		CloudConfigPath:         plan.Paths.CloudConfigPath,
		CloudStateDir:           plan.Paths.CloudStateDir,
		CloudRcloneConfigPath:   plan.Paths.CloudRcloneConfigPath,
		CloudRemoteName:         plan.Spec.CloudRemoteName,
		CloudRemoteRoot:         plan.Spec.CloudRemoteRoot,
		CloudSnapshotBackend:    plan.Spec.CloudSnapshotBackend,
		CloudBorgRepository:     plan.Spec.CloudBorgRepository,
		CloudBorgPassphraseFile: plan.Paths.CloudBorgPassphraseFile,
		CloudBorgCacheDir:       plan.Paths.CloudBorgCacheDir,
		CloudBorgSecurityDir:    plan.Paths.CloudBorgSecurityDir,
		BootstrapMode:           plan.Spec.BootstrapMode,
		ProviderMode:            plan.Spec.ProviderMode,
		SafeRoots:               append([]SafeRootSpec{}, plan.Spec.SafeRoots...),
		Services:                installedServicesFromPlan(plan),
		LastStatus:              SetupStatusSummary{Status: "planned"},
	})
}

func installIDFromPlan(plan SetupPlan) string {
	hash := strings.TrimPrefix(plan.PlanHash, "sha256:")
	if len(hash) >= 16 {
		return "install_" + hash[:16]
	}
	return "install_pending"
}

func RedactManifest(manifest InstallManifest) InstallManifest {
	manifest.Metadata = RedactMap(manifest.Metadata)
	manifest.Extra = nil
	return manifest
}

func RedactMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		if IsSensitiveKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = RedactValue(value)
	}
	return out
}

func RedactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return RedactMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = RedactValue(item)
		}
		return out
	default:
		return typed
	}
}

func IsSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, part := range []string{"token", "password", "api_key", "apikey", "secret", "credential_token"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}
