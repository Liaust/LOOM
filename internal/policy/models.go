package policy

import (
	"encoding/json"
	"time"
)

const (
	DecisionAllow            = "allow"
	DecisionDeny             = "deny"
	DecisionApprovalRequired = "approval_required"

	ApprovalDecisionApprove = "approve"
	ApprovalDecisionDeny    = "deny"

	ApprovalPending    = "pending"
	ApprovalApproved   = "approved"
	ApprovalDenied     = "denied"
	ApprovalExpired    = "expired"
	ApprovalCancelled  = "cancelled"
	ApprovalSuperseded = "superseded"

	GrantActive    = "active"
	GrantConsumed  = "consumed"
	GrantExpired   = "expired"
	GrantRevoked   = "revoked"
	GrantSuspended = "suspended"

	GrantOneShot       = "one_shot"
	GrantElevation     = "elevation"
	GrantJob           = "job"
	GrantWorkflow      = "workflow"
	GrantProject       = "project"
	GrantRoute         = "route"
	GrantTransfer      = "transfer"
	GrantCredentialUse = "credential_use"
	GrantBreakGlass    = "break_glass"

	RiskLow      = "low"
	RiskMedium   = "medium"
	RiskHigh     = "high"
	RiskCritical = "critical"
)

type Decision struct {
	PolicyDecisionID            string          `json:"policy_decision_id"`
	DecisionKey                 *string         `json:"decision_key,omitempty"`
	ActorID                     *string         `json:"actor_id,omitempty"`
	OriginNodeID                *string         `json:"origin_node_id,omitempty"`
	TargetNodeID                *string         `json:"target_node_id,omitempty"`
	ScopeID                     *string         `json:"scope_id,omitempty"`
	ResourceKind                string          `json:"resource_kind"`
	ResourceID                  *string         `json:"resource_id,omitempty"`
	Operation                   string          `json:"operation"`
	CapabilityEndpointID        *string         `json:"capability_endpoint_id,omitempty"`
	ProviderID                  *string         `json:"provider_id,omitempty"`
	RiskLevel                   *string         `json:"risk_level,omitempty"`
	ExecutionAuthorizationLevel *int            `json:"execution_authorization_level,omitempty"`
	ActorAuthorizationLevel     *int            `json:"actor_authorization_level,omitempty"`
	Decision                    string          `json:"decision"`
	ReasonCode                  string          `json:"reason_code"`
	SafeExplanation             string          `json:"safe_explanation"`
	ApprovalID                  *string         `json:"approval_id,omitempty"`
	GrantID                     *string         `json:"grant_id,omitempty"`
	ContextHash                 string          `json:"context_hash"`
	ContextSummary              json.RawMessage `json:"context_summary"`
	CreatedAt                   time.Time       `json:"created_at"`
	ExpiresAt                   *time.Time      `json:"expires_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type DecisionInput struct {
	Operation             string          `json:"operation"`
	ActorRef              string          `json:"actor_ref,omitempty"`
	OriginNodeRef         string          `json:"origin_node_ref,omitempty"`
	ScopeRef              string          `json:"scope_ref,omitempty"`
	CreateApprovalRequest bool            `json:"create_approval_request,omitempty"`
	ApprovalReason        string          `json:"approval_reason,omitempty"`
	ApprovalTTL           time.Duration   `json:"-"`
	ApprovalTTLSeconds    int             `json:"approval_ttl_seconds,omitempty"`
	Metadata              json.RawMessage `json:"metadata,omitempty"`
}

type PolicyExplanation struct {
	Decision Decision      `json:"decision"`
	Approval *Approval     `json:"approval,omitempty"`
	Grant    *Grant        `json:"grant,omitempty"`
	Target   PolicyTarget  `json:"target"`
	Context  PolicyContext `json:"context"`
}

type PolicyTarget struct {
	Operation            string `json:"operation"`
	ResourceKind         string `json:"resource_kind"`
	CapabilityAddress    string `json:"capability_address,omitempty"`
	CapabilityEndpointID string `json:"capability_endpoint_id,omitempty"`
	ProviderAddress      string `json:"provider_address,omitempty"`
	ProviderID           string `json:"provider_id,omitempty"`
	TargetNodeID         string `json:"target_node_id,omitempty"`
	TargetNodeKey        string `json:"target_node_key,omitempty"`
}

type DecisionFilter struct {
	Limit                 int
	ActorRef              string
	OriginNodeRef         string
	TargetNodeRef         string
	Operation             string
	Decision              string
	CapabilityEndpointRef string
	ApprovalRef           string
	GrantRef              string
}

type PolicyContext struct {
	ActorID                     string          `json:"actor_id"`
	ActorKey                    string          `json:"actor_key,omitempty"`
	OriginNodeID                string          `json:"origin_node_id"`
	OriginNodeKey               string          `json:"origin_node_key,omitempty"`
	TargetNodeID                string          `json:"target_node_id,omitempty"`
	TargetNodeKey               string          `json:"target_node_key,omitempty"`
	ScopeID                     string          `json:"scope_id,omitempty"`
	ScopeKey                    string          `json:"scope_key,omitempty"`
	ResourceKind                string          `json:"resource_kind"`
	ResourceID                  string          `json:"resource_id,omitempty"`
	Operation                   string          `json:"operation"`
	CapabilityEndpointID        string          `json:"capability_endpoint_id,omitempty"`
	CapabilityAddress           string          `json:"capability_address,omitempty"`
	ProviderID                  string          `json:"provider_id,omitempty"`
	ProviderAddress             string          `json:"provider_address,omitempty"`
	RiskLevel                   string          `json:"risk_level,omitempty"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level,omitempty"`
	ActorAuthorizationLevel     int             `json:"actor_authorization_level,omitempty"`
	GrantID                     string          `json:"grant_id,omitempty"`
	CorrelationID               string          `json:"correlation_id,omitempty"`
	Metadata                    json.RawMessage `json:"metadata,omitempty"`
}

type Approval struct {
	ApprovalID                  string          `json:"approval_id"`
	ApprovalKey                 string          `json:"approval_key"`
	RequestedByActorID          string          `json:"requested_by_actor_id"`
	ApprovingActorID            *string         `json:"approving_actor_id,omitempty"`
	OriginNodeID                *string         `json:"origin_node_id,omitempty"`
	TargetNodeID                *string         `json:"target_node_id,omitempty"`
	ScopeID                     *string         `json:"scope_id,omitempty"`
	ResourceKind                string          `json:"resource_kind"`
	ResourceID                  *string         `json:"resource_id,omitempty"`
	Operation                   string          `json:"operation"`
	CapabilityEndpointID        *string         `json:"capability_endpoint_id,omitempty"`
	ProviderID                  *string         `json:"provider_id,omitempty"`
	PolicyDecisionID            *string         `json:"policy_decision_id,omitempty"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	RequestReason               string          `json:"request_reason"`
	ActionSummary               string          `json:"action_summary"`
	Status                      string          `json:"status"`
	CreatedAt                   time.Time       `json:"created_at"`
	ExpiresAt                   time.Time       `json:"expires_at"`
	DecidedAt                   *time.Time      `json:"decided_at,omitempty"`
	DecisionReason              *string         `json:"decision_reason,omitempty"`
	ResultingGrantID            *string         `json:"resulting_grant_id,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type ApprovalFilter struct {
	Limit                 int
	Status                string
	ActorRef              string
	ApprovingActorRef     string
	TargetNodeRef         string
	CapabilityEndpointRef string
}

type ApprovalDecisionInput struct {
	ApprovalRef      string        `json:"approval_ref"`
	Decision         string        `json:"decision"`
	DecidingActorRef string        `json:"deciding_actor_ref,omitempty"`
	DecisionReason   string        `json:"decision_reason,omitempty"`
	GrantTTL         time.Duration `json:"-"`
	GrantTTLSeconds  int           `json:"grant_ttl_seconds,omitempty"`
}

type ApprovalDecisionResult struct {
	Approval Approval `json:"approval"`
	Grant    *Grant   `json:"grant,omitempty"`
}

type Grant struct {
	GrantID               string          `json:"grant_id"`
	GrantKey              string          `json:"grant_key"`
	GrantType             string          `json:"grant_type"`
	GrantedByActorID      string          `json:"granted_by_actor_id"`
	GrantedToActorID      string          `json:"granted_to_actor_id"`
	ApprovalID            *string         `json:"approval_id,omitempty"`
	Status                string          `json:"status"`
	BypassConfirmation    bool            `json:"bypass_confirmation"`
	MaxRiskLevel          *string         `json:"max_risk_level,omitempty"`
	MaxAuthorizationLevel *int            `json:"max_authorization_level,omitempty"`
	ScopeConstraints      json.RawMessage `json:"scope_constraints"`
	NodeConstraints       json.RawMessage `json:"node_constraints"`
	CapabilityConstraints json.RawMessage `json:"capability_constraints"`
	CredentialConstraints json.RawMessage `json:"credential_constraints"`
	ObjectConstraints     json.RawMessage `json:"object_constraints"`
	EgressConstraints     json.RawMessage `json:"egress_constraints"`
	MaxUses               *int            `json:"max_uses,omitempty"`
	UsesCount             int             `json:"uses_count"`
	AuditLevel            string          `json:"audit_level"`
	CreatedAt             time.Time       `json:"created_at"`
	ExpiresAt             time.Time       `json:"expires_at"`
	RevokedAt             *time.Time      `json:"revoked_at,omitempty"`
	RevokedByActorID      *string         `json:"revoked_by_actor_id,omitempty"`
	RevokedReason         *string         `json:"revoked_reason,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
}

type GrantFilter struct {
	Limit                 int
	Status                string
	GrantType             string
	GrantedToActorRef     string
	GrantedByActorRef     string
	ApprovalRef           string
	TargetNodeRef         string
	CapabilityEndpointRef string
}

type GrantInput struct {
	GrantType                 string          `json:"grant_type"`
	GrantedByActorRef         string          `json:"granted_by_actor_ref"`
	GrantedToActorRef         string          `json:"granted_to_actor_ref"`
	ApprovalRef               string          `json:"approval_ref,omitempty"`
	BypassConfirmation        bool            `json:"bypass_confirmation,omitempty"`
	MaxRiskLevel              string          `json:"max_risk_level,omitempty"`
	MaxAuthorizationLevel     int             `json:"max_authorization_level,omitempty"`
	ScopeConstraintsJSON      json.RawMessage `json:"scope_constraints,omitempty"`
	NodeConstraintsJSON       json.RawMessage `json:"node_constraints,omitempty"`
	CapabilityConstraintsJSON json.RawMessage `json:"capability_constraints,omitempty"`
	CredentialConstraintsJSON json.RawMessage `json:"credential_constraints,omitempty"`
	ObjectConstraintsJSON     json.RawMessage `json:"object_constraints,omitempty"`
	EgressConstraintsJSON     json.RawMessage `json:"egress_constraints,omitempty"`
	MaxUses                   int             `json:"max_uses,omitempty"`
	ExpiresAt                 time.Time       `json:"expires_at"`
	Metadata                  json.RawMessage `json:"metadata,omitempty"`
}

type GrantCoverageInput struct {
	ActorRef                    string `json:"actor_ref"`
	TargetNodeRef               string `json:"target_node_ref"`
	CapabilityEndpointRef       string `json:"capability_endpoint_ref"`
	RiskLevel                   string `json:"risk_level,omitempty"`
	ExecutionAuthorizationLevel int    `json:"execution_authorization_level,omitempty"`
}

type GrantRevokeInput struct {
	GrantRef          string `json:"grant_ref"`
	RevokedByActorRef string `json:"revoked_by_actor_ref,omitempty"`
	Reason            string `json:"reason,omitempty"`
}

type GrantConsumeInput struct {
	GrantRef         string `json:"grant_ref"`
	Reason           string `json:"reason,omitempty"`
	RouteID          string `json:"route_id,omitempty"`
	CapabilityCallID string `json:"capability_call_id,omitempty"`
}

type ExpirationResult struct {
	ApprovalsExpired int `json:"approvals_expired"`
	GrantsExpired    int `json:"grants_expired"`
}
