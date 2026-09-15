package knowledge

import (
	"encoding/json"
	"time"
)

type EmbeddingSettings struct {
	EmbeddingSettingsID string          `json:"embedding_settings_id"`
	Enabled             bool            `json:"enabled"`
	RuntimeKey          string          `json:"runtime_key"`
	ModelKey            string          `json:"model_key"`
	Dimensions          int             `json:"dimensions"`
	DistanceMetric      string          `json:"distance_metric"`
	OllamaURL           string          `json:"ollama_url,omitempty"`
	QuietWindowSeconds  int             `json:"quiet_window_seconds"`
	GlobalConcurrency   int             `json:"global_concurrency"`
	HistoryPerLineage   int             `json:"history_per_lineage"`
	Metadata            json.RawMessage `json:"metadata"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type EmbeddingRuntimeModel struct {
	EmbeddingRuntimeModelID string          `json:"embedding_runtime_model_id"`
	RuntimeKey              string          `json:"runtime_key"`
	ModelKey                string          `json:"model_key"`
	Dimensions              int             `json:"dimensions"`
	DistanceMetric          string          `json:"distance_metric"`
	MaxInputTokens          *int            `json:"max_input_tokens,omitempty"`
	DefaultQuietWindowSec   int             `json:"default_quiet_window_seconds"`
	DefaultConcurrency      int             `json:"default_concurrency"`
	Status                  string          `json:"status"`
	Metadata                json.RawMessage `json:"metadata"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type EmbeddingObjectState struct {
	KnowledgeObjectID        string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID *string         `json:"knowledge_object_version_id,omitempty"`
	RuntimeKey               string          `json:"runtime_key"`
	ModelKey                 string          `json:"model_key"`
	Dimensions               int             `json:"dimensions"`
	Status                   string          `json:"status"`
	Generation               int64           `json:"generation"`
	LastContentChangeAt      *time.Time      `json:"last_content_change_at,omitempty"`
	EligibleAt               *time.Time      `json:"eligible_at,omitempty"`
	LastQueuedAt             *time.Time      `json:"last_queued_at,omitempty"`
	LastStartedAt            *time.Time      `json:"last_started_at,omitempty"`
	LastCompletedAt          *time.Time      `json:"last_completed_at,omitempty"`
	LastFailedAt             *time.Time      `json:"last_failed_at,omitempty"`
	LastErrorCode            string          `json:"last_error_code,omitempty"`
	LastErrorMessage         string          `json:"last_error_message,omitempty"`
	Metadata                 json.RawMessage `json:"metadata"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
}

type ChunkEmbedding struct {
	KnowledgeChunkEmbeddingID string          `json:"knowledge_chunk_embedding_id"`
	KnowledgeChunkID          *string         `json:"knowledge_chunk_id,omitempty"`
	KnowledgeObjectID         string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID  *string         `json:"knowledge_object_version_id,omitempty"`
	EmbeddingRuntimeModelID   string          `json:"embedding_runtime_model_id"`
	RuntimeKey                string          `json:"runtime_key"`
	ModelKey                  string          `json:"model_key"`
	Dimensions                int             `json:"dimensions"`
	DistanceMetric            string          `json:"distance_metric"`
	ChunkHash                 string          `json:"chunk_hash"`
	ChunkerVersion            string          `json:"chunker_version"`
	InputHash                 string          `json:"input_hash"`
	TokenCountEstimate        *int            `json:"token_count_estimate,omitempty"`
	SourceGeneration          int64           `json:"source_generation"`
	Status                    string          `json:"status"`
	Active                    bool            `json:"active"`
	Metadata                  json.RawMessage `json:"metadata"`
	CreatedAt                 time.Time       `json:"created_at"`
	ActivatedAt               *time.Time      `json:"activated_at,omitempty"`
	DeactivatedAt             *time.Time      `json:"deactivated_at,omitempty"`
}

type EmbeddingWorkItem struct {
	KnowledgeEmbeddingWorkItemID string          `json:"knowledge_embedding_work_item_id"`
	KnowledgeObjectID            string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID     *string         `json:"knowledge_object_version_id,omitempty"`
	KnowledgeChunkID             *string         `json:"knowledge_chunk_id,omitempty"`
	RuntimeKey                   string          `json:"runtime_key"`
	ModelKey                     string          `json:"model_key"`
	Dimensions                   int             `json:"dimensions"`
	Generation                   int64           `json:"generation"`
	ChunkHash                    string          `json:"chunk_hash,omitempty"`
	ChunkerVersion               string          `json:"chunker_version,omitempty"`
	Status                       string          `json:"status"`
	EligibleAt                   time.Time       `json:"eligible_at"`
	QueuedAt                     time.Time       `json:"queued_at"`
	StartedAt                    *time.Time      `json:"started_at,omitempty"`
	CompletedAt                  *time.Time      `json:"completed_at,omitempty"`
	FailedAt                     *time.Time      `json:"failed_at,omitempty"`
	AttemptCount                 int             `json:"attempt_count"`
	Priority                     int             `json:"priority"`
	ClaimedByWorkerRunID         string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimExpiresAt               *time.Time      `json:"claim_expires_at,omitempty"`
	LastErrorCode                string          `json:"last_error_code,omitempty"`
	LastErrorMessage             string          `json:"last_error_message,omitempty"`
	Metadata                     json.RawMessage `json:"metadata"`
	CreatedAt                    time.Time       `json:"created_at"`
	UpdatedAt                    time.Time       `json:"updated_at"`
}
