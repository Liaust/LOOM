package realtime

import (
	"encoding/json"
	"time"
)

const (
	SubscriptionCursorFromNow       = "from_now"
	SubscriptionCursorFromLatest    = "from_latest"
	SubscriptionCursorFromStart     = "from_start"
	SubscriptionCursorFromBeginning = "from_beginning"
)

type TopicFilter struct {
	Limit    int
	Status   string
	ScopeRef string
}

type CreateTopicInput struct {
	TopicPath     string          `json:"topic_path"`
	DisplayName   string          `json:"display_name,omitempty"`
	ScopeRef      string          `json:"scope_ref,omitempty"`
	RetentionMode string          `json:"retention_mode,omitempty"`
	DeliveryClass string          `json:"delivery_class,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type PublishInput struct {
	MessageType      string          `json:"message_type"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	PayloadJSON      json.RawMessage `json:"payload_json,omitempty"`
	PayloadSchemaRef string          `json:"payload_schema_ref,omitempty"`
	CausationRef     string          `json:"causation_ref,omitempty"`
	AuthorizationRef string          `json:"authorization_ref,omitempty"`
	MeaningfulEvent  bool            `json:"meaningful_event,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type SubscriptionFilter struct {
	Limit    int
	Status   string
	TopicRef string
}

type CreateSubscriptionInput struct {
	TopicRef           string          `json:"topic_ref"`
	CursorMode         string          `json:"cursor_mode,omitempty"`
	FilterJSON         json.RawMessage `json:"filter_json,omitempty"`
	Filter             json.RawMessage `json:"filter,omitempty"`
	DeliveryTargetKind string          `json:"delivery_target_kind,omitempty"`
	DeliveryTargetRef  string          `json:"delivery_target_ref,omitempty"`
	DeliveryMode       string          `json:"delivery_mode,omitempty"`
	DeliveryClass      string          `json:"delivery_class,omitempty"`
	AuthorizationRef   string          `json:"authorization_ref,omitempty"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
}

type PollSubscriptionInput struct {
	Limit int `json:"limit,omitempty"`
}

type SubscriptionPollResult struct {
	Subscription Subscription       `json:"subscription"`
	Publications []TopicPublication `json:"publications"`
	FromSequence int64              `json:"from_sequence"`
	ToSequence   int64              `json:"to_sequence"`
	Replay       bool               `json:"replay"`
}

type AcknowledgeSubscriptionInput struct {
	Sequence int64 `json:"sequence"`
}

type PresenceFilter struct {
	Limit       int
	SubjectKind string
	State       string
}

type NodePresenceInput struct {
	NodeID           string          `json:"node_id"`
	HeartbeatID      string          `json:"heartbeat_id,omitempty"`
	State            string          `json:"state,omitempty"`
	LastSeenAt       time.Time       `json:"last_seen_at,omitempty"`
	ExpiresInSeconds int             `json:"expires_in_seconds,omitempty"`
	StatusDetail     string          `json:"status_detail,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}

type NotificationFilter struct {
	Limit       int
	Status      string
	Category    string
	TargetKind  string
	TargetRef   string
	ApprovalRef string
}

type CreateNotificationInput struct {
	TargetKind        string          `json:"target_kind"`
	TargetRef         string          `json:"target_ref"`
	SourceKind        string          `json:"source_kind"`
	SourceRef         string          `json:"source_ref,omitempty"`
	ScopeRef          string          `json:"scope_ref,omitempty"`
	Category          string          `json:"category"`
	Priority          string          `json:"priority,omitempty"`
	Summary           string          `json:"summary"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	PayloadJSON       json.RawMessage `json:"payload_json,omitempty"`
	AuthorizationRef  string          `json:"authorization_ref,omitempty"`
	PolicyDecisionRef string          `json:"policy_decision_ref,omitempty"`
	ApprovalRef       string          `json:"approval_ref,omitempty"`
	RouteRef          string          `json:"route_ref,omitempty"`
	CapabilityCallRef string          `json:"capability_call_ref,omitempty"`
	DeliveryMode      string          `json:"delivery_mode,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type ApprovalNotificationInput struct {
	ApprovalRef       string `json:"approval_ref"`
	RouteRef          string `json:"route_ref,omitempty"`
	CapabilityCallRef string `json:"capability_call_ref,omitempty"`
	PolicyDecisionRef string `json:"policy_decision_ref,omitempty"`
	ScopeRef          string `json:"scope_ref,omitempty"`
}

type NotificationExpirationResult struct {
	NotificationsExpired int `json:"notifications_expired"`
}

type ProgressDetail struct {
	Feed    ProgressFeed     `json:"feed"`
	Updates []ProgressUpdate `json:"updates"`
}

type ProgressUpdateResult struct {
	Feed   ProgressFeed   `json:"feed"`
	Update ProgressUpdate `json:"update"`
}

type EnsureProgressFeedInput struct {
	SourceKind string          `json:"source_kind"`
	SourceRef  string          `json:"source_ref"`
	ScopeRef   string          `json:"scope_ref,omitempty"`
	TopicRef   string          `json:"topic_ref,omitempty"`
	Status     string          `json:"status,omitempty"`
	Stage      string          `json:"stage,omitempty"`
	Message    string          `json:"message,omitempty"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type UpdateProgressInput struct {
	SourceKind    string          `json:"source_kind"`
	SourceRef     string          `json:"source_ref"`
	ScopeRef      string          `json:"scope_ref,omitempty"`
	TopicRef      string          `json:"topic_ref,omitempty"`
	Status        string          `json:"status"`
	Stage         string          `json:"stage,omitempty"`
	Message       string          `json:"message,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	PayloadJSON   json.RawMessage `json:"payload_json,omitempty"`
	ProgressValue *float64        `json:"progress_value,omitempty"`
	TotalValue    *float64        `json:"total_value,omitempty"`
	Severity      string          `json:"severity,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
}

type LeaseFilter struct {
	Limit      int
	Status     string
	Resource   string
	HolderKind string
	HolderRef  string
}

type RequestLeaseInput struct {
	Resource          string          `json:"resource"`
	ResourceKind      string          `json:"resource_kind,omitempty"`
	ResourceRef       string          `json:"resource_ref,omitempty"`
	HolderKind        string          `json:"holder_kind,omitempty"`
	HolderRef         string          `json:"holder_ref,omitempty"`
	ScopeRef          string          `json:"scope_ref,omitempty"`
	RouteRef          string          `json:"route_ref,omitempty"`
	CapabilityCallRef string          `json:"capability_call_ref,omitempty"`
	PolicyDecisionRef string          `json:"policy_decision_ref,omitempty"`
	ApprovalRef       string          `json:"approval_ref,omitempty"`
	GrantRef          string          `json:"grant_ref,omitempty"`
	AuthorizationRef  string          `json:"authorization_ref,omitempty"`
	LeaseMode         string          `json:"lease_mode,omitempty"`
	DurationSeconds   int             `json:"duration_seconds,omitempty"`
	ExpiresAt         *time.Time      `json:"expires_at,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
}

type ReleaseLeaseInput struct {
	ReleaseReason string `json:"release_reason,omitempty"`
}

type LeaseExpirationResult struct {
	LeasesExpired int `json:"leases_expired"`
}

type SubscriptionExpirationResult struct {
	SubscriptionsExpired int `json:"subscriptions_expired"`
}

type PresenceExpirationResult struct {
	PresenceMarkedStale int `json:"presence_marked_stale"`
}

type ProgressCloseResult struct {
	ProgressFeedsClosed int `json:"progress_feeds_closed"`
}

type ExpiryResult struct {
	NotificationsExpired int `json:"notifications_expired"`
	LeasesExpired        int `json:"leases_expired"`
	SubscriptionsExpired int `json:"subscriptions_expired"`
	PresenceMarkedStale  int `json:"presence_marked_stale"`
	ProgressFeedsClosed  int `json:"progress_feeds_closed"`
}
