package automation

import (
	"encoding/json"
	"time"
)

type Integration struct {
	IntegrationID           string          `json:"integration_id"`
	IntegrationKey          string          `json:"integration_key"`
	DisplayName             string          `json:"display_name"`
	Description             string          `json:"description"`
	Status                  string          `json:"status"`
	ActorID                 string          `json:"actor_id"`
	MainAuthLevel           *int            `json:"main_auth_level,omitempty"`
	AllowedScopesJSON       json.RawMessage `json:"allowed_scopes_json"`
	AllowedProjectsJSON     json.RawMessage `json:"allowed_projects_json"`
	AllowedEndpointRefsJSON json.RawMessage `json:"allowed_endpoint_refs_json"`
	CreatedByActorID        string          `json:"created_by_actor_id"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	RevokedAt               *time.Time      `json:"revoked_at,omitempty"`
	RevokedReason           *string         `json:"revoked_reason,omitempty"`
	MetadataJSON            json.RawMessage `json:"metadata_json"`
}

type IntegrationAuthProfile struct {
	AuthProfileID           string          `json:"auth_profile_id"`
	IntegrationID           string          `json:"integration_id"`
	DisplayName             string          `json:"display_name"`
	AuthKind                string          `json:"auth_kind"`
	Status                  string          `json:"status"`
	TokenLastFour           string          `json:"token_last_four,omitempty"`
	AllowedEndpointRefsJSON json.RawMessage `json:"allowed_endpoint_refs_json"`
	CreatedByActorID        string          `json:"created_by_actor_id"`
	CreatedAt               time.Time       `json:"created_at"`
	UpdatedAt               time.Time       `json:"updated_at"`
	RevokedAt               *time.Time      `json:"revoked_at,omitempty"`
	RevokedReason           *string         `json:"revoked_reason,omitempty"`
	MetadataJSON            json.RawMessage `json:"metadata_json"`
}

type IntegrationAuthProfileCreateResult struct {
	Profile IntegrationAuthProfile `json:"profile"`
	Token   string                 `json:"token,omitempty"`
}

type DirectEventEndpoint struct {
	EndpointID          string          `json:"endpoint_id"`
	EndpointSlug        string          `json:"endpoint_slug"`
	DisplayName         string          `json:"display_name"`
	Description         string          `json:"description"`
	Status              string          `json:"status"`
	IntegrationID       string          `json:"integration_id"`
	AutomationID        string          `json:"automation_id"`
	EventType           string          `json:"event_type"`
	EndpointPath        string          `json:"endpoint_path"`
	ResponseMode        string          `json:"response_mode"`
	MappingProfileJSON  json.RawMessage `json:"mapping_profile_json"`
	AuthProfileRefsJSON json.RawMessage `json:"auth_profile_refs_json"`
	CreatedByActorID    string          `json:"created_by_actor_id"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	DisabledAt          *time.Time      `json:"disabled_at,omitempty"`
	MetadataJSON        json.RawMessage `json:"metadata_json"`
}

type DirectEvent struct {
	DirectEventID      string          `json:"direct_event_id"`
	EndpointID         string          `json:"endpoint_id"`
	IntegrationID      string          `json:"integration_id"`
	AutomationID       string          `json:"automation_id"`
	Status             string          `json:"status"`
	ExternalEventID    string          `json:"external_event_id"`
	IdempotencyKey     string          `json:"idempotency_key"`
	RequestMethod      string          `json:"request_method"`
	RequestPath        string          `json:"request_path"`
	PayloadHash        string          `json:"payload_hash"`
	AttemptCount       int             `json:"attempt_count"`
	IngestWorkerRunID  *string         `json:"ingest_worker_run_id,omitempty"`
	RequestHeadersJSON json.RawMessage `json:"request_headers_json"`
	QueryJSON          json.RawMessage `json:"query_json"`
	RawBodyJSON        json.RawMessage `json:"-"`
	MappedInputJSON    json.RawMessage `json:"mapped_input_json"`
	ResponseJSON       json.RawMessage `json:"response_json"`
	InvocationID       *string         `json:"invocation_id,omitempty"`
	RouteID            *string         `json:"route_id,omitempty"`
	CapabilityCallID   *string         `json:"capability_call_id,omitempty"`
	JobID              *string         `json:"job_id,omitempty"`
	ResultJSON         json.RawMessage `json:"result_json"`
	ResultRefsJSON     json.RawMessage `json:"result_refs_json"`
	FailureCode        *string         `json:"failure_code,omitempty"`
	FailureMessage     *string         `json:"failure_message,omitempty"`
	ReceivedAt         time.Time       `json:"received_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
	MetadataJSON       json.RawMessage `json:"metadata_json"`
}

type IntegrationDetail struct {
	Integration  Integration              `json:"integration"`
	AuthProfiles []IntegrationAuthProfile `json:"auth_profiles,omitempty"`
	Endpoints    []DirectEventEndpoint    `json:"endpoints,omitempty"`
}

type DirectEventEndpointDetail struct {
	Endpoint    DirectEventEndpoint `json:"endpoint"`
	Integration Integration         `json:"integration"`
	Automation  Automation          `json:"automation"`
}

type DirectEventDetail struct {
	DirectEvent DirectEvent         `json:"direct_event"`
	Endpoint    DirectEventEndpoint `json:"endpoint"`
	Integration Integration         `json:"integration"`
	Automation  Automation          `json:"automation"`
	Invocation  *Invocation         `json:"invocation,omitempty"`
}

type DirectEventStatus struct {
	ActiveEndpointCount        int64      `json:"active_endpoint_count"`
	PausedEndpointCount        int64      `json:"paused_endpoint_count"`
	DisabledEndpointCount      int64      `json:"disabled_endpoint_count"`
	ActiveIntegrationCount     int64      `json:"active_integration_count"`
	AcceptedCount              int64      `json:"accepted_count"`
	MappingPendingCount        int64      `json:"mapping_pending_count"`
	InvocationCreatedCount     int64      `json:"invocation_created_count"`
	FailedCount                int64      `json:"failed_count"`
	OldestAcceptedAt           *time.Time `json:"oldest_accepted_at,omitempty"`
	OldestMappingPendingAt     *time.Time `json:"oldest_mapping_pending_at,omitempty"`
	DirectEventIngestWorkerKey string     `json:"direct_event_ingest_worker_key"`
	DispatcherWorkerKey        string     `json:"dispatcher_worker_key"`
}

type IntegrationFilter struct {
	Limit  int
	Status string
}

type IntegrationAuthProfileFilter struct {
	IntegrationRef string
	Limit          int
	Status         string
}

type DirectEventEndpointFilter struct {
	Limit          int
	Status         string
	IntegrationRef string
	AutomationRef  string
	ProjectRef     string
}

type DirectEventFilter struct {
	Limit          int
	Status         string
	EndpointRef    string
	IntegrationRef string
	AutomationRef  string
	ProjectRef     string
}

type CreateIntegrationInput struct {
	IntegrationKey      string          `json:"integration_key"`
	DisplayName         string          `json:"display_name"`
	Description         string          `json:"description,omitempty"`
	MainAuthLevel       int             `json:"main_auth_level,omitempty"`
	AllowedScopes       json.RawMessage `json:"allowed_scopes,omitempty"`
	AllowedProjects     json.RawMessage `json:"allowed_projects,omitempty"`
	AllowedEndpointRefs json.RawMessage `json:"allowed_endpoint_refs,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type UpdateIntegrationStatusInput struct {
	Reason   string          `json:"reason,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type CreateIntegrationAuthProfileInput struct {
	DisplayName         string          `json:"display_name"`
	AuthKind            string          `json:"auth_kind"`
	AllowedEndpointRefs json.RawMessage `json:"allowed_endpoint_refs,omitempty"`
	Metadata            json.RawMessage `json:"metadata,omitempty"`
}

type UpdateIntegrationAuthProfileStatusInput struct {
	Reason   string          `json:"reason,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type CreateDirectEventEndpointInput struct {
	EndpointSlug         string          `json:"endpoint_slug"`
	DisplayName          string          `json:"display_name"`
	Description          string          `json:"description,omitempty"`
	Status               string          `json:"status,omitempty"`
	IntegrationRef       string          `json:"integration_ref"`
	EventType            string          `json:"event_type"`
	TargetCapability     string          `json:"target_capability"`
	ResponseMode         string          `json:"response_mode,omitempty"`
	MappingProfile       json.RawMessage `json:"mapping_profile,omitempty"`
	IdempotencyProfile   json.RawMessage `json:"idempotency_profile,omitempty"`
	CommunicationProfile json.RawMessage `json:"communication_profile,omitempty"`
	StorageProfile       json.RawMessage `json:"storage_profile,omitempty"`
	TimeoutSeconds       int             `json:"timeout_seconds,omitempty"`
	MaxAttempts          int             `json:"max_attempts,omitempty"`
	AuthProfileRefs      json.RawMessage `json:"auth_profile_refs,omitempty"`
	ScopeRef             string          `json:"scope_ref,omitempty"`
	ProjectRef           string          `json:"project_ref,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
}

type UpdateDirectEventEndpointStatusInput struct {
	Reason   string          `json:"reason,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type MappingPreviewInput struct {
	BodyJSON    json.RawMessage `json:"body_json,omitempty"`
	HeadersJSON json.RawMessage `json:"headers_json,omitempty"`
	QueryJSON   json.RawMessage `json:"query_json,omitempty"`
}

type MappingPreviewResult struct {
	Endpoint      DirectEventEndpoint `json:"endpoint"`
	InputJSON     json.RawMessage     `json:"input_json"`
	MissingFields []string            `json:"missing_fields,omitempty"`
	FieldCount    int                 `json:"field_count"`
}

type MappingProfile struct {
	FieldMappings map[string]string `json:"field_mappings,omitempty"`
	Defaults      map[string]any    `json:"defaults,omitempty"`
	Required      []string          `json:"required,omitempty"`
}

type IdempotencyProfile struct {
	Strategy string `json:"strategy"`
	Path     string `json:"path,omitempty"`
	Header   string `json:"header,omitempty"`
}

type DirectEventIntegrationProfile struct {
	IntegrationID  string   `json:"integration_id"`
	IntegrationKey string   `json:"integration_key"`
	EndpointID     string   `json:"endpoint_id"`
	EndpointSlug   string   `json:"endpoint_slug"`
	EventType      string   `json:"event_type"`
	AuthProfileIDs []string `json:"auth_profile_ids,omitempty"`
}

type CommunicationProfile struct {
	ResponseMode           string `json:"response_mode"`
	SyncWaitTimeoutSeconds int    `json:"sync_wait_timeout_seconds,omitempty"`
}

type DirectEventHTTPRequestInput struct {
	EndpointSlug string
	Method       string
	Path         string
	Headers      map[string][]string
	Query        map[string][]string
	Body         []byte
	RemoteAddr   string
}

type IntegrationAuthResult struct {
	Authenticated bool   `json:"authenticated"`
	AuthKind      string `json:"auth_kind,omitempty"`
	AuthProfileID string `json:"auth_profile_id,omitempty"`
	FailureCode   string `json:"failure_code,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

type DirectEventIngestResult struct {
	DirectEvent  DirectEvent         `json:"direct_event"`
	Endpoint     DirectEventEndpoint `json:"endpoint"`
	Integration  Integration         `json:"integration"`
	Automation   Automation          `json:"automation,omitempty"`
	Invocation   *Invocation         `json:"invocation,omitempty"`
	Status       string              `json:"status"`
	Duplicate    bool                `json:"duplicate"`
	ResponseMode string              `json:"response_mode"`
}

type DirectEventRawPayload struct {
	DirectEventID string          `json:"direct_event_id"`
	HeadersJSON   json.RawMessage `json:"headers_json"`
	QueryJSON     json.RawMessage `json:"query_json"`
	BodyJSON      json.RawMessage `json:"body_json"`
	PayloadHash   string          `json:"payload_hash"`
	ReceivedAt    time.Time       `json:"received_at"`
}

type DirectEventIngestRunInput struct {
	Now            time.Time `json:"now,omitempty"`
	BatchSize      int       `json:"batch_size,omitempty"`
	WorkerRunID    string    `json:"worker_run_id,omitempty"`
	DirectEventRef string    `json:"direct_event_ref,omitempty"`
}

type DirectEventIngestRunResult struct {
	Claimed            int64    `json:"claimed"`
	Mapped             int64    `json:"mapped"`
	CreatedInvocations int64    `json:"created_invocations"`
	Duplicates         int64    `json:"duplicates"`
	MappingFailed      int64    `json:"mapping_failed"`
	Failed             int64    `json:"failed"`
	NoWork             bool     `json:"no_work"`
	MoreWork           bool     `json:"more_work"`
	DirectEventIDs     []string `json:"direct_event_ids,omitempty"`
	InvocationIDs      []string `json:"invocation_ids,omitempty"`
}

type DirectEventLocalIngestInput struct {
	BodyJSON    json.RawMessage
	BearerToken string
	QueryToken  string
}
