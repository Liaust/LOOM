package projectcontracts

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	ProjectContractNotes       = "notes"
	ProjectContractRepos       = "repos"
	ProjectContractSync        = "sync"
	ProjectContractBackup      = "backup"
	ProjectContractWorkers     = "workers"
	ProjectContractCredentials = "credentials"
)

type singletonContractDefinition struct {
	Canonical string
	Legacy    string
	Facet     string
	Reference bool
}

var singletonContractDefinitions = map[string]singletonContractDefinition{
	ProjectContractNotes:       {Canonical: ".loom/contracts/notes.yaml", Legacy: "notes/loom.notes.yaml", Facet: "notes"},
	ProjectContractRepos:       {Canonical: ".loom/contracts/repos.yaml", Legacy: "repos/loom.repos.yaml", Facet: "repos"},
	ProjectContractSync:        {Canonical: ".loom/contracts/sync.yaml", Legacy: "policies/sync.yaml", Facet: "sync_policy", Reference: true},
	ProjectContractBackup:      {Canonical: ".loom/contracts/backup.yaml", Legacy: "policies/backup.yaml", Facet: "backup_policy", Reference: true},
	ProjectContractWorkers:     {Canonical: ".loom/contracts/workers.yaml", Legacy: "policies/workers.yaml", Facet: "worker_policy", Reference: true},
	ProjectContractCredentials: {Canonical: ".loom/contracts/credentials.yaml", Legacy: "policies/credentials.yaml", Facet: "secrets", Reference: true},
}

var contractCollectionPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

type ContractPathResolution struct {
	Kind             string        `json:"kind"`
	Path             string        `json:"path"`
	RelativePath     string        `json:"relative_path"`
	Layout           ProjectLayout `json:"layout"`
	CanonicalPath    string        `json:"canonical_path"`
	LegacyPath       string        `json:"legacy_path"`
	CanonicalPresent bool          `json:"canonical_present"`
	LegacyPresent    bool          `json:"legacy_present"`
	Present          bool          `json:"present"`
	Explicit         bool          `json:"explicit"`
}

type ContractPathError struct {
	Code string
	Kind string
	Err  error
}

func (e ContractPathError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Err.Error()
}

func (e ContractPathError) Unwrap() error { return e.Err }

func CanonicalSingletonContractPath(kind string) (string, error) {
	definition, ok := singletonContractDefinitions[strings.TrimSpace(kind)]
	if !ok {
		return "", fmt.Errorf("unsupported singleton project contract kind: %s", kind)
	}
	return definition.Canonical, nil
}

func LegacySingletonContractPath(kind string) (string, error) {
	definition, ok := singletonContractDefinitions[strings.TrimSpace(kind)]
	if !ok {
		return "", fmt.Errorf("unsupported singleton project contract kind: %s", kind)
	}
	return definition.Legacy, nil
}

func KeyedProjectContractPath(pluralKind, key string) (string, error) {
	pluralKind = strings.TrimSpace(pluralKind)
	key = strings.TrimSpace(key)
	if !contractCollectionPattern.MatchString(pluralKind) {
		return "", fmt.Errorf("invalid project contract collection: %s", pluralKind)
	}
	if !contractCollectionPattern.MatchString(key) {
		return "", fmt.Errorf("invalid project contract key: %s", key)
	}
	return filepath.ToSlash(filepath.Join(ProjectMetadataDir, "contracts", pluralKind, key+".yaml")), nil
}

func ResolveSingletonContract(loaded LoadedProject, kind, explicitPath string) (ContractPathResolution, error) {
	if loaded.Contract.SchemaVersion == ProjectSchemaV05 {
		return resolveDeclarationSingleton(loaded, kind, explicitPath)
	}
	kind = strings.TrimSpace(kind)
	definition, ok := singletonContractDefinitions[kind]
	if !ok {
		return ContractPathResolution{}, ContractPathError{Code: "contract.kind_unsupported", Kind: kind, Err: fmt.Errorf("unsupported singleton project contract kind: %s", kind)}
	}
	canonicalPath := filepath.Join(loaded.RootPath, filepath.FromSlash(definition.Canonical))
	legacyPath := filepath.Join(loaded.RootPath, filepath.FromSlash(definition.Legacy))
	canonicalRaw, canonicalPresent, err := readOptionalContract(canonicalPath)
	if err != nil {
		return ContractPathResolution{}, ContractPathError{Code: "contract.read_failed", Kind: kind, Err: err}
	}
	legacyRaw, legacyPresent, err := readOptionalContract(legacyPath)
	if err != nil {
		return ContractPathResolution{}, ContractPathError{Code: "contract.read_failed", Kind: kind, Err: err}
	}
	resolution := ContractPathResolution{
		Kind:             kind,
		CanonicalPath:    canonicalPath,
		LegacyPath:       legacyPath,
		CanonicalPresent: canonicalPresent,
		LegacyPresent:    legacyPresent,
	}

	explicitPath = strings.TrimSpace(explicitPath)
	if explicitPath != "" {
		normalized, err := normalizeRelativePath(explicitPath)
		if err != nil {
			return resolution, ContractPathError{Code: "contract.path_unsafe", Kind: kind, Err: fmt.Errorf("%s contract path is unsafe: %s", kind, explicitPath)}
		}
		resolution.Explicit = true
		resolution.RelativePath = normalized
		resolution.Path = filepath.Join(loaded.RootPath, filepath.FromSlash(normalized))
		resolution.Present = pathExists(resolution.Path)
		switch normalized {
		case definition.Canonical:
			resolution.Layout = ProjectLayoutCanonical
		case definition.Legacy:
			resolution.Layout = ProjectLayoutLegacy
		default:
			resolution.Layout = loaded.Layout
		}
	}

	compareDefaults := explicitPath == "" || resolution.RelativePath == definition.Canonical || resolution.RelativePath == definition.Legacy
	if compareDefaults && canonicalPresent && legacyPresent {
		equal, err := yamlDocumentsSemanticallyEqual(canonicalRaw, legacyRaw)
		if err != nil {
			return resolution, ContractPathError{Code: "contract.parse_failed", Kind: kind, Err: fmt.Errorf("compare %s contract layouts: %w", kind, err)}
		}
		if !equal {
			return resolution, ContractPathError{
				Code: "contract.singleton_layout_conflict",
				Kind: kind,
				Err:  fmt.Errorf("%s contract layout conflict: %s and %s contain different definitions", kind, canonicalPath, legacyPath),
			}
		}
		if !resolution.Explicit {
			resolution.Path = canonicalPath
			resolution.RelativePath = definition.Canonical
			resolution.Present = true
			resolution.Layout = ProjectLayoutCanonicalWithLegacy
		}
	}
	if resolution.Explicit {
		return resolution, nil
	}

	preferCanonical := kind == ProjectContractNotes || kind == ProjectContractRepos || loaded.Layout != ProjectLayoutLegacy
	preferredRelative, preferredPath, preferredPresent := definition.Canonical, canonicalPath, canonicalPresent
	alternateRelative, alternatePath, alternatePresent := definition.Legacy, legacyPath, legacyPresent
	preferredLayout, alternateLayout := ProjectLayoutCanonical, ProjectLayoutLegacy
	if !preferCanonical {
		preferredRelative, alternateRelative = alternateRelative, preferredRelative
		preferredPath, alternatePath = alternatePath, preferredPath
		preferredPresent, alternatePresent = alternatePresent, preferredPresent
		preferredLayout, alternateLayout = alternateLayout, preferredLayout
	}
	resolution.RelativePath = preferredRelative
	resolution.Path = preferredPath
	resolution.Present = preferredPresent
	resolution.Layout = preferredLayout
	if !preferredPresent && alternatePresent {
		resolution.RelativePath = alternateRelative
		resolution.Path = alternatePath
		resolution.Present = true
		resolution.Layout = alternateLayout
	}
	return resolution, nil
}

func singletonContractFacet(kind string) string {
	return singletonContractDefinitions[kind].Facet
}

func singletonContractUsesReference(kind string) bool {
	return singletonContractDefinitions[kind].Reference
}

func yamlDocumentsSemanticallyEqual(left, right []byte) (bool, error) {
	leftValue, err := decodeYAMLSemanticValue(left)
	if err != nil {
		return false, err
	}
	rightValue, err := decodeYAMLSemanticValue(right)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftValue, rightValue), nil
}

func decodeYAMLSemanticValue(raw []byte) (any, error) {
	var value any
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple YAML documents are not supported")
		}
		return nil, err
	}
	return value, nil
}

func contractPathDiagnostic(err error, file, field string) Diagnostic {
	code := "contract.path_invalid"
	if pathErr, ok := err.(ContractPathError); ok {
		code = pathErr.Code
	}
	return Diagnostic{Severity: SeverityError, Code: code, Message: err.Error(), File: file, Field: field}
}

func resolveSingletonForValidation(loaded LoadedProject, kind, explicitPath, field string, add func(Diagnostic)) (ContractPathResolution, bool) {
	resolution, err := ResolveSingletonContract(loaded, kind, explicitPath)
	if err != nil {
		add(contractPathDiagnostic(err, loaded.ContractPath, field))
		return resolution, false
	}
	if resolution.CanonicalPresent && resolution.LegacyPresent {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "contract.singleton_layout_duplicate",
			Message:    kind + " contract has equivalent canonical and legacy copies; the legacy copy is redundant",
			File:       resolution.LegacyPath,
			Field:      field,
			Suggestion: "keep " + singletonContractDefinitions[kind].Canonical + " as the portable project contract",
		})
	}
	if resolution.Layout == ProjectLayoutLegacy && loaded.Layout != ProjectLayoutLegacy {
		add(Diagnostic{
			Severity:   SeverityWarning,
			Code:       "contract.path_legacy",
			Message:    kind + " contract uses the legacy project-relative path " + resolution.RelativePath,
			File:       resolution.Path,
			Field:      field,
			Suggestion: "migrate the contract to " + singletonContractDefinitions[kind].Canonical,
		})
	}
	return resolution, true
}
