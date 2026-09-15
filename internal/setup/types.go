package setup

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/enrollmentflow"
)

const (
	SchemaVersion         = "loom.setup.v0.5"
	ManifestSchemaVersion = "loom.install.v0.5"

	InstallModeService   = "service"
	InstallModeUser      = "user"
	InstallModeDeveloper = "developer"

	ServiceManagerSystemd = "systemd"
	ServiceManagerLaunchd = "launchd"
	ServiceManagerNone    = "none"

	PackageModeLocalBuild = "local-build"
	PackageModePrebuilt   = "prebuilt"
	PackageModeNix        = "nix"
	PackageModeUnknown    = "unknown"

	StepStatusPending          = "pending"
	StepStatusAlreadySatisfied = "already_satisfied"
	StepStatusWouldChange      = "would_change"
	StepStatusBlocked          = "blocked"
	StepStatusSkipped          = "skipped"

	DiagnosticInfo     = "info"
	DiagnosticWarning  = "warning"
	DiagnosticError    = "error"
	DiagnosticBlocking = "blocking"

	SummaryNotInstalled = "not_installed"
	SummaryPartial      = "partial"
	SummaryConfigured   = "configured"
	SummaryHealthy      = "healthy"
	SummaryDegraded     = "degraded"
	SummaryBlocked      = "blocked"
	SummaryUnknown      = "unknown"

	RepairAllSafe                       = "all-safe"
	RepairCreateMissingConfigDir        = "create-missing-config-dir"
	RepairCreateMissingStateDir         = "create-missing-state-dir"
	RepairCreateMissingDataDir          = "create-missing-data-dir"
	RepairCreateMissingLogDir           = "create-missing-log-dir"
	RepairCreateMissingBoxRoot          = "create-missing-box-root"
	RepairCreateMissingBoxLoomDir       = "create-missing-box-loom-dir"
	RepairCreateMissingMainDocuments    = "create-missing-main-documents"
	RepairCreateMissingStorageExport    = "create-missing-storage-export"
	RepairCreateMissingServiceRoot      = "create-missing-service-root"
	RepairCreateMissingStorageRoot      = "create-missing-storage-root"
	RepairCreateMissingImportsRoot      = "create-missing-imports-root"
	RepairCreateMissingUserBackupsRoot  = "create-missing-user-backups-root"
	RepairCreateMissingArchiveRoot      = "create-missing-archive-root"
	RepairCreateMissingGeneratedRoot    = "create-missing-generated-root"
	RepairCreateMissingBoxStateRoot     = "create-missing-box-state-root"
	RepairMainHumanLinks                = "repair-main-human-links"
	RepairMainBoxServiceACL             = "repair-main-box-service-acl"
	RepairCreateMissingNodeAgentDataDir = "create-missing-node-agent-data-dir"
	RepairInstallLaunchAgent            = "install-launch-agent"
	RepairRefreshWorkspaceBinaryLinks   = "refresh-workspace-binary-links"
	RepairRewriteRedactedManifest       = "rewrite-redacted-manifest"

	ApplyStatusChanged          = "changed"
	ApplyStatusAlreadySatisfied = "already_satisfied"
	ApplyStatusWouldChange      = "would_change"
	ApplyStatusSkipped          = "skipped"
	ApplyStatusBlocked          = "blocked"
)

type SetupSpec struct {
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`

	NodeKey      string `json:"node_key" yaml:"node_key"`
	DisplayName  string `json:"display_name" yaml:"display_name"`
	NodeKind     string `json:"node_kind" yaml:"node_kind"`
	NodeRole     string `json:"node_role" yaml:"node_role"`
	RuntimeClass string `json:"runtime_class" yaml:"runtime_class"`

	MainURL string `json:"main_url,omitempty" yaml:"main_url,omitempty"`

	InstallMode    string `json:"install_mode" yaml:"install_mode"`
	ServiceManager string `json:"service_manager" yaml:"service_manager"`
	PackageMode    string `json:"package_mode" yaml:"package_mode"`

	UserName string `json:"user_name,omitempty" yaml:"user_name,omitempty"`
	HomeDir  string `json:"home_dir,omitempty" yaml:"home_dir,omitempty"`

	SourcePath    string `json:"source_path,omitempty" yaml:"source_path,omitempty"`
	SourceCommit  string `json:"source_commit,omitempty" yaml:"source_commit,omitempty"`
	MigrationsDir string `json:"migrations_dir,omitempty" yaml:"migrations_dir,omitempty"`

	ConfigDir string `json:"config_dir,omitempty" yaml:"config_dir,omitempty"`
	DataDir   string `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
	StateDir  string `json:"state_dir,omitempty" yaml:"state_dir,omitempty"`
	LogDir    string `json:"log_dir,omitempty" yaml:"log_dir,omitempty"`

	ServiceRoot     string `json:"service_root,omitempty" yaml:"service_root,omitempty"`
	StorageRoot     string `json:"storage_root,omitempty" yaml:"storage_root,omitempty"`
	ImportsRoot     string `json:"imports_root,omitempty" yaml:"imports_root,omitempty"`
	UserBackupsRoot string `json:"user_backups_root,omitempty" yaml:"user_backups_root,omitempty"`
	ArchiveRoot     string `json:"archive_root,omitempty" yaml:"archive_root,omitempty"`
	GeneratedRoot   string `json:"generated_root,omitempty" yaml:"generated_root,omitempty"`
	BoxStateRoot    string `json:"box_state_root,omitempty" yaml:"box_state_root,omitempty"`

	BoxPath    string `json:"box_path,omitempty" yaml:"box_path,omitempty"`
	BoxProfile string `json:"box_profile,omitempty" yaml:"box_profile,omitempty"`

	NodeAgentConfigPath string `json:"node_agent_config_path,omitempty" yaml:"node_agent_config_path,omitempty"`
	NodeAgentStatePath  string `json:"node_agent_state_path,omitempty" yaml:"node_agent_state_path,omitempty"`
	NodeAgentDataDir    string `json:"node_agent_data_dir,omitempty" yaml:"node_agent_data_dir,omitempty"`

	ObjectStorePath   string `json:"object_store_path,omitempty" yaml:"object_store_path,omitempty"`
	MainDocumentsPath string `json:"main_documents_path,omitempty" yaml:"main_documents_path,omitempty"`
	StorageExportRoot string `json:"storage_export_root,omitempty" yaml:"storage_export_root,omitempty"`
	SocketPath        string `json:"socket_path,omitempty" yaml:"socket_path,omitempty"`
	HTTPListenAddr    string `json:"http_listen_addr,omitempty" yaml:"http_listen_addr,omitempty"`
	DBURL             string `json:"db_url,omitempty" yaml:"db_url,omitempty"`

	EnableCloud             bool   `json:"enable_cloud" yaml:"enable_cloud"`
	CloudConfigPath         string `json:"cloud_config_path,omitempty" yaml:"cloud_config_path,omitempty"`
	CloudStateDir           string `json:"cloud_state_dir,omitempty" yaml:"cloud_state_dir,omitempty"`
	CloudRcloneConfigPath   string `json:"cloud_rclone_config_path,omitempty" yaml:"cloud_rclone_config_path,omitempty"`
	CloudRemoteName         string `json:"cloud_remote_name,omitempty" yaml:"cloud_remote_name,omitempty"`
	CloudRemoteRoot         string `json:"cloud_remote_root,omitempty" yaml:"cloud_remote_root,omitempty"`
	CloudSnapshotBackend    string `json:"cloud_snapshot_backend,omitempty" yaml:"cloud_snapshot_backend,omitempty"`
	CloudBorgRepository     string `json:"cloud_borg_repository,omitempty" yaml:"cloud_borg_repository,omitempty"`
	CloudBorgPassphraseFile string `json:"cloud_borg_passphrase_file,omitempty" yaml:"cloud_borg_passphrase_file,omitempty"`
	CloudBorgCacheDir       string `json:"cloud_borg_cache_dir,omitempty" yaml:"cloud_borg_cache_dir,omitempty"`
	CloudBorgSecurityDir    string `json:"cloud_borg_security_dir,omitempty" yaml:"cloud_borg_security_dir,omitempty"`

	AuthorityProfile string         `json:"authority_profile,omitempty" yaml:"authority_profile,omitempty"`
	RuntimeProfile   string         `json:"runtime_profile,omitempty" yaml:"runtime_profile,omitempty"`
	ProviderMode     string         `json:"provider_mode,omitempty" yaml:"provider_mode,omitempty"`
	SafeRoots        []SafeRootSpec `json:"safe_roots,omitempty" yaml:"safe_roots,omitempty"`

	EnableLoomd     bool `json:"enable_loomd" yaml:"enable_loomd"`
	EnableNodeAgent bool `json:"enable_node_agent" yaml:"enable_node_agent"`
	EnableBox       bool `json:"enable_box" yaml:"enable_box"`
	// EnableDropzone is retained only to decode older setup manifests. New
	// plans ignore it and never seed a Dropzone worker.
	EnableDropzone              bool `json:"enable_dropzone" yaml:"enable_dropzone"`
	EnableWatchedRoots          bool `json:"enable_watched_roots" yaml:"enable_watched_roots"`
	EnableProviders             bool `json:"enable_providers" yaml:"enable_providers"`
	EnableStorageCatalog        bool `json:"enable_storage_catalog" yaml:"enable_storage_catalog"`
	EnableStorageExportWorker   bool `json:"enable_storage_export_worker" yaml:"enable_storage_export_worker"`
	EnableMainDocumentsWorker   bool `json:"enable_main_documents_worker" yaml:"enable_main_documents_worker"`
	EnableStorageRetention      bool `json:"enable_storage_retention" yaml:"enable_storage_retention"`
	EnableProjectArchiveRuntime bool `json:"enable_project_archive_runtime" yaml:"enable_project_archive_runtime"`

	AutoMigrate          bool   `json:"auto_migrate" yaml:"auto_migrate"`
	BootstrapMode        string `json:"bootstrap_mode,omitempty" yaml:"bootstrap_mode,omitempty"`
	ProductionBootstrap  bool   `json:"production_bootstrap" yaml:"production_bootstrap"`
	SkipEnroll           bool   `json:"skip_enroll" yaml:"skip_enroll"`
	RunEnrollment        bool   `json:"run_enrollment,omitempty" yaml:"run_enrollment,omitempty"`
	EnrollmentTTLSeconds int    `json:"enrollment_ttl_seconds,omitempty" yaml:"enrollment_ttl_seconds,omitempty"`
	ApproveEnrollment    bool   `json:"approve_enrollment,omitempty" yaml:"approve_enrollment,omitempty"`
	VerifyHeartbeat      bool   `json:"verify_heartbeat,omitempty" yaml:"verify_heartbeat,omitempty"`
}

type SafeRootSpec struct {
	Name string `json:"name" yaml:"name"`
	Path string `json:"path" yaml:"path"`
	Mode string `json:"mode" yaml:"mode"`
}

type TargetFacts struct {
	OS       string `json:"os" yaml:"os"`
	Arch     string `json:"arch" yaml:"arch"`
	Hostname string `json:"hostname" yaml:"hostname"`
	UserName string `json:"user_name" yaml:"user_name"`
	HomeDir  string `json:"home_dir" yaml:"home_dir"`

	HasSudo    bool `json:"has_sudo" yaml:"has_sudo"`
	HasSystemd bool `json:"has_systemd" yaml:"has_systemd"`
	HasLaunchd bool `json:"has_launchd" yaml:"has_launchd"`
	HasNix     bool `json:"has_nix" yaml:"has_nix"`
	HasGit     bool `json:"has_git" yaml:"has_git"`
	HasGo      bool `json:"has_go" yaml:"has_go"`

	ExistingLoom         BinaryFact `json:"existing_loom" yaml:"existing_loom"`
	ExistingLoomd        BinaryFact `json:"existing_loomd" yaml:"existing_loomd"`
	ExistingNodeAgent    BinaryFact `json:"existing_node_agent" yaml:"existing_node_agent"`
	ExistingManifestPath string     `json:"existing_manifest_path,omitempty" yaml:"existing_manifest_path,omitempty"`
}

type BinaryFact struct {
	Name  string `json:"name" yaml:"name"`
	Path  string `json:"path,omitempty" yaml:"path,omitempty"`
	Found bool   `json:"found" yaml:"found"`
}

type SetupPlan struct {
	SchemaVersion        string                    `json:"schema_version"`
	PlanID               string                    `json:"plan_id"`
	CreatedAt            time.Time                 `json:"created_at"`
	Spec                 SetupSpec                 `json:"spec"`
	Facts                TargetFacts               `json:"facts"`
	Profile              ProfilePlan               `json:"profile"`
	Paths                PathPlan                  `json:"paths"`
	Steps                []SetupStep               `json:"steps"`
	Diagnostics          []Diagnostic              `json:"diagnostics,omitempty"`
	BoxStateMigration    *BoxStateMigrationPlan    `json:"box_state_migration,omitempty"`
	RetiredIntakeCleanup *RetiredIntakeCleanupPlan `json:"retired_intake_cleanup,omitempty"`
	PlanHash             string                    `json:"plan_hash"`
}

type RetiredIntakeCleanupPlan struct {
	SchemaVersion string                      `json:"schema_version"`
	GeneratedAt   time.Time                   `json:"generated_at"`
	PlanDigest    string                      `json:"plan_digest"`
	Summary       RetiredIntakeCleanupSummary `json:"summary"`
	Items         []RetiredIntakeCleanupItem  `json:"items"`
}

type RetiredIntakeCleanupSummary struct {
	Total              int `json:"total"`
	Absent             int `json:"absent"`
	EligibleEmptyDirs  int `json:"eligible_empty_dirs"`
	EligibleKnownLinks int `json:"eligible_known_links"`
	Skipped            int `json:"skipped"`
}

type RetiredIntakeCleanupItem struct {
	ID                  string   `json:"id"`
	Category            string   `json:"category"`
	Root                string   `json:"root"`
	RelativePath        string   `json:"relative_path"`
	Path                string   `json:"path"`
	State               string   `json:"state"`
	ObservedType        string   `json:"observed_type,omitempty"`
	LinkTarget          string   `json:"link_target,omitempty"`
	ExpectedLinkTargets []string `json:"expected_link_targets,omitempty"`
	AnchorDigest        string   `json:"anchor_digest,omitempty"`
	ObjectDigest        string   `json:"object_digest,omitempty"`
	Eligible            bool     `json:"eligible"`
	Reason              string   `json:"reason"`
}

type RetiredIntakeCleanupApplyInput struct {
	Plan          SetupPlan
	ConfirmDigest string
	DryRun        bool
	Yes           bool
}

type RetiredIntakeCleanupChange struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type RetiredIntakeCleanupResult struct {
	PlanDigest string                       `json:"plan_digest"`
	DryRun     bool                         `json:"dry_run"`
	Status     string                       `json:"status"`
	Removed    []RetiredIntakeCleanupChange `json:"removed,omitempty"`
	Skipped    []RetiredIntakeCleanupChange `json:"skipped,omitempty"`
}

type BoxStateMigrationPlan struct {
	SchemaVersion  string                      `json:"schema_version"`
	SourceRoot     string                      `json:"source_root"`
	TargetRoot     string                      `json:"target_root"`
	State          string                      `json:"state"`
	DryRun         bool                        `json:"dry_run"`
	FileCount      int                         `json:"file_count"`
	DirectoryCount int                         `json:"directory_count"`
	SymlinkCount   int                         `json:"symlink_count"`
	TotalBytes     int64                       `json:"total_bytes"`
	HashedFiles    int                         `json:"hashed_files"`
	Entries        []BoxStateMigrationEntry    `json:"entries,omitempty"`
	Conflicts      []BoxStateMigrationConflict `json:"conflicts,omitempty"`
}

type BoxStateMigrationEntry struct {
	RelativePath string `json:"relative_path"`
	Type         string `json:"type"`
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256,omitempty"`
	LinkTarget   string `json:"link_target,omitempty"`
	Action       string `json:"action"`
}

type BoxStateMigrationConflict struct {
	RelativePath string `json:"relative_path"`
	SourceType   string `json:"source_type"`
	TargetType   string `json:"target_type"`
	Message      string `json:"message"`
}

type ProfilePlan struct {
	AuthorityProfileKey string `json:"authority_profile_key"`
	RuntimeProfileKey   string `json:"runtime_profile_key"`
}

type PathPlan struct {
	ManifestPath            string          `json:"manifest_path"`
	ConfigDir               string          `json:"config_dir"`
	DataDir                 string          `json:"data_dir"`
	StateDir                string          `json:"state_dir"`
	LogDir                  string          `json:"log_dir"`
	ServiceRoot             string          `json:"service_root,omitempty"`
	StorageRoot             string          `json:"storage_root,omitempty"`
	ImportsRoot             string          `json:"imports_root,omitempty"`
	UserBackupsRoot         string          `json:"user_backups_root,omitempty"`
	ArchiveRoot             string          `json:"archive_root,omitempty"`
	GeneratedRoot           string          `json:"generated_root,omitempty"`
	BoxStateRoot            string          `json:"box_state_root,omitempty"`
	LegacyBoxStateRoot      string          `json:"legacy_box_state_root,omitempty"`
	BoxPath                 string          `json:"box_path,omitempty"`
	BoxLoomDir              string          `json:"box_loom_dir,omitempty"`
	ObjectStorePath         string          `json:"object_store_path,omitempty"`
	MainDocumentsPath       string          `json:"main_documents_path,omitempty"`
	StorageExportRoot       string          `json:"storage_export_root,omitempty"`
	SocketPath              string          `json:"socket_path,omitempty"`
	CloudConfigPath         string          `json:"cloud_config_path,omitempty"`
	CloudConfigDir          string          `json:"cloud_config_dir,omitempty"`
	CloudStateDir           string          `json:"cloud_state_dir,omitempty"`
	CloudRcloneConfigPath   string          `json:"cloud_rclone_config_path,omitempty"`
	CloudBorgCacheDir       string          `json:"cloud_borg_cache_dir,omitempty"`
	CloudBorgSecurityDir    string          `json:"cloud_borg_security_dir,omitempty"`
	CloudBorgPassphraseFile string          `json:"cloud_borg_passphrase_file,omitempty"`
	NodeAgentConfigPath     string          `json:"node_agent_config_path,omitempty"`
	NodeAgentStatePath      string          `json:"node_agent_state_path,omitempty"`
	NodeAgentDataDir        string          `json:"node_agent_data_dir,omitempty"`
	LaunchAgentPlistPath    string          `json:"launch_agent_plist_path,omitempty"`
	ServiceReadWritePaths   []string        `json:"service_read_write_paths,omitempty"`
	ManagedTmpfilesPaths    []string        `json:"managed_tmpfiles_paths,omitempty"`
	HumanLinks              []HumanLinkPlan `json:"human_links,omitempty"`
}

type HumanLinkPlan struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Intent     string `json:"intent"`
	Label      string `json:"label"`
	LinkPath   string `json:"link_path"`
	TargetPath string `json:"target_path,omitempty"`
	Required   bool   `json:"required"`
}

type SetupStep struct {
	ID          string         `json:"id"`
	Category    string         `json:"category"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Required    bool           `json:"required"`
	Mutating    bool           `json:"mutating"`
	Privileged  bool           `json:"privileged"`
	Status      string         `json:"status"`
	Check       string         `json:"check,omitempty"`
	Action      string         `json:"action,omitempty"`
	RepairHint  string         `json:"repair_hint,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Field      string `json:"field,omitempty"`
	Path       string `json:"path,omitempty"`
	RepairHint string `json:"repair_hint,omitempty"`
}

type InstallManifest struct {
	SchemaVersion string    `json:"schema_version" yaml:"schema_version"`
	InstallID     string    `json:"install_id" yaml:"install_id"`
	InstalledAt   time.Time `json:"installed_at" yaml:"installed_at"`
	UpdatedAt     time.Time `json:"updated_at" yaml:"updated_at"`

	SetupVersion  string `json:"setup_version" yaml:"setup_version"`
	SourcePath    string `json:"source_path,omitempty" yaml:"source_path,omitempty"`
	SourceCommit  string `json:"source_commit,omitempty" yaml:"source_commit,omitempty"`
	MigrationsDir string `json:"migrations_dir,omitempty" yaml:"migrations_dir,omitempty"`
	PlanHash      string `json:"plan_hash,omitempty" yaml:"plan_hash,omitempty"`

	NodeKey      string `json:"node_key" yaml:"node_key"`
	NodeID       string `json:"node_id,omitempty" yaml:"node_id,omitempty"`
	DisplayName  string `json:"display_name" yaml:"display_name"`
	NodeKind     string `json:"node_kind" yaml:"node_kind"`
	NodeRole     string `json:"node_role" yaml:"node_role"`
	RuntimeClass string `json:"runtime_class" yaml:"runtime_class"`

	MainURL string `json:"main_url,omitempty" yaml:"main_url,omitempty"`

	AuthorityProfile string `json:"authority_profile" yaml:"authority_profile"`
	RuntimeProfile   string `json:"runtime_profile" yaml:"runtime_profile"`

	InstallMode    string `json:"install_mode" yaml:"install_mode"`
	ServiceManager string `json:"service_manager" yaml:"service_manager"`
	PackageMode    string `json:"package_mode" yaml:"package_mode"`

	UserName string `json:"user_name,omitempty" yaml:"user_name,omitempty"`
	HomeDir  string `json:"home_dir,omitempty" yaml:"home_dir,omitempty"`

	ConfigDir string `json:"config_dir,omitempty" yaml:"config_dir,omitempty"`
	DataDir   string `json:"data_dir,omitempty" yaml:"data_dir,omitempty"`
	StateDir  string `json:"state_dir,omitempty" yaml:"state_dir,omitempty"`
	LogDir    string `json:"log_dir,omitempty" yaml:"log_dir,omitempty"`

	ServiceRoot     string `json:"service_root,omitempty" yaml:"service_root,omitempty"`
	StorageRoot     string `json:"storage_root,omitempty" yaml:"storage_root,omitempty"`
	ImportsRoot     string `json:"imports_root,omitempty" yaml:"imports_root,omitempty"`
	UserBackupsRoot string `json:"user_backups_root,omitempty" yaml:"user_backups_root,omitempty"`
	ArchiveRoot     string `json:"archive_root,omitempty" yaml:"archive_root,omitempty"`
	GeneratedRoot   string `json:"generated_root,omitempty" yaml:"generated_root,omitempty"`
	BoxStateRoot    string `json:"box_state_root,omitempty" yaml:"box_state_root,omitempty"`

	BoxPath    string `json:"box_path,omitempty" yaml:"box_path,omitempty"`
	BoxProfile string `json:"box_profile,omitempty" yaml:"box_profile,omitempty"`

	NodeAgentConfigPath string `json:"node_agent_config_path,omitempty" yaml:"node_agent_config_path,omitempty"`
	NodeAgentStatePath  string `json:"node_agent_state_path,omitempty" yaml:"node_agent_state_path,omitempty"`
	NodeAgentDataDir    string `json:"node_agent_data_dir,omitempty" yaml:"node_agent_data_dir,omitempty"`

	ObjectStorePath         string         `json:"object_store_path,omitempty" yaml:"object_store_path,omitempty"`
	MainDocumentsPath       string         `json:"main_documents_path,omitempty" yaml:"main_documents_path,omitempty"`
	StorageExportRoot       string         `json:"storage_export_root,omitempty" yaml:"storage_export_root,omitempty"`
	SocketPath              string         `json:"socket_path,omitempty" yaml:"socket_path,omitempty"`
	HTTPListenAddr          string         `json:"http_listen_addr,omitempty" yaml:"http_listen_addr,omitempty"`
	EnableCloud             bool           `json:"enable_cloud" yaml:"enable_cloud"`
	CloudConfigPath         string         `json:"cloud_config_path,omitempty" yaml:"cloud_config_path,omitempty"`
	CloudStateDir           string         `json:"cloud_state_dir,omitempty" yaml:"cloud_state_dir,omitempty"`
	CloudRcloneConfigPath   string         `json:"cloud_rclone_config_path,omitempty" yaml:"cloud_rclone_config_path,omitempty"`
	CloudRemoteName         string         `json:"cloud_remote_name,omitempty" yaml:"cloud_remote_name,omitempty"`
	CloudRemoteRoot         string         `json:"cloud_remote_root,omitempty" yaml:"cloud_remote_root,omitempty"`
	CloudSnapshotBackend    string         `json:"cloud_snapshot_backend,omitempty" yaml:"cloud_snapshot_backend,omitempty"`
	CloudBorgRepository     string         `json:"cloud_borg_repository,omitempty" yaml:"cloud_borg_repository,omitempty"`
	CloudBorgPassphraseFile string         `json:"cloud_borg_passphrase_file,omitempty" yaml:"cloud_borg_passphrase_file,omitempty"`
	CloudBorgCacheDir       string         `json:"cloud_borg_cache_dir,omitempty" yaml:"cloud_borg_cache_dir,omitempty"`
	CloudBorgSecurityDir    string         `json:"cloud_borg_security_dir,omitempty" yaml:"cloud_borg_security_dir,omitempty"`
	BootstrapMode           string         `json:"bootstrap_mode,omitempty" yaml:"bootstrap_mode,omitempty"`
	ProviderMode            string         `json:"provider_mode,omitempty" yaml:"provider_mode,omitempty"`
	SafeRoots               []SafeRootSpec `json:"safe_roots,omitempty" yaml:"safe_roots,omitempty"`

	ProductionBootstrap ProductionBootstrapManifest `json:"production_bootstrap,omitempty" yaml:"production_bootstrap,omitempty"`
	Binaries            []InstalledBinary           `json:"binaries,omitempty" yaml:"binaries,omitempty"`
	Services            []InstalledService          `json:"services,omitempty" yaml:"services,omitempty"`
	Enrollment          EnrollmentManifest          `json:"enrollment,omitempty" yaml:"enrollment,omitempty"`
	Credential          CredentialManifest          `json:"credential,omitempty" yaml:"credential,omitempty"`
	LastStatus          SetupStatusSummary          `json:"last_status,omitempty" yaml:"last_status,omitempty"`
	Metadata            map[string]any              `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Extra               json.RawMessage             `json:"-" yaml:"-"`
}

type ProductionBootstrapManifest struct {
	Checked    bool     `json:"checked" yaml:"checked"`
	Ready      bool     `json:"ready" yaml:"ready"`
	EventID    string   `json:"event_id,omitempty" yaml:"event_id,omitempty"`
	EventCount int      `json:"event_count,omitempty" yaml:"event_count,omitempty"`
	Warnings   []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

type InstalledBinary struct {
	Name    string `json:"name" yaml:"name"`
	Path    string `json:"path" yaml:"path"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
}

type InstalledService struct {
	Name    string `json:"name" yaml:"name"`
	Manager string `json:"manager" yaml:"manager"`
	Label   string `json:"label,omitempty" yaml:"label,omitempty"`
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
	Status  string `json:"status,omitempty" yaml:"status,omitempty"`
}

type EnrollmentManifest struct {
	Status              string     `json:"status,omitempty" yaml:"status,omitempty"`
	EnrollmentRequestID string     `json:"enrollment_request_id,omitempty" yaml:"enrollment_request_id,omitempty"`
	ApprovedAt          *time.Time `json:"approved_at,omitempty" yaml:"approved_at,omitempty"`
	FailureCode         string     `json:"failure_code,omitempty" yaml:"failure_code,omitempty"`
	FailureMessage      string     `json:"failure_message,omitempty" yaml:"failure_message,omitempty"`
	PresenceState       string     `json:"presence_state,omitempty" yaml:"presence_state,omitempty"`
	VerifiedOnMain      bool       `json:"verified_on_main,omitempty" yaml:"verified_on_main,omitempty"`
}

type CredentialManifest struct {
	Configured       bool   `json:"configured" yaml:"configured"`
	NodeCredentialID string `json:"node_credential_id,omitempty" yaml:"node_credential_id,omitempty"`
	CredentialHint   string `json:"credential_hint,omitempty" yaml:"credential_hint,omitempty"`
}

type SetupStatusSummary struct {
	Status          string     `json:"status,omitempty" yaml:"status,omitempty"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty" yaml:"last_checked_at,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty" yaml:"last_heartbeat_at,omitempty"`
}

type ManifestPathResult struct {
	Path        string `json:"path"`
	Source      string `json:"source"`
	InstallMode string `json:"install_mode"`
}

type ManifestInspection struct {
	Path     string           `json:"path"`
	Exists   bool             `json:"exists"`
	Manifest *InstallManifest `json:"manifest,omitempty"`
	Error    string           `json:"error,omitempty"`
}

type StatusInput struct {
	ManifestPath  string
	Spec          SetupSpec
	Facts         TargetFacts
	Now           func() time.Time
	CollectFacts  bool
	LaunchdRunner LaunchdRunner
}

type SetupStatus struct {
	CheckedAt        time.Time            `json:"checked_at"`
	Manifest         ManifestStatus       `json:"manifest"`
	Node             NodeSetupStatus      `json:"node"`
	Profiles         ProfileStatus        `json:"profiles"`
	Paths            []SetupPathStatus    `json:"paths"`
	Binaries         []BinaryStatus       `json:"binaries"`
	Services         []ServiceStatus      `json:"services,omitempty"`
	Box              BoxSetupStatus       `json:"box"`
	NodeAgent        NodeAgentSetupStatus `json:"node_agent"`
	HumanLinks       []HumanLinkStatus    `json:"human_links,omitempty"`
	Enrollment       EnrollmentStatus     `json:"enrollment"`
	MainConnectivity ConnectivityStatus   `json:"main_connectivity"`
	Summary          SetupStatusSummary   `json:"summary"`
	Diagnostics      []Diagnostic         `json:"diagnostics,omitempty"`
	Plan             *SetupPlan           `json:"plan,omitempty"`
}

type ManifestStatus struct {
	Path          string           `json:"path"`
	Exists        bool             `json:"exists"`
	State         string           `json:"state"`
	SchemaVersion string           `json:"schema_version,omitempty"`
	Error         string           `json:"error,omitempty"`
	Manifest      *InstallManifest `json:"manifest,omitempty"`
}

type NodeSetupStatus struct {
	NodeKey      string `json:"node_key,omitempty"`
	NodeID       string `json:"node_id,omitempty"`
	DisplayName  string `json:"display_name,omitempty"`
	NodeKind     string `json:"node_kind,omitempty"`
	NodeRole     string `json:"node_role,omitempty"`
	RuntimeClass string `json:"runtime_class,omitempty"`
}

type ProfileStatus struct {
	AuthorityProfile string `json:"authority_profile,omitempty"`
	RuntimeProfile   string `json:"runtime_profile,omitempty"`
	Valid            bool   `json:"valid"`
	Error            string `json:"error,omitempty"`
}

type SetupPathStatus struct {
	Key         string   `json:"key"`
	Path        string   `json:"path"`
	Exists      bool     `json:"exists"`
	IsDir       bool     `json:"is_dir"`
	Readable    bool     `json:"readable,omitempty"`
	Writable    bool     `json:"writable,omitempty"`
	Executable  bool     `json:"executable,omitempty"`
	AccessError string   `json:"access_error,omitempty"`
	Required    bool     `json:"required"`
	Status      string   `json:"status"`
	MissingMode []string `json:"missing_modes,omitempty"`
}

type BinaryStatus struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Status   string `json:"status"`
}

type ServiceStatus struct {
	Name     string `json:"name"`
	Manager  string `json:"manager"`
	Label    string `json:"label,omitempty"`
	Path     string `json:"path,omitempty"`
	Expected bool   `json:"expected"`
	Exists   bool   `json:"exists,omitempty"`
	Loaded   bool   `json:"loaded,omitempty"`
	PID      int    `json:"pid,omitempty"`
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
}

type BoxSetupStatus struct {
	Enabled           bool   `json:"enabled"`
	Profile           string `json:"profile,omitempty"`
	Path              string `json:"path,omitempty"`
	Exists            bool   `json:"exists"`
	LoomDir           string `json:"loom_dir,omitempty"`
	LoomDirExists     bool   `json:"loom_dir_exists"`
	Status            string `json:"status"`
	RuntimeStateRoot  string `json:"runtime_state_root,omitempty"`
	LegacyStateRoot   string `json:"legacy_state_root,omitempty"`
	LegacyStateExists bool   `json:"legacy_state_exists"`
	MigrationState    string `json:"migration_state,omitempty"`
}

type NodeAgentSetupStatus struct {
	Enabled              bool   `json:"enabled"`
	ConfigPath           string `json:"config_path,omitempty"`
	ConfigExists         bool   `json:"config_exists"`
	StatePath            string `json:"state_path,omitempty"`
	StateExists          bool   `json:"state_exists"`
	DataDir              string `json:"data_dir,omitempty"`
	DataDirExists        bool   `json:"data_dir_exists"`
	CredentialConfigured bool   `json:"credential_configured"`
	Status               string `json:"status"`
}

type HumanLinkStatus struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	Intent       string `json:"intent"`
	Label        string `json:"label"`
	LinkPath     string `json:"link_path"`
	TargetPath   string `json:"target_path,omitempty"`
	Exists       bool   `json:"exists"`
	IsSymlink    bool   `json:"is_symlink"`
	TargetExists bool   `json:"target_exists,omitempty"`
	Required     bool   `json:"required"`
	Status       string `json:"status"`
	Message      string `json:"message,omitempty"`
}

type EnrollmentStatus struct {
	Status               string `json:"status"`
	EnrollmentRequestID  string `json:"enrollment_request_id,omitempty"`
	CredentialConfigured bool   `json:"credential_configured"`
	NodeCredentialID     string `json:"node_credential_id,omitempty"`
	CredentialHint       string `json:"credential_hint,omitempty"`
	FailureCode          string `json:"failure_code,omitempty"`
	FailureMessage       string `json:"failure_message,omitempty"`
	PresenceState        string `json:"presence_state,omitempty"`
	VerifiedOnMain       bool   `json:"verified_on_main,omitempty"`
}

type ConnectivityStatus struct {
	MainURL   string `json:"main_url,omitempty"`
	Required  bool   `json:"required"`
	Reachable *bool  `json:"reachable,omitempty"`
	Status    string `json:"status"`
}

type DoctorInput struct {
	StatusInput
	Strict bool
}

type DoctorReport struct {
	CheckedAt time.Time       `json:"checked_at"`
	Summary   string          `json:"summary"`
	Findings  []DoctorFinding `json:"findings"`
	Repairs   []RepairAction  `json:"repairs,omitempty"`
	Status    SetupStatus     `json:"status"`
}

type DoctorFinding struct {
	Code       string         `json:"code"`
	Severity   string         `json:"severity"`
	Message    string         `json:"message"`
	Evidence   map[string]any `json:"evidence,omitempty"`
	RepairID   string         `json:"repair_id,omitempty"`
	RepairHint string         `json:"repair_hint,omitempty"`
}

type RepairAction struct {
	ID          string      `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description,omitempty"`
	Safe        bool        `json:"safe"`
	Destructive bool        `json:"destructive"`
	RequiresYes bool        `json:"requires_yes"`
	FindingCode string      `json:"finding_code,omitempty"`
	Steps       []SetupStep `json:"steps,omitempty"`
}

type RepairInput struct {
	StatusInput
	FixIDs        []string
	DryRun        bool
	Yes           bool
	NoInteractive bool
	LaunchdRunner LaunchdRunner
}

type RepairResult struct {
	DryRun    bool           `json:"dry_run"`
	Requested []string       `json:"requested"`
	Changed   []RepairChange `json:"changed,omitempty"`
	Skipped   []RepairChange `json:"skipped,omitempty"`
	Refused   bool           `json:"refused"`
	Refusal   string         `json:"refusal,omitempty"`
	Actions   []RepairAction `json:"actions,omitempty"`
	Doctor    DoctorReport   `json:"doctor"`
}

type RepairChange struct {
	RepairID string `json:"repair_id"`
	Status   string `json:"status"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message,omitempty"`
}

type ApplyInput struct {
	Plan          *SetupPlan
	PlanPath      string
	Spec          SetupSpec
	Facts         TargetFacts
	ManifestPath  string
	DryRun        bool
	Yes           bool
	Resume        bool
	NoInteractive bool
	CollectFacts  bool
	Now           func() time.Time

	EnrollmentMainRunner   enrollmentflow.MainRunner
	EnrollmentTargetRunner enrollmentflow.TargetRunner
	LaunchdRunner          LaunchdRunner
}

type ApplyResult struct {
	DryRun     bool                   `json:"dry_run"`
	PlanID     string                 `json:"plan_id"`
	PlanHash   string                 `json:"plan_hash"`
	Refused    bool                   `json:"refused"`
	Refusal    string                 `json:"refusal,omitempty"`
	Changed    []ApplyChange          `json:"changed,omitempty"`
	Skipped    []ApplyChange          `json:"skipped,omitempty"`
	Blocked    []ApplyChange          `json:"blocked,omitempty"`
	Manifest   InstallManifest        `json:"manifest,omitempty"`
	Status     SetupStatus            `json:"status,omitempty"`
	Doctor     DoctorReport           `json:"doctor,omitempty"`
	ServiceEnv string                 `json:"service_env,omitempty"`
	Migrations ApplyChange            `json:"migrations,omitempty"`
	Bootstrap  ApplyChange            `json:"bootstrap,omitempty"`
	Enrollment *enrollmentflow.Result `json:"enrollment,omitempty"`
	Resume     bool                   `json:"resume,omitempty"`
}

type ApplyChange struct {
	ID       string         `json:"id"`
	Category string         `json:"category,omitempty"`
	Status   string         `json:"status"`
	Path     string         `json:"path,omitempty"`
	Message  string         `json:"message,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}
