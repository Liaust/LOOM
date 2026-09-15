package scripts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const ManifestKind = "loom.script"

var scriptIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

type Manifest struct {
	Kind           string          `json:"kind" yaml:"kind"`
	ID             string          `json:"id" yaml:"id"`
	Name           string          `json:"name" yaml:"name"`
	Version        string          `json:"version" yaml:"version"`
	Description    string          `json:"description,omitempty" yaml:"description"`
	Entrypoint     Entrypoint      `json:"entrypoint" yaml:"entrypoint"`
	Runtime        map[string]any  `json:"runtime,omitempty" yaml:"runtime"`
	Inputs         map[string]any  `json:"inputs,omitempty" yaml:"inputs"`
	Outputs        map[string]any  `json:"outputs,omitempty" yaml:"outputs"`
	Execution      Execution       `json:"execution" yaml:"execution"`
	Artifacts      []ArtifactSpec  `json:"artifacts,omitempty" yaml:"artifacts"`
	UsageDocuments []UsageDocument `json:"usage_documents,omitempty" yaml:"usage_documents"`
	Metadata       map[string]any  `json:"metadata,omitempty" yaml:"metadata"`
}

type Entrypoint struct {
	Command []string `json:"command" yaml:"command"`
}

type Execution struct {
	TimeoutSeconds int            `json:"timeout_seconds" yaml:"timeout_seconds"`
	Network        bool           `json:"network" yaml:"network"`
	Filesystem     map[string]any `json:"filesystem,omitempty" yaml:"filesystem"`
}

type ArtifactSpec struct {
	Key   string `json:"key" yaml:"key"`
	Path  string `json:"path" yaml:"path"`
	Type  string `json:"type" yaml:"type"`
	Title string `json:"title,omitempty" yaml:"title"`
}

type UsageDocument struct {
	Path   string `json:"path" yaml:"path"`
	Target string `json:"target,omitempty" yaml:"target"`
}

func LoadManifest(path string) (Manifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read script manifest: %w", err)
	}
	return ParseManifest(payload)
}

func ParseManifest(payload []byte) (Manifest, error) {
	var manifest Manifest
	if err := yaml.Unmarshal(payload, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse script manifest yaml: %w", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ValidateManifest(manifest Manifest) error {
	if strings.TrimSpace(manifest.Kind) != ManifestKind {
		return fmt.Errorf("script manifest kind must be %q", ManifestKind)
	}
	if !scriptIDPattern.MatchString(strings.TrimSpace(manifest.ID)) {
		return fmt.Errorf("script manifest id must match %s", scriptIDPattern.String())
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("script manifest name is required")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return fmt.Errorf("script manifest version is required")
	}
	if len(manifest.Entrypoint.Command) == 0 {
		return fmt.Errorf("script manifest entrypoint.command is required")
	}
	for i, part := range manifest.Entrypoint.Command {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("script manifest entrypoint.command[%d] is empty", i)
		}
	}
	if manifest.Execution.TimeoutSeconds <= 0 {
		return fmt.Errorf("script manifest execution.timeout_seconds must be positive")
	}
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Key) == "" {
			return fmt.Errorf("script manifest artifact key is required")
		}
		if strings.TrimSpace(artifact.Type) == "" {
			return fmt.Errorf("script manifest artifact type is required")
		}
		if err := validatePackageRelativePath(artifact.Path); err != nil {
			return fmt.Errorf("script manifest artifact %q path is invalid: %w", artifact.Key, err)
		}
	}
	for _, usageDocument := range manifest.UsageDocuments {
		if err := validatePackageRelativePath(usageDocument.Path); err != nil {
			return fmt.Errorf("script manifest usage document path is invalid: %w", err)
		}
	}
	return nil
}

func NormalizeManifestJSON(manifest Manifest) ([]byte, error) {
	normalized, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("normalize script manifest: %w", err)
	}
	return normalized, nil
}

func HashManifest(manifest Manifest) (string, error) {
	normalized, err := NormalizeManifestJSON(manifest)
	if err != nil {
		return "", err
	}
	return sha256URI(normalized), nil
}

func HashPackage(root string) (string, error) {
	root = filepath.Clean(root)
	entries := []packageFile{}
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == root {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() && ignoredPackageDir(name) {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		if ignoredPackageFile(name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		payload, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		entries = append(entries, packageFile{
			RelativePath: filepath.ToSlash(rel),
			Payload:      payload,
		})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash script package: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelativePath < entries[j].RelativePath
	})

	hash := sha256.New()
	for _, entry := range entries {
		hash.Write([]byte(entry.RelativePath))
		hash.Write([]byte{0})
		hash.Write(entry.Payload)
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

type packageFile struct {
	RelativePath string
	Payload      []byte
}

func validatePackageRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path is required")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("path contains NUL")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if path.IsAbs(value) {
		return fmt.Errorf("path must be relative")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("path must not contain ..")
		}
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path must resolve inside package")
	}
	return nil
}

func ignoredPackageDir(name string) bool {
	switch name {
	case ".git", ".direnv", ".cache", "node_modules", "tmp", "result":
		return true
	default:
		return false
	}
}

func ignoredPackageFile(name string) bool {
	switch name {
	case ".DS_Store":
		return true
	default:
		return false
	}
}

func sha256URI(payload []byte) string {
	hash := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(hash[:])
}
