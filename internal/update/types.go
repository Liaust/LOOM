package update

import (
	"context"
	"time"

	"loom.local/loom/internal/maintenance"
)

const (
	SchemaVersion                   = "loom.update.v0.5.1"
	ReleaseManifestSchemaVersion    = "loom.release.v0.5.1"
	UpdateManifestSchemaVersion     = "loom.update.manifest.v0.5.1"
	ReleaseBackupScopeSchemaVersion = "loom.release.backup_scope.v1"

	ReleaseBackupScopeOperationalOnly   = "operational_only"
	ReleaseBackupScopeCanonicalUserData = "canonical_user_data"
	UpdateBackupRequirementOperational  = "operational_package"
	UpdateBackupRequirementComplete     = "complete_backup"
	BackupScopeDeclarationValid         = "valid"
	BackupScopeDeclarationAbsent        = "absent"
	BackupScopeDeclarationMalformed     = "malformed"
	BackupScopeDeclarationUnknownSchema = "unknown_schema"
	BackupScopeDeclarationUnknownClass  = "unknown_class"

	PlanStatusReady    = "ready"
	PlanStatusBlocked  = "blocked"
	PlanStatusDegraded = "degraded"

	StepStatusPending = "pending"
	StepStatusBlocked = "blocked"
	StepStatusReady   = "ready"

	DiagnosticInfo     = "info"
	DiagnosticWarning  = "warning"
	DiagnosticError    = "error"
	DiagnosticBlocking = "blocking"

	RollbackClassServiceOnly            = "service_only_possible"
	RollbackClassRestoreRequired        = "restore_required_if_migrations_apply"
	RollbackClassManualOnly             = "manual_only"
	DefaultProductionFlakeOutput        = ".#loom-main"
	DefaultReleaseManifestFileYAML      = "loom-release.yaml"
	DefaultReleaseManifestFileJSON      = "loom-release.json"
	DefaultActiveUpdateManifestFileYAML = "active.yaml"

	UpdateStatusPlanned                 = "planned"
	UpdateStatusRunning                 = "running"
	UpdateStatusSucceeded               = "succeeded"
	UpdateStatusFailed                  = "failed"
	UpdateStatusDryRun                  = "dry_run"
	UpdateStatusRolledBack              = "rolled_back"
	UpdateStatusDatabaseRestoreRequired = "database_restore_required"

	MaintenanceWindowSchemaVersion = "loom.update.maintenance_window.v0.5.1"

	MaintenanceWindowStatusPlanned        = "planned"
	MaintenanceWindowStatusSkipped        = "skipped"
	MaintenanceWindowStatusPausing        = "pausing"
	MaintenanceWindowStatusPaused         = "paused"
	MaintenanceWindowStatusResuming       = "resuming"
	MaintenanceWindowStatusResumed        = "resumed"
	MaintenanceWindowStatusResumeRequired = "resume_required"
	MaintenanceWindowStatusFailed         = "failed"

	MaintenanceItemKindSchedule            = "schedule"
	MaintenanceItemKindDirectEventEndpoint = "direct_event_endpoint"
	MaintenanceItemKindWorker              = "worker"

	MaintenanceItemStatusPaused         = "paused"
	MaintenanceItemStatusResumed        = "resumed"
	MaintenanceItemStatusSkipped        = "skipped"
	MaintenanceItemStatusFailed         = "failed"
	MaintenanceItemStatusUnsupported    = "unsupported"
	MaintenanceItemStatusAlreadyPaused  = "already_paused"
	MaintenanceItemStatusAlreadyActive  = "already_active"
	MaintenanceItemStatusResumeRequired = "resume_required"
)

type UpdateSpec struct {
	ReleasePath             string `json:"release_path" yaml:"release_path"`
	StateDir                string `json:"state_dir,omitempty" yaml:"state_dir,omitempty"`
	ManifestPath            string `json:"manifest_path,omitempty" yaml:"manifest_path,omitempty"`
	ActivePath              string `json:"active_path,omitempty" yaml:"active_path,omitempty"`
	ActiveMigrationsDir     string `json:"active_migrations_dir,omitempty" yaml:"active_migrations_dir,omitempty"`
	TargetMigrationsDir     string `json:"target_migrations_dir,omitempty" yaml:"target_migrations_dir,omitempty"`
	FlakeOutput             string `json:"flake_output,omitempty" yaml:"flake_output,omitempty"`
	DBURL                   string `json:"-" yaml:"-"`
	CurrentMigrationVersion *int64 `json:"current_migration_version,omitempty" yaml:"current_migration_version,omitempty"`
}

type UpdatePlan struct {
	SchemaVersion string             `json:"schema_version"`
	PlanID        string             `json:"plan_id"`
	PlanHash      string             `json:"plan_hash"`
	Status        string             `json:"status"`
	CreatedAt     time.Time          `json:"created_at"`
	Spec          UpdateSpec         `json:"spec"`
	Active        ReleaseState       `json:"active"`
	Target        ReleaseState       `json:"target"`
	Migrations    MigrationPlan      `json:"migrations"`
	Nix           NixPlan            `json:"nix"`
	Backup        BackupRequirement  `json:"backup"`
	BackupScope   UpdateBackupScope  `json:"backup_scope"`
	ServiceImpact ServiceImpact      `json:"service_impact"`
	Rollback      RollbackPlan       `json:"rollback"`
	Steps         []UpdateStep       `json:"steps"`
	Diagnostics   []UpdateDiagnostic `json:"diagnostics,omitempty"`
}

type ReleaseState struct {
	ReleaseID     string              `json:"release_id,omitempty" yaml:"release_id,omitempty"`
	Path          string              `json:"path,omitempty" yaml:"path,omitempty"`
	Version       string              `json:"version,omitempty" yaml:"version,omitempty"`
	Commit        string              `json:"commit,omitempty" yaml:"commit,omitempty"`
	FlakeOutput   string              `json:"flake_output,omitempty" yaml:"flake_output,omitempty"`
	ManifestPath  string              `json:"manifest_path,omitempty" yaml:"manifest_path,omitempty"`
	MigrationsDir string              `json:"migrations_dir,omitempty" yaml:"migrations_dir,omitempty"`
	Source        string              `json:"source,omitempty" yaml:"source,omitempty"`
	BackupScope   *ReleaseBackupScope `json:"backup_scope,omitempty" yaml:"backup_scope,omitempty"`
}

type MigrationPlan struct {
	Status              string `json:"status"`
	CurrentVersion      int64  `json:"current_version"`
	ActiveLatestVersion int64  `json:"active_latest_version"`
	TargetLatestVersion int64  `json:"target_latest_version"`
	Pending             int64  `json:"pending"`
	WillApply           bool   `json:"will_apply"`
	Error               string `json:"error,omitempty"`
}

type NixPlan struct {
	CurrentGeneration string `json:"current_generation,omitempty"`
	TargetFlakeOutput string `json:"target_flake_output"`
	RebuildExpected   bool   `json:"rebuild_expected"`
}

type BackupRequirement struct {
	Required       bool   `json:"required"`
	Reason         string `json:"reason"`
	MinimumCommand string `json:"minimum_command"`
}

// ReleaseBackupScope is a trusted, versioned declaration carried by the
// release manifest. It describes the update's possible canonical user-data
// effects independently of generic Nix/rebuild step names.
type ReleaseBackupScope struct {
	SchemaVersion string `json:"schema_version" yaml:"schema_version"`
	Class         string `json:"class" yaml:"class"`
}

// UpdateBackupScope is the fail-closed normalized declaration embedded in the
// hashed update plan. Only a valid operational_only declaration permits a
// bounded operational package; every other state requires a complete backup.
type UpdateBackupScope struct {
	DeclarationStatus         string `json:"declaration_status"`
	DeclaredSchema            string `json:"declared_schema,omitempty"`
	DeclaredClass             string `json:"declared_class,omitempty"`
	RequiredBackup            string `json:"required_backup"`
	OperationalPackageAllowed bool   `json:"operational_package_allowed"`
	Reason                    string `json:"reason"`
}

type ServiceImpact struct {
	LoomdRestartExpected           bool `json:"loomd_restart_expected"`
	PostgresRemainUpExpected       bool `json:"postgres_remain_up_expected"`
	SchedulerPauseRecommended      bool `json:"scheduler_pause_recommended"`
	DirectEventPauseRecommended    bool `json:"direct_event_pause_recommended"`
	OptionalWorkerPauseRecommended bool `json:"optional_worker_pause_recommended"`
}

type RollbackPlan struct {
	Class               string `json:"class"`
	Reason              string `json:"reason"`
	ServiceOnlyPossible bool   `json:"service_only_possible"`
	RestoreRequired     bool   `json:"restore_required"`
}

type UpdateStep struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status"`
	Mutating    bool   `json:"mutating"`
}

type UpdateDiagnostic struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Path     string `json:"path,omitempty"`
}

type ReleaseManifest struct {
	SchemaVersion string              `json:"schema_version" yaml:"schema_version"`
	ReleaseID     string              `json:"release_id,omitempty" yaml:"release_id,omitempty"`
	Version       string              `json:"version,omitempty" yaml:"version,omitempty"`
	Commit        string              `json:"commit,omitempty" yaml:"commit,omitempty"`
	SourcePath    string              `json:"source_path,omitempty" yaml:"source_path,omitempty"`
	MigrationsDir string              `json:"migrations_dir,omitempty" yaml:"migrations_dir,omitempty"`
	FlakeOutput   string              `json:"flake_output,omitempty" yaml:"flake_output,omitempty"`
	CreatedAt     time.Time           `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	Metadata      map[string]any      `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	BackupScope   *ReleaseBackupScope `json:"backup_scope,omitempty" yaml:"backup_scope,omitempty"`
}

type UpdateManifest struct {
	SchemaVersion string             `json:"schema_version" yaml:"schema_version"`
	UpdateID      string             `json:"update_id" yaml:"update_id"`
	Status        string             `json:"status" yaml:"status"`
	PlanHash      string             `json:"plan_hash,omitempty" yaml:"plan_hash,omitempty"`
	StartedAt     time.Time          `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	FinishedAt    *time.Time         `json:"finished_at,omitempty" yaml:"finished_at,omitempty"`
	Active        ReleaseState       `json:"active" yaml:"active"`
	Target        ReleaseState       `json:"target" yaml:"target"`
	Migrations    MigrationPlan      `json:"migrations" yaml:"migrations"`
	BackupRef     string             `json:"backup_ref,omitempty" yaml:"backup_ref,omitempty"`
	BackupPath    string             `json:"backup_path,omitempty" yaml:"backup_path,omitempty"`
	Rollback      RollbackPlan       `json:"rollback" yaml:"rollback"`
	Diagnostics   []UpdateDiagnostic `json:"diagnostics,omitempty" yaml:"diagnostics,omitempty"`
	Metadata      map[string]any     `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Maintenance   *MaintenanceWindow `json:"maintenance_window,omitempty" yaml:"maintenance_window,omitempty"`
}

type RollbackPlanSummary struct {
	UpdateID   string       `json:"update_id"`
	Target     ReleaseState `json:"target"`
	Previous   ReleaseState `json:"previous"`
	Rollback   RollbackPlan `json:"rollback"`
	BackupRef  string       `json:"backup_ref,omitempty"`
	BackupPath string       `json:"backup_path,omitempty"`
}

type UpdateStatus struct {
	CheckedAt          time.Time          `json:"checked_at"`
	StateDir           string             `json:"state_dir"`
	ActiveManifestPath string             `json:"active_manifest_path"`
	ActiveExists       bool               `json:"active_exists"`
	Active             *UpdateManifest    `json:"active,omitempty"`
	History            []UpdateManifest   `json:"history"`
	Diagnostics        []UpdateDiagnostic `json:"diagnostics,omitempty"`
}

type RuntimeIdentity struct {
	Environment string `json:"environment"`
	NodeID      string `json:"node_id"`
	NodeRole    string `json:"node_role"`
}

type BackupVerificationInput struct {
	BackupPath string
	BackupRef  string
}

type BackupVerifier func(context.Context, BackupVerificationInput) (maintenance.BackupVerification, error)

type ApplyInput struct {
	Spec               UpdateSpec
	Runtime            RuntimeIdentity
	Yes                bool
	DryRun             bool
	AllowNonProduction bool
	SkipBackup         bool
	SkipRebuild        bool
	SkipHealthCheck    bool
	BackupPath         string
	BackupRef          string
	BackupVerifier     BackupVerifier
	MaintenancePolicy  MaintenancePausePolicy
	Maintenance        MaintenanceCoordinator
	Now                func() time.Time
	Runner             CommandRunner
}

type ApplyResult struct {
	Status       string         `json:"status"`
	UpdateID     string         `json:"update_id"`
	ManifestPath string         `json:"manifest_path,omitempty"`
	HistoryPath  string         `json:"history_path,omitempty"`
	PendingPath  string         `json:"pending_path,omitempty"`
	Plan         UpdatePlan     `json:"plan"`
	Manifest     UpdateManifest `json:"manifest"`
	Changed      []UpdateChange `json:"changed"`
	Refused      bool           `json:"refused"`
	Refusal      string         `json:"refusal,omitempty"`
}

type RollbackInput struct {
	StateDir           string
	ManifestPath       string
	ToReleasePath      string
	Yes                bool
	DryRun             bool
	AllowNonProduction bool
	ServiceOnly        bool
	RestoreRequired    bool
	SkipRebuild        bool
	SkipHealthCheck    bool
	Runtime            RuntimeIdentity
	Now                func() time.Time
	Runner             CommandRunner
}

type RollbackResult struct {
	Status       string          `json:"status"`
	UpdateID     string          `json:"update_id,omitempty"`
	ManifestPath string          `json:"manifest_path,omitempty"`
	HistoryPath  string          `json:"history_path,omitempty"`
	TargetPath   string          `json:"target_path,omitempty"`
	Runbook      []string        `json:"runbook,omitempty"`
	Manifest     *UpdateManifest `json:"manifest,omitempty"`
	Changed      []UpdateChange  `json:"changed"`
	Refused      bool            `json:"refused"`
	Refusal      string          `json:"refusal,omitempty"`
}

type UpdateChange struct {
	Step    string `json:"step"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Path    string `json:"path,omitempty"`
}

type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type MaintenanceCoordinator interface {
	Open(ctx context.Context, input MaintenanceOpenInput) (MaintenanceWindow, error)
	Resume(ctx context.Context, input MaintenanceResumeInput) (MaintenanceWindow, error)
}

type MaintenancePausePolicy struct {
	PauseSchedules            bool `json:"pause_schedules" yaml:"pause_schedules"`
	PauseDirectEventEndpoints bool `json:"pause_direct_event_endpoints" yaml:"pause_direct_event_endpoints"`
	PauseOptionalWorkers      bool `json:"pause_optional_workers" yaml:"pause_optional_workers"`
}

type MaintenanceWindow struct {
	SchemaVersion string                  `json:"schema_version" yaml:"schema_version"`
	WindowID      string                  `json:"window_id" yaml:"window_id"`
	UpdateID      string                  `json:"update_id" yaml:"update_id"`
	Status        string                  `json:"status" yaml:"status"`
	StartedAt     time.Time               `json:"started_at,omitempty" yaml:"started_at,omitempty"`
	FinishedAt    *time.Time              `json:"finished_at,omitempty" yaml:"finished_at,omitempty"`
	PausePolicy   MaintenancePausePolicy  `json:"pause_policy" yaml:"pause_policy"`
	Schedules     []MaintenancePausedItem `json:"schedules,omitempty" yaml:"schedules,omitempty"`
	DirectEvents  []MaintenancePausedItem `json:"direct_events,omitempty" yaml:"direct_events,omitempty"`
	Workers       []MaintenancePausedItem `json:"workers,omitempty" yaml:"workers,omitempty"`
	Diagnostics   []UpdateDiagnostic      `json:"diagnostics,omitempty" yaml:"diagnostics,omitempty"`
}

type MaintenancePausedItem struct {
	Kind           string     `json:"kind" yaml:"kind"`
	Ref            string     `json:"ref" yaml:"ref"`
	DisplayName    string     `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	PreviousStatus string     `json:"previous_status,omitempty" yaml:"previous_status,omitempty"`
	PausedStatus   string     `json:"paused_status,omitempty" yaml:"paused_status,omitempty"`
	PauseStatus    string     `json:"pause_status" yaml:"pause_status"`
	ResumeStatus   string     `json:"resume_status,omitempty" yaml:"resume_status,omitempty"`
	PausedAt       *time.Time `json:"paused_at,omitempty" yaml:"paused_at,omitempty"`
	ResumedAt      *time.Time `json:"resumed_at,omitempty" yaml:"resumed_at,omitempty"`
	Error          string     `json:"error,omitempty" yaml:"error,omitempty"`
}

type MaintenanceOpenInput struct {
	UpdateID string
	Policy   MaintenancePausePolicy
	Now      func() time.Time
}

type MaintenanceResumeInput struct {
	UpdateID string
	Window   MaintenanceWindow
	Now      func() time.Time
}

type ResumeMaintenanceInput struct {
	StateDir           string
	ManifestPath       string
	Yes                bool
	AllowNonProduction bool
	Runtime            RuntimeIdentity
	Maintenance        MaintenanceCoordinator
	Now                func() time.Time
}

type ResumeMaintenanceResult struct {
	Status       string             `json:"status"`
	UpdateID     string             `json:"update_id,omitempty"`
	ManifestPath string             `json:"manifest_path,omitempty"`
	HistoryPath  string             `json:"history_path,omitempty"`
	Window       *MaintenanceWindow `json:"maintenance_window,omitempty"`
	Manifest     *UpdateManifest    `json:"manifest,omitempty"`
	Changed      []UpdateChange     `json:"changed"`
	Refused      bool               `json:"refused"`
	Refusal      string             `json:"refusal,omitempty"`
}

type WorkspaceUpdateSpec struct {
	ReleasePath        string `json:"release_path" yaml:"release_path"`
	HomeDir            string `json:"home_dir,omitempty" yaml:"home_dir,omitempty"`
	StateDir           string `json:"state_dir,omitempty" yaml:"state_dir,omitempty"`
	ActivePath         string `json:"active_path,omitempty" yaml:"active_path,omitempty"`
	LocalBinDir        string `json:"local_bin_dir,omitempty" yaml:"local_bin_dir,omitempty"`
	NodeKey            string `json:"node_key,omitempty" yaml:"node_key,omitempty"`
	MainHost           string `json:"main_host,omitempty" yaml:"main_host,omitempty"`
	ServiceManager     string `json:"service_manager,omitempty" yaml:"service_manager,omitempty"`
	LaunchAgentLabel   string `json:"launch_agent_label,omitempty" yaml:"launch_agent_label,omitempty"`
	LaunchAgentPlist   string `json:"launch_agent_plist,omitempty" yaml:"launch_agent_plist,omitempty"`
	LoomBinary         string `json:"loom_binary,omitempty" yaml:"loom_binary,omitempty"`
	NodeAgentBinary    string `json:"node_agent_binary,omitempty" yaml:"node_agent_binary,omitempty"`
	SkipServiceRestart bool   `json:"skip_service_restart,omitempty" yaml:"skip_service_restart,omitempty"`
	SkipHealthCheck    bool   `json:"skip_health_check,omitempty" yaml:"skip_health_check,omitempty"`
}

type WorkspaceUpdatePlan struct {
	SchemaVersion string              `json:"schema_version"`
	PlanID        string              `json:"plan_id"`
	PlanHash      string              `json:"plan_hash"`
	Status        string              `json:"status"`
	CreatedAt     time.Time           `json:"created_at"`
	Spec          WorkspaceUpdateSpec `json:"spec"`
	Active        ReleaseState        `json:"active"`
	Target        ReleaseState        `json:"target"`
	Backup        BackupRequirement   `json:"backup"`
	ServiceImpact ServiceImpact       `json:"service_impact"`
	Rollback      RollbackPlan        `json:"rollback"`
	Steps         []UpdateStep        `json:"steps"`
	Diagnostics   []UpdateDiagnostic  `json:"diagnostics,omitempty"`
	Metadata      map[string]string   `json:"metadata,omitempty"`
}

type WorkspaceUpdatePlanInput struct {
	Spec WorkspaceUpdateSpec
	Now  func() time.Time
}

type WorkspaceApplyInput struct {
	Spec   WorkspaceUpdateSpec
	Yes    bool
	DryRun bool
	Now    func() time.Time
	Runner CommandRunner
}

type WorkspaceApplyResult struct {
	Status       string              `json:"status"`
	UpdateID     string              `json:"update_id"`
	ManifestPath string              `json:"manifest_path,omitempty"`
	HistoryPath  string              `json:"history_path,omitempty"`
	PendingPath  string              `json:"pending_path,omitempty"`
	Plan         WorkspaceUpdatePlan `json:"plan"`
	Manifest     UpdateManifest      `json:"manifest"`
	Changed      []UpdateChange      `json:"changed"`
	Refused      bool                `json:"refused"`
	Refusal      string              `json:"refusal,omitempty"`
}

type WorkspaceRollbackInput struct {
	StateDir           string
	ManifestPath       string
	ToReleasePath      string
	HomeDir            string
	ActivePath         string
	LocalBinDir        string
	NodeKey            string
	MainHost           string
	ServiceManager     string
	LaunchAgentLabel   string
	LaunchAgentPlist   string
	LoomBinary         string
	NodeAgentBinary    string
	SkipServiceRestart bool
	SkipHealthCheck    bool
	Yes                bool
	DryRun             bool
	Now                func() time.Time
	Runner             CommandRunner
}
