package box

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"loom.local/loom/internal/filepolicy"
)

const (
	metadataDir     = ".loom"
	contractRelPath = ".loom/box.yaml"
	policiesRelDir  = ".loom/policies"
	contractsRelDir = ".loom/contracts"
	stateRelDir     = ".loom/state"
	areaTopics      = "topics"
	areaLibrary     = "library"
)

func ContractPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(contractRelPath))
}

func LoadContract(path string) (Contract, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, err
	}
	return ParseContract(payload)
}

func ParseContract(payload []byte) (Contract, error) {
	var fields map[string]any
	if err := yaml.Unmarshal(payload, &fields); err != nil {
		return Contract{}, fmt.Errorf("parse box contract yaml: %w", err)
	}
	var contract Contract
	version, _ := fields["schema_version"].(string)
	if strings.TrimSpace(version) == FolderPolicySchemaVersion {
		if areas, ok := fields["areas"].(map[string]any); ok {
			for key, raw := range areas {
				if area, ok := raw.(map[string]any); ok {
					if watch, exists := area["watch"]; exists && watch == nil {
						return Contract{}, fmt.Errorf("box contract areas.%s.watch must be an object, not null", key)
					}
				}
			}
		}
		// Versioned readers precede new default writers. JSON decoding preserves
		// YAML value types instead of coercing strings/bools in new descriptors.
		decoder := yaml.NewDecoder(bytes.NewReader(payload))
		decoder.KnownFields(true)
		var document any
		if err := decoder.Decode(&contract); err != nil {
			return Contract{}, fmt.Errorf("parse box contract yaml: %w", err)
		}
		if err := decoder.Decode(&document); err != io.EOF {
			return Contract{}, fmt.Errorf("box contract must contain exactly one YAML document")
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			return Contract{}, fmt.Errorf("parse box contract fields: %w", err)
		}
		strict := json.NewDecoder(bytes.NewReader(raw))
		strict.DisallowUnknownFields()
		if err := strict.Decode(&contract); err != nil {
			return Contract{}, fmt.Errorf("parse box contract: %w", err)
		}
		if files, exists := fields["files"]; exists {
			raw, _ := json.Marshal(files)
			policy, err := filepolicy.DecodeFolderPolicy(raw)
			if err != nil {
				return Contract{}, fmt.Errorf("box contract %s: %w", contractRelPath, err)
			}
			contract.Files = &policy
		}
	} else {
		if _, exists := fields["files"]; exists {
			return Contract{}, fmt.Errorf("box contract files requires schema_version %q", FolderPolicySchemaVersion)
		}
		if areas, ok := fields["areas"].(map[string]any); ok {
			for key, raw := range areas {
				if area, ok := raw.(map[string]any); ok {
					if _, exists := area["watch"]; exists {
						return Contract{}, fmt.Errorf("box contract areas.%s.watch requires schema_version %q", key, FolderPolicySchemaVersion)
					}
				}
			}
		}
		if err := yaml.Unmarshal(payload, &contract); err != nil {
			return Contract{}, fmt.Errorf("parse box contract yaml: %w", err)
		}
	}
	contract = NormalizeContract(contract)
	if err := ValidateContract(contract); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func NormalizeContract(contract Contract) Contract {
	contract.SchemaVersion = strings.TrimSpace(contract.SchemaVersion)
	contract.BoxID = strings.TrimSpace(contract.BoxID)
	contract.OwnerNode = strings.TrimSpace(contract.OwnerNode)
	contract.Profile = strings.ToLower(strings.TrimSpace(contract.Profile))
	contract.RootPath = strings.TrimSpace(contract.RootPath)
	contract.DefaultProjectPath = strings.TrimSpace(contract.DefaultProjectPath)
	if contract.Areas == nil {
		contract.Areas = map[string]Area{}
	}
	normalizedAreas := map[string]Area{}
	for key, area := range contract.Areas {
		key = strings.ToLower(strings.TrimSpace(key))
		area.Path = filepath.ToSlash(strings.TrimSpace(area.Path))
		area.TransferStatus = strings.ToLower(strings.TrimSpace(area.TransferStatus))
		normalizedAreas[key] = area
	}
	contract.Areas = normalizedAreas
	if contract.Policies == nil {
		contract.Policies = map[string]string{}
	}
	normalizedPolicies := map[string]string{}
	for key, value := range contract.Policies {
		key = strings.ToLower(strings.TrimSpace(key))
		normalizedPolicies[key] = filepath.ToSlash(strings.TrimSpace(value))
	}
	contract.Policies = normalizedPolicies
	return contract
}

func ValidateContract(contract Contract) error {
	if contract.SchemaVersion != SchemaVersion && contract.SchemaVersion != FolderPolicySchemaVersion {
		return fmt.Errorf("box contract schema_version must be %q or %q", SchemaVersion, FolderPolicySchemaVersion)
	}
	if contract.Files != nil {
		if contract.SchemaVersion != FolderPolicySchemaVersion {
			return fmt.Errorf("box contract files requires schema_version %q", FolderPolicySchemaVersion)
		}
		if _, err := filepolicy.NormalizeFolderPolicy(*contract.Files); err != nil {
			return fmt.Errorf("box contract %s: %w", contractRelPath, err)
		}
	}
	if strings.TrimSpace(contract.BoxID) == "" {
		return fmt.Errorf("box contract box_id is required")
	}
	if !strings.HasPrefix(contract.BoxID, "box_") {
		return fmt.Errorf("box contract box_id must start with box_")
	}
	if strings.TrimSpace(contract.OwnerNode) == "" {
		return fmt.Errorf("box contract owner_node is required")
	}
	if !IsValidProfile(contract.Profile) {
		return fmt.Errorf("box contract profile must be one of: %s", strings.Join(SupportedProfiles(), ", "))
	}
	if strings.TrimSpace(contract.RootPath) == "" {
		return fmt.Errorf("box contract root_path is required")
	}
	if strings.ContainsRune(contract.RootPath, '\x00') {
		return fmt.Errorf("box contract root_path contains NUL")
	}
	if !filepath.IsAbs(contract.RootPath) {
		return fmt.Errorf("box contract root_path must be absolute")
	}
	if err := validateRelativePath(contract.DefaultProjectPath); err != nil {
		return fmt.Errorf("box contract default_project_path is invalid: %w", err)
	}
	for _, key := range []string{AreaProjects, AreaNotes} {
		area, ok := contract.Areas[key]
		if !ok {
			return fmt.Errorf("box contract missing area %q", key)
		}
		if err := validateRelativePath(area.Path); err != nil {
			return fmt.Errorf("box contract area %q path is invalid: %w", key, err)
		}
	}
	if _, hasDocuments := contract.Areas[AreaDocuments]; !hasDocuments {
		if _, hasLaunchpad := contract.Areas[AreaLaunchpad]; !hasLaunchpad {
			return fmt.Errorf("box contract missing area %q", AreaDocuments)
		}
	}
	for key, area := range contract.Areas {
		if err := validateRelativePath(area.Path); err != nil {
			return fmt.Errorf("box contract area %q path is invalid: %w", key, err)
		}
		if area.Watch != nil {
			if contract.SchemaVersion != FolderPolicySchemaVersion {
				return fmt.Errorf("box contract areas.%s.watch requires schema_version %q", key, FolderPolicySchemaVersion)
			}
			if !isBoxWatchArea(key) {
				return fmt.Errorf("box contract areas.%s.watch: area does not own a watched root", key)
			}
			if contract.Policies[key] != "" {
				return fmt.Errorf("box contract areas.%s.watch and policies.%s are mutually exclusive", key, key)
			}
			policy := inlineWatchPolicy(*area.Watch, key)
			for _, diagnostic := range validateWatchPolicy(policy, key, contractRelPath) {
				if diagnostic.Severity == "error" {
					return fmt.Errorf("box contract areas.%s.watch: %s", key, diagnostic.Message)
				}
			}
			if policy.Path != "" && policy.Path != area.Path {
				return fmt.Errorf("box contract areas.%s.watch.path must match its area path (or be omitted)", key)
			}
		}
	}
	for key, value := range contract.Policies {
		if key == "" {
			return fmt.Errorf("box contract policy key is required")
		}
		if err := validateRelativePath(value); err != nil {
			return fmt.Errorf("box contract policy %q path is invalid: %w", key, err)
		}
	}
	for _, key := range []string{AreaNotes} {
		if !hasAreaWatchSource(contract, key) {
			return fmt.Errorf("box contract missing policy %q", key)
		}
	}
	if _, hasLane := contract.Areas[AreaLane]; hasLane && strings.TrimSpace(contract.Policies[AreaLane]) == "" {
		return fmt.Errorf("box contract missing policy %q", AreaLane)
	}
	if !hasAreaWatchSource(contract, AreaDocuments) && !hasAreaWatchSource(contract, AreaLaunchpad) {
		return fmt.Errorf("box contract missing policy %q", AreaDocuments)
	}
	return nil
}

func DefaultContract(resolved Resolved) Contract {
	profile := resolved.Profile
	if profile == "" {
		profile = ProfileWorkspace
	}
	areas := map[string]Area{
		AreaProjects:  {Path: "Projects", Enabled: true},
		AreaNotes:     {Path: "Notes", Enabled: true, Watch: defaultAreaWatch(AreaNotes)},
		AreaDocuments: {Path: "Documents", Enabled: true, Watch: defaultAreaWatch(AreaDocuments)},
	}
	policies := map[string]string{
		PolicyBackupContracts: DefaultBackupContractsRelDir,
	}
	if profile == ProfileMain {
		areas[areaTopics] = Area{Path: "Topics", Enabled: true}
		areas[areaLibrary] = Area{Path: "Library", Enabled: true}
	} else {
		areas[AreaLane] = Area{Path: DefaultLaneDirName, Enabled: true}
		policies[AreaLane] = ".loom/policies/lane.transfer.yaml"
	}
	return Contract{
		SchemaVersion:      FolderPolicySchemaVersion,
		BoxID:              "box_pending",
		OwnerNode:          resolved.OwnerNode,
		Profile:            profile,
		RootPath:           resolved.RootPath,
		Areas:              areas,
		DefaultProjectPath: "Projects",
		Policies:           policies,
	}
}

func ExpectedDirectories(contract Contract) []PathStatus {
	statuses := []PathStatus{
		expectedDirectory("metadata", contract.RootPath, metadataDir, true),
	}
	for key := range contract.Policies {
		if key != PolicyBackupContracts {
			statuses = append(statuses, expectedDirectory("policies", contract.RootPath, policiesRelDir, true))
			break
		}
	}
	if strings.TrimSpace(contract.Policies[PolicyBackupContracts]) != "" {
		statuses = append(statuses,
			expectedDirectory("contracts", contract.RootPath, contractsRelDir, true),
			expectedDirectory(PolicyBackupContracts, contract.RootPath, contract.Policies[PolicyBackupContracts], true),
		)
	}
	areaKeys := make([]string, 0, len(contract.Areas))
	for key := range contract.Areas {
		areaKeys = append(areaKeys, key)
	}
	sort.Strings(areaKeys)
	for _, key := range areaKeys {
		area := contract.Areas[key]
		statuses = append(statuses, expectedDirectory(key, contract.RootPath, area.Path, area.Enabled))
	}
	return statuses
}

func ExpectedPolicyFiles(contract Contract) []PathStatus {
	keys := make([]string, 0, len(contract.Policies))
	for key := range contract.Policies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	statuses := make([]PathStatus, 0, len(keys)+1)
	statuses = append(statuses, expectedFile("contract", contract.RootPath, contractRelPath, true))
	for _, key := range keys {
		if key == PolicyBackupContracts {
			continue
		}
		enabled := true
		if area, ok := contract.Areas[key]; ok {
			enabled = area.Enabled
		}
		statuses = append(statuses, expectedFile(key, contract.RootPath, contract.Policies[key], enabled))
	}
	return statuses
}

func expectedDirectory(key, root, rel string, enabled bool) PathStatus {
	return PathStatus{
		Key:          key,
		Path:         filepath.Join(root, filepath.FromSlash(rel)),
		RelativePath: rel,
		Kind:         "directory",
		Enabled:      enabled,
	}
}

func expectedFile(key, root, rel string, enabled bool) PathStatus {
	return PathStatus{
		Key:          key,
		Path:         filepath.Join(root, filepath.FromSlash(rel)),
		RelativePath: rel,
		Kind:         "file",
		Enabled:      enabled,
	}
}

func validateRelativePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path is required")
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("path contains NUL")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") {
		return fmt.Errorf("path must be relative")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return fmt.Errorf("path must not contain ..")
		}
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path must resolve inside the box")
	}
	return nil
}
