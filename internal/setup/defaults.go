package setup

import (
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/box"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/config"
	"loom.local/loom/internal/filesystemlayout"
	"loom.local/loom/internal/nodeprofiles"
)

func NormalizeSpec(input SetupSpec, facts TargetFacts) (SetupSpec, []Diagnostic, error) {
	spec := trimSpec(input)
	diagnostics := []Diagnostic{}
	if spec.SchemaVersion == "" {
		spec.SchemaVersion = SchemaVersion
	}
	if spec.NodeKind == "" {
		spec.NodeKind = "workspace"
	}
	if spec.RuntimeClass == "" {
		spec.RuntimeClass = defaultRuntimeClass(spec.NodeKind, spec.NodeRole)
	}
	if spec.NodeRole == "" {
		spec.NodeRole = defaultNodeRole(spec.NodeKind, spec.RuntimeClass)
	}
	if spec.NodeKey == "" {
		spec.NodeKey = defaultNodeKey(facts)
	}
	if spec.DisplayName == "" {
		spec.DisplayName = spec.NodeKey
	}
	if spec.UserName == "" {
		spec.UserName = facts.UserName
	}
	if spec.HomeDir == "" {
		spec.HomeDir = facts.HomeDir
	}
	if spec.InstallMode == "" {
		spec.InstallMode = defaultInstallMode(spec.NodeKind)
	}
	if spec.ServiceManager == "" {
		spec.ServiceManager = defaultServiceManager(spec.InstallMode, facts)
	}
	if spec.PackageMode == "" {
		spec.PackageMode = PackageModeLocalBuild
	}
	if spec.MigrationsDir == "" {
		spec.MigrationsDir = config.DefaultMigrationsDir
	}

	profile, err := nodeprofiles.Resolve(nodeprofiles.ResolveInput{
		NodeKind:     spec.NodeKind,
		NodeRole:     spec.NodeRole,
		RuntimeClass: spec.RuntimeClass,
	})
	if err != nil {
		return spec, diagnostics, err
	}
	for _, warning := range profile.Warnings {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: warning.Severity,
			Code:     warning.Code,
			Message:  warning.Message,
			Field:    warning.Field,
		})
	}
	if spec.AuthorityProfile == "" {
		spec.AuthorityProfile = profile.AuthorityProfileKey
	}
	if spec.RuntimeProfile == "" {
		spec.RuntimeProfile = profile.RuntimeProfileKey
	}

	spec = defaultFeatureFlags(spec)
	spec = defaultBootstrapMode(spec)
	spec = defaultPaths(spec)
	if err := validateSpecShape(spec, &diagnostics); err != nil {
		return spec, diagnostics, err
	}
	return spec, diagnostics, nil
}

func trimSpec(input SetupSpec) SetupSpec {
	input.SchemaVersion = strings.TrimSpace(input.SchemaVersion)
	input.NodeKey = strings.TrimSpace(input.NodeKey)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.NodeKind = normalizeToken(input.NodeKind)
	input.NodeRole = normalizeToken(input.NodeRole)
	input.RuntimeClass = normalizeToken(input.RuntimeClass)
	input.MainURL = strings.TrimSpace(input.MainURL)
	input.InstallMode = normalizeToken(input.InstallMode)
	input.ServiceManager = normalizeToken(input.ServiceManager)
	input.PackageMode = normalizeToken(input.PackageMode)
	input.UserName = strings.TrimSpace(input.UserName)
	input.HomeDir = cleanPathSoft(input.HomeDir)
	input.SourcePath = cleanPathSoft(input.SourcePath)
	input.SourceCommit = strings.TrimSpace(input.SourceCommit)
	input.MigrationsDir = cleanPathSoft(input.MigrationsDir)
	input.ConfigDir = cleanPathSoft(input.ConfigDir)
	input.DataDir = cleanPathSoft(input.DataDir)
	input.StateDir = cleanPathSoft(input.StateDir)
	input.LogDir = cleanPathSoft(input.LogDir)
	input.ServiceRoot = cleanPathSoft(input.ServiceRoot)
	input.StorageRoot = cleanPathSoft(input.StorageRoot)
	input.ImportsRoot = cleanPathSoft(input.ImportsRoot)
	input.UserBackupsRoot = cleanPathSoft(input.UserBackupsRoot)
	input.ArchiveRoot = cleanPathSoft(input.ArchiveRoot)
	input.GeneratedRoot = cleanPathSoft(input.GeneratedRoot)
	input.BoxStateRoot = cleanPathSoft(input.BoxStateRoot)
	input.BoxPath = cleanPathSoft(input.BoxPath)
	input.BoxProfile = normalizeToken(input.BoxProfile)
	input.NodeAgentConfigPath = cleanPathSoft(input.NodeAgentConfigPath)
	input.NodeAgentStatePath = cleanPathSoft(input.NodeAgentStatePath)
	input.NodeAgentDataDir = cleanPathSoft(input.NodeAgentDataDir)
	input.ObjectStorePath = cleanPathSoft(input.ObjectStorePath)
	input.MainDocumentsPath = cleanPathSoft(input.MainDocumentsPath)
	input.StorageExportRoot = cleanPathSoft(input.StorageExportRoot)
	input.SocketPath = cleanPathSoft(input.SocketPath)
	input.CloudConfigPath = cleanPathSoft(input.CloudConfigPath)
	input.CloudStateDir = cleanPathSoft(input.CloudStateDir)
	input.CloudRcloneConfigPath = cleanPathSoft(input.CloudRcloneConfigPath)
	input.CloudRemoteName = strings.TrimSpace(input.CloudRemoteName)
	input.CloudRemoteRoot = strings.TrimSpace(input.CloudRemoteRoot)
	input.CloudSnapshotBackend = normalizeToken(input.CloudSnapshotBackend)
	input.CloudBorgRepository = strings.TrimSpace(input.CloudBorgRepository)
	input.CloudBorgPassphraseFile = cleanPathSoft(input.CloudBorgPassphraseFile)
	input.CloudBorgCacheDir = cleanPathSoft(input.CloudBorgCacheDir)
	input.CloudBorgSecurityDir = cleanPathSoft(input.CloudBorgSecurityDir)
	input.HTTPListenAddr = strings.TrimSpace(input.HTTPListenAddr)
	input.DBURL = strings.TrimSpace(input.DBURL)
	input.AuthorityProfile = normalizeToken(input.AuthorityProfile)
	input.RuntimeProfile = normalizeToken(input.RuntimeProfile)
	input.ProviderMode = normalizeToken(input.ProviderMode)
	for i := range input.SafeRoots {
		input.SafeRoots[i].Name = normalizeToken(input.SafeRoots[i].Name)
		input.SafeRoots[i].Path = cleanPathSoft(input.SafeRoots[i].Path)
		input.SafeRoots[i].Mode = normalizeToken(input.SafeRoots[i].Mode)
	}
	input.BootstrapMode = normalizeToken(input.BootstrapMode)
	if input.EnrollmentTTLSeconds < 0 {
		input.EnrollmentTTLSeconds = 0
	}
	return input
}

func defaultFeatureFlags(spec SetupSpec) SetupSpec {
	switch spec.NodeKind {
	case "main":
		spec.EnableLoomd = true
		spec.EnableNodeAgent = false
		spec.EnableBox = true
		spec.EnableDropzone = false
		spec.EnableWatchedRoots = false
		spec.EnableProviders = true
		spec.EnableStorageCatalog = true
		spec.EnableStorageExportWorker = false
		spec.EnableMainDocumentsWorker = true
		spec.EnableStorageRetention = true
		spec.EnableProjectArchiveRuntime = true
		spec.EnableCloud = true
		spec.AutoMigrate = true
		spec.ProductionBootstrap = true
	case "workspace":
		spec.EnableLoomd = false
		spec.EnableNodeAgent = true
		spec.EnableBox = true
		spec.EnableDropzone = false
		spec.EnableWatchedRoots = true
		spec.EnableProviders = spec.RuntimeClass == nodeprofiles.RuntimeWorkspaceFull
		spec.SkipEnroll = true
		spec.ApproveEnrollment = true
		spec.VerifyHeartbeat = true
	case "hardware":
		spec.EnableLoomd = false
		spec.EnableNodeAgent = true
		spec.EnableBox = false
		spec.EnableDropzone = false
		spec.EnableWatchedRoots = false
		spec.EnableProviders = spec.NodeRole == "capability_node" || spec.NodeRole == "compute_node"
		spec.SkipEnroll = true
		spec.ApproveEnrollment = true
		spec.VerifyHeartbeat = true
	case "integration":
		spec.EnableLoomd = false
		spec.EnableNodeAgent = true
		spec.EnableBox = false
		spec.EnableDropzone = false
		spec.EnableWatchedRoots = false
		spec.EnableProviders = true
		spec.SkipEnroll = true
		spec.ApproveEnrollment = true
		spec.VerifyHeartbeat = true
	default:
		spec.EnableLoomd = false
		spec.EnableNodeAgent = false
		spec.EnableBox = false
		spec.EnableDropzone = false
		spec.EnableWatchedRoots = false
		spec.EnableProviders = false
	}
	if spec.RunEnrollment {
		spec.SkipEnroll = false
	}
	if spec.EnrollmentTTLSeconds <= 0 {
		spec.EnrollmentTTLSeconds = 1800
	}
	return spec
}

func defaultBootstrapMode(spec SetupSpec) SetupSpec {
	switch spec.BootstrapMode {
	case "":
		if spec.ProductionBootstrap {
			spec.BootstrapMode = "production"
		} else {
			spec.BootstrapMode = config.DefaultBootstrapMode
		}
	case config.DefaultBootstrapMode, "dev":
		spec.ProductionBootstrap = false
	case "production":
		spec.ProductionBootstrap = true
	}
	return spec
}

func defaultPaths(spec SetupSpec) SetupSpec {
	home := spec.HomeDir
	if spec.NodeKind == "main" {
		if spec.ServiceRoot == "" {
			spec.ServiceRoot = config.DefaultServiceRoot
		}
		if spec.StorageRoot == "" {
			spec.StorageRoot = filepath.Join(spec.ServiceRoot, "storage")
		}
		if spec.ImportsRoot == "" {
			spec.ImportsRoot = filepath.Join(spec.StorageRoot, "imports")
		}
		if spec.UserBackupsRoot == "" {
			spec.UserBackupsRoot = filepath.Join(spec.StorageRoot, "backups")
		}
		if spec.ArchiveRoot == "" {
			spec.ArchiveRoot = filepath.Join(spec.StorageRoot, "archive")
		}
	}
	if spec.InstallMode == InstallModeService && spec.ServiceManager != ServiceManagerLaunchd {
		if spec.ConfigDir == "" {
			spec.ConfigDir = "/etc/loom"
		}
		if spec.DataDir == "" {
			spec.DataDir = config.DefaultDataDir
		}
		if spec.StateDir == "" {
			spec.StateDir = filepath.Join(spec.DataDir, "state")
		}
		if spec.LogDir == "" {
			spec.LogDir = "/var/log/loom"
		}
		if spec.ObjectStorePath == "" && spec.NodeKind == "main" {
			spec.ObjectStorePath = config.DefaultObjectStore
		}
		if spec.MainDocumentsPath == "" && spec.NodeKind == "main" {
			spec.MainDocumentsPath = config.DefaultMainDocuments
		}
		if spec.SocketPath == "" && spec.NodeKind == "main" {
			spec.SocketPath = config.DefaultSocketPath
		}
	} else {
		if spec.ConfigDir == "" && home != "" {
			spec.ConfigDir = filepath.Join(home, ".config", "loom")
		}
		if spec.DataDir == "" && home != "" {
			spec.DataDir = filepath.Join(home, ".local", "share", "loom")
		}
		if spec.StateDir == "" && home != "" {
			spec.StateDir = filepath.Join(home, ".local", "state", "loom")
		}
		if spec.LogDir == "" && spec.StateDir != "" {
			spec.LogDir = filepath.Join(spec.StateDir, "logs")
		}
	}
	if spec.BoxProfile == "" {
		if spec.NodeKind == "main" {
			spec.BoxProfile = "main"
		} else {
			spec.BoxProfile = "workspace"
		}
	}
	if spec.EnableBox && spec.BoxPath == "" {
		if spec.NodeKind == "main" {
			spec.BoxPath = filepath.Join(spec.ServiceRoot, "box")
		} else if home != "" {
			spec.BoxPath = filepath.Join(home, box.DefaultRootDirName)
		}
	}
	if spec.GeneratedRoot == "" && spec.DataDir != "" {
		spec.GeneratedRoot = filepath.Join(spec.DataDir, "generated")
	}
	if spec.EnableBox && spec.BoxStateRoot == "" && spec.DataDir != "" {
		spec.BoxStateRoot = filepath.Join(spec.DataDir, "box-state")
	}
	if spec.ProviderMode == "" {
		if spec.EnableProviders {
			spec.ProviderMode = "enabled"
		} else {
			spec.ProviderMode = "disabled"
		}
	}
	if spec.ObjectStorePath == "" && spec.NodeKind == "main" && spec.DataDir != "" {
		spec.ObjectStorePath = filepath.Join(spec.DataDir, "object-store")
	}
	if spec.MainDocumentsPath == "" && spec.NodeKind == "main" && spec.DataDir != "" {
		spec.MainDocumentsPath = filepath.Join(spec.DataDir, "main-documents")
	}
	if spec.SocketPath == "" && spec.NodeKind == "main" {
		spec.SocketPath = config.DefaultSocketPath
	}
	if spec.EnableCloud && spec.NodeKind == "main" {
		if spec.CloudConfigPath == "" {
			if spec.ConfigDir != "" {
				spec.CloudConfigPath = filepath.Join(spec.ConfigDir, "cloud", "config.json")
			} else {
				spec.CloudConfigPath = cloudstorage.DefaultConfigPath
			}
		}
		if spec.CloudRcloneConfigPath == "" {
			if spec.ConfigDir != "" {
				spec.CloudRcloneConfigPath = filepath.Join(spec.ConfigDir, "cloud", "rclone.conf")
			} else {
				spec.CloudRcloneConfigPath = cloudstorage.DefaultRcloneConfigPath
			}
		}
		if spec.CloudStateDir == "" && spec.DataDir != "" {
			spec.CloudStateDir = filepath.Join(spec.DataDir, "cloud")
		}
		if spec.CloudRemoteName == "" {
			spec.CloudRemoteName = cloudstorage.DefaultRemoteName
		}
		if spec.CloudRemoteRoot == "" {
			spec.CloudRemoteRoot = cloudstorage.DefaultRemoteRoot
		}
		if spec.CloudSnapshotBackend == "" {
			spec.CloudSnapshotBackend = cloudstorage.DefaultSnapshotBackend
		}
		if spec.CloudBorgPassphraseFile == "" {
			spec.CloudBorgPassphraseFile = filepath.Join(filepath.Dir(spec.CloudConfigPath), "borg.passphrase")
		}
		if spec.CloudBorgCacheDir == "" && spec.CloudStateDir != "" {
			spec.CloudBorgCacheDir = filepath.Join(spec.CloudStateDir, "borg", "cache")
		}
		if spec.CloudBorgSecurityDir == "" && spec.CloudStateDir != "" {
			spec.CloudBorgSecurityDir = filepath.Join(spec.CloudStateDir, "borg", "security")
		}
	}
	return spec
}

func defaultRuntimeClass(kind, role string) string {
	kind = normalizeToken(kind)
	role = normalizeToken(role)
	switch kind {
	case "main":
		return nodeprofiles.RuntimeMainFull
	case "workspace":
		if role == "secondary_workspace" {
			return nodeprofiles.RuntimeWorkspaceLight
		}
		return nodeprofiles.RuntimeWorkspaceFull
	case "hardware":
		switch role {
		case "compute_node":
			return nodeprofiles.RuntimeComputeRunner
		case "storage_node":
			return nodeprofiles.RuntimeStorageEdge
		default:
			return nodeprofiles.RuntimeHardwareAgent
		}
	case "integration":
		return nodeprofiles.RuntimeHardwareAgent
	case "guest":
		return nodeprofiles.RuntimeGuestRestricted
	default:
		return nodeprofiles.RuntimeGuestRestricted
	}
}

func defaultNodeRole(kind, runtimeClass string) string {
	kind = normalizeToken(kind)
	runtimeClass = normalizeToken(runtimeClass)
	switch kind {
	case "main":
		return "main"
	case "workspace":
		if runtimeClass == nodeprofiles.RuntimeWorkspaceLight {
			return "secondary_workspace"
		}
		return "primary_workspace"
	case "hardware":
		switch runtimeClass {
		case nodeprofiles.RuntimeComputeRunner:
			return "compute_node"
		case nodeprofiles.RuntimeStorageEdge:
			return "storage_node"
		default:
			return "capability_node"
		}
	case "integration":
		return "automation_edge"
	case "guest":
		return "guest"
	default:
		return "guest"
	}
}

func defaultInstallMode(kind string) string {
	if normalizeToken(kind) == "main" {
		return InstallModeService
	}
	return InstallModeUser
}

func defaultServiceManager(installMode string, facts TargetFacts) string {
	if installMode != InstallModeService {
		return ServiceManagerNone
	}
	if facts.HasSystemd {
		return ServiceManagerSystemd
	}
	if facts.HasLaunchd {
		return ServiceManagerLaunchd
	}
	return ServiceManagerNone
}

func defaultNodeKey(facts TargetFacts) string {
	host := normalizeToken(facts.Hostname)
	if host == "" {
		return "loom-node"
	}
	replacer := strings.NewReplacer(".", "-", "_", "-", " ", "-")
	return strings.Trim(replacer.Replace(host), "-")
}

func validateSpecShape(spec SetupSpec, diagnostics *[]Diagnostic) error {
	switch spec.InstallMode {
	case InstallModeService, InstallModeUser, InstallModeDeveloper:
	default:
		return fmt.Errorf("unsupported install_mode: %s", spec.InstallMode)
	}
	switch spec.ServiceManager {
	case ServiceManagerSystemd, ServiceManagerLaunchd, ServiceManagerNone:
	default:
		return fmt.Errorf("unsupported service_manager: %s", spec.ServiceManager)
	}
	switch spec.BootstrapMode {
	case config.DefaultBootstrapMode, "dev", "production":
	default:
		return fmt.Errorf("unsupported bootstrap_mode: %s", spec.BootstrapMode)
	}
	if spec.NodeKey == "" {
		return fmt.Errorf("node_key is required")
	}
	if spec.DisplayName == "" {
		return fmt.Errorf("display_name is required")
	}
	if spec.NodeKind != "main" && strings.TrimSpace(spec.MainURL) == "" {
		*diagnostics = append(*diagnostics, Diagnostic{
			Severity:   DiagnosticError,
			Code:       "setup.main_url_required",
			Message:    "Non-main setup requires a main URL before enrollment can run.",
			Field:      "main_url",
			RepairHint: "Pass --main-url with the private URL of the main node.",
		})
	}
	for field, path := range map[string]string{
		"config_dir":                 spec.ConfigDir,
		"data_dir":                   spec.DataDir,
		"state_dir":                  spec.StateDir,
		"log_dir":                    spec.LogDir,
		"service_root":               spec.ServiceRoot,
		"storage_root":               spec.StorageRoot,
		"imports_root":               spec.ImportsRoot,
		"user_backups_root":          spec.UserBackupsRoot,
		"archive_root":               spec.ArchiveRoot,
		"generated_root":             spec.GeneratedRoot,
		"box_state_root":             spec.BoxStateRoot,
		"box_path":                   spec.BoxPath,
		"node_agent_config_path":     spec.NodeAgentConfigPath,
		"node_agent_state_path":      spec.NodeAgentStatePath,
		"node_agent_data_dir":        spec.NodeAgentDataDir,
		"object_store_path":          spec.ObjectStorePath,
		"main_documents_path":        spec.MainDocumentsPath,
		"storage_export_root":        spec.StorageExportRoot,
		"socket_path":                spec.SocketPath,
		"cloud_config_path":          spec.CloudConfigPath,
		"cloud_state_dir":            spec.CloudStateDir,
		"cloud_rclone_config_path":   spec.CloudRcloneConfigPath,
		"cloud_borg_passphrase_file": spec.CloudBorgPassphraseFile,
		"cloud_borg_cache_dir":       spec.CloudBorgCacheDir,
		"cloud_borg_security_dir":    spec.CloudBorgSecurityDir,
	} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticError,
				Code:       "setup.path_not_absolute",
				Message:    fmt.Sprintf("%s must be an absolute path.", field),
				Field:      field,
				Path:       path,
				RepairHint: "Use an absolute path.",
			})
		}
	}
	if spec.NodeKind == "main" {
		if _, err := filesystemlayout.New(filesystemlayout.Options{
			ServiceRoot:                 spec.ServiceRoot,
			BoxRoot:                     spec.BoxPath,
			StorageRoot:                 spec.StorageRoot,
			ImportsRoot:                 spec.ImportsRoot,
			UserBackupsRoot:             spec.UserBackupsRoot,
			ArchiveRoot:                 spec.ArchiveRoot,
			DataRoot:                    spec.DataDir,
			BoxStateRoot:                spec.BoxStateRoot,
			GeneratedRoot:               spec.GeneratedRoot,
			DeprecatedStorageExportRoot: spec.StorageExportRoot,
			DeprecatedMainDocumentsRoot: spec.MainDocumentsPath,
		}); err != nil {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticError,
				Code:       "setup.canonical_layout_invalid",
				Message:    err.Error(),
				Field:      "service_root",
				RepairHint: "Review the canonical filesystem root overrides before applying setup.",
			})
		}
	}
	for _, root := range spec.SafeRoots {
		if root.Path == "" {
			continue
		}
		if !filepath.IsAbs(root.Path) {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticError,
				Code:       "setup.safe_root_path_not_absolute",
				Message:    fmt.Sprintf("safe root %s path must be absolute.", root.Name),
				Field:      "safe_roots",
				Path:       root.Path,
				RepairHint: "Use an absolute path.",
			})
		}
	}
	if spec.EnableCloud {
		if spec.NodeKind != "main" {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticError,
				Code:       "setup.cloud_main_only",
				Message:    "Cloud managed operations are main-node only.",
				Field:      "enable_cloud",
				RepairHint: "Disable cloud on non-main nodes.",
			})
		}
		if spec.CloudRemoteRoot != "" {
			if err := cloudstorage.ValidateRemotePrefix("cloud_remote_root", spec.CloudRemoteRoot); err != nil {
				*diagnostics = append(*diagnostics, Diagnostic{
					Severity:   DiagnosticError,
					Code:       "setup.cloud_remote_root_invalid",
					Message:    err.Error(),
					Field:      "cloud_remote_root",
					RepairHint: "Use a relative remote prefix such as loom.",
				})
			}
		}
		switch spec.CloudSnapshotBackend {
		case "", cloudstorage.SnapshotBackendLegacyTree, cloudstorage.SnapshotBackendBorg:
		default:
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticError,
				Code:       "setup.cloud_snapshot_backend_invalid",
				Message:    fmt.Sprintf("unsupported cloud snapshot backend %q", spec.CloudSnapshotBackend),
				Field:      "cloud_snapshot_backend",
				RepairHint: "Use legacy_tree until Borg credentials are ready, then switch to borg.",
			})
		}
		if spec.CloudSnapshotBackend == cloudstorage.SnapshotBackendBorg && strings.TrimSpace(spec.CloudBorgRepository) == "" {
			*diagnostics = append(*diagnostics, Diagnostic{
				Severity:   DiagnosticWarning,
				Code:       "setup.cloud_borg_repository_missing",
				Message:    "Borg snapshot backend is selected but no Borg repository is configured.",
				Field:      "cloud_borg_repository",
				RepairHint: "Set cloud_borg_repository in the setup spec and snapshots.borg.repository in /etc/loom/cloud/config.json before enabling Borg in production.",
			})
		}
	}
	return nil
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func cleanPathSoft(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}
