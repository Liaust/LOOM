package capabilities

import (
	"encoding/json"
	"time"
)

const (
	ProviderTypeSystem         = "system"
	ProviderTypeObjectStore    = "object_store"
	ProviderTypeScriptRunner   = "script_runner"
	ProviderTypeWorkflowRunner = "workflow_runner"
	ProviderTypeConnector      = "connector"
	ProviderTypeModule         = "module"
	ProviderTypeHardware       = "hardware"
	ProviderTypeAgent          = "agent"
	ProviderTypeService        = "service"

	ProviderStatusRegistered = "registered"
	ProviderStatusActive     = "active"
	ProviderStatusDisabled   = "disabled"
	ProviderStatusDeprecated = "deprecated"
	ProviderStatusRevoked    = "revoked"

	ProviderAdvertisementStatusReceived      = "received"
	ProviderAdvertisementStatusValidated     = "validated"
	ProviderAdvertisementStatusPendingReview = "pending_review"
	ProviderAdvertisementStatusApproved      = "approved"
	ProviderAdvertisementStatusRejected      = "rejected"
	ProviderAdvertisementStatusStale         = "stale"

	HealthStatusUnknown   = "unknown"
	HealthStatusOK        = "ok"
	HealthStatusDegraded  = "degraded"
	HealthStatusUnhealthy = "unhealthy"
	HealthStatusOffline   = "offline"

	AvailabilityStatusUnknown     = "unknown"
	AvailabilityStatusAvailable   = "available"
	AvailabilityStatusLimited     = "limited"
	AvailabilityStatusUnavailable = "unavailable"

	CapabilityFormQuery        = "query"
	CapabilityFormCommand      = "command"
	CapabilityFormJob          = "job"
	CapabilityFormSession      = "session"
	CapabilityFormStream       = "stream"
	CapabilityFormSubscription = "subscription"
	CapabilityFormLease        = "lease"

	RiskLevelLow      = "low"
	RiskLevelMedium   = "medium"
	RiskLevelHigh     = "high"
	RiskLevelCritical = "critical"

	CapabilityClassStatusActive     = "active"
	CapabilityClassStatusDeprecated = "deprecated"
	CapabilityClassStatusDisabled   = "disabled"
	CapabilityClassStatusRevoked    = "revoked"

	EndpointStatusRegistered = "registered"
	EndpointStatusActive     = "active"
	EndpointStatusDisabled   = "disabled"
	EndpointStatusDeprecated = "deprecated"
	EndpointStatusRevoked    = "revoked"

	EndpointVersionStatusPendingReview = "pending_review"
	EndpointVersionStatusActive        = "active"
	EndpointVersionStatusDisabled      = "disabled"
	EndpointVersionStatusDeprecated    = "deprecated"
	EndpointVersionStatusRevoked       = "revoked"
	EndpointVersionStatusSuperseded    = "superseded"

	RuntimeKindScript          = "script"
	RuntimeKindCommand         = "command"
	RuntimeKindHTTP            = "http"
	RuntimeKindServiceManager  = "service_manager"
	RuntimeKindNodeAgent       = "node_agent"
	RuntimeKindNative          = "native"
	RuntimeKindModule          = "module"
	RuntimeKindWorkflow        = "workflow"
	RuntimeKindExternalProcess = "external_process"

	RuntimeBindingStatusRegistered = "registered"
	RuntimeBindingStatusActive     = "active"
	RuntimeBindingStatusDisabled   = "disabled"
	RuntimeBindingStatusDeprecated = "deprecated"
	RuntimeBindingStatusRevoked    = "revoked"

	UsageTargetKindCapabilityClass     = "capability_class"
	UsageTargetKindCapabilityEndpoint  = "capability_endpoint"
	UsageTargetKindProvider            = "provider"
	UsageTargetKindCapabilityPack      = "capability_pack"
	UsageTargetKindProviderPack        = "provider_pack"
	UsageTargetKindScript              = "script"
	UsageTargetKindWorkflow            = "workflow"
	UsageTargetKindModuleCapability    = "module_capability"
	UsageTargetKindConnectorCapability = "connector_capability"

	UsageDocumentFormatMarkdown = "markdown"

	UsageReviewStatusDraft         = "draft"
	UsageReviewStatusPendingReview = "pending_review"
	UsageReviewStatusApproved      = "approved"
	UsageReviewStatusStale         = "stale"
	UsageReviewStatusRejected      = "rejected"
	UsageReviewStatusDeprecated    = "deprecated"

	UsageSourceKindBootstrap = "bootstrap"
	UsageSourceKindManifest  = "manifest"
	UsageSourceKindManual    = "manual"
	UsageSourceKindGenerated = "generated"
)

type Provider struct {
	ProviderID            string          `json:"provider_id"`
	ProviderKey           string          `json:"provider_key"`
	CompactAddress        string          `json:"compact_address"`
	DisplayName           string          `json:"display_name"`
	Description           string          `json:"description"`
	ProviderType          string          `json:"provider_type"`
	NodeID                string          `json:"node_id"`
	ScopeID               string          `json:"scope_id"`
	Version               string          `json:"version"`
	Status                string          `json:"status"`
	RuntimeProfileJSON    json.RawMessage `json:"runtime_profile_json"`
	DocumentationRefsJSON json.RawMessage `json:"documentation_refs_json"`
	PackageRef            *string         `json:"package_ref,omitempty"`
	CreatedByActorID      string          `json:"created_by_actor_id"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	LastAdvertisedAt      *time.Time      `json:"last_advertised_at,omitempty"`
	Metadata              json.RawMessage `json:"metadata"`
}

type ProviderHealth struct {
	ProviderID         string          `json:"provider_id"`
	HealthStatus       string          `json:"health_status"`
	AvailabilityStatus string          `json:"availability_status"`
	LastCheckedAt      *time.Time      `json:"last_checked_at,omitempty"`
	LastOKAt           *time.Time      `json:"last_ok_at,omitempty"`
	Message            string          `json:"message"`
	DetailsJSON        json.RawMessage `json:"details_json"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type ProviderAdvertisement struct {
	ProviderAdvertisementID string          `json:"provider_advertisement_id"`
	OriginNodeID            string          `json:"origin_node_id"`
	ProviderID              *string         `json:"provider_id,omitempty"`
	CommunicationMessageID  *string         `json:"communication_message_id,omitempty"`
	AdvertisementHash       string          `json:"advertisement_hash"`
	IdempotencyKey          *string         `json:"idempotency_key,omitempty"`
	Status                  string          `json:"status"`
	RawPayloadJSON          json.RawMessage `json:"raw_payload_json"`
	ValidationSummaryJSON   json.RawMessage `json:"validation_summary_json"`
	UpsertSummaryJSON       json.RawMessage `json:"upsert_summary_json"`
	RejectionReason         string          `json:"rejection_reason"`
	ReceivedAt              time.Time       `json:"received_at"`
	ValidatedAt             *time.Time      `json:"validated_at,omitempty"`
	ReviewedAt              *time.Time      `json:"reviewed_at,omitempty"`
	ReviewedByActorID       *string         `json:"reviewed_by_actor_id,omitempty"`
	Metadata                json.RawMessage `json:"metadata"`
}

type CapabilityClass struct {
	CapabilityClassID             string          `json:"capability_class_id"`
	Namespace                     string          `json:"namespace"`
	Name                          string          `json:"name"`
	Version                       string          `json:"version"`
	DisplayName                   string          `json:"display_name"`
	Description                   string          `json:"description"`
	Form                          string          `json:"form"`
	InputSchemaJSON               json.RawMessage `json:"input_schema_json"`
	OutputSchemaJSON              json.RawMessage `json:"output_schema_json"`
	DefaultRiskLevel              string          `json:"default_risk_level"`
	DefaultPolicyRequirementsJSON json.RawMessage `json:"default_policy_requirements_json"`
	Status                        string          `json:"status"`
	CreatedAt                     time.Time       `json:"created_at"`
	UpdatedAt                     time.Time       `json:"updated_at"`
	DeprecatedAt                  *time.Time      `json:"deprecated_at,omitempty"`
	Metadata                      json.RawMessage `json:"metadata"`
}

type CapabilityEndpoint struct {
	CapabilityEndpointID        string          `json:"capability_endpoint_id"`
	ProviderID                  string          `json:"provider_id"`
	CapabilityClassID           string          `json:"capability_class_id"`
	ActiveEndpointVersionID     *string         `json:"active_endpoint_version_id,omitempty"`
	EndpointName                string          `json:"endpoint_name"`
	CompactAddress              string          `json:"compact_address"`
	Form                        string          `json:"form"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	SideEffectsJSON             json.RawMessage `json:"side_effects_json"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json"`
	ApprovalRequirementsJSON    json.RawMessage `json:"approval_requirements_json"`
	JobBehaviorJSON             json.RawMessage `json:"job_behavior_json"`
	SessionBehaviorJSON         json.RawMessage `json:"session_behavior_json"`
	StreamBehaviorJSON          json.RawMessage `json:"stream_behavior_json"`
	LeaseBehaviorJSON           json.RawMessage `json:"lease_behavior_json"`
	Status                      string          `json:"status"`
	CreatedAt                   time.Time       `json:"created_at"`
	UpdatedAt                   time.Time       `json:"updated_at"`
	DeprecatedAt                *time.Time      `json:"deprecated_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type EndpointVersion struct {
	CapabilityEndpointVersionID string          `json:"capability_endpoint_version_id"`
	CapabilityEndpointID        string          `json:"capability_endpoint_id"`
	VersionLabel                string          `json:"version_label"`
	ImplementationHash          *string         `json:"implementation_hash,omitempty"`
	ManifestJSON                json.RawMessage `json:"manifest_json"`
	InputSchemaJSON             json.RawMessage `json:"input_schema_json"`
	OutputSchemaJSON            json.RawMessage `json:"output_schema_json"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	PolicyRequirementsJSON      json.RawMessage `json:"policy_requirements_json"`
	CredentialRequirementsJSON  json.RawMessage `json:"credential_requirements_json"`
	ApprovalRequirementsJSON    json.RawMessage `json:"approval_requirements_json"`
	Status                      string          `json:"status"`
	ApprovedByActorID           *string         `json:"approved_by_actor_id,omitempty"`
	ApprovedAt                  *time.Time      `json:"approved_at,omitempty"`
	CreatedAt                   time.Time       `json:"created_at"`
	DeprecatedAt                *time.Time      `json:"deprecated_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type EndpointRuntimeBinding struct {
	RuntimeBindingID            string          `json:"runtime_binding_id"`
	CapabilityEndpointVersionID string          `json:"capability_endpoint_version_id"`
	RuntimeKind                 string          `json:"runtime_kind"`
	RuntimeConfigJSON           json.RawMessage `json:"runtime_config_json"`
	InputMappingJSON            json.RawMessage `json:"input_mapping_json"`
	OutputMappingJSON           json.RawMessage `json:"output_mapping_json"`
	Status                      string          `json:"status"`
	CreatedByActorID            string          `json:"created_by_actor_id"`
	ApprovedByActorID           *string         `json:"approved_by_actor_id,omitempty"`
	ApprovedAt                  *time.Time      `json:"approved_at,omitempty"`
	CreatedAt                   time.Time       `json:"created_at"`
	UpdatedAt                   time.Time       `json:"updated_at"`
	DisabledAt                  *time.Time      `json:"disabled_at,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

type UsageDocument struct {
	CapabilityUsageDocumentID string          `json:"capability_usage_document_id"`
	TargetKind                string          `json:"target_kind"`
	TargetID                  string          `json:"target_id"`
	TargetAddress             *string         `json:"target_address,omitempty"`
	Title                     string          `json:"title"`
	VersionLabel              string          `json:"version_label"`
	BodyFormat                string          `json:"body_format"`
	Body                      string          `json:"body"`
	SectionMapJSON            json.RawMessage `json:"section_map_json"`
	VisibilityPolicyJSON      json.RawMessage `json:"visibility_policy_json"`
	ReviewStatus              string          `json:"review_status"`
	ContentHash               string          `json:"content_hash"`
	SourceKind                string          `json:"source_kind"`
	SourceRef                 *string         `json:"source_ref,omitempty"`
	CreatedByActorID          *string         `json:"created_by_actor_id,omitempty"`
	ApprovedByActorID         *string         `json:"approved_by_actor_id,omitempty"`
	ApprovedAt                *time.Time      `json:"approved_at,omitempty"`
	IndexedAt                 *time.Time      `json:"indexed_at,omitempty"`
	CreatedAt                 time.Time       `json:"created_at"`
	UpdatedAt                 time.Time       `json:"updated_at"`
	DeprecatedAt              *time.Time      `json:"deprecated_at,omitempty"`
	Metadata                  json.RawMessage `json:"metadata"`
}

type ProviderInspection struct {
	Provider       Provider             `json:"provider"`
	Health         *ProviderHealth      `json:"health,omitempty"`
	Endpoints      []CapabilityEndpoint `json:"endpoints"`
	UsageDocuments []UsageDocument      `json:"usage_documents"`
}

type CapabilityInspection struct {
	Endpoint       CapabilityEndpoint      `json:"endpoint"`
	Class          CapabilityClass         `json:"class"`
	Provider       Provider                `json:"provider"`
	ProviderHealth *ProviderHealth         `json:"provider_health,omitempty"`
	ActiveVersion  *EndpointVersion        `json:"active_version,omitempty"`
	RuntimeBinding *EndpointRuntimeBinding `json:"runtime_binding,omitempty"`
	UsageDocuments []UsageDocument         `json:"usage_documents"`
}

type RuntimeBindingInspection struct {
	Binding         EndpointRuntimeBinding `json:"binding"`
	EndpointVersion EndpointVersion        `json:"endpoint_version"`
	Endpoint        CapabilityEndpoint     `json:"endpoint"`
	Provider        Provider               `json:"provider"`
	Class           CapabilityClass        `json:"class"`
}

type RuntimeBindingFilter struct {
	Limit              int
	CapabilityRef      string
	EndpointVersionRef string
	ProviderRef        string
	RuntimeKind        string
	Status             string
}

type RegisterRuntimeBindingInput struct {
	EndpointVersionRef string          `json:"endpoint_version_ref"`
	RuntimeKind        string          `json:"runtime_kind"`
	RuntimeConfigJSON  json.RawMessage `json:"runtime_config_json,omitempty"`
	InputMappingJSON   json.RawMessage `json:"input_mapping_json,omitempty"`
	OutputMappingJSON  json.RawMessage `json:"output_mapping_json,omitempty"`
	Status             string          `json:"status,omitempty"`
	ApprovedByActorID  string          `json:"approved_by_actor_id,omitempty"`
	ApprovedAt         *time.Time      `json:"approved_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

type ProviderAdvertisementInspection struct {
	Advertisement  ProviderAdvertisement `json:"advertisement"`
	Provider       *Provider             `json:"provider,omitempty"`
	Endpoints      []CapabilityEndpoint  `json:"endpoints,omitempty"`
	UsageDocuments []UsageDocument       `json:"usage_documents,omitempty"`
}

type ProviderListItem struct {
	Provider
	HealthStatus       string          `json:"health_status"`
	AvailabilityStatus string          `json:"availability_status"`
	HealthDetailsJSON  json.RawMessage `json:"health_details_json"`
	LastCheckedAt      *time.Time      `json:"last_checked_at,omitempty"`
}

type CapabilityListItem struct {
	CapabilityEndpoint
	ProviderAddress string `json:"provider_address"`
	ProviderKey     string `json:"provider_key"`
	ProviderHealth  string `json:"provider_health"`
	ClassName       string `json:"class_name"`
	DisplayName     string `json:"display_name"`
	Description     string `json:"description"`
}

type CapabilityCandidate struct {
	CapabilityEndpointID        string          `json:"capability_endpoint_id"`
	CompactAddress              string          `json:"compact_address"`
	ProviderID                  string          `json:"provider_id"`
	ProviderAddress             string          `json:"provider_address"`
	ProviderHealth              string          `json:"provider_health"`
	CapabilityClassID           string          `json:"capability_class_id"`
	ClassName                   string          `json:"class_name"`
	DisplayName                 string          `json:"display_name"`
	Description                 string          `json:"description"`
	MatchedUseSummary           string          `json:"matched_use_summary"`
	Form                        string          `json:"form"`
	RiskLevel                   string          `json:"risk_level"`
	ExecutionAuthorizationLevel int             `json:"execution_authorization_level"`
	Tags                        []string        `json:"tags,omitempty"`
	Score                       float64         `json:"score"`
	MatchReasons                []string        `json:"match_reasons,omitempty"`
	Metadata                    json.RawMessage `json:"metadata"`
}

func ValidExecutionAuthorizationLevel(level int) bool {
	return level >= 1 && level <= 5
}

func ValidProviderType(value string) bool {
	switch value {
	case ProviderTypeSystem,
		ProviderTypeObjectStore,
		ProviderTypeScriptRunner,
		ProviderTypeWorkflowRunner,
		ProviderTypeConnector,
		ProviderTypeModule,
		ProviderTypeHardware,
		ProviderTypeAgent,
		ProviderTypeService:
		return true
	default:
		return false
	}
}

func ValidProviderStatus(value string) bool {
	switch value {
	case ProviderStatusRegistered,
		ProviderStatusActive,
		ProviderStatusDisabled,
		ProviderStatusDeprecated,
		ProviderStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidProviderAdvertisementStatus(value string) bool {
	switch value {
	case ProviderAdvertisementStatusReceived,
		ProviderAdvertisementStatusValidated,
		ProviderAdvertisementStatusPendingReview,
		ProviderAdvertisementStatusApproved,
		ProviderAdvertisementStatusRejected,
		ProviderAdvertisementStatusStale:
		return true
	default:
		return false
	}
}

func ValidHealthStatus(value string) bool {
	switch value {
	case HealthStatusUnknown,
		HealthStatusOK,
		HealthStatusDegraded,
		HealthStatusUnhealthy,
		HealthStatusOffline:
		return true
	default:
		return false
	}
}

func ValidAvailabilityStatus(value string) bool {
	switch value {
	case AvailabilityStatusUnknown,
		AvailabilityStatusAvailable,
		AvailabilityStatusLimited,
		AvailabilityStatusUnavailable:
		return true
	default:
		return false
	}
}

func ValidCapabilityForm(value string) bool {
	switch value {
	case CapabilityFormQuery,
		CapabilityFormCommand,
		CapabilityFormJob,
		CapabilityFormSession,
		CapabilityFormStream,
		CapabilityFormSubscription,
		CapabilityFormLease:
		return true
	default:
		return false
	}
}

func ValidRiskLevel(value string) bool {
	switch value {
	case RiskLevelLow,
		RiskLevelMedium,
		RiskLevelHigh,
		RiskLevelCritical:
		return true
	default:
		return false
	}
}

func ValidCapabilityClassStatus(value string) bool {
	switch value {
	case CapabilityClassStatusActive,
		CapabilityClassStatusDeprecated,
		CapabilityClassStatusDisabled,
		CapabilityClassStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidEndpointStatus(value string) bool {
	switch value {
	case EndpointStatusRegistered,
		EndpointStatusActive,
		EndpointStatusDisabled,
		EndpointStatusDeprecated,
		EndpointStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidEndpointVersionStatus(value string) bool {
	switch value {
	case EndpointVersionStatusPendingReview,
		EndpointVersionStatusActive,
		EndpointVersionStatusDisabled,
		EndpointVersionStatusDeprecated,
		EndpointVersionStatusRevoked,
		EndpointVersionStatusSuperseded:
		return true
	default:
		return false
	}
}

func ValidRuntimeKind(value string) bool {
	switch value {
	case RuntimeKindScript,
		RuntimeKindCommand,
		RuntimeKindHTTP,
		RuntimeKindServiceManager,
		RuntimeKindNodeAgent,
		RuntimeKindNative,
		RuntimeKindModule,
		RuntimeKindWorkflow,
		RuntimeKindExternalProcess:
		return true
	default:
		return false
	}
}

func ValidRuntimeBindingStatus(value string) bool {
	switch value {
	case RuntimeBindingStatusRegistered,
		RuntimeBindingStatusActive,
		RuntimeBindingStatusDisabled,
		RuntimeBindingStatusDeprecated,
		RuntimeBindingStatusRevoked:
		return true
	default:
		return false
	}
}

func ValidUsageTargetKind(value string) bool {
	switch value {
	case UsageTargetKindCapabilityClass,
		UsageTargetKindCapabilityEndpoint,
		UsageTargetKindProvider,
		UsageTargetKindCapabilityPack,
		UsageTargetKindProviderPack,
		UsageTargetKindScript,
		UsageTargetKindWorkflow,
		UsageTargetKindModuleCapability,
		UsageTargetKindConnectorCapability:
		return true
	default:
		return false
	}
}

func ValidUsageReviewStatus(value string) bool {
	switch value {
	case UsageReviewStatusDraft,
		UsageReviewStatusPendingReview,
		UsageReviewStatusApproved,
		UsageReviewStatusStale,
		UsageReviewStatusRejected,
		UsageReviewStatusDeprecated:
		return true
	default:
		return false
	}
}

func ValidUsageSourceKind(value string) bool {
	switch value {
	case UsageSourceKindBootstrap,
		UsageSourceKindManifest,
		UsageSourceKindManual,
		UsageSourceKindGenerated:
		return true
	default:
		return false
	}
}
