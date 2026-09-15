package sync

import (
	"encoding/json"
	"strings"
	"time"

	"loom.local/loom/internal/nodes"
)

const (
	ItemKindEvent           = "event"
	ItemKindObjectMetadata  = "object_metadata"
	ItemKindObjectBlob      = "object_blob"
	ItemKindPrivateBackup   = "private_backup"
	ItemKindDeletionRequest = "deletion_request"

	StreamEvents           = "events"
	StreamObjectMetadata   = "object_metadata"
	StreamObjectBlobs      = "object_blobs"
	StreamPrivateBackups   = "private_backups"
	StreamDeletionRequests = "deletion_requests"

	BatchKindEvents         = "events"
	BatchKindObjectMetadata = "object_metadata"
	BatchKindObjectBlobs    = "object_blobs"
	BatchKindMixed          = "mixed"

	BatchStatusReceived   = "received"
	BatchStatusAccepted   = "accepted"
	BatchStatusPartial    = "partial"
	BatchStatusConflicted = "conflicted"
	BatchStatusFailed     = "failed"

	ItemStatusAccepted   = "accepted"
	ItemStatusDuplicate  = "duplicate"
	ItemStatusConflicted = "conflicted"
	ItemStatusFailed     = "failed"

	ConflictDuplicatePayloadMismatch = "duplicate_payload_mismatch"
	ConflictSequenceGap              = "sequence_gap"
	ConflictObjectHashMismatch       = "object_hash_mismatch"
	ConflictUnsupportedItemKind      = "unsupported_item_kind"

	IndexPolicyNone           = "none"
	IndexPolicyMetadataOnly   = "metadata_only"
	IndexPolicyTextLater      = "text_later"
	IndexPolicySemanticLater  = "semantic_later"
	IndexPolicyPrivateNoIndex = "private_no_index"

	RawBackupPolicyNormal           = "normal"
	RawBackupPolicyPriority         = "priority"
	RawBackupPolicyRateLimited      = "rate_limited"
	RawBackupPolicyDelayed          = "delayed"
	RawBackupPolicyPrivateRawBackup = "private_raw_backup"
	RawBackupPolicyExcludedTemp     = "excluded_temp"

	MaxInlineObjectUploadBytes = 1024 * 1024
	MaxPrivateBackupBytes      = 1024 * 1024

	DeletionRequestStatusPendingReview = "pending_review"
	DeletionRequestStatusRecorded      = "recorded"
	DeletionRequestStatusApproved      = "approved"
	DeletionRequestStatusDenied        = "denied"
	DeletionRequestStatusCompleted     = "completed"
	DeletionRequestStatusActive        = "active"
	DeletionRequestStatusAll           = "all"
)

type PushBatchInput struct {
	NodeRef         string          `json:"node_ref"`
	CredentialToken string          `json:"credential_token,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	BatchKind       string          `json:"batch_kind,omitempty"`
	Items           []PushBatchItem `json:"items"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type PushBatchItem struct {
	LocalRef      string          `json:"local_ref"`
	ItemKind      string          `json:"item_kind"`
	StreamName    string          `json:"stream_name"`
	LocalSequence int64           `json:"local_sequence"`
	EventType     string          `json:"event_type,omitempty"`
	EventLevel    string          `json:"event_level,omitempty"`
	CreatedAt     *time.Time      `json:"created_at,omitempty"`
	PayloadJSON   json.RawMessage `json:"payload,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type PushBatchResult struct {
	Batch     SyncBatch        `json:"batch"`
	Items     []SyncItemResult `json:"items"`
	Cursors   []SyncCursor     `json:"cursors,omitempty"`
	Conflicts []SyncConflict   `json:"conflicts,omitempty"`
}

type SyncItemResult struct {
	SyncBatchItemID string          `json:"sync_batch_item_id"`
	LocalRef        string          `json:"local_ref"`
	ItemKind        string          `json:"item_kind"`
	StreamName      string          `json:"stream_name"`
	LocalSequence   int64           `json:"local_sequence"`
	Status          string          `json:"status"`
	GlobalRef       string          `json:"global_ref,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	PayloadHash     string          `json:"payload_hash"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type SyncBatch struct {
	SyncBatchID    string          `json:"sync_batch_id"`
	OriginNodeID   string          `json:"origin_node_id"`
	IdempotencyKey *string         `json:"idempotency_key,omitempty"`
	BatchKind      string          `json:"batch_kind"`
	Status         string          `json:"status"`
	ItemCount      int             `json:"item_count"`
	AcceptedCount  int             `json:"accepted_count"`
	ConflictCount  int             `json:"conflict_count"`
	FailedCount    int             `json:"failed_count"`
	CursorBefore   json.RawMessage `json:"cursor_before_json"`
	CursorAfter    json.RawMessage `json:"cursor_after_json"`
	ReceivedAt     time.Time       `json:"received_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty"`
	Metadata       json.RawMessage `json:"metadata"`
}

type SyncCursor struct {
	SyncCursorID             string          `json:"sync_cursor_id"`
	NodeID                   string          `json:"node_id"`
	StreamName               string          `json:"stream_name"`
	LastAcceptedSequence     int64           `json:"last_accepted_sequence"`
	LastAcceptedLocalEventID string          `json:"last_accepted_local_event_id"`
	LastBatchID              *string         `json:"last_batch_id,omitempty"`
	LastSuccessAt            *time.Time      `json:"last_success_at,omitempty"`
	LastErrorAt              *time.Time      `json:"last_error_at,omitempty"`
	LastErrorCode            string          `json:"last_error_code"`
	LastErrorMessage         string          `json:"last_error_message"`
	Metadata                 json.RawMessage `json:"metadata"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

type SyncConflict struct {
	SyncConflictID string          `json:"sync_conflict_id"`
	OriginNodeID   string          `json:"origin_node_id"`
	SyncBatchID    *string         `json:"sync_batch_id,omitempty"`
	LocalRef       string          `json:"local_ref"`
	ConflictType   string          `json:"conflict_type"`
	Status         string          `json:"status"`
	Summary        string          `json:"summary"`
	LocalPayload   json.RawMessage `json:"local_payload_json"`
	MainPayload    json.RawMessage `json:"main_payload_json"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      time.Time       `json:"created_at"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty"`
}

type SyncStatus struct {
	Node          nodes.Node     `json:"node"`
	Cursors       []SyncCursor   `json:"cursors"`
	RecentBatches []SyncBatch    `json:"recent_batches,omitempty"`
	OpenConflicts []SyncConflict `json:"open_conflicts,omitempty"`
	Replicas      []SyncReplica  `json:"replicas,omitempty"`
	Summary       SyncSummary    `json:"summary"`
}

type SyncSummary struct {
	CursorCount       int `json:"cursor_count"`
	RecentBatchCount  int `json:"recent_batch_count"`
	OpenConflictCount int `json:"open_conflict_count"`
	ReplicaCount      int `json:"replica_count"`
}

type ListFilter struct {
	NodeRef         string
	Status          string
	Limit           int
	ProjectRef      string
	ActiveOnly      bool
	IncludeResolved bool
}

type SyncedObjectInput struct {
	NodeRef              string          `json:"node_ref"`
	CredentialToken      string          `json:"credential_token,omitempty"`
	IdempotencyKey       string          `json:"idempotency_key,omitempty"`
	LocalObjectRef       string          `json:"local_object_ref"`
	LocalVersionRef      string          `json:"local_version_ref"`
	LocalSequence        int64           `json:"local_sequence"`
	ProjectRef           string          `json:"project_ref,omitempty"`
	ScopeRef             string          `json:"scope_ref,omitempty"`
	LogicalName          string          `json:"logical_name"`
	SourcePath           string          `json:"source_path"`
	SourceMtime          *time.Time      `json:"source_mtime,omitempty"`
	SourceMtimeBasis     string          `json:"source_mtime_basis,omitempty"`
	SourceCreatedAt      *time.Time      `json:"source_created_at,omitempty"`
	SourceCreatedBasis   string          `json:"source_created_basis,omitempty"`
	SizeBytes            int64           `json:"size_bytes"`
	MimeType             string          `json:"mime_type"`
	HashURI              string          `json:"hash_uri"`
	IndexPolicy          string          `json:"index_policy,omitempty"`
	RawBackupPolicy      string          `json:"raw_backup_policy,omitempty"`
	FileClass            string          `json:"file_class,omitempty"`
	ClassificationSource string          `json:"classification_source,omitempty"`
	IndexingState        string          `json:"indexing_state,omitempty"`
	IndexingReason       string          `json:"indexing_reason,omitempty"`
	ContentBase64        string          `json:"content_base64"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type SyncedObjectResult struct {
	Batch       SyncBatch       `json:"batch"`
	Item        SyncItemResult  `json:"item"`
	Conflict    *SyncConflict   `json:"conflict,omitempty"`
	Cursor      SyncCursor      `json:"cursor"`
	Replica     *SyncReplica    `json:"replica,omitempty"`
	ObjectID    string          `json:"object_id,omitempty"`
	VersionID   string          `json:"object_version_id,omitempty"`
	BlobID      string          `json:"blob_id,omitempty"`
	HashURI     string          `json:"hash_uri,omitempty"`
	IndexStatus string          `json:"index_status,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type SyncReplica struct {
	ReplicaID       string          `json:"replica_id"`
	ReplicatedKind  string          `json:"replicated_kind"`
	ReplicatedID    string          `json:"replicated_id"`
	SourceNodeID    string          `json:"source_node_id"`
	ReplicaNodeID   string          `json:"replica_node_id"`
	ReplicaMode     string          `json:"replica_mode"`
	FreshnessState  string          `json:"freshness_state"`
	SourceCursorRef string          `json:"source_cursor_ref"`
	StorageRef      string          `json:"storage_ref"`
	LastVerifiedAt  *time.Time      `json:"last_verified_at,omitempty"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type PrivateBackupInput struct {
	NodeRef         string          `json:"node_ref"`
	CredentialToken string          `json:"credential_token,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	PayloadBase64   string          `json:"payload_base64"`
	CoarseSizeBytes int64           `json:"coarse_size_bytes"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type PrivateBackupOperation struct {
	PrivateBackupOperationID string          `json:"private_backup_operation_id"`
	OriginNodeID             string          `json:"origin_node_id"`
	IdempotencyKey           *string         `json:"idempotency_key,omitempty"`
	Status                   string          `json:"status"`
	CoarseSizeBytes          int64           `json:"coarse_size_bytes"`
	StorageRef               string          `json:"storage_ref"`
	StartedAt                time.Time       `json:"started_at"`
	CompletedAt              *time.Time      `json:"completed_at,omitempty"`
	ErrorCode                string          `json:"error_code"`
	ErrorMessage             string          `json:"error_message"`
	Metadata                 json.RawMessage `json:"metadata"`
}

type PrivateBackupResult struct {
	Operation PrivateBackupOperation `json:"operation"`
}

type DeletionRequestInput struct {
	NodeRef         string          `json:"node_ref"`
	CredentialToken string          `json:"credential_token,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	TargetKind      string          `json:"target_kind"`
	TargetRef       string          `json:"target_ref"`
	RequestedAction string          `json:"requested_action,omitempty"`
	Reason          string          `json:"reason,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type DeletionRequest struct {
	DeletionRequestID string          `json:"deletion_request_id"`
	OriginNodeID      string          `json:"origin_node_id"`
	TargetKind        string          `json:"target_kind"`
	TargetRef         string          `json:"target_ref"`
	RequestedAction   string          `json:"requested_action"`
	Status            string          `json:"status"`
	Reason            string          `json:"reason"`
	RequestedAt       time.Time       `json:"requested_at"`
	ReviewedAt        *time.Time      `json:"reviewed_at,omitempty"`
	ReviewedByActorID *string         `json:"reviewed_by_actor_id,omitempty"`
	Metadata          json.RawMessage `json:"metadata"`
}

type DeletionRequestResult struct {
	Request DeletionRequest `json:"request"`
}

type DeletionRequestUpdateInput struct {
	RequestRef string `json:"request_ref"`
	Reason     string `json:"reason,omitempty"`
}

func NormalizeDeletionRequestStatus(status string) string {
	status = strings.ToLower(strings.TrimSpace(status))
	switch status {
	case "":
		return ""
	case "pending", "open", "requested":
		return DeletionRequestStatusPendingReview
	case DeletionRequestStatusPendingReview,
		DeletionRequestStatusRecorded,
		DeletionRequestStatusApproved,
		DeletionRequestStatusDenied,
		DeletionRequestStatusCompleted:
		return status
	default:
		return ""
	}
}

func ValidDeletionRequestStatus(status string) bool {
	return NormalizeDeletionRequestStatus(status) != ""
}

func DeletionRequestStatusIsActive(status string) bool {
	switch NormalizeDeletionRequestStatus(status) {
	case "", DeletionRequestStatusPendingReview, DeletionRequestStatusRecorded:
		return true
	default:
		return false
	}
}

func DeletionRequestIsActive(request DeletionRequest) bool {
	return DeletionRequestStatusIsActive(request.Status)
}
