package backupcontracts

import (
	"encoding/json"
	"strings"
	"time"

	"loom.local/loom/internal/projectcontracts"
)

type CreateRequest struct {
	Key           string   `json:"key"`
	DisplayName   string   `json:"display_name,omitempty"`
	OwnerNode     string   `json:"owner_node,omitempty"`
	TargetPath    string   `json:"target_path"`
	TargetScope   string   `json:"target_scope,omitempty"`
	Include       []string `json:"include,omitempty"`
	Exclude       []string `json:"exclude,omitempty"`
	BackupMode    string   `json:"backup_mode,omitempty"`
	MaxFileBytes  int64    `json:"max_file_bytes,omitempty"`
	MaxBatchBytes int64    `json:"max_batch_bytes,omitempty"`
	Replace       bool     `json:"replace,omitempty"`
	DryRun        bool     `json:"dry_run,omitempty"`
	PreflightID   string   `json:"preflight_id,omitempty"`
}

type DisableRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type DeleteRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type EnableRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type RetryActivationRequest struct {
	Reason string `json:"reason,omitempty"`
}

type PreflightCreateRequest struct {
	NodeRef        string                          `json:"node_ref"`
	Path           string                          `json:"path"`
	Recheck        *ProtectedFolderRecheckIdentity `json:"recheck,omitempty"`
	Ignore         *IgnorePolicy                   `json:"ignore,omitempty"`
	Include        []string                        `json:"include,omitempty"`
	Exclude        []string                        `json:"exclude,omitempty"`
	Budget         PreflightBudget                 `json:"budget,omitempty"`
	IdempotencyKey string                          `json:"idempotency_key,omitempty"`
}

type PreflightFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Blocking bool   `json:"blocking"`
}

type PreflightPolicyEvidence struct {
	Profile        string          `json:"profile"`
	Fingerprint    string          `json:"fingerprint,omitempty"`
	PolicyFiles    []string        `json:"policy_files,omitempty"`
	RuleProvenance json.RawMessage `json:"rule_provenance,omitempty"`
	IncludedCount  int64           `json:"included_count"`
	IncludedBytes  int64           `json:"included_bytes"`
	IgnoredCount   int64           `json:"ignored_count"`
	IgnoredBytes   int64           `json:"ignored_bytes"`
}

type PreflightResult struct {
	SchemaVersion  string                  `json:"schema_version"`
	RequestedPath  string                  `json:"requested_path"`
	CanonicalPath  string                  `json:"canonical_path,omitempty"`
	Exists         bool                    `json:"exists"`
	Directory      bool                    `json:"directory"`
	Readable       bool                    `json:"readable"`
	FilesystemType string                  `json:"filesystem_type,omitempty"`
	MountPoint     string                  `json:"mount_point,omitempty"`
	FileCount      int64                   `json:"file_count"`
	DirectoryCount int64                   `json:"directory_count"`
	ApparentBytes  int64                   `json:"apparent_bytes"`
	Truncated      bool                    `json:"truncated"`
	Budget         PreflightBudget         `json:"budget"`
	Policy         PreflightPolicyEvidence `json:"policy"`
	Findings       []PreflightFinding      `json:"findings,omitempty"`
}

type PreflightRecord struct {
	PreflightID            string           `json:"preflight_id"`
	TargetNodeID           string           `json:"target_node_id"`
	RequestedPath          string           `json:"requested_path"`
	CanonicalPath          string           `json:"canonical_path,omitempty"`
	Status                 string           `json:"status"`
	RequestSchemaVersion   string           `json:"request_schema_version"`
	ResponseSchemaVersion  string           `json:"response_schema_version,omitempty"`
	CommunicationMessageID string           `json:"communication_message_id,omitempty"`
	Result                 *PreflightResult `json:"result,omitempty"`
	ErrorCode              string           `json:"error_code,omitempty"`
	ErrorMessage           string           `json:"error_message,omitempty"`
	ExpiresAt              time.Time        `json:"expires_at"`
	CreatedAt              time.Time        `json:"created_at"`
	CompletedAt            *time.Time       `json:"completed_at,omitempty"`
	RetryOfPreflightID     string           `json:"retry_of_preflight_id,omitempty"`
}

type MigrateIgnorePolicyRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
	Apply  bool `json:"apply,omitempty"`
	Yes    bool `json:"yes,omitempty"`
}

type MigrateIgnorePolicyResponse struct {
	Migration    MigrateIgnorePolicyResult                 `json:"migration"`
	WatchedRoots []projectcontracts.ProjectWatchedRootItem `json:"watched_roots,omitempty"`
	Reconciled   bool                                      `json:"reconciled"`
}

type ContractRecord struct {
	Key      string    `json:"key"`
	Path     string    `json:"path"`
	Status   string    `json:"status,omitempty"`
	Contract *Contract `json:"contract,omitempty"`
	Error    string    `json:"error,omitempty"`
}

type ListResult struct {
	Contracts []ContractRecord   `json:"contracts"`
	Problems  []WatchPlanProblem `json:"problems,omitempty"`
}

type LifecycleResult struct {
	Action        string                                    `json:"action"`
	DryRun        bool                                      `json:"dry_run"`
	Path          string                                    `json:"path"`
	DirectoryPath string                                    `json:"directory_path,omitempty"`
	Contract      Contract                                  `json:"contract"`
	RenderedYAML  string                                    `json:"rendered_yaml,omitempty"`
	WatchedRoots  []projectcontracts.ProjectWatchedRootItem `json:"watched_roots,omitempty"`
	Problems      []WatchPlanProblem                        `json:"problems,omitempty"`
	// Applied is retained for old JSON clients and now means node-applied, not
	// main-side registration. New clients should use the explicit fields.
	Applied                bool  `json:"applied"`
	DesiredStateRegistered bool  `json:"desired_state_registered"`
	NodeApplyQueued        bool  `json:"node_apply_queued"`
	NodeApplied            bool  `json:"node_applied"`
	DesiredRevision        int64 `json:"desired_revision,omitempty"`
	AppliedRevision        int64 `json:"applied_revision,omitempty"`
}

type ProtectedFolderRecord struct {
	// Key, Path, and Status keep older local clients source-compatible while
	// lifecycle evidence is adopted by the CLI and Portal.
	Key                   string           `json:"key"`
	Path                  string           `json:"path"`
	Status                string           `json:"status"`
	Contract              Contract         `json:"contract"`
	ContractPath          string           `json:"contract_path"`
	Lifecycle             string           `json:"lifecycle"`
	AttentionReason       string           `json:"attention_reason,omitempty"`
	MissingContract       bool             `json:"missing_contract,omitempty"`
	Deleting              bool             `json:"deleting,omitempty"`
	Preflight             *PreflightRecord `json:"preflight,omitempty"`
	OwnerNodeID           string           `json:"owner_node_id,omitempty"`
	OwnerNodeKey          string           `json:"owner_node_key,omitempty"`
	RootKey               string           `json:"root_key,omitempty"`
	DesiredRegistered     bool             `json:"desired_registered"`
	NodeApplied           bool             `json:"node_applied"`
	RootReported          bool             `json:"root_reported"`
	ConfigHashMatches     bool             `json:"config_hash_matches"`
	DesiredRevision       int64            `json:"desired_revision,omitempty"`
	AppliedRevision       int64            `json:"applied_revision,omitempty"`
	DesiredConfigHash     string           `json:"desired_config_hash,omitempty"`
	AppliedConfigHash     string           `json:"applied_config_hash,omitempty"`
	ReconciliationMessage string           `json:"reconciliation_message_id,omitempty"`
	LastBackupAcceptedAt  *time.Time       `json:"last_backup_accepted_at,omitempty"`
	Details               json.RawMessage  `json:"details,omitempty"`
}

type ProtectedFolderListResult struct {
	Folders []ProtectedFolderRecord `json:"folders"`
	Counts  map[string]int          `json:"counts"`
	// Contracts is a temporary compatibility view for pre-lifecycle clients.
	Contracts []ContractRecord   `json:"contracts,omitempty"`
	Problems  []WatchPlanProblem `json:"problems,omitempty"`
}

func ContractFromCreateRequest(input CreateRequest, defaultOwnerNode string) Contract {
	ownerNode := strings.TrimSpace(input.OwnerNode)
	if ownerNode == "" {
		ownerNode = strings.TrimSpace(defaultOwnerNode)
	}
	return Contract{
		Key:         input.Key,
		DisplayName: input.DisplayName,
		OwnerNode:   ownerNode,
		Target: TargetSpec{
			Scope: input.TargetScope,
			Path:  input.TargetPath,
		},
		Include: input.Include,
		Exclude: input.Exclude,
		Ignore: &IgnorePolicy{
			Profile:           "managed",
			DiscoverUserRules: true,
		},
		Backup: BackupPolicy{
			Mode:          input.BackupMode,
			MaxFileBytes:  input.MaxFileBytes,
			MaxBatchBytes: input.MaxBatchBytes,
		},
	}
}

func RecordsFromStored(items []StoredContract) ListResult {
	result := ListResult{Contracts: []ContractRecord{}}
	for _, item := range items {
		record := ContractRecord{
			Key:   item.Key,
			Path:  item.Path,
			Error: item.Error,
		}
		if item.Error == "" {
			contract := item.Contract
			record.Status = contract.Status
			record.Contract = &contract
		} else {
			result.Problems = append(result.Problems, WatchPlanProblem{Key: item.Key, Path: item.Path, Message: item.Error})
		}
		result.Contracts = append(result.Contracts, record)
	}
	return result
}
