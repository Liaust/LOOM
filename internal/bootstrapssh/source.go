package bootstrapssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func DefaultExcludes() []string {
	return []string{
		".git/",
		".direnv/",
		".DS_Store",
		"result",
		"result-*",
		"node_modules/",
		"tmp/",
		".cache/",
	}
}

func BuildSourcePlan(spec Spec, facts RemoteFacts) (SourcePlan, error) {
	mode, err := normalizeSourceMode(spec.SourceMode)
	if err != nil {
		return SourcePlan{}, err
	}
	remotePath := strings.TrimSpace(spec.RemoteSourceDir)
	if remotePath == "" {
		remotePath = DefaultRemoteSourceDir(spec, facts)
	}
	plan := SourcePlan{
		Mode:       mode,
		RemotePath: remotePath,
		Excludes:   DefaultExcludes(),
	}
	switch mode {
	case SourceModeCurrentRsync:
		localPath := strings.TrimSpace(spec.SourcePath)
		if localPath == "" {
			var wdErr error
			localPath, wdErr = os.Getwd()
			if wdErr != nil {
				return SourcePlan{}, wdErr
			}
		}
		abs, err := filepath.Abs(localPath)
		if err != nil {
			return SourcePlan{}, err
		}
		plan.LocalPath = filepath.Clean(abs)
	case SourceModeGitClone:
		plan.GitURL = strings.TrimSpace(spec.GitURL)
		plan.GitRef = strings.TrimSpace(spec.GitRef)
	}
	return plan, nil
}

func DefaultRemoteSourceDir(spec Spec, facts RemoteFacts) string {
	installMode := strings.ToLower(strings.TrimSpace(spec.InstallMode))
	nodeKind := strings.ToLower(strings.TrimSpace(spec.NodeKind))
	home := strings.TrimSpace(facts.HomeDir)
	switch {
	case installMode == "service" || nodeKind == "main":
		return DefaultRemoteSourceService
	case installMode == "developer":
		if home != "" {
			return filepath.Join(home, DefaultRemoteSourceDeveloper)
		}
		return "~/loom/current"
	default:
		if home != "" {
			return filepath.Join(home, DefaultRemoteSourceUser)
		}
		return "~/.local/share/loom/source/current"
	}
}

func normalizeSourceMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "current", SourceModeCurrentRsync:
		return SourceModeCurrentRsync, nil
	case "git", SourceModeGitClone:
		return SourceModeGitClone, nil
	default:
		return "", fmt.Errorf("unsupported source mode %q", value)
	}
}
