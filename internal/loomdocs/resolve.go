package loomdocs

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
	LoomVersion    string
}

func ResolveRoot(options ResolveOptions) (ResolvedRoot, error) {
	if value := strings.TrimSpace(options.ExplicitPath); value != "" {
		return resolveCandidate(value, SourceExplicit, options.LoomVersion)
	}
	environment := options.Environment
	if environment == "" {
		environment = os.Getenv("LOOM_DOCS_DIR")
	}
	if value := strings.TrimSpace(environment); value != "" {
		return resolveCandidate(value, SourceEnvironment, options.LoomVersion)
	}

	executable := strings.TrimSpace(options.ExecutablePath)
	if executable == "" {
		if current, err := os.Executable(); err == nil {
			executable = current
		}
	}
	if executable != "" {
		candidate := filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "share", "loom", "docs"))
		if isDirectory(candidate) {
			return resolveCandidate(candidate, SourcePackaged, options.LoomVersion)
		}
	}

	workingDir := strings.TrimSpace(options.WorkingDir)
	if workingDir == "" {
		current, err := os.Getwd()
		if err != nil {
			return ResolvedRoot{}, fmt.Errorf("resolve working directory: %w", err)
		}
		workingDir = current
	}
	if candidate := findRepositoryDocs(workingDir); candidate != "" {
		return resolveCandidate(candidate, SourceRepository, options.LoomVersion)
	}
	return ResolvedRoot{}, fmt.Errorf("LOOM documentation root not found; use --docs-dir or LOOM_DOCS_DIR")
}

func resolveCandidate(path string, source SourceKind, loomVersion string) (ResolvedRoot, error) {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return ResolvedRoot{}, fmt.Errorf("resolve documentation root %q: %w", path, err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return ResolvedRoot{}, fmt.Errorf("documentation root %q: %w", absolute, err)
	}
	if !info.IsDir() {
		return ResolvedRoot{}, fmt.Errorf("documentation root %q is not a directory", absolute)
	}
	return ResolvedRoot{
		Path:           absolute,
		Source:         source,
		LoomVersion:    strings.TrimSpace(loomVersion),
		ReleaseMatched: source == SourcePackaged || source == SourceRepository,
	}, nil
}

func findRepositoryDocs(start string) string {
	current, err := filepath.Abs(filepath.Clean(start))
	if err != nil {
		return ""
	}
	for {
		docs := filepath.Join(current, "docs")
		if isDirectory(docs) && (pathExists(filepath.Join(current, "go.mod")) || pathExists(filepath.Join(current, ".git"))) {
			return docs
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
