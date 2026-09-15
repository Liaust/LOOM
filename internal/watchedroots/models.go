package watchedroots

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/nodes"
)

const (
	StatusUnknown  = "unknown"
	StatusHealthy  = "healthy"
	StatusDegraded = "degraded"
	StatusBlocked  = "blocked"

	FindingSeverityInfo     = "info"
	FindingSeverityWarning  = "warning"
	FindingSeverityError    = "error"
	FindingSeverityCritical = "critical"

	FindingStatusOpen     = "open"
	FindingStatusResolved = "resolved"
	FindingStatusIgnored  = "ignored"

	BackupBatchStatusAccepted  = "accepted"
	BackupBatchStatusPartial   = "partial"
	BackupBatchStatusFailed    = "failed"
	BackupBatchKindWatchedRoot = "watched_root_backup"

	BackupItemStatusAccepted  = "accepted"
	BackupItemStatusDuplicate = "duplicate"
	BackupItemStatusSkipped   = "skipped"
	BackupItemStatusFailed    = "failed"

	BackupItemKindFile           = "file"
	BackupItemKindMetadata       = "metadata"
	BackupItemKindDirectory      = "directory"
	BackupItemKindDeletionMarker = "deletion_marker"

	BackupArtifactKindPrivateBackupOperation = "private_backup_operation"
	BackupArtifactKindFileTransfer           = "file_transfer"
)

type ReportInput struct {
	NodeRef                string          `json:"node_ref"`
	CredentialToken        string          `json:"credential_token,omitempty"`
	RootKey                string          `json:"root_key"`
	WorkerKey              string          `json:"worker_key,omitempty"`
	DisplayName            string          `json:"display_name,omitempty"`
	SafeRootKey            string          `json:"safe_root_key,omitempty"`
	Status                 string          `json:"status,omitempty"`
	ConfigHash             string          `json:"config_hash,omitempty"`
	ConfigJSON             json.RawMessage `json:"config_json,omitempty"`
	SummaryJSON            json.RawMessage `json:"summary_json,omitempty"`
	Findings               []FindingInput  `json:"findings,omitempty"`
	ResolveMissingFindings bool            `json:"resolve_missing_findings,omitempty"`
	Metadata               json.RawMessage `json:"metadata,omitempty"`
}

type FindingInput struct {
	FindingKey   string          `json:"finding_key,omitempty"`
	Severity     string          `json:"severity,omitempty"`
	Status       string          `json:"status,omitempty"`
	Kind         string          `json:"kind"`
	RelativePath string          `json:"relative_path,omitempty"`
	Summary      string          `json:"summary,omitempty"`
	DetailsJSON  json.RawMessage `json:"details_json,omitempty"`
	FirstSeenAt  *time.Time      `json:"first_seen_at,omitempty"`
	LastSeenAt   *time.Time      `json:"last_seen_at,omitempty"`
}

type ReportResult struct {
	Root          WatchedRoot `json:"root"`
	Findings      []Finding   `json:"findings,omitempty"`
	ResolvedCount int         `json:"resolved_count,omitempty"`
}

type BackupBatchInput struct {
	NodeRef         string                 `json:"node_ref"`
	CredentialToken string                 `json:"credential_token,omitempty"`
	IdempotencyKey  string                 `json:"idempotency_key,omitempty"`
	RootKey         string                 `json:"root_key"`
	WorkerKey       string                 `json:"worker_key,omitempty"`
	BatchKind       string                 `json:"batch_kind"`
	BackupMode      string                 `json:"backup_mode"`
	Items           []BackupBatchItemInput `json:"items"`
	Metadata        json.RawMessage        `json:"metadata,omitempty"`
}

type BackupBatchItemInput struct {
	LocalItemRef             string          `json:"local_item_ref,omitempty"`
	ItemKind                 string          `json:"item_kind"`
	Status                   string          `json:"status,omitempty"`
	BackupMode               string          `json:"backup_mode,omitempty"`
	RelativePath             string          `json:"relative_path,omitempty"`
	ContentHashURI           string          `json:"content_hash_uri,omitempty"`
	PreviousHashURI          string          `json:"previous_hash_uri,omitempty"`
	SizeBytes                int64           `json:"size_bytes,omitempty"`
	ModifiedAt               *time.Time      `json:"modified_at,omitempty"`
	DeletedAt                *time.Time      `json:"deleted_at,omitempty"`
	ArtifactKind             string          `json:"artifact_kind,omitempty"`
	ArtifactRef              string          `json:"artifact_ref,omitempty"`
	PrivateBackupOperationID string          `json:"private_backup_operation_id,omitempty"`
	ErrorCode                string          `json:"error_code,omitempty"`
	ErrorMessage             string          `json:"error_message,omitempty"`
	Metadata                 json.RawMessage `json:"metadata,omitempty"`
}

type BackupBatchResult struct {
	Root  WatchedRoot  `json:"root"`
	Batch BackupBatch  `json:"batch"`
	Items []BackupItem `json:"items,omitempty"`
}

type RootStatus struct {
	Root           WatchedRoot `json:"root"`
	LatestFindings []Finding   `json:"latest_findings,omitempty"`
}

// LatestEvidence is the bounded lifecycle read used by protected-folder
// projection. It intentionally contains no payload or deployment lifecycle.
type LatestEvidence struct {
	Root         WatchedRoot  `json:"root"`
	OpenFindings int          `json:"open_findings"`
	LatestBackup *BackupBatch `json:"latest_backup,omitempty"`
}

type BackupStatus struct {
	Root                WatchedRoot  `json:"root"`
	Status              string       `json:"status"`
	LatestBatch         *BackupBatch `json:"latest_batch,omitempty"`
	BatchCount          int          `json:"batch_count"`
	ItemCount           int          `json:"item_count"`
	AcceptedCount       int          `json:"accepted_count"`
	DuplicateCount      int          `json:"duplicate_count"`
	SkippedCount        int          `json:"skipped_count"`
	FailedCount         int          `json:"failed_count"`
	ArtifactCount       int          `json:"artifact_count"`
	DeletionMarkerCount int          `json:"deletion_marker_count"`
	TotalBytes          int64        `json:"total_bytes"`
	LatestFindings      []Finding    `json:"latest_findings,omitempty"`
}

type WatchedRoot struct {
	WatchedRootID  string          `json:"watched_root_id"`
	NodeID         string          `json:"node_id"`
	RootKey        string          `json:"root_key"`
	WorkerKey      string          `json:"worker_key"`
	DisplayName    string          `json:"display_name"`
	SafeRootKey    string          `json:"safe_root_key"`
	Status         string          `json:"status"`
	ConfigHash     string          `json:"config_hash"`
	ConfigJSON     json.RawMessage `json:"config_json"`
	SummaryJSON    json.RawMessage `json:"summary_json"`
	Metadata       json.RawMessage `json:"metadata"`
	LastReportedAt time.Time       `json:"last_reported_at"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	Node           *nodes.Node     `json:"node,omitempty"`
}

type Finding struct {
	WatchedRootFindingID string          `json:"watched_root_finding_id"`
	WatchedRootID        string          `json:"watched_root_id"`
	NodeID               string          `json:"node_id"`
	RootKey              string          `json:"root_key"`
	FindingKey           string          `json:"finding_key"`
	Severity             string          `json:"severity"`
	Status               string          `json:"status"`
	Kind                 string          `json:"kind"`
	RelativePath         string          `json:"relative_path,omitempty"`
	Summary              string          `json:"summary"`
	DetailsJSON          json.RawMessage `json:"details_json"`
	FirstSeenAt          time.Time       `json:"first_seen_at"`
	LastSeenAt           time.Time       `json:"last_seen_at"`
	ResolvedAt           *time.Time      `json:"resolved_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type BackupBatch struct {
	WatchedRootBackupBatchID string          `json:"watched_root_backup_batch_id"`
	WatchedRootID            string          `json:"watched_root_id"`
	NodeID                   string          `json:"node_id"`
	RootKey                  string          `json:"root_key"`
	WorkerKey                string          `json:"worker_key"`
	IdempotencyKey           *string         `json:"idempotency_key,omitempty"`
	BatchKind                string          `json:"batch_kind"`
	BackupMode               string          `json:"backup_mode"`
	Status                   string          `json:"status"`
	ItemCount                int             `json:"item_count"`
	AcceptedCount            int             `json:"accepted_count"`
	DuplicateCount           int             `json:"duplicate_count"`
	SkippedCount             int             `json:"skipped_count"`
	FailedCount              int             `json:"failed_count"`
	ArtifactCount            int             `json:"artifact_count"`
	DeletionMarkerCount      int             `json:"deletion_marker_count"`
	TotalBytes               int64           `json:"total_bytes"`
	ReceivedAt               time.Time       `json:"received_at"`
	CompletedAt              *time.Time      `json:"completed_at,omitempty"`
	Metadata                 json.RawMessage `json:"metadata"`
}

type BackupItem struct {
	WatchedRootBackupItemID  string          `json:"watched_root_backup_item_id"`
	WatchedRootBackupBatchID string          `json:"watched_root_backup_batch_id"`
	WatchedRootID            string          `json:"watched_root_id"`
	NodeID                   string          `json:"node_id"`
	RootKey                  string          `json:"root_key"`
	LocalItemRef             string          `json:"local_item_ref,omitempty"`
	ItemKind                 string          `json:"item_kind"`
	Status                   string          `json:"status"`
	BackupMode               string          `json:"backup_mode"`
	RelativePath             string          `json:"relative_path,omitempty"`
	ContentHashURI           string          `json:"content_hash_uri,omitempty"`
	PreviousHashURI          string          `json:"previous_hash_uri,omitempty"`
	SizeBytes                int64           `json:"size_bytes,omitempty"`
	ModifiedAt               *time.Time      `json:"modified_at,omitempty"`
	DeletedAt                *time.Time      `json:"deleted_at,omitempty"`
	ArtifactKind             string          `json:"artifact_kind,omitempty"`
	ArtifactRef              string          `json:"artifact_ref,omitempty"`
	PrivateBackupOperationID *string         `json:"private_backup_operation_id,omitempty"`
	ErrorCode                string          `json:"error_code,omitempty"`
	ErrorMessage             string          `json:"error_message,omitempty"`
	Metadata                 json.RawMessage `json:"metadata"`
	CreatedAt                time.Time       `json:"created_at"`
}

type StatusFilter struct {
	NodeRef    string `json:"node_ref,omitempty"`
	RootKey    string `json:"root_key,omitempty"`
	ProjectRef string `json:"project_ref,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type FindingFilter struct {
	NodeRef    string `json:"node_ref,omitempty"`
	RootKey    string `json:"root_key,omitempty"`
	ProjectRef string `json:"project_ref,omitempty"`
	Status     string `json:"status,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type BackupFilter struct {
	NodeRef    string `json:"node_ref,omitempty"`
	RootKey    string `json:"root_key,omitempty"`
	ProjectRef string `json:"project_ref,omitempty"`
	Status     string `json:"status,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type BackupItemFilter struct {
	NodeRef    string `json:"node_ref,omitempty"`
	RootKey    string `json:"root_key,omitempty"`
	ProjectRef string `json:"project_ref,omitempty"`
	Status     string `json:"status,omitempty"`
	BatchRef   string `json:"batch_ref,omitempty"`
	Path       string `json:"path,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}
