package backupcontracts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"loom.local/loom/internal/filepolicy"
)

const (
	MigrationStatusMigrated  = "migrated"
	MigrationStatusUnchanged = "unchanged"
	MigrationStatusAttention = "attention"
)

type MigrateIgnorePolicyInput struct {
	BoxRoot          string
	DirectoryRelPath string
	DryRun           bool
	Apply            bool
	Yes              bool
}

type MigrateIgnorePolicyResult struct {
	DryRun         bool                        `json:"dry_run"`
	Applied        bool                        `json:"applied"`
	ChangedCount   int                         `json:"changed_count"`
	AttentionCount int                         `json:"attention_count"`
	Items          []IgnorePolicyMigrationItem `json:"items"`
}

type IgnorePolicyMigrationItem struct {
	Key               string   `json:"key"`
	Path              string   `json:"path"`
	Status            string   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	FromSchema        string   `json:"from_schema,omitempty"`
	ToSchema          string   `json:"to_schema,omitempty"`
	RemovedExcludes   []string `json:"removed_excludes,omitempty"`
	PreservedExcludes []string `json:"preserved_excludes,omitempty"`
	BeforeYAML        string   `json:"before_yaml,omitempty"`
	AfterYAML         string   `json:"after_yaml,omitempty"`
}

func MigrateIgnorePolicy(input MigrateIgnorePolicyInput) (MigrateIgnorePolicyResult, error) {
	if input.Apply && !input.Yes {
		return MigrateIgnorePolicyResult{}, fmt.Errorf("backup contract ignore-policy migration requires yes confirmation when applying")
	}
	if input.Apply && input.DryRun {
		return MigrateIgnorePolicyResult{}, fmt.Errorf("choose either dry-run or apply for backup contract ignore-policy migration")
	}
	if !input.Apply {
		input.DryRun = true
	}
	directory, err := contractsDirectoryPath(input.BoxRoot, input.DirectoryRelPath)
	if err != nil {
		return MigrateIgnorePolicyResult{}, err
	}
	result := MigrateIgnorePolicyResult{DryRun: input.DryRun, Applied: input.Apply, Items: []IgnorePolicyMigrationItem{}}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("list backup contract directory %s: %w", directory, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		pathValue := filepath.Join(directory, entry.Name())
		item, migratedPayload := planIgnorePolicyMigration(pathValue, strings.TrimSuffix(entry.Name(), ".yaml"))
		result.Items = append(result.Items, item)
		switch item.Status {
		case MigrationStatusMigrated:
			result.ChangedCount++
			if input.Apply {
				if err := writeContractFile(pathValue, migratedPayload); err != nil {
					return result, fmt.Errorf("write migrated backup contract %s: %w", pathValue, err)
				}
			}
		case MigrationStatusAttention:
			result.AttentionCount++
		}
	}
	sort.Slice(result.Items, func(left, right int) bool { return result.Items[left].Path < result.Items[right].Path })
	return result, nil
}

func planIgnorePolicyMigration(pathValue, fallbackKey string) (IgnorePolicyMigrationItem, []byte) {
	item := IgnorePolicyMigrationItem{Key: fallbackKey, Path: pathValue}
	payload, err := os.ReadFile(pathValue)
	if err != nil {
		item.Status = MigrationStatusAttention
		item.Reason = err.Error()
		return item, nil
	}
	item.BeforeYAML = string(payload)
	var contract Contract
	if err := yaml.Unmarshal(payload, &contract); err != nil {
		item.Status = MigrationStatusAttention
		item.Reason = "invalid YAML: " + err.Error()
		return item, nil
	}
	contract = Normalize(contract)
	if contract.Key != "" {
		item.Key = contract.Key
	}
	item.FromSchema = contract.SchemaVersion
	if contract.SchemaVersion == SchemaVersion && contract.Ignore != nil {
		item.Status = MigrationStatusUnchanged
		item.ToSchema = SchemaVersion
		item.Reason = "contract already references the managed ignore profile"
		return item, payload
	}
	if contract.SchemaVersion != LegacySchemaVersion {
		item.Status = MigrationStatusAttention
		item.Reason = "unsupported or ambiguous contract schema"
		return item, nil
	}
	if err := Validate(contract); err != nil {
		item.Status = MigrationStatusAttention
		item.Reason = err.Error()
		return item, nil
	}

	legacyDefaults := DefaultExcludePatterns()
	legacySet := make(map[string]bool, len(legacyDefaults))
	for _, pattern := range legacyDefaults {
		legacySet[pattern] = true
	}
	overlap := 0
	preserved := []string{}
	for _, pattern := range contract.Exclude {
		if legacySet[pattern] {
			overlap++
		} else {
			preserved = append(preserved, pattern)
		}
	}
	if overlap > 0 && overlap != len(legacyDefaults) {
		item.Status = MigrationStatusAttention
		item.Reason = "legacy default origin is ambiguous because only part of the known expansion is present"
		item.PreservedExcludes = append([]string{}, contract.Exclude...)
		return item, nil
	}
	contract.SchemaVersion = SchemaVersion
	contract.Ignore = &IgnorePolicy{Profile: string(filepolicy.ProfileManaged), DiscoverUserRules: true}
	contract.Exclude = preserved
	migratedPayload, err := Render(contract)
	if err != nil {
		item.Status = MigrationStatusAttention
		item.Reason = err.Error()
		return item, nil
	}
	item.Status = MigrationStatusMigrated
	item.ToSchema = SchemaVersion
	item.PreservedExcludes = append([]string{}, preserved...)
	if overlap == len(legacyDefaults) {
		item.RemovedExcludes = append([]string{}, legacyDefaults...)
	}
	item.AfterYAML = string(migratedPayload)
	return item, migratedPayload
}
