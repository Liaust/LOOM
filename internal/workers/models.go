package workers

import (
	"encoding/json"
	"time"
)

const (
	KindSelfcheck            = "worker_selfcheck"
	KindPolicyExpiry         = "policy_expiry"
	KindRealtimeExpiry       = "realtime_expiry"
	KindDBMaintenance        = "db_maintenance"
	KindMainBackup           = "main_backup"
	KindObjectStore          = "object_store_integrity"
	KindIndexerText          = "indexer_text"
	KindKnowledgeIndexer     = "knowledge_indexer"
	KindKnowledgeHeavy       = "knowledge_heavy"
	KindKnowledgeEmbedder    = "knowledge_embedder"
	KindJobRunner            = "job_runner"
	KindJobSweeper           = "job_sweeper"
	KindAutomationScheduler  = "automation_scheduler"
	KindDirectEventIngest    = "direct_event_ingest"
	KindAutomationDispatcher = "automation_dispatcher"
	KindMainDocumentsImport  = "main_documents_import"
	KindCloudSnapshotUpload  = "cloud_snapshot_upload"

	RuntimeOwnerLoomd         = "loomd"
	RuntimeOwnerNodeAgent     = "loom-node-agent"
	RuntimeOwnerTrustedModule = "trusted_module"

	KindStatusActive     = "active"
	KindStatusDeprecated = "deprecated"
	KindStatusDisabled   = "disabled"

	LocalityMainOwned        = "main_owned"
	LocalityNodeAgentOwned   = "node_agent_owned"
	LocalityExternalReported = "external_reported"

	LifecycleRegistered = "registered"
	LifecycleActive     = "active"
	LifecycleDegraded   = "degraded"
	LifecycleFailed     = "failed"
	LifecycleDisabled   = "disabled"
	LifecycleRetired    = "retired"

	HealthUnknown  = "unknown"
	HealthHealthy  = "healthy"
	HealthRunning  = "running"
	HealthLagging  = "lagging"
	HealthDegraded = "degraded"
	HealthFailed   = "failed"
	HealthPaused   = "paused"
	HealthDisabled = "disabled"

	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"

	RunStatusStarting  = "starting"
	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"
	RunStatusTimedOut  = "timed_out"

	TriggerManual         = "manual"
	TriggerSupervisorTick = "supervisor_tick"
	TriggerSchedule       = "schedule"
	TriggerDirectEvent    = "direct_event"
	TriggerRetry          = "retry"
	TriggerStartup        = "startup"

	LeaseStatusActive   = "active"
	LeaseStatusReleased = "released"
	LeaseStatusExpired  = "expired"
	LeaseStatusStolen   = "stolen"

	LeaseHolderLoomd     = "loomd"
	LeaseHolderNodeAgent = "loom-node-agent"
	LeaseHolderCLI       = "cli"

	ControlRunOnce           = "run_once"
	ControlPause             = "pause"
	ControlResume            = "resume"
	ControlDisable           = "disable"
	ControlForceReleaseLease = "force_release_lease"

	ControlStatusRequested = "requested"
	ControlStatusApplied   = "applied"
	ControlStatusRejected  = "rejected"
	ControlStatusExpired   = "expired"
	ControlStatusFailed    = "failed"
)

type KindDescriptor struct {
	WorkerKind                   string          `json:"worker_kind"`
	DisplayName                  string          `json:"display_name"`
	Description                  string          `json:"description"`
	RuntimeOwner                 string          `json:"runtime_owner"`
	RuntimePackage               string          `json:"runtime_package"`
	Status                       string          `json:"status"`
	SupportedLocalities          []string        `json:"supported_localities"`
	MayCreateJobs                bool            `json:"may_create_jobs"`
	MayCallCapabilities          bool            `json:"may_call_capabilities"`
	MayTouchFilesystem           bool            `json:"may_touch_filesystem"`
	MayStoreRawPayloads          bool            `json:"may_store_raw_payloads"`
	DefaultTickPolicyJSON        json.RawMessage `json:"default_tick_policy_json"`
	DefaultConcurrencyPolicyJSON json.RawMessage `json:"default_concurrency_policy_json"`
	DefaultRetryPolicyJSON       json.RawMessage `json:"default_retry_policy_json"`
	DefaultTimeoutPolicyJSON     json.RawMessage `json:"default_timeout_policy_json"`
	DefaultResourceLimitsJSON    json.RawMessage `json:"default_resource_limits_json"`
	ConfigSchemaJSON             json.RawMessage `json:"config_schema_json"`
	CheckpointSchemaJSON         json.RawMessage `json:"checkpoint_schema_json"`
	ResultSchemaJSON             json.RawMessage `json:"result_schema_json"`
	Metadata                     json.RawMessage `json:"metadata"`
}

type InstanceDescriptor struct {
	WorkerKey          string          `json:"worker_key"`
	WorkerKind         string          `json:"worker_kind"`
	DisplayName        string          `json:"display_name"`
	Description        string          `json:"description"`
	Locality           string          `json:"locality"`
	ConfigJSON         json.RawMessage `json:"config_json"`
	TickPolicyJSON     json.RawMessage `json:"tick_policy_json"`
	ConcurrencyJSON    json.RawMessage `json:"concurrency_policy_json"`
	RetryPolicyJSON    json.RawMessage `json:"retry_policy_json"`
	TimeoutPolicyJSON  json.RawMessage `json:"timeout_policy_json"`
	ResourceLimitsJSON json.RawMessage `json:"resource_limits_json"`
	VisibilityJSON     json.RawMessage `json:"visibility_json"`
	Metadata           json.RawMessage `json:"metadata"`
}

type WorkerKind struct {
	WorkerKind                   string          `json:"worker_kind"`
	DisplayName                  string          `json:"display_name"`
	Description                  string          `json:"description"`
	RuntimeOwner                 string          `json:"runtime_owner"`
	RuntimePackage               string          `json:"runtime_package"`
	Status                       string          `json:"status"`
	SupportedLocalities          []string        `json:"supported_localities"`
	MayCreateJobs                bool            `json:"may_create_jobs"`
	MayCallCapabilities          bool            `json:"may_call_capabilities"`
	MayTouchFilesystem           bool            `json:"may_touch_filesystem"`
	MayStoreRawPayloads          bool            `json:"may_store_raw_payloads"`
	DefaultTickPolicyJSON        json.RawMessage `json:"default_tick_policy_json"`
	DefaultConcurrencyPolicyJSON json.RawMessage `json:"default_concurrency_policy_json"`
	DefaultRetryPolicyJSON       json.RawMessage `json:"default_retry_policy_json"`
	DefaultTimeoutPolicyJSON     json.RawMessage `json:"default_timeout_policy_json"`
	DefaultResourceLimitsJSON    json.RawMessage `json:"default_resource_limits_json"`
	ConfigSchemaJSON             json.RawMessage `json:"config_schema_json"`
	CheckpointSchemaJSON         json.RawMessage `json:"checkpoint_schema_json"`
	ResultSchemaJSON             json.RawMessage `json:"result_schema_json"`
	RegisteredByActorID          *string         `json:"registered_by_actor_id,omitempty"`
	CreatedAt                    time.Time       `json:"created_at"`
	UpdatedAt                    time.Time       `json:"updated_at"`
	Metadata                     json.RawMessage `json:"metadata"`
}

type WorkerInstance struct {
	WorkerInstanceID   string          `json:"worker_instance_id"`
	WorkerKey          string          `json:"worker_key"`
	WorkerKind         string          `json:"worker_kind"`
	DisplayName        string          `json:"display_name"`
	Description        string          `json:"description"`
	OwnerNodeID        string          `json:"owner_node_id"`
	HostNodeID         string          `json:"host_node_id"`
	ScopeID            *string         `json:"scope_id,omitempty"`
	ProjectID          *string         `json:"project_id,omitempty"`
	Locality           string          `json:"locality"`
	LifecycleStatus    string          `json:"lifecycle_status"`
	Enabled            bool            `json:"enabled"`
	Paused             bool            `json:"paused"`
	ConfigJSON         json.RawMessage `json:"config_json"`
	TickPolicyJSON     json.RawMessage `json:"tick_policy_json"`
	ConcurrencyJSON    json.RawMessage `json:"concurrency_policy_json"`
	RetryPolicyJSON    json.RawMessage `json:"retry_policy_json"`
	TimeoutPolicyJSON  json.RawMessage `json:"timeout_policy_json"`
	ResourceLimitsJSON json.RawMessage `json:"resource_limits_json"`
	VisibilityJSON     json.RawMessage `json:"visibility_json"`
	CurrentRunID       *string         `json:"current_run_id,omitempty"`
	LastRunID          *string         `json:"last_run_id,omitempty"`
	LastSuccessAt      *time.Time      `json:"last_success_at,omitempty"`
	LastFailureAt      *time.Time      `json:"last_failure_at,omitempty"`
	LastHeartbeatAt    *time.Time      `json:"last_heartbeat_at,omitempty"`
	NextRunAfter       *time.Time      `json:"next_run_after,omitempty"`
	BackoffUntil       *time.Time      `json:"backoff_until,omitempty"`
	ConsecutiveFails   int             `json:"consecutive_failures"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Metadata           json.RawMessage `json:"metadata"`
}

type WorkerRun struct {
	WorkerRunID       string          `json:"worker_run_id"`
	WorkerInstanceID  string          `json:"worker_instance_id"`
	WorkerKind        string          `json:"worker_kind"`
	RunStatus         string          `json:"run_status"`
	TriggerKind       string          `json:"trigger_kind"`
	TriggerRef        string          `json:"trigger_ref"`
	LeaseID           *string         `json:"lease_id,omitempty"`
	LeaseGeneration   *int64          `json:"lease_generation,omitempty"`
	CorrelationID     string          `json:"correlation_id"`
	IdempotencyKey    string          `json:"idempotency_key"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
	DeadlineAt        *time.Time      `json:"deadline_at,omitempty"`
	ResultSummaryJSON json.RawMessage `json:"result_summary_json"`
	CountersJSON      json.RawMessage `json:"counters_json"`
	ResourceUsageJSON json.RawMessage `json:"resource_usage_json"`
	ErrorJSON         json.RawMessage `json:"error_json"`
	Retryable         bool            `json:"retryable"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Metadata          json.RawMessage `json:"metadata"`
}

type WorkerHealth struct {
	WorkerHealthID    string          `json:"worker_health_id"`
	WorkerInstanceID  string          `json:"worker_instance_id"`
	HealthStatus      string          `json:"health_status"`
	Severity          string          `json:"severity"`
	Summary           string          `json:"summary"`
	AttentionRequired bool            `json:"attention_required"`
	LastSuccessAt     *time.Time      `json:"last_success_at,omitempty"`
	LastFailureAt     *time.Time      `json:"last_failure_at,omitempty"`
	CurrentRunID      *string         `json:"current_run_id,omitempty"`
	QueueDepth        int             `json:"queue_depth"`
	ConsecutiveFails  int             `json:"consecutive_failures"`
	ComputedAt        time.Time       `json:"computed_at"`
	DetailsJSON       json.RawMessage `json:"details_json"`
	Metadata          json.RawMessage `json:"metadata"`
}

type WorkerLease struct {
	WorkerLeaseID    string          `json:"worker_lease_id"`
	WorkerInstanceID string          `json:"worker_instance_id"`
	LeaseKey         string          `json:"lease_key"`
	LeaseStatus      string          `json:"lease_status"`
	Generation       int64           `json:"generation"`
	HolderID         string          `json:"holder_id"`
	HolderKind       string          `json:"holder_kind"`
	RunID            *string         `json:"run_id,omitempty"`
	AcquiredAt       time.Time       `json:"acquired_at"`
	RenewedAt        time.Time       `json:"renewed_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ReleasedAt       *time.Time      `json:"released_at,omitempty"`
	Metadata         json.RawMessage `json:"metadata"`
}

type WorkerControl struct {
	WorkerControlID    string          `json:"worker_control_id"`
	WorkerInstanceID   string          `json:"worker_instance_id"`
	ControlKind        string          `json:"control_kind"`
	ControlStatus      string          `json:"control_status"`
	RequestedByActorID string          `json:"requested_by_actor_id"`
	RequestedAt        time.Time       `json:"requested_at"`
	AppliedByRunID     *string         `json:"applied_by_run_id,omitempty"`
	AppliedAt          *time.Time      `json:"applied_at,omitempty"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
	InputJSON          json.RawMessage `json:"input_json"`
	ResultJSON         json.RawMessage `json:"result_json"`
	ErrorJSON          json.RawMessage `json:"error_json"`
	Metadata           json.RawMessage `json:"metadata"`
}

type StaleRunRepairResult struct {
	CheckedAt         time.Time `json:"checked_at"`
	TimedOutRuns      int       `json:"timed_out_runs"`
	ExpiredLeases     int       `json:"expired_leases"`
	RepairedInstances int       `json:"repaired_instances"`
	RunIDs            []string  `json:"run_ids,omitempty"`
}

type WorkerListItem struct {
	WorkerInstanceID  string     `json:"worker_instance_id"`
	WorkerKey         string     `json:"worker_key"`
	WorkerKind        string     `json:"worker_kind"`
	DisplayName       string     `json:"display_name"`
	Locality          string     `json:"locality"`
	LifecycleStatus   string     `json:"lifecycle_status"`
	Enabled           bool       `json:"enabled"`
	Paused            bool       `json:"paused"`
	HealthStatus      string     `json:"health_status"`
	Severity          string     `json:"severity"`
	LastSuccessAt     *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
	CurrentRunID      *string    `json:"current_run_id,omitempty"`
	AttentionRequired bool       `json:"attention_required"`
}

type SeedResult struct {
	KindsCreated     int `json:"kinds_created"`
	KindsUpdated     int `json:"kinds_updated"`
	InstancesCreated int `json:"instances_created"`
	InstancesUpdated int `json:"instances_updated"`
	HealthCreated    int `json:"health_created"`
}

type WorkerDetail struct {
	Instance         WorkerInstance     `json:"instance"`
	Kind             WorkerKind         `json:"kind"`
	Health           *WorkerHealth      `json:"health,omitempty"`
	CurrentRun       *WorkerRun         `json:"current_run,omitempty"`
	LastRun          *WorkerRun         `json:"last_run,omitempty"`
	LatestCheckpoint *WorkerCheckpoint  `json:"latest_checkpoint,omitempty"`
	RecentRuns       []WorkerRun        `json:"recent_runs"`
	Checkpoints      []WorkerCheckpoint `json:"checkpoints,omitempty"`
}

type RunOnceResult struct {
	Worker      WorkerDetail       `json:"worker"`
	Run         WorkerRun          `json:"run"`
	Health      WorkerHealth       `json:"health"`
	Checkpoints []WorkerCheckpoint `json:"checkpoints"`
	Control     *WorkerControl     `json:"control,omitempty"`
}

type WorkerPolicyState struct {
	WorkerInstanceID  string     `json:"worker_instance_id"`
	WorkerKey         string     `json:"worker_key"`
	Locality          string     `json:"locality"`
	LifecycleStatus   string     `json:"lifecycle_status"`
	Enabled           bool       `json:"enabled"`
	Paused            bool       `json:"paused"`
	CurrentRunID      *string    `json:"current_run_id,omitempty"`
	Policy            TickPolicy `json:"policy"`
	PolicyFingerprint string     `json:"policy_fingerprint"`
	NextRunAfter      *time.Time `json:"next_run_after,omitempty"`
	CapturedAt        time.Time  `json:"captured_at"`
}

type SetWorkerPolicyInput struct {
	Policy                    TickPolicy `json:"policy"`
	ExpectedPolicyFingerprint string     `json:"expected_policy_fingerprint"`
	Reason                    string     `json:"reason"`
	DryRun                    bool       `json:"dry_run"`
	Confirm                   bool       `json:"confirm"`
	IdempotencyKey            string     `json:"-"`
}

type WorkerPolicyNextRunEvidence struct {
	DerivedAt       time.Time  `json:"derived_at"`
	Previous        *time.Time `json:"previous,omitempty"`
	Proposed        *time.Time `json:"proposed,omitempty"`
	DerivationBasis string     `json:"derivation_basis"`
}

type SetWorkerPolicyResult struct {
	WorkerInstanceID string                      `json:"worker_instance_id"`
	WorkerKey        string                      `json:"worker_key"`
	DryRun           bool                        `json:"dry_run"`
	Applied          bool                        `json:"applied"`
	Changed          bool                        `json:"changed"`
	IdempotentReplay bool                        `json:"idempotent_replay"`
	Reason           string                      `json:"reason"`
	Old              WorkerPolicyState           `json:"old"`
	New              WorkerPolicyState           `json:"new"`
	NextRunEvidence  WorkerPolicyNextRunEvidence `json:"next_run_evidence"`
	EventType        string                      `json:"event_type,omitempty"`
}
