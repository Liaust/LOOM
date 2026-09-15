package agentpack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ResolveOptions struct {
	ExplicitPath   string
	Environment    string
	ExecutablePath string
	WorkingDir     string
}

func ResolveRoot(options ResolveOptions) (ResolvedRoot, error) {
	if value := strings.TrimSpace(options.ExplicitPath); value != "" {
		return resolveRootCandidate(value, RootExplicit)
	}
	environment := options.Environment
	if environment == "" {
		environment = os.Getenv("LOOM_AGENT_PACK_DIR")
	}
	if value := strings.TrimSpace(environment); value != "" {
		return resolveRootCandidate(value, RootEnvironment)
	}
	executable := strings.TrimSpace(options.ExecutablePath)
	if executable == "" {
		if value, err := os.Executable(); err == nil {
			executable = value
		}
	}
	if executable != "" {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "share", "loom", "ai-loom-pack"))
		if directoryExists(candidate) {
			return resolveRootCandidate(candidate, RootPackaged)
		}
	}
	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		value, err := os.Getwd()
		if err != nil {
			return ResolvedRoot{}, fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = value
	}
	if candidate := findRepositoryPack(workingDir); candidate != "" {
		return resolveRootCandidate(candidate, RootRepository)
	}
	return ResolvedRoot{}, fmt.Errorf("AI LOOM pack not found; use --pack-dir or LOOM_AGENT_PACK_DIR")
}

func resolveRootCandidate(path string, source RootSource) (ResolvedRoot, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return ResolvedRoot{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return ResolvedRoot{}, fmt.Errorf("AI LOOM pack %q: %w", absolute, err)
	}
	if !info.IsDir() {
		return ResolvedRoot{}, fmt.Errorf("AI LOOM pack %q is not a directory", absolute)
	}
	return ResolvedRoot{Path: absolute, Source: source}, nil
}

func findRepositoryPack(start string) string {
	current, err := filepath.Abs(filepath.Clean(start))
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(current, "ai-loom-pack")
		if directoryExists(candidate) && (fileExists(filepath.Join(current, "go.mod")) || fileExists(filepath.Join(current, ".git"))) {
			return candidate
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
