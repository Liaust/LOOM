package realtime

import (
	"encoding/json"
	"testing"
)

func TestValidationHelpersAcceptKnownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "topic status", ok: ValidTopicStatus(TopicStatusActive)},
		{name: "retention mode", ok: ValidRetentionMode(RetentionModeRetainBounded)},
		{name: "delivery class", ok: ValidDeliveryClass(DeliveryClassPolling)},
		{name: "delivery mode", ok: ValidDeliveryMode(DeliveryClassMainOutbox)},
		{name: "ordering mode", ok: ValidOrderingMode(OrderingModeTopicSequence)},
		{name: "durability mode", ok: ValidDurabilityMode(DurabilityModeRetained)},
		{name: "subscription source kind", ok: ValidSubscriptionSourceKind(SubscriptionSourceKindTopic)},
		{name: "subscription status", ok: ValidSubscriptionStatus(SubscriptionStatusActive)},
		{name: "presence subject kind", ok: ValidPresenceSubjectKind(PresenceSubjectKindNode)},
		{name: "presence state", ok: ValidPresenceState(PresenceStateOnline)},
		{name: "presence source kind", ok: ValidPresenceSourceKind(PresenceSourceKindHeartbeat)},
		{name: "notification target kind", ok: ValidNotificationTargetKind(NotificationTargetKindActor)},
		{name: "notification source kind", ok: ValidNotificationSourceKind(NotificationSourceKindPolicy)},
		{name: "notification category", ok: ValidNotificationCategory(NotificationCategoryApproval)},
		{name: "notification priority", ok: ValidNotificationPriority(NotificationPriorityUrgent)},
		{name: "notification status", ok: ValidNotificationStatus(NotificationStatusAcknowledged)},
		{name: "notification delivery target kind", ok: ValidNotificationDeliveryTargetKind(NotificationDeliveryTargetPolling)},
		{name: "notification delivery status", ok: ValidNotificationDeliveryStatus(NotificationDeliveryStatusPending)},
		{name: "progress source kind", ok: ValidProgressSourceKind(ProgressSourceKindJob)},
		{name: "progress status", ok: ValidProgressStatus(ProgressStatusRunning)},
		{name: "progress severity", ok: ValidProgressSeverity(ProgressSeverityWarning)},
		{name: "lease holder kind", ok: ValidLeaseHolderKind(LeaseHolderKindActor)},
		{name: "lease mode", ok: ValidLeaseMode(LeaseModeControl)},
		{name: "lease status", ok: ValidLeaseStatus(LeaseStatusGranted)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.ok {
				t.Fatal("validation helper rejected known value")
			}
		})
	}
}

func TestValidationHelpersRejectUnknownValues(t *testing.T) {
	tests := []struct {
		name string
		ok   bool
	}{
		{name: "topic status", ok: ValidTopicStatus("open")},
		{name: "retention mode", ok: ValidRetentionMode("retain_forever")},
		{name: "delivery class", ok: ValidDeliveryClass("websocket")},
		{name: "delivery mode", ok: ValidDeliveryMode("node_channel")},
		{name: "ordering mode", ok: ValidOrderingMode("global_sequence")},
		{name: "durability mode", ok: ValidDurabilityMode("transient")},
		{name: "subscription source kind", ok: ValidSubscriptionSourceKind("wildcard_topic")},
		{name: "subscription status", ok: ValidSubscriptionStatus("paused")},
		{name: "presence subject kind", ok: ValidPresenceSubjectKind("stream")},
		{name: "presence state", ok: ValidPresenceState("trusted")},
		{name: "presence source kind", ok: ValidPresenceSourceKind("websocket")},
		{name: "notification target kind", ok: ValidNotificationTargetKind("push_provider")},
		{name: "notification source kind", ok: ValidNotificationSourceKind("gmail")},
		{name: "notification category", ok: ValidNotificationCategory("automation")},
		{name: "notification priority", ok: ValidNotificationPriority("panic")},
		{name: "notification status", ok: ValidNotificationStatus("seen")},
		{name: "notification delivery target kind", ok: ValidNotificationDeliveryTargetKind("email")},
		{name: "notification delivery status", ok: ValidNotificationDeliveryStatus("routing")},
		{name: "progress source kind", ok: ValidProgressSourceKind("stream_frame")},
		{name: "progress status", ok: ValidProgressStatus("done")},
		{name: "progress severity", ok: ValidProgressSeverity("critical")},
		{name: "lease holder kind", ok: ValidLeaseHolderKind("session")},
		{name: "lease mode", ok: ValidLeaseMode("admin")},
		{name: "lease status", ok: ValidLeaseStatus("renewed")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.ok {
				t.Fatal("validation helper accepted unknown value")
			}
		})
	}
}

func TestValidateObjectJSONAcceptsObjectsAndEmptyInput(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "empty", raw: nil},
		{name: "blank", raw: json.RawMessage("   ")},
		{name: "empty object", raw: json.RawMessage(`{}`)},
		{name: "payload object", raw: json.RawMessage(`{"message":"ok"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateObjectJSON(tt.raw, "payload"); err != nil {
				t.Fatalf("ValidateObjectJSON returned error: %v", err)
			}
		})
	}
}

func TestValidateObjectJSONRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{name: "invalid json", raw: json.RawMessage(`{"message":`)},
		{name: "null", raw: json.RawMessage(`null`)},
		{name: "array", raw: json.RawMessage(`[]`)},
		{name: "string", raw: json.RawMessage(`"message"`)},
		{name: "number", raw: json.RawMessage(`42`)},
		{name: "boolean", raw: json.RawMessage(`true`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateObjectJSON(tt.raw, "payload"); err == nil {
				t.Fatal("ValidateObjectJSON accepted invalid shape")
			}
		})
	}
}
