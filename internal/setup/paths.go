package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func BuildPathPlan(spec SetupSpec) (PathPlan, error) {
	manifest, source, err := ResolveManifestPath(ManifestPathInput{Spec: spec})
	if err != nil {
		return PathPlan{}, err
	}
	_ = source
	plan := PathPlan{
		ManifestPath:            manifest,
		ConfigDir:               spec.ConfigDir,
		DataDir:                 spec.DataDir,
		StateDir:                spec.StateDir,
		LogDir:                  spec.LogDir,
		ServiceRoot:             spec.ServiceRoot,
		StorageRoot:             spec.StorageRoot,
		ImportsRoot:             spec.ImportsRoot,
		UserBackupsRoot:         spec.UserBackupsRoot,
		ArchiveRoot:             spec.ArchiveRoot,
		GeneratedRoot:           spec.GeneratedRoot,
		BoxStateRoot:            spec.BoxStateRoot,
		BoxPath:                 spec.BoxPath,
		ObjectStorePath:         spec.ObjectStorePath,
		MainDocumentsPath:       spec.MainDocumentsPath,
		StorageExportRoot:       spec.StorageExportRoot,
		SocketPath:              spec.SocketPath,
		CloudConfigPath:         spec.CloudConfigPath,
		CloudStateDir:           spec.CloudStateDir,
		CloudRcloneConfigPath:   spec.CloudRcloneConfigPath,
		CloudBorgCacheDir:       spec.CloudBorgCacheDir,
		CloudBorgSecurityDir:    spec.CloudBorgSecurityDir,
		CloudBorgPassphraseFile: spec.CloudBorgPassphraseFile,
	}
	if spec.CloudConfigPath != "" {
		plan.CloudConfigDir = filepath.Dir(spec.CloudConfigPath)
	}
	if spec.BoxPath != "" {
		plan.BoxLoomDir = filepath.Join(spec.BoxPath, ".loom")
		plan.LegacyBoxStateRoot = filepath.Join(plan.BoxLoomDir, "state")
	}
	if spec.EnableLoomd {
		if plan.DataDir != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.DataDir)
		}
		plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, "/run/loom")
		if plan.BoxLoomDir != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.BoxLoomDir)
		}
		if plan.BoxStateRoot != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.BoxStateRoot)
		}
		if plan.StorageRoot != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.StorageRoot)
		}
		for _, path := range []string{plan.ImportsRoot, plan.UserBackupsRoot, plan.ArchiveRoot, plan.GeneratedRoot} {
			if path != "" {
				plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, path)
			}
		}
		if plan.MainDocumentsPath != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.MainDocumentsPath)
		}
		if spec.EnableCloud && plan.CloudStateDir != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.CloudStateDir)
		}
		if spec.EnableCloud && plan.CloudBorgCacheDir != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.CloudBorgCacheDir)
		}
		if spec.EnableCloud && plan.CloudBorgSecurityDir != "" {
			plan.ServiceReadWritePaths = append(plan.ServiceReadWritePaths, plan.CloudBorgSecurityDir)
		}
	}
	if spec.InstallMode == InstallModeService && spec.ServiceManager != ServiceManagerLaunchd && spec.EnableBox && plan.BoxLoomDir != "" {
		plan.ManagedTmpfilesPaths = []string{plan.BoxPath, plan.BoxLoomDir}
		for _, path := range []string{
			filepath.Join(plan.BoxLoomDir, "contracts"),
			filepath.Join(plan.BoxLoomDir, "policies"),
			plan.BoxStateRoot,
			plan.StorageRoot,
			plan.ImportsRoot,
			plan.UserBackupsRoot,
			plan.ArchiveRoot,
			plan.GeneratedRoot,
		} {
			if strings.TrimSpace(path) != "" {
				plan.ManagedTmpfilesPaths = append(plan.ManagedTmpfilesPaths, path)
			}
		}
	}
	plan.HumanLinks = planHumanLinks(spec, plan)
	if spec.EnableNodeAgent {
		home := spec.HomeDir
		if home == "" {
			home = os.Getenv("HOME")
		}
		if home == "" {
			return plan, fmt.Errorf("home_dir is required to plan node-agent paths")
		}
		plan.NodeAgentConfigPath = spec.NodeAgentConfigPath
		if plan.NodeAgentConfigPath == "" {
			plan.NodeAgentConfigPath = filepath.Join(home, ".config", "loom-node-agent", "config.json")
		}
		plan.NodeAgentStatePath = spec.NodeAgentStatePath
		if plan.NodeAgentStatePath == "" {
			plan.NodeAgentStatePath = filepath.Join(home, ".local", "state", "loom-node-agent", "state.json")
		}
		plan.NodeAgentDataDir = spec.NodeAgentDataDir
		if plan.NodeAgentDataDir == "" {
			plan.NodeAgentDataDir = filepath.Join(home, ".local", "state", "loom-node-agent")
		}
	}
	if spec.InstallMode == InstallModeService && spec.ServiceManager == ServiceManagerLaunchd && spec.EnableNodeAgent {
		home := spec.HomeDir
		if home == "" {
			home = os.Getenv("HOME")
		}
		if home == "" {
			return plan, fmt.Errorf("home_dir is required to plan LaunchAgent path")
		}
		plan.LaunchAgentPlistPath = LaunchAgentPlistPath(home)
	}
	return plan, nil
}

type ManifestPathInput struct {
	ExplicitPath string
	Spec         SetupSpec
	HomeDir      string
}

func ResolveManifestPath(input ManifestPathInput) (path string, source string, err error) {
	if strings.TrimSpace(input.ExplicitPath) != "" {
		path, err := normalizePath(input.ExplicitPath, firstNonEmpty(input.Spec.HomeDir, input.HomeDir))
		if err != nil {
			return "", "", err
		}
		return path, "explicit", nil
	}
	spec := input.Spec
	if spec.InstallMode == InstallModeService && spec.ServiceManager != ServiceManagerLaunchd {
		return "/etc/loom/install.yaml", "service_default", nil
	}
	if strings.TrimSpace(spec.ConfigDir) != "" {
		return filepath.Join(spec.ConfigDir, "install.yaml"), "config_dir", nil
	}
	home := firstNonEmpty(input.HomeDir, spec.HomeDir, os.Getenv("HOME"))
	if strings.TrimSpace(home) == "" {
		detected, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", "", fmt.Errorf("resolve manifest path: %w", homeErr)
		}
		home = detected
	}
	return filepath.Join(filepath.Clean(home), ".config", "loom", "install.yaml"), "home_default", nil
}

func normalizePath(value string, homeDir string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("path contains NUL")
	}
	if strings.HasPrefix(value, "~") {
		home := strings.TrimSpace(homeDir)
		if home == "" {
			detected, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve home directory: %w", err)
			}
			home = detected
		}
		switch {
		case value == "~":
			value = home
		case strings.HasPrefix(value, "~/"):
			value = filepath.Join(home, value[2:])
		default:
			return "", fmt.Errorf("only ~ and ~/ paths are supported")
		}
	}
	if !filepath.IsAbs(value) {
		abs, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		value = abs
	}
	return filepath.Clean(value), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
