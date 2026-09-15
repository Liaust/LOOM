package portal

import (
	"context"

	"loom.local/loom/internal/automation"
	"loom.local/loom/internal/backupcontracts"
	"loom.local/loom/internal/box"
	"loom.local/loom/internal/capabilities"
	"loom.local/loom/internal/cloudstorage"
	"loom.local/loom/internal/filetransfer"
	"loom.local/loom/internal/health"
	"loom.local/loom/internal/jobs"
	"loom.local/loom/internal/knowledge"
	"loom.local/loom/internal/localclient"
	"loom.local/loom/internal/mainstorage"
	"loom.local/loom/internal/maintenance"
	"loom.local/loom/internal/nodes"
	"loom.local/loom/internal/objects"
	"loom.local/loom/internal/projectcontracts"
	"loom.local/loom/internal/projectdoctor"
	"loom.local/loom/internal/projects"
	"loom.local/loom/internal/projectwatch"
	"loom.local/loom/internal/response"
	"loom.local/loom/internal/routing"
	"loom.local/loom/internal/search"
	"loom.local/loom/internal/serviceregistry"
	loomstatus "loom.local/loom/internal/status"
	"loom.local/loom/internal/storagearchive"
	"loom.local/loom/internal/storagecatalog"
	"loom.local/loom/internal/storagedoctor"
	"loom.local/loom/internal/storageretention"
	"loom.local/loom/internal/storageview"
	loomsync "loom.local/loom/internal/sync"
	mainwatchedroots "loom.local/loom/internal/watchedroots"
	"loom.local/loom/internal/workers"
)

type Client interface {
	Health(context.Context, string) (response.Envelope[health.Report], error)
	Status(context.Context, string) (response.Envelope[loomstatus.Report], error)
	CloudStatusLive(context.Context, string, cloudstorage.CloudStatusLiveInput) (response.Envelope[cloudstorage.StatusReport], error)
	ListWorkers(context.Context, string, workers.WorkerFilter) (response.Envelope[[]workers.WorkerListItem], error)
	InspectWorker(context.Context, string, string) (response.Envelope[workers.WorkerDetail], error)
	ListWorkerRuns(context.Context, string, string, workers.RunFilter) (response.Envelope[[]workers.WorkerRun], error)
	MaintenanceStatus(context.Context, string) (response.Envelope[maintenance.Status], error)
	ListMaintenanceFindings(context.Context, string, maintenance.FindingFilter) (response.Envelope[[]maintenance.Finding], error)
	ListAutomations(context.Context, string, automation.AutomationFilter) (response.Envelope[[]automation.Automation], error)
	ListSchedules(context.Context, string, automation.ScheduleFilter) (response.Envelope[[]automation.Schedule], error)
	GetSchedule(context.Context, string, string) (response.Envelope[automation.ScheduleDetail], error)
	FireScheduleNow(context.Context, string, string, automation.FireScheduleInput) (response.Envelope[automation.FireScheduleResult], error)
	PauseSchedule(context.Context, string, string, automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error)
	ResumeSchedule(context.Context, string, string, automation.UpdateScheduleStatusInput) (response.Envelope[automation.ScheduleDetail], error)
	ScheduleStatus(context.Context, string) (response.Envelope[automation.ScheduleStatus], error)
	ListScheduleFires(context.Context, string, automation.ScheduleFireFilter) (response.Envelope[[]automation.ScheduleFire], error)
	GetScheduleFire(context.Context, string, string) (response.Envelope[automation.ScheduleFire], error)
	ListIntegrations(context.Context, string, automation.IntegrationFilter) (response.Envelope[[]automation.Integration], error)
	ListDirectEventEndpoints(context.Context, string, automation.DirectEventEndpointFilter) (response.Envelope[[]automation.DirectEventEndpoint], error)
	GetDirectEventEndpoint(context.Context, string, string) (response.Envelope[automation.DirectEventEndpointDetail], error)
	ListDirectEvents(context.Context, string, automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error)
	GetDirectEvent(context.Context, string, string) (response.Envelope[automation.DirectEventDetail], error)
	GetDirectEventRawPayload(context.Context, string, string) (response.Envelope[automation.DirectEventRawPayload], error)
	DirectEventStatus(context.Context, string) (response.Envelope[automation.DirectEventStatus], error)
	ListInvocations(context.Context, string, automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error)
	GetInvocation(context.Context, string, string) (response.Envelope[automation.Invocation], error)
	ListInvocationFailures(context.Context, string, automation.InvocationFilter) (response.Envelope[[]automation.Invocation], error)
	ListDirectEventFailures(context.Context, string, automation.DirectEventFilter) (response.Envelope[[]automation.DirectEvent], error)
	JobStatus(context.Context, string) (response.Envelope[jobs.QueueSummary], error)
	ListJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error)
	ListQueuedJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error)
	ListFailedJobs(context.Context, string, jobs.ListFilter) (response.Envelope[[]jobs.Job], error)
	GetJob(context.Context, string, string) (response.Envelope[jobs.JobDetail], error)
	GetJobLogs(context.Context, string, string) (response.Envelope[[]jobs.JobLog], error)
	GetJobOutputs(context.Context, string, string) (response.Envelope[[]jobs.JobOutput], error)
	CancelJob(context.Context, string, jobs.CancelJobInput) (response.Envelope[jobs.Job], error)
	RetryJob(context.Context, string, jobs.RetryJobInput) (response.Envelope[jobs.Job], error)
	AcknowledgeJobAttention(context.Context, string, jobs.JobAttentionInput) (response.Envelope[jobs.Job], error)
	ArchiveJobAttention(context.Context, string, jobs.JobAttentionInput) (response.Envelope[jobs.Job], error)
	RunnerStatus(context.Context, string) (response.Envelope[jobs.QueueSummary], error)
	ListRunners(context.Context, string, jobs.RunnerFilter) (response.Envelope[[]jobs.Runner], error)
	GetKnowledgeNotesOverview(context.Context, string, knowledge.NotesOverviewInput) (response.Envelope[knowledge.NotesOverview], error)
	SearchKnowledgeNotes(context.Context, string, knowledge.NotesSearchInput) (response.Envelope[knowledge.NotesSearchResultSet], error)
	GetKnowledgeNotesEmbeddingStatus(context.Context, string) (response.Envelope[knowledge.EmbeddingStatus], error)
	EnableKnowledgeNotesEmbeddings(context.Context, string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error)
	DisableKnowledgeNotesEmbeddings(context.Context, string) (response.Envelope[knowledge.SetEmbeddingsEnabledResult], error)
	ListIndexStatus(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error)
	ListIndexQueue(context.Context, string, search.IndexQueueFilter) (response.Envelope[[]search.IndexStatus], error)
	ListIndexFailures(context.Context, string, search.StatusFilter) (response.Envelope[[]search.IndexStatus], error)
	GetIndexQueueItem(context.Context, string, string) (response.Envelope[search.IndexStatus], error)
	ExplainIndexObject(context.Context, string, search.IndexExplainInput) (response.Envelope[search.IndexExplainResult], error)
	RetryIndexWork(context.Context, string, search.IndexStatusRefInput) (response.Envelope[search.IndexStatus], error)
	RetryFailedIndexWork(context.Context, string, search.IndexRetryFailedInput) (response.Envelope[search.IndexRetrySummary], error)
	RebuildIndexObject(context.Context, string, search.RebuildInput) (response.Envelope[search.IndexResult], error)
	ListNodes(context.Context, string, int) (response.Envelope[[]nodes.Node], error)
	GetNode(context.Context, string, string) (response.Envelope[nodes.Node], error)
	GetNodeHealth(context.Context, string, string) (response.Envelope[nodes.NodeHealth], error)
	ListProtectedFolders(context.Context, string, backupcontracts.ProtectedFolderFilter) (response.Envelope[backupcontracts.ProtectedFolderListResult], error)
	GetProtectedFolder(context.Context, string, string) (response.Envelope[backupcontracts.ProtectedFolderRecord], error)
	CreateBackupContractPreflight(context.Context, string, backupcontracts.PreflightCreateRequest) (response.Envelope[backupcontracts.PreflightRecord], error)
	GetBackupContractPreflight(context.Context, string, string) (response.Envelope[backupcontracts.PreflightRecord], error)
	CreateBackupContract(context.Context, string, backupcontracts.CreateRequest) (response.Envelope[backupcontracts.LifecycleResult], error)
	EnableBackupContract(context.Context, string, string, backupcontracts.EnableRequest) (response.Envelope[backupcontracts.LifecycleResult], error)
	DisableBackupContract(context.Context, string, string, backupcontracts.DisableRequest) (response.Envelope[backupcontracts.LifecycleResult], error)
	DeleteBackupContract(context.Context, string, string, backupcontracts.DeleteRequest) (response.Envelope[backupcontracts.LifecycleResult], error)
	RetryBackupContractActivation(context.Context, string, string, backupcontracts.RetryActivationRequest) (response.Envelope[backupcontracts.ReconcileQueueResult], error)
	RecheckBackupContract(context.Context, string, string) (response.Envelope[backupcontracts.PreflightRecord], error)
	ListWatchedRootStatus(context.Context, string, mainwatchedroots.StatusFilter) (response.Envelope[[]mainwatchedroots.RootStatus], error)
	ListWatchedRootFindings(context.Context, string, mainwatchedroots.FindingFilter) (response.Envelope[[]mainwatchedroots.Finding], error)
	GetWatchedRootBackupStatus(context.Context, string, mainwatchedroots.BackupFilter) (response.Envelope[mainwatchedroots.BackupStatus], error)
	ListWatchedRootBackupBatches(context.Context, string, mainwatchedroots.BackupFilter) (response.Envelope[[]mainwatchedroots.BackupBatch], error)
	ListWatchedRootBackupItems(context.Context, string, mainwatchedroots.BackupItemFilter) (response.Envelope[[]mainwatchedroots.BackupItem], error)
	GetSyncStatus(context.Context, string, loomsync.ListFilter) (response.Envelope[loomsync.SyncStatus], error)
	ListSyncBatches(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncBatch], error)
	ListSyncConflicts(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncConflict], error)
	ListSyncReplicas(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.SyncReplica], error)
	ListPrivateBackups(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.PrivateBackupOperation], error)
	ListDeletionRequests(context.Context, string, loomsync.ListFilter) (response.Envelope[[]loomsync.DeletionRequest], error)
	GetDeletionRequest(context.Context, string, string) (response.Envelope[loomsync.DeletionRequest], error)
	ReviewDeletionRequest(context.Context, string, loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error)
	ApproveDeletionRequest(context.Context, string, loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error)
	DenyDeletionRequest(context.Context, string, loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error)
	CompleteDeletionRequest(context.Context, string, loomsync.DeletionRequestUpdateInput) (response.Envelope[loomsync.DeletionRequest], error)
	ListProviders(context.Context, string, capabilities.ProviderFilter) (response.Envelope[[]capabilities.ProviderListItem], error)
	ListServices(context.Context, string, serviceregistry.ServiceFilter) (response.Envelope[[]serviceregistry.ServiceListItem], error)
	GetService(context.Context, string, string) (response.Envelope[serviceregistry.ServiceInspection], error)
	GetProvider(context.Context, string, string) (response.Envelope[capabilities.ProviderInspection], error)
	GetProviderHealth(context.Context, string, string) (response.Envelope[capabilities.ProviderHealth], error)
	ListProviderAdvertisements(context.Context, string, capabilities.ProviderAdvertisementFilter) (response.Envelope[[]capabilities.ProviderAdvertisement], error)
	GetProviderAdvertisement(context.Context, string, string) (response.Envelope[capabilities.ProviderAdvertisementInspection], error)
	ListCapabilities(context.Context, string, capabilities.CapabilityFilter) (response.Envelope[[]capabilities.CapabilityListItem], error)
	GetCapability(context.Context, string, string) (response.Envelope[capabilities.CapabilityInspection], error)
	GetCapabilityUsageDocs(context.Context, string, string) (response.Envelope[[]capabilities.UsageDocument], error)
	SearchCapabilities(context.Context, string, capabilities.CapabilitySearchInput) (response.Envelope[[]capabilities.CapabilityCandidate], error)
	ListRuntimeBindings(context.Context, string, capabilities.RuntimeBindingFilter) (response.Envelope[[]capabilities.RuntimeBindingInspection], error)
	GetRuntimeBinding(context.Context, string, string) (response.Envelope[capabilities.RuntimeBindingInspection], error)
	CallCapability(context.Context, string, routing.CapabilityCallInput) (response.Envelope[routing.CapabilityCallOutcome], error)
	ListCapabilityCalls(context.Context, string, routing.CapabilityCallFilter) (response.Envelope[[]routing.CapabilityCall], error)
	GetCapabilityCall(context.Context, string, string) (response.Envelope[routing.CapabilityCall], error)
	ListObjects(context.Context, string, objects.ListFilter) (response.Envelope[[]objects.Object], error)
	GetObject(context.Context, string, string) (response.Envelope[objects.ObjectDetail], error)
	ListObjectVersions(context.Context, string, string) (response.Envelope[[]objects.ObjectVersion], error)
	Search(context.Context, string, search.SearchInput) (response.Envelope[search.SearchResultSet], error)
	ListStorageEntries(context.Context, string, storagecatalog.ListFilter) (response.Envelope[[]storagecatalog.Entry], error)
	ListFileTransfers(context.Context, string, filetransfer.ListFilter) (response.Envelope[[]filetransfer.Status], error)
	InspectStorageEntry(context.Context, string, string) (response.Envelope[storagecatalog.EntryDetail], error)
	ResolveStoragePath(context.Context, string, string) (response.Envelope[storageview.ResolveResult], error)
	GetMainDocumentsStatus(context.Context, string) (response.Envelope[mainstorage.Status], error)
	GetStorageFilesystemStatus(context.Context, string) (response.Envelope[storagedoctor.FilesystemStatus], error)
	GetStorageRetentionStatus(context.Context, string) (response.Envelope[storagecatalog.RetentionStatus], error)
	ArchiveStorage(context.Context, string, storagearchive.ArchiveInput) (response.Envelope[storagearchive.ArchiveResult], error)
	CheckStorageSafeToDelete(context.Context, string, storageretention.SafeToDeleteInput) (response.Envelope[storageretention.SafeToDeleteResult], error)
	FetchStorage(context.Context, string, storageretention.FetchInput) (response.Envelope[storageretention.FetchResult], error)
	RestoreStorage(context.Context, string, storageretention.RestoreInput) (response.Envelope[storageretention.RestoreResult], error)
	ListProjects(context.Context, string, int) (response.Envelope[[]projects.Project], error)
	GetProject(context.Context, string, string) (response.Envelope[projects.ProjectDetail], error)
	ScaffoldProject(context.Context, string, projectcontracts.ScaffoldOptions) (response.Envelope[projectcontracts.ScaffoldResult], error)
	AddProjectFacets(context.Context, string, projectcontracts.AddProjectFacetsOptions) (response.Envelope[projectcontracts.AddProjectFacetsResult], error)
	MigrateProjectLayout(context.Context, string, projectcontracts.LayoutMigrationOptions) (response.Envelope[projectcontracts.LayoutMigrationResult], error)
	AnalyzeProjectContractBackend(context.Context, string, projectcontracts.BackendAnalysisInput) (response.Envelope[projectdoctor.BackendAnalysisResult], error)
	RegisterProjectContractFromBackend(context.Context, string, projects.RegisterProjectContractFromBackendInput) (response.Envelope[projects.RegisterProjectContractResult], error)
	GetProjectRegistrationStatus(context.Context, string, string) (response.Envelope[projects.ProjectRegistrationDetail], error)
	ActivateProject(context.Context, string, string, projects.ActivateProjectInput) (response.Envelope[projects.ProjectRegistrationDetail], error)
	DeactivateProject(context.Context, string, string, projects.DeactivateProjectInput) (response.Envelope[projects.ProjectDeactivationResult], error)
	ArchiveProject(context.Context, string, string, storagearchive.ProjectArchiveInput) (response.Envelope[storagearchive.ProjectArchiveResult], error)
	InspectProjectArchive(context.Context, string, string) (response.Envelope[storagearchive.ProjectArchiveInspectResult], error)
	PlanProjectArchiveRestore(context.Context, string, string, storagearchive.ProjectArchiveRestoreInput) (response.Envelope[storagearchive.ProjectArchiveRestorePlan], error)
	BuildProjectWatchPlan(context.Context, string, string, projectwatch.BuildPlanInput) (response.Envelope[projectwatch.ProjectWatchPlan], error)
	GetProjectSyncStatus(context.Context, string, string) (response.Envelope[projectwatch.ProjectSyncStatus], error)
	GetProjectBackupStatus(context.Context, string, string) (response.Envelope[projectwatch.ProjectBackupStatus], error)
	GetProjectRepositoryStatus(context.Context, string, string, localclient.ProjectRepositoryPageOptions) (response.Envelope[localclient.ProjectRepositoryStatusResult], error)
	RunWorkerOnce(context.Context, string, string, workers.RunOnceInput) (response.Envelope[workers.RunOnceResult], error)
	MaintenanceDBStatus(context.Context, string) (response.Envelope[maintenance.DBStatus], error)
	MaintenanceBackupStatus(context.Context, string) (response.Envelope[maintenance.BackupStatus], error)
	ListMaintenanceBackups(context.Context, string, maintenance.OperationFilter) (response.Envelope[[]maintenance.BackupOperation], error)
	RunMaintenanceBackup(context.Context, string, maintenance.BackupRunInput) (response.Envelope[workers.RunOnceResult], error)
	VerifyMaintenanceBackup(context.Context, string, maintenance.BackupVerifyInput) (response.Envelope[maintenance.BackupVerification], error)
	MaintenanceObjectStoreStatus(context.Context, string) (response.Envelope[maintenance.ObjectStoreStatus], error)
	RunMaintenanceObjectStoreScan(context.Context, string, maintenance.ObjectStoreScanInput) (response.Envelope[workers.RunOnceResult], error)
	ApplyBoxWatchPolicy(context.Context, string, box.WatchApplyInput) (response.Envelope[box.WatchApplyResult], error)
	GetBoxWatchStatus(context.Context, string, box.WatchStatusInput) (response.Envelope[box.WatchStatusResult], error)
}
