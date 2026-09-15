package events

import (
	"testing"

	"loom.local/loom/internal/requestctx"
)

func TestValidateAppendAcceptsSlice3EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeProjectCreated,
		TypeProjectRestored,
		TypeProjectPolicyCreated,
		TypeProjectWorkspaceViewCreated,
		TypeProjectMemberAdded,
		TypeProjectContractRegistered,
		TypeProjectContractUpdated,
		TypeProjectBaseActivated,
		TypeProjectFacetDeactivated,
		TypeObjectIngested,
		TypeObjectVersionCreated,
		TypeObjectScopeLinked,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:  eventType,
				EventLevel: "audit",
				Request:    req,
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsProtectedFolderEventTypes(t *testing.T) {
	req := requestctx.Context{ActorID: "actor_test", OriginNodeID: "node_test"}
	for _, eventType := range []string{TypeBackupPreflightRequested, TypeBackupPreflightCompleted, TypeBackupDesiredQueued, TypeBackupNodeApplied, TypeBackupContractEnabled, TypeBackupContractDisabled, TypeBackupContractDeleted, TypeBackupRetryRequested} {
		if err := validateAppend(AppendInput{EventType: eventType, EventLevel: "audit", Request: req}); err != nil {
			t.Fatalf("event type %s was not registered: %v", eventType, err)
		}
	}
}

func TestValidateAppendAcceptsSlice4EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeTextExtracted,
		TypeTextExtractionFailed,
		TypeChunksCreated,
		TypeSearchIndexed,
		TypeSearchIndexFailed,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:  eventType,
				EventLevel: "audit",
				Request:    req,
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsV03Slice05ProjectScriptEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeProjectFacetActivated,
		TypeProjectScriptExposed,
		TypeProjectScriptExposureStale,
		TypeProjectScriptExposureBlocked,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:  eventType,
				EventLevel: "audit",
				Request:    req,
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice5EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeJobCreated,
		TypeJobQueued,
		TypeJobStarted,
		TypeJobCompleted,
		TypeJobFailed,
		TypeJobTimedOut,
		TypeJobCancelled,
		TypeJobAttentionAcknowledged,
		TypeJobAttentionArchived,
		TypeScriptRegistered,
		TypeScriptVersionCreated,
		TypeScriptRunRequested,
		TypeScriptRunStarted,
		TypeScriptRunCompleted,
		TypeScriptRunFailed,
		TypeWorkflowRunRequested,
		TypeWorkflowRunStarted,
		TypeWorkflowRunCompleted,
		TypeWorkflowRunFailed,
		TypeArtifactCreated,
		TypeRunnerRegistered,
		TypeRunnerHeartbeat,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:  eventType,
				EventLevel: "audit",
				Request:    req,
				JobID:      "job_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice7EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeProviderRegistered,
		TypeProviderHealthUpdated,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:  eventType,
				EventLevel: "audit",
				Request:    req,
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice11ProviderAdvertisementEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeProviderAdvertisementRecv,
		TypeProviderAdvertisementValid,
		TypeProviderAdvertisementAppr,
		TypeProviderAdvertisementRej,
		TypeProviderAdvertisementStale,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "security",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice8EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypePolicyDecisionCreated,
		TypeApprovalRequested,
		TypeApprovalApproved,
		TypeApprovalDenied,
		TypeApprovalExpired,
		TypeGrantIssued,
		TypeGrantRevoked,
		TypeGrantExpired,
		TypeGrantUsed,
		TypeAdminPolicyActionRecorded,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "security",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice9EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeRouteCreated,
		TypeRouteAuthorized,
		TypeRouteWaitingForApproval,
		TypeRouteDispatched,
		TypeRouteExecuting,
		TypeRouteCompleted,
		TypeRouteFailed,
		TypeCapabilityCallCreated,
		TypeCapabilityCallAuthorized,
		TypeCapabilityCallApprovalReq,
		TypeCapabilityCallDispatched,
		TypeCapabilityCallCompleted,
		TypeCapabilityCallFailed,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "security",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice10EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeNodeEnrollmentTokenCreated,
		TypeNodeEnrollmentRequested,
		TypeNodeEnrollmentApproved,
		TypeNodeEnrollmentDenied,
		TypeNodeCredentialIssued,
		TypeNodeCredentialRevoked,
		TypeNodeHeartbeatReceived,
		TypeNodePresenceChanged,
		TypeCommunicationMessageCreated,
		TypeCommunicationMessageClaimed,
		TypeCommunicationMessageAcked,
		TypeCommunicationMessageFailed,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "security",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice12EventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeNodeLocalTestEvent,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "node_activity",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice15RealtimeEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeRealtimeTopicCreated,
		TypeRealtimeTopicClosed,
		TypeRealtimePublicationCreated,
		TypeRealtimeSubscriptionCreated,
		TypeRealtimeSubscriptionAcked,
		TypeRealtimeSubscriptionCancel,
		TypeRealtimePresenceChanged,
		TypeRealtimeNotificationCreated,
		TypeRealtimeNotificationRouted,
		TypeRealtimeNotificationAcked,
		TypeRealtimeNotificationDismiss,
		TypeRealtimeNotificationExpired,
		TypeRealtimeProgressUpdated,
		TypeRealtimeProgressClosed,
		TypeRealtimeLeaseGranted,
		TypeRealtimeLeaseReleased,
		TypeRealtimeLeaseExpired,
		TypeRealtimeLeaseConflict,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "node_activity",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice16AgentVisibilityEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeAgentAccessSessionCreated,
		TypeAgentWorkContextCreated,
		TypeAgentToolViewCreated,
		TypeAgentToolSearched,
		TypeAgentToolInspected,
		TypeAgentToolCalled,
		TypeAgentWorklogWritten,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsSlice17ModuleEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeModuleRegistered,
		TypeModuleRegistrationFailed,
		TypeModuleManifestValidated,
		TypeModuleManifestRejected,
		TypeModuleInstallStarted,
		TypeModuleInstalled,
		TypeModuleInstallFailed,
		TypeModuleEnabled,
		TypeModuleDisabled,
		TypeModuleCapabilityExposed,
		TypeModuleCapabilityDisabled,
		TypeModuleBackupExported,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsV02Slice01WorkerEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeWorkerKindRegistered,
		TypeWorkerInstanceRegistered,
		TypeWorkerInstanceUpdated,
		TypeWorkerRunStarted,
		TypeWorkerRunSucceeded,
		TypeWorkerRunFailed,
		TypeWorkerRunCancelled,
		TypeWorkerLeaseAcquired,
		TypeWorkerLeaseReleased,
		TypeWorkerLeaseExpired,
		TypeWorkerCheckpointUpdated,
		TypeWorkerHealthUpdated,
		TypeWorkerControlRequested,
		TypeWorkerControlApplied,
		TypeWorkerControlFailed,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendAcceptsV02Slice02MaintenanceEventTypes(t *testing.T) {
	req := requestctx.Context{
		ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
	}

	for _, eventType := range []string{
		TypeMaintenanceOperationStarted,
		TypeMaintenanceOperationSucceeded,
		TypeMaintenanceOperationFailed,
		TypeMaintenanceFindingOpened,
		TypeMaintenanceFindingResolved,
	} {
		t.Run(eventType, func(t *testing.T) {
			err := validateAppend(AppendInput{
				EventType:       eventType,
				EventLevel:      "audit",
				Request:         req,
				VisibilityClass: "internal",
			})
			if err != nil {
				t.Fatalf("validateAppend returned error: %v", err)
			}
		})
	}
}

func TestValidateAppendRejectsUnknownType(t *testing.T) {
	err := validateAppend(AppendInput{
		EventType:  "project.unknown",
		EventLevel: "audit",
		Request: requestctx.Context{
			ActorID:      "actor_01ARZ3NDEKTSV4RRFFQ69G5FAV",
			OriginNodeID: "node_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		},
	})
	if err == nil {
		t.Fatal("validateAppend accepted unknown event type")
	}
}
