package backupcontracts

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemconnector"
	noderuntime "loom.local/loom/internal/nodeagent/runtime"
	agentwatchedroots "loom.local/loom/internal/nodeagent/watchedroots"
	"loom.local/loom/internal/projectcontracts"
)

const (
	SourceKindBoxBackupContract = "box_backup_contract"
	MetadataSource              = "box.backup_contract"
)

type WatchPlanOptions struct {
	BoxRoot          string
	BoxID            string
	OwnerNode        string
	DirectoryRelPath string
}

type WatchPlanResult struct {
	Items    []projectcontracts.ProjectWatchedRootItem
	Skipped  []StoredContract
	Problems []WatchPlanProblem
}

type WatchPlanProblem struct {
	Key     string
	Path    string
	Message string
}

func PlanWatchedRoots(options WatchPlanOptions) (WatchPlanResult, error) {
	stored, err := List(options.BoxRoot, options.DirectoryRelPath)
	if err != nil {
		return WatchPlanResult{}, err
	}
	result := WatchPlanResult{}
	for _, entry := range stored {
		if entry.Error != "" {
			result.Problems = append(result.Problems, WatchPlanProblem{Key: entry.Key, Path: entry.Path, Message: entry.Error})
			continue
		}
		if entry.Contract.Status == StatusDisabled {
			result.Skipped = append(result.Skipped, entry)
			continue
		}
		item, err := WatchedRootItem(entry.Contract, entry.Path, options)
		if err != nil {
			result.Problems = append(result.Problems, WatchPlanProblem{Key: entry.Key, Path: entry.Path, Message: err.Error()})
			continue
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func WatchedRootItem(contract Contract, contractPath string, options WatchPlanOptions) (projectcontracts.ProjectWatchedRootItem, error) {
	contract = Normalize(contract)
	contract.OwnerNode = firstNonEmpty(contract.OwnerNode, options.OwnerNode)
	if err := Validate(contract); err != nil {
		return projectcontracts.ProjectWatchedRootItem{}, err
	}
	if contract.Status != StatusActive {
		return projectcontracts.ProjectWatchedRootItem{}, fmt.Errorf("backup contract %q is not active", contract.Key)
	}
	backendRootKey := BackendRootKey(contract.Key)
	safeRootKey := "loom_box"
	rootRelativePath := contract.Target.Path
	if contract.Target.Scope == TargetScopeOwnerNodeAbsolute {
		safeRootKey = SafeRootKey(contract.Key)
		rootRelativePath = "."
	}
	backupMode := watchedRootBackupMode(contract.Backup.Mode)
	config := agentwatchedroots.RootConfig{
		SchemaVersion:    agentwatchedroots.ConfigSchemaVersion,
		RootKey:          backendRootKey,
		DisplayName:      displayName(contract),
		SafeRootKey:      safeRootKey,
		RootRelativePath: rootRelativePath,
		Include:          append([]string{}, contract.Include...),
		Exclude:          append([]string{}, contract.Exclude...),
		Watch:            agentwatchedroots.WatchConfig{Enabled: true, Backend: agentwatchedroots.WatchBackendAuto},
		Scan: agentwatchedroots.ScanConfig{
			FullRescanInterval: "1m",
			MaxHashFileBytes:   maxEnabledBytes(contract.Backup.MaxFileBytes),
		},
		BackupPolicy: agentwatchedroots.BackupPolicy{
			Mode:          backupMode,
			MaxFileBytes:  maxEnabledBytes(contract.Backup.MaxFileBytes),
			MaxBatchBytes: maxEnabledBytes(contract.Backup.MaxBatchBytes),
		},
		SyncPolicy:   agentwatchedroots.SyncPolicy{Mode: agentwatchedroots.SyncModeNone},
		IndexPolicy:  agentwatchedroots.IndexPolicy{Mode: agentwatchedroots.IndexModeNone},
		DeletePolicy: agentwatchedroots.DeletePolicy{Mode: agentwatchedroots.DeleteModeLocalStateOnly},
	}
	if contract.Ignore != nil {
		config.IgnorePolicy = agentwatchedroots.IgnorePolicy{
			Profile:                contract.Ignore.Profile,
			DiscoverUserRules:      contract.Ignore.DiscoverUserRules,
			PolicyRootRelativePath: ".",
		}
		config.Scan.HiddenPolicy = agentwatchedroots.HiddenPolicyPolicyControlled
		if config.IgnorePolicy.Profile == "" {
			config.IgnorePolicy.Profile = string(filepolicy.ProfileManaged)
		}
	}
	validated, err := agentwatchedroots.ValidatePortableRootConfig(config)
	if err != nil {
		return projectcontracts.ProjectWatchedRootItem{}, fmt.Errorf("compiled watched-root config is invalid: %w", err)
	}
	configJSON, err := json.Marshal(validated)
	if err != nil {
		return projectcontracts.ProjectWatchedRootItem{}, fmt.Errorf("encode watched-root config: %w", err)
	}
	metadata := metadataForContract(contract, contractPath, options)
	return projectcontracts.ProjectWatchedRootItem{
		Key:              AreaKey(contract.Key),
		BackendRootKey:   backendRootKey,
		WorkerKey:        noderuntime.WatchedRootWorkerKey(backendRootKey),
		SourceKinds:      []string{SourceKindBoxBackupContract},
		OwnerNode:        firstNonEmpty(contract.OwnerNode, options.OwnerNode),
		SafeRootKey:      validated.SafeRootKey,
		RootRelativePath: validated.RootRelativePath,
		DisplayName:      validated.DisplayName,
		Include:          append([]string{}, validated.Include...),
		Exclude:          append([]string{}, validated.Exclude...),
		SyncMode:         validated.SyncPolicy.Mode,
		BackupMode:       validated.BackupPolicy.Mode,
		IndexMode:        validated.IndexPolicy.Mode,
		DeleteMode:       validated.DeletePolicy.Mode,
		ConfigHash:       agentwatchedroots.ConfigHash(validated),
		ConfigJSON:       json.RawMessage(configJSON),
		ActivationStatus: "pending_agent_apply",
		Metadata:         metadata,
	}, nil
}

func AreaKey(contractKey string) string {
	return "backup_" + strings.ToLower(strings.TrimSpace(contractKey))
}

func BackendRootKey(contractKey string) string {
	return "loom_box_backup__" + strings.ToLower(strings.TrimSpace(contractKey))
}

func SafeRootKey(contractKey string) string {
	key := filesystemconnector.NormalizeRootKey("backup_" + contractKey)
	if len(key) <= 63 {
		return key
	}
	key = strings.Trim(key[:63], "-_")
	if key == "" {
		return "backup"
	}
	return key
}

func IsBackupAreaKey(area string) bool {
	area = strings.ToLower(strings.TrimSpace(area))
	if !strings.HasPrefix(area, "backup_") {
		return false
	}
	return ValidateKey(strings.TrimPrefix(area, "backup_")) == nil
}

func IsBackupRootItem(item projectcontracts.ProjectWatchedRootItem) bool {
	if IsBackupAreaKey(item.Key) || strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.BackendRootKey)), "loom_box_backup__") {
		return true
	}
	for _, kind := range item.SourceKinds {
		if kind == SourceKindBoxBackupContract {
			return true
		}
	}
	if source, _ := item.Metadata["source"].(string); source == MetadataSource {
		return true
	}
	return false
}

func metadataForContract(contract Contract, contractPath string, options WatchPlanOptions) map[string]any {
	targetPath := contract.Target.Path
	if contract.Target.Scope == TargetScopeOwnerNodeAbsolute {
		targetPath = filepath.Clean(targetPath)
	}
	metadata := map[string]any{
		"source":        MetadataSource,
		"box_id":        strings.TrimSpace(options.BoxID),
		"box_root_path": strings.TrimSpace(options.BoxRoot),
		"contract_key":  contract.Key,
		"contract_path": strings.TrimSpace(contractPath),
		"target_scope":  contract.Target.Scope,
		"target_path":   targetPath,
		"status":        contract.Status,
		"owner_node":    firstNonEmpty(contract.OwnerNode, options.OwnerNode),
	}
	if contract.Target.Scope == TargetScopeOwnerNodeAbsolute {
		metadata["safe_root_key"] = SafeRootKey(contract.Key)
		metadata["safe_root_absolute_path"] = targetPath
		metadata["safe_root_private_backup_only"] = true
	}
	return metadata
}

func watchedRootBackupMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case BackupModeMetadataOnly:
		return agentwatchedroots.BackupModeMetadataOnly
	case BackupModeNone:
		return agentwatchedroots.BackupModeNone
	default:
		return agentwatchedroots.BackupModeIncrementalRaw
	}
}

func displayName(contract Contract) string {
	if strings.TrimSpace(contract.DisplayName) != "" {
		return contract.DisplayName
	}
	return "Backup " + strings.ReplaceAll(contract.Key, "_", " ")
}

func maxEnabledBytes(value int64) int64 {
	if value > 0 {
		return value
	}
	return DefaultMaxFileBytes
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
