package knowledge

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/workers"
)

// PipelineProgress is a bounded observation of selected unified trajectories.
// Neither the queue window nor worker turn counters are corpus totals.
type PipelineProgress struct {
	SchemaVersion string                   `json:"schema_version"`
	ObservedAt    time.Time                `json:"observed_at"`
	Scope         string                   `json:"scope"`
	Limit         int                      `json:"limit"`
	WindowFull    bool                     `json:"window_full"`
	CurrentKnown  bool                     `json:"current_known"`
	CountsKnown   bool                     `json:"counts_known"`
	Unknown       map[string]string        `json:"unknown"`
	Runs          []PipelineRunProgress    `json:"runs"`
	Workers       []PipelineWorkerProgress `json:"workers"`
	PartialErrors []string                 `json:"partial_errors,omitempty"`
}

type PipelineRunProgress struct {
	PipelineRunID     string                       `json:"pipeline_run_id"`
	ObjectID          string                       `json:"object_id"`
	SelectedVersionID string                       `json:"selected_version_id"`
	Generation        int64                        `json:"generation"`
	SourceRevision    string                       `json:"source_revision"`
	SourceHash        string                       `json:"source_hash"`
	RunAgeSeconds     float64                      `json:"age_seconds"`
	Status            string                       `json:"status,omitempty"`
	CurrentStage      string                       `json:"current_stage,omitempty"`
	LastChangedAt     time.Time                    `json:"last_changed_at"`
	LastProgressAt    *time.Time                   `json:"last_progress_at,omitempty"`
	Source            *PipelineSourceStatus        `json:"source,omitempty"`
	Reason            string                       `json:"reason,omitempty"`
	NextEligibleAt    *time.Time                   `json:"next_eligible_at,omitempty"`
	Publication       *PipelineProgressPublication `json:"publication,omitempty"`
	Stages            []PipelineStageProgress      `json:"stages"`
}

// Eligibility describes the current source fence, not a lease or a promise
// that the worker will claim the stage now. Unavailable evidence stays unknown.
type PipelineSourceStatus struct {
	ObservedAt          time.Time `json:"observed_at"`
	ClaimSourceEligible *bool     `json:"claim_source_eligible"`
	Readable            *bool     `json:"readable"`
	Reason              string    `json:"reason"`
	RelativePath        string    `json:"relative_path,omitempty"`
}

type PipelineProgressPublication struct {
	CatalogCurrent      bool       `json:"catalog_current"`
	RefreshPending      bool       `json:"refresh_pending"`
	LexicalVersionID    string     `json:"lexical_version_id,omitempty"`
	SemanticVersionID   string     `json:"semantic_version_id,omitempty"`
	LexicalCurrent      bool       `json:"lexical_current"`
	SemanticCurrent     bool       `json:"semantic_current"`
	BodyCurrent         bool       `json:"body_current"`
	LexicalPublishedAt  *time.Time `json:"lexical_published_at,omitempty"`
	SemanticPublishedAt *time.Time `json:"semantic_published_at,omitempty"`
}

type PipelineStageProgress struct {
	StageKey       string                         `json:"stage_key"`
	Status         string                         `json:"status"`
	Attempts       int                            `json:"attempts"`
	Selected       *bool                          `json:"selected"`
	SkipReason     string                         `json:"skip_reason,omitempty"`
	CompletedUnits int                            `json:"completed_units"`
	TotalUnits     *int                           `json:"total_units"`
	ReusedUnits    *int                           `json:"reused_units"`
	Unknown        map[string]string              `json:"unknown,omitempty"`
	NextAttemptAt  *time.Time                     `json:"next_attempt_at,omitempty"`
	Admission      *workers.ResourceTurnAdmission `json:"admission,omitempty"`
}

// These are the existing owner's last committed run and pressure observation,
// with receipt identity/time. They do not assert current headroom or throughput.
type PipelineWorkerProgress struct {
	CurrentRunID           string                   `json:"current_run_id,omitempty"`
	CounterScope           string                   `json:"counter_scope"`
	Turns                  []HeavyExecutorRunResult `json:"turns,omitempty"`
	MoreWork               *bool                    `json:"more_work"`
	AdmissionTimedOut      *bool                    `json:"admission_timed_out"`
	AdmissionWarning       *AdmissionPhaseWarning   `json:"admission_warning,omitempty"`
	WorkerKey              string                   `json:"worker_key"`
	RunID                  string                   `json:"run_id,omitempty"`
	Status                 string                   `json:"status,omitempty"`
	StartedAt              *time.Time               `json:"started_at,omitempty"`
	FinishedAt             *time.Time               `json:"finished_at,omitempty"`
	NextRunAfter           *time.Time               `json:"next_run_after,omitempty"`
	Counters               map[string]int64         `json:"counters,omitempty"`
	RecentIntervalsSeconds []float64                `json:"recent_intervals_seconds,omitempty"`
}

const (
	PipelineExecutionCoordinator = "coordinator"
	PipelineExecutionHeavy       = "heavy"
)

const (
	FilePipelineStageMetadata         = "metadata"
	FilePipelineStageNativeText       = "native_text"
	FilePipelineStagePDFPageAnalysis  = "pdf_page_analysis"
	FilePipelineStagePDFOCR           = "pdf_ocr"
	FilePipelineStageImageOCR         = "image_ocr"
	FilePipelineStageImageDescription = "image_description"
	FilePipelineStageConsolidateText  = "consolidate_text"
	FilePipelineStageChunk            = "chunk"
	FilePipelineStageLexicalIndex     = "lexical_index"
	FilePipelineStageEmbedding        = "embedding"
	FilePipelineStageFinalize         = "finalize"
)

const (
	FilePipelineStatusQueued              = "queued"
	FilePipelineStatusWaitingQuietWindow  = "waiting_quiet_window"
	FilePipelineStatusWaitingCoordinator  = "waiting_coordinator"
	FilePipelineStatusWaitingHeavy        = "waiting_heavy"
	FilePipelineStatusProcessing          = "processing"
	FilePipelineStatusComplete            = "complete"
	FilePipelineStatusCompleteWithWarning = "complete_with_warnings"
	FilePipelineStatusBlockedManual       = "blocked_manual_action"
	FilePipelineStatusFailed              = "failed"
	FilePipelineStatusStale               = "stale"
	FilePipelineStatusCancelled           = "cancelled"
)

const (
	PipelineStageStatusPlanned              = "planned"
	PipelineStageStatusWaitingDependency    = "waiting_dependency"
	PipelineStageStatusWaitingQuietWindow   = "waiting_quiet_window"
	PipelineStageStatusReady                = "ready"
	PipelineStageStatusProcessing           = "processing"
	PipelineStageStatusComplete             = "complete"
	PipelineStageStatusCompleteWithWarning  = "complete_with_warnings"
	PipelineStageStatusSkippedNotApplicable = "skipped_not_applicable"
	PipelineStageStatusSkippedByPolicy      = "skipped_by_policy"
	PipelineStageStatusFailedRetryable      = "failed_retryable"
	PipelineStageStatusBlockedManual        = "blocked_manual_action"
	PipelineStageStatusStale                = "stale"
	PipelineStageStatusCancelled            = "cancelled"
)

const (
	ArtifactKindMetadataText      = "metadata_text"
	ArtifactKindEmbeddedText      = "embedded_text"
	ArtifactKindStructuredText    = "structured_text"
	ArtifactKindOCRText           = "ocr_text"
	ArtifactKindVisionDescription = "vision_description"
	ArtifactKindConsolidatedText  = "consolidated_text"
	ArtifactStateActive           = "active"
	ArtifactStateHistorical       = "historical"
	ArtifactStateReusable         = "reusable"
	ArtifactStateStale            = "stale"
)

type PipelineRun struct {
	// SourceSnapshot is private worker state, never a public path-bearing receipt.
	SourceSnapshot            json.RawMessage `json:"-"`
	KnowledgePipelineRunID    string          `json:"knowledge_pipeline_run_id"`
	KnowledgeObjectID         string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID  string          `json:"knowledge_object_version_id"`
	PipelineDefinitionKey     string          `json:"pipeline_definition_key"`
	PipelineDefinitionVersion string          `json:"pipeline_definition_version"`
	Generation                int64           `json:"generation"`
	Status                    string          `json:"status"`
	CurrentStageKey           string          `json:"current_stage_key,omitempty"`
	CurrentExecutionClass     string          `json:"current_execution_class,omitempty"`
	Priority                  int             `json:"priority"`
	SourceRevision            string          `json:"source_revision,omitempty"`
	SourceHash                string          `json:"source_hash,omitempty"`
	QuietWindowEligibleAt     *time.Time      `json:"quiet_window_eligible_at,omitempty"`
	ClaimedByWorkerRunID      string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimGeneration           int64           `json:"claim_generation"`
	ClaimExpiresAt            *time.Time      `json:"claim_expires_at,omitempty"`
	LastErrorCode             string          `json:"last_error_code,omitempty"`
	LastErrorMessage          string          `json:"last_error_message,omitempty"`
	WarningCount              int             `json:"warning_count"`
	PlanSnapshot              json.RawMessage `json:"plan_snapshot"`
	ResourceTotals            json.RawMessage `json:"resource_totals"`
	Metadata                  json.RawMessage `json:"metadata"`
	CreatedAt                 time.Time       `json:"created_at"`
	StartedAt                 *time.Time      `json:"started_at,omitempty"`
	UpdatedAt                 time.Time       `json:"updated_at"`
	CompletedAt               *time.Time      `json:"completed_at,omitempty"`
}

type PipelineStageRun struct {
	KnowledgePipelineStageRunID string          `json:"knowledge_pipeline_stage_run_id"`
	KnowledgePipelineRunID      string          `json:"knowledge_pipeline_run_id"`
	StageKey                    string          `json:"stage_key"`
	StageContractVersion        string          `json:"stage_contract_version"`
	Ordinal                     int             `json:"ordinal"`
	DependencySnapshot          json.RawMessage `json:"dependency_snapshot"`
	ExecutionClass              string          `json:"execution_class"`
	Status                      string          `json:"status"`
	AttemptCount                int             `json:"attempt_count"`
	NextAttemptAt               *time.Time      `json:"next_attempt_at,omitempty"`
	ClaimedByWorkerRunID        string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimGeneration             int64           `json:"claim_generation"`
	InputHash                   string          `json:"input_hash,omitempty"`
	OutputArtifactCount         int             `json:"output_artifact_count"`
	ProgressCompleted           int             `json:"progress_completed"`
	ProgressTotal               int             `json:"progress_total"`
	ResourceRequest             json.RawMessage `json:"resource_request"`
	ResourceUsage               json.RawMessage `json:"resource_usage"`
	Warnings                    json.RawMessage `json:"warnings"`
	Error                       json.RawMessage `json:"error"`
	Metadata                    json.RawMessage `json:"metadata"`
	CreatedAt                   time.Time       `json:"created_at"`
	StartedAt                   *time.Time      `json:"started_at,omitempty"`
	UpdatedAt                   time.Time       `json:"updated_at"`
	CompletedAt                 *time.Time      `json:"completed_at,omitempty"`
}

type PipelineStageUnit struct {
	KnowledgePipelineStageUnitID string          `json:"knowledge_pipeline_stage_unit_id"`
	KnowledgePipelineStageRunID  string          `json:"knowledge_pipeline_stage_run_id"`
	UnitKey                      string          `json:"unit_key"`
	PageNumber                   *int            `json:"page_number,omitempty"`
	UnitInputHash                string          `json:"unit_input_hash"`
	Status                       string          `json:"status"`
	AttemptCount                 int             `json:"attempt_count"`
	ClaimedByWorkerRunID         string          `json:"claimed_by_worker_run_id,omitempty"`
	ClaimGeneration              int64           `json:"claim_generation"`
	OutputArtifactID             *string         `json:"output_artifact_id,omitempty"`
	Confidence                   *float64        `json:"confidence,omitempty"`
	ResourceUsage                json.RawMessage `json:"resource_usage"`
	Warnings                     json.RawMessage `json:"warnings"`
	Error                        json.RawMessage `json:"error"`
	Metadata                     json.RawMessage `json:"metadata"`
	CreatedAt                    time.Time       `json:"created_at"`
	StartedAt                    *time.Time      `json:"started_at,omitempty"`
	UpdatedAt                    time.Time       `json:"updated_at"`
	CompletedAt                  *time.Time      `json:"completed_at,omitempty"`
}

type DerivedArtifact struct {
	KnowledgeDerivedArtifactID  string          `json:"knowledge_derived_artifact_id"`
	KnowledgeObjectID           string          `json:"knowledge_object_id"`
	KnowledgeObjectVersionID    string          `json:"knowledge_object_version_id"`
	KnowledgePipelineRunID      string          `json:"knowledge_pipeline_run_id"`
	KnowledgePipelineStageRunID string          `json:"knowledge_pipeline_stage_run_id"`
	Generation                  int64           `json:"generation"`
	ArtifactKind                string          `json:"artifact_kind"`
	SourceLocator               string          `json:"source_locator"`
	TextContent                 *string         `json:"text_content,omitempty"`
	PayloadRef                  string          `json:"payload_ref,omitempty"`
	ContentHash                 string          `json:"content_hash"`
	InputHash                   string          `json:"input_hash"`
	GeneratorKey                string          `json:"generator_key"`
	GeneratorVersion            string          `json:"generator_version"`
	EngineKey                   string          `json:"engine_key,omitempty"`
	EngineVersion               string          `json:"engine_version,omitempty"`
	PromptVersion               string          `json:"prompt_version,omitempty"`
	Language                    string          `json:"language,omitempty"`
	Confidence                  *float64        `json:"confidence,omitempty"`
	State                       string          `json:"state"`
	Active                      bool            `json:"active"`
	SourceCreatedAt             *time.Time      `json:"source_created_at,omitempty"`
	SourceModifiedAt            *time.Time      `json:"source_modified_at,omitempty"`
	RecencyAt                   time.Time       `json:"recency_at"`
	RecencyBasis                string          `json:"recency_basis"`
	Metadata                    json.RawMessage `json:"metadata"`
	CreatedAt                   time.Time       `json:"created_at"`
	ActivatedAt                 *time.Time      `json:"activated_at,omitempty"`
	DeactivatedAt               *time.Time      `json:"deactivated_at,omitempty"`
}
