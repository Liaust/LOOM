package agents

import (
	"encoding/json"
	"time"

	"loom.local/loom/internal/routing"
)

const (
	AccessSessionStatusActive    = "active"
	AccessSessionStatusCompleted = "completed"
	AccessSessionStatusCancelled = "cancelled"
	AccessSessionStatusFailed    = "failed"
	AccessSessionStatusExpired   = "expired"
	AccessSessionStatusRevoked   = "revoked"

	WorkContextStatusActive    = "active"
	WorkContextStatusCompleted = "completed"
	WorkContextStatusCancelled = "cancelled"
	WorkContextStatusFailed    = "failed"
	WorkContextStatusExpired   = "expired"
	WorkContextStatusRevoked   = "revoked"

	ToolViewStatusActive      = "active"
	ToolViewStatusInvalidated = "invalidated"
	ToolViewStatusExpired     = "expired"

	EntryKindOperatingTool = "operating_tool"
	EntryKindCapability    = "capability"

	VisibilityVisible     = "visible"
	VisibilityRequestable = "requestable"
	VisibilityRedacted    = "redacted"

	SourceLayerOperating    = "operating"
	SourceLayerHome         = "home"
	SourceLayerCurrentScope = "current_scope"
	SourceLayerMounted      = "mounted"
	SourceLayerTaskMount    = "task_mount"
	SourceLayerGrant        = "grant"
	SourceLayerSearch       = "search_candidate"

	OriginKindLocalCLI       = "local_cli"
	OriginKindRemoteAPI      = "remote_api"
	OriginKindExternalClient = "external_client"
	OriginKindService        = "service"
	OriginKindManual         = "manual"

	DefaultToolViewMaxEntries                  = 40
	DefaultToolViewSchemaBudget                = 12
	DefaultToolViewDirectCapabilitySchemaLimit = 4

	AgentToolCallStatusPlanned          = "planned"
	AgentToolCallStatusApprovalRequired = "approval_required"
	AgentToolCallStatusDispatched       = "dispatched"
	AgentToolCallStatusCompleted        = "completed"
	AgentToolCallStatusFailed           = "failed"
	AgentToolCallStatusDenied           = "denied"

	WorklogEntryKindNote        = "note"
	WorklogEntryKindObservation = "observation"
	WorklogEntryKindDecision    = "decision"
	WorklogEntryKindResult      = "result"
	WorklogEntryKindArtifactRef = "artifact_ref"
)

type AccessSession struct {
	AgentAccessSessionID string          `json:"agent_access_session_id"`
	AccessSessionKey     string          `json:"access_session_key"`
	ActorID              string          `json:"actor_id"`
	CreatedByActorID     string          `json:"created_by_actor_id"`
	OriginKind           string          `json:"origin_kind"`
	OriginNodeID         *string         `json:"origin_node_id,omitempty"`
	OriginClientID       string          `json:"origin_client_id"`
	RuntimeNodeID        *string         `json:"runtime_node_id,omitempty"`
	HomeNodeID           *string         `json:"home_node_id,omitempty"`
	CurrentScopeID       *string         `json:"current_scope_id,omitempty"`
	ActiveProjectID      *string         `json:"active_project_id,omitempty"`
	Status               string          `json:"status"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ExpiresAt            *time.Time      `json:"expires_at,omitempty"`
	ClosedAt             *time.Time      `json:"closed_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
}

type WorkContext struct {
	AgentWorkContextID   string          `json:"agent_work_context_id"`
	WorkContextKey       string          `json:"work_context_key"`
	AgentAccessSessionID string          `json:"agent_access_session_id"`
	ActorID              string          `json:"actor_id"`
	CreatedByActorID     string          `json:"created_by_actor_id"`
	Objective            string          `json:"objective"`
	CurrentScopeID       *string         `json:"current_scope_id,omitempty"`
	ActiveProjectID      *string         `json:"active_project_id,omitempty"`
	RuntimeNodeID        *string         `json:"runtime_node_id,omitempty"`
	HomeNodeID           *string         `json:"home_node_id,omitempty"`
	ActiveToolViewID     *string         `json:"active_tool_view_id,omitempty"`
	Status               string          `json:"status"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	ClosedAt             *time.Time      `json:"closed_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
}

type WorkContextDetail struct {
	WorkContext   WorkContext     `json:"work_context"`
	AccessSession AccessSession   `json:"access_session"`
	ToolView      *ToolViewDetail `json:"tool_view,omitempty"`
}

type ToolView struct {
	ToolViewID                   string          `json:"tool_view_id"`
	AgentAccessSessionID         *string         `json:"agent_access_session_id,omitempty"`
	AgentWorkContextID           *string         `json:"agent_work_context_id,omitempty"`
	ActorID                      string          `json:"actor_id"`
	RuntimeNodeID                *string         `json:"runtime_node_id,omitempty"`
	HomeNodeID                   *string         `json:"home_node_id,omitempty"`
	CurrentScopeID               *string         `json:"current_scope_id,omitempty"`
	ActiveProjectID              *string         `json:"active_project_id,omitempty"`
	Version                      int             `json:"version"`
	Status                       string          `json:"status"`
	MaxEntries                   int             `json:"max_entries"`
	SchemaBudget                 int             `json:"schema_budget"`
	DirectCapabilitySchemaBudget int             `json:"direct_capability_schema_budget"`
	SourceHash                   string          `json:"source_hash"`
	CreatedAt                    time.Time       `json:"created_at"`
	InvalidatedAt                *time.Time      `json:"invalidated_at,omitempty"`
	Metadata                     json.RawMessage `json:"metadata"`
}

type ToolViewEntry struct {
	ToolViewEntryID             string          `json:"tool_view_entry_id"`
	ToolViewID                  string          `json:"tool_view_id"`
	EntryKind                   string          `json:"entry_kind"`
	ToolName                    string          `json:"tool_name"`
	VisibilityState             string          `json:"visibility_state"`
	SourceLayer                 string          `json:"source_layer"`
	ReasonCode                  string          `json:"reason_code"`
	CapabilityEndpointID        *string         `json:"capability_endpoint_id,omitempty"`
	CapabilityAddress           string          `json:"capability_address"`
	ProviderID                  *string         `json:"provider_id,omitempty"`
	ProviderAddress             string          `json:"provider_address"`
	TargetNodeID                *string         `json:"target_node_id,omitempty"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description"`
	RiskLevel                   *string         `json:"risk_level,omitempty"`
	ExecutionAuthorizationLevel *int            `json:"execution_authorization_level,omitempty"`
	ActorAuthorizationLevel     *int            `json:"actor_authorization_level,omitempty"`
	ApprovalHint                string          `json:"approval_hint"`
	AvailabilityHint            string          `json:"availability_hint"`
	MatchedUseSummary           string          `json:"matched_use_summary"`
	CompactMetadata             json.RawMessage `json:"compact_metadata"`
	SchemaSummaryJSON           json.RawMessage `json:"schema_summary_json"`
	CreatedAt                   time.Time       `json:"created_at"`
}

type ToolViewDetail struct {
	ToolView ToolView        `json:"tool_view"`
	Entries  []ToolViewEntry `json:"entries"`
}

type CreateAccessSessionInput struct {
	AccessSessionKey string          `json:"access_session_key,omitempty"`
	ActorRef         string          `json:"actor_ref"`
	OriginKind       string          `json:"origin_kind,omitempty"`
	OriginNodeRef    string          `json:"origin_node_ref,omitempty"`
	OriginClientID   string          `json:"origin_client_id,omitempty"`
	RuntimeNodeRef   string          `json:"runtime_node_ref,omitempty"`
	HomeNodeRef      string          `json:"home_node_ref,omitempty"`
	ScopeRef         string          `json:"scope_ref,omitempty"`
	ProjectRef       string          `json:"project_ref,omitempty"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type AccessSessionFilter struct {
	Limit    int
	ActorRef string
	Status   string
}

type CreateWorkContextInput struct {
	WorkContextKey   string          `json:"work_context_key,omitempty"`
	AccessSessionRef string          `json:"access_session_ref"`
	Objective        string          `json:"objective"`
	ScopeRef         string          `json:"scope_ref,omitempty"`
	ProjectRef       string          `json:"project_ref,omitempty"`
	RuntimeNodeRef   string          `json:"runtime_node_ref,omitempty"`
	HomeNodeRef      string          `json:"home_node_ref,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type WorkContextFilter struct {
	Limit            int
	ActorRef         string
	AccessSessionRef string
	Status           string
}

type ToolSearchInput struct {
	WorkContextRef     string          `json:"work_context_ref,omitempty"`
	Query              string          `json:"query"`
	Limit              int             `json:"limit,omitempty"`
	IncludeRequestable *bool           `json:"include_requestable,omitempty"`
	IncludeRedacted    bool            `json:"include_redacted,omitempty"`
	Form               string          `json:"form,omitempty"`
	ProviderRef        string          `json:"provider_ref,omitempty"`
	ScopeRef           string          `json:"scope_ref,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

type ToolSearchResult struct {
	AgentWorkContextID string                `json:"agent_work_context_id"`
	ToolViewID         string                `json:"tool_view_id"`
	Query              string                `json:"query"`
	Candidates         []ToolSearchCandidate `json:"candidates"`
	Message            string                `json:"message,omitempty"`
}

type ToolSearchCandidate struct {
	ToolViewEntryID             string          `json:"tool_view_entry_id"`
	ToolName                    string          `json:"tool_name"`
	CapabilityAddress           string          `json:"capability_address"`
	ProviderAddress             string          `json:"provider_address"`
	TargetNodeID                *string         `json:"target_node_id,omitempty"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description"`
	MatchedUseSummary           string          `json:"matched_use_summary"`
	MatchedUsageDocumentID      string          `json:"matched_usage_document_id,omitempty"`
	MatchedUsageSectionLabel    string          `json:"matched_usage_section_label,omitempty"`
	Tags                        []string        `json:"tags,omitempty"`
	RiskLevel                   *string         `json:"risk_level,omitempty"`
	ExecutionAuthorizationLevel *int            `json:"execution_authorization_level,omitempty"`
	ActorAuthorizationLevel     *int            `json:"actor_authorization_level,omitempty"`
	VisibilityState             string          `json:"visibility_state"`
	ApprovalHint                string          `json:"approval_hint"`
	AvailabilityHint            string          `json:"availability_hint"`
	PresenceHint                string          `json:"presence_hint"`
	Score                       float64         `json:"score"`
	MatchReasons                []string        `json:"match_reasons,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type ToolInspectInput struct {
	WorkContextRef     string `json:"work_context_ref,omitempty"`
	ToolRef            string `json:"tool_ref,omitempty"`
	Query              string `json:"query,omitempty"`
	UsageSectionLabel  string `json:"usage_section_label,omitempty"`
	MaxSections        int    `json:"max_sections,omitempty"`
	MaxCharsPerSection int    `json:"max_chars_per_section,omitempty"`
}

type ToolInspection struct {
	AgentWorkContextID string          `json:"agent_work_context_id"`
	ToolViewID         string          `json:"tool_view_id"`
	Entry              ToolViewEntry   `json:"entry"`
	ToolSchema         ToolSchema      `json:"tool_schema"`
	UsageSections      []UsageSection  `json:"usage_sections,omitempty"`
	PolicyHint         ToolPolicyHint  `json:"policy_hint"`
	AvailabilityHint   string          `json:"availability_hint"`
	PresenceHint       string          `json:"presence_hint"`
	RouteHint          string          `json:"route_hint"`
	Metadata           json.RawMessage `json:"metadata"`
}

type ToolSchema struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
	Loom         ToolSchemaLOOM  `json:"loom"`
}

type ToolSchemaLOOM struct {
	CapabilityEndpointID        string  `json:"capability_endpoint_id,omitempty"`
	CapabilityAddress           string  `json:"capability_address,omitempty"`
	ProviderID                  string  `json:"provider_id,omitempty"`
	ProviderAddress             string  `json:"provider_address,omitempty"`
	TargetNodeID                *string `json:"target_node_id,omitempty"`
	EntryKind                   string  `json:"entry_kind"`
	ToolViewEntryID             string  `json:"tool_view_entry_id"`
	ToolViewID                  string  `json:"tool_view_id"`
	ExecutionAuthorizationLevel *int    `json:"execution_authorization_level,omitempty"`
	ActorAuthorizationLevel     *int    `json:"actor_authorization_level,omitempty"`
	RiskLevel                   *string `json:"risk_level,omitempty"`
	VisibilityState             string  `json:"visibility_state"`
	CallVia                     string  `json:"call_via"`
}

type UsageSection struct {
	UsageDocumentID string `json:"usage_document_id"`
	Title           string `json:"title"`
	SectionLabel    string `json:"section_label"`
	SectionHeading  string `json:"section_heading"`
	Excerpt         string `json:"excerpt"`
	MatchReason     string `json:"match_reason"`
}

type ToolPolicyHint struct {
	VisibilityState string `json:"visibility_state"`
	ApprovalHint    string `json:"approval_hint"`
	ReasonCode      string `json:"reason_code"`
}

type AgentToolCall struct {
	AgentToolCallID      string          `json:"agent_tool_call_id"`
	AgentAccessSessionID *string         `json:"agent_access_session_id,omitempty"`
	AgentWorkContextID   *string         `json:"agent_work_context_id,omitempty"`
	ToolViewID           *string         `json:"tool_view_id,omitempty"`
	ToolViewEntryID      *string         `json:"tool_view_entry_id,omitempty"`
	ActorID              string          `json:"actor_id"`
	ToolName             string          `json:"tool_name"`
	CapabilityEndpointID *string         `json:"capability_endpoint_id,omitempty"`
	CapabilityAddress    string          `json:"capability_address"`
	RouteID              *string         `json:"route_id,omitempty"`
	CapabilityCallID     *string         `json:"capability_call_id,omitempty"`
	PolicyDecisionID     *string         `json:"policy_decision_id,omitempty"`
	ApprovalID           *string         `json:"approval_id,omitempty"`
	GrantID              *string         `json:"grant_id,omitempty"`
	IdempotencyKey       string          `json:"idempotency_key"`
	Status               string          `json:"status"`
	CreatedAt            time.Time       `json:"created_at"`
	CompletedAt          *time.Time      `json:"completed_at,omitempty"`
	Metadata             json.RawMessage `json:"metadata"`
}

type AgentToolCallInput struct {
	WorkContextRef  string          `json:"work_context_ref,omitempty"`
	ToolRef         string          `json:"tool_ref,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	RequestApproval bool            `json:"request_approval,omitempty"`
	ApprovalReason  string          `json:"approval_reason,omitempty"`
	IdempotencyKey  string          `json:"idempotency_key,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type AgentToolCallOutcome struct {
	AgentToolCallID  string                         `json:"agent_tool_call_id"`
	Status           string                         `json:"status"`
	ReasonCode       string                         `json:"reason_code,omitempty"`
	SafeExplanation  string                         `json:"safe_explanation,omitempty"`
	NextActionHint   string                         `json:"next_action_hint,omitempty"`
	AgentToolCall    AgentToolCall                  `json:"agent_tool_call"`
	ToolViewEntry    ToolViewEntry                  `json:"tool_view_entry"`
	RoutingOutcome   *routing.CapabilityCallOutcome `json:"routing_outcome,omitempty"`
	OperatingResult  json.RawMessage                `json:"operating_result,omitempty"`
	ApprovalID       string                         `json:"approval_id,omitempty"`
	PolicyDecisionID string                         `json:"policy_decision_id,omitempty"`
	CapabilityCallID string                         `json:"capability_call_id,omitempty"`
	RouteID          string                         `json:"route_id,omitempty"`
	WorklogEntryID   string                         `json:"worklog_entry_id,omitempty"`
}

type AgentToolCallFilter struct {
	Limit          int
	WorkContextRef string
	Status         string
}

type WorklogEntry struct {
	WorklogEntryID       string          `json:"worklog_entry_id"`
	AgentAccessSessionID *string         `json:"agent_access_session_id,omitempty"`
	AgentWorkContextID   string          `json:"agent_work_context_id"`
	ActorID              string          `json:"actor_id"`
	EntryKind            string          `json:"entry_kind"`
	Summary              string          `json:"summary"`
	Body                 string          `json:"body"`
	ObjectID             *string         `json:"object_id,omitempty"`
	VisibilityClass      string          `json:"visibility_class"`
	CreatedAt            time.Time       `json:"created_at"`
	Metadata             json.RawMessage `json:"metadata"`
}

type WriteWorklogInput struct {
	WorkContextRef  string          `json:"work_context_ref,omitempty"`
	EntryKind       string          `json:"entry_kind,omitempty"`
	Summary         string          `json:"summary"`
	Body            string          `json:"body,omitempty"`
	ObjectRef       string          `json:"object_ref,omitempty"`
	VisibilityClass string          `json:"visibility_class,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

type WorklogFilter struct {
	Limit          int
	WorkContextRef string
}
