package knowledge

import (
	"encoding/json"
	"time"
)

// Custody never replaces historical source or citation identity.
type NotesCustodyContext struct {
	SourceLifecycle           SourceLifecycle `json:"source_lifecycle"`
	OriginalPath              string          `json:"original_path"`
	CanonicalPath             string          `json:"canonical_path"`
	WorkspaceKind             string          `json:"workspace_kind,omitempty"`
	WorkspaceObjectID         string          `json:"workspace_object_id,omitempty"`
	ArchiveOperationID        string          `json:"archive_operation_id,omitempty"`
	WorkspaceLifecycleEventID string          `json:"workspace_lifecycle_event_id,omitempty"`
	ArchivedAt                *time.Time      `json:"archived_at,omitempty"`
	OccurredAt                *time.Time      `json:"occurred_at,omitempty"`
}

const (
	RootKindBoxNotes        = "box_notes"
	RootKindProjectNotes    = "project_notes"
	RootKindBoxTopics       = "box_topics"
	RootKindBoxLibrary      = "box_library"
	RootKindProjectMaterial = "project_material"
)

const (
	SourceRootStatusActive   = "active"
	SourceRootStatusStale    = "stale"
	SourceRootStatusDisabled = "disabled"
	SourceRootStatusDeleted  = "deleted"
	SourceRootStatusBlocked  = "blocked"
)

const (
	ProcessingStateMetadataOnly  = "metadata_only"
	ProcessingStateTextExtracted = "text_extracted"
	ProcessingStateChunked       = "chunked"
	ProcessingStateIndexed       = "indexed"
	ProcessingStateEmbedded      = "embedded"
	ProcessingStateFailed        = "failed"
	ProcessingStateStale         = "stale"
	ProcessingStateDeleted       = "deleted"
)

const (
	ChunkStatusCreated          = "created"
	ChunkStatusIndexed          = "indexed"
	ChunkStatusStale            = "stale"
	ChunkStatusFailed           = "failed"
	ChunkStatusDisabledByPolicy = "disabled_by_policy"
)

const (
	PipelineStageMetadata       = "metadata"
	PipelineStageTextExtraction = "text_extraction"
	PipelineStageChunking       = "chunking"
	PipelineStageBM25           = "bm25"
	PipelineStageProjection     = "projection"
	PipelineStageEmbedding      = "embedding"
)

const (
	PipelineStatusNotStarted         = "not_started"
	PipelineStatusQueued             = "queued"
	PipelineStatusProcessing         = "processing"
	PipelineStatusComplete           = "complete"
	PipelineStatusStale              = "stale"
	PipelineStatusFailed             = "failed"
	PipelineStatusDisabledByPolicy   = "disabled_by_policy"
	PipelineStatusSkippedUnsupported = "skipped_unsupported"
)

const (
	LinkKindMarkdown = "markdown"
	LinkKindWikilink = "wikilink"
	LinkKindURL      = "url"
	LinkKindFile     = "file"
	LinkKindUnknown  = "unknown"
)

const (
	LinkStatusUnresolved = "unresolved"
	LinkStatusResolved   = "resolved"
	LinkStatusBroken     = "broken"
	LinkStatusIgnored    = "ignored"
)

const (
	EmbeddingSettingsID               = "notes_embeddings"
	EmbeddingRuntimeOllama            = "ollama"
	EmbeddingModelMXBAIEmbedLarge     = "mxbai-embed-large"
	EmbeddingDistanceCosine           = "cosine"
	DefaultEmbeddingDimensions        = 1024
	DefaultEmbeddingQuietWindowSec    = 600
	DefaultEmbeddingConcurrency       = 1
	DefaultEmbeddingHistoryPerLineage = 5
)

const (
	EmbeddingObjectStatusNotStarted         = "not_started"
	EmbeddingObjectStatusQueued             = "queued"
	EmbeddingObjectStatusProcessing         = "processing"
	EmbeddingObjectStatusComplete           = "complete"
	EmbeddingObjectStatusStale              = "stale"
	EmbeddingObjectStatusFailed             = "failed"
	EmbeddingObjectStatusDisabledByPolicy   = "disabled_by_policy"
	EmbeddingObjectStatusSkippedUnsupported = "skipped_unsupported"
)

const (
	ChunkEmbeddingStatusActive     = "active"
	ChunkEmbeddingStatusHistorical = "historical"
	ChunkEmbeddingStatusReusable   = "reusable"
	ChunkEmbeddingStatusStale      = "stale"
	ChunkEmbeddingStatusFailed     = "failed"
)

const (
	EmbeddingWorkStatusQueued             = "queued"
	EmbeddingWorkStatusProcessing         = "processing"
	EmbeddingWorkStatusComplete           = "complete"
	EmbeddingWorkStatusStale              = "stale"
	EmbeddingWorkStatusFailed             = "failed"
	EmbeddingWorkStatusDisabledByPolicy   = "disabled_by_policy"
	EmbeddingWorkStatusSkippedUnsupported = "skipped_unsupported"
)

const (
	NotesSearchModeLexical  = "lexical"
	NotesSearchModeSemantic = "semantic"
	NotesSearchModeHybrid   = "hybrid"
)

const (
	AbsoluteTimeKindCreated  = "created"
	AbsoluteTimeKindModified = "modified"
	AbsoluteTimeKindObserved = "observed"
)

const (
	AbsoluteTimeBasisSourceFilesystemMtime     = "source_filesystem_mtime"
	AbsoluteTimeBasisSourceFilesystemBirthtime = "source_filesystem_birthtime"
	AbsoluteTimeBasisFrontmatterUpdatedAt      = "frontmatter_updated_at"
	AbsoluteTimeBasisFrontmatterCreatedAt      = "frontmatter_created_at"
	AbsoluteTimeBasisEmbeddedModifiedAt        = "embedded_modified_at"
	AbsoluteTimeBasisEmbeddedCreatedAt         = "embedded_created_at"
	AbsoluteTimeBasisSourceObjectMetadata      = "source_object_metadata"
	AbsoluteTimeBasisObservedAtFallback        = "observed_at_fallback"
)

const AbsoluteTimeWarningInvalidTimestamp = "timestamp.invalid"

var emptyJSONObject = json.RawMessage(`{}`)

type AbsoluteTimeCandidate struct {
	Kind      string     `json:"kind"`
	Basis     string     `json:"basis"`
	RawValue  string     `json:"raw_value"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

type AbsoluteTimeWarning struct {
	Code     string `json:"code"`
	Basis    string `json:"basis"`
	RawValue string `json:"raw_value"`
	Message  string `json:"message"`
}

type AbsoluteTime struct {
	SourceCreatedAt  *time.Time              `json:"source_created_at,omitempty"`
	SourceModifiedAt *time.Time              `json:"source_modified_at,omitempty"`
	RecencyAt        time.Time               `json:"recency_at"`
	RecencyBasis     string                  `json:"recency_basis"`
	Candidates       []AbsoluteTimeCandidate `json:"candidates"`
	Warnings         []AbsoluteTimeWarning   `json:"warnings,omitempty"`
}

type SourceRoot struct {
	SourceContext
	LifecycleCounts                  *NotesLifecycleCounts `json:"lifecycle_counts,omitempty"`
	NotesSourceRootID                string                `json:"notes_source_root_id"`
	RootKind                         string                `json:"root_kind"`
	NodeID                           *string               `json:"node_id,omitempty"`
	NodeKey                          string                `json:"node_key,omitempty"`
	ProjectID                        *string               `json:"project_id,omitempty"`
	BoxWatchRootRegistrationID       *string               `json:"box_watch_root_registration_id,omitempty"`
	ProjectWatchedRootRegistrationID *string               `json:"project_watched_root_registration_id,omitempty"`
	BackendRootKey                   string                `json:"backend_root_key"`
	DisplayName                      string                `json:"display_name,omitempty"`
	SourcePath                       string                `json:"source_path,omitempty"`
	RootRelativePath                 string                `json:"root_relative_path,omitempty"`
	Status                           string                `json:"status"`
	AuthorizationMetadata            json.RawMessage       `json:"authorization_metadata"`
	Metadata                         json.RawMessage       `json:"metadata"`
	CreatedAt                        time.Time             `json:"created_at"`
	UpdatedAt                        time.Time             `json:"updated_at"`
}

type KnowledgeObject struct {
	SourceContext
	*NotesCustodyContext
	KnowledgeObjectID    string          `json:"knowledge_object_id"`
	NotesSourceRootID    string          `json:"notes_source_root_id"`
	StorageEntryID       *string         `json:"storage_entry_id,omitempty"`
	SourceNodeID         *string         `json:"source_node_id,omitempty"`
	SourceNodeKey        string          `json:"source_node_key,omitempty"`
	ProjectID            *string         `json:"project_id,omitempty"`
	SourcePath           string          `json:"source_path,omitempty"`
	RelativePath         string          `json:"relative_path"`
	Title                string          `json:"title,omitempty"`
	FileClass            string          `json:"file_class"`
	MimeType             string          `json:"mime_type,omitempty"`
	SizeBytes            *int64          `json:"size_bytes,omitempty"`
	SourceHash           string          `json:"source_hash,omitempty"`
	SourceRevision       string          `json:"source_revision,omitempty"`
	ProcessingState      string          `json:"processing_state"`
	PipelineKey          string          `json:"pipeline_key,omitempty"`
	PipelineVersion      string          `json:"pipeline_version,omitempty"`
	SourceCreatedAt      *time.Time      `json:"source_created_at,omitempty"`
	SourceModifiedAt     *time.Time      `json:"source_modified_at,omitempty"`
	RecencyAt            time.Time       `json:"recency_at"`
	RecencyBasis         string          `json:"recency_basis"`
	AbsoluteTimeMetadata json.RawMessage `json:"absolute_time_metadata"`
	LastSeenAt           time.Time       `json:"last_seen_at"`
	LastProcessedAt      *time.Time      `json:"last_processed_at,omitempty"`
	LastErrorCode        string          `json:"last_error_code,omitempty"`
	LastErrorMessage     string          `json:"last_error_message,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	DeletedAt            *time.Time      `json:"deleted_at,omitempty"`
}

type KnowledgeObjectVersion struct {
	KnowledgeObjectVersionID string          `json:"knowledge_object_version_id"`
	KnowledgeObjectID        string          `json:"knowledge_object_id"`
	VersionNumber            int             `json:"version_number"`
	StorageEntryID           *string         `json:"storage_entry_id,omitempty"`
	SourceHash               string          `json:"source_hash,omitempty"`
	SourceRevision           string          `json:"source_revision,omitempty"`
	SizeBytes                *int64          `json:"size_bytes,omitempty"`
	MimeType                 string          `json:"mime_type,omitempty"`
	FileClass                string          `json:"file_class"`
	SourcePath               string          `json:"source_path,omitempty"`
	SourceCreatedAt          *time.Time      `json:"source_created_at,omitempty"`
	SourceModifiedAt         *time.Time      `json:"source_modified_at,omitempty"`
	RecencyAt                time.Time       `json:"recency_at"`
	RecencyBasis             string          `json:"recency_basis"`
	AbsoluteTimeMetadata     json.RawMessage `json:"absolute_time_metadata"`
	Metadata                 json.RawMessage `json:"metadata"`
	ObservedAt               time.Time       `json:"observed_at"`
	CreatedAt                time.Time       `json:"created_at"`
}

type KnowledgeChunk struct {
	KnowledgeChunkID         string          `json:"knowledge_chunk_id"`
	KnowledgeObjectID        string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID *string         `json:"knowledge_object_version_id,omitempty"`
	ChunkIndex               int             `json:"chunk_index"`
	ChunkText                string          `json:"chunk_text"`
	ChunkHash                string          `json:"chunk_hash"`
	StructuralPath           string          `json:"structural_path,omitempty"`
	StartOffset              *int            `json:"start_offset,omitempty"`
	EndOffset                *int            `json:"end_offset,omitempty"`
	TokenCountEstimate       *int            `json:"token_count_estimate,omitempty"`
	ChunkerVersion           string          `json:"chunker_version"`
	Status                   string          `json:"status"`
	Metadata                 json.RawMessage `json:"metadata"`
	CreatedAt                time.Time       `json:"created_at"`
	IndexedAt                *time.Time      `json:"indexed_at,omitempty"`
}

type PipelineStatus struct {
	KnowledgePipelineStatusID string          `json:"knowledge_pipeline_status_id"`
	KnowledgeObjectID         string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID  *string         `json:"knowledge_object_version_id,omitempty"`
	PipelineKey               string          `json:"pipeline_key"`
	PipelineVersion           string          `json:"pipeline_version"`
	Stage                     string          `json:"stage"`
	Status                    string          `json:"status"`
	QueuedAt                  *time.Time      `json:"queued_at,omitempty"`
	StartedAt                 *time.Time      `json:"started_at,omitempty"`
	CompletedAt               *time.Time      `json:"completed_at,omitempty"`
	FailedAt                  *time.Time      `json:"failed_at,omitempty"`
	LastErrorCode             string          `json:"last_error_code,omitempty"`
	LastErrorMessage          string          `json:"last_error_message,omitempty"`
	Metadata                  json.RawMessage `json:"metadata"`
	AttemptCount              int             `json:"attempt_count"`
	NextAttemptAt             *time.Time      `json:"next_attempt_at,omitempty"`
	ClaimedByWorkerRunID      string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimExpiresAt            *time.Time      `json:"claim_expires_at,omitempty"`
	Priority                  int             `json:"priority"`
	ManualActionRequired      bool            `json:"manual_action_required"`
	LastWorkerRunID           string          `json:"last_worker_run_id,omitempty"`
	LastAttemptAt             *time.Time      `json:"last_attempt_at,omitempty"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
}

type ObjectLink struct {
	KnowledgeObjectLinkID   string          `json:"knowledge_object_link_id"`
	SourceKnowledgeObjectID string          `json:"source_knowledge_object_id"`
	TargetKnowledgeObjectID *string         `json:"target_knowledge_object_id,omitempty"`
	ChunkID                 *string         `json:"chunk_id,omitempty"`
	LinkKind                string          `json:"link_kind"`
	RawTarget               string          `json:"raw_target"`
	NormalizedTarget        string          `json:"normalized_target,omitempty"`
	LinkText                string          `json:"link_text,omitempty"`
	Status                  string          `json:"status"`
	Metadata                json.RawMessage `json:"metadata"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}
