package maintenance

import (
	"encoding/json"
	"time"
)

const (
	OperationPending              = "pending"
	OperationRunning              = "running"
	OperationSucceeded            = "succeeded"
	OperationFailed               = "failed"
	OperationCancelled            = "cancelled"
	OperationRequiresManualAction = "requires_manual_action"
	OperationSkipped              = "skipped"

	FindingOpen         = "open"
	FindingAcknowledged = "acknowledged"
	FindingResolved     = "resolved"
	FindingIgnored      = "ignored"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"

	OverallOK       = "ok"
	OverallWarning  = "warning"
	OverallCritical = "critical"
	OverallUnknown  = "unknown"

	WorkerKindPolicyExpiry        = "policy_expiry"
	WorkerKindRealtimeExpiry      = "realtime_expiry"
	WorkerKindDBMaintenance       = "db_maintenance"
	WorkerKindMainBackup          = "main_backup"
	WorkerKindObjectStore         = "object_store_integrity"
	WorkerKindCloudSnapshotUpload = "cloud_snapshot_upload"

	OperationKindMainBackup             = "main_backup"
	OperationKindMainBackupVerify       = "main_backup_verify"
	OperationKindCloudSnapshotUpload    = "cloud_snapshot_upload"
	OperationKindCloudSnapshotRetention = "cloud_snapshot_retention"
	OperationKindObjectStoreSampleScan  = "object_store_integrity_sample"
	OperationKindObjectStoreFullScan    = "object_store_integrity_full"
	OperationKindDatabaseCompaction     = "database_compaction"

	ArtifactKindBackupManifest           = "backup_manifest"
	ArtifactKindBackupHealthReport       = "backup_health_report"
	ArtifactKindPostgresDump             = "postgres_dump"
	ArtifactKindObjectStoreSnapshot      = "object_store_snapshot"
	ArtifactKindImportsSnapshot          = "imports_snapshot"
	ArtifactKindImportsEvidence          = "imports_evidence"
	ArtifactKindPrivateBackupsSnapshot   = "private_backups_snapshot"
	ArtifactKindMainDocumentsSnapshot    = "main_documents_snapshot"
	ArtifactKindStorageRetentionSnapshot = "storage_retention_snapshot"
	ArtifactKindStorageArchiveSnapshot   = "storage_archive_snapshot"
	ArtifactKindNotesProjectionSnapshot  = "notes_projection_snapshot"
	ArtifactKindObjectStoreInventory     = "object_store_inventory"
	ArtifactKindVerificationReport       = "verification_report"
	ArtifactKindCloudSnapshot            = "cloud_snapshot"
	ArtifactKindCloudRetentionPlan       = "cloud_retention_plan"

	FindingKindBackupFailed                = "backup_failed"
	FindingKindBackupUnverified            = "backup_unverified"
	FindingKindBackupStale                 = "backup_stale"
	FindingKindBackupManifestInvalid       = "backup_manifest_invalid"
	FindingKindBackupArtifactMissing       = "backup_artifact_missing"
	FindingKindObjectStoreBlobMissing      = "object_store_blob_missing"
	FindingKindObjectStoreBlobCorrupt      = "object_store_blob_corrupt"
	FindingKindObjectStoreBlobSizeMismatch = "object_store_blob_size_mismatch"
	FindingKindObjectStoreOrphanDetected   = "object_store_orphan_detected"

	VerificationSucceeded = "succeeded"
	VerificationFailed    = "failed"
)

type Operation struct {
	MaintenanceOperationID string          `json:"maintenance_operation_id"`
	OperationKey           string          `json:"operation_key"`
	WorkerInstanceID       string          `json:"worker_instance_id"`
	WorkerRunID            *string         `json:"worker_run_id,omitempty"`
	OperationKind          string          `json:"operation_kind"`
	Status                 string          `json:"status"`
	SubjectKind            string          `json:"subject_kind"`
	SubjectID              string          `json:"subject_id"`
	StartedAt              time.Time       `json:"started_at"`
	FinishedAt             *time.Time      `json:"finished_at,omitempty"`
	ConfigJSON             json.RawMessage `json:"config_json"`
	ResultJSON             json.RawMessage `json:"result_json"`
	ErrorJSON              json.RawMessage `json:"error_json"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type Artifact struct {
	MaintenanceArtifactID  string          `json:"maintenance_artifact_id"`
	MaintenanceOperationID string          `json:"maintenance_operation_id"`
	ArtifactKind           string          `json:"artifact_kind"`
	URI                    string          `json:"uri"`
	SizeBytes              *int64          `json:"size_bytes,omitempty"`
	SHA256                 *string         `json:"sha256,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type Finding struct {
	MaintenanceFindingID string          `json:"maintenance_finding_id"`
	FindingKey           string          `json:"finding_key"`
	WorkerInstanceID     string          `json:"worker_instance_id"`
	WorkerKey            string          `json:"worker_key"`
	WorkerKind           string          `json:"worker_kind"`
	WorkerRunID          *string         `json:"worker_run_id,omitempty"`
	FindingKind          string          `json:"finding_kind"`
	Severity             string          `json:"severity"`
	Status               string          `json:"status"`
	SubjectKind          string          `json:"subject_kind"`
	SubjectID            string          `json:"subject_id"`
	FirstSeenAt          time.Time       `json:"first_seen_at"`
	LastSeenAt           time.Time       `json:"last_seen_at"`
	ResolvedAt           *time.Time      `json:"resolved_at,omitempty"`
	Summary              string          `json:"summary"`
	DetailsJSON          json.RawMessage `json:"details_json"`
	ResolutionJSON       json.RawMessage `json:"resolution_json"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Metadata             json.RawMessage `json:"metadata"`
}

type FindingFilter struct {
	Limit     int
	Status    string
	Severity  string
	WorkerRef string
}

type OperationFilter struct {
	Kind        string
	Status      string
	SubjectKind string
	SubjectID   string
	Limit       int
}

type CreateOperationInput struct {
	OperationKey     string          `json:"operation_key"`
	WorkerInstanceID string          `json:"worker_instance_id"`
	WorkerRunID      string          `json:"worker_run_id,omitempty"`
	OperationKind    string          `json:"operation_kind"`
	Status           string          `json:"status,omitempty"`
	SubjectKind      string          `json:"subject_kind,omitempty"`
	SubjectID        string          `json:"subject_id,omitempty"`
	ConfigJSON       json.RawMessage `json:"config_json,omitempty"`
	ResultJSON       json.RawMessage `json:"result_json,omitempty"`
	ErrorJSON        json.RawMessage `json:"error_json,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type CompleteOperationInput struct {
	OperationRef string          `json:"operation_ref"`
	Status       string          `json:"status"`
	ResultJSON   json.RawMessage `json:"result_json,omitempty"`
	ErrorJSON    json.RawMessage `json:"error_json,omitempty"`
}

type CreateArtifactInput struct {
	MaintenanceOperationID string          `json:"maintenance_operation_id"`
	ArtifactKind           string          `json:"artifact_kind"`
	URI                    string          `json:"uri"`
	SizeBytes              *int64          `json:"size_bytes,omitempty"`
	SHA256                 string          `json:"sha256,omitempty"`
	Metadata               json.RawMessage `json:"metadata,omitempty"`
}

type UpsertFindingInput struct {
	FindingKey       string          `json:"finding_key"`
	WorkerInstanceID string          `json:"worker_instance_id"`
	WorkerRunID      string          `json:"worker_run_id,omitempty"`
	FindingKind      string          `json:"finding_kind"`
	Severity         string          `json:"severity"`
	SubjectKind      string          `json:"subject_kind"`
	SubjectID        string          `json:"subject_id"`
	Summary          string          `json:"summary"`
	DetailsJSON      json.RawMessage `json:"details_json"`
	Metadata         json.RawMessage `json:"metadata"`
}

type Status struct {
	OverallStatus string         `json:"overall_status"`
	Workers       []WorkerStatus `json:"workers"`
	Findings      FindingSummary `json:"findings"`
}

type WorkerStatus struct {
	WorkerInstanceID  string     `json:"worker_instance_id"`
	WorkerKey         string     `json:"worker_key"`
	WorkerKind        string     `json:"worker_kind"`
	HealthStatus      string     `json:"health_status"`
	Severity          string     `json:"severity"`
	AttentionRequired bool       `json:"attention_required"`
	LastSuccessAt     *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
	ConsecutiveFails  int        `json:"consecutive_failures"`
	OpenFindings      int        `json:"open_findings"`
}

type FindingSummary struct {
	Open     int `json:"open"`
	Critical int `json:"critical"`
	Error    int `json:"error"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type DBStatus struct {
	Status          string            `json:"status"`
	Worker          *WorkerStatus     `json:"worker,omitempty"`
	MigrationStatus string            `json:"migration_status"`
	CurrentVersion  int64             `json:"current_version"`
	LatestVersion   int64             `json:"latest_version"`
	Pending         int64             `json:"pending"`
	LatestRun       *DBMaintenanceRun `json:"latest_run,omitempty"`
	Tables          []DatabaseTable   `json:"tables,omitempty"`
	Retention       DatabaseRetention `json:"retention,omitempty"`
	Rollups         []DatabaseRollup  `json:"rollups,omitempty"`
	Warnings        []string          `json:"warnings,omitempty"`
}

type DBMaintenanceRun struct {
	WorkerRunID       string          `json:"worker_run_id"`
	RunStatus         string          `json:"run_status"`
	TriggerKind       string          `json:"trigger_kind"`
	TriggerRef        string          `json:"trigger_ref"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	ResultSummaryJSON json.RawMessage `json:"result_summary_json"`
	CountersJSON      json.RawMessage `json:"counters_json"`
}

type DatabaseTable struct {
	QualifiedName string `json:"qualified_name"`
	Schema        string `json:"schema"`
	Name          string `json:"name"`
	Rows          int64  `json:"rows"`
	TotalBytes    int64  `json:"total_bytes"`
	RetentionRole string `json:"retention_role"`
	Pressure      string `json:"pressure"`
}

type DatabaseRetention struct {
	RecentSuccessDays       int                          `json:"recent_success_days"`
	CutoffAt                time.Time                    `json:"cutoff_at"`
	Candidates              map[string]int64             `json:"candidates"`
	Deleted                 map[string]int64             `json:"deleted,omitempty"`
	Policy                  map[string]string            `json:"policy,omitempty"`
	Plans                   []DatabaseRetentionPlan      `json:"plans,omitempty"`
	AuditCriticalExclusions []DatabaseRetentionExclusion `json:"audit_critical_exclusions,omitempty"`
	LastCompaction          *DatabaseCompactResult       `json:"last_compaction,omitempty"`
}

type DatabaseRetentionPlan struct {
	Table            string `json:"table"`
	RetentionRole    string `json:"retention_role"`
	CandidateClass   string `json:"candidate_class"`
	CandidateRows    int64  `json:"candidate_rows"`
	EstimatedBytes   int64  `json:"estimated_bytes,omitempty"`
	CompactableLater bool   `json:"compactable_later"`
	KeepRule         string `json:"keep_rule"`
	FutureAction     string `json:"future_action"`
	Reason           string `json:"reason"`
}

type DatabaseRetentionExclusion struct {
	Table  string `json:"table"`
	Rule   string `json:"rule"`
	Reason string `json:"reason"`
}

type DatabaseRollup struct {
	RollupDay       time.Time       `json:"rollup_day"`
	RollupKind      string          `json:"rollup_kind"`
	RollupKey       string          `json:"rollup_key"`
	SuccessCount    int64           `json:"success_count"`
	FailureCount    int64           `json:"failure_count"`
	TotalCount      int64           `json:"total_count"`
	TotalDurationMS int64           `json:"total_duration_ms"`
	Metadata        json.RawMessage `json:"metadata"`
}

type DatabaseCompactInput struct {
	DryRun            bool            `json:"dry_run"`
	Confirm           bool            `json:"confirm"`
	RecentSuccessDays int             `json:"recent_success_days"`
	PlanHash          string          `json:"plan_hash,omitempty"`
	Plan              json.RawMessage `json:"plan,omitempty"`
	MaxRowsPerBatch   int64           `json:"max_rows_per_batch,omitempty"`
	MaxTotalRows      int64           `json:"max_total_rows,omitempty"`
	Reason            string          `json:"reason,omitempty"`
}

type DatabaseCompactResult struct {
	Status                  string                       `json:"status"`
	PlanID                  string                       `json:"plan_id,omitempty"`
	PlanHash                string                       `json:"plan_hash,omitempty"`
	DryRun                  bool                         `json:"dry_run"`
	Confirmed               bool                         `json:"confirmed"`
	MutatesDatabase         bool                         `json:"mutates_database"`
	RecentSuccessDays       int                          `json:"recent_success_days"`
	CutoffAt                time.Time                    `json:"cutoff_at"`
	AllowedTables           []string                     `json:"allowed_tables,omitempty"`
	RowLimits               map[string]int64             `json:"row_limits,omitempty"`
	MaxRowsPerBatch         int64                        `json:"max_rows_per_batch,omitempty"`
	MaxTotalRows            int64                        `json:"max_total_rows,omitempty"`
	Candidates              map[string]int64             `json:"candidates"`
	CandidateDrift          map[string]int64             `json:"candidate_drift,omitempty"`
	Deleted                 map[string]int64             `json:"deleted,omitempty"`
	Policy                  map[string]string            `json:"policy,omitempty"`
	Plans                   []DatabaseRetentionPlan      `json:"plans,omitempty"`
	AuditCriticalExclusions []DatabaseRetentionExclusion `json:"audit_critical_exclusions,omitempty"`
	Rollups                 []DatabaseRollup             `json:"rollups,omitempty"`
	Warnings                []string                     `json:"warnings,omitempty"`
	Reason                  string                       `json:"reason,omitempty"`
	EvidenceOperationID     string                       `json:"evidence_operation_id,omitempty"`
	EvidenceStatus          string                       `json:"evidence_status,omitempty"`
	PhysicalStorageNote     string                       `json:"physical_storage_note,omitempty"`
	PlannedAt               time.Time                    `json:"planned_at"`
	CompletedAt             *time.Time                   `json:"completed_at,omitempty"`
}

type BackupOperation struct {
	Operation Operation  `json:"operation"`
	Artifacts []Artifact `json:"artifacts"`
}

type BackupStatus struct {
	Status           string           `json:"status"`
	Worker           *WorkerStatus    `json:"worker,omitempty"`
	LatestSuccessful *BackupOperation `json:"latest_successful,omitempty"`
	LatestFailed     *BackupOperation `json:"latest_failed,omitempty"`
	OpenFindings     FindingSummary   `json:"open_findings"`
}

type CloudProtectionStatus struct {
	Available        bool       `json:"available"`
	WorkerState      string     `json:"worker_state"`
	LastSuccessAt    *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	Verification     string     `json:"verification_state"`
	OpenFindingCount int        `json:"open_finding_count"`
	WarningFindings  int        `json:"warning_findings"`
	CriticalFindings int        `json:"critical_findings"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type BackupVerification struct {
	Status              string            `json:"status"`
	BackupOperationID   string            `json:"backup_operation_id,omitempty"`
	BackupDir           string            `json:"backup_dir"`
	ManifestPath        string            `json:"manifest_path,omitempty"`
	CheckedAt           time.Time         `json:"checked_at"`
	Checks              map[string]string `json:"checks"`
	Errors              []string          `json:"errors"`
	ManifestSchema      string            `json:"manifest_schema,omitempty"`
	CurrentMigration    *int64            `json:"current_migration,omitempty"`
	LatestMigration     *int64            `json:"latest_migration,omitempty"`
	TotalArtifactBytes  int64             `json:"total_artifact_bytes"`
	VerifiedArtifactNum int               `json:"verified_artifact_count"`
}

type ObjectStoreBlobCounts struct {
	Total    int64 `json:"total"`
	Pending  int64 `json:"pending"`
	Verified int64 `json:"verified"`
	Missing  int64 `json:"missing"`
	Corrupt  int64 `json:"corrupt"`
}

type ObjectStoreStatus struct {
	Status     string                `json:"status"`
	Worker     *WorkerStatus         `json:"worker,omitempty"`
	BlobCounts ObjectStoreBlobCounts `json:"blob_counts"`
	LatestRun  *ObjectStoreScanRun   `json:"latest_run,omitempty"`
	Findings   FindingSummary        `json:"findings"`
}

type ObjectStoreScanRun struct {
	WorkerRunID       string          `json:"worker_run_id"`
	RunStatus         string          `json:"run_status"`
	TriggerKind       string          `json:"trigger_kind"`
	TriggerRef        string          `json:"trigger_ref"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	ResultSummaryJSON json.RawMessage `json:"result_summary_json"`
	CountersJSON      json.RawMessage `json:"counters_json"`
}

type ObjectStoreScanInput struct {
	Mode           string          `json:"mode,omitempty"`
	TargetBlobRef  string          `json:"target_blob_ref,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type BackupRunInput struct {
	Reason         string          `json:"reason,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
}

type BackupVerifyInput struct {
	BackupRef string `json:"backup_ref"`
}
