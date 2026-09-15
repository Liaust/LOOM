package realtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	TopicStatusActive   = "active"
	TopicStatusClosed   = "closed"
	TopicStatusArchived = "archived"
	TopicStatusFailed   = "failed"

	RetentionModeRetainLatest     = "retain_latest"
	RetentionModeRetainBounded    = "retain_bounded"
	RetentionModeDurableEventOnly = "durable_event_only"

	DeliveryClassPolling    = "polling"
	DeliveryClassActorInbox = "actor_inbox"
	DeliveryClassMainOutbox = "main_outbox"

	OrderingModeTopicSequence = "topic_sequence"

	DurabilityModeRetained         = "retained"
	DurabilityModeLatestOnly       = "latest_only"
	DurabilityModeDurableEventOnly = "durable_event_only"

	SubscriptionSourceKindTopic = "topic"

	SubscriptionStatusActive    = "active"
	SubscriptionStatusCancelled = "cancelled"
	SubscriptionStatusExpired   = "expired"
	SubscriptionStatusFailed    = "failed"

	PresenceSubjectKindNode          = "node"
	PresenceSubjectKindActor         = "actor"
	PresenceSubjectKindProvider      = "provider"
	PresenceSubjectKindModuleService = "module_service"
	PresenceSubjectKindJobRunner     = "job_runner"

	PresenceStateOnline       = "online"
	PresenceStateRecentlySeen = "recently_seen"
	PresenceStateOffline      = "offline"
	PresenceStateDegraded     = "degraded"
	PresenceStateUnknown      = "unknown"
	PresenceStateStale        = "stale"
	PresenceStateQuarantined  = "quarantined"
	PresenceStateRevoked      = "revoked"

	PresenceSourceKindHeartbeat      = "heartbeat"
	PresenceSourceKindManual         = "manual"
	PresenceSourceKindProviderStatus = "provider_status"
	PresenceSourceKindRunnerStatus   = "runner_status"
	PresenceSourceKindExpiryWorker   = "expiry_worker"

	NotificationTargetKindActor         = "actor"
	NotificationTargetKindNode          = "node"
	NotificationTargetKindDevice        = "device"
	NotificationTargetKindSession       = "session"
	NotificationTargetKindModuleService = "module_service"
	NotificationTargetKindApprovalQueue = "approval_queue"

	NotificationSourceKindPolicy         = "policy"
	NotificationSourceKindRoute          = "route"
	NotificationSourceKindCapabilityCall = "capability_call"
	NotificationSourceKindJob            = "job"
	NotificationSourceKindSync           = "sync"
	NotificationSourceKindNode           = "node"
	NotificationSourceKindProvider       = "provider"
	NotificationSourceKindModule         = "module"
	NotificationSourceKindSystem         = "system"

	NotificationCategoryApproval = "approval"
	NotificationCategorySecurity = "security"
	NotificationCategoryJob      = "job"
	NotificationCategoryWorkflow = "workflow"
	NotificationCategorySync     = "sync"
	NotificationCategoryNode     = "node"
	NotificationCategoryProvider = "provider"
	NotificationCategoryModule   = "module"
	NotificationCategoryProject  = "project"
	NotificationCategoryReminder = "reminder"
	NotificationCategoryMessage  = "message"

	NotificationPriorityLow      = "low"
	NotificationPriorityNormal   = "normal"
	NotificationPriorityHigh     = "high"
	NotificationPriorityUrgent   = "urgent"
	NotificationPriorityCritical = "critical"

	NotificationStatusCreated      = "created"
	NotificationStatusRouted       = "routed"
	NotificationStatusDelivered    = "delivered"
	NotificationStatusAcknowledged = "acknowledged"
	NotificationStatusDismissed    = "dismissed"
	NotificationStatusExpired      = "expired"
	NotificationStatusFailed       = "failed"
	NotificationStatusCancelled    = "cancelled"

	NotificationDeliveryTargetActorInbox  = "actor_inbox"
	NotificationDeliveryTargetMainOutbox  = "main_outbox"
	NotificationDeliveryTargetPolling     = "polling"
	NotificationDeliveryTargetNodeChannel = "node_channel"

	NotificationDeliveryStatusPending      = "pending"
	NotificationDeliveryStatusDelivered    = "delivered"
	NotificationDeliveryStatusAcknowledged = "acknowledged"
	NotificationDeliveryStatusFailed       = "failed"
	NotificationDeliveryStatusCancelled    = "cancelled"
	NotificationDeliveryStatusExpired      = "expired"

	ProgressSourceKindJob               = "job"
	ProgressSourceKindSync              = "sync"
	ProgressSourceKindCapabilityCall    = "capability_call"
	ProgressSourceKindRoute             = "route"
	ProgressSourceKindPrivateBackup     = "private_backup"
	ProgressSourceKindWorkflowRun       = "workflow_run"
	ProgressSourceKindScriptRun         = "script_run"
	ProgressSourceKindTransfer          = "transfer"
	ProgressSourceKindIndexWorker       = "index_worker"
	ProgressSourceKindBackupWorker      = "backup_worker"
	ProgressSourceKindConnectorProvider = "connector_provider"

	ProgressStatusPending   = "pending"
	ProgressStatusRunning   = "running"
	ProgressStatusWaiting   = "waiting"
	ProgressStatusBlocked   = "blocked"
	ProgressStatusSucceeded = "succeeded"
	ProgressStatusFailed    = "failed"
	ProgressStatusCancelled = "cancelled"
	ProgressStatusWarning   = "warning"
	ProgressStatusClosed    = "closed"

	ProgressSeverityDebug   = "debug"
	ProgressSeverityInfo    = "info"
	ProgressSeverityNormal  = "normal"
	ProgressSeverityWarning = "warning"
	ProgressSeverityError   = "error"

	LeaseHolderKindActor          = "actor"
	LeaseHolderKindNode           = "node"
	LeaseHolderKindJob            = "job"
	LeaseHolderKindRoute          = "route"
	LeaseHolderKindCapabilityCall = "capability_call"
	LeaseHolderKindProvider       = "provider"
	LeaseHolderKindSystem         = "system"

	LeaseModeRead      = "read"
	LeaseModeWrite     = "write"
	LeaseModeControl   = "control"
	LeaseModeExclusive = "exclusive"
	LeaseModeShared    = "shared"

	LeaseStatusGranted  = "granted"
	LeaseStatusReleased = "released"
	LeaseStatusExpired  = "expired"
	LeaseStatusRevoked  = "revoked"
	LeaseStatusFailed   = "failed"
)

func ValidTopicStatus(value string) bool {
	switch value {
	case TopicStatusActive, TopicStatusClosed, TopicStatusArchived, TopicStatusFailed:
		return true
	default:
		return false
	}
}

func ValidRetentionMode(value string) bool {
	switch value {
	case RetentionModeRetainLatest, RetentionModeRetainBounded, RetentionModeDurableEventOnly:
		return true
	default:
		return false
	}
}

func ValidDeliveryClass(value string) bool {
	switch value {
	case DeliveryClassPolling, DeliveryClassActorInbox, DeliveryClassMainOutbox:
		return true
	default:
		return false
	}
}

func ValidDeliveryMode(value string) bool {
	return ValidDeliveryClass(value)
}

func ValidOrderingMode(value string) bool {
	return value == OrderingModeTopicSequence
}

func ValidDurabilityMode(value string) bool {
	switch value {
	case DurabilityModeRetained, DurabilityModeLatestOnly, DurabilityModeDurableEventOnly:
		return true
	default:
		return false
	}
}

func ValidSubscriptionSourceKind(value string) bool {
	return value == SubscriptionSourceKindTopic
}

func ValidSubscriptionStatus(value string) bool {
	switch value {
	case SubscriptionStatusActive, SubscriptionStatusCancelled, SubscriptionStatusExpired, SubscriptionStatusFailed:
		return true
	default:
		return false
	}
}

func ValidPresenceSubjectKind(value string) bool {
	switch value {
	case PresenceSubjectKindNode, PresenceSubjectKindActor, PresenceSubjectKindProvider, PresenceSubjectKindModuleService, PresenceSubjectKindJobRunner:
		return true
	default:
		return false
	}
}

func ValidPresenceState(value string) bool {
	switch value {
	case PresenceStateOnline, PresenceStateRecentlySeen, PresenceStateOffline, PresenceStateDegraded, PresenceStateUnknown, PresenceStateStale, PresenceStateQuarantined, PresenceStateRevoked:
		return true
	default:
		return false
	}
}

func ValidPresenceSourceKind(value string) bool {
	switch value {
	case PresenceSourceKindHeartbeat, PresenceSourceKindManual, PresenceSourceKindProviderStatus, PresenceSourceKindRunnerStatus, PresenceSourceKindExpiryWorker:
		return true
	default:
		return false
	}
}

func ValidNotificationTargetKind(value string) bool {
	switch value {
	case NotificationTargetKindActor, NotificationTargetKindNode, NotificationTargetKindDevice, NotificationTargetKindSession, NotificationTargetKindModuleService, NotificationTargetKindApprovalQueue:
		return true
	default:
		return false
	}
}

func ValidNotificationSourceKind(value string) bool {
	switch value {
	case NotificationSourceKindPolicy, NotificationSourceKindRoute, NotificationSourceKindCapabilityCall, NotificationSourceKindJob, NotificationSourceKindSync, NotificationSourceKindNode, NotificationSourceKindProvider, NotificationSourceKindModule, NotificationSourceKindSystem:
		return true
	default:
		return false
	}
}

func ValidNotificationCategory(value string) bool {
	switch value {
	case NotificationCategoryApproval, NotificationCategorySecurity, NotificationCategoryJob, NotificationCategoryWorkflow, NotificationCategorySync, NotificationCategoryNode, NotificationCategoryProvider, NotificationCategoryModule, NotificationCategoryProject, NotificationCategoryReminder, NotificationCategoryMessage:
		return true
	default:
		return false
	}
}

func ValidNotificationPriority(value string) bool {
	switch value {
	case NotificationPriorityLow, NotificationPriorityNormal, NotificationPriorityHigh, NotificationPriorityUrgent, NotificationPriorityCritical:
		return true
	default:
		return false
	}
}

func ValidNotificationStatus(value string) bool {
	switch value {
	case NotificationStatusCreated, NotificationStatusRouted, NotificationStatusDelivered, NotificationStatusAcknowledged, NotificationStatusDismissed, NotificationStatusExpired, NotificationStatusFailed, NotificationStatusCancelled:
		return true
	default:
		return false
	}
}

func ValidNotificationDeliveryTargetKind(value string) bool {
	switch value {
	case NotificationDeliveryTargetActorInbox, NotificationDeliveryTargetMainOutbox, NotificationDeliveryTargetPolling, NotificationDeliveryTargetNodeChannel:
		return true
	default:
		return false
	}
}

func ValidNotificationDeliveryStatus(value string) bool {
	switch value {
	case NotificationDeliveryStatusPending, NotificationDeliveryStatusDelivered, NotificationDeliveryStatusAcknowledged, NotificationDeliveryStatusFailed, NotificationDeliveryStatusCancelled, NotificationDeliveryStatusExpired:
		return true
	default:
		return false
	}
}

func ValidProgressSourceKind(value string) bool {
	switch value {
	case ProgressSourceKindJob, ProgressSourceKindSync, ProgressSourceKindCapabilityCall, ProgressSourceKindRoute, ProgressSourceKindPrivateBackup, ProgressSourceKindWorkflowRun, ProgressSourceKindScriptRun, ProgressSourceKindTransfer, ProgressSourceKindIndexWorker, ProgressSourceKindBackupWorker, ProgressSourceKindConnectorProvider:
		return true
	default:
		return false
	}
}

func ValidProgressStatus(value string) bool {
	switch value {
	case ProgressStatusPending, ProgressStatusRunning, ProgressStatusWaiting, ProgressStatusBlocked, ProgressStatusSucceeded, ProgressStatusFailed, ProgressStatusCancelled, ProgressStatusWarning, ProgressStatusClosed:
		return true
	default:
		return false
	}
}

func ValidProgressSeverity(value string) bool {
	switch value {
	case ProgressSeverityDebug, ProgressSeverityInfo, ProgressSeverityNormal, ProgressSeverityWarning, ProgressSeverityError:
		return true
	default:
		return false
	}
}

func ValidLeaseHolderKind(value string) bool {
	switch value {
	case LeaseHolderKindActor, LeaseHolderKindNode, LeaseHolderKindJob, LeaseHolderKindRoute, LeaseHolderKindCapabilityCall, LeaseHolderKindProvider, LeaseHolderKindSystem:
		return true
	default:
		return false
	}
}

func ValidLeaseMode(value string) bool {
	switch value {
	case LeaseModeRead, LeaseModeWrite, LeaseModeControl, LeaseModeExclusive, LeaseModeShared:
		return true
	default:
		return false
	}
}

func ValidLeaseStatus(value string) bool {
	switch value {
	case LeaseStatusGranted, LeaseStatusReleased, LeaseStatusExpired, LeaseStatusRevoked, LeaseStatusFailed:
		return true
	default:
		return false
	}
}

func ValidateObjectJSON(raw json.RawMessage, name string) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return fmt.Errorf("%s must be valid JSON object: %w", name, err)
	}
	if object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	return nil
}
