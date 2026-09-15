package realtime

import (
	"encoding/json"
	"time"
)

type Topic struct {
	TopicID            string          `json:"topic_id"`
	TopicPath          string          `json:"topic_path"`
	DisplayName        string          `json:"display_name"`
	ScopeID            *string         `json:"scope_id,omitempty"`
	OwnerActorID       *string         `json:"owner_actor_id,omitempty"`
	CreatedByActorID   string          `json:"created_by_actor_id"`
	PublisherPolicyID  *string         `json:"publisher_policy_id,omitempty"`
	SubscriberPolicyID *string         `json:"subscriber_policy_id,omitempty"`
	RetentionMode      string          `json:"retention_mode"`
	DeliveryClass      string          `json:"delivery_class"`
	OrderingMode       string          `json:"ordering_mode"`
	Status             string          `json:"status"`
	LastSequence       int64           `json:"last_sequence"`
	LatestPayloadJSON  json.RawMessage `json:"latest_payload_json"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	ClosedAt           *time.Time      `json:"closed_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
}

type TopicPublication struct {
	TopicPublicationID  string          `json:"topic_publication_id"`
	TopicID             string          `json:"topic_id"`
	Sequence            int64           `json:"sequence"`
	PublisherActorID    *string         `json:"publisher_actor_id,omitempty"`
	PublisherNodeID     *string         `json:"publisher_node_id,omitempty"`
	PublisherProviderID *string         `json:"publisher_provider_id,omitempty"`
	MessageType         string          `json:"message_type"`
	PayloadJSON         json.RawMessage `json:"payload_json"`
	PayloadSchemaRef    string          `json:"payload_schema_ref"`
	CorrelationID       string          `json:"correlation_id"`
	CausationRef        string          `json:"causation_ref"`
	AuthorizationRef    string          `json:"authorization_ref"`
	PolicyDecisionID    *string         `json:"policy_decision_id,omitempty"`
	DurabilityMode      string          `json:"durability_mode"`
	CreatedAt           time.Time       `json:"created_at"`
	Metadata            json.RawMessage `json:"metadata"`
}

type Subscription struct {
	SubscriptionID           string          `json:"subscription_id"`
	TopicID                  string          `json:"topic_id"`
	SourceKind               string          `json:"source_kind"`
	SourceRef                string          `json:"source_ref"`
	SubscriberActorID        *string         `json:"subscriber_actor_id,omitempty"`
	SubscriberNodeID         *string         `json:"subscriber_node_id,omitempty"`
	ScopeID                  *string         `json:"scope_id,omitempty"`
	FilterJSON               json.RawMessage `json:"filter_json"`
	CursorSequence           int64           `json:"cursor_sequence"`
	LastAcknowledgedSequence int64           `json:"last_acknowledged_sequence"`
	DeliveryTargetKind       string          `json:"delivery_target_kind"`
	DeliveryTargetRef        string          `json:"delivery_target_ref"`
	DeliveryMode             string          `json:"delivery_mode"`
	DeliveryClass            string          `json:"delivery_class"`
	AuthorizationRef         string          `json:"authorization_ref"`
	PolicyDecisionID         *string         `json:"policy_decision_id,omitempty"`
	Status                   string          `json:"status"`
	ExpiresAt                *time.Time      `json:"expires_at,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
	UpdatedAt                time.Time       `json:"updated_at"`
	LastDeliveredAt          *time.Time      `json:"last_delivered_at,omitempty"`
	LastAcknowledgedAt       *time.Time      `json:"last_acknowledged_at,omitempty"`
	Metadata                 json.RawMessage `json:"metadata"`
}

type Presence struct {
	PresenceID   string          `json:"presence_id"`
	SubjectKind  string          `json:"subject_kind"`
	SubjectRef   string          `json:"subject_ref"`
	NodeID       *string         `json:"node_id,omitempty"`
	State        string          `json:"state"`
	SourceKind   string          `json:"source_kind"`
	SourceRef    string          `json:"source_ref"`
	LastSeenAt   time.Time       `json:"last_seen_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	Confidence   *float64        `json:"confidence,omitempty"`
	StatusDetail string          `json:"status_detail"`
	Metadata     json.RawMessage `json:"metadata"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type Notification struct {
	NotificationID   string          `json:"notification_id"`
	TargetKind       string          `json:"target_kind"`
	TargetRef        string          `json:"target_ref"`
	SourceKind       string          `json:"source_kind"`
	SourceRef        string          `json:"source_ref"`
	ScopeID          *string         `json:"scope_id,omitempty"`
	Category         string          `json:"category"`
	Priority         string          `json:"priority"`
	Summary          string          `json:"summary"`
	PayloadJSON      json.RawMessage `json:"payload_json"`
	AuthorizationRef string          `json:"authorization_ref"`
	PolicyDecisionID *string         `json:"policy_decision_id,omitempty"`
	ApprovalID       *string         `json:"approval_id,omitempty"`
	RouteID          *string         `json:"route_id,omitempty"`
	CapabilityCallID *string         `json:"capability_call_id,omitempty"`
	Status           string          `json:"status"`
	ExpiresAt        *time.Time      `json:"expires_at,omitempty"`
	AcknowledgedAt   *time.Time      `json:"acknowledged_at,omitempty"`
	DismissedAt      *time.Time      `json:"dismissed_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Metadata         json.RawMessage `json:"metadata"`
}

type NotificationDelivery struct {
	NotificationDeliveryID string          `json:"notification_delivery_id"`
	NotificationID         string          `json:"notification_id"`
	TargetKind             string          `json:"target_kind"`
	TargetRef              string          `json:"target_ref"`
	DeliveryMode           string          `json:"delivery_mode"`
	Status                 string          `json:"status"`
	AttemptCount           int             `json:"attempt_count"`
	NextAttemptAt          *time.Time      `json:"next_attempt_at,omitempty"`
	LastAttemptAt          *time.Time      `json:"last_attempt_at,omitempty"`
	DeliveredAt            *time.Time      `json:"delivered_at,omitempty"`
	AcknowledgedAt         *time.Time      `json:"acknowledged_at,omitempty"`
	ErrorCode              string          `json:"error_code"`
	ErrorMessage           string          `json:"error_message"`
	CommunicationMessageID *string         `json:"communication_message_id,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	Metadata               json.RawMessage `json:"metadata"`
}

type ProgressFeed struct {
	ProgressFeedID string          `json:"progress_feed_id"`
	SourceKind     string          `json:"source_kind"`
	SourceRef      string          `json:"source_ref"`
	ScopeID        *string         `json:"scope_id,omitempty"`
	TopicID        *string         `json:"topic_id,omitempty"`
	CurrentStatus  string          `json:"current_status"`
	CurrentStage   string          `json:"current_stage"`
	CurrentMessage string          `json:"current_message"`
	CurrentValue   *float64        `json:"current_value,omitempty"`
	TotalValue     *float64        `json:"total_value,omitempty"`
	LastSequence   int64           `json:"last_sequence"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	ClosedAt       *time.Time      `json:"closed_at,omitempty"`
	Metadata       json.RawMessage `json:"metadata"`
}

type ProgressUpdate struct {
	ProgressUpdateID string          `json:"progress_update_id"`
	ProgressFeedID   string          `json:"progress_feed_id"`
	SourceKind       string          `json:"source_kind"`
	SourceRef        string          `json:"source_ref"`
	Sequence         int64           `json:"sequence"`
	Stage            string          `json:"stage"`
	Status           string          `json:"status"`
	Message          string          `json:"message"`
	PayloadJSON      json.RawMessage `json:"payload_json"`
	ProgressValue    *float64        `json:"progress_value,omitempty"`
	TotalValue       *float64        `json:"total_value,omitempty"`
	Severity         string          `json:"severity"`
	CorrelationID    string          `json:"correlation_id"`
	CreatedAt        time.Time       `json:"created_at"`
	Metadata         json.RawMessage `json:"metadata"`
}

type Lease struct {
	LeaseID          string          `json:"lease_id"`
	ResourceKind     string          `json:"resource_kind"`
	ResourceRef      string          `json:"resource_ref"`
	HolderKind       string          `json:"holder_kind"`
	HolderRef        string          `json:"holder_ref"`
	HolderActorID    *string         `json:"holder_actor_id,omitempty"`
	NodeID           *string         `json:"node_id,omitempty"`
	ProviderID       *string         `json:"provider_id,omitempty"`
	ScopeID          *string         `json:"scope_id,omitempty"`
	RouteID          *string         `json:"route_id,omitempty"`
	CapabilityCallID *string         `json:"capability_call_id,omitempty"`
	AuthorizationRef string          `json:"authorization_ref"`
	PolicyDecisionID *string         `json:"policy_decision_id,omitempty"`
	ApprovalID       *string         `json:"approval_id,omitempty"`
	GrantID          *string         `json:"grant_id,omitempty"`
	LeaseMode        string          `json:"lease_mode"`
	Status           string          `json:"status"`
	StartsAt         time.Time       `json:"starts_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	ReleasedAt       *time.Time      `json:"released_at,omitempty"`
	ReleaseReason    string          `json:"release_reason"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
	Metadata         json.RawMessage `json:"metadata"`
}
