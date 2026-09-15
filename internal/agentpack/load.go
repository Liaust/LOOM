package agentpack

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

func Load(root ResolvedRoot) (*Pack, error) {
	manifestPath, err := secureJoin(root.Path, "manifest.yaml")
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := decodeYAMLFile(manifestPath, &manifest); err != nil {
		return nil, fmt.Errorf("load pack manifest: %w", err)
	}
	if strings.TrimSpace(manifest.Catalogue) == "" {
		return nil, fmt.Errorf("pack manifest catalogue path is required")
	}
	cataloguePath, err := secureJoin(root.Path, manifest.Catalogue)
	if err != nil {
		return nil, fmt.Errorf("resolve catalogue: %w", err)
	}
	var catalogue Catalogue
	if err := decodeYAMLFile(cataloguePath, &catalogue); err != nil {
		return nil, fmt.Errorf("load pack catalogue: %w", err)
	}
	return &Pack{Root: root, Manifest: manifest, Catalogue: catalogue}, nil
}

func LoadFromPath(path string) (*Pack, error) {
	root, err := resolveRootCandidate(path, RootExplicit)
	if err != nil {
		return nil, err
	}
	return Load(root)
}

func decodeYAMLFile(path string, target any) error {
	payload, err := readWorkspaceFile(path)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(payload))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		// YAML type errors can include the input scalar, including a mistakenly
		// supplied credential. Report the source path without its contents.
		return fmt.Errorf("invalid YAML or unsupported fields in %s", path)
	}
	return nil
}

func secureJoin(root, relative string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("path must be non-empty and relative: %q", relative)
	}
	rootAbs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	joined := filepath.Join(rootAbs, filepath.Clean(filepath.FromSlash(relative)))
	rel, err := filepath.Rel(rootAbs, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", relative)
	}
	return joined, nil
}
