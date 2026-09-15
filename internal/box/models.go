package box

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/dropzone"
	"loom.local/loom/internal/lane"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/watchedroots"
)

const (
	SchemaVersion = "loom.box.v0.4.1"

	DefaultRootDirName            = "loom-box"
	LegacyRootDirName             = "LOOM Box"
	DefaultLaneDirName            = "loom-lane"
	LegacyLaneDirName             = "LOOM Lane"
	DefaultStorageLinkName        = "loom-storage"
	LegacyStorageLinkName         = "LOOM Storage"
	DefaultMainBoxLinkName        = "loom-main-box"
	LegacyMainBoxLinkName         = "LOOM Main Box"
	DefaultAcceptanceDirName      = ".loom-acceptance"
	LegacyAcceptanceDirName       = "LOOM Acceptance"
	LegacyHyphenAcceptanceDirName = "loom-acceptance"

	ProfileWorkspace = "workspace"
	ProfileMain      = "main"

	AreaProjects  = "projects"
	AreaNotes     = "notes"
	AreaDocuments = "documents"
	AreaLane      = "lane"
	AreaLaunchpad = "launchpad"
	AreaDropzone  = "dropzone"

	PolicyBackupContracts        = "backup_contracts"
	DefaultBackupContractsRelDir = ".loom/contracts/backup"

	DropzoneTransferInactive = "inactive"
	DropzoneTransferFuture   = "scaffolded_for_v0.4.2"
)

var CanonicalAreas = []string{AreaProjects, AreaNotes, AreaDocuments, AreaLane, AreaDropzone}

type Contract struct {
	SchemaVersion      string            `json:"schema_version" yaml:"schema_version"`
	BoxID              string            `json:"box_id" yaml:"box_id"`
	OwnerNode          string            `json:"owner_node" yaml:"owner_node"`
	Profile            string            `json:"profile" yaml:"profile"`
	RootPath           string            `json:"root_path" yaml:"root_path"`
	Areas              map[string]Area   `json:"areas" yaml:"areas"`
	DefaultProjectPath string            `json:"default_project_path" yaml:"default_project_path"`
	Policies           map[string]string `json:"policies" yaml:"policies"`
	Metadata           Metadata          `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type Area struct {
	Path           string `json:"path" yaml:"path"`
	Enabled        bool   `json:"enabled" yaml:"enabled"`
	TransferStatus string `json:"transfer_status,omitempty" yaml:"transfer_status,omitempty"`
}

type Metadata struct {
	CreatedAt string `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	CreatedBy string `json:"created_by,omitempty" yaml:"created_by,omitempty"`
}

type ResolveInput struct {
	ExplicitPath     string
	ConfiguredPath   string
	RuntimeStateRoot string
	ExplicitProfile  string
	ConfigProfile    string
	NodeID           string
	NodeRole         string
	HomeDir          string
}

type Resolved struct {
	RootPath         string `json:"root_path"`
	PathSource       string `json:"path_source"`
	Profile          string `json:"profile"`
	ProfileSource    string `json:"profile_source"`
	OwnerNode        string `json:"owner_node"`
	NodeRole         string `json:"node_role"`
	RuntimeStateRoot string `json:"runtime_state_root"`
	LegacyStateRoot  string `json:"legacy_state_root"`
}

type Status struct {
	SchemaVersion                 string           `json:"schema_version"`
	RootPath                      string           `json:"root_path"`
	PathSource                    string           `json:"path_source"`
	Profile                       string           `json:"profile"`
	ProfileSource                 string           `json:"profile_source"`
	OwnerNode                     string           `json:"owner_node"`
	NodeRole                      string           `json:"node_role"`
	RuntimeStateRoot              string           `json:"runtime_state_root"`
	RuntimeStateReadRoot          string           `json:"runtime_state_read_root"`
	RuntimeStateWriteRoot         string           `json:"runtime_state_write_root"`
	RuntimeStateSource            string           `json:"runtime_state_source"`
	RuntimeStateMigrationRequired bool             `json:"runtime_state_migration_required"`
	State                         string           `json:"state"`
	Initialized                   bool             `json:"initialized"`
	ContractPath                  string           `json:"contract_path"`
	ContractState                 string           `json:"contract_state"`
	Contract                      *Contract        `json:"contract,omitempty"`
	DefaultProjectPath            string           `json:"default_project_path"`
	LaneState                     string           `json:"lane_state"`
	Lane                          *lane.Status     `json:"lane,omitempty"`
	DropzoneState                 string           `json:"dropzone_state"`
	DropzoneTransfers             *dropzone.Status `json:"dropzone_transfers,omitempty"`
	Areas                         []PathStatus     `json:"areas"`
	Policies                      []PathStatus     `json:"policies"`
	Diagnostics                   []Diagnostic     `json:"diagnostics,omitempty"`
	InspectedAt                   time.Time        `json:"inspected_at"`
	WouldCreateFolders            []string         `json:"would_create_folders,omitempty"`
	WouldCreateFiles              []string         `json:"would_create_files,omitempty"`
}

type PathStatus struct {
	Key          string `json:"key"`
	Path         string `json:"path"`
	RelativePath string `json:"relative_path"`
	Kind         string `json:"kind"`
	Exists       bool   `json:"exists"`
	IsDir        bool   `json:"is_dir"`
	Status       string `json:"status"`
	Enabled      bool   `json:"enabled,omitempty"`
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

type PathResult struct {
	RootPath         string `json:"root_path"`
	PathSource       string `json:"path_source"`
	Profile          string `json:"profile"`
	ProfileSource    string `json:"profile_source"`
	OwnerNode        string `json:"owner_node"`
	NodeRole         string `json:"node_role"`
	RuntimeStateRoot string `json:"runtime_state_root"`
}

type RuntimeStateResolution struct {
	ReadRoot          string `json:"read_root"`
	WriteRoot         string `json:"write_root"`
	Source            string `json:"source"`
	MigrationRequired bool   `json:"migration_required"`
}

type InitInput struct {
	Resolved Resolved
	DryRun   bool
	Now      func() time.Time
	NewBoxID func() string
}

type InitResult struct {
	SchemaVersion  string       `json:"schema_version"`
	DryRun         bool         `json:"dry_run"`
	RootPath       string       `json:"root_path"`
	Profile        string       `json:"profile"`
	OwnerNode      string       `json:"owner_node"`
	StatusBefore   Status       `json:"status_before"`
	StatusAfter    Status       `json:"status_after"`
	CreatedDirs    []string     `json:"created_dirs,omitempty"`
	CreatedFiles   []string     `json:"created_files,omitempty"`
	CreatedAliases []string     `json:"created_aliases,omitempty"`
	BackupFiles    []string     `json:"backup_files,omitempty"`
	SkippedDirs    []string     `json:"skipped_dirs,omitempty"`
	SkippedFiles   []string     `json:"skipped_files,omitempty"`
	SkippedAliases []string     `json:"skipped_aliases,omitempty"`
	PlannedDirs    []string     `json:"planned_dirs,omitempty"`
	PlannedFiles   []string     `json:"planned_files,omitempty"`
	PlannedAliases []string     `json:"planned_aliases,omitempty"`
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
}

type WatchPolicy struct {
	SchemaVersion string            `json:"schema_version" yaml:"schema_version"`
	Area          string            `json:"area" yaml:"area"`
	Path          string            `json:"path" yaml:"path"`
	Enabled       bool              `json:"enabled" yaml:"enabled"`
	Mode          string            `json:"mode" yaml:"mode"`
	Semantics     string            `json:"semantics" yaml:"semantics"`
	Catalog       WatchPolicyToggle `json:"catalog" yaml:"catalog"`
	Object        WatchPolicyToggle `json:"object" yaml:"object"`
	Text          WatchPolicyToggle `json:"text" yaml:"text"`
	Index         WatchPolicyToggle `json:"index" yaml:"index"`
	Sync          WatchPolicyToggle `json:"sync" yaml:"sync"`
	Backup        WatchPolicyToggle `json:"backup" yaml:"backup"`
	Delete        string            `json:"delete_semantics" yaml:"delete_semantics"`
	Notes         string            `json:"notes,omitempty" yaml:"notes,omitempty"`
	Metadata      map[string]any    `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type WatchPolicyToggle struct {
	Enabled bool `json:"enabled" yaml:"enabled"`
}

type WatchPlan struct {
	SchemaVersion string                                    `json:"schema_version"`
	ProjectRoot   string                                    `json:"project_root"`
	RootPath      string                                    `json:"root_path"`
	Profile       string                                    `json:"profile"`
	OwnerNode     string                                    `json:"owner_node"`
	BoxID         string                                    `json:"box_id"`
	ContractPath  string                                    `json:"contract_path"`
	WatchedRoots  []projectcontracts.ProjectWatchedRootItem `json:"watched_roots"`
	Commands      []projects.ProjectWatchedRootCommand      `json:"commands,omitempty"`
	Excluded      []WatchExcludedArea                       `json:"excluded,omitempty"`
	Diagnostics   []Diagnostic                              `json:"diagnostics,omitempty"`
}

type WatchExcludedArea struct {
	Area   string `json:"area"`
	Reason string `json:"reason"`
}

type WatchApplyInput struct {
	Resolved Resolved   `json:"resolved"`
	Plan     *WatchPlan `json:"plan,omitempty"`
	DryRun   bool       `json:"dry_run,omitempty"`
}

type WatchApplyResult struct {
	DryRun        bool                    `json:"dry_run"`
	Plan          WatchPlan               `json:"plan"`
	Scopes        []WatchScope            `json:"scopes,omitempty"`
	Registrations []WatchRootRegistration `json:"registrations,omitempty"`
	Nodes         []WatchNodeApplyResult  `json:"nodes,omitempty"`
}

type WatchNodeApplyResult struct {
	NodeID        string                  `json:"node_id"`
	OwnerNodeKey  string                  `json:"owner_node_key"`
	SourceKinds   []string                `json:"source_kinds,omitempty"`
	Registrations []WatchRootRegistration `json:"registrations,omitempty"`
}

type WatchStatusInput struct {
	Resolved Resolved   `json:"resolved"`
	Plan     *WatchPlan `json:"plan,omitempty"`
}

type WatchStatusResult struct {
	Plan          WatchPlan                 `json:"plan"`
	Scopes        []WatchScope              `json:"scopes,omitempty"`
	Registrations []WatchRootRegistration   `json:"registrations"`
	Statuses      []watchedroots.RootStatus `json:"statuses,omitempty"`
}

type WatchScope struct {
	ScopeID     string          `json:"scope_id"`
	ScopeType   string          `json:"scope_type"`
	ScopeKey    string          `json:"scope_key"`
	Slug        string          `json:"slug"`
	DisplayName string          `json:"display_name"`
	AreaKey     string          `json:"area_key"`
	BoxID       string          `json:"box_id"`
	Created     bool            `json:"created"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type WatchRootRegistration struct {
	BoxWatchRootRegistrationID string          `json:"box_watch_root_registration_id"`
	BoxID                      string          `json:"box_id"`
	BoxRootPath                string          `json:"box_root_path"`
	BoxContractPath            string          `json:"box_contract_path"`
	NodeID                     string          `json:"node_id"`
	OwnerNodeKey               string          `json:"owner_node_key"`
	AreaKey                    string          `json:"area_key"`
	LocalRootKey               string          `json:"local_root_key"`
	BackendRootKey             string          `json:"backend_root_key"`
	WorkerKey                  string          `json:"worker_key"`
	SourceKinds                json.RawMessage `json:"source_kinds"`
	SafeRootKey                string          `json:"safe_root_key"`
	RootRelativePath           string          `json:"root_relative_path"`
	DisplayName                string          `json:"display_name"`
	SyncMode                   string          `json:"sync_mode"`
	BackupMode                 string          `json:"backup_mode"`
	IndexMode                  string          `json:"index_mode"`
	DeleteMode                 string          `json:"delete_mode"`
	ConfigHash                 string          `json:"config_hash"`
	ConfigJSON                 json.RawMessage `json:"config_json"`
	CommandJSON                json.RawMessage `json:"command_json"`
	WatchedRootID              *string         `json:"watched_root_id,omitempty"`
	ActivationStatus           string          `json:"activation_status"`
	LastAppliedByActorID       *string         `json:"last_applied_by_actor_id,omitempty"`
	LastAppliedAt              *time.Time      `json:"last_applied_at,omitempty"`
	LastReportedAt             *time.Time      `json:"last_reported_at,omitempty"`
	SourceKind                 string          `json:"source_kind,omitempty"`
	SourceContractKey          string          `json:"source_contract_key,omitempty"`
	SourceContractPath         string          `json:"source_contract_path,omitempty"`
	SourceContractDeletedAt    *time.Time      `json:"source_contract_deleted_at,omitempty"`
	DesiredRevision            int64           `json:"desired_revision"`
	AppliedRevision            int64           `json:"applied_revision"`
	DesiredConfigHash          string          `json:"desired_config_hash,omitempty"`
	AppliedConfigHash          string          `json:"applied_config_hash,omitempty"`
	ReconciliationMessageID    *string         `json:"reconciliation_message_id,omitempty"`
	LastNodeAckStatus          string          `json:"last_node_ack_status,omitempty"`
	LastNodeAcknowledgedAt     *time.Time      `json:"last_node_acknowledged_at,omitempty"`
	LastApplyErrorCode         string          `json:"last_apply_error_code,omitempty"`
	LastApplyErrorMessage      string          `json:"last_apply_error_message,omitempty"`
	Metadata                   json.RawMessage `json:"metadata"`
	CreatedAt                  time.Time       `json:"created_at"`
	UpdatedAt                  time.Time       `json:"updated_at"`
}

func (c Contract) JSON() json.RawMessage {
	payload, err := json.Marshal(c)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}
