package routing

import (
	"encoding/json"
	"time"
)

const (
	RouteKindLocal  = "local"
	RouteKindRemote = "remote"

	ExecutionModeImmediate = "immediate"
	ExecutionModeJob       = "job"
	ExecutionModeSession   = "session"
	ExecutionModeStream    = "stream"

	RouteStatusPlanned            = "planned"
	RouteStatusAuthorized         = "authorized"
	RouteStatusWaitingForApproval = "waiting_for_approval"
	RouteStatusQueued             = "queued"
	RouteStatusDispatched         = "dispatched"
	RouteStatusExecuting          = "executing"
	RouteStatusCompleted          = "completed"
	RouteStatusFailed             = "failed"
	RouteStatusCancelled          = "cancelled"
	RouteStatusExpired            = "expired"

	CapabilityCallStatusPlanned          = "planned"
	CapabilityCallStatusApprovalRequired = "approval_required"
	CapabilityCallStatusAuthorized       = "authorized"
	CapabilityCallStatusDispatched       = "dispatched"
	CapabilityCallStatusExecuting        = "executing"
	CapabilityCallStatusCompleted        = "completed"
	CapabilityCallStatusFailed           = "failed"
	CapabilityCallStatusCancelled        = "cancelled"
)

type Route struct {
	RouteID              string          `json:"route_id"`
	RouteKey             string          `json:"route_key"`
	CorrelationID        string          `json:"correlation_id"`
	ActorID              string          `json:"actor_id"`
	OriginNodeID         string          `json:"origin_node_id"`
	OriginScopeID        *string         `json:"origin_scope_id,omitempty"`
	RuntimeNodeID        *string         `json:"runtime_node_id,omitempty"`
	TargetNodeID         string          `json:"target_node_id"`
	ProviderID           string          `json:"provider_id"`
	CapabilityEndpointID string          `json:"capability_endpoint_id"`
	CapabilityClassID    *string         `json:"capability_class_id,omitempty"`
	PolicyDecisionID     *string         `json:"policy_decision_id,omitempty"`
	ApprovalID           *string         `json:"approval_id,omitempty"`
	GrantID              *string         `json:"grant_id,omitempty"`
	JobID                *string         `json:"job_id,omitempty"`
	RouteKind            string          `json:"route_kind"`
	ExecutionMode        string          `json:"execution_mode"`
	SelectedPathJSON     json.RawMessage `json:"selected_path_json"`
	RequestSummaryJSON   json.RawMessage `json:"request_summary_json"`
	ResultTargetJSON     json.RawMessage `json:"result_target_json"`
	Status               string          `json:"status"`
	FailureCode          *string         `json:"failure_code,omitempty"`
	FailureMessage       *string         `json:"failure_message,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	AuthorizedAt         *time.Time      `json:"authorized_at,omitempty"`
	DispatchedAt         *time.Time      `json:"dispatched_at,omitempty"`
	StartedAt            *time.Time      `json:"started_at,omitempty"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	FailedAt             *time.Time      `json:"failed_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
}

type CapabilityCall struct {
	CapabilityCallID     string          `json:"capability_call_id"`
	CapabilityCallKey    string          `json:"capability_call_key"`
	RouteID              string          `json:"route_id"`
	CorrelationID        string          `json:"correlation_id"`
	IdempotencyKey       *string         `json:"idempotency_key,omitempty"`
	ActorID              string          `json:"actor_id"`
	OriginNodeID         string          `json:"origin_node_id"`
	ScopeID              *string         `json:"scope_id,omitempty"`
	TargetNodeID         string          `json:"target_node_id"`
	ProviderID           string          `json:"provider_id"`
	CapabilityEndpointID string          `json:"capability_endpoint_id"`
	Operation            string          `json:"operation"`
	ExecutionMode        string          `json:"execution_mode"`
	PolicyDecisionID     *string         `json:"policy_decision_id,omitempty"`
	ApprovalID           *string         `json:"approval_id,omitempty"`
	GrantID              *string         `json:"grant_id,omitempty"`
	JobID                *string         `json:"job_id,omitempty"`
	Status               string          `json:"status"`
	InputJSON            json.RawMessage `json:"input_json"`
	InputSummaryJSON     json.RawMessage `json:"input_summary_json"`
	ResultJSON           json.RawMessage `json:"result_json"`
	ResultRefsJSON       json.RawMessage `json:"result_refs_json"`
	ErrorCode            *string         `json:"error_code,omitempty"`
	ErrorMessage         *string         `json:"error_message,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	FailedAt             *time.Time      `json:"failed_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
}

type RouteFilter struct {
	Limit                 int
	Status                string
	ActorRef              string
	OriginNodeRef         string
	OriginScopeRef        string
	RuntimeNodeRef        string
	TargetNodeRef         string
	ProviderRef           string
	CapabilityEndpointRef string
	PolicyDecisionRef     string
	ApprovalRef           string
	GrantRef              string
	JobRef                string
	CorrelationID         string
}

type CapabilityCallFilter struct {
	Limit                 int
	Status                string
	ActorRef              string
	OriginNodeRef         string
	ScopeRef              string
	TargetNodeRef         string
	ProviderRef           string
	CapabilityEndpointRef string
	PolicyDecisionRef     string
	ApprovalRef           string
	GrantRef              string
	JobRef                string
	CorrelationID         string
	IdempotencyKey        string
}

type CapabilityCallInput struct {
	Target          string          `json:"target"`
	ActorRef        string          `json:"actor_ref,omitempty"`
	OriginNodeRef   string          `json:"origin_node_ref,omitempty"`
	ScopeRef        string          `json:"scope_ref,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	ResultTarget    json.RawMessage `json:"result_target,omitempty"`
	RequestApproval bool            `json:"request_approval,omitempty"`
	ApprovalReason  string          `json:"approval_reason,omitempty"`
	DryRun          bool            `json:"dry_run,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type CapabilityCallOutcome struct {
	Route             Route           `json:"route"`
	CapabilityCall    CapabilityCall  `json:"capability_call"`
	PolicyDecisionID  string          `json:"policy_decision_id,omitempty"`
	ApprovalID        string          `json:"approval_id,omitempty"`
	GrantID           string          `json:"grant_id,omitempty"`
	JobID             string          `json:"job_id,omitempty"`
	DispatchMessageID string          `json:"dispatch_message_id,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	ResultRefs        json.RawMessage `json:"result_refs,omitempty"`
	EventIDs          []string        `json:"event_ids,omitempty"`
	Status            string          `json:"status"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
}

type RoutePlan struct {
	RouteKind               string          `json:"route_kind"`
	ExecutionMode           string          `json:"execution_mode"`
	ActorID                 string          `json:"actor_id"`
	OriginNodeID            string          `json:"origin_node_id"`
	OriginScopeID           string          `json:"origin_scope_id,omitempty"`
	RuntimeNodeID           string          `json:"runtime_node_id,omitempty"`
	TargetNodeID            string          `json:"target_node_id"`
	ProviderID              string          `json:"provider_id"`
	ProviderAddress         string          `json:"provider_address"`
	CapabilityEndpointID    string          `json:"capability_endpoint_id"`
	CapabilityAddress       string          `json:"capability_address"`
	ActiveEndpointVersionID string          `json:"active_endpoint_version_id,omitempty"`
	CapabilityClassID       string          `json:"capability_class_id,omitempty"`
	SelectedPathJSON        json.RawMessage `json:"selected_path_json"`
	RequestSummaryJSON      json.RawMessage `json:"request_summary_json"`
	ResultTargetJSON        json.RawMessage `json:"result_target_json"`
}

type ExecutionContext struct {
	RouteID                 string `json:"route_id"`
	CapabilityCallID        string `json:"capability_call_id"`
	CorrelationID           string `json:"correlation_id"`
	ActorID                 string `json:"actor_id"`
	OriginNodeID            string `json:"origin_node_id"`
	ScopeID                 string `json:"scope_id,omitempty"`
	TargetNodeID            string `json:"target_node_id"`
	ProviderID              string `json:"provider_id"`
	CapabilityEndpointID    string `json:"capability_endpoint_id"`
	ActiveEndpointVersionID string `json:"active_endpoint_version_id,omitempty"`
	Operation               string `json:"operation"`
	PolicyDecisionID        string `json:"policy_decision_id,omitempty"`
	GrantID                 string `json:"grant_id,omitempty"`
}

type ExecutionResult struct {
	Status     string          `json:"status"`
	Result     json.RawMessage `json:"result,omitempty"`
	ResultRefs json.RawMessage `json:"result_refs,omitempty"`
	JobID      string          `json:"job_id,omitempty"`
	EventIDs   []string        `json:"event_ids,omitempty"`
}

type RuntimeBindingSnapshot struct {
	RuntimeBindingID            string          `json:"runtime_binding_id"`
	CapabilityEndpointVersionID string          `json:"capability_endpoint_version_id"`
	RuntimeKind                 string          `json:"runtime_kind"`
	RuntimeConfigJSON           json.RawMessage `json:"runtime_config_json"`
	InputMappingJSON            json.RawMessage `json:"input_mapping_json"`
	OutputMappingJSON           json.RawMessage `json:"output_mapping_json"`
	Status                      string          `json:"status"`
	ImplementationHash          string          `json:"implementation_hash,omitempty"`
	VersionLabel                string          `json:"version_label,omitempty"`
	SnapshotHash                string          `json:"snapshot_hash,omitempty"`
	CapturedAt                  time.Time       `json:"captured_at"`
}

type RemoteDispatchPayload struct {
	RouteID              string                  `json:"route_id"`
	CapabilityCallID     string                  `json:"capability_call_id"`
	TargetNodeID         string                  `json:"target_node_id"`
	ProviderID           string                  `json:"provider_id"`
	ProviderAddress      string                  `json:"provider_address"`
	CapabilityEndpointID string                  `json:"capability_endpoint_id"`
	CapabilityAddress    string                  `json:"capability_address"`
	Operation            string                  `json:"operation"`
	ActorID              string                  `json:"actor_id"`
	OriginNodeID         string                  `json:"origin_node_id"`
	ScopeID              string                  `json:"scope_id,omitempty"`
	PolicyDecisionID     string                  `json:"policy_decision_id,omitempty"`
	GrantID              string                  `json:"grant_id,omitempty"`
	Input                json.RawMessage         `json:"input"`
	IdempotencyKey       string                  `json:"idempotency_key,omitempty"`
	RequestedAt          time.Time               `json:"requested_at"`
	RuntimeBinding       *RuntimeBindingSnapshot `json:"runtime_binding,omitempty"`
	Metadata             json.RawMessage         `json:"metadata"`
}

type RemoteResultInput struct {
	NodeRef         string              `json:"node_ref"`
	CredentialToken string              `json:"credential_token,omitempty"`
	IdempotencyKey  string              `json:"idempotency_key,omitempty"`
	Payload         RemoteResultPayload `json:"payload"`
	Metadata        json.RawMessage     `json:"metadata,omitempty"`
}

type RemoteResultPayload struct {
	RouteID              string          `json:"route_id"`
	CapabilityCallID     string          `json:"capability_call_id"`
	NodeID               string          `json:"node_id"`
	ProviderID           string          `json:"provider_id"`
	ProviderAddress      string          `json:"provider_address,omitempty"`
	CapabilityEndpointID string          `json:"capability_endpoint_id"`
	CapabilityAddress    string          `json:"capability_address,omitempty"`
	Operation            string          `json:"operation,omitempty"`
	ExecutionStatus      string          `json:"execution_status"`
	ResultJSON           json.RawMessage `json:"result,omitempty"`
	ResultRefsJSON       json.RawMessage `json:"result_refs,omitempty"`
	ErrorCode            string          `json:"error_code,omitempty"`
	ErrorMessage         string          `json:"error_message,omitempty"`
	StartedAt            *time.Time      `json:"started_at,omitempty"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	RuntimeMetadataJSON  json.RawMessage `json:"runtime_metadata,omitempty"`
}

type RemoteResultOutcome struct {
	MessageID      string          `json:"message_id"`
	Route          Route           `json:"route"`
	CapabilityCall CapabilityCall  `json:"capability_call"`
	EventIDs       []string        `json:"event_ids,omitempty"`
	Status         string          `json:"status"`
	Result         json.RawMessage `json:"result,omitempty"`
	ResultRefs     json.RawMessage `json:"result_refs,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
	ErrorMessage   string          `json:"error_message,omitempty"`
	Idempotent     bool            `json:"idempotent,omitempty"`
}
