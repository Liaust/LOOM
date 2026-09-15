package watchedroots

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/filesystemmeta"
)

const (
	PathStateSchemaVersion  = "watched_root.path_state.v0.3"
	SummarySchemaVersion    = "watched_root.summary.v0.2"
	CheckpointSchemaVersion = "watched_root.checkpoint.v0.2"

	PathStatusIncluded        = "included"
	PathStatusExcluded        = "excluded"
	PathStatusSkipped         = "skipped"
	PathStatusDeleted         = "deleted"
	PathStatusMissingDeferred = "missing_deferred"
	PathStatusError           = "error"

	PathKindFile      = "file"
	PathKindDirectory = "directory"
	PathKindSymlink   = "symlink"
	PathKindOther     = "other"
	PathKindMissing   = "missing"

	HashStatusComputed       = "computed"
	HashStatusUnchanged      = "unchanged"
	HashStatusDeferred       = "deferred"
	HashStatusTooLarge       = "too_large"
	HashStatusNotRegularFile = "not_regular_file"
	HashStatusNotNeeded      = "not_needed"
	HashStatusError          = "error"

	OutputActionSyncObject           = "sync_object"
	OutputActionSyncMetadata         = "sync_metadata"
	OutputActionDeletionRequest      = "deletion_request"
	OutputActionBackupFile           = "backup_file"
	OutputActionBackupMetadata       = "backup_metadata"
	OutputActionBackupDeletionMarker = "backup_deletion_marker"
	OutputActionBackupSkipped        = "backup_skipped"
	OutputActionSkipped              = "skipped"

	OutputStatusPending        = "pending"
	OutputStatusQueued         = "queued"
	OutputStatusSkipped        = "skipped"
	OutputStatusAlreadyCurrent = "already_current"
	OutputStatusFailed         = "failed"
	OutputStatusDisabled       = "disabled"
	OutputStatusRecorded       = "recorded"

	ReasonIncludedByPattern         = "included_by_pattern"
	ReasonExcludedByPattern         = "excluded_by_pattern"
	ReasonExcludedGeneratedMetadata = "excluded_generated_metadata"
	ReasonExcludedPackageBoundary   = "excluded_package_boundary"
	ReasonExcludedHidden            = "excluded_hidden"
	ReasonExcludedNoInclude         = "excluded_no_include"
	ReasonExcludedFilePolicy        = "excluded_file_policy"
	ReasonExcludedDirectoryMetadata = "excluded_directory_metadata_only"
	ReasonSkippedPathEscape         = "skipped_path_escape"
	ReasonSkippedSymlink            = "skipped_symlink"
	ReasonSkippedSpecialFile        = "skipped_special_file"
	ReasonSkippedPermissionDenied   = "skipped_permission_denied"
	ReasonSkippedTooLarge           = "skipped_too_large"
	ReasonDeletedLocalState         = "deleted_local_state"
	ReasonRootUnavailable           = "root_unavailable"

	ScanModeAuto  = "auto"
	ScanModeFull  = "full"
	ScanModeDirty = "dirty"

	RunStatusHealthy              = "healthy"
	RunStatusDegraded             = "degraded"
	RunStatusBlocked              = "blocked"
	RunStatusRequiresManualAction = "requires_manual_action"
	RunStatusSkipped              = "skipped"

	DirtyHintCreated         = "created"
	DirtyHintModified        = "modified"
	DirtyHintDeleted         = "deleted"
	DirtyHintRescanRequired  = "rescan_required"
	DirtyHintMetadataChanged = "metadata_changed"

	DirtyHintSourceScanner = "scanner"
	DirtyHintSourceManual  = "manual"
	DirtyHintSourceStartup = "startup"

	FindingSeverityInfo     = "info"
	FindingSeverityWarning  = "warning"
	FindingSeverityCritical = "critical"
	FindingStatusOpen       = "open"
	FindingStatusResolved   = "resolved"
	FindingStatusIgnored    = "ignored"

	FindingMassDeleteDeferred           = "mass_delete_deferred"
	FindingRootUnavailable              = "root_unavailable"
	FindingScanBudgetExceeded           = "scan_budget_exhausted"
	FindingPermissionDenied             = "permission_denied"
	FindingPathEscapeSkipped            = "path_escape_skipped"
	FindingBackupFileTooLarge           = "backup_file_too_large"
	FindingBackupQueueLimit             = "backup_queue_limit_reached"
	FindingBackupStageFailed            = "backup_artifact_stage_failed"
	FindingBackupTransportLimitExceeded = "backup_transport_limit_exceeded"
	FindingBackupUnsupportedByPolicy    = "backup_unsupported_by_policy"
	FindingBackupArtifactUploadFailed   = "backup_artifact_upload_failed"
	FindingBackupBatchUploadFailed      = "backup_batch_upload_failed"
	FindingPathCollisionWarning         = "path_collision_warning"
	FindingExternalSymlinkReference     = "external_symlink_reference"
	FindingSpecialFileObserved          = "special_file_observed"
	FindingPackageBoundaryObserved      = "package_boundary_observed"
	FindingGeneratedMetadataObserved    = "generated_metadata_observed"
)

type Policies struct {
	Backup string `json:"backup"`
	Sync   string `json:"sync"`
	Index  string `json:"index"`
	Delete string `json:"delete"`
}

type Classification struct {
	Included          bool     `json:"included"`
	ReasonCode        string   `json:"reason_code"`
	Reason            string   `json:"reason"`
	MatchedInclude    string   `json:"matched_include,omitempty"`
	MatchedExclude    string   `json:"matched_exclude,omitempty"`
	Hidden            bool     `json:"hidden"`
	Symlink           bool     `json:"symlink"`
	Safe              bool     `json:"safe"`
	Policies          Policies `json:"policies"`
	PolicyProfile     string   `json:"policy_profile,omitempty"`
	PolicyVersion     string   `json:"policy_version,omitempty"`
	PolicyFingerprint string   `json:"policy_fingerprint,omitempty"`
	PolicyRuleSource  string   `json:"policy_rule_source,omitempty"`
	PolicyPattern     string   `json:"policy_pattern,omitempty"`
	PolicySourceFile  string   `json:"policy_source_file,omitempty"`
	PolicySourceLine  int      `json:"policy_source_line,omitempty"`
}

type PathObservation struct {
	RelativePath string                      `json:"relative_path"`
	Exists       bool                        `json:"exists"`
	Kind         string                      `json:"kind"`
	Hidden       bool                        `json:"hidden"`
	Symlink      bool                        `json:"symlink"`
	Safe         bool                        `json:"safe"`
	SizeBytes    int64                       `json:"size_bytes,omitempty"`
	Mode         string                      `json:"mode,omitempty"`
	ModifiedAt   time.Time                   `json:"modified_at,omitempty"`
	Fidelity     *filesystemmeta.Observation `json:"fidelity,omitempty"`
	ErrorCode    string                      `json:"error_code,omitempty"`
	Error        string                      `json:"error,omitempty"`
}

type PathState struct {
	SchemaVersion              string                      `json:"schema_version"`
	RootKey                    string                      `json:"root_key"`
	PathKey                    string                      `json:"path_key"`
	RelativePath               string                      `json:"relative_path"`
	Status                     string                      `json:"status"`
	Kind                       string                      `json:"kind"`
	Classification             Classification              `json:"classification"`
	SizeBytes                  int64                       `json:"size_bytes,omitempty"`
	Mode                       string                      `json:"mode,omitempty"`
	ModifiedAt                 *time.Time                  `json:"modified_at,omitempty"`
	ContentHashURI             string                      `json:"content_hash_uri,omitempty"`
	HashStatus                 string                      `json:"hash_status,omitempty"`
	SyncStatus                 string                      `json:"sync_status,omitempty"`
	IndexStatus                string                      `json:"index_status,omitempty"`
	DeletionStatus             string                      `json:"deletion_status,omitempty"`
	BackupStatus               string                      `json:"backup_status,omitempty"`
	BackupMode                 string                      `json:"backup_mode,omitempty"`
	LocalObjectID              string                      `json:"local_object_id,omitempty"`
	LocalVersionID             string                      `json:"local_version_id,omitempty"`
	LocalSyncOutboxID          string                      `json:"local_sync_outbox_id,omitempty"`
	LocalBackupArtifactID      string                      `json:"local_backup_artifact_id,omitempty"`
	LocalBackupOutboxID        string                      `json:"local_backup_outbox_id,omitempty"`
	LocalBackupBatchID         string                      `json:"local_backup_batch_id,omitempty"`
	LastQueuedHashURI          string                      `json:"last_queued_hash_uri,omitempty"`
	LastSyncedHashURI          string                      `json:"last_synced_hash_uri,omitempty"`
	LastSyncedModifiedAt       *time.Time                  `json:"last_synced_modified_at,omitempty"`
	LastQueuedMetadataMtime    *time.Time                  `json:"last_queued_metadata_mtime,omitempty"`
	LastSyncedMetadataSequence int64                       `json:"last_synced_metadata_sequence,omitempty"`
	LastQueuedBackupHashURI    string                      `json:"last_queued_backup_hash_uri,omitempty"`
	LastBackedUpHashURI        string                      `json:"last_backed_up_hash_uri,omitempty"`
	MainObjectID               string                      `json:"main_object_id,omitempty"`
	MainVersionID              string                      `json:"main_version_id,omitempty"`
	MainBlobID                 string                      `json:"main_blob_id,omitempty"`
	MainBackupBatchID          string                      `json:"main_backup_batch_id,omitempty"`
	MainBackupItemID           string                      `json:"main_backup_item_id,omitempty"`
	PrivateBackupOperationID   string                      `json:"private_backup_operation_id,omitempty"`
	FileTransferID             string                      `json:"file_transfer_id,omitempty"`
	FileTransferStorageEntryID string                      `json:"file_transfer_storage_entry_id,omitempty"`
	DeletionRequestID          string                      `json:"deletion_request_id,omitempty"`
	BackupDeletionMarkerID     string                      `json:"backup_deletion_marker_id,omitempty"`
	LastOutputPlannedAt        *time.Time                  `json:"last_output_planned_at,omitempty"`
	LastOutputAppliedAt        *time.Time                  `json:"last_output_applied_at,omitempty"`
	LastBackupQueuedAt         *time.Time                  `json:"last_backup_queued_at,omitempty"`
	LastBackedUpAt             *time.Time                  `json:"last_backed_up_at,omitempty"`
	LastOutputErrorCode        string                      `json:"last_output_error_code,omitempty"`
	LastOutputErrorMessage     string                      `json:"last_output_error_message,omitempty"`
	LastBackupErrorCode        string                      `json:"last_backup_error_code,omitempty"`
	LastBackupErrorMessage     string                      `json:"last_backup_error_message,omitempty"`
	Fidelity                   *filesystemmeta.Observation `json:"fidelity,omitempty"`
	FirstSeenAt                time.Time                   `json:"first_seen_at"`
	LastSeenAt                 time.Time                   `json:"last_seen_at"`
	LastScannedAt              time.Time                   `json:"last_scanned_at"`
	DeletedAt                  *time.Time                  `json:"deleted_at,omitempty"`
	LastErrorCode              string                      `json:"last_error_code,omitempty"`
	LastErrorMessage           string                      `json:"last_error_message,omitempty"`
	Metadata                   json.RawMessage             `json:"metadata,omitempty"`
}

type DirtyHint struct {
	RootKey       string    `json:"root_key"`
	RelativePath  string    `json:"relative_path"`
	HintKind      string    `json:"hint_kind"`
	Source        string    `json:"source"`
	Count         int       `json:"count"`
	FirstSeenAt   time.Time `json:"first_seen_at"`
	LastSeenAt    time.Time `json:"last_seen_at"`
	LastErrorCode string    `json:"last_error_code,omitempty"`
}

type Finding struct {
	FindingID    string          `json:"finding_id"`
	RootKey      string          `json:"root_key"`
	Severity     string          `json:"severity"`
	Status       string          `json:"status"`
	Kind         string          `json:"kind"`
	RelativePath string          `json:"relative_path,omitempty"`
	Summary      string          `json:"summary"`
	Details      json.RawMessage `json:"details,omitempty"`
	FirstSeenAt  time.Time       `json:"first_seen_at"`
	LastSeenAt   time.Time       `json:"last_seen_at"`
}

type RootCheckpoint struct {
	SchemaVersion             string     `json:"schema_version"`
	RootKey                   string     `json:"root_key"`
	WorkerKey                 string     `json:"worker_key"`
	ConfigHash                string     `json:"config_hash"`
	LastRunID                 string     `json:"last_run_id,omitempty"`
	LastStartedAt             *time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt            *time.Time `json:"last_finished_at,omitempty"`
	LastSuccessfulReconcileAt *time.Time `json:"last_successful_reconcile_at,omitempty"`
	LastFullRescanAt          *time.Time `json:"last_full_rescan_at,omitempty"`
	LastSequence              int64      `json:"last_sequence"`
	PendingRescan             bool       `json:"pending_rescan"`
	RootReachable             bool       `json:"root_reachable"`
	PathStateCount            int        `json:"path_state_count"`
	PathStateSnapshotHash     string     `json:"path_state_snapshot_hash,omitempty"`
	LastErrorCode             string     `json:"last_error_code,omitempty"`
	LastErrorMessage          string     `json:"last_error_message,omitempty"`
}

type ScanCounts struct {
	Visited           int            `json:"visited"`
	Files             int            `json:"files"`
	Directories       int            `json:"directories"`
	Included          int            `json:"included"`
	Excluded          int            `json:"excluded"`
	Skipped           int            `json:"skipped"`
	Deleted           int            `json:"deleted"`
	MissingDeferred   int            `json:"missing_deferred"`
	Changed           int            `json:"changed"`
	Unchanged         int            `json:"unchanged"`
	HashComputed      int            `json:"hash_computed"`
	HashUnchanged     int            `json:"hash_unchanged"`
	HashDeferred      int            `json:"hash_deferred"`
	BudgetExhausted   int            `json:"budget_exhausted"`
	Errors            int            `json:"errors"`
	DirtyHintsBefore  int            `json:"dirty_hints_before"`
	DirtyHintsCleared int            `json:"dirty_hints_cleared"`
	Findings          int            `json:"findings"`
	FindingsResolved  int            `json:"findings_resolved,omitempty"`
	IgnoredDirtyHints int            `json:"ignored_dirty_hints,omitempty"`
	IgnoredBySource   map[string]int `json:"ignored_by_source,omitempty"`
}

type PathChange struct {
	RelativePath   string `json:"relative_path"`
	ChangeKind     string `json:"change_kind"`
	PreviousHash   string `json:"previous_hash_uri,omitempty"`
	CurrentHash    string `json:"current_hash_uri,omitempty"`
	PreviousStatus string `json:"previous_status,omitempty"`
	CurrentStatus  string `json:"current_status,omitempty"`
}

type ScanResult struct {
	RootKey           string              `json:"root_key"`
	Mode              string              `json:"mode"`
	Status            string              `json:"status"`
	Message           string              `json:"message,omitempty"`
	StartedAt         time.Time           `json:"started_at"`
	FinishedAt        time.Time           `json:"finished_at"`
	DurationMS        int64               `json:"duration_ms"`
	Counts            ScanCounts          `json:"counts"`
	ChangedPaths      []PathChange        `json:"changed_paths,omitempty"`
	Findings          []Finding           `json:"findings,omitempty"`
	Checkpoint        RootCheckpoint      `json:"checkpoint"`
	Summary           RootSummary         `json:"summary"`
	OutputPlan        *OutputPlan         `json:"output_plan,omitempty"`
	OutputFlush       *OutputFlush        `json:"output_flush,omitempty"`
	BackupFlush       *BackupOutputFlush  `json:"backup_output_flush,omitempty"`
	BackupStatus      *BackupOutputStatus `json:"backup_status,omitempty"`
	MainReport        *MainReport         `json:"main_report,omitempty"`
	PolicyVersion     string              `json:"policy_version,omitempty"`
	PolicyFingerprint string              `json:"policy_fingerprint,omitempty"`
}

type BackupOutputStatus struct {
	RootKey           string `json:"root_key,omitempty"`
	Artifacts         int    `json:"artifacts"`
	Batches           int    `json:"batches"`
	Items             int    `json:"items"`
	Pending           int    `json:"pending"`
	Retryable         int    `json:"retryable"`
	Accepted          int    `json:"accepted"`
	Duplicates        int    `json:"duplicates"`
	Conflicted        int    `json:"conflicted"`
	Failed            int    `json:"failed"`
	ManualAction      int    `json:"manual_action"`
	PendingBytes      int64  `json:"pending_bytes"`
	LastErrorCode     string `json:"last_error_code,omitempty"`
	Protected         int    `json:"protected"`
	Ignored           int    `json:"ignored"`
	PolicyVersion     string `json:"policy_version,omitempty"`
	PolicyFingerprint string `json:"policy_fingerprint,omitempty"`
}

type OutputFlush struct {
	Attempted       bool   `json:"attempted"`
	Status          string `json:"status"`
	SubmittedItems  int    `json:"submitted_items"`
	PendingBefore   int    `json:"pending_before"`
	PendingAfter    int    `json:"pending_after"`
	AcceptedAfter   int    `json:"accepted_after"`
	ConflictedAfter int    `json:"conflicted_after"`
	FailedAfter     int    `json:"failed_after"`
	Error           string `json:"error,omitempty"`
}

type BackupOutputFlush struct {
	Attempted       bool   `json:"attempted"`
	Status          string `json:"status"`
	SubmittedItems  int    `json:"submitted_items"`
	PendingBefore   int    `json:"pending_before"`
	PendingAfter    int    `json:"pending_after"`
	RetryableAfter  int    `json:"retryable_after"`
	AcceptedAfter   int    `json:"accepted_after"`
	ConflictedAfter int    `json:"conflicted_after"`
	FailedAfter     int    `json:"failed_after"`
	ManualAfter     int    `json:"manual_after"`
	Error           string `json:"error,omitempty"`
}

type MainReport struct {
	Attempted        bool   `json:"attempted"`
	Status           string `json:"status"`
	WatchedRootID    string `json:"watched_root_id,omitempty"`
	FindingsReported int    `json:"findings_reported"`
	FindingsResolved int    `json:"findings_resolved,omitempty"`
	Error            string `json:"error,omitempty"`
}

type RootSummary struct {
	SchemaVersion     string         `json:"schema_version"`
	RootKey           string         `json:"root_key"`
	WorkerKey         string         `json:"worker_key"`
	Status            string         `json:"status"`
	RootReachable     bool           `json:"root_reachable"`
	LastScanAt        time.Time      `json:"last_scan_at,omitempty"`
	LastFullScanAt    time.Time      `json:"last_full_scan_at,omitempty"`
	Included          int            `json:"included"`
	Excluded          int            `json:"excluded"`
	Skipped           int            `json:"skipped"`
	Deleted           int            `json:"deleted"`
	MissingDeferred   int            `json:"missing_deferred"`
	Changed           int            `json:"changed"`
	HashComputed      int            `json:"hash_computed"`
	BudgetExhausted   int            `json:"budget_exhausted"`
	DirtyHints        int            `json:"dirty_hints"`
	Findings          int            `json:"findings"`
	Message           string         `json:"message,omitempty"`
	GeneratedAt       time.Time      `json:"generated_at"`
	PolicyVersion     string         `json:"policy_version,omitempty"`
	PolicyFingerprint string         `json:"policy_fingerprint,omitempty"`
	IgnoredBySource   map[string]int `json:"ignored_by_source,omitempty"`
}

type RootPaths struct {
	RootDir       string `json:"root_dir"`
	Checkpoint    string `json:"checkpoint"`
	DirtyHints    string `json:"dirty_hints"`
	LatestSummary string `json:"latest_summary"`
	PathsDir      string `json:"paths_dir"`
	FindingsDir   string `json:"findings_dir"`
}
