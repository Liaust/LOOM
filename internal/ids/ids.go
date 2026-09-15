package ids

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

const (
	ActorPrefix                          = "actor"
	ActorNodeAuthorizationPrefix         = "auth"
	NodePrefix                           = "node"
	ScopePrefix                          = "scope"
	EventPrefix                          = "event"
	ProjectPrefix                        = "project"
	ProjectContractRegistrationPrefix    = "project_contract_registration"
	ProjectContractFacetPrefix           = "project_contract_facet"
	ProjectScriptExposurePrefix          = "project_script_exposure"
	ProjectScheduleRegistrationPrefix    = "project_schedule_registration"
	ProjectDirectEventRegistrationPrefix = "project_direct_event_registration"
	ProjectWatchedRootRegistrationPrefix = "project_watched_root_registration"
	ProjectConnectorRegistrationPrefix   = "project_connector_registration"
	ProjectModuleRegistrationPrefix      = "project_module_registration"
	ProjectWorkflowRegistrationPrefix    = "project_workflow_registration"
	ObjectPrefix                         = "object"
	ObjectVersionPrefix                  = "version"
	BlobPrefix                           = "blob"
	ObjectScopeLinkPrefix                = "object_scope_link"
	ObjectLocationPrefix                 = "object_location"
	BoxPrefix                            = "box"
	BoxWatchRootRegistrationPrefix       = "box_watch_root_registration"
	ProjectMembershipPrefix              = "project_membership"
	ProjectPolicyPrefix                  = "project_policy"
	WorkspaceViewPrefix                  = "workspace_view"
	ExtractedTextPrefix                  = "extracted_text"
	DocumentChunkPrefix                  = "document_chunk"
	SearchDocumentPrefix                 = "search_document"
	IndexStatusPrefix                    = "index_status"
	NotesSourceRootPrefix                = "notes_source_root"
	KnowledgeObjectPrefix                = "knowledge_object"
	KnowledgeObjectVersionPrefix         = "knowledge_object_version"
	KnowledgeChunkPrefix                 = "knowledge_chunk"
	KnowledgePipelineStatusPrefix        = "knowledge_pipeline_status"
	KnowledgeObjectLinkPrefix            = "knowledge_object_link"
	KnowledgePipelineRunPrefix           = "knowledge_pipeline_run"
	KnowledgePipelineStageRunPrefix      = "knowledge_pipeline_stage_run"
	KnowledgePipelineStageUnitPrefix     = "knowledge_pipeline_stage_unit"
	KnowledgeDerivedArtifactPrefix       = "knowledge_derived_artifact"
	ScriptPrefix                         = "script"
	ScriptVersionPrefix                  = "script_version"
	WorkflowPrefix                       = "workflow"
	WorkflowVersionPrefix                = "workflow_version"
	RunnerPrefix                         = "runner"
	JobPrefix                            = "job"
	JobAttemptPrefix                     = "job_attempt"
	JobLogPrefix                         = "job_log"
	JobOutputPrefix                      = "job_output"
	ArtifactPrefix                       = "artifact"
	IdempotencyPrefix                    = "idempotency"
	AdminOperationPrefix                 = "admin_operation"
	ProviderPrefix                       = "prov"
	ProviderAdvertisementPrefix          = "provider_advertisement"
	CapabilityClassPrefix                = "cls"
	CapabilityEndpointPrefix             = "endp"
	CapabilityEndpointVersionPrefix      = "endpv"
	CapabilityRuntimeBindingPrefix       = "runtime_binding"
	CapabilityUsageDocumentPrefix        = "udoc"
	PolicyDecisionPrefix                 = "policy_decision"
	ApprovalPrefix                       = "approval"
	GrantPrefix                          = "grant"
	RoutePrefix                          = "route"
	CapabilityCallPrefix                 = "capability_call"
	NodeEnrollmentTokenPrefix            = "node_enrollment_token"
	NodeEnrollmentRequestPrefix          = "node_enrollment_request"
	NodeCredentialPrefix                 = "node_credential"
	NodeHeartbeatPrefix                  = "node_heartbeat"
	CommunicationMessagePrefix           = "communication_message"
	CommunicationAckPrefix               = "communication_ack"
	LocalEventPrefix                     = "local_event"
	LocalOutboxPrefix                    = "local_outbox"
	LocalCursorPrefix                    = "local_cursor"
	LocalConflictPrefix                  = "local_conflict"
	LocalBackupArtifactPrefix            = "local_backup_artifact"
	LocalBackupBatchPrefix               = "local_backup_batch"
	LocalBackupItemPrefix                = "local_backup_item"
	IngestedEventPrefix                  = "ingested_event"
	SyncBatchPrefix                      = "sync_batch"
	SyncBatchItemPrefix                  = "sync_batch_item"
	SyncCursorPrefix                     = "sync_cursor"
	SyncConflictPrefix                   = "sync_conflict"
	ReplicaPrefix                        = "replica"
	PrivateBackupPrefix                  = "private_backup"
	DeletionRequestPrefix                = "deletion_request"
	TopicPrefix                          = "topic"
	TopicPublicationPrefix               = "topic_publication"
	SubscriptionPrefix                   = "subscription"
	PresencePrefix                       = "presence"
	NotificationPrefix                   = "notification"
	NotificationDeliveryPrefix           = "notification_delivery"
	ProgressFeedPrefix                   = "progress_feed"
	ProgressUpdatePrefix                 = "progress_update"
	LeasePrefix                          = "lease"
	AgentAccessSessionPrefix             = "agent_access_session"
	AgentWorkContextPrefix               = "agent_work_context"
	ToolViewPrefix                       = "tool_view"
	ToolViewEntryPrefix                  = "tool_view_entry"
	AgentToolCallPrefix                  = "agent_tool_call"
	WorklogEntryPrefix                   = "worklog_entry"
	ModulePackagePrefix                  = "module_package"
	ModuleVersionPrefix                  = "module_version"
	ModuleRequirementPrefix              = "module_requirement"
	ModuleDeclarationPrefix              = "module_declaration"
	ModuleNamespacePrefix                = "module_namespace"
	ModuleInstallationPrefix             = "module_installation"
	ModuleHealthPrefix                   = "module_health"
	ModuleBackupExportPrefix             = "module_backup_export"
	WorkerInstancePrefix                 = "worker_instance"
	WorkerRunPrefix                      = "worker_run"
	WorkerLeasePrefix                    = "worker_lease"
	WorkerCheckpointPrefix               = "worker_checkpoint"
	WorkerControlPrefix                  = "worker_control"
	WorkerHeartbeatPrefix                = "worker_heartbeat"
	WorkerHealthPrefix                   = "worker_health"
	WorkerResourceCapacityPrefix         = "worker_resource_capacity"
	WorkerResourceLeasePrefix            = "worker_resource_lease"
	MaintenanceOperationPrefix           = "maintenance_operation"
	MaintenanceFindingPrefix             = "maintenance_finding"
	MaintenanceArtifactPrefix            = "maintenance_artifact"
	DatabaseRollupPrefix                 = "database_rollup"
	AutomationPrefix                     = "automation"
	SchedulePrefix                       = "schedule"
	ScheduleFirePrefix                   = "schedule_fire"
	InvocationPrefix                     = "invocation"
	IntegrationPrefix                    = "integration"
	IntegrationAuthProfilePrefix         = "integration_auth_profile"
	DirectEventEndpointPrefix            = "direct_event_endpoint"
	DirectEventPrefix                    = "direct_event"
	WatchedRootPrefix                    = "watched_root"
	WatchedRootFindingPrefix             = "watched_root_finding"
	WatchedRootBackupBatchPrefix         = "watched_root_backup_batch"
	WatchedRootBackupItemPrefix          = "watched_root_backup_item"
	StorageEntryPrefix                   = "storage_entry"
	StorageEntryVersionPrefix            = "storage_entry_version"
	StoragePhysicalRefPrefix             = "storage_physical_ref"
	StorageViewBuildPrefix               = "storage_view_build"
	StorageViewEntryPrefix               = "storage_view_entry"
	StorageArchiveManifestPrefix         = "storage_archive_manifest"
	StorageRetentionEntryPrefix          = "storage_retention_entry"
	StorageTombstonePrefix               = "storage_tombstone"
	StorageFilesystemObservationPrefix   = "storage_filesystem_observation"
	StorageFidelityFindingPrefix         = "storage_fidelity_finding"
	ProjectRuntimeArchivePrefix          = "project_runtime_archive"
	StorageExportRootPrefix              = "storage_export_root"
	StorageExportBuildPrefix             = "storage_export_build"
	FileTransferPrefix                   = "file_transfer"
	FileTransferChunkPrefix              = "file_transfer_chunk"
	BackupPreflightPrefix                = "backup_preflight"
)

func NewActorID() string {
	return newTypedID(ActorPrefix)
}

func NewActorNodeAuthorizationID() string {
	return newTypedID(ActorNodeAuthorizationPrefix)
}

func NewNodeID() string {
	return newTypedID(NodePrefix)
}

func NewScopeID() string {
	return newTypedID(ScopePrefix)
}

func NewEventID() string {
	return newTypedID(EventPrefix)
}

func NewProjectID() string {
	return newTypedID(ProjectPrefix)
}

func NewProjectContractRegistrationID() string {
	return newTypedID(ProjectContractRegistrationPrefix)
}

func NewProjectContractFacetID() string {
	return newTypedID(ProjectContractFacetPrefix)
}

func NewProjectScriptExposureID() string {
	return newTypedID(ProjectScriptExposurePrefix)
}

func NewProjectScheduleRegistrationID() string {
	return newTypedID(ProjectScheduleRegistrationPrefix)
}

func NewProjectDirectEventRegistrationID() string {
	return newTypedID(ProjectDirectEventRegistrationPrefix)
}

func NewProjectWatchedRootRegistrationID() string {
	return newTypedID(ProjectWatchedRootRegistrationPrefix)
}

func NewProjectConnectorRegistrationID() string {
	return newTypedID(ProjectConnectorRegistrationPrefix)
}

func NewProjectModuleRegistrationID() string {
	return newTypedID(ProjectModuleRegistrationPrefix)
}

func NewProjectWorkflowRegistrationID() string {
	return newTypedID(ProjectWorkflowRegistrationPrefix)
}

func NewObjectID() string {
	return newTypedID(ObjectPrefix)
}

func NewObjectVersionID() string {
	return newTypedID(ObjectVersionPrefix)
}

func NewBlobID() string {
	return newTypedID(BlobPrefix)
}

func NewObjectScopeLinkID() string {
	return newTypedID(ObjectScopeLinkPrefix)
}

func NewObjectLocationID() string {
	return newTypedID(ObjectLocationPrefix)
}

func NewBoxID() string {
	return newTypedID(BoxPrefix)
}

func NewBoxWatchRootRegistrationID() string {
	return newTypedID(BoxWatchRootRegistrationPrefix)
}

func NewProjectMembershipID() string {
	return newTypedID(ProjectMembershipPrefix)
}

func NewProjectPolicyID() string {
	return newTypedID(ProjectPolicyPrefix)
}

func NewWorkspaceViewID() string {
	return newTypedID(WorkspaceViewPrefix)
}

func NewExtractedTextID() string {
	return newTypedID(ExtractedTextPrefix)
}

func NewDocumentChunkID() string {
	return newTypedID(DocumentChunkPrefix)
}

func NewSearchDocumentID() string {
	return newTypedID(SearchDocumentPrefix)
}

func NewIndexStatusID() string {
	return newTypedID(IndexStatusPrefix)
}

func NewNotesSourceRootID() string {
	return newTypedID(NotesSourceRootPrefix)
}

func NewKnowledgeObjectID() string {
	return newTypedID(KnowledgeObjectPrefix)
}

func NewKnowledgeObjectVersionID() string {
	return newTypedID(KnowledgeObjectVersionPrefix)
}

func NewKnowledgeChunkID() string {
	return newTypedID(KnowledgeChunkPrefix)
}

func NewKnowledgePipelineStatusID() string {
	return newTypedID(KnowledgePipelineStatusPrefix)
}

func NewKnowledgeObjectLinkID() string {
	return newTypedID(KnowledgeObjectLinkPrefix)
}

func NewKnowledgePipelineRunID() string {
	return newTypedID(KnowledgePipelineRunPrefix)
}

func NewKnowledgePipelineStageRunID() string {
	return newTypedID(KnowledgePipelineStageRunPrefix)
}

func NewKnowledgePipelineStageUnitID() string {
	return newTypedID(KnowledgePipelineStageUnitPrefix)
}

func NewKnowledgeDerivedArtifactID() string {
	return newTypedID(KnowledgeDerivedArtifactPrefix)
}

func NewScriptID() string {
	return newTypedID(ScriptPrefix)
}

func NewScriptVersionID() string {
	return newTypedID(ScriptVersionPrefix)
}

func NewWorkflowID() string {
	return newTypedID(WorkflowPrefix)
}

func NewWorkflowVersionID() string {
	return newTypedID(WorkflowVersionPrefix)
}

func NewRunnerID() string {
	return newTypedID(RunnerPrefix)
}

func NewJobID() string {
	return newTypedID(JobPrefix)
}

func NewJobAttemptID() string {
	return newTypedID(JobAttemptPrefix)
}

func NewJobLogID() string {
	return newTypedID(JobLogPrefix)
}

func NewJobOutputID() string {
	return newTypedID(JobOutputPrefix)
}

func NewArtifactID() string {
	return newTypedID(ArtifactPrefix)
}

func NewIdempotencyID() string {
	return newTypedID(IdempotencyPrefix)
}

func NewAdminOperationID() string {
	return newTypedID(AdminOperationPrefix)
}

func NewProviderID() string {
	return newTypedID(ProviderPrefix)
}

func NewProviderAdvertisementID() string {
	return newTypedID(ProviderAdvertisementPrefix)
}

func NewCapabilityClassID() string {
	return newTypedID(CapabilityClassPrefix)
}

func NewCapabilityEndpointID() string {
	return newTypedID(CapabilityEndpointPrefix)
}

func NewCapabilityEndpointVersionID() string {
	return newTypedID(CapabilityEndpointVersionPrefix)
}

func NewCapabilityRuntimeBindingID() string {
	return newTypedID(CapabilityRuntimeBindingPrefix)
}

func NewCapabilityUsageDocumentID() string {
	return newTypedID(CapabilityUsageDocumentPrefix)
}

func NewPolicyDecisionID() string {
	return newTypedID(PolicyDecisionPrefix)
}

func NewApprovalID() string {
	return newTypedID(ApprovalPrefix)
}

func NewGrantID() string {
	return newTypedID(GrantPrefix)
}

func NewRouteID() string {
	return newTypedID(RoutePrefix)
}

func NewCapabilityCallID() string {
	return newTypedID(CapabilityCallPrefix)
}

func NewNodeEnrollmentTokenID() string {
	return newTypedID(NodeEnrollmentTokenPrefix)
}

func NewNodeEnrollmentRequestID() string {
	return newTypedID(NodeEnrollmentRequestPrefix)
}

func NewNodeCredentialID() string {
	return newTypedID(NodeCredentialPrefix)
}

func NewNodeHeartbeatID() string {
	return newTypedID(NodeHeartbeatPrefix)
}

func NewCommunicationMessageID() string {
	return newTypedID(CommunicationMessagePrefix)
}

func NewCommunicationAckID() string {
	return newTypedID(CommunicationAckPrefix)
}

func NewLocalEventID() string {
	return newTypedID(LocalEventPrefix)
}

func NewLocalOutboxID() string {
	return newTypedID(LocalOutboxPrefix)
}

func NewLocalCursorID() string {
	return newTypedID(LocalCursorPrefix)
}

func NewLocalConflictID() string {
	return newTypedID(LocalConflictPrefix)
}

func NewLocalBackupArtifactID() string {
	return newTypedID(LocalBackupArtifactPrefix)
}

func NewLocalBackupBatchID() string {
	return newTypedID(LocalBackupBatchPrefix)
}

func NewLocalBackupItemID() string {
	return newTypedID(LocalBackupItemPrefix)
}

func NewIngestedEventID() string {
	return newTypedID(IngestedEventPrefix)
}

func NewSyncBatchID() string {
	return newTypedID(SyncBatchPrefix)
}

func NewSyncBatchItemID() string {
	return newTypedID(SyncBatchItemPrefix)
}

func NewSyncCursorID() string {
	return newTypedID(SyncCursorPrefix)
}

func NewSyncConflictID() string {
	return newTypedID(SyncConflictPrefix)
}

func NewReplicaID() string {
	return newTypedID(ReplicaPrefix)
}

func NewPrivateBackupID() string {
	return newTypedID(PrivateBackupPrefix)
}

func NewDeletionRequestID() string {
	return newTypedID(DeletionRequestPrefix)
}

func NewTopicID() string {
	return newTypedID(TopicPrefix)
}

func NewTopicPublicationID() string {
	return newTypedID(TopicPublicationPrefix)
}

func NewSubscriptionID() string {
	return newTypedID(SubscriptionPrefix)
}

func NewPresenceID() string {
	return newTypedID(PresencePrefix)
}

func NewNotificationID() string {
	return newTypedID(NotificationPrefix)
}

func NewNotificationDeliveryID() string {
	return newTypedID(NotificationDeliveryPrefix)
}

func NewProgressFeedID() string {
	return newTypedID(ProgressFeedPrefix)
}

func NewProgressUpdateID() string {
	return newTypedID(ProgressUpdatePrefix)
}

func NewLeaseID() string {
	return newTypedID(LeasePrefix)
}

func NewAgentAccessSessionID() string {
	return newTypedID(AgentAccessSessionPrefix)
}

func NewAgentWorkContextID() string {
	return newTypedID(AgentWorkContextPrefix)
}

func NewToolViewID() string {
	return newTypedID(ToolViewPrefix)
}

func NewToolViewEntryID() string {
	return newTypedID(ToolViewEntryPrefix)
}

func NewAgentToolCallID() string {
	return newTypedID(AgentToolCallPrefix)
}

func NewWorklogEntryID() string {
	return newTypedID(WorklogEntryPrefix)
}

func NewModulePackageID() string {
	return newTypedID(ModulePackagePrefix)
}

func NewModuleVersionID() string {
	return newTypedID(ModuleVersionPrefix)
}

func NewModuleRequirementID() string {
	return newTypedID(ModuleRequirementPrefix)
}

func NewModuleDeclarationID() string {
	return newTypedID(ModuleDeclarationPrefix)
}

func NewModuleNamespaceID() string {
	return newTypedID(ModuleNamespacePrefix)
}

func NewModuleInstallationID() string {
	return newTypedID(ModuleInstallationPrefix)
}

func NewModuleHealthID() string {
	return newTypedID(ModuleHealthPrefix)
}

func NewModuleBackupExportID() string {
	return newTypedID(ModuleBackupExportPrefix)
}

func NewWorkerInstanceID() string {
	return newTypedID(WorkerInstancePrefix)
}

func NewWorkerRunID() string {
	return newTypedID(WorkerRunPrefix)
}

func NewWorkerLeaseID() string {
	return newTypedID(WorkerLeasePrefix)
}

func NewWorkerCheckpointID() string {
	return newTypedID(WorkerCheckpointPrefix)
}

func NewWorkerControlID() string {
	return newTypedID(WorkerControlPrefix)
}

func NewWorkerHeartbeatID() string {
	return newTypedID(WorkerHeartbeatPrefix)
}

func NewWorkerHealthID() string {
	return newTypedID(WorkerHealthPrefix)
}

func NewWorkerResourceCapacityID() string {
	return newTypedID(WorkerResourceCapacityPrefix)
}

func NewWorkerResourceLeaseID() string {
	return newTypedID(WorkerResourceLeasePrefix)
}

func NewMaintenanceOperationID() string {
	return newTypedID(MaintenanceOperationPrefix)
}

func NewMaintenanceFindingID() string {
	return newTypedID(MaintenanceFindingPrefix)
}

func NewMaintenanceArtifactID() string {
	return newTypedID(MaintenanceArtifactPrefix)
}

func NewDatabaseRollupID() string {
	return newTypedID(DatabaseRollupPrefix)
}

func NewAutomationID() string {
	return newTypedID(AutomationPrefix)
}

func NewScheduleID() string {
	return newTypedID(SchedulePrefix)
}

func NewScheduleFireID() string {
	return newTypedID(ScheduleFirePrefix)
}

func NewInvocationID() string {
	return newTypedID(InvocationPrefix)
}

func NewIntegrationID() string {
	return newTypedID(IntegrationPrefix)
}

func NewIntegrationAuthProfileID() string {
	return newTypedID(IntegrationAuthProfilePrefix)
}

func NewDirectEventEndpointID() string {
	return newTypedID(DirectEventEndpointPrefix)
}

func NewDirectEventID() string {
	return newTypedID(DirectEventPrefix)
}

func NewWatchedRootID() string {
	return newTypedID(WatchedRootPrefix)
}

func NewWatchedRootFindingID() string {
	return newTypedID(WatchedRootFindingPrefix)
}

func NewWatchedRootBackupBatchID() string {
	return newTypedID(WatchedRootBackupBatchPrefix)
}

func NewWatchedRootBackupItemID() string {
	return newTypedID(WatchedRootBackupItemPrefix)
}

func NewStorageEntryID() string {
	return newTypedID(StorageEntryPrefix)
}

func NewStorageEntryVersionID() string {
	return newTypedID(StorageEntryVersionPrefix)
}

func NewStoragePhysicalRefID() string {
	return newTypedID(StoragePhysicalRefPrefix)
}

func NewStorageViewBuildID() string {
	return newTypedID(StorageViewBuildPrefix)
}

func NewStorageViewEntryID() string {
	return newTypedID(StorageViewEntryPrefix)
}

func NewStorageArchiveManifestID() string {
	return newTypedID(StorageArchiveManifestPrefix)
}

func NewStorageRetentionEntryID() string {
	return newTypedID(StorageRetentionEntryPrefix)
}

func NewStorageTombstoneID() string {
	return newTypedID(StorageTombstonePrefix)
}

func NewStorageFilesystemObservationID() string {
	return newTypedID(StorageFilesystemObservationPrefix)
}

func NewStorageFidelityFindingID() string {
	return newTypedID(StorageFidelityFindingPrefix)
}

func NewProjectRuntimeArchiveID() string {
	return newTypedID(ProjectRuntimeArchivePrefix)
}

func NewStorageExportRootID() string {
	return newTypedID(StorageExportRootPrefix)
}

func NewStorageExportBuildID() string {
	return newTypedID(StorageExportBuildPrefix)
}

func NewFileTransferID() string {
	return newTypedID(FileTransferPrefix)
}

func NewFileTransferChunkID() string {
	return newTypedID(FileTransferChunkPrefix)
}

func NewBackupPreflightID() string {
	return newTypedID(BackupPreflightPrefix)
}

func Validate(prefix, id string) error {
	expected := prefix + "_"
	if !strings.HasPrefix(id, expected) {
		return fmt.Errorf("id %q does not have expected prefix %q", id, expected)
	}

	raw := strings.TrimPrefix(id, expected)
	if _, err := ulid.ParseStrict(raw); err != nil {
		return fmt.Errorf("id %q does not contain a valid ULID: %w", id, err)
	}

	return nil
}

func newTypedID(prefix string) string {
	return prefix + "_" + ulid.MustNew(ulid.Timestamp(time.Now().UTC()), rand.Reader).String()
}
