package backupcontracts

import "time"

const (
	SchemaVersion       = "loom.backup.contract.v1"
	LegacySchemaVersion = "loom.backup.contract.v0.9.9"

	DefaultDirectoryRelPath = ".loom/contracts/backup"

	StatusActive   = "active"
	StatusDisabled = "disabled"

	TargetScopeOwnerNodeAbsolute = "owner_node_absolute"
	TargetScopeBoxRelative       = "box_relative"

	BackupModeIncrementalRaw = "incremental_raw"
	BackupModeMetadataOnly   = "metadata_only"
	BackupModeNone           = "none"

	DefaultMaxFileBytes  int64 = 50 * 1024 * 1024
	DefaultMaxBatchBytes int64 = 250 * 1024 * 1024
)

type Contract struct {
	SchemaVersion string        `json:"schema_version" yaml:"schema_version"`
	Key           string        `json:"key" yaml:"key"`
	DisplayName   string        `json:"display_name" yaml:"display_name"`
	OwnerNode     string        `json:"owner_node" yaml:"owner_node"`
	Status        string        `json:"status" yaml:"status"`
	Target        TargetSpec    `json:"target" yaml:"target"`
	Include       []string      `json:"include" yaml:"include"`
	Exclude       []string      `json:"exclude" yaml:"exclude"`
	Ignore        *IgnorePolicy `json:"ignore,omitempty" yaml:"ignore,omitempty"`
	Backup        BackupPolicy  `json:"backup" yaml:"backup"`
	Metadata      Metadata      `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type IgnorePolicy struct {
	Profile           string `json:"profile" yaml:"profile"`
	DiscoverUserRules bool   `json:"discover_user_rules" yaml:"discover_user_rules"`
}

type TargetSpec struct {
	Scope string `json:"scope" yaml:"scope"`
	Path  string `json:"path" yaml:"path"`
}

type BackupPolicy struct {
	Mode          string `json:"mode" yaml:"mode"`
	MaxFileBytes  int64  `json:"max_file_bytes,omitempty" yaml:"max_file_bytes,omitempty"`
	MaxBatchBytes int64  `json:"max_batch_bytes,omitempty" yaml:"max_batch_bytes,omitempty"`
}

type Metadata struct {
	CreatedAt  string `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	CreatedBy  string `json:"created_by,omitempty" yaml:"created_by,omitempty"`
	DisabledAt string `json:"disabled_at,omitempty" yaml:"disabled_at,omitempty"`
	DisabledBy string `json:"disabled_by,omitempty" yaml:"disabled_by,omitempty"`
}

type StoredContract struct {
	Key      string
	Path     string
	Raw      []byte
	Contract Contract
	Error    string
}

type MutateInput struct {
	BoxRoot          string
	DirectoryRelPath string
	Contract         Contract
	Replace          bool
	DryRun           bool
	Actor            string
	Now              func() time.Time
}

type DisableInput struct {
	BoxRoot          string
	DirectoryRelPath string
	Key              string
	DryRun           bool
	Actor            string
	Now              func() time.Time
}

type DeleteInput struct {
	BoxRoot          string
	DirectoryRelPath string
	Key              string
	DryRun           bool
}

type EnableInput struct {
	BoxRoot          string
	DirectoryRelPath string
	Key              string
	DryRun           bool
	Actor            string
	Now              func() time.Time
}

type MutateResult struct {
	DryRun        bool
	Path          string
	DirectoryPath string
	Action        string
	Contract      Contract
	RenderedYAML  string
	Created       bool
	Updated       bool
	Deleted       bool
}
