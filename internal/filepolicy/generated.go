package filepolicy

import (
	"path/filepath"
	"strings"
)

// The generated-path taxonomy is retained for indexing classification and
// compatibility callers. It must not be used as a backup or transfer policy.
var indexingGeneratedDirectoryNames = []string{
	".git",
	"node_modules",
	".venv",
	"venv",
	"dist",
	"build",
	"target",
	".cache",
	".data",
	".secrets",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".ruff_cache",
}

var indexingGeneratedFileNames = []string{
	".DS_Store",
	".localized",
	".env",
}

var indexingGeneratedNamePrefixes = []string{
	"._",
	"~$",
}

var indexingGeneratedNameSuffixes = []string{
	".loom-meta.json",
	".tmp",
	".part",
	".partial",
	".crdownload",
}

func GeneratedDirectoryNames() []string {
	return append([]string{}, indexingGeneratedDirectoryNames...)
}

func DefaultExcludePatterns() []string {
	patterns := []string{}
	for _, name := range indexingGeneratedDirectoryNames {
		patterns = append(patterns,
			name,
			name+"/**",
			"**/"+name,
			"**/"+name+"/**",
		)
	}
	for _, name := range indexingGeneratedFileNames {
		patterns = append(patterns, name, "**/"+name)
	}
	for _, prefix := range indexingGeneratedNamePrefixes {
		patterns = append(patterns, prefix+"*", "**/"+prefix+"*")
	}
	for _, suffix := range indexingGeneratedNameSuffixes {
		patterns = append(patterns, "*"+suffix, "**/*"+suffix)
	}
	return patterns
}

func RsyncExcludePatterns() []string {
	patterns := []string{}
	for _, name := range indexingGeneratedDirectoryNames {
		patterns = append(patterns, name+"/")
	}
	for _, name := range indexingGeneratedFileNames {
		patterns = append(patterns, name)
	}
	for _, prefix := range indexingGeneratedNamePrefixes {
		patterns = append(patterns, prefix+"*")
	}
	for _, suffix := range indexingGeneratedNameSuffixes {
		patterns = append(patterns, "*"+suffix)
	}
	return patterns
}

func IsGeneratedName(name string) bool {
	name = strings.TrimSpace(filepath.Base(filepath.Clean(name)))
	if name == "" || name == "." || name == ".." {
		return true
	}
	for _, candidate := range indexingGeneratedDirectoryNames {
		if name == candidate {
			return true
		}
	}
	for _, candidate := range indexingGeneratedFileNames {
		if name == candidate {
			return true
		}
	}
	for _, prefix := range indexingGeneratedNamePrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for _, suffix := range indexingGeneratedNameSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func IsGeneratedPath(pathValue string) bool {
	return IsIndexingExcludedPath(pathValue)
}

// IsIndexingExcludedPath classifies paths which rich indexing may omit. It is
// intentionally independent from backup and transfer eligibility.
func IsIndexingExcludedPath(pathValue string) bool {
	pathValue = filepath.ToSlash(strings.TrimSpace(pathValue))
	if pathValue == "" {
		return false
	}
	for _, part := range strings.Split(pathValue, "/") {
		if part == "" {
			continue
		}
		if IsGeneratedName(part) {
			return true
		}
	}
	return false
}
