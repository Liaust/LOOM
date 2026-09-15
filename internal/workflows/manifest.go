package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/scripts"
)

const (
	ManifestKind       = "loom.workflow"
	ManifestSchemaV031 = "workflow.contract.v0.3.1"

	ImplementationKindWorkflow = "workflow"
)

var workflowIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

type Manifest struct {
	Kind           string                  `json:"kind" yaml:"kind"`
	SchemaVersion  string                  `json:"schema_version" yaml:"schema_version"`
	Workflow       ManifestWorkflow        `json:"workflow" yaml:"workflow"`
	Implementation ManifestImplementation  `json:"implementation" yaml:"implementation"`
	Entrypoint     scripts.Entrypoint      `json:"entrypoint" yaml:"entrypoint"`
	Runtime        map[string]any          `json:"runtime,omitempty" yaml:"runtime"`
	Inputs         map[string]any          `json:"inputs,omitempty" yaml:"inputs"`
	Outputs        map[string]any          `json:"outputs,omitempty" yaml:"outputs"`
	Execution      scripts.Execution       `json:"execution" yaml:"execution"`
	Artifacts      []scripts.ArtifactSpec  `json:"artifacts,omitempty" yaml:"artifacts"`
	UsageDocuments []scripts.UsageDocument `json:"usage_documents,omitempty" yaml:"usage_documents"`
	Metadata       map[string]any          `json:"metadata,omitempty" yaml:"metadata"`
}

type ManifestWorkflow struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description"`
	Status      string `json:"status,omitempty" yaml:"status"`
	Version     string `json:"version" yaml:"version"`
}

type ManifestImplementation struct {
	Kind string `json:"kind" yaml:"kind"`
}

func LoadManifest(path string) (Manifest, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read workflow manifest: %w", err)
	}
	return ParseManifest(payload)
}

func ParseManifest(payload []byte) (Manifest, error) {
	var manifest Manifest
	if err := yaml.Unmarshal(payload, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse workflow yaml: %w", err)
	}
	manifest = NormalizeManifest(manifest)
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func NormalizeManifest(manifest Manifest) Manifest {
	manifest.Kind = strings.TrimSpace(manifest.Kind)
	manifest.SchemaVersion = strings.TrimSpace(manifest.SchemaVersion)
	manifest.Workflow.ID = strings.TrimSpace(manifest.Workflow.ID)
	manifest.Workflow.Name = strings.TrimSpace(manifest.Workflow.Name)
	manifest.Workflow.Description = strings.TrimSpace(manifest.Workflow.Description)
	manifest.Workflow.Status = strings.ToLower(strings.TrimSpace(manifest.Workflow.Status))
	if manifest.Workflow.Status == "" {
		manifest.Workflow.Status = "draft"
	}
	manifest.Workflow.Version = strings.TrimSpace(manifest.Workflow.Version)
	if manifest.Workflow.Version == "" {
		manifest.Workflow.Version = "0.1.0"
	}
	manifest.Implementation.Kind = strings.ToLower(strings.TrimSpace(manifest.Implementation.Kind))
	for i := range manifest.Entrypoint.Command {
		manifest.Entrypoint.Command[i] = strings.TrimSpace(manifest.Entrypoint.Command[i])
	}
	if manifest.Runtime == nil {
		manifest.Runtime = map[string]any{}
	}
	if manifest.Inputs == nil {
		manifest.Inputs = map[string]any{}
	}
	if manifest.Outputs == nil {
		manifest.Outputs = map[string]any{}
	}
	if manifest.Metadata == nil {
		manifest.Metadata = map[string]any{}
	}
	return manifest
}

func ValidateManifest(manifest Manifest) error {
	if manifest.Kind != ManifestKind {
		return fmt.Errorf("workflow manifest kind must be %q", ManifestKind)
	}
	if manifest.SchemaVersion != ManifestSchemaV031 {
		return fmt.Errorf("workflow manifest schema_version must be %q", ManifestSchemaV031)
	}
	if manifest.Implementation.Kind != ImplementationKindWorkflow {
		return fmt.Errorf("workflow manifest implementation.kind must be %q", ImplementationKindWorkflow)
	}
	if !workflowIDPattern.MatchString(manifest.Workflow.ID) {
		return fmt.Errorf("workflow manifest workflow.id must match %s", workflowIDPattern.String())
	}
	if manifest.Workflow.Name == "" {
		return fmt.Errorf("workflow manifest workflow.name is required")
	}
	if manifest.Workflow.Version == "" {
		return fmt.Errorf("workflow manifest workflow.version is required")
	}
	if len(manifest.Entrypoint.Command) == 0 {
		return fmt.Errorf("workflow manifest entrypoint.command is required")
	}
	for i, part := range manifest.Entrypoint.Command {
		if strings.TrimSpace(part) == "" {
			return fmt.Errorf("workflow manifest entrypoint.command[%d] is empty", i)
		}
	}
	if manifest.Execution.TimeoutSeconds <= 0 {
		return fmt.Errorf("workflow manifest execution.timeout_seconds must be positive")
	}
	for _, artifact := range manifest.Artifacts {
		if strings.TrimSpace(artifact.Key) == "" {
			return fmt.Errorf("workflow manifest artifact key is required")
		}
		if strings.TrimSpace(artifact.Type) == "" {
			return fmt.Errorf("workflow manifest artifact type is required")
		}
		if err := validatePackageRelativePath(artifact.Path); err != nil {
			return fmt.Errorf("workflow manifest artifact %q path is invalid: %w", artifact.Key, err)
		}
	}
	for _, usageDocument := range manifest.UsageDocuments {
		if err := validatePackageRelativePath(usageDocument.Path); err != nil {
			return fmt.Errorf("workflow manifest usage document path is invalid: %w", err)
		}
	}
	return nil
}

func NormalizeManifestJSON(manifest Manifest) ([]byte, error) {
	normalized, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("normalize workflow manifest: %w", err)
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
	return scripts.HashPackage(root)
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

func sha256URI(payload []byte) string {
	hash := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func PackageRoot(manifestPath string) (string, error) {
	root, err := filepath.Abs(filepath.Dir(manifestPath))
	if err != nil {
		return "", fmt.Errorf("resolve workflow package root: %w", err)
	}
	return root, nil
}
