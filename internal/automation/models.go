package automation

import (
	"encoding/json"
	"time"
)

const (
	SourceKindSchedule    = "schedule"
	SourceKindDirectEvent = "direct_event"

	AutomationStatusActive   = "active"
	AutomationStatusPaused   = "paused"
	AutomationStatusDisabled = "disabled"
	AutomationStatusArchived = "archived"

	IntegrationStatusActive   = "active"
	IntegrationStatusDisabled = "disabled"
	IntegrationStatusRevoked  = "revoked"

	IntegrationAuthStatusActive   = "active"
	IntegrationAuthStatusDisabled = "disabled"
	IntegrationAuthStatusRevoked  = "revoked"

	IntegrationAuthBearerHeader   = "bearer_header"
	IntegrationAuthQueryToken     = "query_token"
	IntegrationAuthPrivateNetwork = "private_network"

	DirectEventEndpointStatusActive   = "active"
	DirectEventEndpointStatusPaused   = "paused"
	DirectEventEndpointStatusDisabled = "disabled"

	DirectEventResponseAccepted = "accepted"
	DirectEventResponseSyncWait = "sync_wait"

	DirectEventIdempotencyHeader      = "header"
	DirectEventIdempotencyPayloadPath = "payload_path"
	DirectEventIdempotencyNone        = "none"

	DirectEventStatusReceived          = "received"
	DirectEventStatusAccepted          = "accepted"
	DirectEventStatusAuthenticated     = "authenticated"
	DirectEventStatusRejected          = "rejected"
	DirectEventStatusDuplicate         = "duplicate"
	DirectEventStatusMapped            = "mapped"
	DirectEventStatusMappingFailed     = "mapping_failed"
	DirectEventStatusInvocationCreated = "invocation_created"
	DirectEventStatusCompleted         = "completed"
	DirectEventStatusFailed            = "failed"
	DirectEventStatusTimedOut          = "timed_out"

	ScheduleStatusActive    = "active"
	ScheduleStatusPaused    = "paused"
	ScheduleStatusDisabled  = "disabled"
	ScheduleStatusCompleted = "completed"

	ScheduleKindOneShot  = "one_shot"
	ScheduleKindInterval = "interval"
	ScheduleKindCron     = "cron"

	FireStatusCreated              = "created"
	FireStatusMissed               = "missed"
	FireStatusSkipped              = "skipped"
	FireStatusPendingInvocation    = "pending_invocation"
	FireStatusInvocationCreated    = "invocation_created"
	FireStatusCompleted            = "completed"
	FireStatusFailed               = "failed"
	FireStatusRequiresManualAction = "requires_manual_action"

	InvocationStatusPending              = "pending"
	InvocationStatusLeased               = "leased"
	InvocationStatusCalling              = "calling"
	InvocationStatusWaitingApproval      = "waiting_approval"
	InvocationStatusSucceeded            = "succeeded"
	InvocationStatusFailed               = "failed"
	InvocationStatusTimedOut             = "timed_out"
	InvocationStatusCancelled            = "cancelled"
	InvocationStatusRequiresManualAction = "requires_manual_action"

	MisfireMarkMissed      = "mark_missed"
	MisfireRunIfLateWithin = "run_if_late_within"

	ConcurrencyAllowParallel = "allow_parallel"
	ConcurrencySkipIfPending = "skip_if_pending"

	ApprovalNone                = "none"
	ApprovalCreateAndWait       = "create_approval_and_wait"
	ApprovalFailIfRequired      = "fail_if_approval_required"
	defaultSchedulerActorRef    = "scheduler:loom"
	defaultSchedulerOriginNode  = "main"
	defaultSchedulerScope       = "system"
	defaultScheduleListLimit    = 50
	maxScheduleListLimit        = 200
	defaultTimeoutSeconds       = 30
	maxTimeoutSeconds           = 86400
	defaultLatenessWindowSecs   = 0
	defaultMaxAttempts          = 1
	defaultAutomationSource     = "schedule"
	defaultAutomationSourceName = "LOOM schedule"
)

type Automation struct {
	AutomationID             string          `json:"automation_id"`
	AutomationKey            string          `json:"automation_key"`
	DisplayName              string          `json:"display_name"`
	Description              string          `json:"description"`
	Status                   string          `json:"status"`
	SourceKind               string          `json:"source_kind"`
	SourceProfileJSON        json.RawMessage `json:"source_profile_json"`
	IntegrationProfileJSON   json.RawMessage `json:"integration_profile_json"`
	MappingProfileJSON       json.RawMessage `json:"mapping_profile_json"`
	TargetProfileJSON        json.RawMessage `json:"target_profile_json"`
	CommunicationProfileJSON json.RawMessage `json:"communication_profile_json"`
	IdempotencyProfileJSON   json.RawMessage `json:"idempotency_profile_json"`
	StorageProfileJSON       json.RawMessage `json:"storage_profile_json"`
	ExecutionProfileJSON     json.RawMessage `json:"execution_profile_json"`
	TimeoutProfileJSON       json.RawMessage `json:"timeout_profile_json"`
	RetryProfileJSON         json.RawMessage `json:"retry_profile_json"`
	ConcurrencyProfileJSON   json.RawMessage `json:"concurrency_profile_json"`
	MisfireProfileJSON       json.RawMessage `json:"misfire_profile_json"`
	ApprovalProfileJSON      json.RawMessage `json:"approval_profile_json"`
	CreatedByActorID         string          `json:"created_by_actor_id"`
	RunAsActorID             string          `json:"run_as_actor_id"`
	ScopeID                  *string         `json:"scope_id,omitempty"`
	ProjectID                *string         `json:"project_id,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	MetadataJSON             json.RawMessage `json:"metadata_json"`
}

type Schedule struct {
	ScheduleID             string          `json:"schedule_id"`
	AutomationID           string          `json:"automation_id"`
	ScheduleKey            string          `json:"schedule_key"`
	DisplayName            string          `json:"display_name"`
	Description            string          `json:"description"`
	Status                 string          `json:"status"`
	ScheduleKind           string          `json:"schedule_kind"`
	ScheduleExpr           string          `json:"schedule_expr"`
	Timezone               string          `json:"timezone"`
	StartAt                *time.Time      `json:"start_at,omitempty"`
	NextFireAt             *time.Time      `json:"next_fire_at,omitempty"`
	LastFireAt             *time.Time      `json:"last_fire_at,omitempty"`
	LastScheduleFireID     *string         `json:"last_schedule_fire_id,omitempty"`
	InputJSON              json.RawMessage `json:"input_json"`
	TargetProfileJSON      json.RawMessage `json:"target_profile_json"`
	MisfireProfileJSON     json.RawMessage `json:"misfire_profile_json"`
	ConcurrencyProfileJSON json.RawMessage `json:"concurrency_profile_json"`
	ApprovalProfileJSON    json.RawMessage `json:"approval_profile_json"`
	TimeoutProfileJSON     json.RawMessage `json:"timeout_profile_json"`
	RetryProfileJSON       json.RawMessage `json:"retry_profile_json"`
	CreatedByActorID       string          `json:"created_by_actor_id"`
	RunAsActorID           string          `json:"run_as_actor_id"`
	ScopeID                *string         `json:"scope_id,omitempty"`
	ProjectID              *string         `json:"project_id,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	MetadataJSON           json.RawMessage `json:"metadata_json"`
}

type ScheduleFire struct {
	ScheduleFireID   string          `json:"schedule_fire_id"`
	ScheduleID       string          `json:"schedule_id"`
	AutomationID     string          `json:"automation_id"`
	ScheduledFor     time.Time       `json:"scheduled_for"`
	Status           string          `json:"status"`
	MisfireStatus    string          `json:"misfire_status"`
	LatenessSeconds  int             `json:"lateness_seconds"`
	WorkerRunID      *string         `json:"worker_run_id,omitempty"`
	InvocationID     *string         `json:"invocation_id,omitempty"`
	RouteID          *string         `json:"route_id,omitempty"`
	CapabilityCallID *string         `json:"capability_call_id,omitempty"`
	JobID            *string         `json:"job_id,omitempty"`
	FailureCode      *string         `json:"failure_code,omitempty"`
	FailureMessage   *string         `json:"failure_message,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	FailedAt         *time.Time      `json:"failed_at,omitempty"`
	MetadataJSON     json.RawMessage `json:"metadata_json"`
}

type Invocation struct {
	InvocationID        string          `json:"invocation_id"`
	AutomationID        string          `json:"automation_id"`
	SourceKind          string          `json:"source_kind"`
	SourceRef           string          `json:"source_ref"`
	SourceOccurrenceRef string          `json:"source_occurrence_ref"`
	ActorID             string          `json:"actor_id"`
	OriginNodeID        string          `json:"origin_node_id"`
	ScopeID             *string         `json:"scope_id,omitempty"`
	ProjectID           *string         `json:"project_id,omitempty"`
	TargetCapability    string          `json:"target_capability"`
	InputJSON           json.RawMessage `json:"input_json"`
	InputHash           string          `json:"input_hash"`
	IdempotencyKey      string          `json:"idempotency_key"`
	Status              string          `json:"status"`
	AttemptCount        int             `json:"attempt_count"`
	MaxAttempts         int             `json:"max_attempts"`
	NextAttemptAt       *time.Time      `json:"next_attempt_at,omitempty"`
	LeasedByWorkerRunID *string         `json:"leased_by_worker_run_id,omitempty"`
	LeasedAt            *time.Time      `json:"leased_at,omitempty"`
	LeaseExpiresAt      *time.Time      `json:"lease_expires_at,omitempty"`
	RouteID             *string         `json:"route_id,omitempty"`
	CapabilityCallID    *string         `json:"capability_call_id,omitempty"`
	JobID               *string         `json:"job_id,omitempty"`
	PolicyDecisionID    *string         `json:"policy_decision_id,omitempty"`
	ApprovalID          *string         `json:"approval_id,omitempty"`
	GrantID             *string         `json:"grant_id,omitempty"`
	ResultJSON          json.RawMessage `json:"result_json"`
	ResultRefsJSON      json.RawMessage `json:"result_refs_json"`
	FailureCode         *string         `json:"failure_code,omitempty"`
	FailureMessage      *string         `json:"failure_message,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	StartedAt           *time.Time      `json:"started_at,omitempty"`
	CompletedAt         *time.Time      `json:"completed_at,omitempty"`
	FailedAt            *time.Time      `json:"failed_at,omitempty"`
	MetadataJSON        json.RawMessage `json:"metadata_json"`
}

type AutomationDetail struct {
	Automation           Automation            `json:"automation"`
	Schedules            []Schedule            `json:"schedules,omitempty"`
	DirectEventEndpoints []DirectEventEndpoint `json:"direct_event_endpoints,omitempty"`
}

type ScheduleDetail struct {
	DryRun     bool       `json:"dry_run,omitempty"`
	Schedule   Schedule   `json:"schedule"`
	Automation Automation `json:"automation"`
}

type AutomationFilter struct {
	Limit      int
	Status     string
	SourceKind string
}

type ScheduleFilter struct {
	Limit         int
	Status        string
	AutomationRef string
	ProjectRef    string
}

type ScheduleFireFilter struct {
	Limit         int
	Status        string
	ScheduleRef   string
	AutomationRef string
}

type InvocationFilter struct {
	Limit         int
	Status        string
	AutomationRef string
	ProjectRef    string
	SourceKind    string
}

type CreateScheduleInput struct {
	DryRun             bool            `json:"dry_run,omitempty"`
	ScheduleKey        string          `json:"schedule_key"`
	DisplayName        string          `json:"display_name"`
	Description        string          `json:"description,omitempty"`
	TargetCapability   string          `json:"target_capability"`
	InputJSON          json.RawMessage `json:"input_json,omitempty"`
	ScheduleKind       string          `json:"schedule_kind"`
	ScheduleExpr       string          `json:"schedule_expr"`
	Timezone           string          `json:"timezone,omitempty"`
	ScopeRef           string          `json:"scope_ref,omitempty"`
	ProjectRef         string          `json:"project_ref,omitempty"`
	RunAsActorRef      string          `json:"run_as_actor_ref,omitempty"`
	MisfirePolicy      string          `json:"misfire_policy,omitempty"`
	LatenessWindowSecs int             `json:"lateness_window_seconds,omitempty"`
	ConcurrencyPolicy  string          `json:"concurrency_policy,omitempty"`
	TimeoutSeconds     int             `json:"timeout_seconds,omitempty"`
	MaxAttempts        int             `json:"max_attempts,omitempty"`
	Status             string          `json:"status,omitempty"`
	ApprovalPolicy     string          `json:"approval_policy,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

type UpdateScheduleStatusInput struct {
	Reason   string          `json:"reason,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ScheduleStatus struct {
	ActiveScheduleCount       int64      `json:"active_schedule_count"`
	PausedScheduleCount       int64      `json:"paused_schedule_count"`
	DisabledScheduleCount     int64      `json:"disabled_schedule_count"`
	CompletedScheduleCount    int64      `json:"completed_schedule_count"`
	DueScheduleCount          int64      `json:"due_schedule_count"`
	MissedFireCount           int64      `json:"missed_fire_count"`
	PendingInvocationCount    int64      `json:"pending_invocation_count"`
	FailedInvocationCount     int64      `json:"failed_invocation_count"`
	NextFireAt                *time.Time `json:"next_fire_at,omitempty"`
	OldestPendingInvocationAt *time.Time `json:"oldest_pending_invocation_at,omitempty"`
	SchedulerWorkerKey        string     `json:"scheduler_worker_key"`
	DispatcherWorkerKey       string     `json:"dispatcher_worker_key"`
}

type FireScheduleInput struct {
	Reason      string          `json:"reason,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	DispatchNow bool            `json:"dispatch_now,omitempty"`
}

type FireScheduleResult struct {
	Schedule      Schedule             `json:"schedule"`
	Fire          ScheduleFire         `json:"fire"`
	Invocation    Invocation           `json:"invocation"`
	Dispatch      *DispatcherRunResult `json:"dispatch,omitempty"`
	DispatchError string               `json:"dispatch_error,omitempty"`
}

type SchedulerRunInput struct {
	Now             time.Time     `json:"now,omitempty"`
	BatchSize       int           `json:"batch_size,omitempty"`
	LookaheadWindow time.Duration `json:"lookahead_window,omitempty"`
	WorkerRunID     string        `json:"worker_run_id,omitempty"`
}

type SchedulerRunResult struct {
	ScannedSchedules   int64      `json:"scanned_schedules"`
	CreatedFires       int64      `json:"created_fires"`
	CreatedInvocations int64      `json:"created_invocations"`
	MissedFires        int64      `json:"missed_fires"`
	SkippedFires       int64      `json:"skipped_fires"`
	FailedSchedules    int64      `json:"failed_schedules"`
	NextDueAt          *time.Time `json:"next_due_at,omitempty"`
	ScheduleFireIDs    []string   `json:"schedule_fire_ids,omitempty"`
	InvocationIDs      []string   `json:"invocation_ids,omitempty"`
}

type DispatcherRunInput struct {
	Now           time.Time     `json:"now,omitempty"`
	BatchSize     int           `json:"batch_size,omitempty"`
	LeaseDuration time.Duration `json:"lease_duration,omitempty"`
	WorkerRunID   string        `json:"worker_run_id,omitempty"`
	InvocationRef string        `json:"invocation_ref,omitempty"`
}

type DispatcherRunResult struct {
	Claimed            int64  `json:"claimed"`
	Succeeded          int64  `json:"succeeded"`
	Failed             int64  `json:"failed"`
	WaitingApproval    int64  `json:"waiting_approval"`
	Retrying           int64  `json:"retrying"`
	ManualAction       int64  `json:"manual_action"`
	LastInvocationID   string `json:"last_invocation_id,omitempty"`
	LastCapabilityCall string `json:"last_capability_call,omitempty"`
	LastRouteID        string `json:"last_route_id,omitempty"`
	LastJobID          string `json:"last_job_id,omitempty"`
}

type TargetProfile struct {
	CapabilityRef string `json:"capability_ref"`
	ScopeRef      string `json:"scope_ref,omitempty"`
	ProjectRef    string `json:"project_ref,omitempty"`
}

type MisfireProfile struct {
	Policy                string `json:"policy"`
	LatenessWindowSeconds int    `json:"lateness_window_seconds,omitempty"`
}

type ConcurrencyProfile struct {
	Policy string `json:"policy"`
}

type TimeoutProfile struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

type RetryProfile struct {
	MaxAttempts int `json:"max_attempts"`
}

type ApprovalProfile struct {
	Mode string `json:"mode"`
}

type SchedulePlan struct {
	Kind       string
	Expr       string
	StartAt    *time.Time
	NextFireAt *time.Time
}
