package lane

import (
	"context"
	"time"

	"loom.local/loom/internal/filepolicy"
	"loom.local/loom/internal/filesystemmeta"
)

const (
	StatusSchemaVersion         = "loom.lane.status.v0.9.10"
	BatchSchemaVersion          = "loom.lane.batch.v0.9.10"
	TransferPlanSchemaVersion   = "loom.lane.transfer_plan.v1"
	BundlePlanSchemaVersion     = "loom.lane.bundle_plan.v1"
	BundleManifestSchemaVersion = "loom.lane.bundle_manifest.v1"
	AttentionSchemaVersion      = "loom.lane.attention.v0.9.2"

	DefaultLaneRelPath  = "loom-lane"
	DefaultStateRelPath = ".loom/state/lane"
	DefaultMainHost     = "loom-main"
	DefaultRemoteRoot   = "/var/lib/loom/lane"

	StateNotInitialized = "not_initialized"
	StateMissing        = "missing"
	StateEmpty          = "empty"
	StatePending        = "pending"
	StateError          = "error"

	PreflightReady        = "ready"
	PreflightMissingTools = "missing_tools"
	PreflightDegraded     = "degraded"

	BatchStatusPrepared                          = "prepared"
	BatchStatusPending                           = BatchStatusPrepared
	BatchStatusBundling                          = "bundling"
	BatchStatusBundleReady                       = "bundle_ready"
	BatchStatusTransferring                      = "transferring"
	BatchStatusTransferred                       = "transferred"
	BatchStatusPromotionFailed                   = "promotion_failed"
	BatchStatusAcceptedOnMain                    = "accepted_on_main"
	BatchStatusCatalogFailed                     = "catalog_failed"
	BatchStatusCataloged                         = "cataloged"
	BatchStatusSourceCleanupFailed               = "source_cleanup_failed"
	BatchStatusObsoleteAcceptedMigrationRequired = "obsolete_accepted_migration_required"
	BatchStatusPublishedStorageView              = "published_to_storage_view"
	BatchStatusLocalCleanupWithheld              = "local_cleanup_withheld"
	BatchStatusLocalCleanupDone                  = "local_cleanup_done"
	BatchStatusAccepted                          = "accepted"
	BatchStatusFailed                            = "failed"
	BatchStatusSuperseded                        = "superseded"

	AttentionStatusActive       = "active"
	AttentionStatusAcknowledged = "acknowledged"
	AttentionStatusArchived     = "archived"

	DefaultCommandOutputLimit = 12 * 1024

	TransportModeAuto       TransportMode = "auto"
	TransportModeFileTree   TransportMode = "file_tree"
	TransportModeBundleSeed TransportMode = "bundle_seed"

	RemoteStagingPrepare RemoteStagingOperation = "prepare"
	RemoteStagingResume  RemoteStagingOperation = "resume"
	RemoteStagingCleanup RemoteStagingOperation = "cleanup"

	LocalCleanupIntentKeep       = "keep_local"
	LocalCleanupIntentQuarantine = "quarantine_after_catalog"

	BundleArchiveFormatPAXTar      = "pax_tar"
	BundleCompressionNone          = "none"
	BundleTriggerManualLane        = "manual_lane"
	BundleReadinessOperatorTrigger = "operator_trigger"
	BundleCustodyUnpack            = "unpack_normal_custody"

	SafetyArtifactRetainedForRetry    = "retained_for_retry"
	SafetyArtifactRemovalPending      = "removal_pending"
	SafetyArtifactRemovedAfterSuccess = "removed_after_success"
	BundleCleanupRetainedForRetry     = SafetyArtifactRetainedForRetry
	BundleCleanupRemovalPending       = SafetyArtifactRemovalPending
	BundleCleanupRemovedAfterSuccess  = SafetyArtifactRemovedAfterSuccess

	CleanupQuarantineRetainedForRecovery = "retained_for_recovery"
	CleanupQuarantineRestored            = "restored"
	CleanupQuarantineRemovalPending      = "removal_pending"
	CleanupQuarantineRemovedAfterGrace   = "removed_after_grace"

	CleanupRemovalReasonGraceExpired = "grace_expired"
	CleanupRemovalReasonNewerSuccess = "newer_success"
	SafetyRemovalReasonMainPublished = "main_published"
	SafetyRemovalReasonMainCataloged = "main_cataloged"

	SuccessfulCleanupQuarantineGrace       = 30 * time.Minute
	DefaultRecoveryStorageWarningBytes     = int64(1 << 30)
	DefaultLaneHousekeepingIntervalSeconds = 60
)

var remoteRootBoundary = DefaultRemoteRoot

type StatusInput struct {
	RootPath             string
	LaneRelPath          string
	StateRelPath         string
	StatePath            string
	MainHost             string
	MaxItems             int
	Now                  func() time.Time
	LookupPath           func(string) (string, error)
	SSHConfigCheck       func(string) error
	Profile              filepolicy.Profile
	RecoveryWarningBytes int64
}

type Status struct {
	SchemaVersion            string               `json:"schema_version"`
	State                    string               `json:"state"`
	RootPath                 string               `json:"root_path"`
	LanePath                 string               `json:"lane_path"`
	LaneRelativePath         string               `json:"lane_relative_path"`
	StatePath                string               `json:"state_path"`
	PendingItems             int                  `json:"pending_items"`
	PendingFiles             int                  `json:"pending_files"`
	PendingDirs              int                  `json:"pending_dirs"`
	PendingBytes             int64                `json:"pending_bytes"`
	ActivePendingItems       int                  `json:"active_pending_items,omitempty"`
	AcknowledgedPendingItems int                  `json:"acknowledged_pending_items,omitempty"`
	Items                    []PendingItem        `json:"items,omitempty"`
	LastTransfer             *TransferSummary     `json:"last_transfer,omitempty"`
	Preflight                PreflightStatus      `json:"preflight"`
	Diagnostics              []Diagnostic         `json:"diagnostics,omitempty"`
	InspectedAt              time.Time            `json:"inspected_at"`
	IgnoredEntryCount        int                  `json:"ignored_entry_count,omitempty"`
	IgnoredFileCount         int                  `json:"ignored_file_count,omitempty"`
	IgnoredBytes             int64                `json:"ignored_bytes,omitempty"`
	Profile                  filepolicy.Profile   `json:"profile"`
	PolicyVersion            string               `json:"policy_version"`
	PolicyFingerprint        string               `json:"policy_fingerprint,omitempty"`
	InventoryHash            string               `json:"inventory_hash,omitempty"`
	PolicyHashes             map[string]string    `json:"policy_hashes,omitempty"`
	Warnings                 []string             `json:"warnings,omitempty"`
	Transport                BundleRecommendation `json:"transport"`
	RecoveryStorage          RecoveryStorage      `json:"recovery_storage"`
	transferPlan             TransferPlan
}

type RecoveryStorage struct {
	RetainedBytes                     int64      `json:"retained_bytes"`
	SuccessfulGraceBytes              int64      `json:"successful_grace_bytes"`
	RedundantSuccessfulArtifactBytes  int64      `json:"redundant_successful_artifact_bytes"`
	ProtectedEvidenceBytes            int64      `json:"protected_evidence_bytes"`
	CleanupQuarantineBytes            int64      `json:"cleanup_quarantine_bytes"`
	TransportSafetyBytes              int64      `json:"transport_safety_bytes"`
	UntrackedBytes                    int64      `json:"untracked_bytes"`
	SuccessfulQuarantineCount         int        `json:"successful_quarantine_count"`
	ProtectedEvidenceCount            int        `json:"protected_evidence_count"`
	RedundantSuccessfulArtifactCount  int        `json:"redundant_successful_artifact_count"`
	UntrackedPathCount                int        `json:"untracked_path_count"`
	NextSuccessfulQuarantineExpiresAt *time.Time `json:"next_successful_quarantine_expires_at,omitempty"`
}

type PendingItem struct {
	RelativePath    string                      `json:"relative_path"`
	Kind            string                      `json:"kind"`
	Bytes           int64                       `json:"bytes"`
	FileCount       int                         `json:"file_count"`
	DirCount        int                         `json:"dir_count"`
	ModifiedAt      time.Time                   `json:"modified_at"`
	AgeSeconds      int64                       `json:"age_seconds,omitempty"`
	AttentionStatus string                      `json:"attention_status,omitempty"`
	AttentionReason string                      `json:"attention_reason,omitempty"`
	Fingerprint     string                      `json:"fingerprint,omitempty"`
	NextActions     []SafeAction                `json:"next_actions,omitempty"`
	Fidelity        *filesystemmeta.Observation `json:"fidelity,omitempty"`
	Warnings        []string                    `json:"warnings,omitempty"`
}

type TransferSummary struct {
	BatchID                             string             `json:"batch_id"`
	Status                              string             `json:"status"`
	VisibleStoragePath                  string             `json:"visible_storage_path,omitempty"`
	FileCount                           int                `json:"file_count"`
	TotalBytes                          int64              `json:"total_bytes"`
	Progress                            Progress           `json:"progress,omitempty"`
	Metrics                             Metrics            `json:"metrics,omitempty"`
	StartedAt                           *time.Time         `json:"started_at,omitempty"`
	CompletedAt                         *time.Time         `json:"completed_at,omitempty"`
	ErrorMessage                        string             `json:"error_message,omitempty"`
	AttentionStatus                     string             `json:"attention_status,omitempty"`
	AttentionNote                       string             `json:"attention_note,omitempty"`
	AttentionUpdatedAt                  *time.Time         `json:"attention_updated_at,omitempty"`
	NextActions                         []SafeAction       `json:"next_actions,omitempty"`
	Profile                             filepolicy.Profile `json:"profile,omitempty"`
	PolicyVersion                       string             `json:"policy_version,omitempty"`
	PolicyFingerprint                   string             `json:"policy_fingerprint,omitempty"`
	InventoryHash                       string             `json:"inventory_hash,omitempty"`
	IgnoredFileCount                    int                `json:"ignored_file_count,omitempty"`
	IgnoredBytes                        int64              `json:"ignored_bytes,omitempty"`
	RequestedTransport                  TransportMode      `json:"requested_transport,omitempty"`
	SelectedTransport                   TransportMode      `json:"selected_transport,omitempty"`
	TransportReason                     string             `json:"transport_reason,omitempty"`
	LocalCleanupIntent                  string             `json:"local_cleanup_intent,omitempty"`
	AllowCrossDevicePromotion           bool               `json:"allow_cross_device_promotion,omitempty"`
	BundleArtifactPath                  string             `json:"bundle_artifact_path,omitempty"`
	BundleArchiveSHA256                 string             `json:"bundle_archive_sha256,omitempty"`
	BundleCleanupState                  string             `json:"bundle_cleanup_state,omitempty"`
	LocalSafetyCleanupState             string             `json:"local_safety_cleanup_state,omitempty"`
	LocalSafetyRemovedAt                *time.Time         `json:"local_safety_removed_at,omitempty"`
	LocalSafetyRemovalReason            string             `json:"local_safety_removal_reason,omitempty"`
	LocalCleanupQuarantinePath          string             `json:"local_cleanup_quarantine_path,omitempty"`
	LocalCleanupQuarantineState         string             `json:"local_cleanup_quarantine_state,omitempty"`
	LocalCleanupQuarantineBytes         int64              `json:"local_cleanup_quarantine_bytes,omitempty"`
	LocalCleanupQuarantineExpiresAt     *time.Time         `json:"local_cleanup_quarantine_expires_at,omitempty"`
	LocalCleanupQuarantineRemovedAt     *time.Time         `json:"local_cleanup_quarantine_removed_at,omitempty"`
	LocalCleanupQuarantineRemovalReason string             `json:"local_cleanup_quarantine_removal_reason,omitempty"`
	QuarantinedLocalItems               []string           `json:"quarantined_local_items,omitempty"`
	RestoredLocalItems                  []string           `json:"restored_local_items,omitempty"`
	RemovedLocalItems                   []string           `json:"removed_local_items,omitempty"`
}

type BatchRecord struct {
	SchemaVersion                       string             `json:"schema_version"`
	BatchID                             string             `json:"batch_id"`
	Status                              string             `json:"status"`
	SourceNodeKey                       string             `json:"source_node_key,omitempty"`
	SourceBoxID                         string             `json:"source_box_id,omitempty"`
	VisibleStoragePath                  string             `json:"visible_storage_path,omitempty"`
	RemoteStagingPath                   string             `json:"remote_staging_path,omitempty"`
	RemoteRuntimeRoot                   string             `json:"remote_runtime_root,omitempty"`
	RemoteReceiverUser                  string             `json:"remote_receiver_user,omitempty"`
	RemoteReceiverSwitchRequired        bool               `json:"remote_receiver_switch_required,omitempty"`
	RemoteAcceptedPath                  string             `json:"remote_accepted_path,omitempty"`
	LocalSafetyPath                     string             `json:"local_safety_path,omitempty"`
	FileCount                           int                `json:"file_count"`
	TotalBytes                          int64              `json:"total_bytes"`
	Items                               []PendingItem      `json:"items,omitempty"`
	Progress                            Progress           `json:"progress,omitempty"`
	Metrics                             Metrics            `json:"metrics,omitempty"`
	StartedAt                           *time.Time         `json:"started_at,omitempty"`
	CompletedAt                         *time.Time         `json:"completed_at,omitempty"`
	ErrorMessage                        string             `json:"error_message,omitempty"`
	AttentionStatus                     string             `json:"attention_status,omitempty"`
	AttentionNote                       string             `json:"attention_note,omitempty"`
	AttentionUpdatedAt                  *time.Time         `json:"attention_updated_at,omitempty"`
	Profile                             filepolicy.Profile `json:"profile,omitempty"`
	PolicyVersion                       string             `json:"policy_version,omitempty"`
	PolicyFingerprint                   string             `json:"policy_fingerprint,omitempty"`
	InventoryHash                       string             `json:"inventory_hash,omitempty"`
	PolicyHashes                        map[string]string  `json:"policy_hashes,omitempty"`
	IgnoredFileCount                    int                `json:"ignored_file_count,omitempty"`
	IgnoredBytes                        int64              `json:"ignored_bytes,omitempty"`
	TransferManifestPath                string             `json:"transfer_manifest_path,omitempty"`
	LocalCleanupIntent                  string             `json:"local_cleanup_intent,omitempty"`
	AllowCrossDevicePromotion           bool               `json:"allow_cross_device_promotion,omitempty"`
	RequestedTransport                  TransportMode      `json:"requested_transport,omitempty"`
	SelectedTransport                   TransportMode      `json:"selected_transport,omitempty"`
	TransportReason                     string             `json:"transport_reason,omitempty"`
	BundleArtifactPath                  string             `json:"bundle_artifact_path,omitempty"`
	BundleArchivePath                   string             `json:"bundle_archive_path,omitempty"`
	BundleManifestPath                  string             `json:"bundle_manifest_path,omitempty"`
	BundleArchiveSHA256                 string             `json:"bundle_archive_sha256,omitempty"`
	BundleManifestSHA256                string             `json:"bundle_manifest_sha256,omitempty"`
	BundleArchiveBytes                  int64              `json:"bundle_archive_bytes,omitempty"`
	BundleCleanupState                  string             `json:"bundle_cleanup_state,omitempty"`
	BundleResumed                       bool               `json:"bundle_resumed,omitempty"`
	LocalSafetyCleanupState             string             `json:"local_safety_cleanup_state,omitempty"`
	LocalSafetyRemovedAt                *time.Time         `json:"local_safety_removed_at,omitempty"`
	LocalSafetyRemovalReason            string             `json:"local_safety_removal_reason,omitempty"`
	LocalCleanupQuarantinePath          string             `json:"local_cleanup_quarantine_path,omitempty"`
	LocalCleanupQuarantineState         string             `json:"local_cleanup_quarantine_state,omitempty"`
	LocalCleanupQuarantineBytes         int64              `json:"local_cleanup_quarantine_bytes,omitempty"`
	LocalCleanupQuarantineExpiresAt     *time.Time         `json:"local_cleanup_quarantine_expires_at,omitempty"`
	LocalCleanupQuarantineRemovedAt     *time.Time         `json:"local_cleanup_quarantine_removed_at,omitempty"`
	LocalCleanupQuarantineRemovalReason string             `json:"local_cleanup_quarantine_removal_reason,omitempty"`
	QuarantinedLocalItems               []string           `json:"quarantined_local_items,omitempty"`
	RestoredLocalItems                  []string           `json:"restored_local_items,omitempty"`
	RemovedLocalItems                   []string           `json:"removed_local_items,omitempty"`
}

type TransferPlan struct {
	SchemaVersion     string                `json:"schema_version"`
	Profile           filepolicy.Profile    `json:"profile"`
	PolicyVersion     string                `json:"policy_version"`
	PolicyFingerprint string                `json:"policy_fingerprint"`
	InventoryHash     string                `json:"inventory_hash"`
	PolicyHashes      map[string]string     `json:"policy_hashes,omitempty"`
	Entries           []TransferEntry       `json:"entries,omitempty"`
	Ignored           []filepolicy.Decision `json:"ignored,omitempty"`
	Items             []PendingItem         `json:"items,omitempty"`
	FileCount         int                   `json:"file_count"`
	DirCount          int                   `json:"dir_count"`
	TotalBytes        int64                 `json:"total_bytes"`
	IgnoredFileCount  int                   `json:"ignored_file_count"`
	IgnoredDirCount   int                   `json:"ignored_dir_count"`
	IgnoredBytes      int64                 `json:"ignored_bytes"`
	Warnings          []string              `json:"warnings,omitempty"`
	Transport         BundleRecommendation  `json:"transport"`
}

type TransportMode string

type RemoteStagingOperation string

type RemoteStagingInput struct {
	RuntimeRoot          string                 `json:"-"`
	ExpectedRuntimeRoot  string                 `json:"expected_runtime_root,omitempty"`
	ExpectedReceiverUser string                 `json:"expected_receiver_user,omitempty"`
	SourceNodeKey        string                 `json:"source_node_key"`
	BatchID              string                 `json:"batch_id"`
	Operation            RemoteStagingOperation `json:"operation"`
}

type RemoteStagingResult struct {
	SourceNodeKey          string                 `json:"source_node_key"`
	BatchID                string                 `json:"batch_id"`
	Operation              RemoteStagingOperation `json:"operation"`
	RuntimeRoot            string                 `json:"runtime_root"`
	StagingPath            string                 `json:"staging_path"`
	Status                 string                 `json:"status"`
	Created                bool                   `json:"created,omitempty"`
	Removed                bool                   `json:"removed,omitempty"`
	ReceiverUser           string                 `json:"receiver_user,omitempty"`
	ReceiverSwitchRequired bool                   `json:"receiver_switch_required,omitempty"`
}

type BundleThresholds struct {
	FileCountAbove        int   `json:"file_count_above"`
	SmallFileCountAbove   int   `json:"small_file_count_above"`
	SmallFileAverageBelow int64 `json:"small_file_average_below_bytes"`
}

type BundleRecommendation struct {
	SchemaVersion           string           `json:"schema_version"`
	RequestedMode           TransportMode    `json:"requested_mode"`
	RecommendedMode         TransportMode    `json:"recommended_mode"`
	SelectedMode            TransportMode    `json:"selected_mode"`
	ReasonCode              string           `json:"reason_code"`
	Reason                  string           `json:"reason"`
	RegularFileCount        int              `json:"regular_file_count"`
	RegularFileBytes        int64            `json:"regular_file_bytes"`
	AverageFileBytes        int64            `json:"average_file_bytes"`
	EstimatedArchiveBytes   int64            `json:"estimated_archive_bytes"`
	TemporarySafetyMargin   int64            `json:"temporary_safety_margin_bytes"`
	EstimatedTemporaryBytes int64            `json:"estimated_temporary_bytes"`
	Thresholds              BundleThresholds `json:"thresholds"`
	Forced                  bool             `json:"forced"`
	ForceWarning            string           `json:"force_warning,omitempty"`
}

type BundleManifest struct {
	SchemaVersion           string              `json:"schema_version"`
	BatchID                 string              `json:"batch_id"`
	SourcePath              string              `json:"source_path"`
	SourceNodeKey           string              `json:"source_node_key"`
	SourceBoxID             string              `json:"source_box_id,omitempty"`
	CreatedAt               time.Time           `json:"created_at"`
	ArchiveFormat           string              `json:"archive_format"`
	Compression             string              `json:"compression"`
	TriggerKind             string              `json:"trigger_kind"`
	ReadinessBasis          string              `json:"readiness_basis"`
	TransferMode            TransportMode       `json:"transfer_mode"`
	SelectionReason         string              `json:"selection_reason"`
	PolicyFingerprint       string              `json:"policy_fingerprint"`
	PolicyHashes            map[string]string   `json:"policy_hashes,omitempty"`
	InventoryHash           string              `json:"inventory_hash"`
	TotalSourceFiles        int                 `json:"total_source_files"`
	TotalSourceBytes        int64               `json:"total_source_bytes"`
	ExcludedEntryCount      int                 `json:"excluded_entry_count"`
	ExcludedFileCount       int                 `json:"excluded_file_count"`
	ExcludedBytes           int64               `json:"excluded_bytes"`
	ArchiveParts            []BundleArchivePart `json:"archive_parts"`
	Plan                    TransferPlan        `json:"transfer_plan"`
	SourceFingerprintBefore string              `json:"source_fingerprint_before"`
	SourceFingerprintAfter  string              `json:"source_fingerprint_after"`
	CustodyAction           string              `json:"custody_action"`
	CleanupState            string              `json:"cleanup_state"`
	RestoreInstructions     string              `json:"restore_instructions"`
}

type BundleArchivePart struct {
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type BundleArtifact struct {
	BatchID        string    `json:"batch_id"`
	ArtifactPath   string    `json:"artifact_path"`
	ArchivePath    string    `json:"archive_path"`
	ManifestPath   string    `json:"manifest_path"`
	ArchiveBytes   int64     `json:"archive_bytes"`
	ArchiveSHA256  string    `json:"archive_sha256"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	CreatedAt      time.Time `json:"created_at"`
	CleanupState   string    `json:"cleanup_state"`
}

type BundleAcceptInput struct {
	RemoteRoot                string
	TrustedRemoteRoot         bool
	ImportsRoot               string
	SourceNodeKey             string
	SourceBoxID               string
	BatchID                   string
	AcceptedDate              string
	CustodyNodeKey            string
	ManifestPath              string
	ArchivePath               string
	AcceptedPath              string
	AcceptedAt                time.Time
	AllowCrossDevicePromotion bool
	DeviceID                  func(string) (uint64, error)
	AvailableBytes            func(string) (int64, error)
	Rename                    func(string, string) error
	BeforeCopyEntry           func(int, string) error
	BeforeExtractEntry        func(int, TransferEntry) error
}

type BundleUnpackResult struct {
	BatchID          string `json:"batch_id"`
	AcceptedPath     string `json:"accepted_path"`
	ManifestPath     string `json:"manifest_path"`
	ArchivePath      string `json:"archive_path"`
	ArchiveSHA256    string `json:"archive_sha256"`
	FileCount        int    `json:"file_count"`
	DirCount         int    `json:"dir_count"`
	TotalBytes       int64  `json:"total_bytes"`
	Idempotent       bool   `json:"idempotent"`
	UnpackDurationMS int64  `json:"unpack_duration_ms,omitempty"`
}

type BundleAcceptResult struct {
	Unpack  BundleUnpackResult `json:"unpack"`
	Catalog AcceptResult       `json:"catalog"`
}

type CreateBundleInput struct {
	LanePath       string
	PolicyRoot     string
	ArtifactRoot   string
	BatchID        string
	SourceNodeKey  string
	SourceBoxID    string
	Plan           TransferPlan
	CreatedAt      time.Time
	AvailableBytes func(string) (int64, error)
	BeforeEntry    func(int, TransferEntry) error
}

type TransferEntry struct {
	RelativePath string              `json:"relative_path"`
	Kind         string              `json:"kind"`
	Bytes        int64               `json:"bytes,omitempty"`
	Mode         uint32              `json:"mode,omitempty"`
	ModifiedAt   time.Time           `json:"modified_at,omitempty"`
	Decision     filepolicy.Decision `json:"decision"`
}

type SafeAction struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	Risk        string `json:"risk,omitempty"`
}

type AttentionState struct {
	SchemaVersion string                          `json:"schema_version"`
	UpdatedAt     time.Time                       `json:"updated_at"`
	PendingItems  map[string]PendingAttentionItem `json:"pending_items,omitempty"`
}

type PendingAttentionItem struct {
	RelativePath string    `json:"relative_path"`
	Fingerprint  string    `json:"fingerprint"`
	Status       string    `json:"status"`
	Note         string    `json:"note,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AcknowledgePendingInput struct {
	RootPath       string
	LaneRelPath    string
	StateRelPath   string
	StatePath      string
	RelativePath   string
	Note           string
	Now            func() time.Time
	LookupPath     func(string) (string, error)
	SSHConfigCheck func(string) error
}

type AcknowledgeTransferInput struct {
	RootPath     string
	StateRelPath string
	StatePath    string
	BatchID      string
	Status       string
	Note         string
	Now          func() time.Time
}

type AttentionResult struct {
	TargetKind      string     `json:"target_kind"`
	TargetRef       string     `json:"target_ref"`
	Status          string     `json:"status"`
	AttentionStatus string     `json:"attention_status"`
	Note            string     `json:"note,omitempty"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FileCount       int        `json:"file_count,omitempty"`
	DirCount        int        `json:"dir_count,omitempty"`
	Bytes           int64      `json:"bytes,omitempty"`
	ModifiedAt      *time.Time `json:"modified_at,omitempty"`
}

type Progress struct {
	Observed           bool       `json:"observed,omitempty"`
	Percent            float64    `json:"percent,omitempty"`
	BytesTransferred   int64      `json:"bytes_transferred,omitempty"`
	TotalBytes         int64      `json:"total_bytes,omitempty"`
	RateBytesPerSecond float64    `json:"rate_bytes_per_second,omitempty"`
	ETASeconds         int64      `json:"eta_seconds,omitempty"`
	Raw                string     `json:"raw,omitempty"`
	UpdatedAt          *time.Time `json:"updated_at,omitempty"`
}

type PreflightStatus struct {
	Status          string       `json:"status"`
	MainHost        string       `json:"main_host"`
	RsyncPath       string       `json:"rsync_path,omitempty"`
	SSHPath         string       `json:"ssh_path,omitempty"`
	SSHConfigStatus string       `json:"ssh_config_status"`
	Diagnostics     []Diagnostic `json:"diagnostics,omitempty"`
}

type Diagnostic struct {
	Severity   string `json:"severity"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Path       string `json:"path,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

type AcceptInput struct {
	AcceptedPath              string                       `json:"accepted_path"`
	RemoteRoot                string                       `json:"remote_root,omitempty"`
	TrustedRemoteRoot         bool                         `json:"-"`
	ImportsRoot               string                       `json:"imports_root,omitempty"`
	SourceNodeKey             string                       `json:"source_node_key"`
	SourceBoxID               string                       `json:"source_box_id,omitempty"`
	BatchID                   string                       `json:"batch_id"`
	AcceptedDate              string                       `json:"accepted_date,omitempty"`
	CustodyNodeKey            string                       `json:"custody_node_key,omitempty"`
	AcceptedAt                time.Time                    `json:"accepted_at,omitempty"`
	RebuildExport             bool                         `json:"rebuild_export,omitempty"`
	SkipAppleDouble           bool                         `json:"skip_apple_double,omitempty"`
	AllowCrossDevicePromotion bool                         `json:"allow_cross_device_promotion,omitempty"`
	DeviceID                  func(string) (uint64, error) `json:"-"`
	AvailableBytes            func(string) (int64, error)  `json:"-"`
	Rename                    func(string, string) error   `json:"-"`
	BeforeCopyEntry           func(int, string) error      `json:"-"`
}

type AcceptResult struct {
	SourceNodeKey            string    `json:"source_node_key"`
	SourceBoxID              string    `json:"source_box_id,omitempty"`
	BatchID                  string    `json:"batch_id"`
	AcceptedPath             string    `json:"accepted_path"`
	VisibleStoragePath       string    `json:"visible_storage_path"`
	FilesCataloged           int       `json:"files_cataloged"`
	FilesSkipped             int       `json:"files_skipped"`
	DirectoriesObserved      int       `json:"directories_observed,omitempty"`
	ObservationsRecorded     int       `json:"observations_recorded,omitempty"`
	TotalBytes               int64     `json:"total_bytes"`
	AcceptedAt               time.Time `json:"accepted_at"`
	StorageEntryIDs          []string  `json:"storage_entry_ids,omitempty"`
	StorageEntryIDsTruncated bool      `json:"storage_entry_ids_truncated,omitempty"`
	PromotionMethod          string    `json:"promotion_method"`
	SourceFilesystemID       uint64    `json:"source_filesystem_id,omitempty"`
	DestinationFilesystemID  uint64    `json:"destination_filesystem_id,omitempty"`
	PromotionIdempotent      bool      `json:"promotion_idempotent,omitempty"`
	StagingCleanupState      string    `json:"staging_cleanup_state,omitempty"`
	Metrics                  Metrics   `json:"metrics,omitempty"`
}

type SendInput struct {
	RootPath                  string
	LaneRelPath               string
	StateRelPath              string
	StatePath                 string
	SourceNodeKey             string
	SourceBoxID               string
	MainHost                  string
	RemoteRoot                string
	DryRun                    bool
	KeepLocal                 bool
	AllowCrossDevicePromotion bool
	Resume                    bool
	Now                       func() time.Time
	NewBatchID                func(time.Time) string
	LookupPath                func(string) (string, error)
	SSHConfigCheck            func(string) error
	Runner                    CommandRunner
	Profile                   filepolicy.Profile
	ExpectedPolicyFingerprint string
	RequestedTransport        TransportMode
	AvailableBytes            func(string) (int64, error)
	BeforeBundleEntry         func(int, TransferEntry) error
	BeforeCleanupEntry        func(int, TransferEntry) error
	AfterCleanupStage         func(int, TransferEntry, string) error
	AfterCompletedRecord      func(BatchRecord) error
	remoteReceiverCommand     string
	remoteReceiverUser        string
	remoteReceiverSwitch      bool
}

type HousekeepingInput struct {
	RootPath       string
	StateRelPath   string
	StatePath      string
	CurrentBatchID string
	Now            func() time.Time
}

type HousekeepingResult struct {
	Status                          string          `json:"status"`
	InspectedAt                     time.Time       `json:"inspected_at"`
	RemovedSafetyArtifactBatches    []string        `json:"removed_safety_artifact_batches,omitempty"`
	RemovedCleanupQuarantineBatches []string        `json:"removed_cleanup_quarantine_batches,omitempty"`
	UpdatedBatchRecords             []string        `json:"updated_batch_records,omitempty"`
	RecoveryStorage                 RecoveryStorage `json:"recovery_storage"`
}

type PublishInput struct {
	RootPath     string
	LaneRelPath  string
	StateRelPath string
	StatePath    string
	BatchID      string
	MainHost     string
	RemoteRoot   string
	DryRun       bool
	Now          func() time.Time
	Runner       CommandRunner
}

type SendResult struct {
	BatchID                             string               `json:"batch_id"`
	Status                              string               `json:"status"`
	DryRun                              bool                 `json:"dry_run"`
	SourceNodeKey                       string               `json:"source_node_key"`
	SourceBoxID                         string               `json:"source_box_id,omitempty"`
	VisibleStoragePath                  string               `json:"visible_storage_path"`
	LanePath                            string               `json:"lane_path"`
	LocalSafetyPath                     string               `json:"local_safety_path,omitempty"`
	RemoteStagingPath                   string               `json:"remote_staging_path"`
	RemoteRuntimeRoot                   string               `json:"remote_runtime_root,omitempty"`
	RemoteReceiverUser                  string               `json:"remote_receiver_user,omitempty"`
	RemoteReceiverSwitchRequired        bool                 `json:"remote_receiver_switch_required,omitempty"`
	RemoteAcceptedPath                  string               `json:"remote_accepted_path"`
	PendingItems                        int                  `json:"pending_items"`
	FileCount                           int                  `json:"file_count"`
	TotalBytes                          int64                `json:"total_bytes"`
	RemovedLocalItems                   []string             `json:"removed_local_items,omitempty"`
	Commands                            []CommandSummary     `json:"commands,omitempty"`
	StartedAt                           time.Time            `json:"started_at"`
	CompletedAt                         *time.Time           `json:"completed_at,omitempty"`
	ErrorMessage                        string               `json:"error_message,omitempty"`
	Progress                            Progress             `json:"progress,omitempty"`
	Metrics                             Metrics              `json:"metrics,omitempty"`
	Profile                             filepolicy.Profile   `json:"profile"`
	PolicyVersion                       string               `json:"policy_version"`
	PolicyFingerprint                   string               `json:"policy_fingerprint,omitempty"`
	InventoryHash                       string               `json:"inventory_hash,omitempty"`
	PolicyHashes                        map[string]string    `json:"policy_hashes,omitempty"`
	IgnoredFileCount                    int                  `json:"ignored_file_count,omitempty"`
	IgnoredBytes                        int64                `json:"ignored_bytes,omitempty"`
	Warnings                            []string             `json:"warnings,omitempty"`
	RequestedTransport                  TransportMode        `json:"requested_transport"`
	SelectedTransport                   TransportMode        `json:"selected_transport"`
	TransportReason                     string               `json:"transport_reason"`
	Transport                           BundleRecommendation `json:"transport"`
	LocalCleanupIntent                  string               `json:"local_cleanup_intent,omitempty"`
	AllowCrossDevicePromotion           bool                 `json:"allow_cross_device_promotion,omitempty"`
	BundleArtifactPath                  string               `json:"bundle_artifact_path,omitempty"`
	BundleArchivePath                   string               `json:"bundle_archive_path,omitempty"`
	BundleManifestPath                  string               `json:"bundle_manifest_path,omitempty"`
	BundleArchiveSHA256                 string               `json:"bundle_archive_sha256,omitempty"`
	BundleManifestSHA256                string               `json:"bundle_manifest_sha256,omitempty"`
	BundleArchiveBytes                  int64                `json:"bundle_archive_bytes,omitempty"`
	BundleCleanupState                  string               `json:"bundle_cleanup_state,omitempty"`
	BundleResumed                       bool                 `json:"bundle_resumed,omitempty"`
	LocalSafetyCleanupState             string               `json:"local_safety_cleanup_state,omitempty"`
	LocalSafetyRemovedAt                *time.Time           `json:"local_safety_removed_at,omitempty"`
	LocalSafetyRemovalReason            string               `json:"local_safety_removal_reason,omitempty"`
	LocalCleanupQuarantinePath          string               `json:"local_cleanup_quarantine_path,omitempty"`
	LocalCleanupQuarantineState         string               `json:"local_cleanup_quarantine_state,omitempty"`
	LocalCleanupQuarantineBytes         int64                `json:"local_cleanup_quarantine_bytes,omitempty"`
	LocalCleanupQuarantineExpiresAt     *time.Time           `json:"local_cleanup_quarantine_expires_at,omitempty"`
	LocalCleanupQuarantineRemovedAt     *time.Time           `json:"local_cleanup_quarantine_removed_at,omitempty"`
	LocalCleanupQuarantineRemovalReason string               `json:"local_cleanup_quarantine_removal_reason,omitempty"`
	QuarantinedLocalItems               []string             `json:"quarantined_local_items,omitempty"`
	RestoredLocalItems                  []string             `json:"restored_local_items,omitempty"`
	RecoveryStorage                     RecoveryStorage      `json:"recovery_storage"`
}

type PublishResult struct {
	BatchID                     string           `json:"batch_id"`
	Status                      string           `json:"status"`
	DryRun                      bool             `json:"dry_run,omitempty"`
	SourceNodeKey               string           `json:"source_node_key,omitempty"`
	SourceBoxID                 string           `json:"source_box_id,omitempty"`
	VisibleStoragePath          string           `json:"visible_storage_path,omitempty"`
	RemoteAcceptedPath          string           `json:"remote_accepted_path,omitempty"`
	Commands                    []CommandSummary `json:"commands,omitempty"`
	StartedAt                   time.Time        `json:"started_at"`
	CompletedAt                 *time.Time       `json:"completed_at,omitempty"`
	ErrorMessage                string           `json:"error_message,omitempty"`
	Warnings                    []string         `json:"warnings,omitempty"`
	Metrics                     Metrics          `json:"metrics,omitempty"`
	LocalCleanupQuarantinePath  string           `json:"local_cleanup_quarantine_path,omitempty"`
	LocalCleanupQuarantineState string           `json:"local_cleanup_quarantine_state,omitempty"`
	QuarantinedLocalItems       []string         `json:"quarantined_local_items,omitempty"`
	RestoredLocalItems          []string         `json:"restored_local_items,omitempty"`
	RemovedLocalItems           []string         `json:"removed_local_items,omitempty"`
}

type Metrics struct {
	PreflightDurationMS        int64 `json:"preflight_duration_ms,omitempty"`
	PendingScanDurationMS      int64 `json:"pending_scan_duration_ms,omitempty"`
	SafetyCopyDurationMS       int64 `json:"safety_copy_duration_ms,omitempty"`
	BundleCreateDurationMS     int64 `json:"bundle_create_duration_ms,omitempty"`
	BundleVerifyDurationMS     int64 `json:"bundle_verify_duration_ms,omitempty"`
	BundleArchiveBytes         int64 `json:"bundle_archive_bytes,omitempty"`
	RemotePrepareDurationMS    int64 `json:"remote_prepare_duration_ms,omitempty"`
	RsyncDurationMS            int64 `json:"rsync_duration_ms,omitempty"`
	RsyncRateBytesPerSecond    int64 `json:"rsync_rate_bytes_per_second,omitempty"`
	RsyncETASeconds            int64 `json:"rsync_eta_seconds,omitempty"`
	RemoteAcceptDurationMS     int64 `json:"remote_accept_duration_ms,omitempty"`
	CatalogDurationMS          int64 `json:"catalog_duration_ms,omitempty"`
	ExportRebuildDurationMS    int64 `json:"export_rebuild_duration_ms,omitempty"`
	ExportRefreshDurationMS    int64 `json:"export_refresh_duration_ms,omitempty"`
	LocalCleanupDurationMS     int64 `json:"local_cleanup_duration_ms,omitempty"`
	AcceptedScanDurationMS     int64 `json:"accepted_scan_duration_ms,omitempty"`
	AcceptedHashDurationMS     int64 `json:"accepted_hash_duration_ms,omitempty"`
	AcceptedHashOperations     int   `json:"accepted_hash_operations,omitempty"`
	AcceptedRegisterDurationMS int64 `json:"accepted_register_duration_ms,omitempty"`
	AcceptedRegisterOperations int   `json:"accepted_register_operations,omitempty"`
	TotalDurationMS            int64 `json:"total_duration_ms,omitempty"`
}

type CommandSummary struct {
	Name            string   `json:"name"`
	Args            []string `json:"args,omitempty"`
	Status          string   `json:"status"`
	Stdout          string   `json:"stdout,omitempty"`
	Stderr          string   `json:"stderr,omitempty"`
	StdoutBytes     int64    `json:"stdout_bytes,omitempty"`
	StderrBytes     int64    `json:"stderr_bytes,omitempty"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	StderrTruncated bool     `json:"stderr_truncated,omitempty"`
	OutputTruncated bool     `json:"output_truncated,omitempty"`
}

type CommandRunner func(ctx context.Context, name string, args ...string) (stdout string, stderr string, err error)
