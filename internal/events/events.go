package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"loom.local/loom/internal/ids"
	"loom.local/loom/internal/requestctx"
)

const (
	TypeSystemBootstrapped            = "system.bootstrapped"
	TypeScopeCreated                  = "scope.created"
	TypeProjectCreated                = "project.created"
	TypeProjectPolicyCreated          = "project.policy_profile.created"
	TypeProjectWorkspaceViewCreated   = "project.workspace_view.created"
	TypeProjectMemberAdded            = "project.member_added"
	TypeProjectContractRegistered     = "project.contract.registered"
	TypeProjectContractUpdated        = "project.contract.updated"
	TypeProjectBaseActivated          = "project.base_activated"
	TypeProjectFacetActivated         = "project.facet.activated"
	TypeProjectFacetDeactivated       = "project.facet.deactivated"
	TypeProjectArchived               = "project.archived"
	TypeProjectRestored               = "project.restored"
	TypeProjectScriptExposed          = "project.script.exposed"
	TypeProjectScriptExposureStale    = "project.script_exposure.stale"
	TypeProjectScriptExposureBlocked  = "project.script_exposure.blocked"
	TypeObjectIngested                = "object.ingested"
	TypeObjectVersionCreated          = "object.version.created"
	TypeObjectScopeLinked             = "object.scope_linked"
	TypeTextExtracted                 = "text.extracted"
	TypeTextExtractionFailed          = "text.extraction_failed"
	TypeChunksCreated                 = "chunks.created"
	TypeSearchIndexed                 = "search.indexed"
	TypeSearchIndexFailed             = "search.index_failed"
	TypeJobCreated                    = "job.created"
	TypeJobQueued                     = "job.queued"
	TypeJobStarted                    = "job.started"
	TypeJobCompleted                  = "job.completed"
	TypeJobFailed                     = "job.failed"
	TypeJobTimedOut                   = "job.timed_out"
	TypeJobCancelled                  = "job.cancelled"
	TypeJobAttentionAcknowledged      = "job.attention.acknowledged"
	TypeJobAttentionArchived          = "job.attention.archived"
	TypeScriptRegistered              = "script.registered"
	TypeScriptVersionCreated          = "script.version.created"
	TypeScriptRunRequested            = "script.run.requested"
	TypeScriptRunStarted              = "script.run.started"
	TypeScriptRunCompleted            = "script.run.completed"
	TypeScriptRunFailed               = "script.run.failed"
	TypeWorkflowRunRequested          = "workflow.run.requested"
	TypeWorkflowRunStarted            = "workflow.run.started"
	TypeWorkflowRunCompleted          = "workflow.run.completed"
	TypeWorkflowRunFailed             = "workflow.run.failed"
	TypeArtifactCreated               = "artifact.created"
	TypeRunnerRegistered              = "runner.registered"
	TypeRunnerHeartbeat               = "runner.heartbeat"
	TypeProviderRegistered            = "provider.registered"
	TypeProviderHealthUpdated         = "provider.health.updated"
	TypeProviderAdvertisementRecv     = "provider.advertisement.received"
	TypeProviderAdvertisementValid    = "provider.advertisement.validated"
	TypeProviderAdvertisementAppr     = "provider.advertisement.approved"
	TypeProviderAdvertisementRej      = "provider.advertisement.rejected"
	TypeProviderAdvertisementStale    = "provider.advertisement.stale"
	TypePolicyDecisionCreated         = "policy.decision.created"
	TypeApprovalRequested             = "approval.requested"
	TypeApprovalApproved              = "approval.approved"
	TypeApprovalDenied                = "approval.denied"
	TypeApprovalExpired               = "approval.expired"
	TypeGrantIssued                   = "grant.issued"
	TypeGrantRevoked                  = "grant.revoked"
	TypeGrantExpired                  = "grant.expired"
	TypeGrantUsed                     = "grant.used"
	TypeAdminPolicyActionRecorded     = "admin.policy_action.recorded"
	TypeRouteCreated                  = "route.created"
	TypeRouteAuthorized               = "route.authorized"
	TypeRouteWaitingForApproval       = "route.waiting_for_approval"
	TypeRouteDispatched               = "route.dispatched"
	TypeRouteExecuting                = "route.executing"
	TypeRouteCompleted                = "route.completed"
	TypeRouteFailed                   = "route.failed"
	TypeCapabilityCallCreated         = "capability_call.created"
	TypeCapabilityCallAuthorized      = "capability_call.authorized"
	TypeCapabilityCallApprovalReq     = "capability_call.approval_required"
	TypeCapabilityCallDispatched      = "capability_call.dispatched"
	TypeCapabilityCallCompleted       = "capability_call.completed"
	TypeCapabilityCallFailed          = "capability_call.failed"
	TypeNodeEnrollmentTokenCreated    = "node.enrollment_token.created"
	TypeNodeEnrollmentRequested       = "node.enrollment.requested"
	TypeNodeEnrollmentApproved        = "node.enrollment.approved"
	TypeNodeEnrollmentDenied          = "node.enrollment.denied"
	TypeNodeCredentialIssued          = "node.credential.issued"
	TypeNodeCredentialRevoked         = "node.credential.revoked"
	TypeNodeHeartbeatReceived         = "node.heartbeat.received"
	TypeNodePresenceChanged           = "node.presence.changed"
	TypeCommunicationMessageCreated   = "communication.message.created"
	TypeCommunicationMessageClaimed   = "communication.message.claimed"
	TypeCommunicationMessageAcked     = "communication.message.acked"
	TypeCommunicationMessageFailed    = "communication.message.failed"
	TypeBackupPreflightRequested      = "backup.protected_folder.preflight.requested"
	TypeBackupPreflightCompleted      = "backup.protected_folder.preflight.completed"
	TypeBackupDesiredQueued           = "backup.protected_folder.desired.queued"
	TypeBackupNodeApplied             = "backup.protected_folder.node.applied"
	TypeBackupContractEnabled         = "backup.contract.enabled"
	TypeBackupContractDisabled        = "backup.contract.disabled"
	TypeBackupContractDeleted         = "backup.contract.deleted"
	TypeBackupRetryRequested          = "backup.protected_folder.retry.requested"
	TypeNodeLocalTestEvent            = "node.local_test_event"
	TypeSyncPrivateBackupStored       = "sync.private_backup.stored"
	TypeSyncDeletionRequested         = "sync.deletion.requested"
	TypeSyncSourceMetadataObserved    = "sync.source_metadata_observed"
	TypeRealtimeTopicCreated          = "realtime.topic.created"
	TypeRealtimeTopicClosed           = "realtime.topic.closed"
	TypeRealtimePublicationCreated    = "realtime.publication.created"
	TypeRealtimeSubscriptionCreated   = "realtime.subscription.created"
	TypeRealtimeSubscriptionAcked     = "realtime.subscription.acknowledged"
	TypeRealtimeSubscriptionCancel    = "realtime.subscription.cancelled"
	TypeRealtimePresenceChanged       = "realtime.presence.changed"
	TypeRealtimeNotificationCreated   = "realtime.notification.created"
	TypeRealtimeNotificationRouted    = "realtime.notification.routed"
	TypeRealtimeNotificationAcked     = "realtime.notification.acknowledged"
	TypeRealtimeNotificationDismiss   = "realtime.notification.dismissed"
	TypeRealtimeNotificationExpired   = "realtime.notification.expired"
	TypeRealtimeProgressUpdated       = "realtime.progress.updated"
	TypeRealtimeProgressClosed        = "realtime.progress.closed"
	TypeRealtimeLeaseGranted          = "realtime.lease.granted"
	TypeRealtimeLeaseReleased         = "realtime.lease.released"
	TypeRealtimeLeaseExpired          = "realtime.lease.expired"
	TypeRealtimeLeaseConflict         = "realtime.lease.conflict"
	TypeAgentAccessSessionCreated     = "agent.access_session.created"
	TypeAgentWorkContextCreated       = "agent.work_context.created"
	TypeAgentToolViewCreated          = "agent.tool_view.created"
	TypeAgentToolSearched             = "agent.tool.searched"
	TypeAgentToolInspected            = "agent.tool.inspected"
	TypeAgentToolCalled               = "agent.tool.called"
	TypeAgentWorklogWritten           = "agent.worklog.written"
	TypeModuleRegistered              = "module.registered"
	TypeModuleRegistrationFailed      = "module.registration_failed"
	TypeModuleManifestValidated       = "module.manifest.validated"
	TypeModuleManifestRejected        = "module.manifest.rejected"
	TypeModuleInstallStarted          = "module.install_started"
	TypeModuleInstalled               = "module.installed"
	TypeModuleInstallFailed           = "module.install_failed"
	TypeModuleEnabled                 = "module.enabled"
	TypeModuleDisabled                = "module.disabled"
	TypeModuleCapabilityExposed       = "module.capability_exposed"
	TypeModuleCapabilityDisabled      = "module.capability_disabled"
	TypeModuleBackupExported          = "module.backup_exported"
	TypeWorkerKindRegistered          = "worker.kind.registered"
	TypeWorkerInstanceRegistered      = "worker.instance.registered"
	TypeWorkerInstanceUpdated         = "worker.instance.updated"
	TypeWorkerRunStarted              = "worker.run.started"
	TypeWorkerRunSucceeded            = "worker.run.succeeded"
	TypeWorkerRunFailed               = "worker.run.failed"
	TypeWorkerRunCancelled            = "worker.run.cancelled"
	TypeWorkerLeaseAcquired           = "worker.lease.acquired"
	TypeWorkerLeaseReleased           = "worker.lease.released"
	TypeWorkerLeaseExpired            = "worker.lease.expired"
	TypeWorkerCheckpointUpdated       = "worker.checkpoint.updated"
	TypeWorkerHealthUpdated           = "worker.health.updated"
	TypeWorkerControlRequested        = "worker.control.requested"
	TypeWorkerControlApplied          = "worker.control.applied"
	TypeWorkerControlFailed           = "worker.control.failed"
	TypeMaintenanceOperationStarted   = "maintenance.operation.started"
	TypeMaintenanceOperationSucceeded = "maintenance.operation.succeeded"
	TypeMaintenanceOperationFailed    = "maintenance.operation.failed"
	TypeMaintenanceFindingOpened      = "maintenance.finding.opened"
	TypeMaintenanceFindingResolved    = "maintenance.finding.resolved"
	TypeAutomationCreated             = "automation.created"
	TypeAutomationUpdated             = "automation.updated"
	TypeAutomationPaused              = "automation.paused"
	TypeAutomationResumed             = "automation.resumed"
	TypeAutomationDisabled            = "automation.disabled"
	TypeScheduleCreated               = "schedule.created"
	TypeScheduleUpdated               = "schedule.updated"
	TypeSchedulePaused                = "schedule.paused"
	TypeScheduleResumed               = "schedule.resumed"
	TypeScheduleDisabled              = "schedule.disabled"
	TypeScheduleFireCreated           = "schedule.fire.created"
	TypeScheduleFireMissed            = "schedule.fire.missed"
	TypeScheduleFireSkipped           = "schedule.fire.skipped"
	TypeScheduleFireInvocationCreated = "schedule.fire.invocation_created"
	TypeScheduleFireCompleted         = "schedule.fire.completed"
	TypeScheduleFireFailed            = "schedule.fire.failed"
	TypeInvocationCreated             = "invocation.created"
	TypeInvocationLeased              = "invocation.leased"
	TypeInvocationCalling             = "invocation.calling"
	TypeInvocationCompleted           = "invocation.completed"
	TypeInvocationFailed              = "invocation.failed"
	TypeInvocationApprovalRequired    = "invocation.approval_required"
	TypeInvocationTimedOut            = "invocation.timed_out"
	TypeIntegrationCreated            = "integration.created"
	TypeIntegrationUpdated            = "integration.updated"
	TypeIntegrationDisabled           = "integration.disabled"
	TypeIntegrationRevoked            = "integration.revoked"
	TypeIntegrationAuthProfileCreated = "integration.auth_profile.created"
	TypeIntegrationAuthProfileRotated = "integration.auth_profile.rotated"
	TypeIntegrationAuthProfileRevoked = "integration.auth_profile.revoked"
	TypeDirectEventEndpointCreated    = "direct_event.endpoint.created"
	TypeDirectEventEndpointUpdated    = "direct_event.endpoint.updated"
	TypeDirectEventEndpointPaused     = "direct_event.endpoint.paused"
	TypeDirectEventEndpointResumed    = "direct_event.endpoint.resumed"
	TypeDirectEventEndpointDisabled   = "direct_event.endpoint.disabled"
	TypeDirectEventReceived           = "direct_event.received"
	TypeDirectEventAuthenticated      = "direct_event.authenticated"
	TypeDirectEventRejected           = "direct_event.rejected"
	TypeDirectEventDuplicateDetected  = "direct_event.duplicate_detected"
	TypeDirectEventMappingPreviewed   = "direct_event.mapping.previewed"
	TypeDirectEventMapped             = "direct_event.mapped"
	TypeDirectEventMappingFailed      = "direct_event.mapping_failed"
	TypeDirectEventInvocationCreated  = "direct_event.invocation_created"
	TypeDirectEventCompleted          = "direct_event.completed"
	TypeDirectEventFailed             = "direct_event.failed"
	TypeDirectEventTimedOut           = "direct_event.timed_out"
)

var allowedTypes = map[string]struct{}{
	TypeSystemBootstrapped:            {},
	"actor.created":                   {},
	"node.created":                    {},
	TypeScopeCreated:                  {},
	TypeProjectCreated:                {},
	TypeProjectPolicyCreated:          {},
	TypeProjectWorkspaceViewCreated:   {},
	TypeProjectMemberAdded:            {},
	TypeProjectContractRegistered:     {},
	TypeProjectContractUpdated:        {},
	TypeProjectBaseActivated:          {},
	TypeProjectFacetActivated:         {},
	TypeProjectFacetDeactivated:       {},
	TypeProjectArchived:               {},
	TypeProjectRestored:               {},
	TypeProjectScriptExposed:          {},
	TypeProjectScriptExposureStale:    {},
	TypeProjectScriptExposureBlocked:  {},
	TypeObjectIngested:                {},
	TypeObjectVersionCreated:          {},
	TypeObjectScopeLinked:             {},
	TypeTextExtracted:                 {},
	TypeTextExtractionFailed:          {},
	TypeChunksCreated:                 {},
	TypeSearchIndexed:                 {},
	TypeSearchIndexFailed:             {},
	TypeJobCreated:                    {},
	TypeJobQueued:                     {},
	TypeJobStarted:                    {},
	TypeJobCompleted:                  {},
	TypeJobFailed:                     {},
	TypeJobTimedOut:                   {},
	TypeJobCancelled:                  {},
	TypeJobAttentionAcknowledged:      {},
	TypeJobAttentionArchived:          {},
	TypeScriptRegistered:              {},
	TypeScriptVersionCreated:          {},
	TypeScriptRunRequested:            {},
	TypeScriptRunStarted:              {},
	TypeScriptRunCompleted:            {},
	TypeScriptRunFailed:               {},
	TypeWorkflowRunRequested:          {},
	TypeWorkflowRunStarted:            {},
	TypeWorkflowRunCompleted:          {},
	TypeWorkflowRunFailed:             {},
	TypeArtifactCreated:               {},
	TypeRunnerRegistered:              {},
	TypeRunnerHeartbeat:               {},
	TypeProviderRegistered:            {},
	TypeProviderHealthUpdated:         {},
	TypeProviderAdvertisementRecv:     {},
	TypeProviderAdvertisementValid:    {},
	TypeProviderAdvertisementAppr:     {},
	TypeProviderAdvertisementRej:      {},
	TypeProviderAdvertisementStale:    {},
	TypePolicyDecisionCreated:         {},
	TypeApprovalRequested:             {},
	TypeApprovalApproved:              {},
	TypeApprovalDenied:                {},
	TypeApprovalExpired:               {},
	TypeGrantIssued:                   {},
	TypeGrantRevoked:                  {},
	TypeGrantExpired:                  {},
	TypeGrantUsed:                     {},
	TypeAdminPolicyActionRecorded:     {},
	TypeRouteCreated:                  {},
	TypeRouteAuthorized:               {},
	TypeRouteWaitingForApproval:       {},
	TypeRouteDispatched:               {},
	TypeRouteExecuting:                {},
	TypeRouteCompleted:                {},
	TypeRouteFailed:                   {},
	TypeCapabilityCallCreated:         {},
	TypeCapabilityCallAuthorized:      {},
	TypeCapabilityCallApprovalReq:     {},
	TypeCapabilityCallDispatched:      {},
	TypeCapabilityCallCompleted:       {},
	TypeCapabilityCallFailed:          {},
	TypeNodeEnrollmentTokenCreated:    {},
	TypeNodeEnrollmentRequested:       {},
	TypeNodeEnrollmentApproved:        {},
	TypeNodeEnrollmentDenied:          {},
	TypeNodeCredentialIssued:          {},
	TypeNodeCredentialRevoked:         {},
	TypeNodeHeartbeatReceived:         {},
	TypeNodePresenceChanged:           {},
	TypeCommunicationMessageCreated:   {},
	TypeCommunicationMessageClaimed:   {},
	TypeCommunicationMessageAcked:     {},
	TypeCommunicationMessageFailed:    {},
	TypeBackupPreflightRequested:      {},
	TypeBackupPreflightCompleted:      {},
	TypeBackupDesiredQueued:           {},
	TypeBackupNodeApplied:             {},
	TypeBackupContractEnabled:         {},
	TypeBackupContractDisabled:        {},
	TypeBackupContractDeleted:         {},
	TypeBackupRetryRequested:          {},
	TypeNodeLocalTestEvent:            {},
	TypeSyncPrivateBackupStored:       {},
	TypeSyncDeletionRequested:         {},
	TypeSyncSourceMetadataObserved:    {},
	TypeRealtimeTopicCreated:          {},
	TypeRealtimeTopicClosed:           {},
	TypeRealtimePublicationCreated:    {},
	TypeRealtimeSubscriptionCreated:   {},
	TypeRealtimeSubscriptionAcked:     {},
	TypeRealtimeSubscriptionCancel:    {},
	TypeRealtimePresenceChanged:       {},
	TypeRealtimeNotificationCreated:   {},
	TypeRealtimeNotificationRouted:    {},
	TypeRealtimeNotificationAcked:     {},
	TypeRealtimeNotificationDismiss:   {},
	TypeRealtimeNotificationExpired:   {},
	TypeRealtimeProgressUpdated:       {},
	TypeRealtimeProgressClosed:        {},
	TypeRealtimeLeaseGranted:          {},
	TypeRealtimeLeaseReleased:         {},
	TypeRealtimeLeaseExpired:          {},
	TypeRealtimeLeaseConflict:         {},
	TypeAgentAccessSessionCreated:     {},
	TypeAgentWorkContextCreated:       {},
	TypeAgentToolViewCreated:          {},
	TypeAgentToolSearched:             {},
	TypeAgentToolInspected:            {},
	TypeAgentToolCalled:               {},
	TypeAgentWorklogWritten:           {},
	TypeModuleRegistered:              {},
	TypeModuleRegistrationFailed:      {},
	TypeModuleManifestValidated:       {},
	TypeModuleManifestRejected:        {},
	TypeModuleInstallStarted:          {},
	TypeModuleInstalled:               {},
	TypeModuleInstallFailed:           {},
	TypeModuleEnabled:                 {},
	TypeModuleDisabled:                {},
	TypeModuleCapabilityExposed:       {},
	TypeModuleCapabilityDisabled:      {},
	TypeModuleBackupExported:          {},
	TypeWorkerKindRegistered:          {},
	TypeWorkerInstanceRegistered:      {},
	TypeWorkerInstanceUpdated:         {},
	TypeWorkerRunStarted:              {},
	TypeWorkerRunSucceeded:            {},
	TypeWorkerRunFailed:               {},
	TypeWorkerRunCancelled:            {},
	TypeWorkerLeaseAcquired:           {},
	TypeWorkerLeaseReleased:           {},
	TypeWorkerLeaseExpired:            {},
	TypeWorkerCheckpointUpdated:       {},
	TypeWorkerHealthUpdated:           {},
	TypeWorkerControlRequested:        {},
	TypeWorkerControlApplied:          {},
	TypeWorkerControlFailed:           {},
	TypeMaintenanceOperationStarted:   {},
	TypeMaintenanceOperationSucceeded: {},
	TypeMaintenanceOperationFailed:    {},
	TypeMaintenanceFindingOpened:      {},
	TypeMaintenanceFindingResolved:    {},
	TypeAutomationCreated:             {},
	TypeAutomationUpdated:             {},
	TypeAutomationPaused:              {},
	TypeAutomationResumed:             {},
	TypeAutomationDisabled:            {},
	TypeScheduleCreated:               {},
	TypeScheduleUpdated:               {},
	TypeSchedulePaused:                {},
	TypeScheduleResumed:               {},
	TypeScheduleDisabled:              {},
	TypeScheduleFireCreated:           {},
	TypeScheduleFireMissed:            {},
	TypeScheduleFireSkipped:           {},
	TypeScheduleFireInvocationCreated: {},
	TypeScheduleFireCompleted:         {},
	TypeScheduleFireFailed:            {},
	TypeInvocationCreated:             {},
	TypeInvocationLeased:              {},
	TypeInvocationCalling:             {},
	TypeInvocationCompleted:           {},
	TypeInvocationFailed:              {},
	TypeInvocationApprovalRequired:    {},
	TypeInvocationTimedOut:            {},
	TypeIntegrationCreated:            {},
	TypeIntegrationUpdated:            {},
	TypeIntegrationDisabled:           {},
	TypeIntegrationRevoked:            {},
	TypeIntegrationAuthProfileCreated: {},
	TypeIntegrationAuthProfileRotated: {},
	TypeIntegrationAuthProfileRevoked: {},
	TypeDirectEventEndpointCreated:    {},
	TypeDirectEventEndpointUpdated:    {},
	TypeDirectEventEndpointPaused:     {},
	TypeDirectEventEndpointResumed:    {},
	TypeDirectEventEndpointDisabled:   {},
	TypeDirectEventReceived:           {},
	TypeDirectEventAuthenticated:      {},
	TypeDirectEventRejected:           {},
	TypeDirectEventDuplicateDetected:  {},
	TypeDirectEventMappingPreviewed:   {},
	TypeDirectEventMapped:             {},
	TypeDirectEventMappingFailed:      {},
	TypeDirectEventInvocationCreated:  {},
	TypeDirectEventCompleted:          {},
	TypeDirectEventFailed:             {},
	TypeDirectEventTimedOut:           {},
}

var allowedLevels = map[string]struct{}{
	"local_debug":   {},
	"node_activity": {},
	"audit":         {},
	"summary":       {},
	"critical":      {},
}

type Event struct {
	EventID         string          `json:"event_id"`
	EventType       string          `json:"event_type"`
	EventLevel      string          `json:"event_level"`
	ActorID         string          `json:"actor_id"`
	OriginNodeID    string          `json:"origin_node_id"`
	ScopeID         *string         `json:"scope_id,omitempty"`
	TargetKind      *string         `json:"target_kind,omitempty"`
	TargetID        *string         `json:"target_id,omitempty"`
	CorrelationID   *string         `json:"correlation_id,omitempty"`
	RouteID         *string         `json:"route_id,omitempty"`
	JobID           *string         `json:"job_id,omitempty"`
	Status          *string         `json:"status,omitempty"`
	Result          *string         `json:"result,omitempty"`
	Payload         json.RawMessage `json:"payload"`
	VisibilityClass string          `json:"visibility_class"`
	CreatedAt       time.Time       `json:"created_at"`
}

type AppendInput struct {
	EventType       string
	EventLevel      string
	Request         requestctx.Context
	ScopeID         string
	TargetKind      string
	TargetID        string
	RouteID         string
	JobID           string
	Status          string
	Result          string
	Payload         any
	VisibilityClass string
}

type ListFilter struct {
	Limit         int
	EventType     string
	ActorRef      string
	NodeRef       string
	ScopeRef      string
	CorrelationID string
	JobRef        string
	TargetKind    string
	TargetID      string
}

type Service struct {
	DB *sql.DB
}

func NewService(db *sql.DB) Service {
	return Service{DB: db}
}

func (s Service) Append(ctx context.Context, input AppendInput) (Event, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, err
	}
	defer tx.Rollback()

	event, err := AppendTx(ctx, tx, input)
	if err != nil {
		return Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s Service) GetEvent(ctx context.Context, ref string) (Event, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Event{}, fmt.Errorf("event ref is required")
	}

	row := s.DB.QueryRowContext(ctx, eventSelectSQL()+` WHERE event_id = $1`, ref)
	return scanEvent(row)
}

func (s Service) ListEvents(ctx context.Context, filter ListFilter) ([]Event, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}

	query := eventSelectSQL() + ` WHERE true`
	args := []any{}
	add := func(condition string, value any) {
		args = append(args, value)
		query += fmt.Sprintf(" AND %s $%d", condition, len(args))
	}

	if strings.TrimSpace(filter.EventType) != "" {
		add("event_type =", strings.TrimSpace(filter.EventType))
	}
	if strings.TrimSpace(filter.ActorRef) != "" {
		add("actor_id =", strings.TrimSpace(filter.ActorRef))
	}
	if strings.TrimSpace(filter.NodeRef) != "" {
		add("origin_node_id =", strings.TrimSpace(filter.NodeRef))
	}
	if strings.TrimSpace(filter.ScopeRef) != "" {
		add("scope_id =", strings.TrimSpace(filter.ScopeRef))
	}
	if strings.TrimSpace(filter.CorrelationID) != "" {
		add("correlation_id =", strings.TrimSpace(filter.CorrelationID))
	}
	if strings.TrimSpace(filter.JobRef) != "" {
		add("job_id =", strings.TrimSpace(filter.JobRef))
	}
	if strings.TrimSpace(filter.TargetKind) != "" {
		add("target_kind =", strings.TrimSpace(filter.TargetKind))
	}
	if strings.TrimSpace(filter.TargetID) != "" {
		add("target_id =", strings.TrimSpace(filter.TargetID))
	}

	args = append(args, filter.Limit)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func AppendTx(ctx context.Context, tx *sql.Tx, input AppendInput) (Event, error) {
	if err := validateAppend(input); err != nil {
		return Event{}, err
	}

	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("marshal event payload: %w", err)
	}
	if string(payload) == "null" {
		payload = []byte(`{}`)
	}

	eventID := ids.NewEventID()
	event := Event{EventID: eventID}
	var scopeID sql.NullString
	var targetKind sql.NullString
	var targetID sql.NullString
	var correlationID sql.NullString
	var routeID sql.NullString
	var jobID sql.NullString
	var status sql.NullString
	var result sql.NullString
	var storedPayload []byte

	err = tx.QueryRowContext(ctx, `
		INSERT INTO events.events (
			event_id, event_type, event_level, actor_id, origin_node_id,
			scope_id, target_kind, target_id, correlation_id, job_id, status, result,
			payload, visibility_class
		)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), nullif($8, ''),
		        nullif($9, ''), nullif($10, ''), nullif($11, ''), nullif($12, ''), $13, $14)
		RETURNING event_id, event_type, event_level, actor_id, origin_node_id,
		          scope_id, target_kind, target_id, correlation_id, route_id, job_id,
		          status, result, payload, visibility_class, created_at
	`,
		eventID,
		input.EventType,
		input.EventLevel,
		input.Request.ActorID,
		input.Request.OriginNodeID,
		input.ScopeID,
		input.TargetKind,
		input.TargetID,
		input.Request.CorrelationID,
		input.JobID,
		input.Status,
		input.Result,
		payload,
		defaultVisibility(input.VisibilityClass),
	).Scan(
		&event.EventID,
		&event.EventType,
		&event.EventLevel,
		&event.ActorID,
		&event.OriginNodeID,
		&scopeID,
		&targetKind,
		&targetID,
		&correlationID,
		&routeID,
		&jobID,
		&status,
		&result,
		&storedPayload,
		&event.VisibilityClass,
		&event.CreatedAt,
	)
	if err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(input.RouteID) != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE events.events
			SET route_id = $2
			WHERE event_id = $1
		`, eventID, strings.TrimSpace(input.RouteID)); err != nil {
			return Event{}, err
		}
		routeID = sql.NullString{String: strings.TrimSpace(input.RouteID), Valid: true}
	}
	event.ScopeID = stringPtr(scopeID)
	event.TargetKind = stringPtr(targetKind)
	event.TargetID = stringPtr(targetID)
	event.CorrelationID = stringPtr(correlationID)
	event.RouteID = stringPtr(routeID)
	event.JobID = stringPtr(jobID)
	event.Status = stringPtr(status)
	event.Result = stringPtr(result)
	event.Payload = json.RawMessage(storedPayload)
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	return event, nil
}

func validateAppend(input AppendInput) error {
	if _, ok := allowedTypes[input.EventType]; !ok {
		return fmt.Errorf("unsupported event type: %s", input.EventType)
	}
	if _, ok := allowedLevels[input.EventLevel]; !ok {
		return fmt.Errorf("unsupported event level: %s", input.EventLevel)
	}
	if strings.TrimSpace(input.Request.ActorID) == "" {
		return fmt.Errorf("event actor_id is required")
	}
	if strings.TrimSpace(input.Request.OriginNodeID) == "" {
		return fmt.Errorf("event origin_node_id is required")
	}
	return nil
}

func defaultVisibility(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "internal"
	}
	return value
}

func eventSelectSQL() string {
	return `
		SELECT event_id, event_type, event_level, actor_id, origin_node_id,
		       scope_id, target_kind, target_id, correlation_id, route_id, job_id,
		       status, result, payload, visibility_class, created_at
		FROM events.events
	`
}

type eventScanner interface {
	Scan(dest ...any) error
}

func scanEvent(scanner eventScanner) (Event, error) {
	var event Event
	var payload []byte
	var scopeID sql.NullString
	var targetKind sql.NullString
	var targetID sql.NullString
	var correlationID sql.NullString
	var routeID sql.NullString
	var jobID sql.NullString
	var status sql.NullString
	var result sql.NullString
	if err := scanner.Scan(
		&event.EventID,
		&event.EventType,
		&event.EventLevel,
		&event.ActorID,
		&event.OriginNodeID,
		&scopeID,
		&targetKind,
		&targetID,
		&correlationID,
		&routeID,
		&jobID,
		&status,
		&result,
		&payload,
		&event.VisibilityClass,
		&event.CreatedAt,
	); err != nil {
		return Event{}, err
	}
	event.ScopeID = stringPtr(scopeID)
	event.TargetKind = stringPtr(targetKind)
	event.TargetID = stringPtr(targetID)
	event.CorrelationID = stringPtr(correlationID)
	event.RouteID = stringPtr(routeID)
	event.JobID = stringPtr(jobID)
	event.Status = stringPtr(status)
	event.Result = stringPtr(result)
	event.Payload = json.RawMessage(payload)
	if len(event.Payload) == 0 {
		event.Payload = json.RawMessage(`{}`)
	}
	return event, nil
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
